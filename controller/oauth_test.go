package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"
	"daof-ai-hub/proxy"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSuggestUsernameFromOAuthName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "github hyphen", in: "354685856-sn", want: "354685856_sn"},
		{name: "trim invalid edges", in: "--alice--", want: "alice"},
		{name: "fallback empty", in: "---", want: "user"},
		{name: "keep han", in: "测试-user", want: "测试_user"},
		{name: "limit runes", in: "abcdefghijklmnopqrstuvwxyz", want: "abcdefghijklmnopqrst"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := suggestUsernameFromOAuthName(tt.in); got != tt.want {
				t.Fatalf("suggestUsernameFromOAuthName(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func setupOAuthControllerTestDB(t *testing.T) {
	t.Helper()
	utils.InitCrypto()
	var err error
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	database.DB, err = gorm.Open(sqlite.Open("file:"+dbName+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := database.DB.AutoMigrate(
		&database.User{},
		&database.UserSession{},
		&database.OperationLog{},
		&database.Channel{},
		&database.ChannelModel{},
		&database.SysConfig{},
		&database.AccessToken{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database.DB.Exec("DELETE FROM operation_logs")
	database.DB.Exec("DELETE FROM user_sessions")
	database.DB.Exec("DELETE FROM users")
	resetOAuthStatesForTest()
	resetTmpTokenConsumedForTest()
}

func resetOAuthStatesForTest() {
	oauthStateStore.Range(func(key, value any) bool {
		oauthStateStore.Delete(key)
		return true
	})
}

func resetTmpTokenConsumedForTest() {
	tmpTokenConsumedStore.Range(func(key, value any) bool {
		tmpTokenConsumedStore.Delete(key)
		return true
	})
}

func setOAuthSysConfigForTest(t *testing.T) {
	t.Helper()
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	proxy.SysConfigCache = map[string]string{
		"github_client_id":             "client-id",
		"github_client_secret":         "client-secret",
		"server_address":               "http://example.test",
		"server_address_require_https": "false",
	}
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	})
}

func prepareOAuthStateForTest(t *testing.T) (state, verifier string) {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/prepare", PrepareOAuthState)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/prepare", nil))
	if err != nil {
		t.Fatalf("prepare request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prepare status=%d", resp.StatusCode)
	}
	var body struct {
		Success             bool   `json:"success"`
		State               string `json:"state"`
		CodeChallenge       string `json:"code_challenge"`
		CodeChallengeMethod string `json:"code_challenge_method"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode prepare: %v", err)
	}
	raw, ok := oauthStateStore.Load(body.State)
	if !ok {
		t.Fatalf("state %q not stored", body.State)
	}
	record, ok := raw.(oauthStateRecord)
	if !ok {
		t.Fatalf("stored state has type %T", raw)
	}
	if body.CodeChallenge != pkceChallenge(record.CodeVerifier) {
		t.Fatalf("code_challenge mismatch")
	}
	return body.State, record.CodeVerifier
}

func installMockGitHub(t *testing.T, expectedVerifier string) *atomic.Int64 {
	t.Helper()
	var tokenHits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			tokenHits.Add(1)
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode token request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if payload["redirect_uri"] != "http://example.test/oauth/github" {
				t.Errorf("redirect_uri=%q", payload["redirect_uri"])
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if payload["code_verifier"] != expectedVerifier {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad_verifier"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"github-access"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer github-access" {
				t.Errorf("bad github user authorization: %q", r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":12345,"login":"octo"}`))
		default:
			t.Errorf("unexpected GitHub mock path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	oldTokenEndpoint := githubTokenEndpoint
	oldUserEndpoint := githubUserEndpoint
	oldClient := githubHTTPClient
	githubTokenEndpoint = server.URL + "/login/oauth/access_token"
	githubUserEndpoint = server.URL + "/user"
	githubHTTPClient = server.Client()
	t.Cleanup(func() {
		githubTokenEndpoint = oldTokenEndpoint
		githubUserEndpoint = oldUserEndpoint
		githubHTTPClient = oldClient
	})
	return &tokenHits
}

func postGithubCallback(t *testing.T, app *fiber.App, code, state string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(
		http.MethodPost,
		fmt.Sprintf("/callback?code=%s&state=%s", code, state),
		bytes.NewBufferString(`{"ref":""}`),
	)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	return resp
}

func TestPrepareOAuthState_ReturnsChallenge(t *testing.T) {
	resetOAuthStatesForTest()
	state, verifier := prepareOAuthStateForTest(t)
	if len(state) != 64 {
		t.Fatalf("state length=%d, want 64", len(state))
	}
	if verifier == "" {
		t.Fatal("verifier is empty")
	}
	if _, ok := consumeOAuthState(state); !ok {
		t.Fatal("first state consume failed")
	}
	if _, ok := consumeOAuthState(state); ok {
		t.Fatal("second state consume succeeded; state must be one-time")
	}
}

func TestGithubCallback_RejectsReusedState(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	user := database.User{Username: "octo", GithubID: "12345", Role: "user", Token: "sk-daof-octo", Status: 1}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	state, verifier := prepareOAuthStateForTest(t)
	tokenHits := installMockGitHub(t, verifier)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/callback", GithubCallback)

	first := postGithubCallback(t, app, "code-ok", state)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first callback status=%d", first.StatusCode)
	}
	var firstBody struct {
		Success   bool   `json:"success"`
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstBody); err != nil {
		t.Fatalf("decode first callback: %v", err)
	}
	if !firstBody.Success || !database.IsSessionID(firstBody.SessionID) {
		t.Fatalf("first callback did not return session: %#v", firstBody)
	}

	second := postGithubCallback(t, app, "code-ok", state)
	if second.StatusCode != http.StatusForbidden {
		t.Fatalf("second callback status=%d, want 403", second.StatusCode)
	}
	var secondBody struct {
		MessageCode string `json:"message_code"`
	}
	if err := json.NewDecoder(second.Body).Decode(&secondBody); err != nil {
		t.Fatalf("decode second callback: %v", err)
	}
	if secondBody.MessageCode != "ERR_OAUTH_STATE_INVALID" {
		t.Fatalf("message_code=%q", secondBody.MessageCode)
	}
	if tokenHits.Load() != 1 {
		t.Fatalf("token endpoint hits=%d, want 1", tokenHits.Load())
	}
}

func TestGithubCallback_RejectsMismatchedVerifier(t *testing.T) {
	setupOAuthControllerTestDB(t)
	setOAuthSysConfigForTest(t)
	user := database.User{Username: "octo", GithubID: "12345", Role: "user", Token: "sk-daof-octo", Status: 1}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	state, verifier := prepareOAuthStateForTest(t)
	oauthStateStore.Store(state, oauthStateRecord{CodeVerifier: "attacker-verifier", ExpiresAt: time.Now().Add(oauthStateTTL)})
	installMockGitHub(t, verifier)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/callback", GithubCallback)
	resp := postGithubCallback(t, app, "code-ok", state)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("callback status=%d, want 401", resp.StatusCode)
	}
	var count int64
	database.DB.Model(&database.UserSession{}).Count(&count)
	if count != 0 {
		t.Fatalf("sessions created=%d, want 0", count)
	}
}

