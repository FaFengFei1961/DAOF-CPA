package database

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupDistributedLockTestDB(t *testing.T) {
	t.Helper()

	oldDB := DB
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(5)
		_, _ = sqlDB.Exec("PRAGMA busy_timeout=5000")
	}
	if err := db.AutoMigrate(&DistributedLock{}); err != nil {
		t.Fatalf("migrate distributed locks: %v", err)
	}
	DB = db
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		DB = oldDB
	})
}

func TestAcquireLock_FirstSucceedsSecondFails(t *testing.T) {
	setupDistributedLockTestDB(t)

	type acquireResult struct {
		owner    string
		acquired bool
		err      error
	}

	start := make(chan struct{})
	results := make(chan acquireResult, 2)
	var wg sync.WaitGroup
	for _, owner := range []string{"owner-a", "owner-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			_, acquired, err := acquireLockForOwner("cliproxy_usage_sync", owner, time.Minute)
			results <- acquireResult{owner: owner, acquired: acquired, err: err}
		}(owner)
	}

	close(start)
	wg.Wait()
	close(results)

	successes := 0
	failures := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("%s acquire: %v", result.owner, result.err)
		}
		if result.acquired {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("successes=%d failures=%d, want exactly one owner to acquire", successes, failures)
	}
}

func TestAcquireLock_AfterExpiryNewOwnerSucceeds(t *testing.T) {
	setupDistributedLockTestDB(t)

	_, acquired, err := acquireLockForOwner("cliproxy_usage_sync", "owner-a", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if !acquired {
		t.Fatal("first owner did not acquire lock")
	}

	time.Sleep(30 * time.Millisecond)

	_, acquired, err = acquireLockForOwner("cliproxy_usage_sync", "owner-b", time.Minute)
	if err != nil {
		t.Fatalf("second acquire after expiry: %v", err)
	}
	if !acquired {
		t.Fatal("new owner did not acquire expired lock")
	}

	var lock DistributedLock
	if err := DB.Where("lock_key = ?", "cliproxy_usage_sync").First(&lock).Error; err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if lock.OwnerID != "owner-b" {
		t.Fatalf("owner=%q want owner-b", lock.OwnerID)
	}
}

func TestRenewLock_SameOwnerExtends(t *testing.T) {
	setupDistributedLockTestDB(t)

	_, acquired, err := acquireLockForOwner("cliproxy_usage_sync", "owner-a", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired {
		t.Fatal("owner did not acquire lock")
	}

	var before DistributedLock
	if err := DB.Where("lock_key = ?", "cliproxy_usage_sync").First(&before).Error; err != nil {
		t.Fatalf("read lock before renew: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	renewed, err := RenewLock("cliproxy_usage_sync", "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed {
		t.Fatal("same owner did not renew lock")
	}

	var after DistributedLock
	if err := DB.Where("lock_key = ?", "cliproxy_usage_sync").First(&after).Error; err != nil {
		t.Fatalf("read lock after renew: %v", err)
	}
	if !after.ExpiresAt.After(before.ExpiresAt) {
		t.Fatalf("expires_at was not extended: before=%s after=%s", before.ExpiresAt, after.ExpiresAt)
	}
	if after.OwnerID != "owner-a" {
		t.Fatalf("owner changed to %q", after.OwnerID)
	}
}

func TestReleaseLock_RequiresOwnerMatch(t *testing.T) {
	setupDistributedLockTestDB(t)

	_, acquired, err := acquireLockForOwner("cliproxy_usage_sync", "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired {
		t.Fatal("owner did not acquire lock")
	}

	if err := ReleaseLock("cliproxy_usage_sync", "owner-b"); err != nil {
		t.Fatalf("release wrong owner: %v", err)
	}

	var count int64
	if err := DB.Model(&DistributedLock{}).Where("lock_key = ?", "cliproxy_usage_sync").Count(&count).Error; err != nil {
		t.Fatalf("count lock after wrong release: %v", err)
	}
	if count != 1 {
		t.Fatalf("wrong owner released lock, count=%d", count)
	}

	if err := ReleaseLock("cliproxy_usage_sync", "owner-a"); err != nil {
		t.Fatalf("release owner: %v", err)
	}
	if err := DB.Model(&DistributedLock{}).Where("lock_key = ?", "cliproxy_usage_sync").Count(&count).Error; err != nil {
		t.Fatalf("count lock after release: %v", err)
	}
	if count != 0 {
		t.Fatalf("owner release did not delete lock, count=%d", count)
	}
}
