package controller

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// registerMu 保护"cap 检查 + 创建用户"为临界区，避免两个并发新注册都通过 cap 检查
// 之后导致 user 总数超过 max_users。SQLite 的串行写只能部分缓解，不能确定性消除。
var (
	registerMu                            sync.Mutex
	deprecatedBalanceConsumeLimitWarnOnce sync.Once
)

const oauthStateTTL = 5 * time.Minute

type oauthStateRecord struct {
	CodeVerifier string
	ExpiresAt    time.Time
}

var (
	oauthStateStore       sync.Map // key: state, value: oauthStateRecord
	oauthStateJanitorOnce sync.Once

	githubTokenEndpoint = "https://github.com/login/oauth/access_token"
	githubUserEndpoint  = "https://api.github.com/user"
	githubHTTPClient    = &http.Client{Timeout: 10 * time.Second}
)

// tmp_token TTL：超过此时长视为过期
const tmpTokenTTL = 15 * time.Minute

// parseTmpToken 解析并校验 OAuth 流程中的 tmp_token
// payload 形如：(clean|sms)|ghID|ghName|ref|timestamp
// 返回 (tokenType, refUser, originalDecryptedStr, error)
func parseTmpToken(tmpToken string) (string, string, string, error) {
	decrypted, err := utils.Decrypt(tmpToken)
	if err != nil || decrypted == "" {
		return "", "", "", fmt.Errorf("无效或被篡改的风控票据")
	}
	parts := strings.Split(decrypted, "|")
	if len(parts) < 5 {
		return "", "", "", fmt.Errorf("票据格式损坏")
	}
	tokenType := parts[0]
	if tokenType != "clean" && tokenType != "sms" {
		return "", "", "", fmt.Errorf("票据类型未知")
	}
	tsStr := parts[4]
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", "", "", fmt.Errorf("票据时间戳损坏")
	}
	issued := time.Unix(ts, 0)
	if time.Since(issued) > tmpTokenTTL {
		return "", "", "", fmt.Errorf("票据已过期，请重新登录")
	}
	if time.Since(issued) < -2*time.Minute {
		// 时钟漂移容忍，但不允许显著未来时间
		return "", "", "", fmt.Errorf("票据时间异常")
	}
	refUser := parts[3]
	return tokenType, refUser, decrypted, nil
}

// PrepareOAuthState 给前端发起 OAuth 之前调用。服务端生成一次性 state 和 PKCE verifier，
// 只把 state + code_challenge 下发给前端，verifier 留在服务端 5 分钟内存表。
func PrepareOAuthState(c *fiber.Ctx) error {
	state, err := randomHex(32)
	if err != nil {
		log.Printf("[OAUTH] generate state failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_GITHUB_INTERNAL"})
	}
	verifier, err := generatePKCEVerifier()
	if err != nil {
		log.Printf("[OAUTH] generate PKCE verifier failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_GITHUB_INTERNAL"})
	}
	storeOAuthState(state, verifier)
	return c.JSON(fiber.Map{
		"success":               true,
		"state":                 state,
		"code_challenge":        pkceChallenge(verifier),
		"code_challenge_method": "S256",
	})
}

func generatePKCEVerifier() (string, error) {
	var b [64]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func randomHex(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func storeOAuthState(state, verifier string) {
	startOAuthStateJanitor()
	oauthStateStore.Store(state, oauthStateRecord{
		CodeVerifier: verifier,
		ExpiresAt:    time.Now().Add(oauthStateTTL),
	})
}

func consumeOAuthState(state string) (string, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return "", false
	}
	raw, ok := oauthStateStore.LoadAndDelete(state)
	if !ok {
		return "", false
	}
	record, ok := raw.(oauthStateRecord)
	if !ok || record.CodeVerifier == "" || time.Now().After(record.ExpiresAt) {
		return "", false
	}
	return record.CodeVerifier, true
}

func startOAuthStateJanitor() {
	oauthStateJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				cleanupExpiredOAuthStates(time.Now())
			}
		}()
	})
}

func cleanupExpiredOAuthStates(now time.Time) {
	oauthStateStore.Range(func(key, value any) bool {
		record, ok := value.(oauthStateRecord)
		if !ok || now.After(record.ExpiresAt) {
			oauthStateStore.Delete(key)
		}
		return true
	})
}

// oauthStateInvalidMessageCode 暴露给前端的统一 message_code。
// 直接返回常量字面量，i18n 覆盖测试可通过 AST 扫描捕获，避免漏译。
func oauthStateInvalidMessageCode() string {
	return "ERR_OAUTH_STATE_INVALID"
}

// maskPhone 把手机号脱敏成 138****8888
func maskPhone(phone string) string {
	if len(phone) < 8 {
		return "****"
	}
	return phone[:3] + "****" + phone[len(phone)-4:]
}

// GithubAuthRequest 承接前台发来的 OAuth Code 和可选的推荐人标识
type GithubAuthRequest struct {
	Code  string `json:"code"`  // 已废弃：code 必须从 query 读取
	State string `json:"state"` // 已废弃：state 必须从 query 读取
	Ref   string `json:"ref"`   // 推荐人 username，可选；若有效则发拉新奖励
}

