package database

import (
	"strings"
	"testing"
	"time"

	"daof-cpa/utils"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupSessionTestDB(t *testing.T) {
	t.Helper()
	var err error
	DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := DB.AutoMigrate(&User{}, &UserSession{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	DB.Exec("DELETE FROM user_sessions")
	DB.Exec("DELETE FROM users")
}

func TestUserSession_LookupAfterRevokeFails(t *testing.T) {
	setupSessionTestDB(t)
	user := User{Username: "session_user", Role: "user", Token: "sk-daof-user", Status: 1}
	if err := DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	sessionID, err := CreateUserSession(user.ID, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if got, ok := LookupUserBySession(sessionID); !ok || got.ID != user.ID {
		t.Fatalf("lookup before revoke got user=%v ok=%v, want user %d", got, ok, user.ID)
	}
	if err := RevokeSessionByID(sessionID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got, ok := LookupUserBySession(sessionID); ok || got != nil {
		t.Fatalf("lookup after revoke got user=%v ok=%v, want revoked", got, ok)
	}
}

func TestUserSession_ExpiredSessionFails(t *testing.T) {
	setupSessionTestDB(t)
	user := User{Username: "expired_user", Role: "user", Token: "sk-daof-expired", Status: 1}
	if err := DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	sessionID, err := GenerateSessionID()
	if err != nil {
		t.Fatalf("generate session id: %v", err)
	}
	now := time.Now()
	expired := UserSession{
		UserID:     user.ID,
		SessionID:  sessionID,
		CreatedAt:  now.Add(-2 * time.Hour),
		LastUsedAt: now.Add(-2 * time.Hour),
		ExpiresAt:  now.Add(-1 * time.Hour),
	}
	if err := DB.Create(&expired).Error; err != nil {
		t.Fatalf("create expired session: %v", err)
	}
	if got, ok := LookupUserBySession(sessionID); ok || got != nil {
		t.Fatalf("lookup expired session got user=%v ok=%v, want expired", got, ok)
	}
}

func TestLookupUserBySession_ThrottlesLastUsedAt(t *testing.T) {
	setupSessionTestDB(t)
	user := User{Username: "throttle_user", Role: "user", Token: "sk-daof-throttle", Status: 1}
	if err := DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	sessionID, err := GenerateSessionID()
	if err != nil {
		t.Fatalf("generate session id: %v", err)
	}
	now := time.Now()
	recent := now.Add(-lastUsedAtRefreshInterval / 2).Truncate(time.Second)
	session := UserSession{
		UserID:     user.ID,
		SessionID:  sessionID,
		CreatedAt:  now.Add(-time.Hour),
		LastUsedAt: recent,
		ExpiresAt:  now.Add(time.Hour),
	}
	if err := DB.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	if got, ok := LookupUserBySession(sessionID); !ok || got.ID != user.ID {
		t.Fatalf("lookup got user=%v ok=%v, want user %d", got, ok, user.ID)
	}
	time.Sleep(50 * time.Millisecond)
	var fresh UserSession
	if err := DB.Where("session_id = ?", sessionID).First(&fresh).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if !fresh.LastUsedAt.Equal(recent) {
		t.Fatalf("last_used_at changed to %s, want unchanged %s", fresh.LastUsedAt, recent)
	}
}

func TestLookupUserBySession_DBWriteFailsButReturnsSuccess(t *testing.T) {
	setupSessionTestDB(t)
	user := User{Username: "write_fail_user", Role: "user", Token: "sk-daof-write-fail", Status: 1}
	if err := DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	sessionID, err := GenerateSessionID()
	if err != nil {
		t.Fatalf("generate session id: %v", err)
	}
	now := time.Now()
	session := UserSession{
		UserID:     user.ID,
		SessionID:  sessionID,
		CreatedAt:  now.Add(-time.Hour),
		LastUsedAt: now.Add(-lastUsedAtRefreshInterval - time.Minute),
		ExpiresAt:  now.Add(time.Hour),
	}
	if err := DB.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := DB.Exec(`CREATE TRIGGER fail_last_used_update
		BEFORE UPDATE OF last_used_at ON user_sessions
		BEGIN
			SELECT RAISE(FAIL, 'last_used_at blocked');
		END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if got, ok := LookupUserBySession(sessionID); !ok || got.ID != user.ID {
		t.Fatalf("lookup with failing refresh got user=%v ok=%v, want user %d", got, ok, user.ID)
	}
	time.Sleep(50 * time.Millisecond)
	var fresh UserSession
	if err := DB.Where("session_id = ?", sessionID).First(&fresh).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if fresh.LastUsedAt.After(now.Add(-lastUsedAtRefreshInterval)) {
		t.Fatalf("last_used_at unexpectedly refreshed despite trigger: %s", fresh.LastUsedAt)
	}
}

func TestBillingEntryUniqueRelated(t *testing.T) {
	t.Setenv("DAOF_DB_PATH", t.TempDir()+"/billing_unique.db")
	utils.InitCrypto()
	InitDB()
	t.Cleanup(func() {
		if sqlDB, err := DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	entry := func(relatedID uint) BillingEntry {
		return BillingEntry{
			UserID:          1,
			OccurredAt:      time.Now(),
			EntryType:       BillingTypeTopup,
			BillingState:    BillingStateSettled,
			AmountUSD:       MicroPerUSD,
			BalanceAfterUSD: MicroPerUSD,
			RelatedType:     "topup_order",
			RelatedID:       relatedID,
			Description:     "unique related test",
			CreatedAt:       time.Now(),
		}
	}
	first := entry(123)
	if err := DB.Create(&first).Error; err != nil {
		t.Fatalf("create first related entry: %v", err)
	}
	dup := entry(123)
	if err := DB.Create(&dup).Error; err == nil {
		t.Fatal("duplicate related billing entry should fail unique index")
	}
	zero1 := entry(0)
	zero2 := entry(0)
	if err := DB.Create(&zero1).Error; err != nil {
		t.Fatalf("create zero related entry 1: %v", err)
	}
	if err := DB.Create(&zero2).Error; err != nil {
		t.Fatalf("create zero related entry 2 should be allowed: %v", err)
	}
}

func TestNotifPartialIndex_ExcludesRevoked(t *testing.T) {
	t.Setenv("DAOF_DB_PATH", t.TempDir()+"/notif_index.db")
	utils.InitCrypto()
	InitDB()
	t.Cleanup(func() {
		if sqlDB, err := DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	var sql string
	if err := DB.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_notif_user_unread'`).Scan(&sql).Error; err != nil {
		t.Fatalf("load index sql: %v", err)
	}
	normalized := strings.ToLower(sql)
	if !strings.Contains(normalized, "read_at is null") || !strings.Contains(normalized, "revoked_at is null") {
		t.Fatalf("idx_notif_user_unread sql=%q, want read_at and revoked_at partial predicates", sql)
	}
}
