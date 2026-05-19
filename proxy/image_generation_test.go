package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupImageGenerationTest(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&database.User{},
		&database.AccessToken{},
		&database.Channel{},
		&database.ChannelModel{},
		&database.ModelCatalog{},
		&database.ModelPricingRule{},
		&database.ApiLog{},
		&database.ApiLogUsageLine{},
		&database.MediaGenerationJob{},
		&database.ApiLogRevenue{},
		&database.BillingEntry{},
		&database.UserSubscription{},
		&database.SubscriptionUsage{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	oldDB := database.DB
	oldAuth := AuthCache
	oldAuthTokens := AuthTokenCache
	oldRoutes := RouteCache
	oldChannels := ChannelMapCache
	oldSys := SysConfigCache
	database.DB = db
	AuthCache = map[string]*database.User{}
	AuthTokenCache = map[string]*database.AccessToken{}
	RouteCache = map[string][]*database.ChannelModel{}
	ChannelMapCache = map[uint]*database.Channel{}
	SysConfigCache = map[string]string{"subscription_engine_fallback_to_quota": "true"}
	FlushAllSubscriptionCache()
	t.Cleanup(func() {
		database.DB = oldDB
		AuthCache = oldAuth
		AuthTokenCache = oldAuthTokens
		RouteCache = oldRoutes
		ChannelMapCache = oldChannels
		SysConfigCache = oldSys
		FlushAllSubscriptionCache()
	})
	return db
}

func TestImageGeneration_BalanceBillingWritesUsageLine(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != database.EndpointImagesGenerations {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-key" {
			t.Fatalf("unexpected upstream auth %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		if body["resolution"] != "1k" {
			t.Fatalf("sanitized body should include normalized resolution, got %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://example.test/a.png"},{"url":"https://example.test/b.png"}]}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{
		ID:                    1,
		Username:              "u",
		Token:                 "sk-user",
		Role:                  "user",
		Status:                1,
		Quota:                 100_000,
		BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&database.ModelPricingRule{
		RuleKey:         "test|grok-imagine-image|image|output|1K",
		PricingVersion:  "test",
		ProviderKey:     "xai",
		ModelID:         "grok-imagine-image",
		OfficialModelID: "grok-imagine-image",
		BillingMode:     database.BillingModeImage,
		Unit:            "image",
		Direction:       "output",
		Resolution:      "1K",
		PriceMicroUSD:   20_000,
	}).Error; err != nil {
		t.Fatalf("seed pricing: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[1] = &database.Channel{ID: 1, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["grok-imagine-image"] = []*database.ChannelModel{{
		ID:               1,
		ChannelID:        1,
		ModelID:          "grok-imagine-image",
		ModelCategory:    database.ModelCategoryImage,
		BillingMode:      database.BillingModeImage,
		AllowedEndpoints: database.DefaultAllowedEndpointsForCategory(database.ModelCategoryImage),
		Weight:           1,
		Status:           1,
	}}

	app := fiber.New()
	app.Post(database.EndpointImagesGenerations, ImageGenerationProxyHandler)
	req := httptest.NewRequest(http.MethodPost, database.EndpointImagesGenerations,
		bytes.NewBufferString(`{"model":"grok-imagine-image","prompt":"draw a small icon","n":2,"size":"1024x1024"}`))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
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
	if fresh.Quota != 60_000 {
		t.Fatalf("quota=%d want 60000", fresh.Quota)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("model_name = ?", "grok-imagine-image").First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Quantity != 2 || line.AmountMicroUSD != 40_000 || line.UnitPriceMicro != 20_000 || line.CostSource != "official_matrix" {
		t.Fatalf("unexpected usage line: %#v", line)
	}
	var bill database.BillingEntry
	if err := db.Where("entry_type = ?", database.BillingTypeApiConsumeBalance).First(&bill).Error; err != nil {
		t.Fatalf("load billing entry: %v", err)
	}
	if bill.AmountUSD != -40_000 || bill.BalanceAfterUSD != 60_000 {
		t.Fatalf("unexpected billing entry: %#v", bill)
	}
	var revenue database.ApiLogRevenue
	if err := db.First(&revenue).Error; err != nil {
		t.Fatalf("load revenue: %v", err)
	}
	if revenue.EffectiveRevenueMicroUSD != 40_000 || revenue.RevenueSource != database.RevenueSourceBalance {
		t.Fatalf("unexpected revenue: %#v", revenue)
	}
}

func TestImageGeneration_GPTImageTokenBillingWritesTokenUsage(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != database.EndpointImagesGenerations {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		if _, ok := body["n"]; ok {
			t.Fatalf("gpt-image-2 sanitized body must not forward n: %#v", body)
		}
		if body["size"] != "1024x1024" || body["quality"] != "high" || body["output_format"] != "png" {
			t.Fatalf("unexpected gpt image body: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"AA=="}],"usage":{"input_tokens":1000,"output_tokens":2000,"total_tokens":3000,"input_tokens_details":{"cached_tokens":200}}}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{
		ID:                    11,
		Username:              "gpt-img",
		Token:                 "sk-gpt-image",
		Role:                  "user",
		Status:                1,
		Quota:                 1_000_000,
		BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[2] = &database.Channel{ID: 2, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["gpt-image-2"] = []*database.ChannelModel{{
		ID:                           2,
		ChannelID:                    2,
		ModelID:                      "gpt-image-2",
		ModelCategory:                database.ModelCategoryImage,
		BillingMode:                  database.BillingModeToken,
		AllowedEndpoints:             database.DefaultAllowedEndpointsForCategory(database.ModelCategoryImage),
		InputPricePicoPerToken:       5 * database.PicoPerTokenPerUSDPerMTok,
		OutputPricePicoPerToken:      30 * database.PicoPerTokenPerUSDPerMTok,
		CachedInputPricePicoPerToken: 125 * database.PicoPerTokenPerUSDPerMTok / 100,
		Weight:                       1,
		Status:                       1,
	}}

	app := fiber.New()
	app.Post(database.EndpointImagesGenerations, ImageGenerationProxyHandler)
	req := httptest.NewRequest(http.MethodPost, database.EndpointImagesGenerations,
		bytes.NewBufferString(`{"model":"gpt-image-2","prompt":"draw a product icon","size":"1024x1024","quality":"high","output_format":"png","response_format":"b64_json"}`))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
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
	const wantCost = int64(64_250)
	if fresh.Quota != user.Quota-wantCost {
		t.Fatalf("quota=%d want %d", fresh.Quota, user.Quota-wantCost)
	}
	var logRow database.ApiLog
	if err := db.Where("model_name = ?", "gpt-image-2").First(&logRow).Error; err != nil {
		t.Fatalf("load api log: %v", err)
	}
	if logRow.PromptTokens != 1000 || logRow.CompletionTokens != 2000 || logRow.CachedTokens != 200 || logRow.Cost != wantCost {
		t.Fatalf("unexpected api log: %#v", logRow)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("api_log_id = ?", logRow.ID).First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Unit != "token" || line.Direction != "total" || line.Quantity != 3000 || line.AmountMicroUSD != wantCost || line.CostSource != "upstream_usage" {
		t.Fatalf("unexpected usage line: %#v", line)
	}
	var bill database.BillingEntry
	if err := db.Where("entry_type = ?", database.BillingTypeApiConsumeBalance).First(&bill).Error; err != nil {
		t.Fatalf("load billing entry: %v", err)
	}
	if bill.AmountUSD != -wantCost || bill.TokensTotal != 3000 {
		t.Fatalf("unexpected billing entry: %#v", bill)
	}
}

func TestImageGeneration_GPTImageMissingUsageRecordsPending(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"AA=="}]}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{
		ID:                    12,
		Username:              "gpt-img-pending",
		Token:                 "sk-gpt-image-pending",
		Role:                  "user",
		Status:                1,
		Quota:                 1_000_000,
		BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[3] = &database.Channel{ID: 3, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["gpt-image-2"] = []*database.ChannelModel{{
		ID:                      3,
		ChannelID:               3,
		ModelID:                 "gpt-image-2",
		ModelCategory:           database.ModelCategoryImage,
		BillingMode:             database.BillingModeToken,
		AllowedEndpoints:        database.DefaultAllowedEndpointsForCategory(database.ModelCategoryImage),
		InputPricePicoPerToken:  5 * database.PicoPerTokenPerUSDPerMTok,
		OutputPricePicoPerToken: 30 * database.PicoPerTokenPerUSDPerMTok,
		Weight:                  1,
		Status:                  1,
	}}

	app := fiber.New()
	app.Post(database.EndpointImagesGenerations, ImageGenerationProxyHandler)
	req := httptest.NewRequest(http.MethodPost, database.EndpointImagesGenerations,
		bytes.NewBufferString(`{"model":"gpt-image-2","prompt":"draw a product icon"}`))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
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
	if fresh.Quota != user.Quota {
		t.Fatalf("quota=%d want unchanged %d", fresh.Quota, user.Quota)
	}
	var pending database.BillingEntry
	if err := db.Where("entry_type = ?", database.BillingTypeApiUsagePendingReconcile).First(&pending).Error; err != nil {
		t.Fatalf("load pending billing entry: %v", err)
	}
	if pending.EstimatedCostUSD <= 0 || !strings.Contains(pending.Description, "token usage missing") {
		t.Fatalf("unexpected pending entry: %#v", pending)
	}
}

func TestParseImageGenerationRequestRejectsInputs(t *testing.T) {
	_, _, err := parseImageGenerationRequest([]byte(`{"model":"grok-imagine-image","prompt":"x","image_url":"https://example.test/a.png"}`))
	if err == nil {
		t.Fatal("expected image_url to be rejected")
	}
}

func TestParseImageGenerationRequestRejectsLegacyImageFormat(t *testing.T) {
	_, _, err := parseImageGenerationRequest([]byte(`{"model":"xai/grok-imagine-image","prompt":"x","image_format":"base64"}`))
	if err == nil {
		t.Fatal("expected legacy image_format to be rejected")
	}
}

func TestParseImageGenerationRequestResponseFormat(t *testing.T) {
	req, sanitized, err := parseImageGenerationRequest([]byte(`{"model":"xai/grok-imagine-image","prompt":"x","response_format":"b64_json","stream":false}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.Model != "grok-imagine-image" {
		t.Fatalf("model=%q want canonical grok-imagine-image", req.Model)
	}
	if req.Resolution != "1K" || req.AspectRatio != "1:1" {
		t.Fatalf("defaults resolution=%q aspect=%q want 1K/1:1", req.Resolution, req.AspectRatio)
	}
	if req.ResponseFormat != "b64_json" {
		t.Fatalf("response_format=%q want b64_json", req.ResponseFormat)
	}
	var body map[string]any
	if err := json.Unmarshal(sanitized, &body); err != nil {
		t.Fatalf("decode sanitized: %v", err)
	}
	if body["response_format"] != "b64_json" {
		t.Fatalf("sanitized body missing b64_json response_format: %#v", body)
	}
	if _, ok := body["image_format"]; ok {
		t.Fatalf("sanitized body must not forward image_format: %#v", body)
	}
}

func TestParseImageGenerationRequestSizeAliases(t *testing.T) {
	req, sanitized, err := parseImageGenerationRequest([]byte(`{"model":"grok/grok-imagine-image-quality","prompt":"x","size":"1792x1024","aspect_ratio":"portrait","response_format":"url"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if req.Model != "grok-imagine-image-quality" {
		t.Fatalf("model=%q want quality canonical", req.Model)
	}
	if req.Resolution != "1K" || req.AspectRatio != "16:9" {
		t.Fatalf("size should drive aspect ratio: resolution=%q aspect=%q", req.Resolution, req.AspectRatio)
	}
	var body map[string]any
	if err := json.Unmarshal(sanitized, &body); err != nil {
		t.Fatalf("decode sanitized: %v", err)
	}
	if body["model"] != "grok-imagine-image-quality" || body["aspect_ratio"] != "16:9" || body["resolution"] != "1k" {
		t.Fatalf("unexpected sanitized body: %#v", body)
	}
}

func TestParseImageGenerationRequestRejectsUnsupportedXAIAspectRatio(t *testing.T) {
	_, _, err := parseImageGenerationRequest([]byte(`{"model":"x-ai/grok-imagine-image","prompt":"x","aspect_ratio":"auto"}`))
	if err == nil || !strings.Contains(err.Error(), "aspect_ratio") {
		t.Fatalf("err=%v want aspect_ratio rejection", err)
	}
}

func TestParseImageGenerationRequestRejectsXAIStreaming(t *testing.T) {
	_, _, err := parseImageGenerationRequest([]byte(`{"model":"grok-imagine-image","prompt":"x","stream":true}`))
	if err == nil || !strings.Contains(err.Error(), "streaming is only supported for gpt-image-2") {
		t.Fatalf("err=%v want xAI streaming rejection", err)
	}
}

func TestParseImageGenerationRequestAllowsGPTImageStreaming(t *testing.T) {
	req, sanitized, err := parseImageGenerationRequest([]byte(`{"model":"gpt-image-2","prompt":"draw a logo","stream":true,"partial_images":2}`))
	if err != nil {
		t.Fatalf("expected gpt-image-2 streaming to be allowed, err=%v", err)
	}
	if req.Model != "gpt-image-2" {
		t.Fatalf("model=%q want gpt-image-2", req.Model)
	}
	var body map[string]any
	if err := json.Unmarshal(sanitized, &body); err != nil {
		t.Fatalf("decode sanitized: %v", err)
	}
	if body["stream"] != true {
		t.Fatalf("sanitized body missing stream=true: %#v", body)
	}
	if body["partial_images"] != float64(2) {
		t.Fatalf("sanitized body partial_images=%v want 2", body["partial_images"])
	}
}

func TestParseImageGenerationRequestRejectsPartialImagesWithoutStream(t *testing.T) {
	_, _, err := parseImageGenerationRequest([]byte(`{"model":"gpt-image-2","prompt":"x","partial_images":2}`))
	if err == nil || !strings.Contains(err.Error(), "partial_images requires stream=true") {
		t.Fatalf("err=%v want partial_images requires stream rejection", err)
	}
}

func TestParseImageGenerationRequestRejectsInvalidPartialImagesCount(t *testing.T) {
	for _, n := range []int{-1, 0, 4, 99} {
		body := []byte(fmt.Sprintf(`{"model":"gpt-image-2","prompt":"x","stream":true,"partial_images":%d}`, n))
		_, _, err := parseImageGenerationRequest(body)
		if n == 0 {
			// partial_images=0 等价于未设置，无需 stream，应该通过
			if err != nil {
				t.Fatalf("partial_images=0 should be allowed (treated as unset): %v", err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "partial_images must be 1, 2, or 3") {
			t.Fatalf("partial_images=%d err=%v want range rejection", n, err)
		}
	}
}

func TestParseImageGenerationRequestRejectsGPTImageURLResponseFormat(t *testing.T) {
	_, _, err := parseImageGenerationRequest([]byte(`{"model":"gpt-image-2","prompt":"x","response_format":"url"}`))
	if err == nil || !strings.Contains(err.Error(), "response_format=url") {
		t.Fatalf("err=%v want response_format=url rejection", err)
	}
}

func TestParseImageGenerationRequestRejectsGPTImageTransparentBackground(t *testing.T) {
	_, _, err := parseImageGenerationRequest([]byte(`{"model":"gpt-image-2","prompt":"x","background":"transparent"}`))
	if err == nil || !strings.Contains(err.Error(), "background=transparent") {
		t.Fatalf("err=%v want transparent background rejection", err)
	}
}

func TestEstimateGPTImageOutputTokensMatchesOfficialPriceTable(t *testing.T) {
	tests := []struct {
		name    string
		req     imageGenerationRequest
		wantOut int
	}{
		{
			name:    "default auto is square high",
			req:     imageGenerationRequest{Model: "gpt-image-2"},
			wantOut: 7034,
		},
		{
			name:    "square low",
			req:     imageGenerationRequest{Model: "gpt-image-2", Size: "1024x1024", Quality: "low"},
			wantOut: 200,
		},
		{
			name:    "square medium",
			req:     imageGenerationRequest{Model: "gpt-image-2", Size: "1024x1024", Quality: "medium"},
			wantOut: 1767,
		},
		{
			name:    "landscape high",
			req:     imageGenerationRequest{Model: "gpt-image-2", Size: "1536x1024", Quality: "high"},
			wantOut: 5500,
		},
		{
			name:    "portrait medium",
			req:     imageGenerationRequest{Model: "gpt-image-2", Size: "1024x1536", Quality: "medium"},
			wantOut: 1367,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := estimateGPTImageOutputTokens(tt.req); got != tt.wantOut {
				t.Fatalf("estimateGPTImageOutputTokens()=%d want %d", got, tt.wantOut)
			}
		})
	}
}

func TestCostTicksFromImageResponse(t *testing.T) {
	req := imageGenerationRequest{Model: "grok-imagine-image", Prompt: "x", N: 1, Resolution: "1K"}
	price, err := resolveImagePrice(req, 1, 200_000_000)
	if err != nil {
		t.Fatalf("resolve ticks: %v", err)
	}
	if price.AmountMicroUSD != 20_000 || price.UnitPriceMicro != 20_000 || price.CostSource != "upstream_usage" {
		t.Fatalf("unexpected tick price: %#v", price)
	}
}

func TestResolveImagePriceUsesActualResponseImageCount(t *testing.T) {
	db := setupImageGenerationTest(t)
	if err := db.Create(&database.ModelPricingRule{
		RuleKey:         "test|grok-imagine-image|image|output|1K",
		PricingVersion:  "test",
		ProviderKey:     "xai",
		ModelID:         "grok-imagine-image",
		OfficialModelID: "grok-imagine-image",
		BillingMode:     database.BillingModeImage,
		Unit:            "image",
		Direction:       "output",
		Resolution:      "1K",
		PriceMicroUSD:   20_000,
	}).Error; err != nil {
		t.Fatalf("seed pricing: %v", err)
	}
	req := imageGenerationRequest{Model: "grok-imagine-image", Prompt: "x", N: 3, Resolution: "1K"}
	price, err := resolveImagePrice(req, 1, 0)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if price.Quantity != 1 || price.AmountMicroUSD != 20_000 || price.ResponseImages != 1 {
		t.Fatalf("unexpected actual-count price: %#v", price)
	}
}

func TestImageBalanceInsufficientWritesPendingReconcile(t *testing.T) {
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
	req := imageGenerationRequest{Model: "grok-imagine-image", Prompt: "x", N: 1, Resolution: "1K"}
	price := imagePriceResolution{
		Quantity:       1,
		UnitPriceMicro: 20_000,
		AmountMicroUSD: 20_000,
		Resolution:     "1K",
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

	apiLogID, effectiveRevenue, _ := deductImageBalanceAndLog(&user, user.Token, req, price, billing, "cliproxy", 200, "127.0.0.1", database.EndpointImagesGenerations, time.Now())
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
	// fix H2 (2026-05-19)：window tracking 在 CAS quota 前调用，window 会 reset
	// 然后 forceTrack 累加 20000 → 最终 20000（与 text path 对齐）。
	if fresh.BalanceConsumedInWindow != 20_000 {
		t.Fatalf("balance window consumed=%d want 20000 (H2 fix: window resets then tracks 20000)", fresh.BalanceConsumedInWindow)
	}
	var pending database.BillingEntry
	if err := db.Where("entry_type = ?", database.BillingTypeApiUsagePendingReconcile).First(&pending).Error; err != nil {
		t.Fatalf("load pending billing entry: %v", err)
	}
	if pending.RelatedID != apiLogID || pending.EstimatedCostUSD != 20_000 {
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

func TestImageGenerationPricingRuleAppendOnly(t *testing.T) {
	db := setupImageGenerationTest(t)
	logRow := database.ApiLog{UserID: 1, ModelName: "grok-imagine-image", Status: 200, CreatedAt: time.Now()}
	if err := db.Create(&logRow).Error; err != nil {
		t.Fatalf("seed log: %v", err)
	}
	line := database.ApiLogUsageLine{ApiLogID: logRow.ID, ModelName: "grok-imagine-image", Unit: "image", Direction: "output", Quantity: 1, AmountMicroUSD: 20_000}
	if err := db.Create(&line).Error; err != nil {
		t.Fatalf("seed usage line: %v", err)
	}
	if err := db.Model(&database.ApiLogUsageLine{}).Where("id = ?", line.ID).Update("amount_micro_usd", int64(1)).Error; err != database.ErrApiLogAppendOnly {
		t.Fatalf("update err=%v want append-only", err)
	}
}

func TestImageGeneration_GPTImageStreamingSuccess(t *testing.T) {
	db := setupImageGenerationTest(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != database.EndpointImagesGenerations {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		if body["stream"] != true {
			t.Errorf("sanitized body must forward stream=true: %#v", body)
		}
		if body["partial_images"] != float64(2) {
			t.Errorf("sanitized body partial_images=%v want 2", body["partial_images"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: image_generation.partial_image\ndata: {\"partial_image_b64\":\"AA==\",\"index\":0}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprint(w, "event: image_generation.completed\ndata: {\"b64_json\":\"FINAL==\",\"usage\":{\"input_tokens\":1000,\"output_tokens\":2000,\"total_tokens\":3000,\"input_tokens_details\":{\"cached_tokens\":200}}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(backend.Close)

	user := database.User{
		ID:                    11,
		Username:              "gpt-img-stream",
		Token:                 "sk-gpt-image-stream",
		Role:                  "user",
		Status:                1,
		Quota:                 1_000_000,
		BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[2] = &database.Channel{ID: 2, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["gpt-image-2"] = []*database.ChannelModel{{
		ID:                           2,
		ChannelID:                    2,
		ModelID:                      "gpt-image-2",
		ModelCategory:                database.ModelCategoryImage,
		BillingMode:                  database.BillingModeToken,
		AllowedEndpoints:             database.DefaultAllowedEndpointsForCategory(database.ModelCategoryImage),
		InputPricePicoPerToken:       5 * database.PicoPerTokenPerUSDPerMTok,
		OutputPricePicoPerToken:      30 * database.PicoPerTokenPerUSDPerMTok,
		CachedInputPricePicoPerToken: 125 * database.PicoPerTokenPerUSDPerMTok / 100,
		Weight:                       1,
		Status:                       1,
	}}

	app := fiber.New()
	app.Post(database.EndpointImagesGenerations, ImageGenerationProxyHandler)
	req := httptest.NewRequest(http.MethodPost, database.EndpointImagesGenerations,
		bytes.NewBufferString(`{"model":"gpt-image-2","prompt":"draw a logo","stream":true,"partial_images":2}`))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("Content-Type=%q want text/event-stream*", got)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(bodyBytes, []byte("image_generation.partial_image")) {
		t.Fatalf("response missing partial_image event: %q", string(bodyBytes))
	}
	if !bytes.Contains(bodyBytes, []byte("image_generation.completed")) {
		t.Fatalf("response missing completed event: %q", string(bodyBytes))
	}

	// 等 SetBodyStreamWriter callback 异步执行完成（含计费写入）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		db.Model(&database.ApiLog{}).Where("model_name = ?", "gpt-image-2").Count(&n)
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	const wantCost = int64(64_250)
	if fresh.Quota != user.Quota-wantCost {
		t.Fatalf("quota=%d want %d", fresh.Quota, user.Quota-wantCost)
	}
	var logRow database.ApiLog
	if err := db.Where("model_name = ?", "gpt-image-2").First(&logRow).Error; err != nil {
		t.Fatalf("load api log: %v", err)
	}
	if logRow.PromptTokens != 1000 || logRow.CompletionTokens != 2000 || logRow.CachedTokens != 200 || logRow.Cost != wantCost {
		t.Fatalf("unexpected api log: %#v", logRow)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("api_log_id = ?", logRow.ID).First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Unit != "token" || line.Direction != "total" || line.Quantity != 3000 || line.AmountMicroUSD != wantCost || line.CostSource != "upstream_usage" {
		t.Fatalf("unexpected usage line: %#v", line)
	}
	var bill database.BillingEntry
	if err := db.Where("entry_type = ?", database.BillingTypeApiConsumeBalance).First(&bill).Error; err != nil {
		t.Fatalf("load billing entry: %v", err)
	}
	if bill.AmountUSD != -wantCost || bill.TokensTotal != 3000 {
		t.Fatalf("unexpected billing entry: %#v", bill)
	}
}

func TestImageGeneration_GPTImageStreamingMissingCompletedWritesPending(t *testing.T) {
	db := setupImageGenerationTest(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 上游开了 SSE 但只发了 partial，没发 completed event 就关闭——模拟上游异常截断
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: image_generation.partial_image\ndata: {\"partial_image_b64\":\"AA==\",\"index\":0}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// 直接 return — 没发 completed 事件
	}))
	t.Cleanup(backend.Close)

	user := database.User{
		ID:                    12,
		Username:              "gpt-img-stream-pending",
		Token:                 "sk-gpt-image-stream-pending",
		Role:                  "user",
		Status:                1,
		Quota:                 1_000_000,
		BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[3] = &database.Channel{ID: 3, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["gpt-image-2"] = []*database.ChannelModel{{
		ID:                      3,
		ChannelID:               3,
		ModelID:                 "gpt-image-2",
		ModelCategory:           database.ModelCategoryImage,
		BillingMode:             database.BillingModeToken,
		AllowedEndpoints:        database.DefaultAllowedEndpointsForCategory(database.ModelCategoryImage),
		InputPricePicoPerToken:  5 * database.PicoPerTokenPerUSDPerMTok,
		OutputPricePicoPerToken: 30 * database.PicoPerTokenPerUSDPerMTok,
		Weight:                  1,
		Status:                  1,
	}}

	app := fiber.New()
	app.Post(database.EndpointImagesGenerations, ImageGenerationProxyHandler)
	req := httptest.NewRequest(http.MethodPost, database.EndpointImagesGenerations,
		bytes.NewBufferString(`{"model":"gpt-image-2","prompt":"draw a logo","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	_, _ = io.ReadAll(resp.Body)

	// 等异步 callback 完成 pending reconcile 写入
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var n int64
		db.Model(&database.BillingEntry{}).Where("entry_type = ?", database.BillingTypeApiUsagePendingReconcile).Count(&n)
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 验证 quota 未扣（pending reconcile 不扣余额）
	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if fresh.Quota != user.Quota {
		t.Fatalf("quota=%d want unchanged %d (pending reconcile must not deduct)", fresh.Quota, user.Quota)
	}
	var pending database.BillingEntry
	if err := db.Where("entry_type = ?", database.BillingTypeApiUsagePendingReconcile).First(&pending).Error; err != nil {
		t.Fatalf("load pending billing entry: %v", err)
	}
	if pending.EstimatedCostUSD <= 0 {
		t.Fatalf("pending entry must carry estimated cost: %#v", pending)
	}
	if !strings.Contains(pending.Description, "completed") && !strings.Contains(pending.Description, "stream") {
		t.Fatalf("pending description missing stream-end marker: %q", pending.Description)
	}
}
