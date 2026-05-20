// Package controller / subscription_snapshot_test.go
//
// 单元测试覆盖 subscription_snapshot.go 的 4 个函数。
//
// readPackageNameFromSnapshot 是纯字符串解析，不需要 DB。
// buildPackageSnapshot[Tx] + getNextStackIndex 都需要 DB（query Plans / Subscriptions），
// 使用 setupSubTestDB（subscription_integration_test.go）的内存 SQLite。
//
// Phase E-2（2026-05-19）：补齐 D-5 拆分后这些函数的直接测试覆盖。
package controller

import (
	"encoding/json"
	"testing"
	"time"

	"daof-cpa/database"
)

func TestReadPackageNameFromSnapshot(t *testing.T) {
	tests := []struct {
		name     string
		snapJSON string
		want     string
	}{
		{"empty string returns empty", "", ""},
		{"malformed json returns empty", "not json", ""},
		{"missing package_name field returns empty", `{"package_id":1}`, ""},
		{"normal package_name extracted", `{"package_name":"Pro Plan"}`, "Pro Plan"},
		{"chinese package_name", `{"package_name":"高级套餐"}`, "高级套餐"},
		{"empty package_name string", `{"package_name":""}`, ""},
		{"extra fields ignored", `{"package_name":"Basic","price_amount":1000,"plans":[]}`, "Basic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := readPackageNameFromSnapshot(tc.snapJSON)
			if got != tc.want {
				t.Errorf("readPackageNameFromSnapshot(%q) = %q; want %q", tc.snapJSON, got, tc.want)
			}
		})
	}
}

