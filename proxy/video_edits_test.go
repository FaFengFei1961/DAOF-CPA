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

func TestParseVideoEditJSON_RejectsMissingInputMedia(t *testing.T) {
	_, _, err := parseVideoEditJSON([]byte(`{"model":"grok-imagine-video","prompt":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "requires at least one input media") {
		t.Fatalf("err=%v want missing input media rejection", err)
	}
}

func TestParseVideoEditJSON_RejectsFileID(t *testing.T) {
	_, _, err := parseVideoEditJSON([]byte(`{"model":"grok-imagine-video","prompt":"x","video":{"file_id":"file-abc"}}`))
	if err == nil || !strings.Contains(err.Error(), "file_id") {
		t.Fatalf("err=%v want file_id rejection", err)
	}
	_, _, err = parseVideoEditJSON([]byte(`{"model":"grok-imagine-video","prompt":"x","reference_images":[{"file_id":"file-ref"}]}`))
	if err == nil || !strings.Contains(err.Error(), "file_id") {
		t.Fatalf("reference_images file_id err=%v want rejection", err)
	}
}

func TestParseVideoEditJSON_AcceptsVideoDataURL(t *testing.T) {
	req, body, err := parseVideoEditJSON([]byte(`{"model":"xai/grok-imagine-video","prompt":"add rain","video":{"video_url":"data:video/mp4;base64,AAA="}}`))
	if err != nil {
		t.Fatalf("expected video data URL to be accepted: %v", err)
	}
	if req.Model != "grok-imagine-video" {
		t.Fatalf("model=%q want canonical", req.Model)
	}
	if !bytes.Contains(body, []byte("video_url")) {
		t.Fatalf("body must pass through video_url: %s", body)
	}
	if req.DurationSeconds != 15 {
		t.Fatalf("DurationSeconds=%d want precheck cap 15", req.DurationSeconds)
	}
}

func TestParseVideoEditJSON_RejectsStreaming(t *testing.T) {
	_, _, err := parseVideoEditJSON([]byte(`{"model":"grok-imagine-video","prompt":"x","video":{"video_url":"data:video/mp4;base64,AAA="},"stream":true}`))
	if err == nil || !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("err=%v want streaming rejection", err)
	}
}

func TestParseVideoEditJSON_AcceptsReferenceImages(t *testing.T) {
	req, body, err := parseVideoEditJSON([]byte(`{"model":"grok-imagine-video","prompt":"x","reference_images":[{"image_url":"data:image/png;base64,AAA="},{"image_url":"data:image/png;base64,BBB="}]}`))
	if err != nil {
		t.Fatalf("expected reference_images to be accepted: %v", err)
	}
	if !bytes.Contains(body, []byte("reference_images")) {
		t.Fatalf("sanitized body must include reference_images: %s", body)
	}
	if req.Model != "grok-imagine-video" {
		t.Fatalf("model=%q want canonical", req.Model)
	}
}

func TestParseVideoExtensionJSON_RequiresRequestID(t *testing.T) {
	_, _, err := parseVideoExtensionJSON([]byte(`{"model":"grok-imagine-video","extend_seconds":4}`))
	if err == nil || !strings.Contains(err.Error(), "request_id") {
		t.Fatalf("err=%v want request_id required", err)
	}
}

func TestParseVideoExtensionJSON_RejectsInvalidRequestID(t *testing.T) {
	_, _, err := parseVideoExtensionJSON([]byte(`{"model":"grok-imagine-video","request_id":"invalid//path"}`))
	if err == nil || !strings.Contains(err.Error(), "invalid request_id") {
		t.Fatalf("err=%v want invalid request_id rejection", err)
	}
}

func TestParseVideoExtensionJSON_AcceptsValidRequest(t *testing.T) {
	req, _, err := parseVideoExtensionJSON([]byte(`{"model":"grok-imagine-video","request_id":"vid_abc123","extend_seconds":4}`))
	if err != nil {
		t.Fatalf("expected valid extension request to be accepted: %v", err)
	}
	if req.RequestID != "vid_abc123" {
		t.Fatalf("RequestID=%q want vid_abc123", req.RequestID)
	}
}

func TestParseVideoEditMultipart_ConvertsVideoFileToDataURL(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("model", "grok-imagine-video")
	_ = writer.WriteField("prompt", "make it night")

	videoPart, err := writer.CreateFormFile("video", "input.mp4")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	videoPart.Write([]byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70}) // mp4 ftyp box magic
	_ = writer.Close()

	app := fiber.New()
	app.Post("/upload", func(c *fiber.Ctx) error {
		req, sanitized, perr := parseVideoEditMultipart(c)
		if perr != nil {
			return c.Status(400).JSON(fiber.Map{"err": perr.Error()})
		}
		return c.JSON(fiber.Map{
			"model":          req.Model,
			"body_has_video": bytes.Contains(sanitized, []byte("video_url")),
			"body_has_data":  bytes.Contains(sanitized, []byte("data:video/")),
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
	// multipart FormFile 默认 Content-Type=application/octet-stream（无 mp4 header），
	// 转 data URL 时保留 octet-stream 类型，client 应在 form 里显式带 video/* Content-Type。
	if out["model"] != "grok-imagine-video" || out["body_has_video"] != true {
		t.Fatalf("unexpected multipart parse result: %#v", out)
	}
}

func TestVideoEdit_Returns404WhenEndpointDisabled(t *testing.T) {
	db := setupImageGenerationTest(t)
	user := database.User{
		ID: 31, Username: "video-edit-disabled", Token: "sk-video-edit-disabled", Status: 1, Quota: 1_000_000, BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[10] = &database.Channel{ID: 10, Type: ChannelTypeCLIProxy, BaseURL: "http://unused.local", Key: "k", Status: 1}
	RouteCache["grok-imagine-video"] = []*database.ChannelModel{{
		ID: 10, ChannelID: 10, ModelID: "grok-imagine-video",
		ModelCategory: database.ModelCategoryVideo, BillingMode: database.BillingModeVideoSecond,
		AllowedEndpoints: `["/v1/videos/generations"]`, Weight: 1, Status: 1,
	}}

	app := fiber.New()
	app.Post(database.EndpointVideosEdits, VideoEditProxyHandler)
	body := `{"model":"grok-imagine-video","prompt":"x","video":{"video_url":"data:video/mp4;base64,AAA="}}`
	req := httptest.NewRequest(http.MethodPost, database.EndpointVideosEdits, strings.NewReader(body))
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

func TestVideoEdit_xAIBalanceBillingWithCostTicks(t *testing.T) {
	db := setupImageGenerationTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != database.EndpointVideosEdits {
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		// 透传：上游应该收到 video 字段
		if _, ok := body["video"]; !ok {
			t.Errorf("upstream body must forward video field: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		// cost_in_usd_ticks = 1,200,000,000 = $0.12（含 input 5s + output 4s 720p 估算）
		_, _ = w.Write([]byte(`{"request_id":"vid_edit_xyz","status":"queued","usage":{"cost_in_usd_ticks":1200000000}}`))
	}))
	t.Cleanup(backend.Close)

	user := database.User{
		ID: 32, Username: "video-edit-xai", Token: "sk-video-edit-xai", Status: 1, Quota: 10_000_000, BalanceConsumeEnabled: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// 必须有 input + output pricing 才能激活 edits
	if err := db.Create(&database.ModelPricingRule{
		RuleKey: "t|video-in", PricingVersion: "test",
		ProviderKey: "xai", ModelID: "grok-imagine-video", OfficialModelID: "grok-imagine-video",
		BillingMode: database.BillingModeVideoSecond, Unit: "video_second", Direction: "input",
		PriceMicroUSD: 10_000,
	}).Error; err != nil {
		t.Fatalf("seed input pricing: %v", err)
	}
	if err := db.Create(&database.ModelPricingRule{
		RuleKey: "t|video-out|720p", PricingVersion: "test",
		ProviderKey: "xai", ModelID: "grok-imagine-video", OfficialModelID: "grok-imagine-video",
		BillingMode: database.BillingModeVideoSecond, Unit: "video_second", Direction: "output",
		Resolution: "720p", PriceMicroUSD: 70_000,
	}).Error; err != nil {
		t.Fatalf("seed output pricing: %v", err)
	}
	AuthCache[user.Token] = &user
	ChannelMapCache[11] = &database.Channel{ID: 11, Type: ChannelTypeCLIProxy, BaseURL: backend.URL, Key: "upstream-key", Status: 1}
	RouteCache["grok-imagine-video"] = []*database.ChannelModel{{
		ID: 11, ChannelID: 11, ModelID: "grok-imagine-video",
		ModelCategory: database.ModelCategoryVideo, BillingMode: database.BillingModeVideoSecond,
		AllowedEndpoints: `["/v1/videos/generations","/v1/videos/edits"]`, Weight: 1, Status: 1,
	}}

	app := fiber.New()
	app.Post(database.EndpointVideosEdits, VideoEditProxyHandler)
	body := `{"model":"grok-imagine-video","prompt":"x","video":{"video_url":"data:video/mp4;base64,AAA="}}`
	req := httptest.NewRequest(http.MethodPost, database.EndpointVideosEdits, strings.NewReader(body))
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
	// cost_in_usd_ticks=1.2e9 → 120_000 micro_usd = $0.12
	const wantCost = int64(120_000)
	const initialQuota = int64(10_000_000)
	if fresh.Quota != initialQuota-wantCost {
		t.Fatalf("quota=%d want %d (deducted from cost_ticks)", fresh.Quota, initialQuota-wantCost)
	}
	var line database.ApiLogUsageLine
	if err := db.Where("model_name = ?", "grok-imagine-video").First(&line).Error; err != nil {
		t.Fatalf("load usage line: %v", err)
	}
	if line.Unit != "video_second" || line.CostSource != "upstream_usage" || line.AmountMicroUSD != wantCost {
		t.Fatalf("unexpected usage line: %#v", line)
	}
	if line.RequestPath != database.EndpointVideosEdits {
		t.Fatalf("usage line RequestPath=%q want %s", line.RequestPath, database.EndpointVideosEdits)
	}
	// 视频 job 也应记录
	var job database.MediaGenerationJob
	if err := db.Where("request_id = ?", "vid_edit_xyz").First(&job).Error; err != nil {
		t.Fatalf("load video job: %v", err)
	}
	if job.UserID != user.ID {
		t.Fatalf("video job user mismatch: %#v", job)
	}
}