// resolveBonusConfig 从 SysConfig 读取新用户奖励三参数。
// 三个 key 都使用 micro_usd 整数字符串；未配置时给默认值
// （signup=$1.0, referrer=0, referee=0）。返回 micro_usd（int64）。
func resolveBonusConfig() (signupMicro, referrerMicro, refereeMicro int64) {
	return readMicroUSDConfig("signup_bonus", database.MicroPerUSD),
		readMicroUSDConfig("referrer_bonus", 0),
		readMicroUSDConfig("referee_bonus", 0)
}

func readDefaultBalanceConsumeLimitMicroUSD() int64 {
	proxy.SysConfigMutex.RLock()
	_, hasDeprecated := proxy.SysConfigCache[deprecatedBalanceConsumeDefaultLimitUSDKey]
	proxy.SysConfigMutex.RUnlock()
	if hasDeprecated {
		deprecatedBalanceConsumeLimitWarnOnce.Do(func() {
			log.Printf("[SYSCONFIG] WARN deprecated key %q ignored; use %q", deprecatedBalanceConsumeDefaultLimitUSDKey, balanceConsumeDefaultLimitMicroUSDKey)
		})
	}
	limit := readInt64Config(balanceConsumeDefaultLimitMicroUSDKey, 0)
	if limit < 0 {
		return 0
	}
	return limit
}

// applyReferralBonuses 处理推荐链路奖励发放。
// newUserID: 刚创建的新用户 ID
// refUsername: 推荐人 username（前端从 ?ref=xxx 透传上来），空字符串表示无推荐
// referrerBonus / refereeBonus: 当前生效的奖励金额
//
// 行为：
//   - refUsername 为空：什么都不做
//   - refUsername 不存在 或 = newUser 本人：什么都不做（防自荐）
//   - refUsername 存在且为普通用户：给推荐人 +referrerBonus，给新用户 +refereeBonus，写两条审计
//
// createUserWithSignupBonus 创建用户 + 写 signup_bonus 账单（原子单事务）。
//
// fix CRITICAL C19-2（codex 第十九轮）：之前 newUser.Create 与 signup_bonus 账单写入分两步，
// 后者用 NonFatal 路径仅日志失败 → "余额已给但账单丢失"路径仍存在。
// 改为单事务：要么 user 创建成功 + 账单成功；要么都失败回滚（不会有 user 但无账单的状态）。
func createUserWithSignupBonus(newUser *database.User, signupBonusMicroUSD int64, via string) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(newUser).Error; err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		if signupBonusMicroUSD > 0 {
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          newUser.ID,
				EntryType:       database.BillingTypeBonusCredit,
				AmountUSD:       signupBonusMicroUSD,
				BalanceAfterUSD: newUser.Quota, // newUser.Quota 已含 signup_bonus
				RelatedType:     "user",
				RelatedID:       newUser.ID,
				Description:     fmt.Sprintf("注册赠送 · %s", via),
			}); err != nil {
				return fmt.Errorf("write signup billing: %w", err)
			}
		}
		// 自动发新人券（如果 admin 在 SysConfig 配置了 signup_coupon_template_id 且模板有效）
		if err := autoGrantSignupCouponTx(tx, newUser.ID, via); err != nil {
			return fmt.Errorf("grant signup coupon: %w", err)
		}
		return nil
	})
}

// autoGrantSignupCouponTx 注册时自动发新人券。读 SysConfig.signup_coupon_template_id：
//   - 空 / 0 → 静默 noop（admin 没配置该功能）
//   - 模板不存在 / disabled / 非法 ID → log warn（admin 配错了，要让运维发现）
//   - 模板有效 → 创建一张 UserCoupon
//
// fix MAJOR R23+2-B5（codex 二轮）：原来直接读 proxy.SysConfigCache 没拿 RLock，
// 与 SyncCacheConfig 的 map 写并发会触发 race。改用 SysConfigMutex.RLock。
func autoGrantSignupCouponTx(tx *gorm.DB, userID uint, via string) error {
	proxy.SysConfigMutex.RLock()
	idStr := strings.TrimSpace(proxy.SysConfigCache["signup_coupon_template_id"])
	proxy.SysConfigMutex.RUnlock()

	if idStr == "" || idStr == "0" {
		return nil // admin 没配置 = 不发券，正常路径
	}
	tplID, err := strconv.Atoi(idStr)
	if err != nil || tplID <= 0 {
		log.Printf("[SIGNUP-COUPON] WARN invalid signup_coupon_template_id=%q: %v (admin 请检查 SysConfig)", idStr, err)
		return nil
	}
	var tpl database.CouponTemplate
	if err := tx.First(&tpl, tplID).Error; err != nil {
		log.Printf("[SIGNUP-COUPON] WARN template %d not found: %v (admin 请检查模板是否存在)", tplID, err)
		return nil
	}
	if !tpl.IsEnabled() {
		log.Printf("[SIGNUP-COUPON] WARN template %d is disabled, skip auto-grant (admin 请重新启用或清空 signup_coupon_template_id)", tplID)
		return nil
	}
	uc, err := buildCouponFromTemplate(userID, &tpl, 0, fmt.Sprintf("注册自动发放 · %s", via))
	if err != nil {
		// fix MAJOR R23+2-B3：rand 失败不应阻塞注册流程（用户体验 > 自动福利）
		log.Printf("[SIGNUP-COUPON] WARN build coupon failed user=%d template=%d: %v (skipping auto-grant)", userID, tplID, err)
		return nil
	}
	if err := tx.Create(&uc).Error; err != nil {
		return fmt.Errorf("create signup coupon: %w", err)
	}
	log.Printf("[SIGNUP-COUPON] granted user=%d template=%d code=%s via=%s", userID, tplID, uc.Code, via)
	return nil
}

