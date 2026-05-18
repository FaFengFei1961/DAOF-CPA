// Package controller / billing_integration_test.go
//
// 端到端账单测试：模拟"充值 → 购买套餐 → 退款"完整流程，验证账单时间线齐全。
//
// 注：API 扣费场景（api_consume_balance / api_usage_sub）走 proxy/stream.go，
// 流式扣费链路太重不在本文件覆盖；交由 helper 单测 + admin/UI 端口集成验证。
package controller

import (
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"
)

// helper：列出指定用户的全部账单（按 occurred_at 升序，便于断言时间线）
func listAllBilling(t *testing.T, userID uint) []database.BillingEntry {
	t.Helper()
	var rows []database.BillingEntry
	if err := database.DB.Where("user_id = ?", userID).
		Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list billing: %v", err)
	}
	return rows
}

// TestBilling_PurchaseSubWritesEntry 购买套餐写入 purchase_sub 一行账单
func TestBilling_PurchaseSubWritesEntry(t *testing.T) {
	setupSubTestDB(t)

	user := seedTestUser(t, 100.0)
	pkg := seedPackage(t)
	app := newTestApp(user)

	code, _ := doJSON(t, app, "POST", "/purchase",
		map[string]any{"package_id": pkg.ID, "quantity": 1})
	if code != 200 {
		t.Fatalf("purchase failed: %d", code)
	}

	rows := listAllBilling(t, user.ID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 billing entry, got %d", len(rows))
	}
	r := rows[0]
	if r.EntryType != database.BillingTypePurchaseSub {
		t.Errorf("entry type = %s, want %s", r.EntryType, database.BillingTypePurchaseSub)
	}
	if r.AmountUSD != -9_900_000 {
		t.Errorf("amount_usd = %d, want -9_900_000 (= -$9.90 micro_usd)", r.AmountUSD)
	}
	if r.BalanceAfterUSD != 100*database.MicroPerUSD-9_900_000 {
		t.Errorf("balance_after = %d, want %d", r.BalanceAfterUSD, 100*database.MicroPerUSD-9_900_000)
	}
	if r.RelatedType != "subscription" || r.RelatedID == 0 {
		t.Errorf("related_type/id = %s/%d, want subscription/<sub.ID>", r.RelatedType, r.RelatedID)
	}
}

