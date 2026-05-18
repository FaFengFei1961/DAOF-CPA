// Package controller / billing_reconcile_test.go
//
// 验证 Sprint5-M8 BillingState 状态机闭环：pending_reconcile → reconciled。
//
// 测试矩阵：
//   1. result=absorbed：reconciliation 入库，不动 quota，原 entry AmountUSD 不变
//   2. result=charged：reconciliation + admin_adjust 入库，quota -= EstimatedCost
//   3. result=voided：reconciliation 入库，不动 quota
//   4. 同一 entry 二次对账被 unique 约束拒绝 (409 ERR_RECONCILE_ALREADY_DONE)
//   5. 非 pending 状态拒绝 (400 ERR_RECONCILE_NOT_PENDING)
//   6. charged 但余额不足拒绝（事务回滚）
//   7. 入参校验：result 非法 / note 空 / note 超长
package controller

import (
	"strings"
	"testing"
	"time"

	"daof-cpa/database"
	"daof-cpa/middleware"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

func newReconcileTestApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Post("/admin/billing/:id/reconcile", AdminReconcileBillingEntry)
	return app
}

// seedPendingBillingEntry 写一条 pending_reconcile 账单（模拟 stream.go 在订阅 commit 阶段
// 失败时记录的待对账行）。
func seedPendingBillingEntry(t *testing.T, userID uint, estimatedCostMicroUSD int64) *database.BillingEntry {
	t.Helper()
	entry := database.BillingEntry{
		UserID:           userID,
		EntryType:        database.BillingTypeApiUsagePendingReconcile,
		BillingState:     database.BillingStatePendingReconcile,
		AmountUSD:        0, // pending entry 不动 quota
		BalanceAfterUSD:  0,
		ModelName:        "gpt-test",
		TokensTotal:      100,
		EstimatedCostUSD: estimatedCostMicroUSD,
		Description:      "test pending entry",
	}
	if err := database.DB.Create(&entry).Error; err != nil {
		t.Fatalf("seed pending entry: %v", err)
	}
	return &entry
}

func TestAdminReconcileBilling_AbsorbedSucceeds(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 50) // $50 USD
	app := newReconcileTestApp(admin)

	entry := seedPendingBillingEntry(t, user.ID, 5_000_000) // $5 estimated cost

	code, resp := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "absorbed", "note": "上游故障，平台吸收"})
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "SUCCESS_RECONCILED" {
		t.Errorf("expected SUCCESS_RECONCILED, got %v", resp["message_code"])
	}

	// 验证 reconciliation 行已写
	var rec database.BillingReconciliation
	if err := database.DB.Where("billing_entry_id = ?", entry.ID).First(&rec).Error; err != nil {
		t.Fatalf("reconciliation row not found: %v", err)
	}
	if rec.Result != "absorbed" {
		t.Errorf("expected result=absorbed, got %q", rec.Result)
	}
	if rec.AdjustmentBillingEntryID != 0 {
		t.Errorf("absorbed should NOT create adjustment, got id=%d", rec.AdjustmentBillingEntryID)
	}

	// 验证 quota 未变
	var u database.User
	database.DB.First(&u, user.ID)
	if u.Quota != 50*database.MicroPerUSD {
		t.Errorf("absorbed should NOT change quota: got %d want %d", u.Quota, 50*database.MicroPerUSD)
	}

	// 验证原 entry AmountUSD 未变（append-only）
	var fresh database.BillingEntry
	database.DB.First(&fresh, entry.ID)
	if fresh.AmountUSD != 0 {
		t.Errorf("original entry AmountUSD tampered: got %d want 0", fresh.AmountUSD)
	}
}

