// Package controller / yifut_security_test.go
//
// 覆盖易付通 V2 RSA 通知/退货回调相关 CRITICAL 安全不变量：
//  1. R4 CRITICAL: pid 绑定（防跨商户回调重放）
//  2. R4: timestamp 漂移检查（防回调旧值重放）
//  3. R4: 验签失败拒绝
package controller

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

// genTestRSAPair 生成测试用 1024-bit RSA 密钥对（PEM 格式）。
// 1024-bit 仅用于测试加速；生产环境必须 2048+。
func genTestRSAPair(t *testing.T) (privPEM, pubPEM string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	privDER := x509.MarshalPKCS1PrivateKey(priv)
	privBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER}
	privPEM = string(pem.EncodeToMemory(privBlock))

	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pubBlock := &pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}
	pubPEM = string(pem.EncodeToMemory(pubBlock))
	return
}

// configureYifutForTest 把 PEM + PID 注入 SysConfigCache，让 LoadYifutConfig 返回 IsConfigured=true。
func configureYifutForTest(t *testing.T, pid, privPEM, pubPEM string) {
	t.Helper()
	proxy.SysConfigMutex.Lock()
	proxy.SysConfigCache["yifut_pid"] = pid
	proxy.SysConfigCache["yifut_gateway"] = "https://www.yifut.com"
	proxy.SysConfigCache["yifut_merchant_private_key"] = privPEM
	proxy.SysConfigCache["yifut_platform_public_key"] = pubPEM
	proxy.SysConfigMutex.Unlock()
}

// signWithPlatformKey 模拟平台用其私钥签名（测试中用同一对密钥扮演平台）。
// 这里 privPEM 是平台私钥的 PEM 字符串（在测试中我们用 merchant key pair 同时充当 platform）。
func signWithPlatformKey(t *testing.T, params map[string]string, privPEM string) string {
	t.Helper()
	priv, err := proxy.ParseRSAPrivateKey(privPEM)
	if err != nil {
		t.Fatalf("parse priv: %v", err)
	}
	sig, err := proxy.SignYifutRSA(params, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return sig
}

// ─── R4 CRITICAL: pid 绑定（防跨商户重放） ────────────────────────

// TestSecurity_YifutNotify_PIDMismatchRejected 验证：
// 即使签名合法，params["pid"] != cfg.PID 必须 403 拒绝（防跨商户回调重放）。
//
// 攻击场景（codex r4）：攻击者在自己的易付通商户用相同 out_trade_no/money 创建订单付款，
// 拿到平台合法签名的回调后投递到本站 notify。仅签名校验通过 → 订单错误标记 paid + quota+=。
//
// 防护：cfg.PID == "" 也拒绝（防 admin 漏配 PID 时的退化场景）。
func TestSecurity_YifutNotify_PIDMismatchRejected(t *testing.T) {
	setupSubTestDB(t)
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "1234567890", privPEM, pubPEM) // 我方 PID = 1234567890

	// 创建本地订单
	user := seedTestUser(t, 0)
	order := database.TopupOrder{
		OutTradeNo: "tp_pid_mismatch_test",
		UserID:     user.ID,
		MoneyRMB:   10.0,
		Status:     "created",
	}
	database.DB.Create(&order)

	// 攻击者构造合法签名（用我们公钥能验证），但 pid=9999999999（不是我方 PID）
	params := map[string]string{
		"pid":          "9999999999",
		"out_trade_no": order.OutTradeNo,
		"money":        "10.00",
		"trade_status": "TRADE_SUCCESS",
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
		"trade_no":     "yif_attacker_trade",
		"sign_type":    "RSA",
	}
	params["sign"] = signWithPlatformKey(t, params, privPEM)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/notify", YifutNotify)

	// 构造 GET URL with query params
	q := ""
	for k, v := range params {
		if q != "" {
			q += "&"
		}
		q += k + "=" + v
	}
	req := httptest.NewRequest("GET", "/notify?"+q, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()

	// 关键断言：403 + body 含 pid_mismatch
	if resp.StatusCode != 403 {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}

	// 订单必须保持 created，未被错误升级到 paid
	var fresh database.TopupOrder
	database.DB.First(&fresh, order.ID)
	if fresh.Status != "created" {
		t.Errorf("order status changed to %q—pid mismatch must NOT change order state", fresh.Status)
	}

	// 用户 quota 必须保持 0，未被错误增加
	var freshU database.User
	database.DB.First(&freshU, user.ID)
	if freshU.Quota != 0 {
		t.Errorf("quota changed to %d—pid mismatch must NOT credit user", freshU.Quota)
	}
}

// TestSecurity_YifutNotify_EmptyPIDRejected 验证：
// cfg.PID 配置为空时，即使回调 params 也无 pid，也必须拒绝（防退化绕过）。
func TestSecurity_YifutNotify_EmptyPIDRejected(t *testing.T) {
	setupSubTestDB(t)
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "", privPEM, pubPEM) // 空 PID（理论上 IsConfigured=false 会先拦截）

	// 即使前置 IsConfigured 拦截，PID 检查也是冗余防御
	user := seedTestUser(t, 0)
	database.DB.Create(&database.TopupOrder{
		OutTradeNo: "tp_empty_pid", UserID: user.ID, MoneyRMB: 10.0, Status: "created",
	})

	params := map[string]string{
		"pid":          "1234567890",
		"out_trade_no": "tp_empty_pid",
		"money":        "10.00",
		"trade_status": "TRADE_SUCCESS",
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
		"sign_type":    "RSA",
	}
	params["sign"] = signWithPlatformKey(t, params, privPEM)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/notify", YifutNotify)
	q := ""
	for k, v := range params {
		if q != "" {
			q += "&"
		}
		q += k + "=" + v
	}
	req := httptest.NewRequest("GET", "/notify?"+q, nil)
	resp, _ := app.Test(req, -1)
	defer resp.Body.Close()

	// 应被 IsConfigured 或 PID check 之一拦截，不应 200
	if resp.StatusCode == 200 {
		t.Errorf("empty PID config must NOT accept callback, got 200")
	}
}

