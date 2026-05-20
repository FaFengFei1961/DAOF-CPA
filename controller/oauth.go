package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"daof-cpa/database"
	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// registerMu 保护"cap 检查 + 创建用户"为临界区，避免两个并发新注册都通过 cap 检查
// 之后导致 user 总数超过 max_users。SQLite 的串行写只能部分缓解，不能确定性消除。
var (
	registerMu sync.Mutex
)

const oauthStateTTL = 5 * time.Minute

type oauthStateRecord struct {
	CodeVerifier string
	ExpiresAt    time.Time
}

var (
	oauthStateStore       sync.Map // key: state, value: oauthStateRecord
	oauthStateJanitorOnce sync.Once
	// fix C-M2 (2026-05-19)：sync.Map 无内置容量限制，攻击者轮换 IP 可写入数万条
	// 让 cleanupExpiredOAuthStates 的 Range 退化为 O(N) 阻塞。加原子计数器 + 上限
	// 拒绝新 state 注入。10K 远超合理峰值（同时段 10000 个并发 GitHub OAuth 流），
	// 触顶说明遭受滥用，直接 503 给客户端 + log 告警。
	oauthStateCount    int64
	oauthStateMaxItems int64 = 10000

	githubTokenEndpoint = "https://github.com/login/oauth/access_token"
	githubUserEndpoint  = "https://api.github.com/user"
	// fix B-H1 (2026-05-19)：加 SafeTransport + RedirectGuard 防 DNS rebinding /
	// open redirect 把 OAuth 流量重定向到内网；下游 io.ReadAll 也需要 LimitReader
	// 防 OOM（见 readGithubResponseBody 助手）。
	githubHTTPClient = &http.Client{
		Timeout:       10 * time.Second,
		Transport:     proxy.SafeTransport(),
		CheckRedirect: proxy.RedirectGuard,
	}
)

// githubResponseLimit 限制 GitHub OAuth/User 响应体大小。
// GitHub 公开 token 响应 ~200 字节，user profile JSON 5~10KB，64KB 充足。
const githubResponseLimit int64 = 64 * 1024

// tmp_token TTL：超过此时长视为过期
const tmpTokenTTL = 15 * time.Minute
const tmpTokenConsumeTTL = tmpTokenTTL

var (
	tmpTokenConsumedStore sync.Map // key: jti (hash of tmp_token), value: consumedAtNano
	tmpTokenJanitorOnce   sync.Once
)

func tmpTokenJTI(tmpToken string) string {
	sum := sha256.Sum256([]byte(tmpToken))
	return hex.EncodeToString(sum[:])
}

func markTmpTokenConsumed(tmpToken string) bool {
	startTmpTokenJanitor()
	jti := tmpTokenJTI(tmpToken)
	_, loaded := tmpTokenConsumedStore.LoadOrStore(jti, time.Now().UnixNano())
	return !loaded
}

func isTmpTokenConsumed(tmpToken string) bool {
	_, loaded := tmpTokenConsumedStore.Load(tmpTokenJTI(tmpToken))
	return loaded
}

func startTmpTokenJanitor() {
	tmpTokenJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				cutoff := time.Now().Add(-tmpTokenConsumeTTL).UnixNano()
				tmpTokenConsumedStore.Range(func(key, value any) bool {
					if v, ok := value.(int64); ok && v < cutoff {
						tmpTokenConsumedStore.Delete(key)
					}
					return true
				})
			}
		}()
	})
}