func TestCompleteProfile_UsesBalanceConsumeDefaultLimitMicroUSD(t *testing.T) {
	setupOAuthControllerTestDB(t)
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	proxy.SysConfigCache = map[string]string{
		"signup_bonus":                             "0",
		"balance_consume_default_enabled":          "true",
		balanceConsumeDefaultLimitMicroUSDKey:      "1234567",
		"balance_consume_default_window_secs":      "86400",
		deprecatedBalanceConsumeDefaultLimitUSDKey: "99.99",
	}
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	})

	tmpToken, err := utils.Encrypt(fmt.Sprintf("clean|gh-limit-default|octo||%d", time.Now().Unix()))
	if err != nil {
		t.Fatalf("encrypt tmp token: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/complete-profile", CompleteProfile)
	body, _ := json.Marshal(map[string]string{
		"tmp_token": tmpToken,
		"username":  "limit_user",
	})
	req := httptest.NewRequest(http.MethodPost, "/complete-profile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("complete profile request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete profile status=%d", resp.StatusCode)
	}

	var user database.User
	if err := database.DB.Where("github_id = ?", "gh-limit-default").First(&user).Error; err != nil {
		t.Fatalf("load created user: %v", err)
	}
	if !user.BalanceConsumeEnabled {
		t.Fatal("BalanceConsumeEnabled=false, want true")
	}
	if user.BalanceConsumeLimitUSD != 1234567 {
		t.Fatalf("BalanceConsumeLimitUSD=%d, want 1234567", user.BalanceConsumeLimitUSD)
	}
	if user.BalanceConsumeWindowSeconds != 86400 {
		t.Fatalf("BalanceConsumeWindowSeconds=%d, want 86400", user.BalanceConsumeWindowSeconds)
	}
}

