// Package database / oauth_identity_schema_test.go
//
// Phase H-1：OAuthIdentity append-only 不变量 + backfill 行为测试。
package database

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupOAuthIdentityTestDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&OAuthIdentity{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// partial unique index 兜底（与 sqlite.go 保持一致）
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uniq_oauth_identity_active
		ON oauth_identities(provider, external_id) WHERE unlinked_at IS NULL`)
	return db
}

func TestOAuthIdentity_InsertOK(t *testing.T) {
	db := setupOAuthIdentityTestDB(t)
	now := time.Now()
	row := OAuthIdentity{
		UserID:         1,
		Provider:       OAuthProviderGitHub,
		ExternalID:     "12345",
		EmailAtLink:    "alice@example.com",
		UsernameAtLink: "alice",
		LinkedAt:       now,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	if row.ID == 0 {
		t.Error("ID should be assigned")
	}
}

func TestOAuthIdentity_DuplicateActiveBlocked(t *testing.T) {
	db := setupOAuthIdentityTestDB(t)
	now := time.Now()
	a := OAuthIdentity{UserID: 1, Provider: OAuthProviderGitHub, ExternalID: "12345", LinkedAt: now}
	if err := db.Create(&a).Error; err != nil {
		t.Fatalf("first insert: %v", err)
	}
	// 同 (provider, external_id) active 行 → 应被 partial unique index 拦下
	b := OAuthIdentity{UserID: 2, Provider: OAuthProviderGitHub, ExternalID: "12345", LinkedAt: now}
	if err := db.Create(&b).Error; err == nil {
		t.Fatal("expected unique constraint to block duplicate active (provider, external_id)")
	}
}

func TestOAuthIdentity_UnlinkedThenRelinkSameProviderOK(t *testing.T) {
	// 用户解绑 → unlinked_at 非 nil → 新 INSERT 同 (provider, external_id) 应通过
	// （因为旧行不再算 active）。
	db := setupOAuthIdentityTestDB(t)
	now := time.Now()
	old := OAuthIdentity{UserID: 1, Provider: OAuthProviderGitHub, ExternalID: "12345", LinkedAt: now.Add(-1 * time.Hour)}
	if err := db.Create(&old).Error; err != nil {
		t.Fatalf("seed old: %v", err)
	}
	// 用户解绑（合法的 unlinked_at 写入）
	if err := db.Model(&old).Where("id = ?", old.ID).Update("unlinked_at", now).Error; err != nil {
		t.Fatalf("unlink: %v", err)
	}
	// 重新绑定（INSERT 新 active 行）
	fresh := OAuthIdentity{UserID: 1, Provider: OAuthProviderGitHub, ExternalID: "12345", LinkedAt: now}
	if err := db.Create(&fresh).Error; err != nil {
		t.Fatalf("re-link should succeed (old row is unlinked): %v", err)
	}
}

func TestOAuthIdentity_BeforeUpdate_OnlyUnlinkedAtAllowed(t *testing.T) {
	db := setupOAuthIdentityTestDB(t)
	row := OAuthIdentity{UserID: 1, Provider: OAuthProviderGitHub, ExternalID: "12345", LinkedAt: time.Now()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}

	t.Run("update unlinked_at via map allowed", func(t *testing.T) {
		now := time.Now()
		if err := db.Model(&row).Where("id = ?", row.ID).Update("unlinked_at", now).Error; err != nil {
			t.Errorf("unlink should succeed: %v", err)
		}
	})

	t.Run("full Save rejected", func(t *testing.T) {
		row.Provider = "fake"
		if err := db.Save(&row).Error; err == nil {
			t.Error("Save should be rejected by BeforeUpdate")
		}
	})

	t.Run("update external_id rejected", func(t *testing.T) {
		if err := db.Model(&row).Where("id = ?", row.ID).Update("external_id", "tampered").Error; err == nil {
			t.Error("Update external_id should be rejected")
		}
	})
}

func TestOAuthIdentity_BeforeDelete_AlwaysRejects(t *testing.T) {
	db := setupOAuthIdentityTestDB(t)
	row := OAuthIdentity{UserID: 1, Provider: OAuthProviderGitHub, ExternalID: "12345", LinkedAt: time.Now()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.Delete(&row).Error; err == nil {
		t.Error("Delete should always be rejected (append-only)")
	}
}

func TestOAuthIdentity_IsActive(t *testing.T) {
	row := OAuthIdentity{}
	if !row.IsActive() {
		t.Error("freshly-built row (unlinked_at nil) should be active")
	}
	now := time.Now()
	row.UnlinkedAt = &now
	if row.IsActive() {
		t.Error("row with unlinked_at != nil should NOT be active")
	}
}
