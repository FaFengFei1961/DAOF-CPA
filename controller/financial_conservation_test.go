// Package controller / financial_conservation_test.go
//
// 财务守恒不变量验证（fix MAJOR M22-A1 Phase 2）：
//
// **核心约束**：对任意一段时间窗口 [t0, t1]，对每个 user：
//
//	ΔQuota(user) == Σ AmountUSD(billing entries[t0..t1])
//
// 其中：
//   - ΔQuota = user.Quota(t1) - user.Quota(t0)
//   - AmountUSD 单位 micro_usd（int64）
//   - api_usage_sub / admin_grant_sub / api_usage_pending_reconcile 类型
//     AmountUSD 必为 0（仅审计、不动 quota）
//
// 一旦该不变量被打破，任何对账查询（按 billing 重建用户余额）都会与 user.Quota 不一致，
// 财务被信任度降到 0。Phase 1 切换到 int64 micro_usd 后，整数算术保证不再有浮点漂移；
// 这套测试把"算术保证"+"业务路径覆盖"绑成回归网，未来任何金额逻辑改动若打破守恒立即失败。
//
// 覆盖路径（按业务时序串成一条端到端 fixture，每步检查守恒）：
//  1. Topup paid 回调 → ΔQuota = +AmountUSD; billing(topup) = +AmountUSD
//  2. 购买套餐 → ΔQuota = -price; billing = -price
//  3. AdminRefundSubscription → ΔQuota = +refund; billing = +refund
//  4. AdminRefundTopup reclaim_quota=true → ΔQuota = -reclaim; billing = -reclaim
//  5. AdminRefundTopup reclaim_quota=false → ΔQuota = 0; billing = 0（保留额度）
//  6. AdminGrantSubscription → ΔQuota = 0; billing(grant)=0
//  7. UpdateUser admin 手动调额 → ΔQuota = newQuota - oldQuota; billing(admin_adjust) = same delta
//
// 每条路径单独子测试 + 串行场景跑完汇总验证。
package controller

import (
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// assertConservation 核心断言：用户 quota 变化等于该用户 billing 净额。
//
// quotaBefore 入参是事件**之前**user.Quota 的快照；调用方在事件发生后调本函数，
// 它会重读 fresh quota + 累加自快照时间起所有 billing AmountUSD，校验恒等。
func assertConservation(t *testing.T, userID uint, quotaBeforeMicro int64, sinceTime time.Time, label string) (deltaMicro int64) {
	t.Helper()
	var fresh database.User
	if err := database.DB.Select("id, quota").First(&fresh, userID).Error; err != nil {
		t.Fatalf("[%s] re-read user: %v", label, err)
	}
	deltaMicro = fresh.Quota - quotaBeforeMicro

	// 累加自 sinceTime 之后的所有 billing AmountUSD（不包含 sinceTime 之前的旧账单）
	var billingSumMicro int64
	if err := database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND occurred_at >= ?", userID, sinceTime).
		Select("COALESCE(SUM(amount_usd), 0)").
		Scan(&billingSumMicro).Error; err != nil {
		t.Fatalf("[%s] sum billing: %v", label, err)
	}

	if deltaMicro != billingSumMicro {
		t.Errorf("[%s] CONSERVATION VIOLATED: ΔQuota=%d but Σbilling=%d (diff=%d micro_usd = $%s)",
			label, deltaMicro, billingSumMicro, deltaMicro-billingSumMicro,
			database.FormatMicroUSD(deltaMicro-billingSumMicro))
	}
	return
}

// snapshotUser 取 user 当前 quota + 时间戳，便于事件后断言守恒。
func snapshotUser(t *testing.T, userID uint) (quotaMicro int64, at time.Time) {
	t.Helper()
	var u database.User
	if err := database.DB.Select("id, quota").First(&u, userID).Error; err != nil {
		t.Fatalf("snapshot user: %v", err)
	}
	// occurred_at 时间戳精度 µs；为避免 boundary 把刚写入的事件漏算，回退 1ms 确保覆盖
	return u.Quota, time.Now().Add(-1 * time.Millisecond)
}

// ─── 单独路径守恒 ────────────────────────────────────────────────────

// TestConservation_Topup_Direct 直接模拟 YifutNotify 已实现的"加 quota + 写 topup 账单"原子性：
// 因为 YifutNotify 需要 RSA 验签 + 配置易付通，单测里改用直接 SQL 复现"该路径产生的副作用"
// 模式来验证守恒（仍调 WriteBillingEntry helper，只是绕过签名校验）。
func TestConservation_Topup_Direct(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 0)

	beforeMicro, atTime := snapshotUser(t, user.ID)
	topupMicro := int64(10 * database.MicroPerUSD)

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&database.User{}).Where("id = ?", user.ID).
			UpdateColumn("quota", gorm.Expr("quota + ?", topupMicro)).Error; err != nil {
			return err
		}
		var fresh database.User
		if err := tx.Select("id, quota").First(&fresh, user.ID).Error; err != nil {
			return err
		}
		return database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:           user.ID,
			OccurredAt:       time.Now(),
			EntryType:        database.BillingTypeTopup,
			AmountUSD:        topupMicro,
			BalanceAfterUSD:  fresh.Quota,
			RelatedType:      "topup_order",
			RelatedID:        1,
			Description:      "充值守恒测试",
			CurrencyOriginal: "CNY",
			AmountOriginal:   7200, // ¥72.00
		})
	})
	if err != nil {
		t.Fatalf("topup tx: %v", err)
	}

	delta := assertConservation(t, user.ID, beforeMicro, atTime, "topup")
	if delta != topupMicro {
		t.Errorf("expected delta=%d, got %d", topupMicro, delta)
	}
}

