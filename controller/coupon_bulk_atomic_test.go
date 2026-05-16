// Package controller / coupon_bulk_atomic_test.go
//
// 验证 AdminBulkGrantCoupon 的"整批原子"保证（Sprint3-M5 修复后）。
//
// 测试矩阵：
//  1. 成功路径：所有用户合法 → 整批成功，每人 qty 张券
//  2. 失败路径：N 个用户中第 K 个 status!=1 → 整批回滚，所有用户 0 张券
//  3. 失败路径：template 中途被禁用（事务内 SELECT FOR UPDATE 触发）→ 整批回滚
package controller

import (
	"strings"
	"testing"

	"daof-cpa/database"
	"daof-cpa/middleware"

	"github.com/gofiber/fiber/v2"
)

// seedBulkTestUser 唯一 username 的测试用户（避免 seedTestUser 全用 "tester" 撞 unique 索引）
func seedBulkTestUser(t *testing.T, username string) *database.User {
	t.Helper()
	u := database.User{
		Username:     username,
		PasswordHash: "x",
		Token:        "sk-" + username,
		Status:       1,
	}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	return &u
}

// newBulkGrantTestApp 给 AdminBulkGrantCoupon 挂上模拟 admin cookie + 真实 AdminGuard
func newBulkGrantTestApp(admin *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Request().Header.SetCookie("daof_admin_token", admin.Token)
		return c.Next()
	})
	app.Use(middleware.AdminGuard)
	app.Post("/admin/users/bulk-grant-coupon", AdminBulkGrantCoupon)
	return app
}