func TestBilling_PurchaseSubWithCouponUsesNetPaidAmount(t *testing.T) {
	setupSubTestDB(t)

	referrer := database.User{
		Username: "coupon-referrer",
		Token:    "sk-coupon-referrer",
		Role:     "user",
		Status:   1,
	}
	if err := database.DB.Create(&referrer).Error; err != nil {
		t.Fatalf("seed referrer: %v", err)
	}
	user := seedTestUser(t, 20)
	if err := database.DB.Model(&database.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"paid_quota":          20 * database.MicroPerUSD,
		"referred_by_user_id": referrer.ID,
		"referred_at":         time.Now().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("mark referred user: %v", err)
	}

	proxy.SysConfigMutex.Lock()
	oldBPS, hadOldBPS := proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey]
	oldWindow, hadOldWindow := proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey]
	proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = "1000" // 10%
	proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = "2592000"
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		defer proxy.SysConfigMutex.Unlock()
		if hadOldBPS {
			proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = oldBPS
		} else {
			delete(proxy.SysConfigCache, database.ReferralPaidSpendRewardBPSConfigKey)
		}
		if hadOldWindow {
			proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = oldWindow
		} else {
			delete(proxy.SysConfigCache, database.ReferralPaidSpendRewardWindowSecondsConfigKey)
		}
	})

	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
		p.CostFloorMicroUSD = 3 * database.MicroPerUSD
	})
	coupon := database.UserCoupon{
		UserID: user.ID, Code: "CP-net-paid", Status: "available",
		SnapshotType: "fixed_price", SnapshotValue: 3 * database.MicroPerUSD,
		SnapshotPackageIDs: "[" + itoaUint(pkg.ID) + "]",
	}
	if err := database.DB.Create(&coupon).Error; err != nil {
		t.Fatalf("seed coupon: %v", err)
	}

	app := newTestApp(user)
	code, resp := doJSON(t, app, "POST", "/purchase",
		map[string]any{"package_id": pkg.ID, "quantity": 1, "coupon_id": coupon.ID})
	if code != 200 {
		t.Fatalf("purchase with coupon failed: %d body=%v", code, resp)
	}

	var purchase database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypePurchaseSub).First(&purchase).Error; err != nil {
		t.Fatalf("purchase billing missing: %v", err)
	}
	if purchase.AmountUSD != -3*database.MicroPerUSD {
		t.Fatalf("purchase amount=%d, want net paid -$3", purchase.AmountUSD)
	}
	if purchase.BalanceAfterUSD != 17*database.MicroPerUSD {
		t.Fatalf("balance_after=%d, want $17", purchase.BalanceAfterUSD)
	}

	var sub database.UserSubscription
	if err := database.DB.Where("user_id = ?", user.ID).First(&sub).Error; err != nil {
		t.Fatalf("subscription missing: %v", err)
	}
	if sub.PurchasedUnitPriceUSD != 3*database.MicroPerUSD || sub.AppliedCouponID != coupon.ID {
		t.Fatalf("subscription price/coupon=(%d,%d), want (3 USD,%d)", sub.PurchasedUnitPriceUSD, sub.AppliedCouponID, coupon.ID)
	}

	var freshCoupon database.UserCoupon
	if err := database.DB.First(&freshCoupon, coupon.ID).Error; err != nil {
		t.Fatalf("coupon missing: %v", err)
	}
	if freshCoupon.Status != "used" || freshCoupon.UsedSavingUSD != 7*database.MicroPerUSD || freshCoupon.UsedOnSubID == nil || *freshCoupon.UsedOnSubID != sub.ID {
		t.Fatalf("coupon usage not recorded correctly: %+v", freshCoupon)
	}

	var freshUser database.User
	if err := database.DB.First(&freshUser, user.ID).Error; err != nil {
		t.Fatalf("user missing: %v", err)
	}
	if freshUser.Quota != 17*database.MicroPerUSD || freshUser.PaidQuota != 17*database.MicroPerUSD {
		t.Fatalf("user quota/paid_quota=(%d,%d), want ($17,$17)", freshUser.Quota, freshUser.PaidQuota)
	}

	var reward database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?", referrer.ID, database.BillingTypeBonusCredit).First(&reward).Error; err != nil {
		t.Fatalf("referral reward missing: %v", err)
	}
	if reward.AmountUSD != 300_000 {
		t.Fatalf("reward amount=%d, want 10%% of net paid $3 = $0.30", reward.AmountUSD)
	}
}

// TestBilling_PurchaseQuantityWritesEntryPerSubscription 叠加购买每份写一行 purchase 账单，不写奖励入账。
func TestBilling_PurchaseQuantityWritesEntryPerSubscription(t *testing.T) {
	setupSubTestDB(t)

	user := seedTestUser(t, 100)
	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
	})
	app := newTestApp(user)

	code, _ := doJSON(t, app, "POST", "/purchase",
		map[string]any{"package_id": pkg.ID, "quantity": 2})
	if code != 200 {
		t.Fatalf("purchase: %d", code)
	}

	rows := listAllBilling(t, user.ID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 purchase entries, got %d", len(rows))
	}
	for _, r := range rows {
		if r.EntryType != database.BillingTypePurchaseSub {
			t.Errorf("entry type = %s, want purchase_sub", r.EntryType)
		}
		if r.AmountUSD != -10*database.MicroPerUSD {
			t.Errorf("purchase amount = %d, want -10*MicroPerUSD", r.AmountUSD)
		}
	}
	var bonusCount int64
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", user.ID, database.BillingTypeBonusCredit).
		Count(&bonusCount)
	if bonusCount != 0 {
		t.Errorf("purchase should not write bonus_credit entries, got %d", bonusCount)
	}
}

