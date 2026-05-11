// Package database / billing_helper_test.go
//
// 验证 WriteBillingEntry 的事务原子性、必填字段校验、nil-safe 行为。
package database

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupBillingTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&BillingEntry{}, &User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	DB = db
}

// TestBilling_WriteEntry_Success 基本写入路径：必填字段齐全 → 写入成功。
func TestBilling_WriteEntry_Success(t *testing.T) {
	setupBillingTestDB(t)
	err := DB.Transaction(func(tx *gorm.DB) error {
		return WriteBillingEntry(tx, BillingEntryInput{
			UserID:          1,
			EntryType:       BillingTypeTopup,
			AmountUSD:       10 * MicroPerUSD,
			BalanceAfterUSD: 10 * MicroPerUSD,
			Description:     "充值 ¥72.00",
		})
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	var entries []BillingEntry
	DB.Find(&entries)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].EntryType != BillingTypeTopup || entries[0].AmountUSD != 10*MicroPerUSD {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
	if entries[0].OccurredAt.IsZero() {
		t.Errorf("OccurredAt should default to now, got zero")
	}
}

// TestBilling_WriteEntry_RejectsEmptyRequiredFields 必填字段缺失返回错误。
func TestBilling_WriteEntry_RejectsEmptyRequiredFields(t *testing.T) {
	setupBillingTestDB(t)
	cases := []struct {
		name string
		in   BillingEntryInput
	}{
		{"missing UserID", BillingEntryInput{EntryType: BillingTypeTopup}},
		{"missing EntryType", BillingEntryInput{UserID: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := DB.Transaction(func(tx *gorm.DB) error {
				return WriteBillingEntry(tx, tc.in)
			})
			if err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

// TestBilling_WriteEntry_TransactionRollback 事务中途失败时账单也回滚（不留垃圾行）。
//
// 这是 helper 最关键的契约：账单与业务原子绑定。如果业务逻辑（如 quota+=）失败回滚，
// 账单条目也必须一起回滚——否则事实表会有"幻账"误导审计。
func TestBilling_WriteEntry_TransactionRollback(t *testing.T) {
	setupBillingTestDB(t)
	sentinel := errors.New("simulated business failure")
	err := DB.Transaction(func(tx *gorm.DB) error {
		// 先写账单
		if err := WriteBillingEntry(tx, BillingEntryInput{
			UserID:          1,
			EntryType:       BillingTypeTopup,
			AmountUSD:       50 * MicroPerUSD,
			BalanceAfterUSD: 50 * MicroPerUSD,
			Description:     "should be rolled back",
		}); err != nil {
			return err
		}
		// 模拟后续业务失败
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel, got %v", err)
	}

	// 验证账单条目被回滚
	var count int64
	DB.Model(&BillingEntry{}).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 entries after rollback, got %d", count)
	}
}

// TestBilling_WriteEntry_NilSafePtrFields *uint 和原币字段可选，不传也能写入。
func TestBilling_WriteEntry_NilSafePtrFields(t *testing.T) {
	setupBillingTestDB(t)
	err := DB.Transaction(func(tx *gorm.DB) error {
		return WriteBillingEntry(tx, BillingEntryInput{
			UserID:          2,
			EntryType:       BillingTypeApiConsumeBalance,
			AmountUSD:       -50_000,    // -$0.05 = -50_000 micro_usd
			BalanceAfterUSD: 9_950_000,  // $9.95 = 9_950_000 micro_usd
			ModelName:       "claude-sonnet",
			TokensTotal:     1500,
			Description:     "余额扣费 · claude-sonnet · 1500 tokens",
			// SourceSubscriptionID 留 nil
			// CurrencyOriginal 留空
		})
	})
	if err != nil {
		t.Fatalf("nil-safe write failed: %v", err)
	}
	var e BillingEntry
	if err := DB.First(&e).Error; err != nil {
		t.Fatalf("first: %v", err)
	}
	if e.SourceSubscriptionID != nil {
		t.Errorf("SourceSubscriptionID should remain nil, got %v", e.SourceSubscriptionID)
	}
	if e.CurrencyOriginal != "" {
		t.Errorf("CurrencyOriginal should be empty, got %q", e.CurrencyOriginal)
	}
}

// TestBilling_WriteEntry_OccurredAtRespectsCallerOverride 调用方传 OccurredAt 应被尊重（不被 now 覆盖）。
//
// 重要：YifutNotify 用 paid_at 作为 OccurredAt 反映"实际付款时刻"——
// 如果 helper 强制覆盖成 now，账单时间线就会失真。
func TestBilling_WriteEntry_OccurredAtRespectsCallerOverride(t *testing.T) {
	setupBillingTestDB(t)
	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	err := DB.Transaction(func(tx *gorm.DB) error {
		return WriteBillingEntry(tx, BillingEntryInput{
			UserID:          3,
			EntryType:       BillingTypeTopup,
			AmountUSD:       20 * MicroPerUSD,
			BalanceAfterUSD: 20 * MicroPerUSD,
			Description:     "充值",
			OccurredAt:      t1,
		})
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	var e BillingEntry
	DB.First(&e)
	if !e.OccurredAt.Equal(t1) {
		t.Errorf("OccurredAt overridden: expected %v, got %v", t1, e.OccurredAt)
	}
}

// TestBilling_EntryType_Helpers IsCreditEntry / IsConsumeEntry 分类正确性。
func TestBilling_EntryType_Helpers(t *testing.T) {
	cases := []struct {
		name      string
		entryType string
		amountUSD int64 // micro_usd
		isCredit  bool
		isConsume bool
	}{
		{"topup", BillingTypeTopup, 10 * MicroPerUSD, true, false},
		{"purchase_sub", BillingTypePurchaseSub, -10 * MicroPerUSD, false, true},
		{"purchase_addon", BillingTypePurchaseAddon, -5 * MicroPerUSD, false, true},
		{"api_consume_balance", BillingTypeApiConsumeBalance, -50_000, false, true}, // -$0.05
		{"refund_sub", BillingTypeRefundSub, 8 * MicroPerUSD, true, false},
		{"refund_topup_with_reclaim", BillingTypeRefundTopup, -10 * MicroPerUSD, false, false},
		{"bonus_credit", BillingTypeBonusCredit, 5 * MicroPerUSD, true, false},
		{"api_usage_sub_zero", BillingTypeApiUsageSub, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := BillingEntry{EntryType: tc.entryType, AmountUSD: tc.amountUSD}
			if got := e.IsCreditEntry(); got != tc.isCredit {
				t.Errorf("IsCreditEntry: got %v, want %v", got, tc.isCredit)
			}
			if got := e.IsConsumeEntry(); got != tc.isConsume {
				t.Errorf("IsConsumeEntry: got %v, want %v", got, tc.isConsume)
			}
		})
	}
}