// fix CRITICAL C4（codex+claude security 第十五轮）：原实现存在 3 个问题：
//  1. 两次 UpdateColumn 不在同一事务 → referrer 成功 / referee 失败导致单边奖励
//  2. 不写 BillingEntry → 奖励对账困难，违反账单事实表契约
//  3. referrer 已有 AuthCache 不刷新 → 余额展示陈旧
//
// 修复：单事务包住 referrer + referee 的 quota update + reward billing 账单写入；事务成功后刷 AuthCache。
//
// 单位：referrerBonusMicro / refereeBonusMicro 均为 micro_usd（int64）。
func applyReferralBonuses(c *fiber.Ctx, newUserID uint, newUsername, refUsername string, referrerBonusMicro, refereeBonusMicro int64) {
	refUsername = strings.TrimSpace(refUsername)
	if refUsername == "" || refUsername == newUsername {
		return
	}
	if referrerBonusMicro <= 0 && refereeBonusMicro <= 0 {
		return
	}
	var referrer database.User
	if err := database.DB.Where("username = ? AND role = ? AND status = 1", refUsername, "user").First(&referrer).Error; err != nil {
		return // 推荐人不存在或被封禁
	}
	if referrer.ID == newUserID {
		return // 自荐保护
	}

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if referrerBonusMicro > 0 {
			if err := tx.Model(&database.User{}).Where("id = ?", referrer.ID).
				UpdateColumn("quota", gorm.Expr("quota + ?", referrerBonusMicro)).Error; err != nil {
				return fmt.Errorf("update referrer quota: %w", err)
			}
			var fresh database.User
			if err := tx.Select("id, quota").First(&fresh, referrer.ID).Error; err != nil {
				return fmt.Errorf("fetch referrer fresh: %w", err)
			}
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          referrer.ID,
				EntryType:       database.BillingTypeBonusCredit,
				AmountUSD:       referrerBonusMicro,
				BalanceAfterUSD: fresh.Quota,
				RelatedType:     "user",
				RelatedID:       newUserID,
				Description:     fmt.Sprintf("推荐奖励：成功邀请用户 %s", newUsername),
			}); err != nil {
				return fmt.Errorf("write billing referrer: %w", err)
			}
		}
		if refereeBonusMicro > 0 {
			if err := tx.Model(&database.User{}).Where("id = ?", newUserID).
				UpdateColumn("quota", gorm.Expr("quota + ?", refereeBonusMicro)).Error; err != nil {
				return fmt.Errorf("update referee quota: %w", err)
			}
			var fresh database.User
			if err := tx.Select("id, quota").First(&fresh, newUserID).Error; err != nil {
				return fmt.Errorf("fetch referee fresh: %w", err)
			}
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          newUserID,
				EntryType:       database.BillingTypeBonusCredit,
				AmountUSD:       refereeBonusMicro,
				BalanceAfterUSD: fresh.Quota,
				RelatedType:     "user",
				RelatedID:       referrer.ID,
				Description:     fmt.Sprintf("被推荐注册奖励：来自 %s", refUsername),
			}); err != nil {
				return fmt.Errorf("write billing referee: %w", err)
			}
		}
		return nil
	})
	if txErr != nil {
		log.Printf("[REFERRAL] tx failed referrer=%d referee=%d: %v", referrer.ID, newUserID, txErr)
		return
	}

	// 审计日志（事务外，账单已落库；这两条 OperationLog 失败不影响账单）
	// fix Suggestion Phase 4-codex（第二十四轮）：amount 字段统一 numeric USD float
	// （与 BULK_QUOTA / QUOTA / REFUND_SUBSCRIPTION 一致），同时附 *_micro 用于精确审计
	if referrerBonusMicro > 0 {
		LogOperationBy(0, referrer.ID, "system", "REFERRAL_REWARD", c.IP(),
			fmt.Sprintf(`[{"type":"REFERRAL_REWARD","role":"referrer","amount":%g,"amount_micro":%d,"new_user":%q,"new_user_id":%d}]`,
				database.MicroToUSD(referrerBonusMicro), referrerBonusMicro, newUsername, newUserID))
	}
	if refereeBonusMicro > 0 {
		LogOperationBy(0, newUserID, "system", "REFERRAL_REWARD", c.IP(),
			fmt.Sprintf(`[{"type":"REFERRAL_REWARD","role":"referee","amount":%g,"amount_micro":%d,"referrer":%q,"referrer_id":%d}]`,
				database.MicroToUSD(refereeBonusMicro), refereeBonusMicro, refUsername, referrer.ID))
	}

	// fix C4：刷新 referrer + referee AuthCache。
	// 注意（codex 第十六轮）：之前以为 newUser 还未登录所以无需刷，但实际注册流程会立即调
	// AddUserToAuthCache(&newUser) 用结构体值缓存——此时 newUser.Quota 是 referee_bonus 写入前的值，
	// 缓存后 /user/me 和首次 API 鉴权都看到陈旧余额。RefreshUserAuth 会重读 DB 修正。
	if referrerBonusMicro > 0 {
		proxy.RefreshUserAuth(referrer.ID)
	}
	if refereeBonusMicro > 0 {
		proxy.RefreshUserAuth(newUserID)
	}
}