func TestBilling_PurchaseSubRewardsReferrerForPaidQuotaWithinWindow(t *testing.T) {
	setupSubTestDB(t)

	referrer := database.User{
		Username: "purchase-referrer",
		Token:    "sk-purchase-referrer",
		Role:     "user",
		Status:   1,
	}
	if err := database.DB.Create(&referrer).Error; err != nil {
		t.Fatalf("seed referrer: %v", err)
	}
	user := seedTestUser(t, 20)
	referredAt := time.Now().Add(-time.Hour)
	if err := database.DB.Model(&database.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"paid_quota":          20 * database.MicroPerUSD,
		"referred_by_user_id": referrer.ID,
		"referred_at":         referredAt,
	}).Error; err != nil {
		t.Fatalf("mark referred user: %v", err)
	}
	proxy.SysConfigMutex.Lock()
	oldBPS, hadOldBPS := proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey]
	oldWindow, hadOldWindow := proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey]
	proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = "1000" // 10%
	proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = "2592000"
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		defer proxy.SysConfigMutex.Unlock()
		if hadOldBPS {
			proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = oldBPS
		} else {
			delete(proxy.SysConfigCache, database.ReferralPaidSpendRewardBPSConfigKey)
		}
		if hadOldWindow {
			proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = oldWindow
		} else {
			delete(proxy.SysConfigCache, database.ReferralPaidSpendRewardWindowSecondsConfigKey)
		}
	})

	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
	})
	app := newTestApp(user)

	code, _ := doJSON(t, app, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 1})
	if code != 200 {
		t.Fatalf("purchase failed: %d", code)
	}

	var freshUser, freshReferrer database.User
	if err := database.DB.First(&freshUser, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if freshUser.PaidQuota != 10*database.MicroPerUSD {
		t.Fatalf("paid_quota=%d, want %d", freshUser.PaidQuota, 10*database.MicroPerUSD)
	}
	if err := database.DB.First(&freshReferrer, referrer.ID).Error; err != nil {
		t.Fatalf("load referrer: %v", err)
	}
	if freshReferrer.Quota != database.MicroPerUSD {
		t.Fatalf("referrer quota=%d, want %d", freshReferrer.Quota, database.MicroPerUSD)
	}

	var reward database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?", referrer.ID, database.BillingTypeBonusCredit).First(&reward).Error; err != nil {
		t.Fatalf("reward billing missing: %v", err)
	}
	if reward.AmountUSD != database.MicroPerUSD || reward.RelatedType != "subscription" || reward.RelatedID == 0 {
		t.Fatalf("unexpected reward billing: %+v", reward)
	}

	var sub database.UserSubscription
	if err := database.DB.Where("user_id = ?", user.ID).First(&sub).Error; err != nil {
		t.Fatalf("load purchased sub: %v", err)
	}
	admin := seedAdminUser(t)
	adminApp := newAdminTestApp(admin)
	code, resp := doJSON(t, adminApp, "POST",
		"/admin/sub/"+itoaUint(sub.ID)+"/refund",
		map[string]any{"amount_micro_usd": 10 * database.MicroPerUSD, "reason": "人工审核退款"})
	if code != 200 {
		t.Fatalf("refund after referral purchase failed: %d body=%v", code, resp)
	}
	if err := database.DB.First(&freshReferrer, referrer.ID).Error; err != nil {
		t.Fatalf("reload referrer after refund: %v", err)
	}
	if freshReferrer.Quota != database.MicroPerUSD {
		t.Fatalf("referrer quota after refund=%d, want reward preserved %d", freshReferrer.Quota, database.MicroPerUSD)
	}
	var rewardCount int64
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", referrer.ID, database.BillingTypeBonusCredit).
		Count(&rewardCount)
	if rewardCount != 1 {
		t.Fatalf("refund should not claw back referral reward entries, got %d bonus entries", rewardCount)
	}
}

func TestBilling_PurchaseSubDoesNotRewardBonusSpend(t *testing.T) {
	setupSubTestDB(t)

	referrer := database.User{
		Username: "bonus-first-referrer",
		Token:    "sk-bonus-first-referrer",
		Role:     "user",
		Status:   1,
	}
	if err := database.DB.Create(&referrer).Error; err != nil {
		t.Fatalf("seed referrer: %v", err)
	}
	user := seedTestUser(t, 20)
	if err := database.DB.Model(&database.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"paid_quota":          10 * database.MicroPerUSD,
		"referred_by_user_id": referrer.ID,
		"referred_at":         time.Now().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("mark referred user: %v", err)
	}
	proxy.SysConfigMutex.Lock()
	oldBPS, hadOldBPS := proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey]
	oldWindow, hadOldWindow := proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey]
	proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = "1000"
	proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = "2592000"
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		defer proxy.SysConfigMutex.Unlock()
		if hadOldBPS {
			proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = oldBPS
		} else {
			delete(proxy.SysConfigCache, database.ReferralPaidSpendRewardBPSConfigKey)
		}
		if hadOldWindow {
			proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = oldWindow
		} else {
			delete(proxy.SysConfigCache, database.ReferralPaidSpendRewardWindowSecondsConfigKey)
		}
	})

	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
	})
	app := newTestApp(user)
	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 1})
	if code != 200 {
		t.Fatalf("purchase failed: %d body=%v", code, resp)
	}

	var freshUser, freshReferrer database.User
	if err := database.DB.First(&freshUser, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if freshUser.Quota != 10*database.MicroPerUSD || freshUser.PaidQuota != 10*database.MicroPerUSD {
		t.Fatalf("quota/paid_quota=%d/%d, want $10/$10 (bonus consumed first)", freshUser.Quota, freshUser.PaidQuota)
	}
	if err := database.DB.First(&freshReferrer, referrer.ID).Error; err != nil {
		t.Fatalf("load referrer: %v", err)
	}
	if freshReferrer.Quota != 0 {
		t.Fatalf("referrer quota=%d, want 0", freshReferrer.Quota)
	}
	var rewardCount int64
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", referrer.ID, database.BillingTypeBonusCredit).
		Count(&rewardCount)
	if rewardCount != 0 {
		t.Fatalf("bonus-layer spend should not reward, got %d reward entries", rewardCount)
	}
}

