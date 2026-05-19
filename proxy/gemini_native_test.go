package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

func TestParseGeminiNativeAction_RejectsMalformed(t *testing.T) {
	app := fiber.New()
	app.Post("/v1beta/models/*", func(c *fiber.Ctx) error {
		_, err := parseGeminiNativeAction(c)
		if err == nil {
			return c.SendStatus(200)
		}
		return c.Status(400).JSON(fiber.Map{"err": err.Error()})
	})

	cases := []struct {
		url     string
		wantErr string
	}{
		// 注意：fiber JSON encoder 把 < 转义为 <，故断言用纯英文短语
		{"/v1beta/models/gemini-2.5-flash", "action must be"},
		{"/v1beta/models/:generateContent", "non-empty model and method"},
		{"/v1beta/models/gemini-2.5-flash:embed", "unsupported method"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, tc.url, nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("test request %s: %v", tc.url, err)
		}
		if resp.StatusCode != 400 {
			t.Errorf("url=%s status=%d want 400", tc.url, resp.StatusCode)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), tc.wantErr) {
			t.Errorf("url=%s body=%s want contains %q", tc.url, body, tc.wantErr)
		}
	}
}

func TestParseGeminiNativeAction_AcceptsValid(t *testing.T) {
	app := fiber.New()
	var captured geminiNativeRequest
	app.Post("/v1beta/models/*", func(c *fiber.Ctx) error {
		req, err := parseGeminiNativeAction(c)
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"err": err.Error()})
		}
		captured = req
		return c.JSON(req)
	})

	cases := []struct {
		url            string
		wantModel      string
		wantMethod     string
		wantIsStream   bool
	}{
		{"/v1beta/models/gemini-3.1-pro-preview:generateContent", "gemini-3.1-pro-preview", "generateContent", false},
		{"/v1beta/models/gemini-2.5-flash:streamGenerateContent", "gemini-2.5-flash", "streamGenerateContent", true},
		{"/v1beta/models/gemini-2.5-pro:countTokens", "gemini-2.5-pro", "countTokens", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, tc.url, nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("test request %s: %v", tc.url, err)
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("url=%s status=%d body=%s want 200", tc.url, resp.StatusCode, body)
			continue
		}
		if captured.Model != tc.wantModel || captured.Method != tc.wantMethod || captured.IsStream != tc.wantIsStream {
			t.Errorf("url=%s captured=%+v want model=%q method=%q isStream=%v", tc.url, captured, tc.wantModel, tc.wantMethod, tc.wantIsStream)
		}
	}
}

