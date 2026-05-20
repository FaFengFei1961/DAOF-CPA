// Package controller / subscription_purchase.go
//
// 用户购买套餐入口：PurchasePackage 是 HTTP handler，purchaseAsInstant 是事务体。
// 涉及行锁 / 事务内重读 package / 优惠券消费 / referral spend reward / 通知发出。
//
// 从 subscription.go 抽出（Phase D-5，2026-05-19）：只是物理拆分，无语义改动。
package controller

import (
	"errors"
	"fmt"
	"log"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// errStackLimitExceeded 是事务内业务级错误的 sentinel，避免外部用 .Error() 字符串比较
var errStackLimitExceeded = errors.New("subscription stack limit exceeded")

// errInsufficientBalance 由购买套餐事务内条件 UPDATE 失败抛出（并发竞态）
var errInsufficientBalance = errors.New("insufficient balance at commit (concurrent purchase race)")

// fix CRITICAL R23+3-C3（codex 第四轮）：事务内重读 package 后的校验失败 sentinel
var (
	errPackageGoneInTx           = errors.New("package vanished during transaction (admin deleted)")
	errPackageDisabledInTx       = errors.New("package disabled during transaction (admin disabled)")
	errPackageNotPublicInTx      = errors.New("package not public during transaction (admin made private)")
	errPackageInvalidNumericInTx = errors.New("package numeric invariant violated during transaction")
	// fix Minor Mi-1（codex 第二十一轮）：BillingPeriodSeconds 上限校验失败专用 sentinel，
	errPackagePeriodInvalidInTx = errors.New("package billing_period_seconds out of range during transaction")
)

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
	if pkg.PriceAmount < 0 {
		log.Printf("[SUB] BLOCKED package %d invalid price=%d (micro_usd)", pkg.ID, pkg.PriceAmount)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_INVALID_NUMERIC"})
	}

	// 事务外乐观估价：仅用于"用户余额是否够付原价"的快速友好检查。
	//
	// fix CRITICAL R23+2-C1（codex 全方面 第二轮）：使用券购买时跳过事务外预检 ——
	// 否则免费券用户余额=0 时会被这里 402 拒绝，根本进不了事务内的 lockAndApplyCoupon。
	// 事务内有 quota >= netDeduct 条件 UPDATE 兜底，并发安全 + 真值由 DB 决定。
	//
	// fix CRITICAL Phase 4-codex（第二十四轮）：price * qty 必须 checked，
	// 防 admin 设极端套餐价 + 大 qty 导致 int64 溢出回绕成负值穿透余额检查。
	if couponID == 0 {
		totalPriceMicro, ok := database.CheckedMulInt64(pkg.PriceAmount, int64(qty))
		if !ok {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PRICE_OVERFLOW"})
		}
		if user.Quota < totalPriceMicro {
			return c.Status(402).JSON(fiber.Map{
				"success":      false,
				"message":      "余额不足",
				"message_code": "ERR_INSUFFICIENT_BALANCE",
				"required":     database.MicroToUSD(totalPriceMicro),
				"current":      database.MicroToUSD(user.Quota),
			})
		}
	}

	created := []database.UserSubscription{}
	referralRewards := []database.ReferralPaidSpendRewardResult{}
	referralRewardBPS, referralRewardWindowSeconds := readReferralPaidSpendRewardConfig()
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
		if freshPkg.PriceAmount < 0 {
			return errPackageInvalidNumericInTx
		}
		// fix CRITICAL C4（codex 第二十轮）+ Mi-1（第二十一轮）：事务内必须复检 BillingPeriodSeconds 上限，
		// 防 admin DB 直改超大值后被购买路径放行（time.Duration 整数溢出 → 异常时间戳）。
		// 拆出独立 sentinel 让前端能区分"金额异常"与"period 越界"。
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

		if totalPriceMicro > 0 {
			// fix CRITICAL：原实现仅做事务外余额检查，事务内无条件 quota - netDeduct，
			// 并发两笔购买请求都通过事务外检查后会让余额变负。
			// 改为条件 UPDATE：WHERE id=? AND quota >= netDeduct，并校验 RowsAffected。
			res := tx.Model(&database.User{}).
				Where("id = ? AND quota >= ?", user.ID, totalPriceMicro).
				UpdateColumn("quota", gorm.Expr("quota - ?", totalPriceMicro))
			if res.Error != nil {
				return fmt.Errorf("deduct quota: %w", res.Error)
			}
			if res.RowsAffected == 0 {
				// 并发竞态：另一笔购买已扣空余额。返回 sentinel 错误让上层给出 402。
				return errInsufficientBalance
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
			// 退款时按这个字段算实际支付价，而不是 snapshot 的原价 → 不再有"用券价买、按原价退"的套利。
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

		// 账单流水：购买
		// 重读余额作为 BalanceAfterUSD 锚点
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, user.ID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		// Phase 8：所有套餐购买都走 purchase_sub
		entryType := database.BillingTypePurchaseSub
		// 计算购买扣款前的基线余额：freshUser.Quota 是事务内最终值。
		balRollingMicro := freshUser.Quota + totalPriceMicro
		// 关联券到第 1 份订阅（在写账单前完成，使描述能引用 used coupon）
		if usedCoupon != nil && len(created) > 0 {
			usedCoupon.UsedOnSubID = &created[0].ID
			usedCoupon.UsedSavingUSD = pkg.PriceAmount - perSubPricesMicro[0]
			if err := tx.Save(usedCoupon).Error; err != nil {
				return fmt.Errorf("link coupon to sub: %w", err)
			}
		}
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
		if totalPriceMicro > 0 && len(created) > 0 {
			rewardRelatedID := created[0].ID
			rewardLabel := fmt.Sprintf("购买「%s」", pkg.Name)
			if qty > 1 {
				rewardLabel = fmt.Sprintf("购买「%s」×%d", pkg.Name, qty)
			}
			reward, err := database.ApplyReferralPaidSpendRewardTx(
				tx,
				user.ID,
				totalPriceMicro,
				referralRewardBPS,
				referralRewardWindowSeconds,
				now,
				"subscription",
				rewardRelatedID,
				rewardLabel,
			)
			if err != nil {
				return fmt.Errorf("apply referral spend reward: %w", err)
			}
			if reward.RewardMicroUSD > 0 {
				referralRewards = append(referralRewards, reward)
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
		if errors.Is(err, errCouponSnapshotBelowCostFloor) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "优惠券价格快照低于当前套餐下限，请提交工单或更换优惠券",
				"message_code": MessageCodeCouponSnapshotBelowCostFloor,
			})
		}
		if errors.Is(err, errCouponNotApplicable) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "该优惠券不适用于当前套餐",
				"message_code": "ERR_COUPON_NOT_APPLICABLE",
			})
		}
		if errors.Is(err, errCouponInvalid) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "优惠券不可用或已失效",
				"message_code": "ERR_COUPON_INVALID",
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
		case errors.Is(err, errPackageInvalidNumericInTx):
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_INVALID_NUMERIC",
				"message": "套餐金额数据损坏，请提交工单"})
		case errors.Is(err, errPackagePeriodInvalidInTx):
			// fix Minor Mi-1（codex 第二十一轮）：period 越界专用错误码
			log.Printf("[PURCHASE] BLOCKED package %d invalid billing_period_seconds in fresh tx", pkg.ID)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_INVALID_PERIOD",
				"message": "套餐周期数据损坏，请提交工单"})
		}
		// 不向客户端泄露 GORM 内部 err（含表名/约束名等）
		log.Printf("[SUB] purchase tx failed user=%d pkg=%d err=%v", user.ID, pkg.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}
	proxy.InvalidateUserSubscriptionCache(user.ID)
	proxy.RefreshUserAuth(user.ID) // 扣 quota 后刷新缓存，否则前端余额陈旧
	for _, reward := range referralRewards {
		proxy.RefreshUserAuth(reward.ReferrerID)
		LogOperationBy(0, reward.ReferrerID, "system", "REFERRAL_SPEND_REWARD", c.IP(),
			fmt.Sprintf(`[{"type":"REFERRAL_SPEND_REWARD","referee_id":%d,"referee":%q,"related_type":%q,"related_id":%d,"eligible_spend":%g,"eligible_spend_micro":%d,"rate_bps":%d,"window_seconds":%d,"amount":%g,"amount_micro":%d}]`,
				reward.RefereeID, reward.RefereeUsername, reward.RelatedType, reward.RelatedID,
				database.MicroToUSD(reward.EligibleSpendMicroUSD), reward.EligibleSpendMicroUSD,
				reward.RateBPS, reward.WindowSeconds,
				database.MicroToUSD(reward.RewardMicroUSD), reward.RewardMicroUSD))
	}
	createPurchaseNotification(user.ID, pkg, len(created))
	return c.JSON(fiber.Map{"success": true, "data": created, "message_code": "SUCCESS_PURCHASED"})
}