// SmsBindRequest 承接需要补充实名的短信验证码
type SmsBindRequest struct {
	TmpToken string `json:"tmp_token"` // Github 验身后给的超短期风控异常Token
	Phone    string `json:"phone"`
	SmsCode  string `json:"sms_code"`
}

// isPlatformUserCapReached 检查平台总用户数是否已达上限。
// max_users <= 0 表示无限制；仅统计 role="user" 的常规用户，不含管理员。
//
// 直接 Find 全表后内存遍历，避开 SQLite 下 `Where("key = ?", ...)` 因
// "key" 字段名解析失败导致 cap 形同虚设的边角问题。SysConfig 表行数极少
// （通常 < 30 行），全表扫描成本可以忽略。
func isPlatformUserCapReached(c *fiber.Ctx) bool {
	var configs []database.SysConfig
	if err := database.DB.Find(&configs).Error; err != nil {
		log.Printf("[CAP] failed to load sys_configs: %v", err)
		return false
	}

	var encryptedMax string
	for _, conf := range configs {
		if conf.Key == "max_users" {
			encryptedMax = conf.Value
			break
		}
	}
	if encryptedMax == "" {
		return false // 未配置 = 无限制
	}

	decrypted, err := utils.Decrypt(encryptedMax)
	if err != nil {
		log.Printf("[CAP] failed to decrypt max_users: %v", err)
		return false
	}

	max, err := strconv.ParseInt(strings.TrimSpace(decrypted), 10, 64)
	if err != nil || max <= 0 {
		return false
	}

	var count int64
	// fix MEDIUM（silent-failure 第十八轮）：Count 错误丢弃 → DB 故障时 count=0 < max →
	// 用户帽限被静默 bypass 允许无限注册。fail-closed：错误时返回 true（达上限），拒绝注册。
	if err := database.DB.Model(&database.User{}).Where("role = ?", "user").Count(&count).Error; err != nil {
		log.Printf("[CAP-CHECK] count query failed: %v — fail-closed (treating as cap reached)", err)
		return true
	}

	reached := count >= max
	// 无论 reached 与否都输出，方便排查"明明拦下了用户数还在涨"的诡异场景
	log.Printf("[CAP-CHECK] max=%d count=%d reached=%v ip=%s path=%s", max, count, reached, c.IP(), c.Path())
	return reached
}

// rejectIfUserCapReached 是注册入口的便捷拦截器，达上限直接写 403 响应。
//
// 返回 true 表示已经拦截（已写响应），调用方必须立即 return 退出。
//
// 注意：之前的版本返回 error 是个隐蔽 bug —— c.Status(403).JSON(...) 返回的是
// json marshal error（永远 nil），不是 fiber.Error。所以 `if resp := ...; resp != nil`
// 永远不成立，下面的 Create 还是被执行了，导致 cap 形同虚设。
func rejectIfUserCapReached(c *fiber.Ctx) bool {
	if isPlatformUserCapReached(c) {
		_ = c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message":      "平台已达到注册容量上限，暂不接受新用户。请联系管理员或稍后再试。",
			"message_code": "ERR_USER_CAP_REACHED",
		})
		return true
	}
	return false
}

