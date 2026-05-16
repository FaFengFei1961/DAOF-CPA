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
