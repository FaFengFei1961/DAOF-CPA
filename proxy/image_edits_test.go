package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

func TestParseImageEditJSON_RejectsMissingImages(t *testing.T) {
	_, _, err := parseImageEditJSON([]byte(`{"model":"grok-imagine-image-quality","prompt":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "at least one input image is required") {
		t.Fatalf("err=%v want missing image rejection", err)
	}
}

func TestParseImageEditJSON_RejectsFileID(t *testing.T) {
	_, _, err := parseImageEditJSON([]byte(`{"model":"grok-imagine-image-quality","prompt":"x","images":[{"file_id":"file-abc"}]}`))
	if err == nil || !strings.Contains(err.Error(), "file_id") {
		t.Fatalf("err=%v want file_id rejection", err)
	}
	_, _, err = parseImageEditJSON([]byte(`{"model":"grok-imagine-image-quality","prompt":"x","images":[{"image_url":"data:image/png;base64,AAA="}],"mask":{"file_id":"file-mask"}}`))
	if err == nil || !strings.Contains(err.Error(), "file_id") {
		t.Fatalf("mask file_id err=%v want rejection", err)
	}
}

func TestParseImageEditJSON_RejectsNonAcceptableURL(t *testing.T) {
	_, _, err := parseImageEditJSON([]byte(`{"model":"grok-imagine-image-quality","prompt":"x","images":[{"image_url":"ftp://example.test/a.png"}]}`))
	if err == nil || !strings.Contains(err.Error(), "must be a data URL or http(s) URL") {
		t.Fatalf("err=%v want non-acceptable url rejection", err)
	}
}

func TestParseImageEditJSON_AcceptsSingleImageStringField(t *testing.T) {
	req, sanitized, err := parseImageEditJSON([]byte(`{"model":"grok-imagine-image-quality","prompt":"x","image":"data:image/png;base64,AAA="}`))
	if err != nil {
		t.Fatalf("expected single image field to be allowed: %v", err)
	}
	if len(req.Images) != 1 || req.Images[0].ImageURL != "data:image/png;base64,AAA=" {
		t.Fatalf("unexpected images: %#v", req.Images)
	}
	if req.InputImageCount != 1 {
		t.Fatalf("InputImageCount=%d want 1", req.InputImageCount)
	}
	var body map[string]any
	if err := json.Unmarshal(sanitized, &body); err != nil {
		t.Fatalf("decode sanitized: %v", err)
	}
	imgs, ok := body["images"].([]any)
	if !ok || len(imgs) != 1 {
		t.Fatalf("sanitized images=%v want one entry", body["images"])
	}
}

func TestParseImageEditJSON_AcceptsMaskAndImages(t *testing.T) {
	req, sanitized, err := parseImageEditJSON([]byte(`{"model":"grok-imagine-image-quality","prompt":"x","images":[{"image_url":"data:image/png;base64,AAA="},{"image_url":"data:image/png;base64,BBB="}],"mask":{"image_url":"data:image/png;base64,CCC="}}`))
	if err != nil {
		t.Fatalf("expected mask+images to be allowed: %v", err)
	}
	if len(req.Images) != 2 {
		t.Fatalf("Images count=%d want 2", len(req.Images))
	}
	if req.MaskImageURL != "data:image/png;base64,CCC=" {
		t.Fatalf("MaskImageURL=%q want data URL", req.MaskImageURL)
	}
	if !bytes.Contains(sanitized, []byte("mask")) {
		t.Fatalf("sanitized missing mask: %s", sanitized)
	}
}

func TestParseImageEditJSON_GPTImage2StreamingAllowed(t *testing.T) {
	req, sanitized, err := parseImageEditJSON([]byte(`{"model":"gpt-image-2","prompt":"x","images":[{"image_url":"data:image/png;base64,AAA="}],"stream":true,"partial_images":2,"input_fidelity":"high"}`))
	if err != nil {
		t.Fatalf("expected gpt-image-2 edit streaming to be allowed: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(sanitized, &body); err != nil {
		t.Fatalf("decode sanitized: %v", err)
	}
	if body["stream"] != true || body["partial_images"] != float64(2) || body["input_fidelity"] != "high" {
		t.Fatalf("sanitized body missing stream/partial/fidelity: %#v", body)
	}
	if req.InputFidelity != "high" {
		t.Fatalf("InputFidelity=%q want high", req.InputFidelity)
	}
}

func TestParseImageEditJSON_xAIRejectsGPTOnlyFields(t *testing.T) {
	_, _, err := parseImageEditJSON([]byte(`{"model":"grok-imagine-image-quality","prompt":"x","images":[{"image_url":"data:image/png;base64,AAA="}],"input_fidelity":"high"}`))
	if err == nil || !strings.Contains(err.Error(), "input_fidelity") {
		t.Fatalf("err=%v want xAI input_fidelity rejection", err)
	}
}

func TestParseImageEditMultipart_ConvertsFilesToDataURLs(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "grok-imagine-image-quality")
	_ = writer.WriteField("prompt", "edit me")
	_ = writer.WriteField("n", "1")

	imagePart, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	imagePart.Write([]byte{0x89, 0x50, 0x4E, 0x47}) // PNG header bytes

	maskPart, err := writer.CreateFormFile("mask", "mask.png")
	if err != nil {
		t.Fatalf("create mask part: %v", err)
	}
	maskPart.Write([]byte{0x89, 0x50, 0x4E, 0x47})

	_ = writer.Close()

	app := fiber.New()
	app.Post("/upload", func(c *fiber.Ctx) error {
		req, sanitized, perr := parseImageEditMultipart(c)
		if perr != nil {
			return c.Status(400).JSON(fiber.Map{"err": perr.Error()})
		}
		return c.JSON(fiber.Map{
			"images_len":     len(req.Images),
			"first_data_url": strings.HasPrefix(req.Images[0].ImageURL, "data:"),
			"mask_data_url":  strings.HasPrefix(req.MaskImageURL, "data:"),
			"sanitized_has_mask": bytes.Contains(sanitized, []byte("mask")),
		})
	})

	httpReq := httptest.NewRequest(http.MethodPost, "/upload", &body)
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := app.Test(httpReq, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, respBody)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode resp: %v", err)
	}
	if out["images_len"] != float64(1) || out["first_data_url"] != true || out["mask_data_url"] != true || out["sanitized_has_mask"] != true {
		t.Fatalf("unexpected multipart parse result: %#v", out)
	}
}

func TestImageEdit_Returns404WhenEndpointDisabled(t *testing.T) {
	db := setupImageGenerationTest(t)
	user := database.User{
		ID: 21, Username: "edit-disabled", Token: "sk-edit-disabled", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	// 模拟 admin 未启用 edits：AllowedEndpoints 默认只含 generations
	ChannelMapCache[5] = &database.Channel{ID: 5, Type: ChannelTypeCLIProxy, BaseURL: "http://unused.local", Key: "k", Status: 1}
	RouteCache["grok-imagine-image-quality"] = []*database.ChannelModel{{
		ID: 5, ChannelID: 5, ModelID: "grok-imagine-image-quality",
		ModelCategory: database.ModelCategoryImage, BillingMode: database.BillingModeImage,
		AllowedEndpoints: `["/v1/images/generations"]`, Weight: 1, Status: 1,
	}}

	app := fiber.New()
	app.Post(database.EndpointImagesEdits, ImageEditProxyHandler)
	body := `{"model":"grok-imagine-image-quality","prompt":"x","images":[{"image_url":"data:image/png;base64,AAA="}]}`
	req := httptest.NewRequest(http.MethodPost, database.EndpointImagesEdits, strings.NewReader(body))
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

func TestImageGeneration_AdminRegisteredThirdPartyModelEndToEnd(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != database.EndpointImagesGenerations {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		if body["model"] != "fal-sd-3.5-large" {
			t.Errorf("upstream model=%v want fal-sd-3.5-large", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		// 模拟第三方 OpenAI 兼容上游返回 cost_in_usd_ticks（fal.ai 等约定）
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"OUTPUT=="}],"usage":{"cost_in_usd_ticks":400000000}}`))
	}))
	t.Cleanup(backend.Close)

	// admin 步骤 1：在 ModelCatalog 注册新 image model
	if err := db.Create(&database.ModelCatalog{
		ProviderKey:    "fal",
		ProviderName:   "fal.ai",
		ModelID:        "fal-sd-3.5-large",
		DisplayName:    "Stable Diffusion 3.5 Large",
		Category:       database.ModelCategoryImage,
		BillingMode:    database.BillingModeImage,
		Supported:      true,
		Public:         true,
		DefaultEnabled: false,
	}).Error; err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	// admin 步骤 2：配 pricing rule（按 image 计费）
	if err := db.Create(&database.ModelPricingRule{
		RuleKey:         "t|fal-out|1K",
		PricingVersion:  "test",
		ProviderKey:     "fal",
		ModelID:         "fal-sd-3.5-large",
		OfficialModelID: "fal-sd-3.5-large",
		BillingMode:     database.BillingModeImage,
		Unit:            "image",
		Direction:       "output",
		Resolution:      "1K",
		PriceMicroUSD:   40_000,
	}).Error; err != nil {
		t.Fatalf("seed pricing: %v", err)
	}
	// admin 步骤 3：建 ChannelModel route + 激活
	user := database.User{
		ID: 50, Username: "fal-user", Token: "sk-fal-user", Status: 1,
		Quota: 1_000_000, BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[20] = &database.Channel{ID: 20, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["fal-sd-3.5-large"] = []*database.ChannelModel{{
		ID: 20, ChannelID: 20, ModelID: "fal-sd-3.5-large",
		ModelCategory:    database.ModelCategoryImage,
		BillingMode:      database.BillingModeImage,
		AllowedEndpoints: `["/v1/images/generations"]`,
		Weight:           1, Status: 1,
	}}

	app := fiber.New()
	app.Post(database.EndpointImagesGenerations, ImageGenerationProxyHandler)
	// client 用未带前缀的 model_id 调用
	req := httptest.NewRequest(http.MethodPost, database.EndpointImagesGenerations,
		bytes.NewBufferString(`{"model":"fal-sd-3.5-large","prompt":"x"}`))
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

	// 验证扣费正确（cost_in_usd_ticks=4e8 → 40_000 micro_usd = $0.04）
	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	const wantCost = int64(40_000)
	if fresh.Quota != user.Quota-wantCost {
		t.Fatalf("quota=%d want %d (admin-registered third-party billing should work)", fresh.Quota, user.Quota-wantCost)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("model_name = ?", "fal-sd-3.5-large").First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Unit != "image" || line.CostSource != "upstream_usage" || line.AmountMicroUSD != wantCost {
		t.Fatalf("unexpected usage line: %#v", line)
	}
}

func TestImageEdit_xAIBalanceBillingWithInputImagePricing(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != database.EndpointImagesEdits {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		if imgs, ok := body["images"].([]any); !ok || len(imgs) != 1 {
			t.Errorf("upstream body must forward images[]: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		// 上游 xAI 返回 cost_in_usd_ticks = 600,000,000 = $0.06（含 1 张 input + 1 张 output 2K）
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"OUT=="}],"usage":{"cost_in_usd_ticks":600000000}}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{
		ID: 22, Username: "edit-xai", Token: "sk-edit-xai", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// 加 input pricing rule（admin 启用 edits 必备）
	if err := db.Create(&database.ModelPricingRule{
		RuleKey: "t|in", PricingVersion: "test",
		ProviderKey: "xai", ModelID: "grok-imagine-image-quality", OfficialModelID: "grok-imagine-image-quality",
		BillingMode: database.BillingModeImage, Unit: "image", Direction: "input", PriceMicroUSD: 10_000,
	}).Error; err != nil {
		t.Fatalf("seed input pricing: %v", err)
	}
	if err := db.Create(&database.ModelPricingRule{
		RuleKey: "t|out|2K", PricingVersion: "test",
		ProviderKey: "xai", ModelID: "grok-imagine-image-quality", OfficialModelID: "grok-imagine-image-quality",
		BillingMode: database.BillingModeImage, Unit: "image", Direction: "output", Resolution: "2K", PriceMicroUSD: 70_000,
	}).Error; err != nil {
		t.Fatalf("seed output pricing: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[6] = &database.Channel{ID: 6, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["grok-imagine-image-quality"] = []*database.ChannelModel{{
		ID: 6, ChannelID: 6, ModelID: "grok-imagine-image-quality",
		ModelCategory: database.ModelCategoryImage, BillingMode: database.BillingModeImage,
		AllowedEndpoints: `["/v1/images/generations","/v1/images/edits"]`, Weight: 1, Status: 1,
	}}

	app := fiber.New()
	app.Post(database.EndpointImagesEdits, ImageEditProxyHandler)
	body := `{"model":"grok-imagine-image-quality","prompt":"x","images":[{"image_url":"data:image/png;base64,AAA="}],"resolution":"2K"}`
	req := httptest.NewRequest(http.MethodPost, database.EndpointImagesEdits, strings.NewReader(body))
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

	var fresh database.User
	if err := db.First(&fresh, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	// cost_in_usd_ticks=6e8 → 60_000 micro_usd = $0.06
	const wantCost = int64(60_000)
	if fresh.Quota != user.Quota-wantCost {
		t.Fatalf("quota=%d want %d (charged from cost_ticks)", fresh.Quota, user.Quota-wantCost)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("model_name = ?", "grok-imagine-image-quality").First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Unit != "image" || line.CostSource != "upstream_usage" || line.AmountMicroUSD != wantCost {
		t.Fatalf("unexpected usage line: %#v", line)
	}
}