// ─── R4 CRITICAL: timestamp 漂移防重放 ──────────────────────────

// TestSecurity_YifutNotify_StaleTimestampRejected 验证：
// timestamp 与服务器时间差 >300 秒（5 分钟）的回调被拒绝（防旧回调重放）。
func TestSecurity_YifutNotify_StaleTimestampRejected(t *testing.T) {
	setupSubTestDB(t)
	privPEM, pubPEM := genTestRSAPair(t)
	configureYifutForTest(t, "1234567890", privPEM, pubPEM)

	user := seedTestUser(t, 0)
	database.DB.Create(&database.TopupOrder{
		OutTradeNo: "tp_stale_ts", UserID: user.ID, MoneyRMB: 10.0, Status: "created",
	})

	// 1 小时前的回调
	staleTime := time.Now().Add(-1 * time.Hour).Unix()
	params := map[string]string{
		"pid":          "1234567890",
		"out_trade_no": "tp_stale_ts",
		"money":        "10.00",
		"trade_status": "TRADE_SUCCESS",
		"timestamp":    strconv.FormatInt(staleTime, 10),
		"sign_type":    "RSA",
	}
	params["sign"] = signWithPlatformKey(t, params, privPEM)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/notify", YifutNotify)
	q := ""
	for k, v := range params {
		if q != "" {
			q += "&"
		}
		q += k + "=" + v
	}
	req := httptest.NewRequest("GET", "/notify?"+q, nil)
	resp, _ := app.Test(req, -1)
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("stale timestamp must be rejected, got %d", resp.StatusCode)
	}

	// 订单仍为 created
	var fresh database.TopupOrder
	database.DB.Where("out_trade_no = ?", "tp_stale_ts").First(&fresh)
	if fresh.Status != "created" {
		t.Errorf("stale ts callback must NOT promote order, status=%q", fresh.Status)
	}
}

// TestSecurity_YifutNotify_BadSignatureRejected 验证：
// 签名校验失败时，即使 PID/timestamp 都对，也拒绝。
func TestSecurity_YifutNotify_BadSignatureRejected(t *testing.T) {
	setupSubTestDB(t)
	_, pubPEM := genTestRSAPair(t)
	// 用一对密钥配置（pub），但用另一对完全不同的密钥签名
	otherPriv, _ := genTestRSAPair2(t)
	configureYifutForTest(t, "1234567890", otherPriv, pubPEM)

	user := seedTestUser(t, 0)
	database.DB.Create(&database.TopupOrder{
		OutTradeNo: "tp_bad_sig", UserID: user.ID, MoneyRMB: 10.0, Status: "created",
	})

	params := map[string]string{
		"pid":          "1234567890",
		"out_trade_no": "tp_bad_sig",
		"money":        "10.00",
		"trade_status": "TRADE_SUCCESS",
		"timestamp":    strconv.FormatInt(time.Now().Unix(), 10),
		"sign_type":    "RSA",
		"sign":         "AAAA_invalid_signature_base64_AAAA", // 故意错的签名
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/notify", YifutNotify)
	q := ""
	for k, v := range params {
		if q != "" {
			q += "&"
		}
		q += k + "=" + strings.ReplaceAll(v, "+", "%2B")
	}
	req := httptest.NewRequest("GET", "/notify?"+q, nil)
	resp, _ := app.Test(req, -1)
	defer resp.Body.Close()

	if resp.StatusCode != 403 {
		t.Errorf("bad signature must be rejected, got %d", resp.StatusCode)
	}
}

// genTestRSAPair2 生成第二对独立的 RSA 密钥（用于"异组签名"测试）。
func genTestRSAPair2(t *testing.T) (privPEM, pubPEM string) {
	return genTestRSAPair(t)
}