func TestAdminReconcileBilling_ChargedDeductsQuotaAndCreatesAdjustEntry(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 50)
	app := newReconcileTestApp(admin)

	entry := seedPendingBillingEntry(t, user.ID, 5_000_000) // $5

	code, resp := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "charged", "note": "已联系用户确认，补扣"})
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}

	// 验证 reconciliation 行
	var rec database.BillingReconciliation
	if err := database.DB.Where("billing_entry_id = ?", entry.ID).First(&rec).Error; err != nil {
		t.Fatalf("reconciliation row not found: %v", err)
	}
	if rec.Result != "charged" {
		t.Errorf("expected charged, got %q", rec.Result)
	}
	if rec.AdjustmentBillingEntryID == 0 {
		t.Errorf("charged must create adjustment_billing_entry")
	}

	// 验证 admin_adjust 反向账单
	var adjust database.BillingEntry
	if err := database.DB.First(&adjust, rec.AdjustmentBillingEntryID).Error; err != nil {
		t.Fatalf("adjustment entry not found: %v", err)
	}
	if adjust.EntryType != database.BillingTypeAdminAdjust {
		t.Errorf("expected admin_adjust, got %q", adjust.EntryType)
	}
	if adjust.AmountUSD != -5_000_000 {
		t.Errorf("adjust amount: got %d want -5_000_000", adjust.AmountUSD)
	}
	if adjust.RelatedID != entry.ID {
		t.Errorf("adjust related_id: got %d want %d (original pending entry)", adjust.RelatedID, entry.ID)
	}

	// 验证 quota 已扣 $5
	var u database.User
	database.DB.First(&u, user.ID)
	want := 50*database.MicroPerUSD - 5_000_000
	if u.Quota != want {
		t.Errorf("quota: got %d want %d (-$5 charged)", u.Quota, want)
	}
}

func TestAdminReconcileBilling_ChargedConsumesPaidQuotaAfterBonusAndRewards(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	referrer := database.User{Username: "reconcile-referrer", Token: "sk-reconcile-referrer", Role: "user", Status: 1}
	if err := database.DB.Create(&referrer).Error; err != nil {
		t.Fatalf("seed referrer: %v", err)
	}
	user := seedTestUser(t, 20)
	referredAt := time.Now().Add(-time.Hour)
	if err := database.DB.Model(&database.User{}).Where("id = ?", user.ID).Updates(map[string]any{
		"paid_quota":          10 * database.MicroPerUSD,
		"referred_by_user_id": referrer.ID,
		"referred_at":         referredAt,
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

	app := newReconcileTestApp(admin)
	entry := seedPendingBillingEntry(t, user.ID, 15*database.MicroPerUSD)

	code, resp := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "charged", "note": "补扣余额消费"})
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}

	var freshUser, freshReferrer database.User
	if err := database.DB.First(&freshUser, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if freshUser.Quota != 5*database.MicroPerUSD || freshUser.PaidQuota != 5*database.MicroPerUSD {
		t.Fatalf("quota/paid_quota=%d/%d, want $5/$5 after bonus-first charged reconcile", freshUser.Quota, freshUser.PaidQuota)
	}
	if err := database.DB.First(&freshReferrer, referrer.ID).Error; err != nil {
		t.Fatalf("load referrer: %v", err)
	}
	if freshReferrer.Quota != 500_000 {
		t.Fatalf("referrer quota=%d, want 10%% of $5 paid spend", freshReferrer.Quota)
	}
	var reward database.BillingEntry
	if err := database.DB.Where("user_id = ? AND entry_type = ?", referrer.ID, database.BillingTypeBonusCredit).First(&reward).Error; err != nil {
		t.Fatalf("reward billing missing: %v", err)
	}
	if reward.AmountUSD != 500_000 || reward.RelatedType != "billing_entry" || reward.RelatedID != entry.ID {
		t.Fatalf("unexpected reward billing: %+v", reward)
	}
}

func TestAdminReconcileBilling_VoidedJustMarksReconciled(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 50)
	app := newReconcileTestApp(admin)

	entry := seedPendingBillingEntry(t, user.ID, 5_000_000)

	code, resp := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "voided", "note": "重复记录，作废"})
	if code != 200 {
		t.Fatalf("expected 200, got %d body=%v", code, resp)
	}

	var rec database.BillingReconciliation
	database.DB.Where("billing_entry_id = ?", entry.ID).First(&rec)
	if rec.Result != "voided" {
		t.Errorf("expected voided, got %q", rec.Result)
	}
	if rec.AdjustmentBillingEntryID != 0 {
		t.Errorf("voided should NOT create adjustment, got %d", rec.AdjustmentBillingEntryID)
	}

	// quota 不变
	var u database.User
	database.DB.First(&u, user.ID)
	if u.Quota != 50*database.MicroPerUSD {
		t.Errorf("voided should NOT change quota: got %d", u.Quota)
	}
}

