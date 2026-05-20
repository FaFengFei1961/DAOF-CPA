// Package proxy / notification_email_dispatch_test.go
//
// Phase G-1.7 单元测试：dispatchEmailIfEligible 的过滤逻辑。
// 验证只有满足全部条件时才入队邮件：
//   1. master switch email_enabled = true
//   2. user 已绑邮箱 + EmailVerifiedAt 非 nil
//   3. user 偏好里该 category 在 EnabledEmailCategories 中显式启用
//
// 不测真实 SMTP 发送（队列层在 SMTP 未配置时静默跳过；G-1.9 mock SMTP 集成测）。
package proxy

import (
	"testing"
	"time"

	"daof-cpa/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupEmailDispatchTestDB(t *testing.T) *database.User {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&database.User{}, &database.SysConfig{}, &database.NotificationPreference{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db

	// 启用 master switch
	SysConfigMutex.Lock()
	SysConfigCache = map[string]string{
		"email_enabled": "true",
	}
	SysConfigMutex.Unlock()

	now := time.Now()
	u := database.User{
		Username:        "alice",
		Token:           "sk-test-dispatch",
		PasswordHash:    "x",
		Status:          1,
		Email:           "alice@example.com",
		EmailVerifiedAt: &now,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return &u
}

func setEmailPref(t *testing.T, userID uint, cats map[string]bool) {
	t.Helper()
	if err := database.SavePreference(userID, map[string]bool{}, []int{}, cats); err != nil {
		t.Fatalf("save pref: %v", err)
	}
	InvalidatePrefCache(userID)
}

func TestDispatchEmailIfEligible_AllConditionsMet(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()
	user := setupEmailDispatchTestDB(t)
	// 显式开启 refund 邮件类别
	setEmailPref(t, user.ID, map[string]bool{"refund": true})

	// 调用 dispatchEmailIfEligible —— 不真实发邮件（SMTP 未配置），但 dedup map 应记录
	key := "test-dedup-refund-1"
	dispatchEmailIfEligible(user.ID, "refund", "退款已到账", "您的订阅已退款", &key)

	// dedup map 里应有 "email:test-dedup-refund-1"
	emailDedupMu.Lock()
	_, found := emailDedupMap["email:"+key]
	emailDedupMu.Unlock()
	if !found {
		t.Error("expected email dedup entry, got none — task may not have been enqueued")
	}
}

func TestDispatchEmailIfEligible_MasterDisabledSkips(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()
	user := setupEmailDispatchTestDB(t)
	setEmailPref(t, user.ID, map[string]bool{"refund": true})

	// 关闭 master
	SysConfigMutex.Lock()
	SysConfigCache["email_enabled"] = "false"
	SysConfigMutex.Unlock()

	key := "test-master-off"
	dispatchEmailIfEligible(user.ID, "refund", "T", "B", &key)

	emailDedupMu.Lock()
	_, found := emailDedupMap["email:"+key]
	emailDedupMu.Unlock()
	if found {
		t.Error("master disabled → should not enqueue email")
	}
}

func TestDispatchEmailIfEligible_NoVerifiedEmailSkips(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()
	user := setupEmailDispatchTestDB(t)
	setEmailPref(t, user.ID, map[string]bool{"refund": true})

	// 清掉 verified_at（绑了但未验证）
	if err := database.DB.Model(&database.User{}).
		Where("id = ?", user.ID).
		Update("email_verified_at", nil).Error; err != nil {
		t.Fatalf("clear verified: %v", err)
	}

	key := "test-no-verified"
	dispatchEmailIfEligible(user.ID, "refund", "T", "B", &key)

	emailDedupMu.Lock()
	_, found := emailDedupMap["email:"+key]
	emailDedupMu.Unlock()
	if found {
		t.Error("unverified email → should not enqueue email")
	}
}

func TestDispatchEmailIfEligible_NoEmailBoundSkips(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()
	user := setupEmailDispatchTestDB(t)
	setEmailPref(t, user.ID, map[string]bool{"refund": true})

	// 清掉 email
	if err := database.DB.Model(&database.User{}).
		Where("id = ?", user.ID).
		Updates(map[string]any{"email": "", "email_verified_at": nil}).Error; err != nil {
		t.Fatalf("clear email: %v", err)
	}

	key := "test-no-email"
	dispatchEmailIfEligible(user.ID, "refund", "T", "B", &key)

	emailDedupMu.Lock()
	_, found := emailDedupMap["email:"+key]
	emailDedupMu.Unlock()
	if found {
		t.Error("no bound email → should not enqueue email")
	}
}

func TestDispatchEmailIfEligible_CategoryNotEnabledSkips(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()
	user := setupEmailDispatchTestDB(t)
	// 偏好开了 refund 但没开 security
	setEmailPref(t, user.ID, map[string]bool{"refund": true})

	key := "test-cat-not-enabled"
	dispatchEmailIfEligible(user.ID, "security", "T", "B", &key)

	emailDedupMu.Lock()
	_, found := emailDedupMap["email:"+key]
	emailDedupMu.Unlock()
	if found {
		t.Error("category not in EnabledEmailCategories → should not enqueue email")
	}
}

func TestDispatchEmailIfEligible_BannedUserSkips(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()
	user := setupEmailDispatchTestDB(t)
	setEmailPref(t, user.ID, map[string]bool{"refund": true})

	// 封禁
	if err := database.DB.Model(&database.User{}).
		Where("id = ?", user.ID).
		Update("status", 2).Error; err != nil {
		t.Fatalf("ban: %v", err)
	}

	key := "test-banned"
	dispatchEmailIfEligible(user.ID, "refund", "T", "B", &key)

	emailDedupMu.Lock()
	_, found := emailDedupMap["email:"+key]
	emailDedupMu.Unlock()
	if found {
		t.Error("banned user → should not enqueue email")
	}
}

func TestDispatchEmailIfEligible_NoDedupKeyAllowed(t *testing.T) {
	resetEmailQueueForTest()
	defer resetEmailQueueForTest()
	user := setupEmailDispatchTestDB(t)
	setEmailPref(t, user.ID, map[string]bool{"refund": true})

	// dedupKey 为 nil → 不写 dedup map（不去重）
	dispatchEmailIfEligible(user.ID, "refund", "T", "B", nil)

	emailDedupMu.Lock()
	dedupSize := len(emailDedupMap)
	emailDedupMu.Unlock()
	if dedupSize != 0 {
		t.Errorf("nil dedupKey should not write to dedup map, got size %d", dedupSize)
	}
}
