// Package controller / subscription.go
//
// 用户购买套餐 + 查询订阅 + 取消/退款。
package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errStackLimitExceeded 是事务内业务级错误的 sentinel，避免外部用 .Error() 字符串比较
var errStackLimitExceeded = errors.New("subscription stack limit exceeded")

// errInsufficientBalance 由购买套餐事务内条件 UPDATE 失败抛出（并发竞态）
var errInsufficientBalance = errors.New("insufficient balance at commit (concurrent purchase race)")

// fix CRITICAL Phase 4-codex（第二十四轮）：购买路径金额累加溢出 sentinels
var (
	errPriceOverflow = errors.New("price * qty overflow int64")
	errBonusOverflow = errors.New("bonus accumulator overflow int64")
)

// fix CRITICAL R23+3-C3（codex 第四轮）：事务内重读 package 后的校验失败 sentinel
var (
	errPackageGoneInTx      = errors.New("package vanished during transaction (admin deleted)")
	errPackageDisabledInTx  = errors.New("package disabled during transaction (admin disabled)")
	errPackageNotPublicInTx = errors.New("package not public during transaction (admin made private)")
	errPackageInvariantInTx = errors.New("package invariant violated during transaction (bonus > price)")
	// fix Minor Mi-1（codex 第二十一轮）：BillingPeriodSeconds 上限校验失败专用 sentinel，
	// 不再复用 errPackageInvariantInTx 让 admin 误以为是 bonus/price 问题。
	errPackagePeriodInvalidInTx = errors.New("package billing_period_seconds out of range during transaction")
)

// lockUserForUpdate 跨数据库方言提供 user 行级排他锁。
//
// fix Major（codex 第九轮）：GORM SQLite 驱动会**忽略** clause.Locking{Strength: "UPDATE"}
// 子句（FOR UPDATE 在 SQLite 不存在），所以 PostgreSQL 上有效的行锁在 SQLite 下完全失效，
// 同 user 并发购买/创建 token 不能被串行化（snapshot isolation 让两个事务都读到 count=0
// 后各自 INSERT，busy_timeout 仅延后 UPDATE 而非 SELECT）。
//
// 跨方言策略：
//   - PostgreSQL/MySQL: clause.Locking → 真正的行级排他锁
//   - SQLite: no-op UPDATE 触发 RESERVED 锁——立刻把事务从 reader 升级为 writer，
//     让其他并发事务的"写"操作在 PRAGMA busy_timeout=5000ms 内排队。
//     这等价于 BEGIN IMMEDIATE 的效果（GORM 不直接暴露事务模式）。
//
// 调用方必须在事务内（tx 必须是 *gorm.DB 的事务句柄）。
func lockUserForUpdate(tx *gorm.DB, userID uint) error {
	dialect := tx.Dialector.Name()
	if dialect == "sqlite" {
		// no-op UPDATE 触发 RESERVED → 升级 writer，与其他写事务串行化
		res := tx.Exec("UPDATE users SET updated_at = updated_at WHERE id = ?", userID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("user %d not found", userID)
		}
		return nil
	}
	// PostgreSQL / MySQL：FOR UPDATE 行锁
	var u database.User
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", userID).First(&u).Error
}

// errSubStateMachineMiss 表示事务内条件 UPDATE rowsAffected=0——
// 实际 sub.status 已脱离允许的源状态集合（被并发取消、退款、暂停等）。
// fix Minor（自审第十三轮）：原 sentinel 名为 errSubAlreadyCanceled 仅描述 cancel 场景，
// 但被 AdminRefundSubscription 复用于 paused/refunded 拒绝 → 名字误导后续维护者。
var errSubStateMachineMiss = errors.New("subscription state machine guard rejected: status not in expected set")

type purchasePayload struct {
	PackageID uint `json:"package_id"`
	// Action 当前仅支持 "stack"（叠加购买，与 "" 同义）。"extend"/"new" 状态机未实现，
	// 未来若加入 extend/new 需要：(a) 用 lockUserForUpdate 串行化，(b) 把"extend EndAt"
	// 视为账务变更走审计日志，(c) 处理 "new"（cancel old + insert new）的退款公式。
	// 在那之前传入未识别值直接拒绝，避免静默 fallback 让 API 合约不清晰。
	Action string `json:"action"`
	// Quantity 用 *int：nil = 字段缺省（默认 1）；非 nil 必须 ≥ 1，0 / 负数 400 拒绝。
	// fix MAJOR（codex 第十六轮）：旧版 int 无法区分"用户清空输入框（显式 0）" vs "字段缺省"，
	// 一律 fallback 1 → 用户意图不清。改 *int 后显式 0/-N 返回 ERR_INVALID_QUANTITY。
	Quantity *int `json:"quantity"`
	CouponID uint `json:"coupon_id"` // 0 = 不使用券；> 0 使用指定 UserCoupon
}

// PurchasePackage 用户购买套餐入口（即时分配）
func PurchasePackage(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var payload purchasePayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	// fix MAJOR（codex 第十六轮）：显式 0/-N 必须拒绝，不能静默 fallback 1
	qty := 1
	if payload.Quantity != nil {
		if *payload.Quantity < 1 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_QUANTITY",
				"message": "quantity 必须 ≥ 1"})
		}
		qty = *payload.Quantity
	}
	if qty > 100 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_QUANTITY_TOO_LARGE"})
	}
	payload.Quantity = &qty
	// fix Major（codex 第九轮）：Action/PurchaseWhenOwned 字段在 schema 存在但业务逻辑未实现，
	// 静默 fallback 到 stack 让 API 合约不清晰。明确只接受空 / "stack"，其他报 400 提示前端：
	// extend/new 状态机暂未实现。
	if payload.Action != "" && payload.Action != "stack" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "当前仅支持 action=stack（叠加购买）；extend/new 暂未实现",
			"message_code": "ERR_ACTION_NOT_SUPPORTED",
		})
	}

	var pkg database.Package
	if err := database.DB.First(&pkg, payload.PackageID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_NOT_FOUND"})
	}
	if !pkg.IsEnabled() {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_DISABLED"})
	}
	// admin 仍可通过未来的 admin 代购 endpoint 操作（当前不存在）
	if !pkg.Public {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_NOT_PUBLIC"})
	}

	// 叠加上限的"前置友好检查"——快速失败避免无谓事务开销。
	// 真正的强制约束在事务内（防 TOCTOU）。
	// fix Major（codex 第九轮）：!Stackable 强制单份；与事务内 effectiveMax 同步策略。
	effectiveMax := pkg.MaxActivePerUser
	if !pkg.IsStackable() {
		effectiveMax = 1
	}
	if effectiveMax > 0 {
		// fix MAJOR R23+3-B7（codex 第四轮）：必须排除 end_at <= now 的过期 active 行 ——
		// cron 延迟把过期订阅状态改 expired 之前，这些行仍计入叠加上限，会阻止用户续买。
		// fix Minor Mi-2（codex 第二十一轮）：检查 .Error，DB 故障时不静默 fallback 到 0 让用户通过预检
		var activeCount int64
		if err := database.DB.Model(&database.UserSubscription{}).
			Where("user_id = ? AND package_id = ? AND status = ? AND end_at > ?", user.ID, pkg.ID, "active", time.Now()).
			Count(&activeCount).Error; err != nil {
			log.Printf("[SUB] pre-tx stack count failed user=%d pkg=%d err=%v", user.ID, pkg.ID, err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
		if int(activeCount)+qty > effectiveMax {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      fmt.Sprintf("已达该套餐持有上限 %d 份", effectiveMax),
				"message_code": "ERR_STACK_LIMIT",
			})
		}
	}

	return purchaseAsInstant(c, user, &pkg, qty, payload.CouponID)
}

