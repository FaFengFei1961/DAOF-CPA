// Package controller / coupon.go
//
// 优惠券系统的所有 HTTP handler + helper。
//
// 路径划分：
//
//	admin: 模板 CRUD + 给指定用户发券 + 撤销已发但未用券
//	user:  我的券列表 + 购买时选用某张券（在 PurchasePackage 路径里）
package controller

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ─── helper ─────────────────────────────────────────────────────────────

var errCouponInvalid = errors.New("coupon invalid")
var errCouponNotApplicable = errors.New("coupon not applicable to this package")
var errCouponFixedPriceBelowPackageCostFloor = errors.New("coupon fixed_price below package cost_floor")

// fix Major（codex 第十五轮）：admin 并发禁用 template / 修改启用状态时，
// 事务内重读会发现脏快照——返回此哨兵让外层映射到 409 + 明确 message_code
var errCouponTemplateChanged = errors.New("coupon template state changed concurrently")

// generateCouponCode 32 字符 hex（128bit）防猜，附 user/template prefix 便于运维查日志。
//
// fix MAJOR R23+2-B3（codex 第三轮）：crypto/rand 失败时**不能**用全零 fallback ——
// 同 user 同 template 重复 regrant/grant 会触发 unique 索引冲突；更糟的是熵源失败暴露给
// 攻击者（猜券码）。失败必须向上传播让调用方处理。
func generateCouponCode(userID, templateID uint) (string, error) {
	b := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("crypto/rand read: %w", err)
	}
	return fmt.Sprintf("CP-%d-%d-%s", userID, templateID, hex.EncodeToString(b)), nil
}

// parsePackageIDsStrict 严格解析 SnapshotPackageIDs。区分三种状态：
//
//	("", true)        — 空字符串，视为"全适用"
//	([1,2,3], true)   — 合法 JSON 数组
//	(nil, false)      — 损坏 JSON，调用方应当拒绝消费券（fail-closed）
//
// fix MAJOR R23+2-B3（codex 二轮）：原 parsePackageIDsJSON 把"损坏 JSON"和"空"当同样处理，
// 让限制券在 snapshot 损坏时变成全适用——攻击面。
func parsePackageIDsStrict(s string) ([]uint, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, true // 空字符串 = 全适用，合法
	}
	var ids []uint
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		return nil, false // 损坏 JSON
	}
	return ids, true
}

// couponMinFixedPriceMicroUSD fixed_price 优惠券的最低面额（micro_usd 单位）。
// 防 admin 误配 / 配置错误生成"$0 全套餐券"放大亏损（参见 codex 模块 5 审计 P0 #2）。
//
// 默认 0.01 USD = 10000 micro_usd（一分钱保底，远低于任何套餐价但绝非"免费"）。
// 业务侧若需更严格成本下限，可通过 SysConfig `coupon_min_fixed_price_micro_usd` 调高。
const couponMinFixedPriceMicroUSD = int64(10_000)

const MessageCodeCouponFixedPriceBelowPackageCostFloor = "ERR" + "_COUPON_FIXED_PRICE_BELOW_PACKAGE_COST_FLOOR"

func couponTemplateValidationMessageCode(err error) string {
	if errors.Is(err, errCouponFixedPriceBelowPackageCostFloor) {
		return MessageCodeCouponFixedPriceBelowPackageCostFloor
	}
	return "ERR_INVALID_TEMPLATE"
}

func validateTemplateFixedPriceCostFloor(t *database.CouponTemplate, packageIDs []uint) error {
	if t.DiscountType != "fixed_price" {
		return nil
	}
	var maxCostPkg database.Package
	q := database.DB.Model(&database.Package{}).
		Select("id, name, cost_floor_micro_usd").
		Where("cost_floor_micro_usd > ?", 0)
	if len(packageIDs) > 0 {
		q = q.Where("id IN ?", packageIDs)
	}
	if err := q.Order("cost_floor_micro_usd DESC, id ASC").Limit(1).Find(&maxCostPkg).Error; err != nil {
		return fmt.Errorf("查询套餐成本下限失败: %w", err)
	}
	if maxCostPkg.ID == 0 {
		return nil
	}
	if t.DiscountValue >= maxCostPkg.CostFloorMicroUSD {
		return nil
	}
	return fmt.Errorf("%w: fixed_price %d micro_usd ($%s) 低于套餐「%s」(#%d) 成本下限 %d micro_usd ($%s)",
		errCouponFixedPriceBelowPackageCostFloor,
		t.DiscountValue,
		database.FormatMicroUSD(t.DiscountValue),
		maxCostPkg.Name,
		maxCostPkg.ID,
		maxCostPkg.CostFloorMicroUSD,
		database.FormatMicroUSD(maxCostPkg.CostFloorMicroUSD))
}