// parseTmpToken 解析并校验 OAuth 流程中的 tmp_token。
// payload 形如（Phase H-3 多 provider 格式）：
//
//	(clean|sms) | provider | externalID | suggestedUsername | ref | timestamp
//
// 返回 (tokenType, refUser, originalDecryptedStr, error)
// originalDecryptedStr 让 caller 自己 split 出 provider / externalID / suggestedUsername。
func parseTmpToken(tmpToken string) (string, string, string, error) {
	decrypted, err := utils.Decrypt(tmpToken)
	if err != nil || decrypted == "" {
		return "", "", "", fmt.Errorf("无效或被篡改的风控票据")
	}
	parts := strings.Split(decrypted, "|")
	// H-3：新格式 6 段。旧格式（5 段）一律拒（项目尚未上线 + 任何漏掉的旧 token TTL 15min 已过）
	if len(parts) < 6 {
		return "", "", "", fmt.Errorf("票据格式损坏")
	}
	tokenType := parts[0]
	if tokenType != "clean" && tokenType != "sms" {
		return "", "", "", fmt.Errorf("票据类型未知")
	}
	// parts[1]=provider parts[2]=externalID parts[3]=username parts[4]=ref parts[5]=ts
	tsStr := parts[5]
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
	refUser := parts[4]
	return tokenType, refUser, decrypted, nil
}

// parseOAuthTmpTokenParts 从 parseTmpToken 返回的 decryptedStr 中提取 (provider, externalID, username)。
// 不重复校验段数（parseTmpToken 已确认 >= 6）。
func parseOAuthTmpTokenParts(decryptedStr string) (provider, externalID, suggestedUsername string) {
	parts := strings.Split(decryptedStr, "|")
	if len(parts) < 6 {
		return "", "", ""
	}
	return parts[1], parts[2], parts[3]
}

// buildOAuthTmpTokenPayload 拼装 tmp_token 明文（caller 再 utils.Encrypt）。
// 严禁 provider/externalID/username/ref 含 "|" —— caller 责任：先用 sanitizeTmpTokenField 过滤。
func buildOAuthTmpTokenPayload(tokenType, provider, externalID, username, refUser string) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%d",
		tokenType,
		sanitizeTmpTokenField(provider),
		sanitizeTmpTokenField(externalID),
		sanitizeTmpTokenField(username),
		sanitizeTmpTokenField(refUser),
		time.Now().Unix(),
	)
}

// sanitizeTmpTokenField 过滤掉 "|" 防止破坏 tmp_token 切分。
func sanitizeTmpTokenField(s string) string { return strings.ReplaceAll(s, "|", "") }

