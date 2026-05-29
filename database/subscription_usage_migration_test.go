package database

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestSQLMigration_SubscriptionUsageAccountScoped 回归（2026-05-29）：
// 账号级时间窗口迁移必须彻底移除遗留 subscription_id 列。
// 该列带 NOT NULL 约束且无默认值，账号级 insert 不再写它 → 若残留则全部撞
// "NOT NULL constraint failed: subscription_usages.subscription_id" → 订阅消费永久失败、usage 恒为 0。
// 关键：DROP COLUMN 前必须先删所有引用该列的索引（SQLite 限制），否则 DROP 静默失败。
func TestSQLMigration_SubscriptionUsageAccountScoped(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:subusage_migration?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db

	// 旧 schema：subscription_id NOT NULL + 旧唯一索引 + GORM per-column 索引（正是会卡住 DROP COLUMN 的状态）
	if err := DB.Exec(`CREATE TABLE subscription_usages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		subscription_id INTEGER NOT NULL,
		quota_plan_id INTEGER NOT NULL,
		model_bucket TEXT NOT NULL,
		window_start_at datetime,
		window_end_at datetime,
		consumed_value REAL DEFAULT 0,
		consumed_value_micro_usd INTEGER DEFAULT 0,
		request_count INTEGER DEFAULT 0,
		created_at datetime,
		updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := DB.Exec(`CREATE UNIQUE INDEX idx_sub_plan_bucket ON subscription_usages(subscription_id, quota_plan_id, model_bucket)`).Error; err != nil {
		t.Fatalf("create legacy unique index: %v", err)
	}
	if err := DB.Exec(`CREATE INDEX idx_subscription_usages_subscription_id ON subscription_usages(subscription_id)`).Error; err != nil {
		t.Fatalf("create legacy per-column index: %v", err)
	}
	if err := DB.Exec(`INSERT INTO subscription_usages (subscription_id, quota_plan_id, model_bucket, consumed_value, request_count) VALUES (1, 3, '*', 5, 2)`).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	migrateSubscriptionUsageToAccountScoped()
	if err := DB.AutoMigrate(&SubscriptionUsage{}); err != nil {
		t.Fatalf("automigrate after migration: %v", err)
	}

	cols := sqliteColumnSetForMigrationTest(t, DB, "subscription_usages")
	if cols["subscription_id"] {
		t.Fatalf("legacy subscription_id column was not dropped — account-scoped inserts will fail with NOT NULL")
	}
	if !cols["user_id"] {
		t.Fatalf("user_id column missing after migration")
	}

	// 纯旧 schema 迁移会清空瞬时窗口计数器（旧行无 user_id，AutoMigrate 后会成孤儿）
	var count int64
	DB.Table("subscription_usages").Count(&count)
	if count != 0 {
		t.Fatalf("legacy rows should be cleared on pure-old-schema migration, got %d", count)
	}

	// 核心回归断言：账号级 insert 必须成功
	now := time.Now()
	row := SubscriptionUsage{UserID: 8, QuotaPlanID: 3, ModelBucket: "*", WindowStartAt: now, WindowEndAt: now.Add(time.Hour), ConsumedValueMicroUSD: 100, RequestCount: 1}
	if err := DB.Create(&row).Error; err != nil {
		t.Fatalf("account-scoped insert must succeed after migration, got: %v", err)
	}

	// 唯一约束按 (user_id, plan, bucket) 生效
	dup := SubscriptionUsage{UserID: 8, QuotaPlanID: 3, ModelBucket: "*", WindowStartAt: now, WindowEndAt: now.Add(time.Hour)}
	if err := DB.Create(&dup).Error; err == nil {
		t.Fatalf("duplicate (user,plan,bucket) must violate idx_user_plan_bucket")
	}
}

// TestSQLMigration_SubscriptionUsageRepairsHalfMigrated 幂等修复：
// 首次迁移漏删 per-column 索引 → DROP COLUMN 失败 → 残留 subscription_id NOT NULL 列 + 已有 user_id 列。
// 修复后的迁移必须能识别这种半损坏态并彻底清除 subscription_id（不能因 user_id 已存在就跳过）。
func TestSQLMigration_SubscriptionUsageRepairsHalfMigrated(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:subusage_repair?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db

	if err := DB.Exec(`CREATE TABLE subscription_usages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		subscription_id INTEGER NOT NULL,
		user_id INTEGER NOT NULL DEFAULT 0,
		quota_plan_id INTEGER NOT NULL,
		model_bucket TEXT NOT NULL,
		window_start_at datetime,
		window_end_at datetime,
		consumed_value REAL DEFAULT 0,
		consumed_value_micro_usd INTEGER DEFAULT 0,
		request_count INTEGER DEFAULT 0,
		created_at datetime,
		updated_at datetime
	)`).Error; err != nil {
		t.Fatalf("create half-migrated table: %v", err)
	}
	if err := DB.Exec(`CREATE INDEX idx_subscription_usages_subscription_id ON subscription_usages(subscription_id)`).Error; err != nil {
		t.Fatalf("create per-column index: %v", err)
	}
	if err := DB.Exec(`CREATE UNIQUE INDEX idx_user_plan_bucket ON subscription_usages(user_id, quota_plan_id, model_bucket)`).Error; err != nil {
		t.Fatalf("create account-scoped unique index: %v", err)
	}

	migrateSubscriptionUsageToAccountScoped()

	cols := sqliteColumnSetForMigrationTest(t, DB, "subscription_usages")
	if cols["subscription_id"] {
		t.Fatalf("half-migrated subscription_id column was not repaired/dropped")
	}

	now := time.Now()
	if err := DB.Create(&SubscriptionUsage{UserID: 6, QuotaPlanID: 4, ModelBucket: "*", WindowStartAt: now, WindowEndAt: now.Add(time.Hour)}).Error; err != nil {
		t.Fatalf("insert after half-migrated repair must succeed: %v", err)
	}
}