func purchaseAsInstant(c *fiber.Ctx, user *database.User, pkg *database.Package, qty int, couponID uint) error {
	// SEC-M3 防御深度：消费时再次校验 bonus<=price，防止 admin 直改 DB / 未来新增 endpoint 漏校
	if pkg.BonusBalanceUSD > pkg.PriceAmount {
		log.Printf("[SUB] BLOCKED package %d invariant violated: bonus=%d > price=%d (micro_usd)", pkg.ID, pkg.BonusBalanceUSD, pkg.PriceAmount)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_INVALID_BONUS"})
	}

	// 事务外乐观估价：仅用于"用户余额是否够付原价"的快速友好检查。
	//
	// fix CRITICAL R23+2-C1（codex 全方面 第二轮）：使用券购买时跳过事务外预检 ——
	// 否则免费券用户余额=0 时会被这里 402 拒绝，根本进不了事务内的 lockAndApplyCoupon。
	// 事务内有 quota >= netDeduct 条件 UPDATE 兜底，并发安全 + 真值由 DB 决定。
	//
	// fix CRITICAL Phase 4-codex（第二十四轮）：price * qty / bonus * qty 必须 checked，
	// 防 admin 设极端套餐价 + 大 qty 导致 int64 溢出回绕成负值穿透余额检查。
	if couponID == 0 {
		totalPriceMicro, ok := database.CheckedMulInt64(pkg.PriceAmount, int64(qty))
		if !ok {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PRICE_OVERFLOW"})
		}
		bonusTotalMicro, ok := database.CheckedMulInt64(pkg.BonusBalanceUSD, int64(qty))
		if !ok {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BONUS_OVERFLOW"})
		}
		requiredAtLeastMicro := totalPriceMicro - bonusTotalMicro
		if requiredAtLeastMicro < 0 {
			requiredAtLeastMicro = 0
		}
		if user.Quota < requiredAtLeastMicro {
			return c.Status(402).JSON(fiber.Map{
				"success":      false,
				"message":      "余额不足",
				"message_code": "ERR_INSUFFICIENT_BALANCE",
				"required":     database.MicroToUSD(requiredAtLeastMicro),
				"current":      database.MicroToUSD(user.Quota),
			})
		}
	}

	created := []database.UserSubscription{}
	now := time.Now()
	var snapshot string // 在事务内重新构建（C3）
	var endAt time.Time
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix Major（codex 第七~第九轮）：跨方言串行化保护
		if err := lockUserForUpdate(tx, user.ID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		// fix CRITICAL R23+3-C3（codex 第四轮）：事务内**重新读取** package 并重新校验，
		// 防 admin 并发禁用/改价/改 plan 时用户买到旧 snapshot 旧价格。
		// 同时校验 package 是否仍 enabled / public。
		//
		// fix CRITICAL（codex 第十五轮）：加 SELECT ... FOR UPDATE 行锁，防 admin 在
		// "事务读 → 用户购买 → 事务写"窗口内改价/禁用导致 snapshot 与最终行为不一致。
		// SQLite 没有 FOR UPDATE 但 GORM 会无害降级；MySQL/Postgres 上锁定 package 行直至 commit。
		var freshPkg database.Package
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&freshPkg, pkg.ID).Error; err != nil {
			return errPackageGoneInTx
		}
		if !freshPkg.IsEnabled() {
			return errPackageDisabledInTx
		}
		if !freshPkg.Public {
			return errPackageNotPublicInTx
		}
		if freshPkg.BonusBalanceUSD > freshPkg.PriceAmount {
			return errPackageInvariantInTx
		}
		// fix CRITICAL C4（codex 第二十轮）+ Mi-1（第二十一轮）：事务内必须复检 BillingPeriodSeconds 上限，
		// 防 admin DB 直改超大值后被购买路径放行（time.Duration 整数溢出 → 异常时间戳）。
		// 拆出独立 sentinel 让前端能区分"bonus>price"与"period 越界"。
		if freshPkg.BillingPeriodSeconds <= 0 || freshPkg.BillingPeriodSeconds > MaxBillingPeriodSeconds {
			return errPackagePeriodInvalidInTx
		}
		// 用事务内重新读到的 package 替换本地副本，后续逻辑（perSubPrices / netDeduct）都用最新值
		pkg = &freshPkg
		endAt = now.Add(time.Duration(pkg.BillingPeriodSeconds) * time.Second)

		// fix MAJOR R23+3-B6：事务内构建 snapshot 时校验 plan 完整性
		freshSnapshot, snapErr := buildPackageSnapshotTx(tx, pkg)
		if snapErr != nil {
			return fmt.Errorf("build snapshot: %w", snapErr)
		}
		snapshot = freshSnapshot
		// 强制约束：在事务内（已锁 user 行）重新查 active count
		// fix Major（codex 第九轮）：原仅靠 MaxActivePerUser，没强制 Stackable=false 时只能持有 1 份。
		// 现在 effective 上限 = min(MaxActivePerUser, 1 if !Stackable else MaxActivePerUser)。
		effectiveMax := pkg.MaxActivePerUser
		if !pkg.IsStackable() {
			effectiveMax = 1
		}
		if effectiveMax > 0 {
			// fix MAJOR R23+3-B7：事务内同样排除已过期 active 行（防 TOCTOU）
			var activeCount int64
			if err := tx.Model(&database.UserSubscription{}).
				Where("user_id = ? AND package_id = ? AND status = ? AND end_at > ?", user.ID, pkg.ID, "active", time.Now()).
				Count(&activeCount).Error; err != nil {
				return fmt.Errorf("count active subs: %w", err)
			}
			if int(activeCount)+qty > effectiveMax {
				return errStackLimitExceeded
			}
		}

		// 优惠券：事务内（持锁 user 行）锁定并消费券。
		// 仅当 couponID > 0 时启用券（用户购买时显式选择）。
		// perSubPrices 默认全部按原价（micro_usd） → 应用券后第 1 份按券价
		perSubPricesMicro := make([]int64, qty)
		for i := range perSubPricesMicro {
			perSubPricesMicro[i] = pkg.PriceAmount
		}
		var usedCoupon *database.UserCoupon
		if couponID > 0 {
			coupon, applyErr := lockAndApplyCoupon(tx, user.ID, couponID, pkg)
			if applyErr != nil {
				return applyErr
			}
			usedCoupon = coupon
			perSubPricesMicro[0] = coupon.SnapshotEffectivePrice(pkg.PriceAmount)
		}
		// fix CRITICAL Phase 4-codex（第二十四轮）：sumInt64 累加溢出守护
		totalPriceMicro, sumOK := database.CheckedSumInt64(perSubPricesMicro)
		if !sumOK {
			return errPriceOverflow
		}

		// 原子合并扣款 + bonus，避免分两次 UPDATE
		//
		// fix CRITICAL R23+2-C2（codex 全方面审查 第二轮）：
		// 券价 < bonus 倒贴保护 — 例：$20 套餐 + $5 bonus + 免费券 → netDeduct = 0 - 5 = -5 →
		// 用户买套餐 + 余额 +5 = 净赚 $5。
		// 修复：每份的 effectiveBonus = min(perSubPrices[i], pkg.BonusBalanceUSD)，
		// 即"bonus 不能让单价为负"。聚合所有份得 effectiveTotalBonus。
		// fix MAJOR R23+3-B2（codex 第四轮）：每份 sub 的 effectiveBonus 单独计算并持久化，
		// 让账单事实表完整可对账（不再只看聚合 effectiveTotalBonus）。
		// fix CRITICAL Phase 4-codex（第二十四轮）：bonus 累加 checked
		perSubBonusMicro := make([]int64, qty)
		var effectiveTotalBonusMicro int64
		for i, p := range perSubPricesMicro {
			b := pkg.BonusBalanceUSD
			if b > p {
				b = p // bonus 不能超过该份单价
			}
			perSubBonusMicro[i] = b
			next, addOK := database.CheckedAddInt64(effectiveTotalBonusMicro, b)
			if !addOK {
				return errBonusOverflow
			}
			effectiveTotalBonusMicro = next
		}
		netDeductMicro := totalPriceMicro - effectiveTotalBonusMicro
		if netDeductMicro > 0 {
			// fix CRITICAL：原实现仅做事务外余额检查，事务内无条件 quota - netDeduct，
			// 并发两笔购买请求都通过事务外检查后会让余额变负。
			// 改为条件 UPDATE：WHERE id=? AND quota >= netDeduct，并校验 RowsAffected。
			res := tx.Model(&database.User{}).
				Where("id = ? AND quota >= ?", user.ID, netDeductMicro).
				UpdateColumn("quota", gorm.Expr("quota - ?", netDeductMicro))
			if res.Error != nil {
				return fmt.Errorf("deduct quota: %w", res.Error)
			}
			if res.RowsAffected == 0 {
				// 并发竞态：另一笔购买已扣空余额。返回 sentinel 错误让上层给出 402。
				return errInsufficientBalance
			}
		} else if netDeductMicro < 0 {
			// bonus > price，净增加余额（不存在并发透支风险）
			if err := tx.Model(&database.User{}).
				Where("id = ?", user.ID).
				UpdateColumn("quota", gorm.Expr("quota + ?", -netDeductMicro)).Error; err != nil {
				return fmt.Errorf("apply bonus: %w", err)
			}
		}

		// 取一次基准 maxIdx，事务内单调递增分配
		baseIdx, err := getNextStackIndex(tx, user.ID, pkg.ID)
		if err != nil {
			return fmt.Errorf("compute stack index: %w", err)
		}
		baseMicro := now.UnixMicro()
		for i := 0; i < qty; i++ {
			// fix CRITICAL R23+2-C1：每份 sub 持久化实际成交价（含券折扣，micro_usd）。
			// 退款时按这个字段算 netCost，而不是 snapshot 的原价 → 不再有"用券价买、按原价退"的套利。
			sub := database.UserSubscription{
				UserID:                user.ID,
				PackageID:             pkg.ID,
				PackageSnapshot:       snapshot,
				StartAt:               now,
				EndAt:                 endAt,
				ConsumptionOrder:      baseMicro + int64(i),
				StackIndex:            baseIdx + i,
				Status:                "active",
				PurchasedUnitPriceUSD: perSubPricesMicro[i],
				AppliedBonusUSD:       perSubBonusMicro[i], // R23+3-B2
			}
			// 仅第 1 份 sub 关联到券（券只用一次，便于退款反查）
			if i == 0 && usedCoupon != nil {
				sub.AppliedCouponID = usedCoupon.ID
			}
			if err := tx.Create(&sub).Error; err != nil {
				return fmt.Errorf("create sub: %w", err)
			}
			created = append(created, sub)
		}

		// 账单流水：购买 + bonus（如果有）
		// 重读余额作为 BalanceAfterUSD 锚点
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, user.ID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		// fix MAJOR R23+3-B1（codex 第四轮）：统一 ledger builder 生成时序账单事件序列。
		//
		// 一次购买（qty=N）产出最多 2N 条账单：
		//   - N 条 purchase_sub/addon（每份 1 条；OccurredAt = now + i*µs）
		//   - 至多 N 条 bonus_credit（仅 perSubBonus[i] > 0；OccurredAt = now + (qty+i)*µs）
		// 所有 purchase 严格先于所有 bonus（语义：先扣款后入账 bonus）。
		// BalanceAfterUSD 沿事件序列严格递进，账单回放与最终余额完全对账。
		entryType := database.BillingTypePurchaseSub
		if pkg.ProductType == "addon" {
			entryType = database.BillingTypePurchaseAddon
		}
		// 计算"购买扣款前 + bonus 入账前"的基线余额：freshUser.Quota 是事务内最终值。
		// totalPriceMicro 是上面已 checked 的累加，复用即可（避免重复 sum）。
		balRollingMicro := freshUser.Quota - effectiveTotalBonusMicro + totalPriceMicro
		// 关联券到第 1 份订阅（在写账单前完成，使描述能引用 used coupon）
		if usedCoupon != nil && len(created) > 0 {
			usedCoupon.UsedOnSubID = &created[0].ID
			usedCoupon.UsedSavingUSD = pkg.PriceAmount - perSubPricesMicro[0]
			if err := tx.Save(usedCoupon).Error; err != nil {
				return fmt.Errorf("link coupon to sub: %w", err)
			}
		}
		// purchase 行：N 条
		for i, sub := range created {
			subID := sub.ID
			unitPriceMicro := perSubPricesMicro[i]
			balRollingMicro -= unitPriceMicro
			desc := fmt.Sprintf("购买「%s」#%d", pkg.Name, sub.StackIndex)
			if usedCoupon != nil && i == 0 {
				desc += fmt.Sprintf("（用券「%s」省 $%s）", usedCoupon.SnapshotName, database.FormatMicroUSD(pkg.PriceAmount-unitPriceMicro))
			}
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:           user.ID,
				OccurredAt:       now.Add(time.Duration(i) * time.Microsecond),
				EntryType:        entryType,
				AmountUSD:        -unitPriceMicro,
				BalanceAfterUSD:  balRollingMicro,
				RelatedType:      "subscription",
				RelatedID:        subID,
				Description:      desc,
				CurrencyOriginal: pkg.PriceCurrency,
				AmountOriginal:   -unitPriceMicro,
			}); err != nil {
				return fmt.Errorf("write billing purchase: %w", err)
			}
		}
		// bonus 行：每份单独一条（与 perSubBonus[i] 对齐，便于 sub_id 退款时精确扣减）
		for i, sub := range created {
			bMicro := perSubBonusMicro[i]
			if bMicro <= 0 {
				continue // 该份没 bonus（如免费券购买的第 1 份）
			}
			balRollingMicro += bMicro
			desc := fmt.Sprintf("「%s」#%d 附赠余额", pkg.Name, sub.StackIndex)
			if usedCoupon != nil && i == 0 && bMicro < pkg.BonusBalanceUSD {
				desc += fmt.Sprintf("（券折后封顶 $%s）", database.FormatMicroUSD(bMicro))
			}
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          user.ID,
				OccurredAt:      now.Add(time.Duration(qty+i) * time.Microsecond),
				EntryType:       database.BillingTypeBonusCredit,
				AmountUSD:       bMicro,
				BalanceAfterUSD: balRollingMicro,
				RelatedType:     "subscription",
				RelatedID:       sub.ID,
				Description:     desc,
			}); err != nil {
				return fmt.Errorf("write billing bonus: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errStackLimitExceeded) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      fmt.Sprintf("已达该套餐叠加上限 %d 份（事务内并发检查）", pkg.MaxActivePerUser),
				"message_code": "ERR_STACK_LIMIT",
			})
		}
		if errors.Is(err, errInsufficientBalance) {
			return c.Status(402).JSON(fiber.Map{
				"success":      false,
				"message":      "余额不足（并发请求已用完）",
				"message_code": "ERR_INSUFFICIENT_BALANCE",
			})
		}
		// fix CRITICAL Phase 4-codex（第二十四轮）：金额溢出 → 400 让前端拒绝异常套餐
		if errors.Is(err, errPriceOverflow) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "套餐价格 × 数量超出范围",
				"message_code": "ERR_PRICE_OVERFLOW",
			})
		}
		if errors.Is(err, errBonusOverflow) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "套餐 bonus 累加超出范围",
				"message_code": "ERR_BONUS_OVERFLOW",
			})
		}
		// fix CRITICAL R23+3-C3：事务内重读 package 发现状态变化 → 让前端刷新重试
		switch {
		case errors.Is(err, errPackageGoneInTx):
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_GONE",
				"message": "套餐已被管理员删除，请刷新页面"})
		case errors.Is(err, errPackageDisabledInTx):
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_DISABLED",
				"message": "套餐已被管理员禁用，请刷新页面"})
		case errors.Is(err, errPackageNotPublicInTx):
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_NOT_PUBLIC",
				"message": "套餐已被管理员下架，请刷新页面"})
		case errors.Is(err, errPackageInvariantInTx):
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_INVALID_BONUS",
				"message": "套餐配置异常（bonus > price），请联系客服"})
		case errors.Is(err, errPackagePeriodInvalidInTx):
			// fix Minor Mi-1（codex 第二十一轮）：period 越界专用错误码
			log.Printf("[PURCHASE] BLOCKED package %d invalid billing_period_seconds in fresh tx", pkg.ID)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_INVALID_PERIOD",
				"message": "套餐周期数据损坏，请联系客服"})
		}
		// 不向客户端泄露 GORM 内部 err（含表名/约束名等）
		log.Printf("[SUB] purchase tx failed user=%d pkg=%d err=%v", user.ID, pkg.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}
	proxy.InvalidateUserSubscriptionCache(user.ID)
	proxy.RefreshUserAuth(user.ID) // 扣 quota 后刷新缓存，否则前端余额陈旧
	createPurchaseNotification(user.ID, pkg, len(created))
	return c.JSON(fiber.Map{"success": true, "data": created, "message_code": "SUCCESS_PURCHASED"})
}

