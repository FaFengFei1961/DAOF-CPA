package database

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const UserSessionTTL = 7 * 24 * time.Hour
const lastUsedAtRefreshInterval = 5 * time.Minute

var userSessionSchemaMu sync.Mutex

func ensureUserSessionSchema() error {
	if DB == nil {
		return fmt.Errorf("database not initialized")
	}
	if DB.Migrator().HasTable(&UserSession{}) {
		return nil
	}
	userSessionSchemaMu.Lock()
	defer userSessionSchemaMu.Unlock()
	if DB.Migrator().HasTable(&UserSession{}) {
		return nil
	}
	return DB.AutoMigrate(&UserSession{})
}

// GenerateSessionID returns a crypto-random 32-byte token encoded as 64 hex chars.
func GenerateSessionID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// IsSessionID reports whether token is the browser session token format.
func IsSessionID(token string) bool {
	token = strings.TrimSpace(token)
	if len(token) != 64 {
		return false
	}
	for _, r := range token {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// CreateUserSession creates a short-lived browser session and returns the SessionID.
func CreateUserSession(userID uint, userAgent, ipAddress string) (string, error) {
	if userID == 0 {
		return "", fmt.Errorf("userID is required")
	}
	if err := ensureUserSessionSchema(); err != nil {
		return "", err
	}
	sessionID, err := GenerateSessionID()
	if err != nil {
		return "", err
	}
	now := time.Now()
	row := UserSession{
		UserID:     userID,
		SessionID:  sessionID,
		UserAgent:  truncateRunes(userAgent, 255),
		IPAddress:  truncateRunes(ipAddress, 64),
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(UserSessionTTL),
	}
	if err := DB.Create(&row).Error; err != nil {
		return "", err
	}
	return sessionID, nil
}

// RevokeSessionByID marks a browser session revoked. Missing/invalid sessions are a no-op.
func RevokeSessionByID(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if !IsSessionID(sessionID) {
		return nil
	}
	if err := ensureUserSessionSchema(); err != nil {
		return err
	}
	now := time.Now()
	return DB.Model(&UserSession{}).
		Where("session_id = ? AND revoked_at IS NULL", sessionID).
		Update("revoked_at", &now).Error
}

// RevokeSessionsForUser revokes all still-active sessions for a user.
func RevokeSessionsForUser(userID uint) error {
	if userID == 0 {
		return nil
	}
	if err := ensureUserSessionSchema(); err != nil {
		return err
	}
	now := time.Now()
	return DB.Model(&UserSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", &now).Error
}

// LookupUserBySession resolves a browser session to a user and refreshes LastUsedAt.
func LookupUserBySession(sessionID string) (*User, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if !IsSessionID(sessionID) {
		return nil, false
	}
	if err := ensureUserSessionSchema(); err != nil {
		return nil, false
	}
	now := time.Now()
	var session UserSession
	if err := DB.Where("session_id = ? AND revoked_at IS NULL AND expires_at > ?", sessionID, now).
		First(&session).Error; err != nil {
		return nil, false
	}
	var user User
	if err := DB.First(&user, session.UserID).Error; err != nil {
		return nil, false
	}
	if now.Sub(session.LastUsedAt) > lastUsedAtRefreshInterval {
		db := DB
		go func(sessionID string) {
			if err := db.Model(&UserSession{}).
				Where("session_id = ? AND revoked_at IS NULL", sessionID).
				Update("last_used_at", time.Now()).Error; err != nil {
				log.Printf("[SESSION] last_used_at refresh failed: %v", err)
			}
		}(sessionID)
	}
	return &user, true
}

func truncateRunes(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