// PrepareOAuthState 给前端发起 OAuth 之前调用。服务端生成一次性 state 和 PKCE verifier，
// 只把 state + code_challenge 下发给前端，verifier 留在服务端 5 分钟内存表。
//
// H-3：路由 /api/auth/oauth/:provider/prepare —— c.Params("provider") 可选校验。
// 旧 /api/auth/github/prepare 已删，此 handler 现在 provider-agnostic（state/verifier 与
// provider 无关，下发的 challenge 复用于任意 provider）。
func PrepareOAuthState(c *fiber.Ctx) error {
	// fix C-M2：触顶就 503，让 cleanupExpiredOAuthStates 有窗口跑完一轮 GC
	if atomic.LoadInt64(&oauthStateCount) >= oauthStateMaxItems {
		log.Printf("[OAUTH-STATE-OVERFLOW] state count >= %d, refusing new", oauthStateMaxItems)
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_OVERLOAD", "message": "OAuth 服务暂时过载，请稍后重试"})
	}
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
	if _, loaded := oauthStateStore.LoadOrStore(state, oauthStateRecord{
		CodeVerifier: verifier,
		ExpiresAt:    time.Now().Add(oauthStateTTL),
	}); !loaded {
		atomic.AddInt64(&oauthStateCount, 1)
	}
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
	atomic.AddInt64(&oauthStateCount, -1)
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
			if _, loaded := oauthStateStore.LoadAndDelete(key); loaded {
				atomic.AddInt64(&oauthStateCount, -1)
			}
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

// GithubAuthRequest 承接前台发来的可选推荐人标识。
// code / state 必须从 query 读取，不接受 body 字段。
type GithubAuthRequest struct {
	Ref string `json:"ref"` // 推荐人 username，可选；若有效则发拉新奖励
}

// resolveBonusConfig 从 SysConfig 读取新用户奖励三参数。
// 三个 key 都使用 micro_usd 整数字符串；未配置时给默认值
// （signup=$1.0, referrer=0, referee=0）。返回 micro_usd（int64）。
func resolveBonusConfig() (signupMicro, referrerMicro, refereeMicro int64) {
	return readMicroUSDConfig("signup_bonus", database.MicroPerUSD),
		readMicroUSDConfig("referrer_bonus", 0),
		readMicroUSDConfig("referee_bonus", 0)
}

func readReferralPaidSpendRewardConfig() (int64, int64) {
	bps := database.NormalizeReferralRewardBPS(readInt64Config(database.ReferralPaidSpendRewardBPSConfigKey, 0))
	windowSeconds := database.NormalizeReferralRewardWindowSeconds(
		readInt64Config(database.ReferralPaidSpendRewardWindowSecondsConfigKey, database.DefaultReferralPaidSpendRewardWindowSeconds),
	)
	return bps, windowSeconds
}

// fix MEDIUM（codex money-unit）：统一所有金额 SysConfig 读路径走 readMicroUSDConfig，
// 与 signup_bonus/referrer_bonus/referee_bonus 一致。readMicroUSDConfig 内部包含非负 +
// 有限数校验，旧 readInt64Config 没有"micro_usd 语义"标注容易让维护者误以为是普通整数。
func readDefaultBalanceConsumeLimitMicroUSD() int64 {
	return readMicroUSDConfig(balanceConsumeDefaultLimitMicroUSDKey, 0)
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
	log.Printf("[SIGNUP-COUPON] granted user=%d template=%d code=%s via=%s", userID, tplID, maskCouponCode(uc.Code), via)
	return nil
}

func maskCouponCode(code string) string {
	if len(code) <= 4 {
		return "****"
	}
	return code[:len(code)-4] + "****"
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
	var referrer database.User
	if err := database.DB.Where("username = ? AND role = ? AND status = 1", refUsername, "user").First(&referrer).Error; err != nil {
		return // 推荐人不存在或被封禁
	}
	if referrer.ID == newUserID {
		return // 自荐保护
	}

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		referredAt := time.Now()
		if err := tx.Model(&database.User{}).
			Where("id = ? AND referred_by_user_id = 0", newUserID).
			Updates(map[string]any{
				"referred_by_user_id": referrer.ID,
				"referred_at":         referredAt,
			}).Error; err != nil {
			return fmt.Errorf("persist referral relation: %w", err)
		}
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
	proxy.RefreshUserAuth(newUserID)
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
// GithubCallback 是 OAuthCallback 的 GitHub-specific 别名，给 oauth_test.go 的
// 现有测试复用（旧测试直接 app.Post("/callback", GithubCallback) 注册，没有 :provider
// URL 段）。新代码 / 生产路由请用 OAuthCallback + /api/auth/oauth/:provider/callback。
func GithubCallback(c *fiber.Ctx) error {
	c.Locals(oauthProviderOverrideKey, database.OAuthProviderGitHub)
	return OAuthCallback(c)
}

// oauthProviderOverrideKey 是 c.Locals 的 key，让 GithubCallback 等 alias handler 在
// 不修改路由参数的情况下传递 provider key 给 OAuthCallback。仅 controller package 内部用。
const oauthProviderOverrideKey = "oauth_provider_override"

// OAuthCallback 是多 provider OAuth 回调统一入口。
// 路由：POST /api/auth/oauth/:provider/callback?code=...&state=...
//
// :provider 由路由捕获，必须是注册过的 OAuthProvider Key（github / google / ...）。
// 流程：
//  1. 校验 :provider 已注册
//  2. 校验 state + 取 code_verifier
//  3. provider.Exchange(ctx, code, verifier) → OAuthIdentityData
//  4. 在 oauth_identities 表查 (provider, external_id) 是否已绑定到 user
//  5. 找到 → 直接发 session（包括 banned 用户的 appeal session）
//  6. 没找到 → 风控决定走 SMS 或直进 profile setup；body 内 ref 写进 tmp_token
//
// **行为兼容**：保留原 GithubCallback 的所有响应字段 / message_code（不区分 provider 的部分）
// 以减少前端改动面。
func OAuthCallback(c *fiber.Ctx) error {
	providerKey := strings.ToLower(strings.TrimSpace(c.Params("provider")))
	if providerKey == "" {
		// 允许通过 Locals 注入（GithubCallback alias / test 用）
		if v, ok := c.Locals(oauthProviderOverrideKey).(string); ok {
			providerKey = v
		}
	}
	if providerKey == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_PROVIDER_UNKNOWN"})
	}
	provider, ok := GetOAuthProvider(providerKey)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_PROVIDER_UNKNOWN"})
	}

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

	// 取风控配置（与 provider 无关）
	proxy.SysConfigMutex.RLock()
	regStrategy := proxy.SysConfigCache["reg_strategy"] // trust, strict, dynamic
	regIpLimitStr := proxy.SysConfigCache["reg_ip_limit"]
	proxy.SysConfigMutex.RUnlock()

	identity, err := provider.Exchange(c.UserContext(), code, codeVerifier)
	if err != nil {
		// GitHub 路径保留旧 ERR_GITHUB_* 错误码不变；其它 provider 用 generic 映射
		if providerKey == database.OAuthProviderGitHub {
			return mapOAuthProviderErrorGitHub(c, err)
		}
		return mapOAuthProviderErrorGeneric(c, providerKey, err)
	}

	extID := identity.ExternalID
	displayName := identity.Username

	// 查 oauth_identities：identity 已绑过哪个 DAOF user？
	existingUser, found, lookupErr := lookupActiveUserByOAuthIdentity(providerKey, extID)
	if lookupErr != nil {
		log.Printf("[OAUTH] identity lookup failed provider=%s ext_id=%s: %v", providerKey, extID, lookupErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if found {
		// 老用户回归
		if existingUser.Status == 2 {
			// Banned 用户保留 session 供 appeal 流程使用
			sessionID, err := database.CreateUserSession(existingUser.ID, c.Get("User-Agent"), c.IP())
			if err != nil {
				log.Printf("[OAUTH] create appeal session failed banned user=%d: %v", existingUser.ID, err)
				return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
			}
			LogOperationBy(existingUser.ID, existingUser.ID, "user", "LOGIN_BANNED_APPEAL", c.IP(),
				fmt.Sprintf(`[{"type":"LOGIN_BANNED_APPEAL","via":%q,"username":%q,"external_id":%q}]`,
					providerKey, existingUser.Username, extID))
			return c.JSON(fiber.Map{
				"success":        true,
				"message_code":   "SUCCESS_APPEAL_SESSION",
				"account_status": 2,
				"ban_reason":     existingUser.BanReason,
				"session_id":     sessionID,
			})
		}
		LogOperationBy(existingUser.ID, existingUser.ID, "user", "LOGIN", c.IP(),
			fmt.Sprintf(`[{"type":"LOGIN","via":%q,"username":%q,"external_id":%q}]`,
				providerKey, existingUser.Username, extID))
		sessionID, err := database.CreateUserSession(existingUser.ID, c.Get("User-Agent"), c.IP())
		if err != nil {
			log.Printf("[OAUTH] create session failed existing user=%d: %v", existingUser.ID, err)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
		}
		return c.JSON(fiber.Map{
			"success":      true,
			"msg":          "欢迎回归, " + displayName,
			"msg_code":     "SUCCESS_WELCOME_BACK",
			"gh_name":      displayName, // 字段名保持 gh_name 兼容现有前端；H-3b 改 display_name
			"session_id":   sessionID,
		})
	}

	// ====== 新用户分支 ======
	if rejectIfUserCapReached(c) {
		return nil
	}

	currentIP := c.IP()
	needSmsBind := false

	switch regStrategy {
	case "strict":
		needSmsBind = true
	case "dynamic":
		limit, _ := strconv.ParseInt(regIpLimitStr, 10, 64)
		if limit <= 0 {
			limit = 3
		}
		var ipRegCount int64
		if err := database.DB.Model(&database.User{}).Where("reg_ip = ?", currentIP).Count(&ipRegCount).Error; err != nil {
			log.Printf("[REG-IP-CHECK] count query failed for ip=%s: %v — fail-closed (force SMS bind)", currentIP, err)
			needSmsBind = true
		} else if ipRegCount >= limit {
			needSmsBind = true
		}
	default:
		// trust 模式
		needSmsBind = false
	}

	refUser := strings.TrimSpace(payload.Ref)
	if refUser == "" {
		refUser = strings.TrimSpace(c.Query("ref"))
	}

	tokenType := "clean"
	action := "require_profile_setup"
	messageCode := "ERR_REQUIRE_PROFILE_SETUP"
	message := "联合登录完成，请指定本平台内用户名用作唯一标识"
	if needSmsBind {
		tokenType = "sms"
		action = "require_sms_bind"
		messageCode = "ERR_REQUIRE_SMS_BIND"
		message = "安全校验未完成：受新账号安全策略影响，请先验证手机号码以完成注册核验。"
	}
	safeTmpToken, _ := utils.Encrypt(buildOAuthTmpTokenPayload(tokenType, providerKey, extID, displayName, refUser))
	resp := fiber.Map{
		"success":      false,
		"action":       action,
		"tmp_token":    safeTmpToken,
		"message":      message,
		"message_code": messageCode,
	}
	if !needSmsBind {
		resp["default_name"] = suggestUsernameFromOAuthName(displayName)
	}
	return c.JSON(resp)
}

// mapOAuthProviderErrorGeneric 给非 GitHub provider 用的通用错误响应。
// 暂用 ERR_OAUTH_* 通用前缀；H-4 接入 Google 时若需要 provider-specific 文案再分。
func mapOAuthProviderErrorGeneric(c *fiber.Ctx, providerKey string, err error) error {
	switch {
	case errors.Is(err, ErrOAuthProviderNotConfigured):
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_OAUTH_PROVIDER_NOT_CONFIGURED",
			"provider":     providerKey,
		})
	case errors.Is(err, ErrOAuthCodeExpired):
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_CODE_EXPIRED", "provider": providerKey})
	case errors.Is(err, ErrOAuthUpstreamUnavailable):
		return c.Status(502).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_UPSTREAM_UNAVAILABLE", "provider": providerKey})
	case errors.Is(err, ErrOAuthUpstreamMalformed):
		return c.Status(502).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_UPSTREAM_MALFORMED", "provider": providerKey})
	default:
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_INTERNAL", "provider": providerKey})
	}
}