// MySubscriptions 查询我的活跃订阅。批量预加载 usage + package name 避免 N+1。
func MySubscriptions(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	var subs []database.UserSubscription
	if err := database.DB.Where("user_id = ?", user.ID).
		Order("consumption_order ASC").Find(&subs).Error; err != nil {
		log.Printf("[SUB] list user=%d failed: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	type subItem struct {
		database.UserSubscription
		Usage       []database.SubscriptionUsage `json:"usage"`
		PackageName string                       `json:"package_name"`
	}
	if len(subs) == 0 {
		return c.JSON(fiber.Map{"success": true, "data": []subItem{}})
	}

	subIDs := make([]uint, 0, len(subs))
	pkgIDs := make([]uint, 0, len(subs))
	pkgIDSet := make(map[uint]bool)
	for _, s := range subs {
		subIDs = append(subIDs, s.ID)
		if !pkgIDSet[s.PackageID] {
			pkgIDs = append(pkgIDs, s.PackageID)
			pkgIDSet[s.PackageID] = true
		}
	}

	// fix Major（自审第十三轮）：原 usage 查询失败仅日志、继续返回空进度条，
	// 用户误判"用量为 0"重复购买。fail-closed：失败立即 500，让前端重试或显示"加载中"。
	var allUsages []database.SubscriptionUsage
	if err := database.DB.Where("subscription_id IN ?", subIDs).Find(&allUsages).Error; err != nil {
		log.Printf("[SUB] usage query failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	usageBySubID := make(map[uint][]database.SubscriptionUsage, len(subs))
	for _, u := range allUsages {
		usageBySubID[u.SubscriptionID] = append(usageBySubID[u.SubscriptionID], u)
	}

	// fix Major（自审第十三轮）：package 查询失败也 fail-closed。
	// 退化展示"未知套餐名"会让用户对自己的订阅产生疑问。
	var pkgs []database.Package
	if err := database.DB.Where("id IN ?", pkgIDs).Find(&pkgs).Error; err != nil {
		log.Printf("[SUB] package name query failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	pkgNameByID := make(map[uint]string, len(pkgs))
	for _, p := range pkgs {
		pkgNameByID[p.ID] = p.Name
	}

	out := make([]subItem, 0, len(subs))
	for _, s := range subs {
		out = append(out, subItem{
			UserSubscription: s,
			Usage:            usageBySubID[s.ID],
			PackageName:      pkgNameByID[s.PackageID],
		})
	}
	return c.JSON(fiber.Map{"success": true, "data": out})
}

// CancelSubscription 用户**取消**订阅。仅标记 status=canceled，不发生任何资金移动。
//
// 业务模型（产品确认）：
//   - 用户端"取消"= 立即停止该订阅消费 quota（订阅引擎下次决策不再命中）
//   - **退款是独立流程**：用户走客服工单（CustomerMessage）提交退款申请，
//     admin 协商金额后调 AdminRefundSubscription 触发实际退款
//
// 历史 bug 修复（用户产品反馈第十轮）：原实现按"剩余时间比例"自动退款，存在两大问题：
//  1. 业务上不符合"协商退款"的运营模型——错把 cancel 等同于退款
//  2. 安全上有套利漏洞——攻击者买月包→1 小时耗尽 quota→取消近全额退款→重复
//
// 现在 cancel 只动状态机，不动钱。所有退款必须经管理员审核（AdminRefundSubscription）。
func CancelSubscription(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var sub database.UserSubscription
	if err := database.DB.First(&sub, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	if sub.UserID != user.ID {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_FORBIDDEN"})
	}
	if sub.Status != "active" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_ACTIVE"})
	}

	now := time.Now()
	// 条件 UPDATE 防并发双取消（虽不再退款，仍要保证状态机原子性）
	res := database.DB.Model(&database.UserSubscription{}).
		Where("id = ? AND status = ?", sub.ID, "active").
		Updates(map[string]any{"status": "canceled", "canceled_at": now})
	if res.Error != nil {
		log.Printf("[SUB] cancel update failed user=%d sub=%d err=%v", user.ID, sub.ID, res.Error)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	if res.RowsAffected == 0 {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_ALREADY_CANCELED"})
	}
	proxy.InvalidateUserSubscriptionCache(user.ID)

	// 通知（仅"已取消"，不提退款金额；用户若想退款应另开工单）
	pkgName := readPackageNameFromSnapshot(sub.PackageSnapshot)
	if pkgName == "" {
		var pkg database.Package
		if database.DB.Select("id, name").First(&pkg, sub.PackageID).Error == nil {
			pkgName = pkg.Name
		}
	}
	title := readSysConfigCached("notif_subscription_canceled_title", "订阅已取消")
	bodyTpl := readSysConfigCached("notif_subscription_canceled_body", "「{package_name}」已取消，将不再消费您的额度。如需退款请联系客服。")
	body := strings.ReplaceAll(bodyTpl, "{package_name}", pkgName)
	dedupKey := fmt.Sprintf("cancel:sub_%d", sub.ID)
	proxy.Dispatch(user.ID, "subscription", "info", title, body,
		proxy.LinkTickets(), "联系客服", "subscription", sub.ID, &dedupKey)

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_CANCELED",
		"message":      "订阅已取消。如需退款，请通过客服工单提交申请",
	})
}

// adminRefundSubscriptionRequest admin 触发订阅退款的请求体
//
// 前端传 USD float（人友好），handler 内转 micro_usd 入业务逻辑。
type adminRefundSubscriptionRequest struct {
	AmountUSD float64 `json:"amount_usd"` // 协商后的退款金额（USD），必须 > 0 且 <= 购买价
	Reason    string  `json:"reason"`     // 退款原因（写入审计）
	//
	// 业务规则（用户 2026-05-10 第三次反馈定稿）：取消/退款都**不**触碰优惠券。
	// admin 视情况想给用户发"补偿券"应**独立**走 AdminGrantCoupon 入口，
	// 不要在退款流程里捆绑——这样审计两边各自清晰，账单 / 券系统解耦。
}

// AdminRefundSubscription POST /api/admin/subscriptions/:id/refund
//
// 业务模型：用户通过工单提交退款申请 → admin 协商金额 → 调本接口执行实际退款。
//
// 状态机：
//   - active / canceled / expired / paused → refunded（最终态）
//   - refunded → 拒绝（已退款终态资金已结算）
//
// 原子性：
//   - 条件 UPDATE 保证 status 只能从允许态转 refunded（防并发双退款）
//   - 同事务内 user.Quota += amount（amount 来自 admin 协商，不再用任何自动公式）
//   - 写 OperationLog 审计
//   - 发通知给用户
func AdminRefundSubscription(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var req adminRefundSubscriptionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	// 数值校验：必须有限正数
	if !isFinite(req.AmountUSD) || req.AmountUSD <= 0 {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "amount_usd 必须为正数（USD）",
			"message_code": "ERR_INVALID_AMOUNT",
		})
	}
	refundAmountMicro, ok := database.USDToMicro(req.AmountUSD)
	if !ok {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "amount_usd 数值非法（NaN/Inf/超范围）",
			"message_code": "ERR_INVALID_AMOUNT",
		})
	}

	var sub database.UserSubscription
	if err := database.DB.First(&sub, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	// fix CRITICAL（grant 改造）：admin 赠送的订阅 net_cost = 0，用户没付钱过，
	// 退款 = 平台白送钱给用户（甚至比购买套利还离谱）。直接拒绝。
	// admin 想"撤回"赠送应该走另外的"撤销赠送"路径（标记 status=canceled，不动 quota）；
	// 当前未实现该路径，所以这里简单地 4xx 拒绝。
	if sub.IsGranted {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "管理员赠送的订阅不能退款（用户未付费），如需停止该订阅请使用『取消赠送』入口",
			"message_code": "ERR_REFUND_GRANTED_SUB",
		})
	}
	// 防超额：退款不能超过用户**净支出**（实际成交价 - 已发放 bonus）
	//
	// fix CRITICAL R23+2-C1（codex 全方面审查 第二轮）：
	// PurchasedUnitPriceUSD 是购买时持久化的实际成交价（含券折扣）。
	//
	// 关键：免费券购买（用户用免费券拿到 sub）→ PurchasedUnitPriceUSD == 0 → netCost == 0
	//       → 退款上限 0 → admin 不能退款（用户没付钱），符合预期。
	//
	// 项目未上线：不再用 snapshot.price_amount 作为兜底——历史快照可能是原价，
	// 让免费券购买被错误退款。统一规则：**只读** sub.PurchasedUnitPriceUSD，
	// 等于 0 表示"实际未付费"（免费券或赠送），不允许退款。
	if sub.PurchasedUnitPriceUSD <= 0 {
		log.Printf("[SUB-REFUND] BLOCKED purchased_unit_price_usd=0 sub=%d user=%d coupon_id=%d (free-coupon purchase or granted subscription)",
			sub.ID, sub.UserID, sub.AppliedCouponID)
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "用户未实际付费（可能用免费券购买或赠送），无可退金额。如需补偿请用『补发优惠券』",
			"message_code": "ERR_REFUND_ZERO_PAID",
		})
	}
	purchasedPriceMicro := sub.PurchasedUnitPriceUSD
	// fix Major（codex 第十五轮）：退款 netCost 必须从 sub.AppliedBonusUSD 读"实际发放给该份的 bonus"，
	// 不能从 snapshot 读 BonusBalanceUSD —— snapshot 是套餐**当时**的 bonus 字段，
	// 而 purchaseAsInstant 实际给用户的是 min(perSubPrice, bonus)（可能因券折扣 < 1 时低于 snapshot.bonus）。
	// 用 snapshot 会让"券价 < snapshot.bonus"的订阅退款上限算高 → 用户白赚差价。
	bonusMicro := sub.AppliedBonusUSD
	netCostMicro := purchasedPriceMicro - bonusMicro
	if netCostMicro < 0 {
		netCostMicro = 0
	}
	// 容差 1000 micro_usd = $0.001 防 admin 客户端浮点输入误差
	if refundAmountMicro > netCostMicro+1000 {
		return c.Status(400).JSON(fiber.Map{
			"success": false,
			"message": fmt.Sprintf("退款金额超过用户净支出 $%s（购买价 $%s - 已赠送 bonus $%s）",
				database.FormatMicroUSD(netCostMicro),
				database.FormatMicroUSD(purchasedPriceMicro),
				database.FormatMicroUSD(bonusMicro)),
			"message_code": "ERR_AMOUNT_EXCEEDS_NET_COST",
		})
	}

	// fix Major M6（claude type-design 第十五轮）：paused 加入可退清单。
	// paused 语义是"已付款但消费暂停"，与 active/canceled 一样代表"用户有未结算钱"，
	// 应允许退款；refunded 是终态资金已结算，仍排除。
	// 状态机：active / canceled / expired / paused 可退；仅 refunded 拒绝
	now := time.Now()
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix MAJOR R23+2-B1（codex 全方面审查）：退款事务必须锁 user 行，与购买事务串行化。
		// 否则退款 + 购买并发可能让 quota 余额状态机错位（同时给同时扣）。
		if err := lockUserForUpdate(tx, sub.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		// fix Major（自审第十一轮）：原 Updates 强制写 canceled_at = now 会覆盖已 canceled 订阅的
		// 原始取消时间，让审计日志里"用户先 cancel 再申请退款"的时序信息丢失。
		// 改为：只更新 status；canceled_at 仅在还为 NULL 时补（直接 active→refunded 路径）。
		// fix MAJOR（codex 第二十轮）：事务内 UPDATE 加 is_granted = false 条件作为防御深度。
		res := tx.Model(&database.UserSubscription{}).
			Where("id = ? AND is_granted = ? AND status IN ?", sub.ID, false, []string{"active", "canceled", "expired", "paused"}).
			Updates(map[string]any{
				"status":      "refunded",
				"canceled_at": gorm.Expr("CASE WHEN canceled_at IS NULL THEN ? ELSE canceled_at END", now),
			})
		if res.Error != nil {
			return fmt.Errorf("update sub status: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return errSubStateMachineMiss // 状态非可退态 / 或 is_granted=true（事务内不可退）
		}
		// 给用户加 quota（micro_usd）
		if err := tx.Model(&database.User{}).Where("id = ?", sub.UserID).
			UpdateColumn("quota", gorm.Expr("quota + ?", refundAmountMicro)).Error; err != nil {
			return fmt.Errorf("refund quota: %w", err)
		}
		// 账单流水：退款入账
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, sub.UserID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		pkgName := readPackageNameFromSnapshot(sub.PackageSnapshot)
		if pkgName == "" {
			pkgName = fmt.Sprintf("套餐#%d", sub.PackageID)
		}
		desc := fmt.Sprintf("退款：「%s」", pkgName)
		if req.Reason != "" {
			desc += " · " + req.Reason
		}
		subID := sub.ID
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:               sub.UserID,
			OccurredAt:           now,
			EntryType:            database.BillingTypeRefundSub,
			AmountUSD:            refundAmountMicro,
			BalanceAfterUSD:      freshUser.Quota,
			RelatedType:          "subscription",
			RelatedID:            sub.ID,
			SourceSubscriptionID: &subID,
			Description:          desc,
		}); err != nil {
			return fmt.Errorf("write billing refund_sub: %w", err)
		}
		// 业务规则（用户 2026-05-10 第三次反馈定稿）：取消/退款**完全不触碰**优惠券。
		// 已用券永远保持 'used'，admin 想补偿用户应独立走 AdminGrantCoupon 端点。
		// 退款审计只记录"原 sub 当时用了哪张券"作为追溯线索，不做任何状态变更。
		auditDetails, _ := json.Marshal(map[string]any{
			"type":              "REFUND_SUBSCRIPTION",
			"sub_id":            sub.ID,
			"amount":            req.AmountUSD,     // USD float（审计展示字段）
			"amount_micro_usd":  refundAmountMicro, // 精确审计（int64）
			"reason":            req.Reason,
			"prev":              sub.Status,
			"package":           sub.PackageID,
			"applied_coupon_id": sub.AppliedCouponID, // 仅信息：该 sub 当时用过哪张券（保持 used，不恢复）
		})
		return LogOperationByTx(tx, op.ID, sub.UserID, "admin", "REFUND_SUBSCRIPTION", c.IP(), string(auditDetails))
	})

	if txErr != nil {
		if errors.Is(txErr, errSubStateMachineMiss) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "订阅状态不可退款（已退款 / 状态已变化，请刷新后重试）",
				"message_code": "ERR_SUB_STATUS_NOT_REFUNDABLE",
			})
		}
		log.Printf("[SUB-REFUND] tx failed admin=%d sub=%d err=%v", op.ID, sub.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	proxy.InvalidateUserSubscriptionCache(sub.UserID)
	proxy.RefreshUserAuth(sub.UserID)

	// fix Major（codex r10 + regression）：本轮重构去掉了退款通知的 Dispatch 调用，
	// 用户被退款但完全无感知。恢复异步通知 — admin 已手动协商，用户必须收到回执。
	pkgName := readPackageNameFromSnapshot(sub.PackageSnapshot)
	if pkgName == "" {
		var pkg database.Package
		if database.DB.Select("id, name").First(&pkg, sub.PackageID).Error == nil {
			pkgName = pkg.Name
		}
	}
	title := readSysConfigCached("notif_refund_title", "退款已到账")
	bodyTpl := readSysConfigCached("notif_refund_body", "「{package_name}」已退款 {amount} {currency}，到账您的余额。")
	body := strings.ReplaceAll(bodyTpl, "{package_name}", pkgName)
	body = strings.ReplaceAll(body, "{amount}", fmt.Sprintf("%.2f", req.AmountUSD))
	body = strings.ReplaceAll(body, "{currency}", "USD")
	dedupKey := fmt.Sprintf("refund:sub_%d", sub.ID)
	proxy.Dispatch(sub.UserID, "refund", "success", title, body,
		proxy.LinkUpgradeMine(), "查看", "subscription", sub.ID, &dedupKey)

	return c.JSON(fiber.Map{
		"success":      true,
		"refund_usd":   req.AmountUSD,
		"message_code": "SUCCESS_REFUNDED",
	})
}