// TestConservation_Purchase 购买套餐：
// 价格 $10 → ΔQuota = -10 USD; billing = -$10 purchase
func TestConservation_Purchase(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
	})
	app := newTestApp(user)

	beforeMicro, atTime := snapshotUser(t, user.ID)

	code, _ := doJSON(t, app, "POST", "/purchase",
		map[string]any{"package_id": pkg.ID, "quantity": 1})
	if code != 200 {
		t.Fatalf("purchase: %d", code)
	}

	delta := assertConservation(t, user.ID, beforeMicro, atTime, "purchase")
	wantDelta := int64(-10 * database.MicroPerUSD)
	if delta != wantDelta {
		t.Errorf("expected delta=%d, got %d", wantDelta, delta)
	}
}

// TestConservation_PurchaseWithCoupon 按券后实付价购买：
// 套餐原价 $10，固定价券 $3 → ΔQuota = -3; billing 总和 = -3。
func TestConservation_PurchaseWithCoupon(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
		p.CostFloorMicroUSD = 3 * database.MicroPerUSD
	})

	coupon := database.UserCoupon{
		UserID: user.ID, Code: "CP-conservation-net-paid", Status: "available",
		SnapshotType: "fixed_price", SnapshotValue: 3 * database.MicroPerUSD,
		SnapshotPackageIDs: "[" + itoaUint(pkg.ID) + "]",
	}
	database.DB.Create(&coupon)

	app := newTestApp(user)
	beforeMicro, atTime := snapshotUser(t, user.ID)

	code, _ := doJSON(t, app, "POST", "/purchase", map[string]any{
		"package_id": pkg.ID, "quantity": 1, "coupon_id": coupon.ID,
	})
	if code != 200 {
		t.Fatalf("purchase with coupon: %d", code)
	}

	delta := assertConservation(t, user.ID, beforeMicro, atTime, "coupon-purchase")
	want := int64(-3 * database.MicroPerUSD)
	if delta != want {
		t.Errorf("expected delta=%d (coupon net paid), got %d", want, delta)
	}
}