func TestRejectGeminiNativeFileURIRefs(t *testing.T) {
	ok := []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	if err := rejectGeminiNativeFileURIRefs(ok); err != nil {
		t.Fatalf("plain text contents should pass: %v", err)
	}
	inline := []byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}]}`)
	if err := rejectGeminiNativeFileURIRefs(inline); err != nil {
		t.Fatalf("inlineData should pass: %v", err)
	}
	bad := []byte(`{"contents":[{"parts":[{"fileData":{"fileUri":"gs://bucket/key.png","mimeType":"image/png"}}]}]}`)
	if err := rejectGeminiNativeFileURIRefs(bad); err == nil || !strings.Contains(err.Error(), "fileUri") {
		t.Fatalf("fileUri must be rejected, err=%v", err)
	}
	badSnake := []byte(`{"contents":[{"parts":[{"fileData":{"file_uri":"gs://bucket/key.png"}}]}]}`)
	if err := rejectGeminiNativeFileURIRefs(badSnake); err == nil {
		t.Fatalf("file_uri snake_case must also be rejected")
	}
}

func TestCanonicalRuntimeGeminiModel(t *testing.T) {
	db := setupImageGenerationTest(t)
	defer func() {}()

	// 未注册：拒绝
	if _, ok := database.CanonicalRuntimeGeminiModel("gemini-2.5-flash"); ok {
		t.Fatalf("unregistered gemini model should be rejected")
	}

	// admin 注册 supported=true 后接受
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "google", ProviderName: "Google Gemini",
		ModelID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash",
		Category: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		Supported: true, Public: true,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, ok := database.CanonicalRuntimeGeminiModel("gemini-2.5-flash")
	if !ok || got != "gemini-2.5-flash" {
		t.Fatalf("got (%q,%v) want gemini-2.5-flash/true", got, ok)
	}

	// 带 models/ URL 前缀也能命中
	got, ok = database.CanonicalRuntimeGeminiModel("models/gemini-2.5-flash")
	if !ok || got != "gemini-2.5-flash" {
		t.Fatalf("models/ prefix lookup=(%q,%v) want gemini-2.5-flash/true", got, ok)
	}

	// 不是 google provider 的：拒绝
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "openai", ProviderName: "OpenAI",
		ModelID: "gpt-4", DisplayName: "GPT-4",
		Category: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		Supported: true, Public: true,
	}).Error; err != nil {
		t.Fatalf("seed openai: %v", err)
	}
	if _, ok := database.CanonicalRuntimeGeminiModel("gpt-4"); ok {
		t.Fatalf("non-google model must not be recognized by Gemini canonical")
	}

	// Supported=false：拒绝
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "google", ProviderName: "Google Gemini",
		ModelID: "gemini-disabled", DisplayName: "disabled",
		Category: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		Supported: false,
	}).Error; err != nil {
		t.Fatalf("seed disabled: %v", err)
	}
	if _, ok := database.CanonicalRuntimeGeminiModel("gemini-disabled"); ok {
		t.Fatalf("supported=false model must not be recognized")
	}
}

func TestGeminiNative_Returns404WhenEndpointDisabled(t *testing.T) {
	db := setupImageGenerationTest(t)
	user := database.User{ID: 60, Username: "gemini-disabled", Token: "sk-gemini-disabled", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "google", ProviderName: "Google Gemini",
		ModelID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash",
		Category: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		Supported: true, Public: true,
	}).Error; err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	// admin 没在 ChannelModel.AllowedEndpoints 加 /v1beta/models
	ChannelMapCache[40] = &database.Channel{ID: 40, Type: ChannelTypeCLIProxy, BaseURL: "http://unused.local", Key: "k", Status: 1}
	RouteCache["gemini-2.5-flash"] = []*database.ChannelModel{{
		ID: 40, ChannelID: 40, ModelID: "gemini-2.5-flash",
		ModelCategory:    database.ModelCategoryText,
		BillingMode:      database.BillingModeToken,
		AllowedEndpoints: "",
		InputPricePicoPerToken:  300 * database.PicoPerTokenPerUSDPerMTok / 1000,
		OutputPricePicoPerToken: 2500 * database.PicoPerTokenPerUSDPerMTok / 1000,
		Weight: 1, Status: 1,
	}}

	app := fiber.New()
	app.Post("/v1beta/models/*", GeminiNativeProxyHandler)
	body := `{"contents":[{"parts":[{"text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 404 body=%s", resp.StatusCode, respBody)
	}
}

func TestGeminiNative_GenerateContentBalanceBilling(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		if !strings.HasSuffix(r.URL.Path, ":generateContent") {
			t.Errorf("upstream URL must end in :generateContent, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		// 模拟 Gemini 响应（含 usageMetadata）
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"parts":[{"text":"Hello!"}],"role":"model"},"finishReason":"STOP"}],
			"usageMetadata":{
				"promptTokenCount":10,
				"candidatesTokenCount":20,
				"totalTokenCount":30
			}
		}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{ID: 61, Username: "gemini-text", Token: "sk-gemini-text", Status: 1, Quota: 10_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "google", ProviderName: "Google Gemini",
		ModelID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash",
		Category: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		Supported: true, Public: true,
	}).Error; err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	ChannelMapCache[41] = &database.Channel{ID: 41, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["gemini-2.5-flash"] = []*database.ChannelModel{{
		ID: 41, ChannelID: 41, ModelID: "gemini-2.5-flash",
		ModelCategory:           database.ModelCategoryText,
		BillingMode:             database.BillingModeToken,
		AllowedEndpoints:        `["/v1beta/models"]`,
		InputPricePicoPerToken:  300 * database.PicoPerTokenPerUSDPerMTok / 1000, // $0.3 / 1M
		OutputPricePicoPerToken: 2500 * database.PicoPerTokenPerUSDPerMTok / 1000, // $2.5 / 1M
		Weight:                  1,
		Status:                  1,
	}}

	app := fiber.New()
	app.Post("/v1beta/models/*", GeminiNativeProxyHandler)
	body := `{"contents":[{"parts":[{"text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 200 body=%s", resp.StatusCode, respBody)
	}

	// 计费：prompt=10 × $0.3/M + candidates=20 × $2.5/M = $0.003 + $0.05 = $0.053
	//      = 3 + 50 = 53 micro_usd（ceil-div per token cost calc）
	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	const initialQuota = int64(10_000_000)
	consumed := initialQuota - fresh.Quota
	if consumed < 50 || consumed > 60 {
		t.Fatalf("quota consumed=%d want ~53 (10×$0.3/M + 20×$2.5/M); fresh.Quota=%d", consumed, fresh.Quota)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("model_name = ?", "gemini-2.5-flash").First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Unit != "token" || line.CostSource != "upstream_usage" {
		t.Fatalf("unexpected usage line: %#v", line)
	}
	if line.RequestPath != database.EndpointGeminiNative {
		t.Fatalf("usage line RequestPath=%q want /v1beta/models", line.RequestPath)
	}
}

func TestGeminiNative_ImagenPerImageBilling(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// CPA 把 Imagen 响应翻译成 Gemini 格式（candidates[].content.parts[].inlineData）
		// 这里 mock 2 张输出
		_, _ = w.Write([]byte(`{
			"candidates":[{
				"content":{
					"parts":[
						{"inlineData":{"mimeType":"image/png","data":"AAA="}},
						{"inlineData":{"mimeType":"image/png","data":"BBB="}}
					],
					"role":"model"
				},
				"finishReason":"STOP"
			}],
			"responseId":"imagen-1234567890"
		}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{ID: 62, Username: "imagen-user", Token: "sk-imagen", Status: 1, Quota: 10_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	// admin 注册 Imagen
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "google", ProviderName: "Google Imagen",
		ModelID: "imagen-4.0-fast-generate-001", DisplayName: "Imagen 4.0 Fast",
		Category: database.ModelCategoryImage, BillingMode: database.BillingModeImage,
		Supported: true, Public: true,
	}).Error; err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	// admin 手填 image/output pricing
	if err := db.Create(&database.ModelPricingRule{
		RuleKey: "test|imagen-out", PricingVersion: "test",
		ProviderKey: "google", ModelID: "imagen-4.0-fast-generate-001", OfficialModelID: "imagen-4.0-fast-generate-001",
		BillingMode: database.BillingModeImage, Unit: "image", Direction: "output",
		PriceMicroUSD: 40_000, // $0.04/image
	}).Error; err != nil {
		t.Fatalf("seed pricing: %v", err)
	}
	ChannelMapCache[42] = &database.Channel{ID: 42, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["imagen-4.0-fast-generate-001"] = []*database.ChannelModel{{
		ID: 42, ChannelID: 42, ModelID: "imagen-4.0-fast-generate-001",
		ModelCategory:    database.ModelCategoryImage,
		BillingMode:      database.BillingModeImage,
		AllowedEndpoints: `["/v1beta/models"]`,
		Weight:           1,
		Status:           1,
	}}

	app := fiber.New()
	app.Post("/v1beta/models/*", GeminiNativeProxyHandler)
	body := `{"contents":[{"parts":[{"text":"a kitten in a basket"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/imagen-4.0-fast-generate-001:generateContent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 200 body=%s", resp.StatusCode, respBody)
	}

	// 计费：2 张图 × $0.04/张 = $0.08 = 80_000 micro_usd
	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	const initialQuota = int64(10_000_000)
	const wantCost = int64(80_000)
	if fresh.Quota != initialQuota-wantCost {
		t.Fatalf("quota=%d want %d (2 images × $0.04)", fresh.Quota, initialQuota-wantCost)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("model_name = ?", "imagen-4.0-fast-generate-001").First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Unit != "image" || line.Quantity != 2 || line.AmountMicroUSD != wantCost {
		t.Fatalf("unexpected usage line: %#v", line)
	}
}

func TestGeminiNative_CountTokensPassthrough(t *testing.T) {
	db := setupImageGenerationTest(t)
	called := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if !strings.HasSuffix(r.URL.Path, ":countTokens") {
			t.Errorf("upstream URL must end in :countTokens, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":42}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{ID: 63, Username: "count-tokens", Token: "sk-count-tokens", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "google", ProviderName: "Google Gemini",
		ModelID: "gemini-2.5-pro", Category: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		Supported: true, Public: true,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	ChannelMapCache[43] = &database.Channel{ID: 43, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "k", Status: 1}
	RouteCache["gemini-2.5-pro"] = []*database.ChannelModel{{
		ID: 43, ChannelID: 43, ModelID: "gemini-2.5-pro",
		ModelCategory:           database.ModelCategoryText,
		BillingMode:             database.BillingModeToken,
		AllowedEndpoints:        `["/v1beta/models"]`,
		InputPricePicoPerToken:  1250 * database.PicoPerTokenPerUSDPerMTok / 1000,
		OutputPricePicoPerToken: 10_000 * database.PicoPerTokenPerUSDPerMTok / 1000,
		Weight: 1, Status: 1,
	}}

	app := fiber.New()
	app.Post("/v1beta/models/*", GeminiNativeProxyHandler)
	body := `{"contents":[{"parts":[{"text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:countTokens", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 200 body=%s", resp.StatusCode, respBody)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, []byte("\"totalTokens\":42")) {
		t.Fatalf("upstream response not passed through: %s", respBody)
	}
	if called != 1 {
		t.Fatalf("backend calls=%d want 1", called)
	}
	// countTokens 不计费：没有 ApiLogUsageLine
	var lineCount int64
	db.Model(&database.ApiLogUsageLine{}).Where("model_name = ?", "gemini-2.5-pro").Count(&lineCount)
	if lineCount != 0 {
		t.Fatalf("countTokens must not create usage line, got %d", lineCount)
	}
}

func TestGeminiNative_GoogleAPIKeyQueryParamAuth(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":1}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{ID: 64, Username: "key-query", Token: "sk-key-query", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	AuthCache[user.Token] = &user
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "google", ModelID: "gemini-2.5-flash",
		Category: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		Supported: true,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	ChannelMapCache[44] = &database.Channel{ID: 44, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "k", Status: 1}
	RouteCache["gemini-2.5-flash"] = []*database.ChannelModel{{
		ID: 44, ChannelID: 44, ModelID: "gemini-2.5-flash",
		ModelCategory: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		AllowedEndpoints: `["/v1beta/models"]`,
		Weight: 1, Status: 1,
	}}

	app := fiber.New()
	app.Post("/v1beta/models/*", GeminiNativeProxyHandler)
	// Google AI SDK 默认用 ?key=xxx 风格
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:countTokens?key="+user.Token, strings.NewReader(`{"contents":[]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s (Google SDK ?key= should authenticate)", resp.StatusCode, body)
	}
}

func TestGeminiNative_RejectsFileURI(t *testing.T) {
	db := setupImageGenerationTest(t)
	user := database.User{ID: 65, Username: "fileuri", Token: "sk-fileuri", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	AuthCache[user.Token] = &user
	if err := db.Create(&database.ModelCatalog{
		ProviderKey: "google", ModelID: "gemini-2.5-pro",
		Category: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		Supported: true,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	ChannelMapCache[45] = &database.Channel{ID: 45, Type: ChannelTypeCLIProxy, BaseURL: "http://unused.local", Key: "k", Status: 1}
	RouteCache["gemini-2.5-pro"] = []*database.ChannelModel{{
		ID: 45, ChannelID: 45, ModelID: "gemini-2.5-pro",
		ModelCategory: database.ModelCategoryText, BillingMode: database.BillingModeToken,
		AllowedEndpoints: `["/v1beta/models"]`,
		Weight: 1, Status: 1,
	}}

	app := fiber.New()
	app.Post("/v1beta/models/*", GeminiNativeProxyHandler)
	body := `{"contents":[{"parts":[{"fileData":{"fileUri":"gs://bucket/file.png","mimeType":"image/png"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+user.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 400 body=%s", resp.StatusCode, respBody)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(respBody, []byte("fileUri")) {
		t.Fatalf("err body must mention fileUri: %s", respBody)
	}
}

func TestGeminiNative_ListModelsPassthrough(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("upstream method=%s want GET", r.Method)
		}
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("upstream path=%s want /v1beta/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-2.5-flash","baseModelId":"gemini-2.5-flash"},{"name":"models/gemini-3.1-pro-preview"}]}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{ID: 70, Username: "list-models", Token: "sk-list-models", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	if err := db.Create(&database.Channel{ID: 50, Name: "list-models-test", Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	app := fiber.New()
	app.Get("/v1beta/models", GeminiNativeProxyHandler)
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	req.Header.Set("Authorization", "Bearer "+user.Token)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d want 200 body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("gemini-2.5-flash")) {
		t.Fatalf("response should passthrough model list: %s", body)
	}
	// listModels 不计费：不应有 ApiLogUsageLine
	var lineCount int64
	db.Model(&database.ApiLogUsageLine{}).Count(&lineCount)
	if lineCount != 0 {
		t.Fatalf("listModels must not create usage line, got %d", lineCount)
	}
}

func init() {
	// 避免 unused import 警告
	_ = json.Marshal
}