// adminSubItem 是 admin 订阅总览的扁平化行——含用户身份、套餐名、价格、剩余天数、消费率，
// 让 admin 在协商退款时一屏看到所有关键信息，不必去 join 多个表。
//
// fix Major（产品反馈第十一/十二轮）：
//   - 第十一轮：原 AdminListSubscriptions 只返回 raw UserSubscription，admin 必须人肉对照 users 表 + 解 snapshot
//   - 第十二轮：包月套餐**按剩余天数**计算退款是行业惯例。原把消费率 (usage_max_pct) 当主要决策依据是错的——
//     消费率反映 plan 的"周期内最大用量"（如每月 100 万 token），但退款是看"还剩多少天"。
//     现在把"剩余天数 + 建议退款金额"作为主决策字段，消费率仅作辅助参考（识别"已耗尽"边缘场景）。
type adminSubItem struct {
	ID                uint       `json:"id"`
	UserID            uint       `json:"user_id"`
	Username          string     `json:"username"`
	UserPhone         string     `json:"user_phone"` // 已 maskPhone 脱敏
	UserGithubID      string     `json:"user_github_id"`
	PackageID         uint       `json:"package_id"`
	PackageName       string     `json:"package_name"`        // 从 snapshot 提取（套餐改名/删除后仍准确）
	ProductType       string     `json:"product_type"`        // subscription | addon
	PurchasedPriceUSD float64    `json:"purchased_price_usd"` // 从 snapshot 提取购买时价格
	BonusUSD          float64    `json:"bonus_usd"`           // 从 snapshot 提取购买时赠送
	Status            string     `json:"status"`              // active | canceled | expired | refunded | paused
	StartAt           time.Time  `json:"start_at"`
	EndAt             time.Time  `json:"end_at"`
	CanceledAt        *time.Time `json:"canceled_at"`

	// ★★★ 退款决策主字段 ★★★
	// TotalDays / RemainingDays / TimeRemainingPct：基于订阅时间窗口计算
	// SuggestedRefundUSD：按 remaining_days/total_days 比例 × 购买价的建议退款金额，admin 可在此基础上调整
	TotalDays          float64 `json:"total_days"`           // 订阅总天数（EndAt-StartAt）
	RemainingDays      float64 `json:"remaining_days"`       // 剩余天数（EndAt-now，已过期则 0）
	TimeRemainingPct   float64 `json:"time_remaining_pct"`   // 剩余时间百分比（0-100）
	SuggestedRefundUSD float64 `json:"suggested_refund_usd"` // 建议退款金额（按时间比例算）

	// 消费率：辅助信息——识别"已耗尽却时间剩余"的边缘场景，避免按时间退款被套利
	// 用户的 plan 可能配了"每 N 小时/天/周/月"的最大用量限制（rolling window），
	// usage_max_pct 反映这些 plan 中已消耗最多的那个的当前 consumed/limit 比例
	UsageMaxPct      float64 `json:"usage_max_pct"`
	UsageDetailsJSON string  `json:"usage_details_json"` // 各 plan 的 consumed/limit/unit JSON，便于面板展开

	// 赠送相关字段（IsGranted=true 时不可退款；前端用此渲染"赠送"标记 + 禁用退款按钮）
	IsGranted   bool   `json:"is_granted"`
	GrantReason string `json:"grant_reason,omitempty"`

	// fix MAJOR R23+2 第三轮（codex）：AdminListSubscriptions 返回 AppliedCouponID 让 admin
	// 在退款前能看到"这份订阅当时用过哪张券"作为补偿决策辅助。退款本身不触碰券（用户业务规则定稿）。
	AppliedCouponID uint `json:"applied_coupon_id,omitempty"`
}

