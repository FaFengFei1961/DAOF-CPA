// Package database / append_only_tables_test.go
//
// fix HIGH（codex audit-integrity）：补全所有 INSERT-only 审计表的反篡改测试。
// 原仅 ApiLog / OperationLog / BillingEntry.CreatedAt 有部分覆盖；本文件补全：
//   - ApiLogAttribution / ApiLogRevenue：BeforeUpdate / BeforeDelete return Err
//   - ApiLogCostEstimate：BeforeUpdate / BeforeDelete return Err (idempotent backfill 走 raw SQL 例外)
//   - BillingEntry / TopupRefund / BillingReconciliation / PaymentWebhookReceipt：
//     Updates(map) / Save 应被 <-:create 列标签拒（虽然 GORM 不直接报错，但字段不更新）
//
// 这些是 INSERT-only 契约的兜底测试，防止未来重构无意中允许 update/delete。
package database

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openInMemoryDBWithModels(t *testing.T, dsn string, models ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(models...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	prev := DB
	DB = db
	t.Cleanup(func() { DB = prev })
	return db
}

// ─── ApiLogAttribution ──────────────────────────────────────────────────────

func TestApiLogAttribution_NoUpdate(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:apilog_attr_no_update?mode=memory&cache=shared",
		&ApiLog{}, &ApiLogAttribution{})

	apiLog := ApiLog{UserID: 1, ModelName: "x", Status: 200, CreatedAt: time.Now()}
	if err := db.Create(&apiLog).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	row := ApiLogAttribution{ApiLogID: apiLog.ID, UpstreamProvider: "p", UpstreamAccountAuthIndex: "a"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed attribution: %v", err)
	}

	res := db.Model(&ApiLogAttribution{}).Where("id = ?", row.ID).Update("upstream_provider", "tampered")
	if !errors.Is(res.Error, ErrApiLogAppendOnly) {
		t.Fatalf("update should return ErrApiLogAppendOnly, got %v", res.Error)
	}
	if res.RowsAffected != 0 {
		t.Fatalf("rows=%d want 0", res.RowsAffected)
	}
}

func TestApiLogAttribution_NoDelete(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:apilog_attr_no_delete?mode=memory&cache=shared",
		&ApiLog{}, &ApiLogAttribution{})

	apiLog := ApiLog{UserID: 1, ModelName: "x", Status: 200, CreatedAt: time.Now()}
	if err := db.Create(&apiLog).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	row := ApiLogAttribution{ApiLogID: apiLog.ID, UpstreamProvider: "p", UpstreamAccountAuthIndex: "a"}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed attribution: %v", err)
	}

	res := db.Where("id = ?", row.ID).Delete(&ApiLogAttribution{})
	if !errors.Is(res.Error, ErrApiLogAppendOnly) {
		t.Fatalf("delete should return ErrApiLogAppendOnly, got %v", res.Error)
	}
}

// ─── ApiLogCostEstimate ─────────────────────────────────────────────────────

func TestApiLogCostEstimate_NoUpdate_ViaGORM(t *testing.T) {
	// 注意：raw SQL `INSERT ON CONFLICT DO UPDATE` 是 idempotent backfill 例外（见 schema 注释）。
	// 此测试仅守 GORM 路径的 INSERT-only 契约。
	db := openInMemoryDBWithModels(t, "file:apilog_cost_no_update?mode=memory&cache=shared",
		&ApiLog{}, &ApiLogCostEstimate{})

	apiLog := ApiLog{UserID: 1, ModelName: "x", Status: 200, CreatedAt: time.Now()}
	if err := db.Create(&apiLog).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	row := ApiLogCostEstimate{ApiLogID: apiLog.ID, PlatformCostMicroUSD: 100, ComputedAt: time.Now()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed estimate: %v", err)
	}

	res := db.Model(&ApiLogCostEstimate{}).Where("id = ?", row.ID).Update("platform_cost_micro_usd", int64(999))
	if !errors.Is(res.Error, ErrApiLogAppendOnly) {
		t.Fatalf("update should return ErrApiLogAppendOnly, got %v", res.Error)
	}
}

