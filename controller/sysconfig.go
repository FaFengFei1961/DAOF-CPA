package controller

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"daof-ai-hub/database"
	"daof-ai-hub/middleware"
	"daof-ai-hub/proxy"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// ConfigItem defines the structure for config payloads
type ConfigItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// MaskSecret partial hides a string. 统一格式：前 2 + ******** + 后 4。
// channel.go 等场景的脱敏统一走这里，避免不同模块出现长度不一致。
//
// fix Minor（codex 第七轮）：按 rune 切，避免 UTF-8 多字节字符在边界处被截成 �
// 进而导致 looksLikeMaskedSecret 不识别 + admin 把 mask 误回写覆盖真实密钥。
func MaskSecret(s string) string {
	r := []rune(s)
	if len(r) <= 6 {
		return "******"
	}
	return string(r[:2]) + "********" + string(r[len(r)-4:])
}

// looksLikeMaskedSecret 判断一个值是否疑似 MaskSecret 输出（"前 2 + ******** + 后 4" 或纯 "******"）。
// 用来阻止前端把 GET 时拿到的 masked 字符串回写到真实密钥位。
//
// fix Minor（自审第六轮）：原 `Contains(v, "****")` 太宽，会误伤含 `****` 的合法配置
// （如 webhook URL pattern、模板字符串）→ 静默丢弃合法更新。
// fix Minor（codex 第七轮）：用 []rune 长度精确匹配 14 个 rune（前 2 + 8 星 + 后 4），
// 不再依赖正则的 byte-mode `.{N}` 在 UTF-8 多字节字符上行为
func looksLikeMaskedSecret(v string) bool {
	if v == "******" {
		return true
	}
	r := []rune(v)
	if len(r) != 14 {
		return false
	}
	for i := 2; i < 10; i++ {
		if r[i] != '*' {
			return false
		}
	}
	return true
}

// 敏感密钥精确匹配清单 + 后缀模式
var sensitiveExactKeys = map[string]bool{
	"github_client_secret": true,
	"aliyun_access_secret": true,
	"cliproxy_key":         true,
}

var sensitiveSuffixes = []string{"_secret", "_password", "_token", "_apikey", "_api_key", "_private_key"}