// GithubCallback 核心注册网关：集成了智能风控引擎
func GithubCallback(c *fiber.Ctx) error {
	var payload GithubAuthRequest
	_ = c.BodyParser(&payload) // body 只承载可选 ref；code/state 必须来自 query
	code := strings.TrimSpace(c.Query("code"))
	state := strings.TrimSpace(c.Query("state"))
	if code == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "授权码 (OAuth Code) 验证失败或无效", "message_code": "ERR_INVALID_OAUTH_CODE"})
	}
	codeVerifier, ok := consumeOAuthState(state)
	if !ok {
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message":      "OAuth 状态校验失败，请重新发起登录",
			"message_code": oauthStateInvalidMessageCode(),
		})
	}

	// 1. 获取动态系统级配置 (Client ID / Secret / Strategy)
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["github_client_id"]
	clientSecret := proxy.SysConfigCache["github_client_secret"]
	regStrategy := proxy.SysConfigCache["reg_strategy"] // trust, strict, dynamic
	regIpLimitStr := proxy.SysConfigCache["reg_ip_limit"]
	proxy.SysConfigMutex.RUnlock()

	if clientID == "" || clientSecret == "" {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "暂时无法提供该授权模式，请使用其他方式登录", "message_code": "ERR_GITHUB_NOT_CONFIGURED"})
	}
	redirectURI, err := buildAbsoluteURL("/oauth/github")
	if err != nil {
		log.Printf("[OAUTH] invalid redirect_uri config: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_SERVER_ADDRESS_NOT_CONFIGURED"})
	}

	// 2. 用 Code 换取远程 Access Token
	reqBody := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
		"redirect_uri":  redirectURI,
		"code_verifier": codeVerifier,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		log.Printf("[OAUTH] marshal token req failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_GITHUB_INTERNAL"})
	}
	req, err := http.NewRequest("POST", githubTokenEndpoint, bytes.NewBuffer(bodyBytes))
	if err != nil {
		log.Printf("[OAUTH] build token req failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_GITHUB_INTERNAL"})
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := githubHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[OAUTH] token exchange failed: %v", err)
		return c.Status(502).JSON(fiber.Map{"success": false, "message": "第三方服务响应超时(502)", "message_code": "ERR_GITHUB_CONN"})
	}
	defer resp.Body.Close()

	var tokenRes map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&tokenRes); err != nil {
		log.Printf("[OAUTH] decode token resp failed (status=%d): %v", resp.StatusCode, err)
		return c.Status(502).JSON(fiber.Map{"success": false, "message_code": "ERR_GITHUB_DECODE"})
	}
	accessToken, ok := tokenRes["access_token"].(string)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "第三方颁发的客户端授权码已过期失效", "message_code": "ERR_GITHUB_CODE_EXPIRED"})
	}

	// 3. 获取用户极客身份
	req2, err := http.NewRequest("GET", githubUserEndpoint, nil)
	if err != nil {
		log.Printf("[OAUTH] build user req failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_GITHUB_INTERNAL"})
	}
	req2.Header.Set("Authorization", "Bearer "+accessToken)
	resp2, err := client.Do(req2)
	if err != nil {
		log.Printf("[OAUTH] fetch user failed: %v", err)
		return c.Status(502).JSON(fiber.Map{"success": false, "message": "无法同步上游服务器资料", "message_code": "ERR_GITHUB_PROFILE_FAIL"})
	}
	defer resp2.Body.Close()

	// 读取 Body 二进制以便解析
	userBody, err := io.ReadAll(resp2.Body)
	if err != nil {
		log.Printf("[OAUTH] read user body failed: %v", err)
		return c.Status(502).JSON(fiber.Map{"success": false, "message_code": "ERR_GITHUB_PROFILE_FAIL"})
	}
	var ghUser map[string]interface{}
	if err := json.Unmarshal(userBody, &ghUser); err != nil {
		log.Printf("[OAUTH] unmarshal user body failed (status=%d, body=%.200q): %v", resp2.StatusCode, string(userBody), err)
		return c.Status(502).JSON(fiber.Map{"success": false, "message_code": "ERR_GITHUB_PROFILE_DECODE"})
	}

	// float64 因为 json default 是 float64
	ghIDFloat, ok := ghUser["id"].(float64)
	if !ok {
		return c.Status(401).JSON(fiber.Map{"success": false, "message": "第三方接口同步异常", "message_code": "ERR_GITHUB_PROFILE_EXCEPTION"})
	}
	ghID := fmt.Sprintf("%.0f", ghIDFloat)
	ghName, _ := ghUser["login"].(string)

	// ====== 核心逻辑：查询该 Github ID 是否已经存在 ======
	var existingUser database.User
	res := database.DB.Where("github_id = ?", ghID).First(&existingUser)
	if res.RowsAffected > 0 {
		if existingUser.Status == 2 {
			return c.Status(403).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_BANNED",
				"ban_reason":   existingUser.BanReason,
			})
		}

		// 老用户回归！直接放行，无视风控
		LogOperationBy(existingUser.ID, existingUser.ID, "user", "LOGIN", c.IP(),
			fmt.Sprintf(`[{"type":"LOGIN","via":"github","username":%q,"github_id":%q}]`, existingUser.Username, ghID))
		sessionID, err := database.CreateUserSession(existingUser.ID, c.Get("User-Agent"), c.IP())
		if err != nil {
			log.Printf("[OAUTH] create session failed existing user=%d: %v", existingUser.ID, err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
		}
		return c.JSON(fiber.Map{
			"success": true,
			"msg":     "欢迎回归, " + ghName, "msg_code": "SUCCESS_WELCOME_BACK", "gh_name": ghName,
			"session_id": sessionID,
		})
	}

	// ====== 平台容量上限拦截 (新用户专用，老用户已在上方放行) ======
	if rejectIfUserCapReached(c) {
		return nil
	}

	// ====== 新注册边缘拦截风控系统 (Zero Trust Engine) ======
	// 获取探针 IP
	currentIP := c.IP()

	needSmsBind := false

	if regStrategy == "strict" {
		// 模式：宁可错杀，必须实名
		needSmsBind = true
	} else if regStrategy == "dynamic" {
		// 模式：沙盒智控
		limit, _ := strconv.ParseInt(regIpLimitStr, 10, 64)
		if limit <= 0 {
			limit = 3
		} // Default 3

		var ipRegCount int64
		// fix MEDIUM（silent-failure 第十八轮）：Count 错误丢弃 → DB 故障时 count=0 < limit →
		// IP 限频被 bypass，绕过 SMS 实名要求。fail-closed：错误时强制 needSmsBind=true。
		if err := database.DB.Model(&database.User{}).Where("reg_ip = ?", currentIP).Count(&ipRegCount).Error; err != nil {
			log.Printf("[REG-IP-CHECK] count query failed for ip=%s: %v — fail-closed (force SMS bind)", currentIP, err)
			needSmsBind = true
		} else if ipRegCount >= limit {
			// 该 IP 的羊毛党超配！勒令实名
			needSmsBind = true
		}
	} else {
		// 模式：trust (无论何种 IP，直接穿透，仅受制于全局黑名单，暂缓)
		needSmsBind = false
	}

	// 推荐人 username（前端从 ?ref=xxx 透传），编进 tmp_token，CompleteProfile/Risk 完成注册时使用
	refUser := strings.TrimSpace(payload.Ref)
	if refUser == "" {
		refUser = strings.TrimSpace(c.Query("ref"))
	}
	// 防止 ref 包含 "|" 破坏 tmp_token 切分
	refUser = strings.ReplaceAll(refUser, "|", "")

	if needSmsBind {
		// 中断注册管道。将身份信息下发临时内存验证环中 (这里使用短效加密token)
		// payload 结构：sms|ghID|ghName|ref|timestamp
		tmpAuthPayload := fmt.Sprintf("sms|%s|%s|%s|%d", ghID, ghName, refUser, time.Now().Unix())
		safeTmpToken, _ := utils.Encrypt(tmpAuthPayload)

		return c.JSON(fiber.Map{
			"success":      false,
			"action":       "require_sms_bind",
			"tmp_token":    safeTmpToken,
			"message":      "安全校验未完成：受新账号安全策略影响，请先验证手机号码以完成注册核验。",
			"message_code": "ERR_REQUIRE_SMS_BIND",
		})
	}

	// ====== 绿灯安全用户，抛出设定昵称拦截 ======
	// payload 结构：clean|ghID|ghName|ref|timestamp
	tmpAuthPayload := fmt.Sprintf("clean|%s|%s|%s|%d", ghID, ghName, refUser, time.Now().Unix())
	safeTmpToken, _ := utils.Encrypt(tmpAuthPayload)
	return c.JSON(fiber.Map{
		"success":      false,
		"action":       "require_profile_setup",
		"tmp_token":    safeTmpToken,
		"default_name": suggestUsernameFromOAuthName(ghName),
		"message":      "联合登录完成，请指定本平台内用户名用作唯一标识",
		"message_code": "ERR_REQUIRE_PROFILE_SETUP",
	})
}