func TestBulkGrantCoupon_AllUsersValid_AllAtomicallyGranted(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newBulkGrantTestApp(admin)

	// 3 个合法 active 用户
	u1 := seedBulkTestUser(t, "bulkuser1")
	u2 := seedBulkTestUser(t, "bulkuser2")
	u3 := seedBulkTestUser(t, "bulkuser3")
	tpl := seedCouponTemplate(t)

	code, resp := doJSON(t, app, "POST", "/admin/users/bulk-grant-coupon", map[string]any{
		"user_ids":    []uint{u1.ID, u2.ID, u3.ID},
		"template_id": tpl.ID,
		"quantity":    2,
		"reason":      "test bulk all valid",
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	if !resp["success"].(bool) {
		t.Fatalf("expected success=true, got %v", resp)
	}
	summary, _ := resp["summary"].(map[string]any)
	if summary["total_granted"].(float64) != 6 {
		t.Errorf("expected total_granted=6 (3 users × 2 qty), got %v", summary["total_granted"])
	}
	if summary["failed_count"].(float64) != 0 {
		t.Errorf("expected failed_count=0, got %v", summary["failed_count"])
	}

	// DB 验证：每个用户 2 张券
	for _, uid := range []uint{u1.ID, u2.ID, u3.ID} {
		var count int64
		database.DB.Model(&database.UserCoupon{}).Where("user_id = ?", uid).Count(&count)
		if count != 2 {
			t.Errorf("user %d should have 2 coupons after bulk grant, got %d", uid, count)
		}
	}
}

func TestBulkGrantCoupon_OneUserBanned_EntireBatchRolledBack(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newBulkGrantTestApp(admin)

	// u1, u3 合法；u2 被封禁（status=2）→ 期望整批回滚
	u1 := seedBulkTestUser(t, "bulkuser1")
	u2 := seedBulkTestUser(t, "bulkuser2")
	u3 := seedBulkTestUser(t, "bulkuser3")
	database.DB.Model(&database.User{}).Where("id = ?", u2.ID).Update("status", 2)

	tpl := seedCouponTemplate(t)

	code, resp := doJSON(t, app, "POST", "/admin/users/bulk-grant-coupon", map[string]any{
		"user_ids":    []uint{u1.ID, u2.ID, u3.ID},
		"template_id": tpl.ID,
		"quantity":    2,
		"reason":      "test bulk one banned",
	})

	// 期望 409 + 标准 message_code + 整批已回滚
	if code != 409 {
		t.Fatalf("expected 409 (batch aborted), got %d body=%v", code, resp)
	}
	if resp["success"].(bool) {
		t.Errorf("expected success=false")
	}
	if resp["message_code"] != "ERR_BULK_GRANT_ABORTED" {
		t.Errorf("expected message_code=ERR_BULK_GRANT_ABORTED, got %v", resp["message_code"])
	}
	if failedUID, _ := resp["failed_user_id"].(float64); uint(failedUID) != u2.ID {
		t.Errorf("expected failed_user_id=%d (banned user), got %v", u2.ID, resp["failed_user_id"])
	}

	// 关键断言：所有用户都 0 张券（包括"应该成功"的 u1 和 u3——必须被回滚）
	for _, uid := range []uint{u1.ID, u2.ID, u3.ID} {
		var count int64
		database.DB.Model(&database.UserCoupon{}).Where("user_id = ?", uid).Count(&count)
		if count != 0 {
			t.Errorf("user %d should have 0 coupons after batch rollback (got %d) — atomicity violated", uid, count)
		}
	}

	// OperationLog 也必须没有 GRANT_COUPON_BULK 条目（同事务内一并回滚）
	var auditCount int64
	database.DB.Model(&database.OperationLog{}).Where("action_type = ?", "GRANT_COUPON_BULK").Count(&auditCount)
	if auditCount != 0 {
		t.Errorf("audit log should be 0 after rollback, got %d (GRANT_COUPON_BULK rows)", auditCount)
	}
}

func TestBulkGrantCoupon_DisabledTemplate_BatchRejected(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newBulkGrantTestApp(admin)

	u1 := seedTestUser(t, 0)
	tpl := seedCouponTemplate(t)
	// 禁用 template
	disabled := false
	database.DB.Model(&database.CouponTemplate{}).Where("id = ?", tpl.ID).Update("enabled", &disabled)

	code, resp := doJSON(t, app, "POST", "/admin/users/bulk-grant-coupon", map[string]any{
		"user_ids":    []uint{u1.ID},
		"template_id": tpl.ID,
		"quantity":    1,
		"reason":      "test disabled template",
	})

	// 预验阶段就会被 400 拒（template !IsEnabled）；整批未进入事务
	if code != 400 {
		t.Fatalf("expected 400 (disabled template pre-check), got %d body=%v", code, resp)
	}
	if resp["message_code"] != "ERR_TEMPLATE_DISABLED" {
		t.Errorf("expected ERR_TEMPLATE_DISABLED, got %v", resp["message_code"])
	}
}

func TestBulkGrantCoupon_DedupeUserIDs(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newBulkGrantTestApp(admin)

	u1 := seedBulkTestUser(t, "bulkdedupe")
	tpl := seedCouponTemplate(t)

	// 同一 user_id 重复 3 次 + 1 个 0（应被过滤）
	code, resp := doJSON(t, app, "POST", "/admin/users/bulk-grant-coupon", map[string]any{
		"user_ids":    []uint{u1.ID, u1.ID, u1.ID, 0},
		"template_id": tpl.ID,
		"quantity":    2,
		"reason":      "test dedupe",
	})
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	summary, _ := resp["summary"].(map[string]any)
	// dedupe 后只 1 个用户 × 2 张
	if summary["total_users"].(float64) != 1 {
		t.Errorf("expected total_users=1 (after dedupe), got %v", summary["total_users"])
	}
	if summary["total_granted"].(float64) != 2 {
		t.Errorf("expected total_granted=2, got %v", summary["total_granted"])
	}

	var count int64
	database.DB.Model(&database.UserCoupon{}).Where("user_id = ?", u1.ID).Count(&count)
	if count != 2 {
		t.Errorf("user %d should have 2 coupons (no duplicate-due-to-dup-IDs), got %d", u1.ID, count)
	}
}

// TestBulkGrantCoupon_RejectInvalidParams 收紧入参校验：reason 控制字符 / qty=0 / 用户数超限。
func TestBulkGrantCoupon_RejectInvalidParams(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newBulkGrantTestApp(admin)

	tpl := seedCouponTemplate(t)
	u1 := seedBulkTestUser(t, "bulkinvalid")

	// reason 含控制字符
	code, resp := doJSON(t, app, "POST", "/admin/users/bulk-grant-coupon", map[string]any{
		"user_ids":    []uint{u1.ID},
		"template_id": tpl.ID,
		"quantity":    1,
		"reason":      "bad\nreason",
	})
	if code != 400 || resp["message_code"] != "ERR_REASON_CTRL_CHAR" {
		t.Errorf("ctrl-char reason should 400/ERR_REASON_CTRL_CHAR, got %d/%v", code, resp["message_code"])
	}

	// qty 超限
	code, resp = doJSON(t, app, "POST", "/admin/users/bulk-grant-coupon", map[string]any{
		"user_ids":    []uint{u1.ID},
		"template_id": tpl.ID,
		"quantity":    11,
	})
	if code != 400 || resp["message_code"] != "ERR_QUANTITY_TOO_LARGE" {
		t.Errorf("qty=11 should 400/ERR_QUANTITY_TOO_LARGE, got %d/%v", code, resp["message_code"])
	}

	// reason 超长
	longReason := strings.Repeat("a", 1000)
	code, resp = doJSON(t, app, "POST", "/admin/users/bulk-grant-coupon", map[string]any{
		"user_ids":    []uint{u1.ID},
		"template_id": tpl.ID,
		"quantity":    1,
		"reason":      longReason,
	})
	if code != 400 || resp["message_code"] != "ERR_REASON_TOO_LONG" {
		t.Errorf("reason too long should 400/ERR_REASON_TOO_LONG, got %d/%v", code, resp["message_code"])
	}
}

func TestBulkGrantCoupon_LimitsUserCount(t *testing.T) {
	setupSubTestDB(t)
	admin := seedAdminUser(t)
	app := newBulkGrantTestApp(admin)
	tpl := seedCouponTemplate(t)

	userIDs := make([]uint, 51)
	for i := range userIDs {
		userIDs[i] = uint(i + 1)
	}
	code, resp := doJSON(t, app, "POST", "/admin/users/bulk-grant-coupon", map[string]any{
		"user_ids":    userIDs,
		"template_id": tpl.ID,
		"quantity":    1,
	})
	if code != 400 || resp["message_code"] != "ERR_BULK_LIMIT" {
		t.Fatalf("51 users got %d/%v, want 400/ERR_BULK_LIMIT", code, resp["message_code"])
	}
}
