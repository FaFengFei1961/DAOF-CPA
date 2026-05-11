package database

import (
	"path/filepath"
	"testing"
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