func TestBilling_PurchaseQuantityRewardsFullPaidOrder(t *testing.T) {
	setupSubTestDB(t)

	referrer := database.User{
		Username: "quantity-referrer",
		Token:    "sk-quantity-referrer",
		Role:     "user",
		Status:   1,
	}
	if err := database.DB.Create(&referrer).Error; err != nil {
		t.Fatalf("seed referrer: %v", err)
	}
	user := seedTestUser(t, 20)
	if err := database.DB.Model(&database.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"paid_quota":          20 * database.MicroPerUSD,
		"referred_by_user_id": referrer.ID,
		"referred_at":         time.Now().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("mark referred user: %v", err)
	}
	proxy.SysConfigMutex.Lock()
	oldBPS, hadOldBPS := proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey]
	oldWindow, hadOldWindow := proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey]
	proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = "1000"
	proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = "2592000"
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		defer proxy.SysConfigMutex.Unlock()
		if hadOldBPS {
			proxy.SysConfigCache[database.ReferralPaidSpendRewardBPSConfigKey] = oldBPS
		} else {
			delete(proxy.SysConfigCache, database.ReferralPaidSpendRewardBPSConfigKey)
		}
		if hadOldWindow {
			proxy.SysConfigCache[database.ReferralPaidSpendRewardWindowSecondsConfigKey] = oldWindow
		} else {
			delete(proxy.SysConfigCache, database.ReferralPaidSpendRewardWindowSecondsConfigKey)
		}
	})

	pkg := seedPackage(t, func(p *database.Package) {
		p.PriceAmount = 10 * database.MicroPerUSD
		p.MaxActivePerUser = 2
	})
	app := newTestApp(user)
	code, resp := doJSON(t, app, "POST", "/purchase", map[string]any{"package_id": pkg.ID, "quantity": 2})
	if code != 200 {
		t.Fatalf("quantity purchase failed: %d body=%v", code, resp)
	}

	var freshUser, freshReferrer database.User
	if err := database.DB.First(&freshUser, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if freshUser.PaidQuota != 0 {
		t.Fatalf("paid_quota=%d, want 0 after full paid order", freshUser.PaidQuota)
	}
	if err := database.DB.First(&freshReferrer, referrer.ID).Error; err != nil {
		t.Fatalf("load referrer: %v", err)
	}
	if freshReferrer.Quota != 2*database.MicroPerUSD {
		t.Fatalf("referrer quota=%d, want $2 reward", freshReferrer.Quota)
	}
	var rewardCount int64
	database.DB.Model(&database.BillingEntry{}).
		Where("user_id = ? AND entry_type = ?", referrer.ID, database.BillingTypeBonusCredit).
		Count(&rewardCount)
	if rewardCount != 1 {
		t.Fatalf("quantity order should write one reward entry, got %d", rewardCount)
	}
}

