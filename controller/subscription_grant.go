// Package controller / subscription_grant.go
//
// 管理员赠送订阅入口。
//
// 业务模型：
//   - admin 通过 POST /api/admin/subscriptions/grant 给目标用户免费开通某个产品
//   - 复用 purchaseAsInstant 的事务骨架（lockUser、stack count 校验、snapshot、stack index）
//   - **跳过 quota 扣款**：用户未付费，所以 user.Quota 不动
//   - 账单类型：admin_grant_sub（AmountUSD=0）
//   - 赠送的 UserSubscription 被标记 IsGranted=true → AdminRefundSubscription 拒绝退款
//     （否则平台等于把套餐白送 + 退款双倍亏损）
//
// 安全要求：
//   - 必须经 AdminGuard 中间件
//   - 必须经 refundLimiter 限流（防误点 / 恶意脚本）
//   - 必须 reason 不为空（审计可追溯）
//   - 不允许给 admin 自己赠送（避免自我审批漏洞）
//   - 必须真实用户存在 + 状态正常（status=1）
//   - 套餐必须启用 + 数量在 stack 上限内
package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// adminGrantPayload 管理员赠送的请求体
type adminGrantPayload struct {
	UserID    uint `json:"user_id"`
	PackageID uint `json:"package_id"`
	// Quantity *int：nil = 默认 1；显式 0/-N 返回 ERR_INVALID_QUANTITY。
	// fix MAJOR（codex 第十六轮）：与 PurchasePackage 一致防御深度。
	Quantity     *int   `json:"quantity"`
	Reason       string `json:"reason"`                  // 必填，进 OperationLog + BillingEntry.Description
	ValidSeconds *int64 `json:"valid_seconds,omitempty"` // 可选：自定义有效期（秒）
	// DeprecatedApplyBonus 显式拒绝旧协议字段，而不是静默忽略。
	DeprecatedApplyBonus *json.RawMessage `json:"apply_bonus"`
}

const (
	grantReasonMaxLen   = 500 // 防 admin 把整段 JSON / log 粘进来
	grantQuantityMaxCap = 100 // 与 PurchasePackage 一致
	// fix CRITICAL C4（codex 第二十轮）：与 package_admin.go MaxBillingPeriodSeconds 统一为 5 年。
	// 旧 100 年上限对 int64 是安全的，但实际业务中超过 5 年的订阅都是数据错误，
	// 收紧上限作为额外屏障 —— 任何 admin 误填都被卡在最初的 validation 而非购买/赠送。
	maxBillingPeriodSec = int64(MaxBillingPeriodSeconds)
)

// fix MAJOR（codex 第二十轮）：事务内重读到 user/pkg 状态变化时的回滚 sentinel
var (
	errTargetUserChanged = errors.New("target user role/status changed concurrently")
	errPackageChanged    = errors.New("package enabled/invariant changed concurrently")
	// fix CRITICAL C-A1（codex 第二十一轮）：tx 内重读 freshPkg 后 BillingPeriodSeconds 校验失败专用 sentinel
	errPackageInvalidPeriodInTx = errors.New("package billing_period_seconds out of range in fresh tx")
)