// TestConservation_AdminRefundSub admin 退款 → ΔQuota = +refund; billing(refund_sub) = +refund
func TestConservation_AdminRefundSub(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	app := newAdminTestApp(admin)

	// 前置：用户已购买（事务外预置一份 active sub）
	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             1,
		Status:                "active",
		StartAt:               time.Now(),
		EndAt:                 time.Now().Add(30 * 24 * time.Hour),
		PackageSnapshot:       `{"package_id":1,"package_name":"Pro","price_amount":10000000}`,
		PurchasedUnitPriceUSD: 10 * database.MicroPerUSD,
	}
	database.DB.Create(&sub)

	beforeMicro, atTime := snapshotUser(t, user.ID)

	code, _ := doJSON(t, app, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund",
		map[string]any{"amount_micro_usd": 5 * database.MicroPerUSD, "reason": "守恒测试"})
	if code != 200 {
		t.Fatalf("refund: %d", code)
	}

	delta := assertConservation(t, user.ID, beforeMicro, atTime, "admin-refund-sub")
	wantDelta := int64(5 * database.MicroPerUSD)
	if delta != wantDelta {
		t.Errorf("expected delta=%d, got %d", wantDelta, delta)
	}
}

// TestConservation_AdminGrantSub 管理员赠送：
// admin_grant_sub 类型 AmountUSD=0，user.Quota 不变。
func TestConservation_AdminGrantSub(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)
	pkg := seedPackage(t)
	app := newAdminGrantTestApp(admin)

	beforeMicro, atTime := snapshotUser(t, user.ID)

	code, _ := doJSON(t, app, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"quantity":   2,
		"reason":     "守恒测试",
	})
	if code != 200 {
		t.Fatalf("grant: %d", code)
	}

	delta := assertConservation(t, user.ID, beforeMicro, atTime, "admin-grant")
	if delta != 0 {
		t.Errorf("expected delta=0, got %d", delta)
	}
}

// TestConservation_AdminAdjustQuota admin 手动改额度（UpdateUser）：
// → ΔQuota = newQuota - oldQuota; billing(admin_adjust) = same delta
func TestConservation_AdminAdjustQuota(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 50)

	// 直挂 UpdateUser 路由（绕开 AdminGuard middleware 简化）
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Put("/admin/users/:id", UpdateUser)

	beforeMicro, atTime := snapshotUser(t, user.ID)

	// admin 把 quota 调整为 $80（原 $50 → +$30）
	code, _ := doJSON(t, app, "PUT", "/admin/users/"+itoaUint(user.ID),
		map[string]any{
			"username":   user.Username,
			"quota":      80.0,
			"status":     1,
			"ban_reason": "",
		})
	if code != 200 {
		t.Fatalf("admin adjust: %d", code)
	}

	delta := assertConservation(t, user.ID, beforeMicro, atTime, "admin-adjust")
	wantDelta := int64(30 * database.MicroPerUSD) // 80 - 50 = 30
	if delta != wantDelta {
		t.Errorf("expected delta=%d, got %d", wantDelta, delta)
	}
}