// validateTemplate 校验模板字段。
//
// fix CRITICAL Sprint3-M5 P0-2：fixed_price 不允许等于 0（旧实现 DiscountValue ≥ 0 通过，
// 但 fixed_price=0 等于"任意套餐 0 元购"，被批量发券放大后即亏损黑洞）。
// 改为：fixed_price 必须 ≥ couponMinFixedPriceMicroUSD 微 USD。
// 若 admin 真需要"免费券"，应通过赠送订阅（AdminGrantSubscription）路径，那条路径有
// IsGranted=true 字段防止冲账与退款，且产品语义清晰。
func validateTemplate(t *database.CouponTemplate) error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("name 必填")
	}
	if t.DiscountType == "" {
		t.DiscountType = "fixed_price"
	}
	if t.DiscountType != "fixed_price" {
		return fmt.Errorf("discount_type 当前仅支持 fixed_price")
	}
	if t.DiscountValue < 0 {
		return fmt.Errorf("discount_value 必须 ≥ 0")
	}
	// fix CRITICAL Sprint3-M5 P0-2：禁止 fixed_price = 0 / 过低
	if t.DiscountType == "fixed_price" && t.DiscountValue < couponMinFixedPriceMicroUSD {
		return fmt.Errorf("fixed_price 不能低于 %d micro_usd（$%.4f）；若需赠送服务请走赠送订阅路径",
			couponMinFixedPriceMicroUSD, float64(couponMinFixedPriceMicroUSD)/1_000_000)
	}
	if t.ValidDays < 0 {
		return fmt.Errorf("valid_days 不能为负数（0 = 永久）")
	}
	packageIDs, parseOK := parsePackageIDsStrict(t.PackageIDs)
	if !parseOK {
		return fmt.Errorf("package_ids 必须是 JSON 数组（如 [1,2,3]）或留空")
	}
	if err := validateTemplateFixedPriceCostFloor(t, packageIDs); err != nil {
		return err
	}
	return nil
}

// snapshotTemplate 把 template 关键字段快照到 UserCoupon（防 admin 改 template 影响已发券）
func snapshotTemplate(uc *database.UserCoupon, t *database.CouponTemplate) {
	uc.SnapshotName = t.Name
	uc.SnapshotType = t.DiscountType
	uc.SnapshotValue = t.DiscountValue
	uc.SnapshotPackageIDs = t.PackageIDs
}

// lockAndApplyCoupon 在事务内锁定并消费券。返回锁定后的 UserCoupon（已标记为 used）。
//
// fix MAJOR R23+2-B2（codex 二轮）：原 SELECT-then-Save 在 Postgres/MySQL 没真正锁行，
// admin revoke 与 user purchase 跨 admin/user 路径并发时可能互相覆盖。
// 改用条件 UPDATE：`UPDATE ... SET status='used' WHERE id=? AND user_id=? AND status='available'`，
// 通过 RowsAffected==1 判定独占消费成功；失败说明被另一方（用户重复点击 / admin revoke）抢走。
//
// 适用范围检查：先按券 ID + user 读快照（必读），再做 UPDATE。读阶段拿到的 SnapshotPackageIDs
// 是不可变快照（创建时落库），不会被 admin 改 template 影响。
func lockAndApplyCoupon(tx *gorm.DB, userID, couponID uint, pkg *database.Package) (*database.UserCoupon, error) {
	var coupon database.UserCoupon
	if err := tx.Where("id = ? AND user_id = ?", couponID, userID).First(&coupon).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errCouponInvalid
		}
		return nil, fmt.Errorf("lookup coupon: %w", err)
	}
	now := time.Now()
	if !coupon.IsAvailable(now) {
		return nil, errCouponInvalid
	}
	// fix MAJOR R23+2-B3（codex 二轮）：分清"空=全适用"和"非法 JSON=拒绝"
	allowed, parseOK := parsePackageIDsStrict(coupon.SnapshotPackageIDs)
	if !parseOK {
		log.Printf("[COUPON] corrupted snapshot package_ids on coupon %d: %q", coupon.ID, coupon.SnapshotPackageIDs)
		return nil, errCouponInvalid
	}
	if !coupon.AppliesToPackage(pkg.ID, allowed) {
		return nil, errCouponNotApplicable
	}
	// 条件 UPDATE：只有当前 status='available' 才能改成 'used'。
	// 任何并发抢占（用户重复点击 / admin revoke）会让 RowsAffected == 0 → 返回 errCouponInvalid。
	res := tx.Model(&database.UserCoupon{}).
		Where("id = ? AND user_id = ? AND status = ?", coupon.ID, userID, "available").
		Updates(map[string]any{
			"status":  "used",
			"used_at": now,
		})
	if res.Error != nil {
		return nil, fmt.Errorf("apply coupon: %w", res.Error)
	}
	if res.RowsAffected != 1 {
		return nil, errCouponInvalid // 被并发请求抢走 / 已变成 revoked / expired
	}
	// 回填本地副本以便调用方使用
	coupon.Status = "used"
	coupon.UsedAt = &now
	return &coupon, nil
}