// AdminGrantSubscription POST /api/admin/subscriptions/grant
//
// 关键约束：
//   - admin 不能赠送给自己（避免漏洞放大：万一 token 被盗，攻击者用 admin 身份不停给自己开 VIP）
//   - 赠送的 UserSubscription 标记 IsGranted=true + GrantReason，refund 路径会拒绝
//   - reason 必填且 ≤ 500 字（防把日志当 reason 粘进来污染审计字段）
func AdminGrantSubscription(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}

	var req adminGrantPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if req.DeprecatedApplyBonus != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "apply_bonus 已移除",
			"message_code": "ERR_DEPRECATED_FIELD",
		})
	}
	// ─── 输入校验 ──────────────────────────────────────────────
	if req.UserID == 0 || req.PackageID == 0 {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "user_id 与 package_id 必填",
			"message_code": "ERR_INVALID_PARAMS",
		})
	}
	if req.UserID == op.ID {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "管理员不能给自己赠送",
			"message_code": "ERR_GRANT_SELF",
		})
	}
	// fix MAJOR（codex 第十六轮）：显式 0/-N 拒绝；nil 视为缺省 1
	qty := 1
	if req.Quantity != nil {
		if *req.Quantity < 1 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "quantity 必须 ≥ 1",
				"message_code": "ERR_INVALID_QUANTITY",
			})
		}
		qty = *req.Quantity
	}
	if qty > grantQuantityMaxCap {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("quantity 不能超过 %d", grantQuantityMaxCap),
			"message_code": "ERR_QUANTITY_TOO_LARGE",
		})
	}
	if req.ValidSeconds != nil {
		if *req.ValidSeconds <= 0 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "valid_seconds 必须大于 0",
				"message_code": "ERR_INVALID_VALID_SECONDS",
			})
		}
		if *req.ValidSeconds > maxBillingPeriodSec {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      fmt.Sprintf("valid_seconds 不能超过 %d 秒", maxBillingPeriodSec),
				"message_code": "ERR_VALID_SECONDS_TOO_LARGE",
			})
		}
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "reason 必填（用于审计 / 用户客服查询）",
			"message_code": "ERR_REASON_REQUIRED",
		})
	}
	// 长度防御：admin 误把整段日志粘进来会把 OperationLog/BillingEntry 字段塞爆。
	// 用 rune 计数避开多字节截断。
	if runeLen := len([]rune(reason)); runeLen > grantReasonMaxLen {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("reason 长度不能超过 %d 字符（当前 %d）", grantReasonMaxLen, runeLen),
			"message_code": "ERR_REASON_TOO_LONG",
		})
	}
	// 控制字符防御：换行 / 回车 / Tab / NUL / ESC 等会让审计日志解析为多行 / 终端伪造，
	// 破坏对账与日志分析。
	// fix MINOR（codex 第二十轮）：原仅过滤 \r\n\t；改为 unicode.IsControl 全量拒绝。
	for _, r := range reason {
		if unicode.IsControl(r) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "reason 不能包含控制字符（换行 / 制表符 / NUL / ESC 等）",
				"message_code": "ERR_REASON_CTRL_CHAR",
			})
		}
	}

	// ─── 加载实体 ──────────────────────────────────────────────
	var targetUser database.User
	if err := database.DB.First(&targetUser, req.UserID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{
			"success":      false,
			"message":      "目标用户不存在",
			"message_code": "ERR_USER_NOT_FOUND",
		})
	}
	if targetUser.Role != "user" {
		// 防止给另一个 admin 赠送（admin 之间的"礼物"会让审计很复杂；如确需可后续放开）
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message":      "只能赠送给普通用户（role=user）",
			"message_code": "ERR_TARGET_NOT_USER",
		})
	}
	if targetUser.Status != 1 {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "目标用户状态异常（封禁 / 删除），无法赠送",
			"message_code": "ERR_TARGET_USER_INACTIVE",
		})
	}

	var pkg database.Package
	if err := database.DB.First(&pkg, req.PackageID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{
			"success":      false,
			"message":      "套餐不存在",
			"message_code": "ERR_PACKAGE_NOT_FOUND",
		})
	}
	if !pkg.IsEnabled() {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "套餐已禁用，无法赠送",
			"message_code": "ERR_PACKAGE_DISABLED",
		})
	}
	// fix MAJOR（codex 第二十轮）：边界防御。
	// fix MAJOR M22-A1 Phase 1：金额已是 int64 micro_usd，整数算术无 NaN/Inf 风险；
	// 仍保留 < 0 检查。
	if pkg.PriceAmount < 0 {
		log.Printf("[GRANT] BLOCKED package %d numeric invariant: price=%d", pkg.ID, pkg.PriceAmount)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_INVALID_NUMERIC"})
	}
	if pkg.BillingPeriodSeconds <= 0 || int64(pkg.BillingPeriodSeconds) > maxBillingPeriodSec {
		log.Printf("[GRANT] BLOCKED package %d invalid billing period: %d", pkg.ID, pkg.BillingPeriodSeconds)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_PACKAGE_INVALID_PERIOD"})
	}
	effectiveSeconds := int64(pkg.BillingPeriodSeconds)
	if req.ValidSeconds != nil {
		effectiveSeconds = *req.ValidSeconds
	}
	customValidity := req.ValidSeconds != nil

	// 事务外快速预检：stack 上限快路径（真正强制在事务内）
	effectiveMax := pkg.MaxActivePerUser
	if !pkg.IsStackable() {
		effectiveMax = 1
	}
	if effectiveMax > 0 {
		// fix MAJOR（codex 第十五轮）：active count 必须排除已过期行（end_at <= now），
		// 否则用户的"过期但未结算"订阅会占名额，导致 admin 无法补发。
		// fix Minor Mi-2（codex 第二十一轮）：检查 .Error，DB 故障时立即返回 500
		var activeCount int64
		if err := database.DB.Model(&database.UserSubscription{}).
			Where("user_id = ? AND package_id = ? AND status = ? AND end_at > ?", req.UserID, pkg.ID, "active", time.Now()).
			Count(&activeCount).Error; err != nil {
			log.Printf("[GRANT] pre-tx stack count failed user=%d pkg=%d err=%v", req.UserID, pkg.ID, err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
		if int(activeCount)+qty > effectiveMax {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      fmt.Sprintf("目标用户已达该套餐持有上限 %d 份", effectiveMax),
				"message_code": "ERR_STACK_LIMIT",
			})
		}
	}

	snapshot, err := buildPackageSnapshot(&pkg)
	if err != nil {
		log.Printf("[GRANT] buildPackageSnapshot pkg=%d err=%v", pkg.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_BUILD_SNAPSHOT"})
	}

	// ─── 事务：创建订阅 + 写账单 + 审计 ──────────────
	created := []database.UserSubscription{}
	now := time.Now()
	endAt := now.Add(time.Duration(effectiveSeconds) * time.Second)

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 锁 user 父行 → 与 purchase / token-create 串行化（避免 stack count 竞态）
		if err := lockUserForUpdate(tx, req.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		// fix MAJOR（codex 第二十轮）：事务内重读 user 状态。
		// 事务外读到 status=1 的用户可能被另一个 admin 并发封禁；之前只锁 user 行不重读
		// 还是会让赠送提交到刚刚被封禁的账户。
		var freshTarget database.User
		if err := tx.Select("id, role, status").First(&freshTarget, req.UserID).Error; err != nil {
			return fmt.Errorf("re-read target user: %w", err)
		}
		if freshTarget.Role != "user" || freshTarget.Status != 1 {
			return errTargetUserChanged
		}
		// fix MAJOR（codex 第二十轮）：事务内重读 package 状态。
		// 事务外读到 enabled=true 的套餐可能被另一个 admin 并发禁用；同理重读避免使用脏快照。
		// fix MAJOR（codex 第十六轮）：加 SELECT FOR UPDATE 锁 package 行，与 purchase 路径对齐
		var freshPkg database.Package
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&freshPkg, req.PackageID).Error; err != nil {
			return fmt.Errorf("re-read package: %w", err)
		}
		if !freshPkg.IsEnabled() {
			return errPackageChanged
		}
		// invariant 在事务内再校验一次（防 admin 并发改 price）
		if freshPkg.PriceAmount < 0 {
			return errPackageChanged
		}
		// fix CRITICAL C-A1（codex 第二十一轮）：tx 内必须复检 BillingPeriodSeconds 上限，
		// 与 purchase 路径对齐，防止 DB 直改 / admin 并发更新留下损坏套餐继续被赠送。
		if freshPkg.BillingPeriodSeconds <= 0 || int64(freshPkg.BillingPeriodSeconds) > maxBillingPeriodSec {
			return errPackageInvalidPeriodInTx
		}
		// 用事务内读到的 fresh 副本继续后续写入，避免脏快照
		pkg = freshPkg
		// fix MAJOR（codex 第十六轮）：tx 内必须用 freshPkg 重算 effectiveMax。
		// 旧代码 effectiveMax 在事务外按旧 pkg.MaxActivePerUser 算；admin 并发降低
		// MaxActivePerUser 或翻转 Stackable=false 时，本 tx 内 stack 校验仍按旧上限放行。
		effectiveMax = pkg.MaxActivePerUser
		if !pkg.IsStackable() {
			effectiveMax = 1
		}
		// fix MAJOR M22-4（codex 第二十二轮）：snapshot/endAt 事务内重建。
		// 必须用 buildPackageSnapshotTx(tx, ...) 而非 buildPackageSnapshot(...)——后者用 database.DB
		// 直查会因 SQLite 单连接（MaxOpenConns=1）等待事务自己持有的连接而死锁。
		var rebuildErr error
		snapshot, rebuildErr = buildPackageSnapshotTx(tx, &pkg)
		if rebuildErr != nil {
			log.Printf("[GRANT] re-build snapshot in tx pkg=%d err=%v", pkg.ID, rebuildErr)
			return fmt.Errorf("re-build snapshot: %w", rebuildErr)
		}
		endAt = now.Add(time.Duration(effectiveSeconds) * time.Second)
		// 事务内重新校验 stack（防 TOCTOU）
		// fix MAJOR（codex 第十五轮）：与事务外预检对齐，必须排除已过期行
		if effectiveMax > 0 {
			var activeCount int64
			if err := tx.Model(&database.UserSubscription{}).
				Where("user_id = ? AND package_id = ? AND status = ? AND end_at > ?", req.UserID, pkg.ID, "active", time.Now()).
				Count(&activeCount).Error; err != nil {
				return fmt.Errorf("count active subs: %w", err)
			}
			if int(activeCount)+qty > effectiveMax {
				return errStackLimitExceeded
			}
		}

		// 分配 stack index + ConsumptionOrder
		baseIdx, err := getNextStackIndex(tx, req.UserID, pkg.ID)
		if err != nil {
			return fmt.Errorf("compute stack index: %w", err)
		}
		baseMicro := now.UnixMicro()
		for i := 0; i < qty; i++ {
			sub := database.UserSubscription{
				UserID:           req.UserID,
				PackageID:        pkg.ID,
				PackageSnapshot:  snapshot,
				StartAt:          now,
				EndAt:            endAt,
				ConsumptionOrder: baseMicro + int64(i),
				StackIndex:       baseIdx + i,
				Status:           "active",
				IsGranted:        true,
				GrantReason:      reason,
			}
			if err := tx.Create(&sub).Error; err != nil {
				return fmt.Errorf("create sub: %w", err)
			}
			created = append(created, sub)
		}

		// 重读余额作为 BalanceAfterUSD 锚点
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, req.UserID).Error; err != nil {
			return fmt.Errorf("fetch fresh quota: %w", err)
		}
		baseEntryMicro := now.UnixMicro()
		// Phase 8：所有 admin grant 都走 admin_grant_sub
		entryType := database.BillingTypeAdminGrantSub
		for i, sub := range created {
			subID := sub.ID
			grantOccurredAt := time.UnixMicro(baseEntryMicro + int64(i))
			description := fmt.Sprintf("管理员赠送「%s」#%d · admin#%d · %s", pkg.Name, sub.StackIndex, op.ID, reason)
			if customValidity {
				description += fmt.Sprintf("（自定义 %d 秒）", effectiveSeconds)
			}
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:           req.UserID,
				OccurredAt:       grantOccurredAt,
				EntryType:        entryType,
				AmountUSD:        0, // 赠送：用户未付费，资金面不动
				BalanceAfterUSD:  freshUser.Quota,
				RelatedType:      "subscription",
				RelatedID:        subID,
				Description:      description,
				CurrencyOriginal: pkg.PriceCurrency,
				AmountOriginal:   0,
			}); err != nil {
				return fmt.Errorf("write billing grant: %w", err)
			}
		}

		// 审计日志：写一条聚合记录（subID 列表）
		// 用 json.Marshal 确保 details 是合法 JSON（slice 不会被 %v 拼成 `[1 2]`），
		// 后续 admin 面板 / 审计工具能直接解析。
		subIDs := make([]uint, 0, len(created))
		for _, s := range created {
			subIDs = append(subIDs, s.ID)
		}
		auditDetail := map[string]any{
			"type":         "GRANT_SUBSCRIPTION",
			"package_id":   pkg.ID,
			"package_name": pkg.Name,
			"quantity":     qty,
			"sub_ids":      subIDs,
			"reason":       reason,
		}
		if customValidity {
			auditDetail["valid_seconds"] = effectiveSeconds
		}
		auditDetails := []map[string]any{auditDetail}
		detailsJSON, err := json.Marshal(auditDetails)
		if err != nil {
			return fmt.Errorf("marshal audit details: %w", err)
		}
		if err := LogOperationByTx(tx, op.ID, req.UserID, "admin", "GRANT_SUBSCRIPTION", c.IP(), string(detailsJSON)); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}
		return nil
	})

	if txErr != nil {
		if errors.Is(txErr, errStackLimitExceeded) {
			// fix Minor（codex 第十七轮）：错误文案用 effectiveMax 而非 MaxActivePerUser，
			// 否则 !Stackable 套餐显示的上限会和实际限制（1）不一致，让 admin 误以为还能加。
			limitForMsg := pkg.MaxActivePerUser
			if !pkg.IsStackable() {
				limitForMsg = 1
			}
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      fmt.Sprintf("已达该套餐叠加上限 %d 份（事务内并发检查）", limitForMsg),
				"message_code": "ERR_STACK_LIMIT",
			})
		}
		if errors.Is(txErr, errTargetUserChanged) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "目标用户状态在事务期间发生变化（被封禁 / 角色变更），赠送已回滚",
				"message_code": "ERR_TARGET_USER_CHANGED",
			})
		}
		if errors.Is(txErr, errPackageChanged) {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message":      "套餐状态在事务期间发生变化（被禁用 / invariant 失效），赠送已回滚",
				"message_code": "ERR_PACKAGE_CHANGED",
			})
		}
		// fix CRITICAL C-A1（codex 第二十一轮）：tx 内 period 上限校验失败专用错误码
		if errors.Is(txErr, errPackageInvalidPeriodInTx) {
			log.Printf("[GRANT] BLOCKED package %d invalid billing_period_seconds in fresh tx", req.PackageID)
			return c.Status(500).JSON(fiber.Map{
				"success":      false,
				"message":      "套餐 billing_period_seconds 数据损坏（DB 直改 / 并发越界），赠送已回滚",
				"message_code": "ERR_PACKAGE_INVALID_PERIOD",
			})
		}
		log.Printf("[GRANT] tx failed admin=%d target=%d pkg=%d err=%v", op.ID, req.UserID, pkg.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	// 事务成功后：刷新缓存 + 通知用户
	proxy.InvalidateUserSubscriptionCache(req.UserID)
	proxy.RefreshUserAuth(req.UserID)
	createGrantNotification(req.UserID, &pkg, len(created), reason)

	return c.JSON(fiber.Map{
		"success":      true,
		"data":         created,
		"message_code": "SUCCESS_GRANTED",
	})
}

// createGrantNotification 给目标用户发"管理员赠送"通知。
// 强制送达类（system 类）→ 永远不被用户偏好屏蔽，因为这是平台单方面给的福利，
// 用户至少要知道发生了什么，否则会出现"我怎么突然多了套餐"的客服疑问。
func createGrantNotification(userID uint, pkg *database.Package, qty int, reason string) {
	body := fmt.Sprintf("管理员为您激活了 %d 份「%s」", qty, pkg.Name)
	if reason != "" {
		// 截短，避免一长串 reason 撑爆通知 body 显示
		shortReason := reason
		if len([]rune(shortReason)) > 80 {
			shortReason = string([]rune(shortReason)[:80]) + "…"
		}
		body += " · " + shortReason
	}
	proxy.Dispatch(
		userID,
		"system", // 强制送达，绕开偏好（赠送是单方面动作，必须告知）
		"success",
		"您收到一份赠送",
		body,
		proxy.LinkUpgradeMine(),
		"查看订阅",
		"subscription", 0,
		nil,
	)
}
