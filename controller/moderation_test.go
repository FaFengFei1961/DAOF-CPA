package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupModerationControllerTestApp(t *testing.T, cfg map[string]string) *fiber.App {
	t.Helper()
	utils.InitCrypto()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&database.Channel{}, &database.ChannelModel{}, &database.User{}, &database.SysConfig{}, &database.AccessToken{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for key, value := range cfg {
		encrypted, err := utils.Encrypt(value)
		if err != nil {
			t.Fatalf("encrypt %s: %v", key, err)
		}
		if err := db.Create(&database.SysConfig{Key: key, Value: encrypted}).Error; err != nil {
			t.Fatalf("seed sysconfig %s: %v", key, err)
		}
	}
	database.DB = db
	proxy.ResetModerationCacheSecret()
	t.Setenv("MODERATION_CACHE_SECRET", "controller-moderation-test-secret")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/api/admin/moderation/test", TestModerationConfig)
	app.Post("/api/admin/moderation/keywords/generate", GenerateModerationKeywords)
	return app
}

func TestModerationConfig_MasksCliproxyURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"decision\":\"allow\",\"category\":\"\",\"confidence\":0.01,\"reason\":\"ok\"}"}}]}`))
	}))
	defer server.Close()

	app := setupModerationControllerTestApp(t, map[string]string{
		"moderation_provider":       "cliproxy_model",
		"moderation_cliproxy_model": "gpt-5.4-mini",
		"cliproxy_url":              server.URL + "/tenant/secret-token",
	})
	resp, err := app.Test(httptest.NewRequest("POST", "/api/admin/moderation/test", nil))
	if err != nil {
		t.Fatalf("test moderation config: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var decoded struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Endpoint != server.URL {
		t.Fatalf("endpoint=%q want %q", decoded.Endpoint, server.URL)
	}
	if strings.Contains(decoded.Endpoint, "tenant") || strings.Contains(decoded.Endpoint, "secret-token") || strings.Contains(decoded.Endpoint, "/v1/chat/completions") {
		t.Fatalf("endpoint leaked path: %q", decoded.Endpoint)
	}
}

func TestGenerateModerationKeywords_MasksCliproxyURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"candidates\":[{\"category\":\"jailbreak\",\"keyword\":\"ignore safeguards\",\"severity\":\"high\",\"reason\":\"test\"}]}"}}]}`))
	}))
	defer server.Close()

	app := setupModerationControllerTestApp(t, map[string]string{
		"moderation_provider":                  "cliproxy_model",
		"moderation_cliproxy_model":            "gpt-5.4-mini",
		"cliproxy_url":                         server.URL + "/tenant/secret-token",
		"moderation_keyword_ai_max_candidates": "1",
	})
	body := bytes.NewBufferString(`{"focus":"prompt injection","max_candidates":1}`)
	req := httptest.NewRequest("POST", "/api/admin/moderation/keywords/generate", body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("generate keywords: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var decoded struct {
		Endpoint string `json:"endpoint"`
		Data     []any  `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Endpoint != server.URL {
		t.Fatalf("endpoint=%q want %q", decoded.Endpoint, server.URL)
	}
	if strings.Contains(decoded.Endpoint, "tenant") || strings.Contains(decoded.Endpoint, "secret-token") || strings.Contains(decoded.Endpoint, "/v1/chat/completions") {
		t.Fatalf("endpoint leaked path: %q", decoded.Endpoint)
	}
	if len(decoded.Data) != 1 {
		t.Fatalf("candidate count=%d want 1", len(decoded.Data))
	}
}