// ─── admin: 模板 CRUD ─────────────────────────────────────────────────

// AdminListCouponTemplates GET /api/admin/coupon-templates
func AdminListCouponTemplates(c *fiber.Ctx) error {
	var list []database.CouponTemplate
	if err := database.DB.Order("id DESC").Find(&list).Error; err != nil {
		log.Printf("[COUPON-LIST] %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{"success": true, "data": couponTemplateViewsFrom(list)})
}

// couponTemplateJSON admin 端 JSON 表示（USD float），handler 内转 micro_usd。
type couponTemplateJSON struct {
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	DiscountType  string  `json:"discount_type"`
	DiscountValue float64 `json:"discount_value"` // USD float
	PackageIDs    string  `json:"package_ids"`
	ValidDays     int     `json:"valid_days"`
	Enabled       *bool   `json:"enabled"`
}

func parseCouponTemplate(c *fiber.Ctx) (database.CouponTemplate, error) {
	var raw couponTemplateJSON
	if err := c.BodyParser(&raw); err != nil {
		return database.CouponTemplate{}, err
	}
	// fix MAJOR Phase 4-codex（第二十四轮）：admin 金额必须过 MaxAdminQuotaUSD 上限
	if err := validateAdminQuotaInput(raw.DiscountValue); err != nil {
		return database.CouponTemplate{}, fmt.Errorf("discount_value: %w", err)
	}
	micro, ok := database.USDToMicro(raw.DiscountValue)
	if !ok {
		return database.CouponTemplate{}, fmt.Errorf("discount_value 非法")
	}
	return database.CouponTemplate{
		Name:          raw.Name,
		Description:   raw.Description,
		DiscountType:  raw.DiscountType,
		DiscountValue: micro,
		PackageIDs:    raw.PackageIDs,
		ValidDays:     raw.ValidDays,
		Enabled:       raw.Enabled,
	}, nil
}

// AdminCreateCouponTemplate POST /api/admin/coupon-templates
func AdminCreateCouponTemplate(c *fiber.Ctx) error {
	t, err := parseCouponTemplate(c)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if err := validateTemplate(&t); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": couponTemplateValidationMessageCode(err)})
	}
	if err := database.DB.Create(&t).Error; err != nil {
		log.Printf("[COUPON-CREATE] %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_CREATE"})
	}
	return c.JSON(fiber.Map{"success": true, "data": t, "message_code": "SUCCESS_CREATED"})
}

// AdminUpdateCouponTemplate PUT /api/admin/coupon-templates/:id
func AdminUpdateCouponTemplate(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var t database.CouponTemplate
	if err := database.DB.First(&t, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_NOT_FOUND"})
	}
	payload, err := parseCouponTemplate(c)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if err := validateTemplate(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": couponTemplateValidationMessageCode(err)})
	}
	updates := map[string]any{
		"name":           payload.Name,
		"description":    payload.Description,
		"discount_type":  payload.DiscountType,
		"discount_value": payload.DiscountValue,
		"package_ids":    payload.PackageIDs,
		"valid_days":     payload.ValidDays,
	}
	if payload.Enabled != nil {
		updates["enabled"] = *payload.Enabled
	}
	if err := database.DB.Model(&t).Updates(updates).Error; err != nil {
		log.Printf("[COUPON-UPDATE] %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_UPDATED"})
}