func TestAdminReconcileBilling_DuplicateRejected(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 50)
	app := newReconcileTestApp(admin)

	entry := seedPendingBillingEntry(t, user.ID, 5_000_000)

	// 第一次 absorbed
	code1, _ := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "absorbed", "note": "first reconcile"})
	if code1 != 200 {
		t.Fatalf("first reconcile expected 200, got %d", code1)
	}

	// 第二次重复对账：DB unique 约束触发
	// 注意：entry.BillingState 没变（append-only），所以前置检查可能放行，
	// 由 reconciliation INSERT 的 unique 约束兜底拒绝
	code2, resp2 := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "voided", "note": "trying again"})
	if code2 != 409 {
		t.Fatalf("duplicate reconcile expected 409, got %d body=%v", code2, resp2)
	}
	if resp2["message_code"] != "ERR_RECONCILE_ALREADY_DONE" {
		t.Errorf("expected ERR_RECONCILE_ALREADY_DONE, got %v", resp2["message_code"])
	}

	// 应仍只有 1 行 reconciliation
	var cnt int64
	database.DB.Model(&database.BillingReconciliation{}).Where("billing_entry_id = ?", entry.ID).Count(&cnt)
	if cnt != 1 {
		t.Errorf("expected 1 reconciliation row, got %d", cnt)
	}
}

func TestAdminReconcileBilling_NonPendingEntryRejected(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 50)
	app := newReconcileTestApp(admin)

	// 写一条 settled 状态的账单（正常 topup）
	entry := database.BillingEntry{
		UserID:          user.ID,
		EntryType:       database.BillingTypeTopup,
		BillingState:    database.BillingStateSettled,
		AmountUSD:       10_000_000,
		BalanceAfterUSD: 60_000_000,
		Description:     "test settled topup",
	}
	database.DB.Create(&entry)

	code, resp := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "absorbed", "note": "test"})
	if code != 400 {
		t.Fatalf("settled entry reconcile expected 400, got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_RECONCILE_NOT_PENDING" {
		t.Errorf("expected ERR_RECONCILE_NOT_PENDING, got %v", resp["message_code"])
	}
}

func TestAdminReconcileBilling_ChargedInsufficientQuotaRollsBack(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 1) // 仅 $1
	app := newReconcileTestApp(admin)

	entry := seedPendingBillingEntry(t, user.ID, 5_000_000) // 待补扣 $5 > 余额

	code, resp := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "charged", "note": "try to charge insufficient"})
	if code != 500 {
		t.Fatalf("insufficient quota charge expected 500 (tx rollback), got %d body=%v", code, resp)
	}

	// 验证 reconciliation / adjust entry / quota 全部回滚
	var recCnt int64
	database.DB.Model(&database.BillingReconciliation{}).Where("billing_entry_id = ?", entry.ID).Count(&recCnt)
	if recCnt != 0 {
		t.Errorf("failed tx should rollback reconciliation, got %d rows", recCnt)
	}
	var adjustCnt int64
	database.DB.Model(&database.BillingEntry{}).Where("entry_type = ? AND related_id = ?", database.BillingTypeAdminAdjust, entry.ID).Count(&adjustCnt)
	if adjustCnt != 0 {
		t.Errorf("failed tx should rollback adjust entry, got %d rows", adjustCnt)
	}
	var u database.User
	database.DB.First(&u, user.ID)
	if u.Quota != 1*database.MicroPerUSD {
		t.Errorf("quota should be untouched on rollback: got %d want %d", u.Quota, 1*database.MicroPerUSD)
	}
}

func TestAdminReconcileBilling_InvalidInputsRejected(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	user := seedTestUser(t, 50)
	app := newReconcileTestApp(admin)

	entry := seedPendingBillingEntry(t, user.ID, 5_000_000)

	// result 非法
	code, resp := doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "wrong_value", "note": "test"})
	if code != 400 || resp["message_code"] != "ERR_RECONCILE_RESULT_INVALID" {
		t.Errorf("invalid result: got %d/%v", code, resp["message_code"])
	}

	// note 空
	code, resp = doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "absorbed", "note": "   "})
	if code != 400 || resp["message_code"] != "ERR_RECONCILE_NOTE_REQUIRED" {
		t.Errorf("empty note: got %d/%v", code, resp["message_code"])
	}

	// note 超长
	longNote := strings.Repeat("a", 600)
	code, resp = doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "absorbed", "note": longNote})
	if code != 400 || resp["message_code"] != "ERR_RECONCILE_NOTE_TOO_LONG" {
		t.Errorf("long note: got %d/%v", code, resp["message_code"])
	}

	// note 含控制字符
	code, resp = doJSON(t, app, "POST",
		"/admin/billing/"+itoaUint(entry.ID)+"/reconcile",
		map[string]any{"result": "absorbed", "note": "bad\nnote"})
	if code != 400 || resp["message_code"] != "ERR_REASON_CTRL_CHAR" {
		t.Errorf("ctrl-char note: got %d/%v", code, resp["message_code"])
	}
}