// GetPublicConfig 暴露不受查验的安全级别配置给前台。
// fix CRITICAL Sprint4-M3：exchange_rate 改为 int64 micros 字段名，杜绝 float 协议。
func GetPublicConfig(c *fiber.Ctx) error {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["github_client_id"]
	serverAddress := proxy.SysConfigCache["server_address"]
	rateStr := proxy.SysConfigCache["exchange_rate_rmb_per_usd_micros"]
	proxy.SysConfigMutex.RUnlock()

	return c.JSON(fiber.Map{
		"success":                          true,
		"github_client_id":                 clientID,
		"server_address":                   serverAddress,
		"exchange_rate_rmb_per_usd_micros": rateStr,
	})
}

// CompleteRisk 处理高危 IP 被拦截后的短信补充实名叫号流程
func CompleteRisk(c *fiber.Ctx) error {
	var req SmsBindRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "请求报文解析失败", "message_code": "ERR_PARSE_REQUEST"})
	}

	// 真实校验：阿里云 SMS 已通过 SendSMS endpoint 发码，verifySMSCode 一次性消费
	if !verifySMSCode(req.Phone, req.SmsCode) {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "短信验证码错误或已过期", "message_code": "ERR_SMS_CODE_INVALID"})
	}

	// 1. 拆解临时票据 + 校验类型 + 校验过期 + 拆 ref（C-4/M-5 修复）
	tokenType, refUser, decryptedStr, err := parseTmpToken(req.TmpToken)
	if err != nil {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_RISK_TICKET_INVALID"})
	}
	if tokenType != "sms" {
		// 防止 clean| 类型 token 被提交到 sms 路径绕过短信验证
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "票据类型错误", "message_code": "ERR_RISK_TICKET_TYPE"})
	}

	// fix CRITICAL（codex 第四轮）：从 tmp_token 解出 GitHub ID 写到 newUser，
	// 防止"同一 GitHub 账号用不同手机号反复注册领取奖励"。
	// tmp_token payload 格式：(clean|sms)|ghID|ghName|ref|timestamp
	tmpParts := strings.Split(decryptedStr, "|")
	ghID := ""
	if len(tmpParts) >= 5 {
		ghID = strings.TrimSpace(tmpParts[1])
	}

	registerMu.Lock()
	defer registerMu.Unlock()

	var dbUser database.User
	if res := database.DB.Where("phone = ?", req.Phone).First(&dbUser); res.RowsAffected > 0 {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "系统判定：该手机号已绑定其它账户", "message_code": "ERR_PHONE_BOUND"})
	}
	// fix CRITICAL：同一 GitHub 账号已绑定其它账户也要拒绝，否则同 ghID 可在 SMS 路径反复开户
	if ghID != "" {
		var dbGh database.User
		if res := database.DB.Where("github_id = ?", ghID).First(&dbGh); res.RowsAffected > 0 {
			return c.Status(403).JSON(fiber.Map{"success": false, "message": "该 GitHub 账号已绑定其它账户", "message_code": "ERR_GITHUB_ALREADY_REGISTERED"})
		}
	}

	if rejectIfUserCapReached(c) {
		return nil
	}

	// 注册奖励配置（单位 micro_usd）
	signupBonusMicro, referrerBonusMicro, refereeBonusMicro := resolveBonusConfig()
	newSk := utils.GenerateRandomToken("sk-daof")
	newUsername := "User_" + req.Phone[len(req.Phone)-4:]
	newUser := database.User{
		Username:     newUsername,
		Phone:        req.Phone,
		GithubID:     ghID, // 修复 CRITICAL：必须把 tmp_token 里的 ghID 写入，关闭重复领奖通道
		Role:         "user",
		Token:        newSk,
		Quota:        signupBonusMicro, // 由 SysConfig.signup_bonus 控制（micro_usd），0 表示不送
		Status:       1,
		RegIP:        c.IP(),
		RegRiskScore: 0,

		// 三段消费模型：从 SysConfig 默认值初始化（admin 可全局调整）
		BalanceConsumeEnabled:       readBoolConfig("balance_consume_default_enabled", false),
		BalanceConsumeLimitUSD:      readDefaultBalanceConsumeLimitMicroUSD(),
		BalanceConsumeWindowSeconds: int(readInt64Config("balance_consume_default_window_secs", 2592000)),
	}

	// fix CRITICAL C19-2（codex 第十九轮）：user 创建 + signup_bonus 账单原子化
	if err := createUserWithSignupBonus(&newUser, signupBonusMicro, "sms"); err != nil {
		log.Printf("[REGISTER-SMS] tx failed username=%s: %v", newUser.Username, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "创建根记录失败", "message_code": "ERR_CREATE_ROOT_RECORD"})
	}

	// fix HIGH NEW-H2（codex 第十八轮）：AddUserToAuthCache 必须在 applyReferralBonuses **之前**调用。
	// 否则 applyReferralBonuses 内部 RefreshUserAuth(newUser.ID) 会把缓存更新到 referee_bonus 后的值，
	// 但接下来 AddUserToAuthCache(&newUser) 用 Go 内存的 newUser（quota = signup_bonus，不含 referee_bonus）
	// 又把缓存覆盖回旧值。先建 cache 再 applyReferralBonuses，让其内部 RefreshUserAuth 修正缓存。
	proxy.AddUserToAuthCache(&newUser)

	// 应用拉新链路奖励（如果 ref 有效；内部会 RefreshUserAuth 修正 cache）
	applyReferralBonuses(c, newUser.ID, newUsername, refUser, referrerBonusMicro, refereeBonusMicro)

	var afterCount int64
	database.DB.Model(&database.User{}).Where("role = ?", "user").Count(&afterCount)
	log.Printf("[USER-CREATED] via=CompleteRisk id=%d username=%s phone=%s ip=%s new_user_count=%d ref=%q signup_bonus=%s",
		newUser.ID, newUser.Username, maskPhone(newUser.Phone), c.IP(), afterCount, refUser, database.FormatMicroUSD(signupBonusMicro))

	LogOperationBy(0, newUser.ID, "system", "REGISTER", c.IP(),
		fmt.Sprintf(`[{"type":"REGISTER","via":"sms","username":%q,"phone":%q,"ref":%q,"signup_bonus":%g,"signup_bonus_micro":%d}]`,
			newUser.Username, newUser.Phone, refUser, database.MicroToUSD(signupBonusMicro), signupBonusMicro))

	sessionID, err := database.CreateUserSession(newUser.ID, c.Get("User-Agent"), c.IP())
	if err != nil {
		log.Printf("[REGISTER-SMS] create session failed user=%d: %v", newUser.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
	}

	return c.JSON(fiber.Map{
		"success":    true,
		"msg":        "实名核验完成，沙盒限制已解除",
		"msg_code":   "SUCCESS_SANDBOX_CLEARED",
		"session_id": sessionID,
	})
}