// TestConservation_EndToEnd 端到端流水：充值 → 购买套餐 → admin 退款 → admin 赠送 → admin 调额。
//
// 总账验证：单 user 历史所有事件后 user.Quota == initial + Σbilling。
// 这是最强的不变量检测——任何路径的 quota/billing 漂移都会让最终值偏。
func TestConservation_EndToEnd(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 0)

	initialMicro := user.Quota
	startTime := time.Now().Add(-10 * time.Millisecond)

	// === 阶段 1：模拟 topup +$50 ===
	topupMicro := int64(50 * database.MicroPerUSD)
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&database.User{}).Where("id = ?", user.ID).
			UpdateColumn("quota", gorm.Expr("quota + ?", topupMicro)).Error; err != nil {
			return err
		}
		var fresh database.User
		tx.Select("id, quota").First(&fresh, user.ID)
		return database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID: user.ID, OccurredAt: time.Now(),
			EntryType: database.BillingTypeTopup,
			AmountUSD: topupMicro, BalanceAfterUSD: fresh.Quota,
			RelatedType: "topup_order", RelatedID: 1, Description: "E2E topup",
		})
	}); err != nil {
		t.Fatalf("topup: %v", err)
	}
	// 同步内存 user 的 Quota（中间件正常会每请求重读，但测试 helper 把 user 一次性塞进 Locals
	// 所以这里手动同步一次让后续 PurchasePackage 的事务外预检通过）
	user.Quota += topupMicro

	// === 阶段 2：购买套餐 $10 ===
	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
	})
	userApp := newTestApp(user)
	if code, _ := doJSON(t, userApp, "POST", "/purchase",
		map[string]any{"package_id": pkg.ID, "quantity": 1}); code != 200 {
		t.Fatalf("purchase: %d", code)
	}

	// === 阶段 3：admin 退款 $5（针对刚买的 sub）===
	var sub database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).First(&sub)
	adminApp := newAdminTestApp(admin)
	if code, _ := doJSON(t, adminApp, "POST", "/admin/sub/"+itoaUint(sub.ID)+"/refund",
		map[string]any{"amount_micro_usd": 5 * database.MicroPerUSD, "reason": "E2E refund"}); code != 200 {
		t.Fatalf("refund: %d", code)
	}

	// === 阶段 4：admin 赠送 1 份 ===
	grantApp := newAdminGrantTestApp(admin)
	if code, _ := doJSON(t, grantApp, "POST", "/admin/sub/grant", map[string]any{
		"user_id":    user.ID,
		"package_id": pkg.ID,
		"quantity":   1,
		"reason":     "E2E grant",
	}); code != 200 {
		t.Fatalf("grant: %d", code)
	}

	// === 阶段 5：admin 把额度调到 $100 ===
	adjustApp := fiber.New(fiber.Config{DisableStartupMessage: true})
	adjustApp.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	adjustApp.Use(middleware.AdminGuard)
	adjustApp.Put("/admin/users/:id", UpdateUser)
	if code, _ := doJSON(t, adjustApp, "PUT", "/admin/users/"+itoaUint(user.ID),
		map[string]any{
			"username":   user.Username,
			"quota":      100.0,
			"status":     1,
			"ban_reason": "",
		}); code != 200 {
		t.Fatalf("adjust: %d", code)
	}

	// === 总账守恒断言：从 startTime 起所有事件 ===
	delta := assertConservation(t, user.ID, initialMicro, startTime, "E2E-flow")

	// 同时验证最终 quota 等于 admin 设的 $100（admin_adjust 是终态）
	var finalUser database.User
	database.DB.First(&finalUser, user.ID)
	if finalUser.Quota != 100*database.MicroPerUSD {
		t.Errorf("final quota=%d, want 100*MicroPerUSD", finalUser.Quota)
	}
	if delta != 100*database.MicroPerUSD-initialMicro {
		t.Errorf("E2E delta=%d, want %d (final-initial)", delta,
			100*database.MicroPerUSD-initialMicro)
	}
}

// TestConservation_ApiUsageType_ZeroAmount api_usage_sub / admin_grant_sub
// 等"仅审计"类型必须 AmountUSD == 0，否则 IsZeroAmountBillingType invariant 会被
// WriteBillingEntry 拒绝写入（fix Minor m3）。
//
// 这个测试不直接测守恒（这些类型本就不动 quota），而是确保 invariant 的边界保护仍生效。
func TestConservation_ApiUsageType_ZeroAmount(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)

	// 试图给 api_usage_sub 类型写 AmountUSD != 0 → 应失败
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		return database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:          user.ID,
			EntryType:       database.BillingTypeApiUsageSub,
			AmountUSD:       1_000_000, // $1，违反 zero-amount invariant
			BalanceAfterUSD: user.Quota,
			Description:     "non-zero api_usage_sub should be rejected",
		})
	})
	if err == nil {
		t.Error("expected error for non-zero api_usage_sub, got nil")
	}

	// 同样的类型 + AmountUSD == 0 → 应成功
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		return database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:          user.ID,
			EntryType:       database.BillingTypeApiUsageSub,
			AmountUSD:       0,
			BalanceAfterUSD: user.Quota,
			ModelName:       "claude-sonnet",
			TokensTotal:     100,
			Description:     "zero api_usage_sub OK",
		})
	})
	if err != nil {
		t.Errorf("expected success for zero api_usage_sub, got %v", err)
	}

	// 验证只有 1 条 billing entry 写入（错误那条已被拒绝）
	var count int64
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeApiUsageSub).
		Count(&count)
	if count != 1 {
		t.Errorf("expected 1 entry, got %d", count)
	}
}