// AdminListSubscriptions admin 看所有订阅总览。支持分页 + 多维过滤：
//
//	?page=1&page_size=50&user_id=&status=&package_id=&q=（用户名/手机号模糊匹配）
func AdminListSubscriptions(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	q := database.DB.Model(&database.UserSubscription{})
	if v := c.Query("user_id"); v != "" {
		q = q.Where("user_id = ?", v)
	}
	if v := c.Query("package_id"); v != "" {
		q = q.Where("package_id = ?", v)
	}
	if v := c.Query("status"); v != "" {
		switch v {
		case "active", "expired", "canceled", "refunded", "paused":
			q = q.Where("status = ?", v)
		default:
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_STATUS"})
		}
	}
	// 通过用户名/手机号模糊匹配筛选（先查用户 IDs 再 Where IN，跨方言安全）
	// fix Minor（codex + gemini r11）：
	//   1) 限长 64 防"100KB _ 通配符" DoS（codex 攻击场景）
	//   2) 转义 % _ 防 catch-all 慢查询
	//   3) 至少 2 字符（gemini 建议防 q=% / q=_ 全表扫描）
	if qStr := strings.TrimSpace(c.Query("q")); qStr != "" {
		if len(qStr) > 64 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_QUERY_TOO_LONG"})
		}
		// 太短拒绝，避免触发全表 LIKE 扫描
		if len([]rune(qStr)) < 2 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_QUERY_TOO_SHORT", "message": "搜索关键字至少 2 个字符"})
		}
		// 转义 SQL LIKE 元字符 % 和 _，防止 admin 误填或恶意构造 catch-all
		escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(qStr)
		var uids []uint
		like := "%" + escaped + "%"
		// fix Minor（codex 第十六轮）：Pluck 错误必须冒泡，DB 故障时不能让 admin 看到"无结果"
		// 假象（实际是查不到）。
		if err := database.DB.Model(&database.User{}).
			Where(`username LIKE ? ESCAPE '\' OR phone LIKE ? ESCAPE '\' OR github_id LIKE ? ESCAPE '\'`, like, like, like).
			Limit(500).
			Pluck("id", &uids).Error; err != nil {
			log.Printf("[ADMIN-SUBS] user search failed q=%q: %v", qStr, err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
		if len(uids) == 0 {
			return c.JSON(fiber.Map{"success": true, "data": []adminSubItem{}, "meta": fiber.Map{"total": 0, "page": page, "page_size": pageSize}})
		}
		q = q.Where("user_id IN ?", uids)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		log.Printf("[ADMIN-SUBS] count failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	var rows []database.UserSubscription
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		log.Printf("[ADMIN-SUBS] find failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// 批量预加载 users + usages，避免 N+1
	userIDSet := make(map[uint]struct{}, len(rows))
	subIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		userIDSet[r.UserID] = struct{}{}
		subIDs = append(subIDs, r.ID)
	}
	userIDs := make([]uint, 0, len(userIDSet))
	for id := range userIDSet {
		userIDs = append(userIDs, id)
	}
	// fix CRITICAL（自审第十三轮）：原 Find 调用未检 .Error。
	// DB 故障 → users / allUsages 全空 map → admin panel 看到所有 username 空、
	// UsageMaxPct=0、SuggestedRefundUSD=$0 → admin 误以为"用户已用完"批 $0 退款。
	// fail-closed：任一查询失败立即 500，不渲染半成品给 admin 做决策。
	var users []database.User
	if len(userIDs) > 0 {
		if err := database.DB.Select("id, username, phone, github_id").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			log.Printf("[ADMIN-SUBS] users batch load failed: %v", err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
	}
	userByID := make(map[uint]database.User, len(users))
	for _, u := range users {
		userByID[u.ID] = u
	}

	var allUsages []database.SubscriptionUsage
	if len(subIDs) > 0 {
		if err := database.DB.Where("subscription_id IN ?", subIDs).Find(&allUsages).Error; err != nil {
			log.Printf("[ADMIN-SUBS] usages batch load failed: %v", err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
	}
	usagesBySubID := make(map[uint][]database.SubscriptionUsage, len(rows))
	for _, u := range allUsages {
		usagesBySubID[u.SubscriptionID] = append(usagesBySubID[u.SubscriptionID], u)
	}

	now := time.Now()
	out := make([]adminSubItem, 0, len(rows))
	for _, sub := range rows {
		item := adminSubItem{
			ID:              sub.ID,
			UserID:          sub.UserID,
			PackageID:       sub.PackageID,
			Status:          sub.Status,
			StartAt:         sub.StartAt,
			EndAt:           sub.EndAt,
			CanceledAt:      sub.CanceledAt,
			IsGranted:       sub.IsGranted,
			GrantReason:     sub.GrantReason,
			AppliedCouponID: sub.AppliedCouponID, // R23+2 第三轮：让 admin 看到"该 sub 用过哪张券"
		}
		// 计算时间相关字段（退款决策主字段）
		const secsPerDay = 86400.0
		totalSec := sub.EndAt.Sub(sub.StartAt).Seconds()
		if totalSec <= 0 {
			totalSec = 1 // 防 0 除
		}
		item.TotalDays = totalSec / secsPerDay
		// 已 canceled / refunded 的订阅，"剩余"以取消时刻为准；active 才以 now 计
		anchor := now
		if sub.CanceledAt != nil && (sub.Status == "canceled" || sub.Status == "refunded") {
			anchor = *sub.CanceledAt
		}
		remainSec := sub.EndAt.Sub(anchor).Seconds()
		if remainSec < 0 {
			remainSec = 0
		}
		item.RemainingDays = remainSec / secsPerDay
		item.TimeRemainingPct = remainSec / totalSec * 100
		if u, ok := userByID[sub.UserID]; ok {
			item.Username = u.Username
			item.UserPhone = maskPhone(u.Phone)
			item.UserGithubID = u.GithubID
		}
		// 从 snapshot 解出 package_name / price / bonus / product_type / plans
		var snap struct {
			PackageName     string  `json:"package_name"`
			ProductType     string  `json:"product_type"`
			PriceAmount     float64 `json:"price_amount"`
			BonusBalanceUSD float64 `json:"bonus_balance_usd"`
			Plans           []struct {
				ID         uint    `json:"id"`
				Name       string  `json:"name"`
				LimitUnit  string  `json:"limit_unit"`
				LimitValue float64 `json:"limit_value"`
			} `json:"plans"`
		}
		if err := json.Unmarshal([]byte(sub.PackageSnapshot), &snap); err == nil {
			item.PackageName = snap.PackageName
			item.ProductType = snap.ProductType
			// fix CRITICAL R23+2-C1（codex 全方面审查 第二轮）：
			// 优先用 sub.PurchasedUnitPriceUSD（实际成交价含券折扣），snapshot 仅作"展示原价"用。
			// 免费券 sub 的 PurchasedUnitPriceUSD=0 → 建议退款金额 0 → admin 看不到误导值。
			if sub.PurchasedUnitPriceUSD > 0 {
				item.PurchasedPriceUSD = database.MicroToUSD(sub.PurchasedUnitPriceUSD)
			} else {
				// 免费券或赠送 sub：实际未付费，建议退款 0
				item.PurchasedPriceUSD = 0
			}
			// fix Major（codex 第十五轮）：BonusUSD 用 sub.AppliedBonusUSD（实际入账值），
			// snapshot.bonus 是套餐当时的字段而非用户实际拿到的——见 RefundSubscription 同处修复说明。
			item.BonusUSD = database.MicroToUSD(sub.AppliedBonusUSD)
		} else {
			// fix Minor（自审第十三轮）：原 unmarshal 错误完全静默——admin 看到 $0 建议退款
			// 完全无线索为何。加日志让 admin 能从服务器 log 检索到 snapshot 损坏。
			log.Printf("[ADMIN-SUBS] sub %d snapshot unmarshal failed: %v (PackageName/Price/Bonus 退化为零值)", sub.ID, err)
		}
		// 计算消费率：取所有限额 plan 中 consumed/limit 最大的那个
		// fix Major（自审第十三轮）：原顺序在 SuggestedRefundUSD 计算后才填 UsageMaxPct，
		// 导致 addon 分支读到的恒为 0，min(time, quota) 永远等于 time，套利防护失效。
		// 必须先算 UsageMaxPct，再算 suggested。
		usages := usagesBySubID[sub.ID]
		consumedByPlan := make(map[uint]float64, len(usages))
		for _, u := range usages {
			consumedByPlan[u.QuotaPlanID] += u.ConsumedValue
		}
		maxPct := 0.0
		usageDetails := make([]map[string]any, 0, len(snap.Plans))
		for _, p := range snap.Plans {
			d := map[string]any{
				"plan_id":  p.ID,
				"name":     p.Name,
				"unit":     p.LimitUnit,
				"limit":    p.LimitValue,
				"consumed": consumedByPlan[p.ID],
			}
			if p.LimitValue > 0 {
				pct := consumedByPlan[p.ID] / p.LimitValue * 100
				if pct > maxPct {
					maxPct = pct
				}
				d["pct"] = pct
			}
			usageDetails = append(usageDetails, d)
		}
		item.UsageMaxPct = maxPct
		if b, err := json.Marshal(usageDetails); err == nil {
			item.UsageDetailsJSON = string(b)
		}

		// fix CRITICAL（codex + gemini r11 独立印证）：建议退款必须扣除已赠送的 bonus，
		// 否则攻击者可"买含 bonus 套餐 → 立即取消 → 拿全额退款"白嫖 bonus 净赚。
		// 攻击场景：套餐 $10 + bonus $5；用户余额 $10。
		//   购买后：余额 = 10 - 10 + 5 = $5
		//   立即取消 → 时间剩余 100% → 错误公式建议 $10 退款 → 用户拿到 $15（净赚 $5）
		// 正确公式：netCost = price - bonus（用户实际净支出），退款上限 = netCost × ratio
		//
		// fix Major（codex r11）：addon（增量包）不按时间退款——它是 "1000 messages / 7 days"
		// 这种 token 包，用户 1 小时耗尽就该 0 退款。订阅(subscription) 才按时间。
		// 增量包：取 min(剩余时间, 剩余 quota) 比例的更小者，防"耗尽即退"。
		if sub.Status != "refunded" {
			netCost := item.PurchasedPriceUSD - item.BonusUSD
			if netCost < 0 {
				netCost = 0
			}
			if netCost > 0 {
				ratio := item.TimeRemainingPct / 100.0
				if item.ProductType == "addon" {
					// 增量包：取剩余 quota 比例（取所有 limit plan 中"剩余最少"的那个）
					quotaRemainRatio := 1.0 - (item.UsageMaxPct / 100.0)
					if quotaRemainRatio < 0 {
						quotaRemainRatio = 0
					}
					if quotaRemainRatio < ratio {
						ratio = quotaRemainRatio
					}
				}
				suggested := netCost * ratio
				item.SuggestedRefundUSD = float64(int(suggested*100)) / 100
			}
		}
		out = append(out, item)
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    out,
		"meta":    fiber.Map{"total": total, "page": page, "page_size": pageSize},
	})
}

// ─── 工具 ────────────────────────────────────────────────────

// buildPackageSnapshot 把当前 Package + 关联 plans 序列化为 JSON 字符串。
//
// fix MAJOR M22-4 follow-up：原实现固定用 database.DB 查 plans，事务内调用会因 SQLite
// 单连接配置（MaxOpenConns=1）等待自己持有的连接而死锁。改成 buildPackageSnapshotTx
// 接受 db *gorm.DB；事务路径传 tx，事务外路径传 database.DB。
func buildPackageSnapshot(pkg *database.Package) (string, error) {
	return buildPackageSnapshotTx(database.DB, pkg)
}

func buildPackageSnapshotTx(db *gorm.DB, pkg *database.Package) (string, error) {
	type planSnap struct {
		ID                 uint    `json:"id"`
		Name               string  `json:"name"`
		ModelMatch         string  `json:"model_match"`
		LimitUnit          string  `json:"limit_unit"`
		LimitValue         float64 `json:"limit_value"`
		WindowSeconds      int     `json:"window_seconds"`
		WeightFactor       string  `json:"weight_factor"`
		Priority           int     `json:"priority"`
		OverflowStrategy   string  `json:"overflow_strategy"`
		QuantityMultiplier float64 `json:"quantity_multiplier"`
	}
	type snap struct {
		// schema_version 标记当前快照语义；QuantityMultiplier 放大限额。
		// fix MAJOR M22-A1 Phase 1：PriceAmount/BonusBalanceUSD 单位 micro_usd（int64）。
		SchemaVersion        int        `json:"schema_version"`
		PackageID            uint       `json:"package_id"`
		PackageName          string     `json:"package_name"`
		ProductType          string     `json:"product_type"` // subscription | addon（决定消费引擎排序）
		PriceAmount          int64      `json:"price_amount"`
		PriceCurrency        string     `json:"price_currency"`
		BillingPeriodSeconds int        `json:"billing_period_seconds"`
		BonusBalanceUSD      int64      `json:"bonus_balance_usd"`
		Plans                []planSnap `json:"plans"`
	}
	productType := pkg.ProductType
	if productType == "" {
		productType = "subscription" // 防御式默认值
	}
	s := snap{
		SchemaVersion:        database.PackageSnapshotCurrentVersion,
		PackageID:            pkg.ID,
		PackageName:          pkg.Name,
		ProductType:          productType,
		PriceAmount:          pkg.PriceAmount,
		PriceCurrency:        pkg.PriceCurrency,
		BillingPeriodSeconds: pkg.BillingPeriodSeconds,
		BonusBalanceUSD:      pkg.BonusBalanceUSD,
	}
	var pps []database.PackagePlan
	if err := db.Where("package_id = ?", pkg.ID).Order("sort_order asc").Find(&pps).Error; err != nil {
		return "", fmt.Errorf("load package_plans pkg=%d: %w", pkg.ID, err)
	}
	if len(pps) == 0 {
		b, err := json.Marshal(s)
		return string(b), err
	}
	planIDs := make([]uint, 0, len(pps))
	for _, pp := range pps {
		planIDs = append(planIDs, pp.QuotaPlanID)
	}
	var plans []database.QuotaPlan
	if err := db.Where("id IN ?", planIDs).Find(&plans).Error; err != nil {
		return "", fmt.Errorf("load quota_plans pkg=%d: %w", pkg.ID, err)
	}
	planMap := make(map[uint]database.QuotaPlan, len(plans))
	for _, p := range plans {
		planMap[p.ID] = p
	}
	// fix MAJOR R23+3-B6（codex 第四轮）：所有绑定的 plan 必须 enabled，否则 fail-closed
	// 防 admin 绑了 disabled plan → 用户购买后引擎走 no_plans → fallback 余额扣费的灰色路径
	missing := make([]uint, 0)
	for _, pp := range pps {
		plan, ok := planMap[pp.QuotaPlanID]
		if !ok {
			missing = append(missing, pp.QuotaPlanID)
			continue
		}
		if !plan.IsEnabled() {
			missing = append(missing, pp.QuotaPlanID)
			continue
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("package %d has invalid plan_ids %v (missing or disabled)", pkg.ID, missing)
	}
	for _, pp := range pps {
		plan, ok := planMap[pp.QuotaPlanID]
		if !ok {
			continue // 已在上面阻止；防御性
		}
		s.Plans = append(s.Plans, planSnap{
			ID: plan.ID, Name: plan.Name, ModelMatch: plan.ModelMatch,
			LimitUnit: plan.LimitUnit, LimitValue: plan.LimitValue,
			WindowSeconds: plan.WindowSeconds, WeightFactor: plan.WeightFactor,
			Priority: plan.Priority, OverflowStrategy: plan.OverflowStrategy,
			QuantityMultiplier: pp.QuantityMultiplier,
		})
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// readPackageNameFromSnapshot 从订阅快照里读购买时套餐名（用于通知正文）。
//
// fix Minor（codex 第四轮）：原代码读字段 "name"，但 buildPackageSnapshot 写的是 "package_name"，
// 导致取消订阅退款通知拿到的永远是空字符串，fallback 当前 pkg.Name（套餐改名后会丢历史名）。
func readPackageNameFromSnapshot(snapJSON string) string {
	if snapJSON == "" {
		return ""
	}
	var s struct {
		PackageName string `json:"package_name"`
	}
	if err := json.Unmarshal([]byte(snapJSON), &s); err != nil {
		return ""
	}
	return s.PackageName
}

// getNextStackIndex 返回该用户该套餐下一个可用的 stack_index。
// 必须在事务内调用，scan 错误显式传播让外层 rollback 整笔购买，避免 stack_index 静默落到 1 破坏单调性。
func getNextStackIndex(tx *gorm.DB, userID, packageID uint) (int, error) {
	var maxIdx int
	if err := tx.Model(&database.UserSubscription{}).
		Where("user_id = ? AND package_id = ?", userID, packageID).
		Select("COALESCE(MAX(stack_index), 0)").
		Scan(&maxIdx).Error; err != nil {
		return 0, fmt.Errorf("scan max stack_index: %w", err)
	}
	return maxIdx + 1, nil
}