func TestApiLogCostEstimate_NoDelete(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:apilog_cost_no_delete?mode=memory&cache=shared",
		&ApiLog{}, &ApiLogCostEstimate{})

	apiLog := ApiLog{UserID: 1, ModelName: "x", Status: 200, CreatedAt: time.Now()}
	if err := db.Create(&apiLog).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	row := ApiLogCostEstimate{ApiLogID: apiLog.ID, PlatformCostMicroUSD: 100, ComputedAt: time.Now()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed estimate: %v", err)
	}

	res := db.Where("id = ?", row.ID).Delete(&ApiLogCostEstimate{})
	if !errors.Is(res.Error, ErrApiLogAppendOnly) {
		t.Fatalf("delete should return ErrApiLogAppendOnly, got %v", res.Error)
	}
}

// ─── ApiLogRevenue ──────────────────────────────────────────────────────────

func TestApiLogRevenue_NoUpdate(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:apilog_rev_no_update?mode=memory&cache=shared",
		&ApiLog{}, &ApiLogRevenue{})

	apiLog := ApiLog{UserID: 1, ModelName: "x", Status: 200, CreatedAt: time.Now()}
	if err := db.Create(&apiLog).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	row := ApiLogRevenue{
		ApiLogID:                 apiLog.ID,
		RevenueSource:            RevenueSourceSubscription,
		EffectiveRevenueMicroUSD: 100,
		RecordedAt:               time.Now(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed revenue: %v", err)
	}

	res := db.Model(&ApiLogRevenue{}).Where("id = ?", row.ID).Update("effective_revenue_micro_usd", int64(999))
	if !errors.Is(res.Error, ErrApiLogAppendOnly) {
		t.Fatalf("update should return ErrApiLogAppendOnly, got %v", res.Error)
	}
}

func TestApiLogRevenue_NoDelete(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:apilog_rev_no_delete?mode=memory&cache=shared",
		&ApiLog{}, &ApiLogRevenue{})

	apiLog := ApiLog{UserID: 1, ModelName: "x", Status: 200, CreatedAt: time.Now()}
	if err := db.Create(&apiLog).Error; err != nil {
		t.Fatalf("seed api log: %v", err)
	}
	row := ApiLogRevenue{
		ApiLogID:                 apiLog.ID,
		RevenueSource:            RevenueSourceSubscription,
		EffectiveRevenueMicroUSD: 100,
		RecordedAt:               time.Now(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed revenue: %v", err)
	}

	res := db.Where("id = ?", row.ID).Delete(&ApiLogRevenue{})
	if !errors.Is(res.Error, ErrApiLogAppendOnly) {
		t.Fatalf("delete should return ErrApiLogAppendOnly, got %v", res.Error)
	}
}

// ─── BillingEntry：<-:create 列标签防 Update（GORM 静默忽略字段） ─────────────

func TestBillingEntry_UpdatesMapIgnoredOnCreateOnlyColumns(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:billing_entry_immutable?mode=memory&cache=shared",
		&BillingEntry{})

	entry := BillingEntry{
		UserID:      1,
		EntryType:   BillingTypeAdminAdjust,
		AmountUSD:   100,
		Description: "original",
		OccurredAt:  time.Now(),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed entry: %v", err)
	}

	// 用 Updates(map) 试图改 amount_usd / description（都标了 <-:create）
	if err := db.Model(&BillingEntry{}).Where("id = ?", entry.ID).Updates(map[string]any{
		"amount_usd":  int64(999),
		"description": "tampered",
	}).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	var reloaded BillingEntry
	if err := db.First(&reloaded, entry.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.AmountUSD != 100 {
		t.Fatalf("amount_usd changed to %d (must be immutable via GORM)", reloaded.AmountUSD)
	}
	if reloaded.Description != "original" {
		t.Fatalf("description changed to %q (must be immutable via GORM)", reloaded.Description)
	}
}

// ─── TopupRefund：<-:create 列标签防 Update ─────────────────────────────────

