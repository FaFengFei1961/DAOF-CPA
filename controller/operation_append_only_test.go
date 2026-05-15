// Package controller / operation_append_only_test.go
//
// 验证 OperationLog 的 append-only 不可篡改保证（Sprint1-P0-7 修复后）。
//
// 测试矩阵：
//   1. INSERT 正常 → 成功
//   2. UPDATE 字段 → GORM 因 `<-:create` 标签拒绝写入（值不变）
//   3. 通过 LogOperationBy 写入后 → 字段持久，且后续 Updates 不影响
//   4. 用户删除流程 → OperationLog 保留（与之前的 Unscoped().Delete 行为对比）
package controller

import (
	"testing"
	"time"

	"daof-ai-hub/database"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupOpLogTestDB 给本测试文件开独立 in-memory DB，避免污染其他测试。
func setupOpLogTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:oplog_append_only?mode=memory&cache=shared&_fk=1"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := db.AutoMigrate(&database.OperationLog{}, &database.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		// 清空表，避免后续测试看到本测试的脏数据
		db.Exec("DELETE FROM operation_logs")
		db.Exec("DELETE FROM users")
	})
}

// TestOperationLog_AppendOnly_RejectsUpdates 验证 GORM 层的 `<-:create` 写权限锁定：
// 已写入的 OperationLog 行任何 UPDATE 都会被 GORM 静默丢弃（字段不变）。
// 这是防 admin 篡改审计的第一道防线。
func TestOperationLog_AppendOnly_RejectsUpdates(t *testing.T) {
	setupOpLogTestDB(t)

	original := database.OperationLog{
		TargetUserID: 100,
		OperatorID:   1,
		OperatorRole: "admin",
		ActionType:   "BAN",
		IPAddress:    "192.168.1.10",
		Details:      `{"reason":"abuse"}`,
		CreatedAt:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := database.DB.Create(&original).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	// 尝试 UPDATE 所有业务字段——`<-:create` 应让所有字段更新被丢弃
	if err := database.DB.Model(&database.OperationLog{}).Where("id = ?", original.ID).Updates(map[string]any{
		"target_user_id": 999,
		"operator_id":    2,
		"operator_role":  "system",
		"action_type":    "UPDATE_QUOTA",
		"ip_address":     "10.0.0.1",
		"details":        `{"reason":"tampered"}`,
		"created_at":     time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
	}).Error; err != nil {
		t.Fatalf("update call: %v", err)
	}

	var got database.OperationLog
	if err := database.DB.First(&got, original.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}

	if got.TargetUserID != 100 {
		t.Errorf("TargetUserID tampered: got %d, want 100", got.TargetUserID)
	}
	if got.OperatorID != 1 {
		t.Errorf("OperatorID tampered: got %d, want 1", got.OperatorID)
	}
	if got.OperatorRole != "admin" {
		t.Errorf("OperatorRole tampered: got %q, want admin", got.OperatorRole)
	}
	if got.ActionType != "BAN" {
		t.Errorf("ActionType tampered: got %q, want BAN", got.ActionType)
	}
	if got.IPAddress != "192.168.1.10" {
		t.Errorf("IPAddress tampered: got %q, want 192.168.1.10", got.IPAddress)
	}
	if got.Details != `{"reason":"abuse"}` {
		t.Errorf("Details tampered: got %q, want original", got.Details)
	}
	if !got.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt tampered: got %v, want %v", got.CreatedAt, original.CreatedAt)
	}
}

// TestPurgeUserDependents_PreservesOperationLog 验证 purgeUserDependents 不再删除
// 目标用户的 OperationLog（修复 Sprint1-P0-7 中的审计链断裂问题）。
//
// 用户被 admin 删除时：
//   - 用户主表 PII 匿名化 + 软删除
//   - 用户的 access_tokens / api_logs / notifications / subscriptions 物理删除
//   - **OperationLog 保留**（审计链不可断）
func TestPurgeUserDependents_PreservesOperationLog(t *testing.T) {
	setupOpLogTestDB(t)

	// 给目标用户写两条审计：一条 admin 创建，一条 system 后续操作
	for _, log := range []database.OperationLog{
		{
			TargetUserID: 555,
			OperatorID:   1,
			OperatorRole: "admin",
			ActionType:   "CREATE_USER",
			IPAddress:    "10.0.0.1",
			Details:      `{"username":"alice"}`,
		},
		{
			TargetUserID: 555,
			OperatorID:   0,
			OperatorRole: "system",
			ActionType:   "AUTO_BAN",
			IPAddress:    "",
			Details:      `{"reason":"abuse_detected"}`,
		},
	} {
		if err := database.DB.Create(&log).Error; err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}

	// 给同一用户写一条 access_token、一条 api_log 验证它们会被 purge 删除
	// 同时不影响 OperationLog
	if err := database.DB.AutoMigrate(&database.AccessToken{}, &database.ApiLog{},
		&database.Notification{}, &database.NotificationBroadcastTarget{},
		&database.SubscriptionUsage{}, &database.UserSubscription{},
		&database.TopupOrder{}, &database.TopupRefund{}, &database.Ticket{}, &database.TicketMessage{},
		&database.NotificationPreference{}, &database.BillingEntry{}); err != nil {
		t.Fatalf("migrate dependents: %v", err)
	}
	if err := database.DB.Create(&database.AccessToken{UserID: 555, Key: "sk-test-555", Name: "T"}).Error; err != nil {
		t.Fatalf("seed access_token: %v", err)
	}

	// 执行 purge
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		return purgeUserDependents(tx, 555)
	}); err != nil {
		t.Fatalf("purge: %v", err)
	}

	// 验证 access_token 已删
	var atCount int64
	database.DB.Model(&database.AccessToken{}).Where("user_id = ?", 555).Count(&atCount)
	if atCount != 0 {
		t.Errorf("AccessToken should be purged: got %d rows", atCount)
	}

	// 验证 OperationLog 保留（审计链完整）
	var opLogs []database.OperationLog
	if err := database.DB.Where("target_user_id = ?", 555).Order("id ASC").Find(&opLogs).Error; err != nil {
		t.Fatalf("reload op_logs: %v", err)
	}
	if len(opLogs) != 2 {
		t.Fatalf("OperationLog should be preserved after purge: got %d rows, want 2", len(opLogs))
	}
	if opLogs[0].ActionType != "CREATE_USER" || opLogs[1].ActionType != "AUTO_BAN" {
		t.Errorf("audit chain corrupted: %+v", opLogs)
	}
}