// isSensitiveConfigKey 判断 key 是否需要脱敏（精确匹配 OR 后缀匹配）
// 改用 HasSuffix 替代 Contains，避免 "monkey" / "key_rotation_counter" 误判
func isSensitiveConfigKey(key string) bool {
	if sensitiveExactKeys[key] {
		return true
	}
	for _, suffix := range sensitiveSuffixes {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	// 特殊后缀 _key 单独处理：避免 _id_key 等被错过
	if strings.HasSuffix(key, "_key") {
		return true
	}
	return false
}

var clearableEmptyConfigKeys = map[string]bool{
	// The UI reset button intentionally writes an empty value so the runtime can
	// generate a fresh HMAC secret.
	"moderation_cache_secret": true,
}

func isClearableEmptyConfigKey(key string) bool {
	return clearableEmptyConfigKeys[key]
}

const (
	balanceConsumeDefaultMinWindowSeconds = 60
	balanceConsumeDefaultMaxWindowSeconds = 365 * 24 * 60 * 60
	moderationAutobanMinWindowSeconds     = 60
	moderationAutobanMaxWindowSeconds     = 365 * 24 * 60 * 60
)

func validateSysConfigPayload(payload map[string]string) (string, string, bool) {
	if raw, ok := payload["signup_coupon_template_id"]; ok {
		v := strings.TrimSpace(raw)
		if v != "" && v != "0" {
			id, err := strconv.ParseUint(v, 10, 32)
			if err != nil || id == 0 {
				return "ERR_INVALID_TEMPLATE", "signup_coupon_template_id 必须是有效的优惠券模板 ID，或填 0 关闭", false
			}
			var tpl database.CouponTemplate
			if err := database.DB.First(&tpl, uint(id)).Error; err != nil {
				return "ERR_TEMPLATE_NOT_FOUND", "新人券模板不存在，请刷新后重试", false
			}
			if !tpl.IsEnabled() {
				return "ERR_TEMPLATE_DISABLED", "新人券模板已禁用，不能设为注册自动发券", false
			}
		}
	}

	if raw, ok := payload["balance_consume_default_enabled"]; ok {
		if !isBoolSysConfigValue(strings.TrimSpace(raw)) {
			return "ERR_INVALID_PARAMS", "balance_consume_default_enabled 必须是 true/false", false
		}
	}
	if raw, ok := payload["balance_consume_default_limit_usd"]; ok {
		v := strings.TrimSpace(raw)
		limit, err := strconv.ParseFloat(v, 64)
		if v == "" || err != nil || limit < 0 {
			return "ERR_LIMIT_INVALID", "balance_consume_default_limit_usd 必须是非负 USD 数值", false
		}
		if _, ok := database.USDToMicro(limit); !ok {
			return "ERR_LIMIT_INVALID", "balance_consume_default_limit_usd 超出允许范围", false
		}
	}
	if raw, ok := payload["balance_consume_default_window_secs"]; ok {
		window, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || window < balanceConsumeDefaultMinWindowSeconds || window > balanceConsumeDefaultMaxWindowSeconds {
			return "ERR_WINDOW_INVALID", "balance_consume_default_window_secs 必须在 60 秒到 365 天之间", false
		}
	}

	if raw, ok := payload["moderation_autoban_enabled"]; ok {
		if !isBoolSysConfigValue(strings.TrimSpace(raw)) {
			return "ERR_INVALID_PARAMS", "moderation_autoban_enabled 必须是 true/false", false
		}
	}
	for _, key := range []string{
		"moderation_autoban_keyword_threshold",
		"moderation_autoban_policy_threshold",
		"moderation_autoban_risk_rule_threshold",
		"moderation_autoban_risk_score_threshold",
		"moderation_autoban_image_threshold",
		"moderation_autoban_oversize_threshold",
	} {
		if raw, ok := payload[key]; ok {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || n < 0 || n > 100 {
				return "ERR_INVALID_PARAMS", key + " 必须是 0-100 之间的整数，0 表示关闭该类自动封禁", false
			}
		}
	}
	if raw, ok := payload["moderation_autoban_window_seconds"]; ok {
		window, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || window < moderationAutobanMinWindowSeconds || window > moderationAutobanMaxWindowSeconds {
			return "ERR_WINDOW_INVALID", "moderation_autoban_window_seconds 必须在 60 秒到 365 天之间", false
		}
	}
	for _, spec := range []struct {
		key string
		min int
		max int
	}{
		{"moderation_api_timeout_seconds", 1, 120},
		{"proxy_nonstream_upstream_timeout_seconds", 30, 3600},
		{"moderation_max_chars", 1024, 8 * 1024 * 1024},
		{"moderation_chunk_chars", 1024, 256 * 1024},
		{"moderation_max_chunks", 1, 128},
		{"moderation_long_context_min_tokens", 0, 5_000_000},
		{"moderation_long_context_max_chars", 0, 16 * 1024 * 1024},
		{"moderation_long_context_max_chunks", 1, 128},
	} {
		if raw, ok := payload[spec.key]; ok {
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || n < spec.min || n > spec.max {
				return "ERR_INVALID_PARAMS", fmt.Sprintf("%s 必须是 %d-%d 之间的整数", spec.key, spec.min, spec.max), false
			}
		}
	}
	if raw, ok := payload["moderation_keyword_ai_max_candidates"]; ok {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 1 || n > 200 {
			return "ERR_INVALID_PARAMS", "moderation_keyword_ai_max_candidates 必须是 1-200 之间的整数", false
		}
	}
	if raw, ok := payload["moderation_provider"]; ok {
		switch normalizeModerationProviderForConfig(raw) {
		case "cliproxy_model":
		default:
			return "ERR_INVALID_PARAMS", "moderation_provider 必须是 cliproxy_model", false
		}
	}
	if raw, ok := payload["moderation_cliproxy_model"]; ok {
		model := strings.TrimSpace(raw)
		if model != "" && len([]rune(model)) > 128 {
			return "ERR_INVALID_PARAMS", "moderation_cliproxy_model 过长", false
		}
	}
	if raw, ok := payload["moderation_risk_rules"]; ok {
		if _, err := proxy.ParseModerationRiskRules(raw); err != nil {
			return "ERR_INVALID_PARAMS", "moderation_risk_rules JSON 或规则格式不合法: " + err.Error(), false
		}
	}
	if raw, ok := payload["upstream_account_cost_presets_json"]; ok {
		if _, err := parseUpstreamAccountCostPresets(raw); err != nil {
			return "ERR_INVALID_JSON", "upstream_account_cost_presets_json JSON 或规则格式不合法: " + err.Error(), false
		}
	}

	return "", "", true
}

func normalizeModerationProviderForConfig(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	v = strings.ReplaceAll(v, "-", "_")
	switch v {
	case "", "cliproxy_model", "cliproxy", "cpa_model", "model", "llm", "cpa":
		return "cliproxy_model"
	default:
		return v
	}
}

func isBoolSysConfigValue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false", "1", "0", "yes", "no", "on", "off":
		return true
	default:
		return false
	}
}

