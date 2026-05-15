package database

import (
	"testing"
	"time"

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