func TestTmpTokenSingleConsume_RejectsReplay(t *testing.T) {
	setupOAuthControllerTestDB(t)
	proxy.SysConfigMutex.Lock()
	old := proxy.SysConfigCache
	proxy.SysConfigCache = map[string]string{"signup_bonus": "0"}
	proxy.SysConfigMutex.Unlock()
	t.Cleanup(func() {
		proxy.SysConfigMutex.Lock()
		proxy.SysConfigCache = old
		proxy.SysConfigMutex.Unlock()
	})

	tmpToken, err := utils.Encrypt(fmt.Sprintf("clean|gh-single-consume|octo||%d", time.Now().Unix()))
	if err != nil {
		t.Fatalf("encrypt tmp token: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/complete-profile", CompleteProfile)

	body, _ := json.Marshal(map[string]string{
		"tmp_token": tmpToken,
		"username":  "single_consume",
	})
	req := httptest.NewRequest(http.MethodPost, "/complete-profile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first complete profile status=%d, want 200", resp.StatusCode)
	}

	body, _ = json.Marshal(map[string]string{
		"tmp_token": tmpToken,
		"username":  "single_consume_2",
	})
	req = httptest.NewRequest(http.MethodPost, "/complete-profile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatalf("replay request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("replay status=%d, want 403", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode replay body: %v", err)
	}
	if got["message_code"] != "ERR_TMP_TOKEN_ALREADY_USED" {
		t.Fatalf("message_code=%v, want ERR_TMP_TOKEN_ALREADY_USED", got["message_code"])
	}
}

func TestLogout_RevokesSession(t *testing.T) {
	setupOAuthControllerTestDB(t)
	user := database.User{Username: "logout_user", Role: "user", Token: "sk-daof-logout", Status: 1}
	if err := database.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	sessionID, err := database.CreateUserSession(user.ID, "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/me", middleware.UserGuard, GetSelfData)
	app.Post("/logout", middleware.UserGuard, AuthLogout)

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+sessionID)
	before, err := app.Test(req)
	if err != nil {
		t.Fatalf("before request: %v", err)
	}
	if before.StatusCode != http.StatusOK {
		t.Fatalf("before status=%d", before.StatusCode)
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+sessionID)
	logoutResp, err := app.Test(logoutReq)
	if err != nil {
		t.Fatalf("logout request: %v", err)
	}
	if logoutResp.StatusCode != http.StatusOK {
		t.Fatalf("logout status=%d", logoutResp.StatusCode)
	}

	afterReq := httptest.NewRequest(http.MethodGet, "/me", nil)
	afterReq.Header.Set("Authorization", "Bearer "+sessionID)
	after, err := app.Test(afterReq)
	if err != nil {
		t.Fatalf("after request: %v", err)
	}
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after status=%d, want 401", after.StatusCode)
	}
}
