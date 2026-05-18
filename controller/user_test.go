package controller

import (
	"sync"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupUserControllerTestDB(t *testing.T) {
	t.Helper()
	prev := database.DB
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(
		&database.User{},
		&database.UserSession{},
		&database.AccessToken{},
		&database.Channel{},
		&database.ChannelModel{},
		&database.SysConfig{},
		&database.ApiLog{},
		&database.ApiLogAttribution{},
		&database.ApiLogCostEstimate{},
		&database.OperationLog{},
		&database.BillingEntry{},
		&database.BillingReconciliation{},
		&database.Notification{},
		&database.NotificationBroadcastTarget{},
		&database.UserSubscription{},
		&database.SubscriptionUsage{},
		&database.TopupOrder{},
		&database.TopupRefund{},
		&database.UserCoupon{},
		&database.NotificationPreference{},
		&database.Ticket{},
		&database.TicketMessage{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = prev
		_ = sqlDB.Close()
	})
}

func newUpdateUserTestApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Put("/admin/users/:id", UpdateUser)
	app.Post("/admin/users/:id/purge", AdminPurgeUser)
	app.Delete("/admin/users/:id", DeleteUser)
	return app
}

func seedUpdateUserTarget(t *testing.T, quotaMicro int64, status int) database.User {
	t.Helper()
	u := database.User{
		Username: "user-update-target",
		Role:     "user",
		Token:    "sk-user-update-target",
		Quota:    quotaMicro,
		Status:   status,
	}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

func TestUpdateUser_UsesQuotaUSDWire(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	user := seedUpdateUserTarget(t, 5*database.MicroPerUSD, 1)

	code, resp := doJSON(t, app, "PUT", "/admin/users/"+itoaUint(user.ID), map[string]any{
		"username":   user.Username,
		"quota":      12.345678,
		"status":     1,
		"ban_reason": "",
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}

	var fresh database.User
	if err := database.DB.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if fresh.Quota != 12_345_678 {
		t.Fatalf("quota=%d want 12345678", fresh.Quota)
	}
}

// fix CRITICAL（codex review --uncommitted）：banned 用户保留 session 以便走 UserGuardAllowBanned
// 申诉路径（提工单 / 查 /user/me / 查账单）。原测试期望 ban 即撤销 session，与新设计冲突——
// 改为断言 session **保留**（appeal flow 可达）；LLM/写动作由 middleware UserGuard + LLM 路径
// 双层 status!=1 检查兜底。
func TestUpdateUser_BanKeepsSessionForAppeal(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	user := seedUpdateUserTarget(t, 10*database.MicroPerUSD, 1)
	sessionID, err := database.CreateUserSession(user.ID, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if got, ok := database.LookupUserBySession(sessionID); !ok || got.ID != user.ID {
		t.Fatalf("session should resolve before ban, got user=%v ok=%v", got, ok)
	}

	code, resp := doJSON(t, app, "PUT", "/admin/users/"+itoaUint(user.ID), map[string]any{
		"username":   user.Username,
		"quota":      database.MicroToUSD(user.Quota),
		"status":     2,
		"ban_reason": "policy",
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}

	// session 不应被撤销 — banned 用户走 UserGuardAllowBanned 端点（/api/user/me、/api/tickets）
	// 需要这条 session 解析成功。
	var session database.UserSession
	if err := database.DB.Where("session_id = ?", sessionID).First(&session).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if session.RevokedAt != nil {
		t.Fatalf("session revoked_at should remain nil after ban, got %v (banned user must retain session for appeal)", session.RevokedAt)
	}
	// LookupUserBySession 仍可解析；middleware 用 c.Locals("user_banned") 区分 banned 用户。
	if got, ok := database.LookupUserBySession(sessionID); !ok || got == nil || got.Status != 2 {
		t.Fatalf("session should resolve to banned user, got user=%v ok=%v", got, ok)
	}
}

func seedAdminForUserMutation(t *testing.T, username string) database.User {
	t.Helper()
	u := database.User{
		Username: username,
		Role:     "admin",
		Token:    "sk-" + username,
		Status:   1,
	}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed admin %s: %v", username, err)
	}
	return u
}

func TestLastActiveAdmin_BlocksBan(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	admin := seedAdminForUserMutation(t, "admin_ban_last")

	code, resp := doJSON(t, app, "PUT", "/admin/users/"+itoaUint(admin.ID), map[string]any{
		"username":   admin.Username,
		"quota":      database.MicroToUSD(admin.Quota),
		"status":     2,
		"ban_reason": "policy",
	})
	if code != 403 || resp["message_code"] != "ERR_SUICIDE_PROTECTION_SEAL" {
		t.Fatalf("ban last admin got %d/%v, want 403/ERR_SUICIDE_PROTECTION_SEAL", code, resp["message_code"])
	}
	var fresh database.User
	if err := database.DB.First(&fresh, admin.ID).Error; err != nil {
		t.Fatalf("reload admin: %v", err)
	}
	if fresh.Status != 1 {
		t.Fatalf("admin status=%d, want 1", fresh.Status)
	}
}

func TestLastActiveAdmin_BlocksDelete(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	admin := seedAdminForUserMutation(t, "admin_delete_last")

	code, resp := doJSON(t, app, "DELETE", "/admin/users/"+itoaUint(admin.ID), nil)
	if code != 403 || resp["message_code"] != "ERR_ADMIN_REQUIRED" {
		t.Fatalf("delete last admin got %d/%v, want 403/ERR_ADMIN_REQUIRED", code, resp["message_code"])
	}
	var count int64
	if err := database.DB.Model(&database.User{}).
		Where("role = ? AND status = ?", "admin", 1).
		Count(&count).Error; err != nil {
		t.Fatalf("count active admins: %v", err)
	}
	if count != 1 {
		t.Fatalf("active admin count=%d, want 1", count)
	}
}

func TestDeleteUser_PreservesBillingChain(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	user := seedUpdateUserTarget(t, 10*database.MicroPerUSD, 1)
	sessionID, err := database.CreateUserSession(user.ID, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sub := database.UserSubscription{
		UserID:                user.ID,
		PackageID:             1,
		Status:                "active",
		PackageSnapshot:       `{"package_id":1,"package_name":"Pro"}`,
		PurchasedUnitPriceUSD: database.MicroPerUSD,
	}
	if err := database.DB.Create(&sub).Error; err != nil {
		t.Fatalf("create sub: %v", err)
	}
	usage := database.SubscriptionUsage{
		SubscriptionID: sub.ID,
		QuotaPlanID:    1,
		ModelBucket:    "*",
		WindowStartAt:  time.Now(),
		WindowEndAt:    time.Now().Add(time.Hour),
		RequestCount:   1,
	}
	if err := database.DB.Create(&usage).Error; err != nil {
		t.Fatalf("create usage: %v", err)
	}
	order := database.TopupOrder{
		OutTradeNo:                  "tp-delete-preserve",
		UserID:                      user.ID,
		PayType:                     "alipay",
		MoneyRMB:                    7200,
		AmountUSD:                   10 * database.MicroPerUSD,
		ExchangeRateRmbPerUsdMicros: 7_200_000,
		Status:                      "paid",
	}
	if err := database.DB.Create(&order).Error; err != nil {
		t.Fatalf("create topup order: %v", err)
	}
	apiLog := database.ApiLog{
		UserID:      user.ID,
		TokenName:   "sk",
		ModelName:   "gpt-test",
		Status:      200,
		RequestPath: "/v1/chat/completions",
	}
	if err := database.DB.Create(&apiLog).Error; err != nil {
		t.Fatalf("create api log: %v", err)
	}
	billing := database.BillingEntry{
		UserID:          user.ID,
		OccurredAt:      time.Now(),
		EntryType:       database.BillingTypeTopup,
		BillingState:    database.BillingStateSettled,
		AmountUSD:       order.AmountUSD,
		BalanceAfterUSD: user.Quota,
		RelatedType:     "topup_order",
		RelatedID:       order.ID,
		Description:     "topup preserve test",
	}
	if err := database.DB.Create(&billing).Error; err != nil {
		t.Fatalf("create billing: %v", err)
	}

	code, resp := doJSON(t, app, "DELETE", "/admin/users/"+itoaUint(user.ID), nil)
	if code != 200 {
		t.Fatalf("delete got %d body=%v", code, resp)
	}

	var deleted database.User
	if err := database.DB.Unscoped().First(&deleted, user.ID).Error; err != nil {
		t.Fatalf("load deleted user: %v", err)
	}
	if !deleted.DeletedAt.Valid {
		t.Fatal("user should be soft deleted")
	}
	if deleted.Username != user.Username || deleted.Status != user.Status {
		t.Fatalf("ordinary delete should not anonymize or change status: %#v", deleted)
	}
	assertCount := func(name string, model any, cond string, args ...any) {
		t.Helper()
		var count int64
		if err := database.DB.Unscoped().Model(model).Where(cond, args...).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("%s count=%d, want 1", name, count)
		}
	}
	assertCount("subscription", &database.UserSubscription{}, "id = ?", sub.ID)
	assertCount("usage", &database.SubscriptionUsage{}, "id = ?", usage.ID)
	assertCount("topup order", &database.TopupOrder{}, "id = ?", order.ID)
	assertCount("api log", &database.ApiLog{}, "id = ?", apiLog.ID)
	assertCount("billing", &database.BillingEntry{}, "id = ?", billing.ID)

	var sessions int64
	if err := database.DB.Model(&database.UserSession{}).Where("session_id = ?", sessionID).Count(&sessions).Error; err != nil {
		t.Fatalf("count session: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("session count=%d, want 1", sessions)
	}
}

func TestAdminPurgeUser_RequiresConfirmAndPurgesDependents(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	user := seedUpdateUserTarget(t, 10*database.MicroPerUSD, 1)
	sessionID, err := database.CreateUserSession(user.ID, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sub := database.UserSubscription{
		UserID:          user.ID,
		PackageID:       1,
		Status:          "active",
		PackageSnapshot: `{"package_id":1}`,
	}
	if err := database.DB.Create(&sub).Error; err != nil {
		t.Fatalf("create sub: %v", err)
	}
	if err := database.DB.Create(&database.SubscriptionUsage{
		SubscriptionID: sub.ID,
		QuotaPlanID:    1,
		ModelBucket:    "*",
		WindowStartAt:  time.Now(),
		WindowEndAt:    time.Now().Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("create usage: %v", err)
	}
	order := database.TopupOrder{
		OutTradeNo:                  "tp-purge",
		UserID:                      user.ID,
		PayType:                     "alipay",
		MoneyRMB:                    7200,
		AmountUSD:                   10 * database.MicroPerUSD,
		ExchangeRateRmbPerUsdMicros: 7_200_000,
		Status:                      "paid",
	}
	if err := database.DB.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	if err := database.DB.Create(&database.TopupRefund{
		TopupOrderID:      order.ID,
		ExternalRefundRef: "rf-purge",
		AmountFen:         100,
		AmountMicroUSD:    database.MicroPerUSD,
		ReclaimQuota:      true,
		OperatorID:        1,
	}).Error; err != nil {
		t.Fatalf("create refund: %v", err)
	}
	apiLog := database.ApiLog{UserID: user.ID, TokenName: "sk", ModelName: "gpt-test", Status: 200}
	if err := database.DB.Create(&apiLog).Error; err != nil {
		t.Fatalf("create api log: %v", err)
	}
	if err := database.DB.Create(&database.ApiLogAttribution{ApiLogID: apiLog.ID, MatchedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create attribution: %v", err)
	}
	if err := database.DB.Create(&database.ApiLogCostEstimate{ApiLogID: apiLog.ID, PlatformCostMicroUSD: 123, ComputedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create estimate: %v", err)
	}
	billing := database.BillingEntry{
		UserID:          user.ID,
		OccurredAt:      time.Now(),
		EntryType:       database.BillingTypeTopup,
		BillingState:    database.BillingStateSettled,
		AmountUSD:       order.AmountUSD,
		BalanceAfterUSD: user.Quota,
		Description:     "purge billing",
	}
	if err := database.DB.Create(&billing).Error; err != nil {
		t.Fatalf("create billing: %v", err)
	}
	if err := database.DB.Create(&database.BillingReconciliation{
		BillingEntryID: billing.ID,
		Result:         database.ReconcileResultAbsorbed,
		OperatorID:     1,
		OperatorRole:   "admin",
		Note:           "purge",
	}).Error; err != nil {
		t.Fatalf("create reconciliation: %v", err)
	}
	if err := database.DB.Create(&database.UserCoupon{UserID: user.ID, TemplateID: 1, Code: "CP-purge", Status: "available"}).Error; err != nil {
		t.Fatalf("create coupon: %v", err)
	}
	notif := database.Notification{UserID: user.ID, Category: "system", Severity: "info", Title: "t"}
	if err := database.DB.Create(&notif).Error; err != nil {
		t.Fatalf("create notification: %v", err)
	}
	if err := database.DB.Create(&database.NotificationBroadcastTarget{BroadcastID: 1, UserID: user.ID, NotificationID: notif.ID}).Error; err != nil {
		t.Fatalf("create broadcast target: %v", err)
	}
	if err := database.DB.Create(&database.NotificationPreference{UserID: user.ID}).Error; err != nil {
		t.Fatalf("create notification pref: %v", err)
	}
	ticket := database.Ticket{UserID: user.ID, Subject: "purge", LastMessageAt: time.Now()}
	if err := database.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	if err := database.DB.Create(&database.TicketMessage{TicketID: ticket.ID, Sender: "user", SenderID: user.ID, Body: "hello"}).Error; err != nil {
		t.Fatalf("create ticket message: %v", err)
	}

	code, resp := doJSON(t, app, "POST", "/admin/users/"+itoaUint(user.ID)+"/purge", nil)
	if code != 400 || resp["message_code"] != "ERR_PURGE_CONFIRM_REQUIRED" {
		t.Fatalf("purge without confirm got %d/%v", code, resp["message_code"])
	}

	code, resp = doJSON(t, app, "POST", "/admin/users/"+itoaUint(user.ID)+"/purge?confirm=YES_DELETE_ALL", nil)
	if code != 200 {
		t.Fatalf("purge got %d body=%v", code, resp)
	}

	assertZero := func(name string, model any, cond string, args ...any) {
		t.Helper()
		var count int64
		if err := database.DB.Unscoped().Model(model).Where(cond, args...).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("%s count=%d, want 0", name, count)
		}
	}
	assertZero("user", &database.User{}, "id = ?", user.ID)
	assertZero("session", &database.UserSession{}, "session_id = ?", sessionID)
	assertZero("subscription", &database.UserSubscription{}, "id = ?", sub.ID)
	assertZero("usage", &database.SubscriptionUsage{}, "subscription_id = ?", sub.ID)
	assertZero("topup order", &database.TopupOrder{}, "id = ?", order.ID)
	assertZero("topup refund", &database.TopupRefund{}, "topup_order_id = ?", order.ID)
	assertZero("api log", &database.ApiLog{}, "id = ?", apiLog.ID)
	assertZero("api attribution", &database.ApiLogAttribution{}, "api_log_id = ?", apiLog.ID)
	assertZero("api estimate", &database.ApiLogCostEstimate{}, "api_log_id = ?", apiLog.ID)
	assertZero("billing", &database.BillingEntry{}, "id = ?", billing.ID)
	assertZero("reconciliation", &database.BillingReconciliation{}, "billing_entry_id = ?", billing.ID)
	assertZero("coupon", &database.UserCoupon{}, "user_id = ?", user.ID)
	assertZero("notification", &database.Notification{}, "id = ?", notif.ID)
	assertZero("broadcast target", &database.NotificationBroadcastTarget{}, "notification_id = ?", notif.ID)
	assertZero("notification preference", &database.NotificationPreference{}, "user_id = ?", user.ID)
	assertZero("ticket", &database.Ticket{}, "id = ?", ticket.ID)
	assertZero("ticket message", &database.TicketMessage{}, "ticket_id = ?", ticket.ID)

	var purgeLog database.OperationLog
	if err := database.DB.Where("target_user_id = ? AND action_type = ?", user.ID, "USER_PURGE_GDPR").First(&purgeLog).Error; err != nil {
		t.Fatalf("purge audit log missing: %v", err)
	}
}

func TestLastActiveAdmin_ConcurrentSafe(t *testing.T) {
	setupUserControllerTestDB(t)
	app := newUpdateUserTestApp()
	admin1 := seedAdminForUserMutation(t, "admin_concurrent_1")
	admin2 := seedAdminForUserMutation(t, "admin_concurrent_2")

	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, admin := range []database.User{admin1, admin2} {
		admin := admin
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := doJSON(t, app, "PUT", "/admin/users/"+itoaUint(admin.ID), map[string]any{
				"username":   admin.Username,
				"quota":      database.MicroToUSD(admin.Quota),
				"status":     2,
				"ban_reason": "policy",
			})
			results <- code
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for code := range results {
		if code == 200 {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent bans=%d, want 1", successes)
	}
	var active int64
	if err := database.DB.Model(&database.User{}).
		Where("role = ? AND status = ?", "admin", 1).
		Count(&active).Error; err != nil {
		t.Fatalf("count active admins: %v", err)
	}
	if active != 1 {
		t.Fatalf("active admin count=%d, want 1", active)
	}
}