// GetPublicConfig 暴露不受查验的安全级别配置给前台。
// fix CRITICAL Sprint4-M3：exchange_rate 改为 int64 micros 字段名，杜绝 float 协议。
func GetPublicConfig(c *fiber.Ctx) error {
	proxy.SysConfigMutex.RLock()
	clientID := proxy.SysConfigCache["github_client_id"]
	serverAddress := proxy.SysConfigCache["server_address"]
	rateStr := proxy.SysConfigCache["exchange_rate_rmb_per_usd_micros"]
	proxy.SysConfigMutex.RUnlock()
	signupBonus, referrerBonus, refereeBonus := resolveBonusConfig()
	paidSpendRewardBPS, paidSpendRewardWindowSeconds := readReferralPaidSpendRewardConfig()

	return c.JSON(fiber.Map{
		"success":                          true,
		"github_client_id":                 clientID,
		"server_address":                   serverAddress,
		"exchange_rate_rmb_per_usd_micros": rateStr,
		"referral_incentives": fiber.Map{
			"signup_bonus_micro_usd":   fmt.Sprintf("%d", signupBonus),
			"referrer_bonus_micro_usd": fmt.Sprintf("%d", referrerBonus),
			"referee_bonus_micro_usd":  fmt.Sprintf("%d", refereeBonus),
			"paid_spend_reward_bps":    fmt.Sprintf("%d", paidSpendRewardBPS),
			"reward_window_seconds":    fmt.Sprintf("%d", paidSpendRewardWindowSeconds),
		},
	})
}