func TestBuildPackageSnapshot(t *testing.T) {
	setupSubTestDB(t)

	t.Run("package without plans serializes basic fields", func(t *testing.T) {
		pkg := database.Package{
			Name:                 "Bare Pkg",
			ProductType:          "subscription",
			PriceAmount:          5_000_000, // $5
			PriceCurrency:        "USD",
			BillingPeriodSeconds: 30 * 24 * 3600,
			Public:               true,
			Enabled:              boolPtr(true),
		}
		if err := database.DB.Create(&pkg).Error; err != nil {
			t.Fatalf("seed pkg: %v", err)
		}
		got, err := buildPackageSnapshot(&pkg)
		if err != nil {
			t.Fatalf("build snapshot: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if parsed["package_name"] != "Bare Pkg" {
			t.Errorf("package_name=%v want Bare Pkg", parsed["package_name"])
		}
		if parsed["product_type"] != "subscription" {
			t.Errorf("product_type=%v want subscription", parsed["product_type"])
		}
		// price_amount 是 int64，JSON 反序列化为 float64
		if parsed["price_amount"].(float64) != 5_000_000 {
			t.Errorf("price_amount=%v want 5000000", parsed["price_amount"])
		}
		if parsed["schema_version"].(float64) != float64(database.PackageSnapshotCurrentVersion) {
			t.Errorf("schema_version=%v", parsed["schema_version"])
		}
	})

	t.Run("missing product_type defaults to subscription", func(t *testing.T) {
		pkg := database.Package{
			Name:                 "No Type Pkg",
			ProductType:          "", // explicit empty
			PriceAmount:          1_000_000,
			PriceCurrency:        "USD",
			BillingPeriodSeconds: 86400,
			Public:               true,
			Enabled:              boolPtr(true),
		}
		if err := database.DB.Create(&pkg).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		got, err := buildPackageSnapshot(&pkg)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		var parsed map[string]any
		_ = json.Unmarshal([]byte(got), &parsed)
		if parsed["product_type"] != "subscription" {
			t.Errorf("empty ProductType should default to 'subscription', got %v", parsed["product_type"])
		}
	})

	t.Run("with enabled plan", func(t *testing.T) {
		plan := database.QuotaPlan{
			Name:          "test-plan-with-enabled",
			DisplayName:   "Test",
			ModelMatch:    `["gpt-4"]`,
			LimitUnit:     "api_calls",
			LimitValue:    1000,
			WindowSeconds: 3600,
			Priority:      1,
			Enabled:       boolPtr(true),
		}
		if err := database.DB.Create(&plan).Error; err != nil {
			t.Fatalf("seed plan: %v", err)
		}
		pkg := database.Package{
			Name:                 "Pkg With Plan",
			PriceAmount:          2_000_000,
			PriceCurrency:        "USD",
			BillingPeriodSeconds: 86400,
			Public:               true,
			Enabled:              boolPtr(true),
		}
		if err := database.DB.Create(&pkg).Error; err != nil {
			t.Fatalf("seed pkg: %v", err)
		}
		if err := database.DB.Create(&database.PackagePlan{
			PackageID:          pkg.ID,
			QuotaPlanID:        plan.ID,
			QuantityMultiplier: 2.0,
		}).Error; err != nil {
			t.Fatalf("seed pkgplan: %v", err)
		}
		got, err := buildPackageSnapshot(&pkg)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		var parsed struct {
			Plans []struct {
				ID                 uint    `json:"id"`
				Name               string  `json:"name"`
				LimitUnit          string  `json:"limit_unit"`
				QuantityMultiplier float64 `json:"quantity_multiplier"`
			} `json:"plans"`
		}
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(parsed.Plans) != 1 {
			t.Fatalf("want 1 plan, got %d", len(parsed.Plans))
		}
		if parsed.Plans[0].QuantityMultiplier != 2.0 {
			t.Errorf("multiplier=%g want 2.0", parsed.Plans[0].QuantityMultiplier)
		}
		if parsed.Plans[0].LimitUnit != "api_calls" {
			t.Errorf("limit_unit=%q want api_calls", parsed.Plans[0].LimitUnit)
		}
	})

	t.Run("disabled plan in package fails snapshot", func(t *testing.T) {
		// fix MAJOR R23+3-B6：所有绑定的 plan 必须 enabled
		disabledPlan := database.QuotaPlan{
			Name:        "disabled-plan-test",
			DisplayName: "Disabled",
			ModelMatch:  `["*"]`,
			LimitUnit:   "api_calls",
			LimitValue:  100,
			Priority:    1,
			Enabled:     boolPtr(false), // explicitly disabled
		}
		if err := database.DB.Create(&disabledPlan).Error; err != nil {
			t.Fatalf("seed plan: %v", err)
		}
		pkg := database.Package{
			Name:                 "Pkg Disabled Plan",
			PriceAmount:          1_000_000,
			PriceCurrency:        "USD",
			BillingPeriodSeconds: 86400,
			Public:               true,
			Enabled:              boolPtr(true),
		}
		if err := database.DB.Create(&pkg).Error; err != nil {
			t.Fatalf("seed pkg: %v", err)
		}
		if err := database.DB.Create(&database.PackagePlan{
			PackageID:          pkg.ID,
			QuotaPlanID:        disabledPlan.ID,
			QuantityMultiplier: 1,
		}).Error; err != nil {
			t.Fatalf("seed pkgplan: %v", err)
		}
		_, err := buildPackageSnapshot(&pkg)
		if err == nil {
			t.Error("buildPackageSnapshot should fail when bound plan is disabled (fail-closed B6)")
		}
	})

	t.Run("api_cost_usd plan without micro_usd fails", func(t *testing.T) {
		plan := database.QuotaPlan{
			Name:               "cost-plan-no-micro",
			DisplayName:        "Cost",
			ModelMatch:         `["*"]`,
			LimitUnit:          "api_cost_usd",
			LimitValue:         5.0,
			LimitValueMicroUSD: 0, // missing!
			Priority:           1,
			Enabled:            boolPtr(true),
		}
		if err := database.DB.Create(&plan).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
		pkg := database.Package{
			Name:                 "Bad Cost Pkg",
			PriceAmount:          1_000_000,
			PriceCurrency:        "USD",
			BillingPeriodSeconds: 86400,
			Public:               true,
			Enabled:              boolPtr(true),
		}
		if err := database.DB.Create(&pkg).Error; err != nil {
			t.Fatalf("seed pkg: %v", err)
		}
		if err := database.DB.Create(&database.PackagePlan{
			PackageID:          pkg.ID,
			QuotaPlanID:        plan.ID,
			QuantityMultiplier: 1,
		}).Error; err != nil {
			t.Fatalf("seed pkgplan: %v", err)
		}
		_, err := buildPackageSnapshot(&pkg)
		if err == nil {
			t.Error("api_cost_usd plan with LimitValueMicroUSD=0 should fail to snapshot")
		}
	})
}

func TestGetNextStackIndex(t *testing.T) {
	setupSubTestDB(t)

	user := seedTestUser(t, 100)
	pkg := seedPackage(t)

	t.Run("first stack index is 1", func(t *testing.T) {
		idx, err := getNextStackIndex(database.DB, user.ID, pkg.ID)
		if err != nil {
			t.Fatalf("get index: %v", err)
		}
		if idx != 1 {
			t.Errorf("first index=%d want 1", idx)
		}
	})

	t.Run("after seeding stack_index=3 returns 4", func(t *testing.T) {
		sub := database.UserSubscription{
			UserID:     user.ID,
			PackageID:  pkg.ID,
			StackIndex: 3,
			Status:     "active",
			StartAt:    time.Now(),
			EndAt:      time.Now().Add(30 * 24 * time.Hour),
		}
		if err := database.DB.Create(&sub).Error; err != nil {
			t.Fatalf("seed sub: %v", err)
		}
		idx, err := getNextStackIndex(database.DB, user.ID, pkg.ID)
		if err != nil {
			t.Fatalf("get index: %v", err)
		}
		if idx != 4 {
			t.Errorf("next index=%d want 4 (3+1)", idx)
		}
	})

	t.Run("different user starts from 1", func(t *testing.T) {
		otherUser := database.User{Username: "other", PasswordHash: "x", Token: "sk-other", Status: 1}
		if err := database.DB.Create(&otherUser).Error; err != nil {
			t.Fatalf("seed other: %v", err)
		}
		idx, err := getNextStackIndex(database.DB, otherUser.ID, pkg.ID)
		if err != nil {
			t.Fatalf("get index: %v", err)
		}
		if idx != 1 {
			t.Errorf("other user first index=%d want 1", idx)
		}
	})

	t.Run("different package starts from 1", func(t *testing.T) {
		otherPkg := database.Package{
			Name:                 "OtherPkg",
			PriceAmount:          1_000_000,
			PriceCurrency:        "USD",
			BillingPeriodSeconds: 86400,
			Public:               true,
			Enabled:              boolPtr(true),
		}
		if err := database.DB.Create(&otherPkg).Error; err != nil {
			t.Fatalf("seed other pkg: %v", err)
		}
		idx, err := getNextStackIndex(database.DB, user.ID, otherPkg.ID)
		if err != nil {
			t.Fatalf("get index: %v", err)
		}
		if idx != 1 {
			t.Errorf("different pkg index=%d want 1", idx)
		}
	})
}
