package controller

import (
	"testing"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

func TestDeleteQuotaPlan_TransactionCountError(t *testing.T) {
	setupSubTestDB(t)
	plan := database.QuotaPlan{
		Name:          "delete_count_error",
		DisplayName:   "Delete Count Error",
		ModelMatch:    `["*"]`,
		LimitUnit:     "request_count",
		LimitValue:    100,
		WindowSeconds: 0,
		Enabled:       boolPtr(true),
	}
	if err := database.DB.Create(&plan).Error; err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if err := database.DB.Exec("DROP TABLE package_plans").Error; err != nil {
		t.Fatalf("drop package_plans: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Delete("/admin/quota-plans/:id", DeleteQuotaPlan)
	code, resp := doJSON(t, app, "DELETE", "/admin/quota-plans/"+itoaUint(plan.ID), nil)
	if code != 500 || resp["message_code"] != "ERR_DB_DELETE" {
		t.Fatalf("delete with count error got %d/%v, want 500/ERR_DB_DELETE", code, resp["message_code"])
	}

	var count int64
	if err := database.DB.Model(&database.QuotaPlan{}).Where("id = ?", plan.ID).Count(&count).Error; err != nil {
		t.Fatalf("count plan: %v", err)
	}
	if count != 1 {
		t.Fatalf("plan count=%d, want 1", count)
	}
}

// TestIsValidOverflowStrategy_AcceptsCanonicalThree 锁定 2026-05-26 修复：
// overdraft 必须被 admin 接受（之前漏配导致 UI 选 overdraft 提交返回 400，
// 现役 plan 全卡在 block，用户用不满 100% 体验问题）。
func TestIsValidOverflowStrategy_AcceptsCanonicalThree(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"block", true},
		{"next_subscription", true},
		{"overdraft", true},          // ← 本次新增
		{"", false},                  // 空串拒绝（避免歧义）
		{"BLOCK", false},             // 大小写敏感
		{"allow", false},             // Sprint2-M4 已删
		{"degrade_model", false},     // Sprint2-M4 已删
		{"random-garbage", false},
	}
	for _, c := range cases {
		if got := isValidOverflowStrategy(c.in); got != c.want {
			t.Errorf("isValidOverflowStrategy(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