// AdminDeleteCouponTemplate DELETE /api/admin/coupon-templates/:id
// 软删 — 不影响已发出的 UserCoupon（已发券有 snapshot 字段，不依赖 template）
func AdminDeleteCouponTemplate(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	if err := database.DB.Delete(&database.CouponTemplate{}, id).Error; err != nil {
		log.Printf("[COUPON-DELETE] %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_DELETE"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_DELETED"})
}

// ─── admin: 发券给用户 ─────────────────────────────────────────────────

type grantCouponPayload struct {
	UserID     uint   `json:"user_id"`
	TemplateID uint   `json:"template_id"`
	Reason     string `json:"reason"`
	// Quantity *int：nil = 默认 1；显式 0/-N 返回 ERR_INVALID_QUANTITY。
	// fix MAJOR（codex 第十六轮）：与 PurchasePackage / AdminGrantSubscription 一致防御。
	Quantity *int `json:"quantity"`
}

// AdminGrantCoupon POST /api/admin/coupons/grant
//
// fix MAJOR R23+3-B8（codex 第四轮）：
//   - 加 quantity 字段支持一次发多张同款券（admin 节日批量补偿场景）
//   - 发券 + 审计放同一事务（之前 LogOperationByTx 错误被丢，券已发但审计断流）
//   - 发券失败整体回滚，不留"半成功"状态
func AdminGrantCoupon(c *fiber.Ctx) error {
	var payload grantCouponPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if payload.UserID == 0 || payload.TemplateID == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_REQUIRED"})
	}
	// fix MAJOR（codex 第十六轮）：显式 0/-N 拒绝；nil 视为缺省 1
	qty := 1
	if payload.Quantity != nil {
		if *payload.Quantity < 1 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "quantity 必须 ≥ 1",
				"message_code": "ERR_INVALID_QUANTITY",
			})
		}
		qty = *payload.Quantity
	}
	if qty > 100 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_QUANTITY_TOO_LARGE",
			"message": "单次最多发放 100 张同款券"})
	}
	// fix Minor（codex 第十六轮）：reason 长度 + 控制字符校验，与 grant subscription 对齐
	reason := strings.TrimSpace(payload.Reason)
	if runeLen := len([]rune(reason)); runeLen > grantReasonMaxLen {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("reason 长度不能超过 %d 字符（当前 %d）", grantReasonMaxLen, runeLen),
			"message_code": "ERR_REASON_TOO_LONG",
		})
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "reason 不能包含控制字符（换行 / 制表符 / NUL / ESC 等）",
				"message_code": "ERR_REASON_CTRL_CHAR",
			})
		}
	}
	payload.Reason = reason

	// fix Major（codex 第十五轮）：事务外 user/template 仅做"快路径校验失败早返回"，
	// 真正可信状态在事务内 SELECT FOR UPDATE 重读——admin 并发禁用模板/封禁用户时不让脏快照通过
	var u database.User
	if err := database.DB.First(&u, payload.UserID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_USER_NOT_FOUND"})
	}
	var tpl database.CouponTemplate
	if err := database.DB.First(&tpl, payload.TemplateID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_TEMPLATE_NOT_FOUND"})
	}
	if !tpl.IsEnabled() {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_TEMPLATE_DISABLED"})
	}

	operatorID := getOperatorID(c)
	createdIDs := make([]uint, 0, qty)
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix MAJOR M2（codex 第二十轮）：锁顺序必须与 PurchasePackage / AdminGrantSubscription 一致：
		//   lockUser → lockPackage/Template
		// 反序（先 template 后 user）会与购买路径互锁导致死锁：
		//   - Tx-A 在购买中先锁了 user → 等待 template
		//   - Tx-B 在发券中先锁了 template → 等待 user
		// 修复：先 lockUserForUpdate 串行化该 user 的所有写路径，再锁 template。
		if err := lockUserForUpdate(tx, payload.UserID); err != nil {
			return fmt.Errorf("lock target user: %w", err)
		}
		var freshU database.User
		if err := tx.Select("id, status").First(&freshU, payload.UserID).Error; err != nil {
			return fmt.Errorf("re-read user: %w", err)
		}
		if freshU.Status != 1 {
			return errTargetUserChanged
		}
		// 用户锁定后再锁 template（防 admin 并发禁用模板，本 tx 仍按旧 freshTpl 创建券）
		var freshTpl database.CouponTemplate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&freshTpl, payload.TemplateID).Error; err != nil {
			return fmt.Errorf("re-read template: %w", err)
		}
		if !freshTpl.IsEnabled() {
			return errCouponTemplateChanged
		}
		for i := 0; i < qty; i++ {
			uc, err := buildCouponFromTemplate(payload.UserID, &freshTpl, operatorID, payload.Reason)
			if err != nil {
				return fmt.Errorf("build coupon #%d: %w", i+1, err)
			}
			if err := tx.Create(&uc).Error; err != nil {
				return fmt.Errorf("create coupon #%d: %w", i+1, err)
			}
			createdIDs = append(createdIDs, uc.ID)
		}
		// 审计与创建同事务，任何失败都回滚
		// fix Major（codex 第十五轮）：原 fmt.Sprintf("%v", []uint) 产出 "[1 2 3]" 不是合法 JSON，
		// admin UI 解析 details 会断；改用 json.Marshal 保证格式
		details, jerr := json.Marshal(map[string]interface{}{
			"template_id": freshTpl.ID,
			"coupon_ids":  createdIDs,
			"quantity":    qty,
			"reason":      payload.Reason,
		})
		if jerr != nil {
			return fmt.Errorf("marshal audit: %w", jerr)
		}
		return LogOperationByTx(tx, operatorID, payload.UserID, "admin", "GRANT_COUPON", c.IP(), string(details))
	})
	if txErr != nil {
		log.Printf("[COUPON-GRANT] tx failed admin=%d user=%d tpl=%d qty=%d: %v",
			operatorID, payload.UserID, tpl.ID, qty, txErr)
		// fix Major（codex 第十五轮）：事务内并发态变更映射到 409
		if errors.Is(txErr, errCouponTemplateChanged) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_TEMPLATE_DISABLED"})
		}
		if errors.Is(txErr, errTargetUserChanged) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_USER_CHANGED"})
		}
		// 区分 rand 失败 vs DB 失败给前端不同 message_code
		if strings.Contains(txErr.Error(), "build coupon") {
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_RAND_FAILED"})
		}
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_CREATE"})
	}
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_GRANTED",
		"granted":      qty,
		"coupon_ids":   createdIDs,
	})
}