// TestBilling_AdminRefundSubWritesEntry admin 退款写入 refund_sub 账单
func TestBilling_AdminRefundSubWritesEntry(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100.0)
	app := newAdminTestApp(admin)

	// 准备一个待退款订阅
	// snapshot 里 price_amount 是 micro_usd（9_900_000 = $9.90）
	sub := database.UserSubscription{
		UserID: user.ID, PackageID: 1, Status: "active",
		StartAt:               time.Now(),
		EndAt:                 time.Now().Add(30 * 24 * time.Hour),
		PackageSnapshot:       `{"package_id":1,"package_name":"TestPro","price_amount":9900000}`,
		PurchasedUnitPriceUSD: 9_900_000, // $9.90 micro_usd
	}
	database.DB.Create(&sub)

	code, _ := doJSON(t, app, "POST",
		"/admin/sub/"+itoaUint(sub.ID)+"/refund",
		map[string]any{"amount_micro_usd": 8 * database.MicroPerUSD, "reason": "用户协商"})
	if code != 200 {
		t.Fatalf("refund failed: %d", code)
	}

	rows := listAllBilling(t, user.ID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(rows))
	}
	r := rows[0]
	if r.EntryType != database.BillingTypeRefundSub {
		t.Errorf("type = %s, want refund_sub", r.EntryType)
	}
	if r.AmountUSD != 8*database.MicroPerUSD {
		t.Errorf("amount = %d, want 8*MicroPerUSD", r.AmountUSD)
	}
	if r.SourceSubscriptionID == nil || *r.SourceSubscriptionID != sub.ID {
		t.Errorf("source_subscription_id = %v, want %d", r.SourceSubscriptionID, sub.ID)
	}
}

// TestBilling_PurchaseRollbackLeavesNoBillingEntry 购买事务因余额不足回滚 → 不留账单垃圾
//
// 关键不变量：账单写入与业务操作原子绑定，业务回滚 → 账单也回滚。
func TestBilling_PurchaseRollbackLeavesNoBillingEntry(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 1) // 余额 $1，不够买 $9.9 套餐
	pkg := seedPackage(t)
	app := newTestApp(user)

	code, _ := doJSON(t, app, "POST", "/purchase",
		map[string]any{"package_id": pkg.ID, "quantity": 1})
	if code != 402 {
		t.Fatalf("expected 402 (insufficient balance), got %d", code)
	}

	rows := listAllBilling(t, user.ID)
	if len(rows) != 0 {
		t.Errorf("expected 0 entries after rollback, got %d: %+v", len(rows), rows)
	}
}

// TestBilling_FullFlow_PurchaseThenRefund 充值→购买→退款 时间线完整
func TestBilling_FullFlow_PurchaseThenRefund(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 100)
	pkg := seedPackage(t)

	// 1) 用户购买
	userApp := newTestApp(user)
	if code, _ := doJSON(t, userApp, "POST", "/purchase",
		map[string]any{"package_id": pkg.ID, "quantity": 1}); code != 200 {
		t.Fatalf("purchase: %d", code)
	}
	var sub database.UserSubscription
	database.DB.Where("user_id = ?", user.ID).First(&sub)

	// 2) admin 退款
	adminApp := newAdminTestApp(admin)
	if code, _ := doJSON(t, adminApp, "POST",
		"/admin/sub/"+itoaUint(sub.ID)+"/refund",
		map[string]any{"amount_micro_usd": 5 * database.MicroPerUSD, "reason": "测试退款"}); code != 200 {
		t.Fatalf("refund: %d", code)
	}

	// 3) 时间线断言：purchase_sub → refund_sub
	rows := listAllBilling(t, user.ID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(rows))
	}
	if rows[0].EntryType != database.BillingTypePurchaseSub {
		t.Errorf("[0].type = %s, want purchase_sub", rows[0].EntryType)
	}
	if rows[1].EntryType != database.BillingTypeRefundSub {
		t.Errorf("[1].type = %s, want refund_sub", rows[1].EntryType)
	}
	// 净收支：-9.9 + 5.0 = -4.9 USD（micro_usd 整数算术）
	var netMicro int64
	for _, r := range rows {
		netMicro += r.AmountUSD
	}
	wantNet := int64(-9_900_000) + int64(5_000_000)
	if netMicro != wantNet {
		t.Errorf("net = %d, want %d", netMicro, wantNet)
	}

	// 4) 当前余额（refresh from DB）
	var fresh database.User
	database.DB.First(&fresh, user.ID)
	wantBalance := int64(100*database.MicroPerUSD) - 9_900_000 + 5_000_000
	if fresh.Quota != wantBalance {
		t.Errorf("balance = %d, want %d", fresh.Quota, wantBalance)
	}
}