// GetSysConfigs 获取系统配置。敏感密钥默认脱敏。
//
// fix Major（自审第六轮）：?reveal=1 解除脱敏前必须二次鉴权——
// 仅靠 admin cookie 不够。要求请求头 `X-Admin-Password` 携带当前 admin 密码，
// 服务端用 bcrypt 校验通过才允许返回明文。
//
// 这阻止：
//   - admin cookie 被 CSRF / XSS 偷走后单次 GET ?reveal=1 → 全量密钥泄露
//   - 共享 admin 终端时旁观者快速一击拷贝密钥
func GetSysConfigs(c *fiber.Ctx) error {
	var configs []database.SysConfig
	if err := database.DB.Find(&configs).Error; err != nil {
		log.Printf("[SYSCONFIG] list failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	reveal := c.Query("reveal") == "1"
	if reveal {
		// 二次鉴权：必须带 X-Admin-Password header，服务端 bcrypt 校验
		token := middleware.ExtractAdminToken(c)
		var operator database.User
		if err := database.DB.Where("token = ? AND role = ? AND status = ?", token, "admin", 1).First(&operator).Error; err != nil {
			return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_REVEAL_REAUTH_REQUIRED"})
		}
		pw := strings.TrimSpace(c.Get("X-Admin-Password"))
		if pw == "" {
			return c.Status(401).JSON(fiber.Map{
				"success":      false,
				"message":      "查看明文密钥需要二次鉴权，请通过 X-Admin-Password 头提供当前 admin 密码",
				"message_code": "ERR_REVEAL_REAUTH_REQUIRED",
			})
		}
		if !utils.CheckHash(pw, operator.PasswordHash) {
			// 失败也写审计 + 防暴力枚举
			LogOperationBy(operator.ID, operator.ID, "admin", "REVEAL_SECRETS_FAIL", c.IP(),
				`[{"type":"REVEAL_SECRETS_FAIL"}]`)
			return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_REVEAL_REAUTH_INVALID"})
		}
		// 成功也写审计：admin 拷贝密钥是高敏操作
		LogOperationBy(operator.ID, operator.ID, "admin", "REVEAL_SECRETS_OK", c.IP(),
			`[{"type":"REVEAL_SECRETS_OK"}]`)
	}

	res := make(map[string]string)
	failed := []string{}
	for _, conf := range configs {
		val, err := utils.Decrypt(conf.Value)
		if err != nil {
			log.Printf("[SYSCONFIG] decrypt key=%s failed: %v", conf.Key, err)
			failed = append(failed, conf.Key)
			continue
		}
		if !reveal && isSensitiveConfigKey(conf.Key) && val != "" {
			// 统一走 MaskSecret 保持脱敏格式一致（前 2 + 后 4 字符）
			res[conf.Key] = MaskSecret(val)
		} else {
			res[conf.Key] = val
		}
	}

	return c.JSON(fiber.Map{
		"success":          true,
		"data":             res,
		"decrypt_failed":   failed,
		"sensitive_masked": !reveal,
	})
}

// BatchUpdateSysConfigs 接收前端批量发来的真实配置，事务化加密入库。
// 任何 encrypt 或 DB 失败都会回滚整批，避免部分写入。
//
// query 参数 ?allow_empty=1 时空字符串写入数据库（"清空"语义），用于单面板独立提交（如 AdminPaymentChannels）；
// 默认空值跳过（"未修改"语义），保护 Settings 全量 POST 时未填字段。
func BatchUpdateSysConfigs(c *fiber.Ctx) error {
	var payload map[string]string
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "请求参数解析失败", "message_code": "ERR_PARSE_PAYLOAD"})
	}
	allowEmpty := c.Query("allow_empty") == "1"
	if code, msg, ok := validateSysConfigPayload(payload); !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": msg, "message_code": code})
	}

	// fix M-3：保存时校验高风险 key 的格式（先于事务执行，避免部分写入失败半事务）
	// 当前校验支付网关 URL（SSRF 严格策略）+ CPA management URL（SSRF 通用策略，允许本地）。
	if rawGw, ok := payload["yifut_gateway"]; ok && rawGw != "" {
		if err := proxy.ValidateGateway(rawGw); err != nil {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      fmt.Sprintf("yifut_gateway 不合法: %v", err),
				"message_code": "ERR_INVALID_GATEWAY",
			})
		}
	}
	// fix Major（codex 第五轮）：cliproxy_url 落库前必须 SSRF 校验，
	// 否则配置入侵后 CLIProxy 反向代理会成为内网穿透通道，并把 cliproxy_key 当 Bearer 泄露。
	if rawCp, ok := payload["cliproxy_url"]; ok && rawCp != "" {
		if err := proxy.ValidateChannelURL(rawCp); err != nil {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message":      fmt.Sprintf("cliproxy_url 不合法: %v", err),
				"message_code": "ERR_CLIPROXY_URL_UNSAFE",
			})
		}
	}
	failedKeys := []string{}
	skippedMasked := []string{}
	updated := 0
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		for k, v := range payload {
			if v == "" && !allowEmpty && !isClearableEmptyConfigKey(k) {
				continue // 默认：空值视为未修改
			}
			// fix Major（codex 第五轮）：前端从 masked GET 拿到 "ab********cdef" 类掩码值后，
			// 用户只改了无关字段就提交全表 → masked 字符串会原样写回真实密钥位，破坏支付/SMS/OAuth。
			// 检测明显的 mask 模式（含 "********" 中段）→ 跳过该 key 保留旧值。
			if isSensitiveConfigKey(k) && looksLikeMaskedSecret(v) {
				skippedMasked = append(skippedMasked, k)
				continue
			}
			encryptedVal, err := utils.Encrypt(v)
			if err != nil {
				log.Printf("[SYSCONFIG] encrypt key=%s failed: %v", k, err)
				failedKeys = append(failedKeys, k)
				return fmt.Errorf("encrypt %s failed", k)
			}
			var config database.SysConfig
			res := tx.Where("key = ?", k).First(&config)
			if res.RowsAffected > 0 {
				config.Value = encryptedVal
				if err := tx.Save(&config).Error; err != nil {
					return fmt.Errorf("save %s: %w", k, err)
				}
			} else {
				if err := tx.Create(&database.SysConfig{Key: k, Value: encryptedVal}).Error; err != nil {
					return fmt.Errorf("create %s: %w", k, err)
				}
			}
			updated++
		}
		return nil
	})
	if err != nil {
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CONFIG_BATCH_FAILED",
			"failed_keys":  failedKeys,
		})
	}

	proxy.SyncCacheConfig()

	// fix MAJOR R23-M2（codex 审查）：moderation 配置变更后必须 reload 关键字过滤器
	// 和清空 moderation policy / 内容缓存，否则要重启进程才生效。
	if _, ok := payload["moderation_keywords"]; ok {
		proxy.InvalidateKeywordFilterCache()
	}
	if _, ok := payload["moderation_risk_rules"]; ok {
		proxy.InvalidateRiskRuleCache()
	}
	// 任何 moderation_* 配置变更（key/secret/threshold/endpoint/keywords/...）都让
	// 内容缓存失效（HMAC policy_version 已含部分字段，但 secret / max_chars 等需要主动清）
	for k := range payload {
		if strings.HasPrefix(k, "moderation_") {
			proxy.FlushModerationContentCache()
			break
		}
	}
	// fix Minor Mi22-2（codex 第二十二轮）：admin 改 notif_default_* / notif_pref_cache_ttl_seconds
	// 后必须 flush PrefCache，否则已缓存的用户视图按旧默认值计算到 TTL 过期才生效。
	for k := range payload {
		if strings.HasPrefix(k, "notif_default_") || k == "notif_pref_cache_ttl_seconds" {
			proxy.FlushPrefCache()
			break
		}
	}

	return c.JSON(fiber.Map{
		"success":        true,
		"message":        "配置重载成功",
		"message_code":   "SUCCESS_CONFIG_SAVED",
		"updated_count":  updated,
		"skipped_masked": skippedMasked, // 前端可据此提示哪些字段未修改（避免误以为已保存）
	})
}