// ─── admin: 批量发券 ─────────────────────────────────────────────────────

type bulkGrantCouponPayload struct {
	UserIDs    []uint `json:"user_ids"`
	TemplateID uint   `json:"template_id"`
	Reason     string `json:"reason"`
	Quantity   *int   `json:"quantity"`
}

type bulkGrantUserResult struct {
	UserID    uint   `json:"user_id"`
	Success   bool   `json:"success"`
	Reason    string `json:"reason,omitempty"`
	CouponIDs []uint `json:"coupon_ids,omitempty"`
}

// AdminBulkGrantCoupon POST /api/admin/users/bulk-grant-coupon
//
// 给一批用户每人发放 qty 张同款券。**全 batch 单事务原子**：
// 任一用户失败 → 整批回滚，不发任何券。
//
// fix CRITICAL Sprint3-M5：旧实现"每用户独立事务"违反 1000 张券要么全发要么全废
// 的硬约束——admin 中途看到失败时已无法回滚前 N-1 个成功的发券。
//
// 限制：max 500 users / max 100 quantity → 单 batch 上限 50,000 券。
// SQLite 单写者 + INSERT 速率约 5k/s，最坏 10s 内完成事务；建议监控 tx 持有时间。
//
// 锁顺序：user_id 升序加锁（与其他用户级事务路径一致），避免 deadlock。
func AdminBulkGrantCoupon(c *fiber.Ctx) error {
	var p bulkGrantCouponPayload
	if err := c.BodyParser(&p); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	if len(p.UserIDs) == 0 || p.TemplateID == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_REQUIRED"})
	}
	if len(p.UserIDs) > 500 {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "批量发券最多支持 500 个用户",
			"message_code": "ERR_BULK_LIMIT",
		})
	}
	qty := 1
	if p.Quantity != nil {
		if *p.Quantity < 1 {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_QUANTITY"})
		}
		qty = *p.Quantity
	}
	if qty > 100 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_QUANTITY_TOO_LARGE",
			"message": "单次最多发放 100 张同款券"})
	}
	reason := strings.TrimSpace(p.Reason)
	if runeLen := len([]rune(reason)); runeLen > grantReasonMaxLen {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("reason 长度不能超过 %d 字符（当前 %d）", grantReasonMaxLen, runeLen),
			"message_code": "ERR_REASON_TOO_LONG",
		})
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_REASON_CTRL_CHAR"})
		}
	}
	p.Reason = reason

	// 预验 template（快路径，事务内仍会 SELECT FOR UPDATE 重读）
	var tpl database.CouponTemplate
	if err := database.DB.First(&tpl, p.TemplateID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_TEMPLATE_NOT_FOUND"})
	}
	if !tpl.IsEnabled() {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_TEMPLATE_DISABLED"})
	}

	// dedupe user_ids（admin 可能重复传同 id）+ 升序排序（lock order = deadlock prevention）
	seen := make(map[uint]bool, len(p.UserIDs))
	uniqueIDs := make([]uint, 0, len(p.UserIDs))
	for _, id := range p.UserIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			uniqueIDs = append(uniqueIDs, id)
		}
	}
	sort.Slice(uniqueIDs, func(i, j int) bool { return uniqueIDs[i] < uniqueIDs[j] })

	operatorID := getOperatorID(c)

	// 收集所有 batch 内创建的 coupon 与失败的用户 ID（用于错误响应）
	type bulkUserOp struct {
		UserID    uint
		CouponIDs []uint
	}
	var ops []bulkUserOp
	var failedUserID uint   // 触发回滚的 user_id（若有）
	var failedReason string // 触发回滚的标准化原因

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 锁 template 一次（整 batch 共享）
		var freshTpl database.CouponTemplate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&freshTpl, p.TemplateID).Error; err != nil {
			return fmt.Errorf("re-read template: %w", err)
		}
		if !freshTpl.IsEnabled() {
			return errCouponTemplateChanged
		}
		// 按升序 user_id 依次加锁 + 发券 + 审计
		for _, uid := range uniqueIDs {
			if err := lockUserForUpdate(tx, uid); err != nil {
				failedUserID = uid
				return fmt.Errorf("lock user %d: %w", uid, err)
			}
			var freshU database.User
			if err := tx.Select("id, status").First(&freshU, uid).Error; err != nil {
				failedUserID = uid
				return fmt.Errorf("re-read user %d: %w", uid, err)
			}
			if freshU.Status != 1 {
				failedUserID = uid
				return errTargetUserChanged
			}
			var createdIDs []uint
			for i := 0; i < qty; i++ {
				uc, err := buildCouponFromTemplate(uid, &freshTpl, operatorID, p.Reason)
				if err != nil {
					failedUserID = uid
					return fmt.Errorf("build coupon for user %d #%d: %w", uid, i+1, err)
				}
				if err := tx.Create(&uc).Error; err != nil {
					failedUserID = uid
					return fmt.Errorf("create coupon for user %d #%d: %w", uid, i+1, err)
				}
				createdIDs = append(createdIDs, uc.ID)
			}
			ops = append(ops, bulkUserOp{UserID: uid, CouponIDs: createdIDs})

			details, jerr := json.Marshal(map[string]interface{}{
				"template_id": freshTpl.ID,
				"coupon_ids":  createdIDs,
				"quantity":    qty,
				"reason":      p.Reason,
				"bulk":        true,
			})
			if jerr != nil {
				failedUserID = uid
				return fmt.Errorf("marshal audit for user %d: %w", uid, jerr)
			}
			if err := LogOperationByTx(tx, operatorID, uid, "admin", "GRANT_COUPON_BULK", c.IP(), string(details)); err != nil {
				failedUserID = uid
				return fmt.Errorf("audit for user %d: %w", uid, err)
			}
		}
		return nil
	})

	if txErr != nil {
		switch {
		case errors.Is(txErr, errCouponTemplateChanged):
			failedReason = "ERR_TEMPLATE_DISABLED"
		case errors.Is(txErr, errTargetUserChanged):
			failedReason = "ERR_USER_CHANGED"
		default:
			failedReason = truncateLog(txErr.Error(), 300)
		}
		log.Printf("[COUPON-BULK-GRANT] ABORTED admin=%d tpl=%d users=%d failed_user=%d reason=%s — full rollback",
			operatorID, tpl.ID, len(uniqueIDs), failedUserID, failedReason)
		return c.Status(409).JSON(fiber.Map{
			"success":        false,
			"message":        "批量发券失败：" + failedReason + "（整批已回滚，未发出任何券）",
			"message_code":   "ERR_BULK_GRANT_ABORTED",
			"failed_user_id": failedUserID,
			"reason":         failedReason,
		})
	}

	// 成功路径：所有 ops 已 commit
	results := make([]bulkGrantUserResult, 0, len(ops))
	for _, op := range ops {
		results = append(results, bulkGrantUserResult{UserID: op.UserID, Success: true, CouponIDs: op.CouponIDs})
	}
	totalGranted := len(uniqueIDs) * qty

	log.Printf("[COUPON-BULK-GRANT] OK admin=%d tpl=%d users=%d granted=%d qty/user=%d (single tx)",
		operatorID, tpl.ID, len(uniqueIDs), totalGranted, qty)

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_BULK_GRANTED",
		"summary": fiber.Map{
			"total_users":   len(uniqueIDs),
			"success_count": len(uniqueIDs),
			"failed_count":  0,
			"total_granted": totalGranted,
		},
		"results": results,
	})
}