func TestTopupRefund_UpdatesMapIgnoredOnCreateOnlyColumns(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:topup_refund_immutable?mode=memory&cache=shared",
		&TopupOrder{}, &TopupRefund{})

	order := TopupOrder{
		UserID:                      1,
		OutTradeNo:                  "test_order",
		PayType:                     "alipay",
		MoneyRMB:                    7200,
		AmountUSD:                   1_000_000,
		ExchangeRateRmbPerUsdMicros: 7_200_000,
		Status:                      "refunded",
		CreatedAt:                   time.Now(),
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	refund := TopupRefund{
		TopupOrderID:      order.ID,
		ExternalRefundRef: "ref_immutable_test",
		AmountFen:         7200,
		AmountMicroUSD:    1_000_000,
	}
	if err := db.Create(&refund).Error; err != nil {
		t.Fatalf("seed refund: %v", err)
	}

	if err := db.Model(&TopupRefund{}).Where("id = ?", refund.ID).Updates(map[string]any{
		"amount_fen":       int64(99999),
		"amount_micro_usd": int64(99999),
	}).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	var reloaded TopupRefund
	if err := db.First(&reloaded, refund.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.AmountFen != 7200 {
		t.Fatalf("amount_fen changed to %d (must be immutable via GORM)", reloaded.AmountFen)
	}
	if reloaded.AmountMicroUSD != 1_000_000 {
		t.Fatalf("amount_micro_usd changed to %d (must be immutable via GORM)", reloaded.AmountMicroUSD)
	}
}

// ─── BillingReconciliation：<-:create 列标签防 Update ───────────────────────

func TestBillingReconciliation_UpdatesMapIgnoredOnCreateOnlyColumns(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:billing_reconcile_immutable?mode=memory&cache=shared",
		&BillingEntry{}, &BillingReconciliation{})

	entry := BillingEntry{
		UserID:      1,
		EntryType:   BillingTypeAdminAdjust,
		AmountUSD:   100,
		Description: "x",
		OccurredAt:  time.Now(),
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("seed entry: %v", err)
	}
	rec := BillingReconciliation{
		BillingEntryID: entry.ID,
		Result:         "charged",
		OperatorID:     1,
		OperatorRole:   "admin",
		Note:           "original",
	}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatalf("seed reconciliation: %v", err)
	}

	if err := db.Model(&BillingReconciliation{}).Where("id = ?", rec.ID).Updates(map[string]any{
		"result": "voided",
		"note":   "tampered",
	}).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	var reloaded BillingReconciliation
	if err := db.First(&reloaded, rec.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Result != "charged" {
		t.Fatalf("result changed to %q (must be immutable via GORM)", reloaded.Result)
	}
	if reloaded.Note != "original" {
		t.Fatalf("note changed to %q (must be immutable via GORM)", reloaded.Note)
	}
}

// ─── PaymentWebhookReceipt：<-:create 列标签防 Update ────────────────────────

func TestPaymentWebhookReceipt_UpdatesMapIgnoredOnCreateOnlyColumns(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:payment_webhook_immutable?mode=memory&cache=shared",
		&PaymentWebhookReceipt{})

	receipt := PaymentWebhookReceipt{
		Provider:      "yifut",
		Nonce:         "test_nonce_immutable",
		SignatureHash: "abcdef0123456789",
		OutTradeNo:    "test_order",
	}
	if err := db.Create(&receipt).Error; err != nil {
		t.Fatalf("seed receipt: %v", err)
	}

	if err := db.Model(&PaymentWebhookReceipt{}).Where("id = ?", receipt.ID).Updates(map[string]any{
		"signature_hash": "tampered_hash",
		"nonce":          "tampered_nonce",
	}).Error; err != nil {
		t.Fatalf("update: %v", err)
	}
	var reloaded PaymentWebhookReceipt
	if err := db.First(&reloaded, receipt.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.SignatureHash != "abcdef0123456789" {
		t.Fatalf("signature_hash changed to %q (must be immutable via GORM)", reloaded.SignatureHash)
	}
	if reloaded.Nonce != "test_nonce_immutable" {
		t.Fatalf("nonce changed to %q (must be immutable via GORM)", reloaded.Nonce)
	}
}
