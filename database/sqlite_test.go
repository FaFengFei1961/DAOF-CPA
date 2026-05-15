package database

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestInitDB(t *testing.T) {
	// fix Major（codex 第八轮）：原测试直接对 cwd 下的 daofa-hub.db 做 Remove + 重建，
	// 在生产/调试目录里跑测试会把真实数据库抹掉。改为放 t.TempDir 下，测试结束自动清理。
	//
	// 必须先注册 DB.Close cleanup（LIFO 顺序：先于 t.TempDir 的 RemoveAll 执行），
	// 否则 SQLite 句柄仍持有文件 → tempdir cleanup unlinkat 失败 → 测试 FAIL。
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test-init.db")
	t.Setenv("DAOF_DB_PATH", dbPath)
	t.Cleanup(func() {
		if DB != nil {
			if sqlDB, err := DB.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
	})

	// First run inserts root
	InitDB()

	var admin User
	DB.Where("role = ?", "admin").First(&admin)
	if admin.Username != "root" {
		t.Errorf("Expected root admin to be populated")
	}

	// Second run shouldn't double insert
	InitDB()
	var count int64
	DB.Model(&User{}).Where("role = ?", "admin").Count(&count)
	if count != 1 {
		t.Errorf("Expected 1 admin, got %d", count)
	}
}

func TestSQLMigration_QuotaPlanLimitValueGoDecimal(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:quota_migration?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db
	if err := DB.Exec(`CREATE TABLE quota_plans (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		limit_unit TEXT NOT NULL,
		limit_value REAL NOT NULL,
		limit_value_micro_usd INTEGER NOT NULL DEFAULT 0
	)`).Error; err != nil {
		t.Fatalf("create quota_plans: %v", err)
	}
	if err := DB.Exec(`INSERT INTO quota_plans (limit_unit, limit_value, limit_value_micro_usd)
		VALUES ('api_cost_usd', 12.345678, 0)`).Error; err != nil {
		t.Fatalf("insert quota plan: %v", err)
	}

	backfillQuotaPlanLimitMicroUSD()

	var got int64
	if err := DB.Raw(`SELECT limit_value_micro_usd FROM quota_plans WHERE id = 1`).Scan(&got).Error; err != nil {
		t.Fatalf("read migrated limit: %v", err)
	}
	if got != 12_345_678 {
		t.Fatalf("limit_value_micro_usd=%d want 12345678", got)
	}
}

func TestSQLMigration_ChannelModelPriceGoDecimal(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:channel_price_migration?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db
	if err := DB.Exec(`CREATE TABLE channel_models (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		input_price REAL NOT NULL DEFAULT 0
	)`).Error; err != nil {
		t.Fatalf("create channel_models: %v", err)
	}
	if err := DB.Exec(`INSERT INTO channel_models (input_price) VALUES (0.123456789)`).Error; err != nil {
		t.Fatalf("insert channel model: %v", err)
	}

	migrateChannelModelFixedPointPricing()

	columns := sqliteColumnSetForMigrationTest(t, DB, "channel_models")
	if columns["input_price"] {
		t.Fatalf("legacy input_price column was not dropped")
	}
	if !columns["input_price_pico_per_token"] {
		t.Fatalf("input_price_pico_per_token column missing")
	}
	var got int64
	if err := DB.Raw(`SELECT input_price_pico_per_token FROM channel_models WHERE id = 1`).Scan(&got).Error; err != nil {
		t.Fatalf("read migrated price: %v", err)
	}
	if got != 123_456_789 {
		t.Fatalf("input_price_pico_per_token=%d want 123456789", got)
	}
}

func sqliteColumnSetForMigrationTest(t *testing.T, db *gorm.DB, table string) map[string]bool {
	t.Helper()
	var rows []struct {
		Name string
	}
	if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&rows).Error; err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.Name] = true
	}
	return out
}