// buildCouponFromTemplate 用 template 构造未保存的 UserCoupon（含快照 + 过期时间 + code）。
//
// fix MAJOR R23+2-B3：generateCouponCode 失败时返回错误，让调用方决定降级策略
// （admin 发券 → 500；注册自动发 → 跳过 + log warn 不阻塞注册）。
func buildCouponFromTemplate(userID uint, tpl *database.CouponTemplate, grantedBy uint, reason string) (database.UserCoupon, error) {
	now := time.Now()
	code, err := generateCouponCode(userID, tpl.ID)
	if err != nil {
		return database.UserCoupon{}, fmt.Errorf("generate code: %w", err)
	}
	uc := database.UserCoupon{
		UserID:      userID,
		TemplateID:  tpl.ID,
		Code:        code,
		Status:      "available",
		GrantedBy:   grantedBy,
		GrantReason: reason,
		GrantedAt:   now,
	}
	snapshotTemplate(&uc, tpl)
	if tpl.ValidDays > 0 {
		exp := now.AddDate(0, 0, tpl.ValidDays)
		uc.ExpiresAt = &exp
	}
	return uc, nil
}

// revokeCouponPayload 可选 reason 写审计
type revokeCouponPayload struct {
	Reason string `json:"reason"`
}

// AdminRevokeCoupon DELETE /api/admin/coupons/:id
// 撤销一张已发但未用的券（status: available → revoked）。已用券不能撤销。
//
// fix MAJOR R23+3-B8：撤销原因写审计 + 状态变更与审计同事务
func AdminRevokeCoupon(c *fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var payload revokeCouponPayload
	_ = c.BodyParser(&payload) // reason 可选，解析失败按空串处理
	// fix Minor（codex 第十六轮）：reason 长度 + 控制字符校验
	reason := strings.TrimSpace(payload.Reason)
	if runeLen := len([]rune(reason)); runeLen > grantReasonMaxLen {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("reason 长度不能超过 %d 字符（当前 %d）", grantReasonMaxLen, runeLen),
			"message_code": "ERR_REASON_TOO_LONG",
		})
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      "reason 不能包含控制字符（换行 / 制表符 / NUL / ESC 等）",
				"message_code": "ERR_REASON_CTRL_CHAR",
			})
		}
	}
	payload.Reason = reason

	operatorID := getOperatorID(c)
	var revoked database.UserCoupon
	now := time.Now()
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// fix MAJOR M3（codex 第二十轮）：DB 中 status='available' 但 expires_at < now 的券
		// 在用户视角已过期（DTO effective_status=expired），admin revoke 这类券会破坏状态机历史 ——
		// 用户看到 "expired"，DB 后来变为 "revoked"，审计/对账逻辑混乱。
		// 修复：revoke 仅允许真正可用的券（status=available 且未过期）。
		if err := tx.Where("id = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)",
			id, "available", now).First(&revoked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errCouponNotRevokable
			}
			return fmt.Errorf("lookup: %w", err)
		}
		// 条件 UPDATE 防并发抢占（同上：过期券也不能在 race 中被撤销）
		res := tx.Model(&database.UserCoupon{}).
			Where("id = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)",
				id, "available", now).
			Update("status", "revoked")
		if res.Error != nil {
			return fmt.Errorf("update: %w", res.Error)
		}
		if res.RowsAffected != 1 {
			return errCouponNotRevokable
		}
		// 审计同事务
		// fix Major（codex 第十五轮）：用 json.Marshal 保证 details 是合法 JSON
		details, jerr := json.Marshal(map[string]interface{}{
			"coupon_id": id,
			"code":      revoked.Code,
			"reason":    payload.Reason,
		})
		if jerr != nil {
			return fmt.Errorf("marshal audit: %w", jerr)
		}
		return LogOperationByTx(tx, operatorID, revoked.UserID, "admin", "REVOKE_COUPON", c.IP(), string(details))
	})
	if txErr != nil {
		if errors.Is(txErr, errCouponNotRevokable) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_COUPON_NOT_REVOKABLE"})
		}
		log.Printf("[COUPON-REVOKE] tx failed admin=%d coupon=%d: %v", operatorID, id, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_REVOKED"})
}

