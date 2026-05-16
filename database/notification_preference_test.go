package database

import (
	"path/filepath"
	"sync"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSavePreference_ConcurrentFirstSave(t *testing.T) {
	prev := DB
	dsn := filepath.Join(t.TempDir(), "notif_pref.db") + "?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	if err := db.AutoMigrate(&NotificationPreference{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	DB = db
	t.Cleanup(func() {
		DB = prev
		_ = sqlDB.Close()
	})

	const goroutines = 16
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- SavePreference(42, map[string]bool{
				"subscription_usage_warn": i%2 == 0,
			}, []int{80, 100, i})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SavePreference returned error: %v", err)
		}
	}

	var count int64
	if err := DB.Model(&NotificationPreference{}).Where("user_id = ?", 42).Count(&count).Error; err != nil {
		t.Fatalf("count preferences: %v", err)
	}
	if count != 1 {
		t.Fatalf("preference rows=%d, want 1", count)
	}
	var pref NotificationPreference
	if err := DB.Where("user_id = ?", 42).First(&pref).Error; err != nil {
		t.Fatalf("load preference: %v", err)
	}
	if pref.EnabledCategories == "" || pref.UsageThresholds == "" {
		t.Fatalf("empty preference fields: %#v", pref)
	}
	if pref.UpdatedAt.IsZero() {
		t.Fatal("updated_at should be set")
	}
}