// CompleteRisk 处理高危 IP 被拦截后的短信补充实名叫号流程
func CompleteRisk(c *fiber.Ctx) error {
	var req SmsBindRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "请求报文解析失败", "message_code": "ERR_PARSE_REQUEST"})
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
	if isTmpTokenConsumed(req.TmpToken) {
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_TMP_TOKEN_ALREADY_USED",
		})
	}
	// 真实校验：阿里云 SMS 已通过 SendSMS endpoint 发码，verifySMSCode 一次性消费。
	// 必须先验 tmp_token，避免攻击者用无效 tmp_token 消耗目标手机号验证码次数。
	if !verifySMSCode(req.Phone, req.SmsCode) {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "短信验证码错误或已过期", "message_code": "ERR_SMS_CODE_INVALID"})
	}
	if !markTmpTokenConsumed(req.TmpToken) {
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_TMP_TOKEN_ALREADY_USED",
		})
	}

	// H-3：tmp_token payload 格式：(clean|sms) | provider | externalID | username | ref | timestamp
	providerKey, externalID, displayName := parseOAuthTmpTokenParts(decryptedStr)
	providerKey = strings.TrimSpace(providerKey)
	externalID = strings.TrimSpace(externalID)
	if providerKey == "" || externalID == "" {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "票据缺少 provider 身份", "message_code": "ERR_RISK_TICKET_INVALID"})
	}

	registerMu.Lock()
	defer registerMu.Unlock()

	var dbUser database.User
	if res := database.DB.Where("phone = ?", req.Phone).First(&dbUser); res.RowsAffected > 0 {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "系统判定：该手机号已绑定其它账户", "message_code": "ERR_PHONE_BOUND"})
	}
	// 同一外部账号已绑定其它 DAOF user 也要拒（同 externalID 在 SMS 路径反复开户）
	if _, found, lookupErr := lookupActiveUserByOAuthIdentity(providerKey, externalID); lookupErr == nil && found {
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message":      "该第三方账号已绑定其它账户",
			"message_code": "ERR_OAUTH_ALREADY_REGISTERED",
			"provider":     providerKey,
		})
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
	// 过渡期：GitHub provider 同时写 User.GithubID，让 admin UI 的"按 github_id 搜索 / 显示"
	// 继续工作（H-3b/H-5 会移除 User.GithubID）。其它 provider 不写。
	if providerKey == database.OAuthProviderGitHub {
		newUser.GithubID = externalID
	}

	// fix CRITICAL C19-2（codex 第十九轮）：user 创建 + signup_bonus 账单原子化
	if err := createUserWithSignupBonus(&newUser, signupBonusMicro, "sms"); err != nil {
		log.Printf("[REGISTER-SMS] tx failed username=%s: %v", newUser.Username, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "创建根记录失败", "message_code": "ERR_CREATE_ROOT_RECORD"})
	}
	// 写 oauth_identities 行（new user 已经有 ID 了）
	if err := linkOAuthIdentityTx(database.DB, newUser.ID, OAuthIdentityData{
		Provider: providerKey, ExternalID: externalID, Username: displayName,
	}); err != nil {
		log.Printf("[REGISTER-SMS] link oauth_identity failed user=%d provider=%s ext=%s: %v",
			newUser.ID, providerKey, externalID, err)
		// user 已建立，但 oauth_identity 没写进去 → 下次该 provider 登录会被当作新用户
		// 风险可接受（运维事后可补）；不阻塞响应。
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
	if !markTmpTokenConsumed(req.TmpToken) {
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_TMP_TOKEN_ALREADY_USED",
		})
	}
	if tokenType != "clean" {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "票据类型错误", "message_code": "ERR_INVALID_PASS_STATE"})
	}

	// H-3：新 tmp_token 格式拆 (provider, externalID, suggestedUsername)
	providerKey, externalID, _ := parseOAuthTmpTokenParts(decryptedStr)
	providerKey = strings.TrimSpace(providerKey)
	externalID = strings.TrimSpace(externalID)
	if providerKey == "" || externalID == "" {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "无效的干净通行证状态", "message_code": "ERR_INVALID_PASS_STATE"})
	}

	registerMu.Lock()
	defer registerMu.Unlock()

	// 拒重复绑：同一外部账号已绑过其它 DAOF user
	if _, found, lookupErr := lookupActiveUserByOAuthIdentity(providerKey, externalID); lookupErr == nil && found {
		return c.Status(403).JSON(fiber.Map{
			"success":      false,
			"message":      "系统防刷判定：此第三方账号已经注册过",
			"message_code": "ERR_OAUTH_ALREADY_REGISTERED",
			"provider":     providerKey,
		})
	}

	if rejectIfUserCapReached(c) {
		return nil
	}

	// 注册奖励配置（单位 micro_usd）
	signupBonusMicro, referrerBonusMicro, refereeBonusMicro := resolveBonusConfig()

	newSk := utils.GenerateRandomToken("sk-daof")
	newUser := database.User{
		Username:     req.Username,
		Role:         "user",
		Token:        newSk,
		Quota:        signupBonusMicro, // 由 SysConfig.signup_bonus 控制（micro_usd）
		Status:       1,
		RegIP:        c.IP(),
		RegRiskScore: 0,

		// 三段消费模型：从 SysConfig 默认值初始化
		BalanceConsumeEnabled:       readBoolConfig("balance_consume_default_enabled", false),
		BalanceConsumeLimitUSD:      readDefaultBalanceConsumeLimitMicroUSD(),
		BalanceConsumeWindowSeconds: int(readInt64Config("balance_consume_default_window_secs", 2592000)),
	}
	// 过渡：GitHub provider 同时双写 User.GithubID（admin UI 兼容）。其它 provider 不写。
	if providerKey == database.OAuthProviderGitHub {
		newUser.GithubID = externalID
	}

	// fix CRITICAL C19-2（codex 第十九轮）：user 创建 + signup_bonus 账单原子化
	if err := createUserWithSignupBonus(&newUser, signupBonusMicro, providerKey); err != nil {
		log.Printf("[REGISTER-OAUTH] tx failed provider=%s username=%s: %v", providerKey, newUser.Username, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "创建通行记录失败", "message_code": "ERR_CREATE_PASS_RECORD"})
	}
	// 写 oauth_identities 行
	if err := linkOAuthIdentityTx(database.DB, newUser.ID, OAuthIdentityData{
		Provider: providerKey, ExternalID: externalID, Username: req.Username,
	}); err != nil {
		log.Printf("[REGISTER-OAUTH] link oauth_identity failed user=%d provider=%s ext=%s: %v",
			newUser.ID, providerKey, externalID, err)
	}

	// fix HIGH NEW-H2：AddUserToAuthCache 在 applyReferralBonuses 前调用（见 CompleteRisk 同样修复）
	proxy.AddUserToAuthCache(&newUser)

	// 应用拉新链路奖励（内部会 RefreshUserAuth 修正 cache）
	applyReferralBonuses(c, newUser.ID, newUser.Username, refUser, referrerBonusMicro, refereeBonusMicro)

	var afterCount int64
	database.DB.Model(&database.User{}).Where("role = ?", "user").Count(&afterCount)
	log.Printf("[USER-CREATED] via=CompleteProfile id=%d username=%s provider=%s ext_id=%s ip=%s new_user_count=%d ref=%q signup_bonus=%s",
		newUser.ID, newUser.Username, providerKey, externalID, c.IP(), afterCount, refUser, database.FormatMicroUSD(signupBonusMicro))

	LogOperationBy(0, newUser.ID, "system", "REGISTER", c.IP(),
		fmt.Sprintf(`[{"type":"REGISTER","via":%q,"username":%q,"external_id":%q,"ref":%q,"signup_bonus":%g,"signup_bonus_micro":%d}]`,
			providerKey, newUser.Username, externalID, refUser, database.MicroToUSD(signupBonusMicro), signupBonusMicro))

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