// errCouponNotRevokable 哨兵：券不存在 / 已用 / 已撤销 / 已过期
var errCouponNotRevokable = errors.New("coupon not revokable")

// AdminListUserCoupons GET /api/admin/users/:userId/coupons?page=1&page_size=50
//
// fix MAJOR R23+3-B10（codex 第四轮）：分页防长期累积拖慢页面。
func AdminListUserCoupons(c *fiber.Ctx) error {
	uid, err := strconv.Atoi(c.Params("userId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	// fix Minor（codex 第十五轮）：count 错误冒泡，与 MyCoupons 同处理
	var total int64
	if err := database.DB.Model(&database.UserCoupon{}).Where("user_id = ?", uid).Count(&total).Error; err != nil {
		log.Printf("[COUPON-LIST-USER] count: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	var list []database.UserCoupon
	if err := database.DB.Where("user_id = ?", uid).
		Order("id DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&list).Error; err != nil {
		log.Printf("[COUPON-LIST-USER] %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    list,
		"meta":    fiber.Map{"total": total, "page": page, "page_size": pageSize},
	})
}

// ─── user: 我的券 ─────────────────────────────────────────────────────

// MyCoupons GET /api/coupons/my?page=1&page_size=50
// 返回当前用户**所有**券（含 used / expired / revoked），前端按 status 自行分组展示。
//
// fix MAJOR R23+2-B4（codex 二轮）：用户端 DTO，**不**直接嵌入 database.UserCoupon。
// fix MAJOR R23+3-B10（codex 四轮）：加分页防长期累积。
func MyCoupons(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	// fix Minor（codex 第十五轮）：count 错误必须冒泡，不能让 total=0 + data=[]
	// 误导前端"没券"。
	var total int64
	if err := database.DB.Model(&database.UserCoupon{}).Where("user_id = ?", user.ID).Count(&total).Error; err != nil {
		log.Printf("[COUPON-MY] count: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	var list []database.UserCoupon
	if err := database.DB.Where("user_id = ?", user.ID).
		Order("id DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&list).Error; err != nil {
		log.Printf("[COUPON-MY] %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	now := time.Now()
	// 用户端 DTO：仅返回展示必需字段
	type userCouponDTO struct {
		ID                 uint       `json:"id"`
		Code               string     `json:"code"`                 // 用户可能想拷贝券码截图客服
		Status             string     `json:"status"`               // 原始 DB 状态
		EffectiveStatus    string     `json:"effective_status"`     // 含过期判定
		SnapshotName       string     `json:"snapshot_name"`        // 券名（用户看的）
		SnapshotType       string     `json:"snapshot_type"`        // fixed_price
		SnapshotValue      float64    `json:"snapshot_value"`       // 折后价（USD，给前端展示）
		SnapshotPackageIDs string     `json:"snapshot_package_ids"` // 适用范围（前端做 filter）
		GrantedAt          time.Time  `json:"granted_at"`
		ExpiresAt          *time.Time `json:"expires_at"`
		UsedAt             *time.Time `json:"used_at"`
		UsedSavingUSD      float64    `json:"used_saving_usd"` // USD（给前端展示）
	}
	out := make([]userCouponDTO, 0, len(list))
	for _, uc := range list {
		eff := uc.Status
		if uc.Status == "available" && uc.ExpiresAt != nil && now.After(*uc.ExpiresAt) {
			eff = "expired"
		}
		out = append(out, userCouponDTO{
			ID:                 uc.ID,
			Code:               uc.Code,
			Status:             uc.Status,
			EffectiveStatus:    eff,
			SnapshotName:       uc.SnapshotName,
			SnapshotType:       uc.SnapshotType,
			SnapshotValue:      database.MicroToUSD(uc.SnapshotValue),
			SnapshotPackageIDs: uc.SnapshotPackageIDs,
			GrantedAt:          uc.GrantedAt,
			ExpiresAt:          uc.ExpiresAt,
			UsedAt:             uc.UsedAt,
			UsedSavingUSD:      database.MicroToUSD(uc.UsedSavingUSD),
		})
	}
	// fix Minor（codex 第十五轮）：MyCoupons 返回 meta（total / page / page_size）让前端正确分页
	return c.JSON(fiber.Map{
		"success": true,
		"data":    out,
		"meta": fiber.Map{
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// getOperatorID 从 fiber locals 取 AdminGuard 注入的 admin 用户 ID（用于审计）。
//
// fix MAJOR R23+3-B9（codex 第四轮）：AdminGuard 现在直接 inject admin_user_id 到 locals，
// 这里直接读，不再需要重新解析 token。
// fallback：locals 没有时调 loadAdminUser（兼容直接挂载 handler 的测试场景）。
func getOperatorID(c *fiber.Ctx) uint {
	if v := c.Locals("admin_user_id"); v != nil {
		if id, ok := v.(uint); ok && id > 0 {
			return id
		}
	}
	if u := loadAdminUser(c); u != nil {
		return u.ID
	}
	return 0
}
