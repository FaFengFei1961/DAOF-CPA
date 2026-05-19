package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

func TestVideoGeneration_BalanceBillingWritesUsageLine(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != database.EndpointVideosGenerations {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Fatalf("unexpected upstream auth %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-idempotency-key") != "client-job-1" {
			t.Fatalf("idempotency key was not forwarded: %q", r.Header.Get("x-idempotency-key"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		if body["model"] != "grok-imagine-video" ||
			body["duration"] != float64(6) ||
			body["aspect_ratio"] != "16:9" ||
			body["resolution"] != "720p" {
			t.Fatalf("unexpected sanitized video body: %#v", body)
		}
		if _, ok := body["seconds"]; ok {
			t.Fatalf("sanitized native video body must not forward seconds: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"vid_123","status":"queued"}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{
		ID:                    1,
		Username:              "u",
		Token:                 "sk-user",
		Role:                  "user",
		Status:                1,
		Quota:                 1_000_000,
		BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&database.ModelPricingRule{
		RuleKey:         "test|grok-imagine-video|video_second|output|720p",
		PricingVersion:  "test",
		ProviderKey:     "xai",
		ModelID:         "grok-imagine-video",
		OfficialModelID: "grok-imagine-video",
		BillingMode:     database.BillingModeVideoSecond,
		Unit:            "video_second",
		Direction:       "output",
		Resolution:      "720p",
		PriceMicroUSD:   70_000,
	}).Error; err != nil {
		t.Fatalf("seed pricing: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["grok-imagine-video"] = []*database.ChannelModel{{
		ID:               1,
		ChannelID:        1,
		ModelID:          "grok-imagine-video",
		ModelCategory:    database.ModelCategoryVideo,
		BillingMode:      database.BillingModeVideoSecond,
		AllowedEndpoints: database.DefaultAllowedEndpointsForCategory(database.ModelCategoryVideo),
		Weight:           1,
		Status:           1,
	}}

	app := fiber.New()
	app.Post(database.EndpointVideosGenerations, VideoGenerationProxyHandler)
	req := httptest.NewRequest(http.MethodPost, database.EndpointVideosGenerations,
		bytes.NewBufferString(`{"model":"xai/grok-imagine-video","prompt":"animate a clean product shot","seconds":"6","size":"1280x720"}`))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-idempotency-key", "client-job-1")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}

	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.Quota != 580_000 {
		t.Fatalf("quota=%d want 580000", fresh.Quota)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("model_name = ?", "grok-imagine-video").First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Unit != "video_second" || line.Quantity != 6 || line.AmountMicroUSD != 420_000 || line.UnitPriceMicro != 70_000 || line.CostSource != "official_matrix" || line.Resolution != "720p" {
		t.Fatalf("unexpected usage line: %#v", line)
	}
	var bill database.BillingEntry
	if err := db.Where("entry_type = ?", database.BillingTypeApiConsumeBalance).First(&bill).Error; err != nil {
		t.Fatalf("load billing entry: %v", err)
	}
	if bill.AmountUSD != -420_000 || bill.BalanceAfterUSD != 580_000 {
		t.Fatalf("unexpected billing entry: %#v", bill)
	}
	var revenue database.ApiLogRevenue
	if err := db.First(&revenue).Error; err != nil {
		t.Fatalf("load revenue: %v", err)
	}
	if revenue.EffectiveRevenueMicroUSD != 420_000 || revenue.RevenueSource != database.RevenueSourceBalance {
		t.Fatalf("unexpected revenue: %#v", revenue)
	}
	var job database.MediaGenerationJob
	if err := db.Where("request_id = ?", "vid_123").First(&job).Error; err != nil {
		t.Fatalf("load video job: %v", err)
	}
	if job.UserID != user.ID || job.ChannelID != 1 || job.ModelName != "grok-imagine-video" {
		t.Fatalf("unexpected video job: %#v", job)
	}
}

func TestParseVideoGenerationRequestRejectsInputs(t *testing.T) {
	_, _, err := parseVideoGenerationRequest([]byte(`{"model":"grok-imagine-video","prompt":"x","image_url":"https://example.test/a.png"}`))
	if err == nil {
		t.Fatal("expected image_url to be rejected")
	}
}

func TestParseVideoGenerationRequestNativeDefaultsAndClamp(t *testing.T) {
	req, sanitized, err := parseVideoGenerationRequest([]byte(`{"model":"grok/grok-imagine-video","prompt":"x","duration":22,"size":"720x1280","resolution":"480p","stream":false}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.Model != "grok-imagine-video" || req.DurationSeconds != 15 || req.AspectRatio != "9:16" || req.Resolution != "480p" {
		t.Fatalf("unexpected normalized request: %#v", req)
	}
	var body map[string]any
	if err := json.Unmarshal(sanitized, &body); err != nil {
		t.Fatalf("decode sanitized: %v", err)
	}
	if body["duration"] != float64(15) || body["aspect_ratio"] != "9:16" || body["resolution"] != "480p" {
		t.Fatalf("unexpected sanitized body: %#v", body)
	}
}

func TestParseVideoGenerationRequestRejectsUserField(t *testing.T) {
	_, _, err := parseVideoGenerationRequest([]byte(`{"model":"grok-imagine-video","prompt":"x","user":"tag"}`))
	if err == nil {
		t.Fatal("expected user to be rejected")
	}
}

func TestParseVideoGenerationRequestOfficialDefaults(t *testing.T) {
	req, sanitized, err := parseVideoGenerationRequest([]byte(`{"model":"grok-imagine-video","prompt":"x"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.DurationSeconds != 4 || req.AspectRatio != "16:9" || req.Resolution != "480p" {
		t.Fatalf("unexpected default video request: %#v", req)
	}
	var body map[string]any
	if err := json.Unmarshal(sanitized, &body); err != nil {
		t.Fatalf("decode sanitized: %v", err)
	}
	if body["duration"] != float64(4) || body["aspect_ratio"] != "16:9" || body["resolution"] != "480p" {
		t.Fatalf("unexpected sanitized defaults: %#v", body)
	}
}

func TestParseVideoGenerationRequestRejectsConflictingSecondsDuration(t *testing.T) {
	_, _, err := parseVideoGenerationRequest([]byte(`{"model":"grok-imagine-video","prompt":"x","seconds":4,"duration":5}`))
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("err=%v want conflict", err)
	}
}

func TestCostTicksFromVideoResponse(t *testing.T) {
	req := videoGenerationRequest{Model: "grok-imagine-video", Prompt: "x", DurationSeconds: 6, Resolution: "720p"}
	price, err := resolveVideoPrice(req, 500_000_000)
	if err != nil {
		t.Fatalf("resolve ticks: %v", err)
	}
	if price.AmountMicroUSD != 50_000 || price.UnitPriceMicro != 8_334 || price.CostSource != "upstream_usage" {
		t.Fatalf("unexpected tick price: %#v", price)
	}
}

func TestVideoBalanceInsufficientWritesPendingReconcile(t *testing.T) {
	db := setupImageGenerationTest(t)
	user := database.User{
		ID:                          7,
		Username:                    "short",
		Token:                       "sk-short",
		Status:                      1,
		Quota:                       10_000,
		BalanceConsumeEnabled:       true,
		BalanceConsumedInWindow:     1_234,
		BalanceConsumeLimitUSD:      100_000,
		BalanceConsumeWindowSeconds: 3600,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	req := videoGenerationRequest{Model: "grok-imagine-video", Prompt: "x", DurationSeconds: 4, Resolution: "720p", Size: defaultVideoSize, AspectRatio: defaultVideoAspectRatio}
	price := videoPriceResolution{
		Quantity:       4,
		UnitPriceMicro: 70_000,
		AmountMicroUSD: 280_000,
		Resolution:     "720p",
		CostSource:     "official_matrix",
	}
	billing := BillingRuleResolution{
		RequestedModel:      req.Model,
		ServedModel:         req.Model,
		ModelWeight:         1,
		HealthMultiplier:    1,
		BillingRulesVersion: "test",
		RawCostMicroUSD:     price.AmountMicroUSD,
		ChargedCostMicroUSD: price.AmountMicroUSD,
	}

	apiLogID, effectiveRevenue, _ := deductVideoBalanceAndLog(&user, user.Token, req, price, billing, "cliproxy", 200, "127.0.0.1", database.EndpointVideosGenerations, time.Now())
	if apiLogID == 0 {
		t.Fatal("expected api log id")
	}
	if effectiveRevenue != 0 {
		t.Fatalf("effective revenue=%d want 0 for pending reconcile", effectiveRevenue)
	}
	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.Quota != 10_000 {
		t.Fatalf("quota=%d want unchanged 10000", fresh.Quota)
	}
	// fix H2 (2026-05-19)：window tracking 改为先于 CAS quota 调用（与 text path 对齐），
	// 即使 CAS 失败（余额不足）window 也会记录"尝试消费"，与 text 路径一致。
	// 这里 user.BalanceConsumeWindowStartAt 是 nil（首次进入），TryConsumeBalanceTx
	// 会先 reset window（清 1234 → 0），再 forceTrack 累加 280000 → 最终 280000。
	if fresh.BalanceConsumedInWindow != 280_000 {
		t.Fatalf("balance window consumed=%d want 280000 (H2 fix: window resets then tracks 280000)", fresh.BalanceConsumedInWindow)
	}
	var pending database.BillingEntry
	if err := db.Where("entry_type = ?", database.BillingTypeApiUsagePendingReconcile).First(&pending).Error; err != nil {
		t.Fatalf("load pending billing entry: %v", err)
	}
	if pending.RelatedID != apiLogID || pending.EstimatedCostUSD != 280_000 {
		t.Fatalf("unexpected pending entry: %#v", pending)
	}
	var revenueCount int64
	if err := db.Model(&database.ApiLogRevenue{}).Count(&revenueCount).Error; err != nil {
		t.Fatalf("count revenue: %v", err)
	}
	if revenueCount != 0 {
		t.Fatalf("pending reconcile must not create revenue rows, got %d", revenueCount)
	}
}

func TestVideoRetrieveRequiresOwnerAndUsesOriginalChannel(t *testing.T) {
	db := setupImageGenerationTest(t)
	called := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/videos/vid_123" {
			t.Fatalf("unexpected retrieve request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Fatalf("unexpected upstream auth %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"done","video":{"url":"https://vidgen.example/video.mp4","duration":6},"usage":{"cost_in_usd_ticks":500000000}}`))
	}))
	t.Cleanup(backend.Close)

	owner := database.User{ID: 11, Username: "owner", Token: "sk-owner", Status: 1}
	other := database.User{ID: 12, Username: "other", Token: "sk-other", Status: 1}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("seed other: %v", err)
	}
	if err := db.Create(&database.Channel{ID: 9, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := db.Create(&database.MediaGenerationJob{
		RequestID:   "vid_123",
		UserID:      owner.ID,
		ChannelID:   9,
		ModelName:   "grok-imagine-video",
		RequestPath: database.EndpointVideosGenerations,
	}).Error; err != nil {
		t.Fatalf("seed job: %v", err)
	}
	AuthCache[owner.Token] = &owner
	AuthCache[other.Token] = &other

	app := fiber.New()
	app.Get("/v1/videos/:request_id", VideoRetrieveProxyHandler)

	req := httptest.NewRequest(http.MethodGet, "/v1/videos/vid_123", nil)
	req.Header.Set("Authorization", "Bearer "+other.Token)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("other request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other status=%d want 404", resp.StatusCode)
	}
	if called != 0 {
		t.Fatalf("backend must not be called for non-owner")
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/videos/vid_123", nil)
	req.Header.Set("Authorization", "Bearer "+owner.Token)
	resp, err = app.Test(req, -1)
	if err != nil {
		t.Fatalf("owner request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner status=%d want 200", resp.StatusCode)
	}
	if called != 1 {
		t.Fatalf("backend calls=%d want 1", called)
	}
	var logRow database.ApiLog
	if err := db.Where("user_id = ? AND request_path = ?", owner.ID, "/v1/videos/vid_123").First(&logRow).Error; err != nil {
		t.Fatalf("load retrieve api log: %v", err)
	}
	if logRow.Cost != 0 || logRow.Status != http.StatusOK {
		t.Fatalf("unexpected retrieve api log: %#v", logRow)
	}
}