// ProfileSetupRequest 承接纯新用户的定名
type ProfileSetupRequest struct {
	TmpToken string `json:"tmp_token"`
	Username string `json:"username"`
}

var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\p{Han}]{2,20}$`)

func suggestUsernameFromOAuthName(name string) string {
	name = strings.TrimSpace(name)
	out := make([]rune, 0, 20)
	lastUnderscore := false
	for _, r := range name {
		if len(out) >= 20 {
			break
		}
		allowed := r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			unicode.Is(unicode.Han, r)
		if allowed {
			out = append(out, r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore && len(out) > 0 {
			out = append(out, '_')
			lastUnderscore = true
		}
	}
	suggested := strings.Trim(string(out), "_")
	if suggested == "" {
		suggested = "user"
	}
	if len([]rune(suggested)) < 2 {
		suggested += "_user"
	}
	runes := []rune(suggested)
	if len(runes) > 20 {
		suggested = string(runes[:20])
	}
	return suggested
}

// CompleteProfile 处理不需要短信但需要取名的新用户注册
func CompleteProfile(c *fiber.Ctx) error {
	var req ProfileSetupRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "请求报文解析失败", "message_code": "ERR_PARSE_REQUEST"})
	}

	req.Username = strings.TrimSpace(req.Username)
	if !usernameRegex.MatchString(req.Username) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "昵称格式非法！仅允许2-20位中英文、数字或下划线", "message_code": "ERR_NICKNAME_FORMAT"})
	}

	tokenType, refUser, decryptedStr, err := parseTmpToken(req.TmpToken)
	if err != nil {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_RISK_TICKET_INVALID"})
	}
	if tokenType != "clean" {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "票据类型错误", "message_code": "ERR_INVALID_PASS_STATE"})
	}

	parts := strings.Split(decryptedStr, "|")
	if len(parts) < 4 {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "无效的干净通行证状态", "message_code": "ERR_INVALID_PASS_STATE"})
	}
	ghID := parts[1]

	registerMu.Lock()
	defer registerMu.Unlock()

	var dbUser database.User
	if res := database.DB.Where("github_id = ?", ghID).First(&dbUser); res.RowsAffected > 0 {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "系统防刷判定：此 Github 账号已经注册过", "message_code": "ERR_GITHUB_ALREADY_REGISTERED"})
	}

	if rejectIfUserCapReached(c) {
		return nil
	}

	// 注册奖励配置（单位 micro_usd）
	signupBonusMicro, referrerBonusMicro, refereeBonusMicro := resolveBonusConfig()

	newSk := utils.GenerateRandomToken("sk-daof")
	newUser := database.User{
		GithubID:     ghID,
		Username:     req.Username,
		Role:         "user",
		Token:        newSk,
		Quota:        signupBonusMicro, // 由 SysConfig.signup_bonus 控制（micro_usd）
		Status:       1,
		RegIP:        c.IP(),
		RegRiskScore: 0,

		// 三段消费模型：从 SysConfig 默认值初始化（避免 GitHub 注册路径漏初始化导致 WindowSeconds=0）
		BalanceConsumeEnabled:       readBoolConfig("balance_consume_default_enabled", false),
		BalanceConsumeLimitUSD:      readDefaultBalanceConsumeLimitMicroUSD(),
		BalanceConsumeWindowSeconds: int(readInt64Config("balance_consume_default_window_secs", 2592000)),
	}

	// fix CRITICAL C19-2（codex 第十九轮）：user 创建 + signup_bonus 账单原子化
	if err := createUserWithSignupBonus(&newUser, signupBonusMicro, "github"); err != nil {
		log.Printf("[REGISTER-GITHUB] tx failed username=%s: %v", newUser.Username, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "创建通行记录失败", "message_code": "ERR_CREATE_PASS_RECORD"})
	}

	// fix HIGH NEW-H2：AddUserToAuthCache 在 applyReferralBonuses 前调用（见 CompleteRisk 同样修复）
	proxy.AddUserToAuthCache(&newUser)

	// 应用拉新链路奖励（内部会 RefreshUserAuth 修正 cache）
	applyReferralBonuses(c, newUser.ID, newUser.Username, refUser, referrerBonusMicro, refereeBonusMicro)

	var afterCount int64
	database.DB.Model(&database.User{}).Where("role = ?", "user").Count(&afterCount)
	log.Printf("[USER-CREATED] via=CompleteProfile id=%d username=%s ghID=%s ip=%s new_user_count=%d ref=%q signup_bonus=%s",
		newUser.ID, newUser.Username, newUser.GithubID, c.IP(), afterCount, refUser, database.FormatMicroUSD(signupBonusMicro))

	LogOperationBy(0, newUser.ID, "system", "REGISTER", c.IP(),
		fmt.Sprintf(`[{"type":"REGISTER","via":"github","username":%q,"github_id":%q,"ref":%q,"signup_bonus":%g,"signup_bonus_micro":%d}]`,
			newUser.Username, newUser.GithubID, refUser, database.MicroToUSD(signupBonusMicro), signupBonusMicro))

	sessionID, err := database.CreateUserSession(newUser.ID, c.Get("User-Agent"), c.IP())
	if err != nil {
		log.Printf("[REGISTER-GITHUB] create session failed user=%d: %v", newUser.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
	}

	return c.JSON(fiber.Map{
		"success":    true,
		"msg":        "名字烙印完成！",
		"msg_code":   "SUCCESS_NAME_FORGED",
		"session_id": sessionID,
	})
}
