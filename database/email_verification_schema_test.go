// Package database / email_verification_schema_test.go
//
// Phase G-1.1 单元测试：EmailVerification 表的 helper 方法 + append-only 不变量。
package database

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestEmailVerification_IsConsumed(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		v    EmailVerification
		want bool
	}{
		{"nil ConsumedAt not consumed", EmailVerification{ConsumedAt: nil}, false},
		{"non-nil ConsumedAt consumed", EmailVerification{ConsumedAt: &now}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.IsConsumed(); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestEmailVerification_IsExpired(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"future not expired", now.Add(time.Hour), false},
		{"now not expired", now, false},
		{"past expired", now.Add(-time.Second), true},
		{"way past expired", now.Add(-24 * time.Hour), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := EmailVerification{ExpiresAt: tc.expiresAt}
			if got := v.IsExpired(now); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestEmailVerification_IsUsable(t *testing.T) {
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	t.Run("usable when unconsumed and unexpired", func(t *testing.T) {
		v := EmailVerification{ExpiresAt: future, ConsumedAt: nil}
		if !v.IsUsable(now) {
			t.Error("should be usable")
		}
	})
	t.Run("not usable when consumed", func(t *testing.T) {
		v := EmailVerification{ExpiresAt: future, ConsumedAt: &past}
		if v.IsUsable(now) {
			t.Error("consumed token should not be usable")
		}
	})
	t.Run("not usable when expired", func(t *testing.T) {
		v := EmailVerification{ExpiresAt: past, ConsumedAt: nil}
		if v.IsUsable(now) {
			t.Error("expired token should not be usable")
		}
	})
	t.Run("not usable when both consumed and expired", func(t *testing.T) {
		v := EmailVerification{ExpiresAt: past, ConsumedAt: &past}
		if v.IsUsable(now) {
			t.Error("consumed+expired token should not be usable")
		}
	})
}

func setupEmailVerificationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&EmailVerification{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestEmailVerification_AppendOnly_DeleteRejected(t *testing.T) {
	db := setupEmailVerificationTestDB(t)
	row := EmailVerification{
		UserID:    1,
		Email:     "alice@example.com",
		TokenHash: "abc123",
		Purpose:   EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Delete(&row).Error; !errors.Is(err, ErrEmailVerificationImmutable) {
		t.Errorf("delete should be rejected with ErrEmailVerificationImmutable, got %v", err)
	}
}

func TestEmailVerification_AppendOnly_FullUpdateRejected(t *testing.T) {
	db := setupEmailVerificationTestDB(t)
	row := EmailVerification{
		UserID:    1,
		Email:     "alice@example.com",
		TokenHash: "abc123",
		Purpose:   EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	// 试图 Save() 全列更新 → 拒绝
	row.Email = "evil@example.com"
	if err := db.Save(&row).Error; !errors.Is(err, ErrEmailVerificationImmutable) {
		t.Errorf("full Save should be rejected, got %v", err)
	}
}

func TestEmailVerification_AppendOnly_ConsumedAtUpdateAllowed(t *testing.T) {
	db := setupEmailVerificationTestDB(t)
	row := EmailVerification{
		UserID:    1,
		Email:     "alice@example.com",
		TokenHash: "abc123",
		Purpose:   EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	// 唯一允许：仅 update consumed_at 一列
	now := time.Now()
	if err := db.Model(&EmailVerification{}).
		Where("id = ?", row.ID).
		Update("consumed_at", now).Error; err != nil {
		t.Errorf("consumed_at update should succeed, got %v", err)
	}
	var fresh EmailVerification
	if err := db.First(&fresh, row.ID).Error; err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if fresh.ConsumedAt == nil {
		t.Error("ConsumedAt should be set after update")
	}
}

func TestEmailVerification_AppendOnly_OtherColumnUpdateRejected(t *testing.T) {
	db := setupEmailVerificationTestDB(t)
	row := EmailVerification{
		UserID:    1,
		Email:     "alice@example.com",
		TokenHash: "abc123",
		Purpose:   EmailVerificationPurposeVerify,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	// 尝试更新 Email 列 → 拒绝
	if err := db.Model(&EmailVerification{}).
		Where("id = ?", row.ID).
		Update("email", "evil@example.com").Error; !errors.Is(err, ErrEmailVerificationImmutable) {
		t.Errorf("email column update should be rejected, got %v", err)
	}
}
