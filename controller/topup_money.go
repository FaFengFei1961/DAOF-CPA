// Package controller / topup_money.go
//
// 充值流程的纯函数 helper：金额单位换算（RMB fen ↔ USD micro）、SysConfig 读取、
// 字符串清理、退款比例分摊。
//
// 这些 helper 不持有状态、不与 fiber.Ctx 耦合，因此被 oauth.go / topup_test 等多处复用。
//
// 从 topup.go 抽出（Phase D-5，2026-05-19）：只是物理拆分，无语义改动。
package controller

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"daof-cpa/proxy"
)

// generateOutTradeNo 生成全局唯一商户订单号。
// 格式 "tp{userID}{unixmilli}{rand16}"，最大 ~38 字节（仍 <64 即数据库列约束）。
// 16 hex 字符 = 8 字节随机 = 2^64 熵，单毫秒同 user 撞概率几乎为零。
func generateOutTradeNo(userID uint) (string, error) {
	r := make([]byte, 8)
	if _, err := rand.Read(r); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	return fmt.Sprintf("tp%d%d%s", userID, time.Now().UnixMilli(), hex.EncodeToString(r)), nil
}

// 注（第十七轮）：原 generateOutRefundNo 用于 V2 退款 API 的商户退款单号生成，
// 现在平台改为手动退款工作流（admin 在易付通后台退款），不再调用 V2 退款 API。
// admin 通过 ExternalRefundRef 字段填入易付通后台的退款单号做对账锚点，
// 该函数已无调用方，删除。

// sanitizeExternalRef 清理 admin 输入的退款单号：剥离控制字符（\n \r \t 等），
// 截断到 64 字符。
//
// fix LOW（security 第十八轮）：原实现仅 TrimSpace + len 截断，未防控制字符注入。
// admin 在易付通后台复制粘贴时可能误带换行符，落入 BillingEntry.Description 后会被
// 外部日志解析工具误读为多行结构，破坏对账。
//
// fix LOW（codex 第十九轮）：原 cleaned[:64] 是 byte 截断 → 多字节 rune（中文/emoji）会被
// 截在 UTF-8 中段产生无效字节序列，最终持久化的 description 在 JSON 序列化时变为 �。
// 改为 rune-based 截断保证 cut 永远在合法边界，并且"长度上限"以语义字符数计而非字节数。
func sanitizeExternalRef(s string) string {
	if s == "" {
		return ""
	}
	// 仅保留可打印 ASCII + 常见 Unicode 字母数字（易付通退款单号实际只有 ASCII 字母数字 + 短横）
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f { // 控制字符 + DEL
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(cleaned) > 64 {
		// 取前 64 个 rune，逐 rune 累计字节长度后切片 → 永远在 rune 边界
		runes := []rune(cleaned)
		cleaned = string(runes[:64])
	}
	return cleaned
}

func readStringConfig(key, def string) string {
	proxy.SysConfigMutex.RLock()
	v := strings.TrimSpace(proxy.SysConfigCache[key])
	proxy.SysConfigMutex.RUnlock()
	if v == "" {
		return def
	}
	return v
}

// readBoolConfig 把 SysConfig 字符串转 bool（"true"/"1" → true，其他 → false）。
func readBoolConfig(key string, def bool) bool {
	v := readStringConfig(key, "")
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

// readInt64Config 读取存为整数字符串的 SysConfig 项。
// fix CRITICAL Sprint4-M3：fen / micros 等定点金额单位入口，杜绝 float 解析。
func readInt64Config(key string, def int64) int64 {
	v := readStringConfig(key, "")
	if v == "" {
		return def
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return i
}

// readMicroUSDConfig 读取存为 micro_usd 整数字符串的 SysConfig 项。
// fix P0-δ A-10：金额配置入口整数化，拒绝 USD float string。
func readMicroUSDConfig(key string, defMicroUSD int64) int64 {
	v := readStringConfig(key, "")
	if v == "" {
		return defMicroUSD
	}
	micro, err := strconv.ParseInt(v, 10, 64)
	if err != nil || micro < 0 {
		return defMicroUSD
	}
	return micro
}

// safeExchangeRateRmbPerUsdMicros 永远返回 >0 的汇率（RMB per USD × 1e6）。
// fix CRITICAL Sprint4-M3：从 float64 改为 int64 定点。
// SysConfig 配 "0" / 负数 / 缺失都回退默认 7_200_000（= 7.2 RMB/USD）。
func safeExchangeRateRmbPerUsdMicros() int64 {
	rate := readInt64Config("exchange_rate_rmb_per_usd_micros", 7_200_000)
	if rate <= 0 {
		return 7_200_000
	}
	return rate
}

// usdMicroFromFenAndRate 把 RMB fen + 汇率（RMB/USD × 1e6）换算成 USD micro_usd。
//
// 公式：usd_micro = fen × 1e10 / rate_rmb_per_usd_micros
// 推导：
//
//	amount_rmb       = fen / 100                  (yuan)
//	amount_rmb_micros = fen × 1e4                  (RMB × 1e6 微元)
//	rate             = rate_micros / 1e6            (RMB/USD)
//	amount_usd_micros = amount_rmb_micros / rate    = fen × 1e4 × 1e6 / rate_micros = fen × 1e10 / rate_micros
//
// floor 截断（保守入账：用户存 ¥72.5 / 7.2 ¥ rate → $10.069444 USD，floor 不多送）。
// fix CRITICAL Sprint4-M3：用 big.Int 全整数运算，杜绝 float64 IEEE 754 噪声。
func usdMicroFromFenAndRate(fen int64, rateRmbPerUsdMicros int64) (int64, bool) {
	if fen <= 0 || rateRmbPerUsdMicros <= 0 {
		return 0, false
	}
	// fen × 1e10 可达 9.2e18 > int64 上限（fen ≤ 9.2e8 即 ¥9.2M），用 big.Int 避免溢出
	product := new(big.Int).Mul(big.NewInt(fen), big.NewInt(10_000_000_000))
	result := new(big.Int).Quo(product, big.NewInt(rateRmbPerUsdMicros))
	if !result.IsInt64() || result.Sign() <= 0 {
		return 0, false
	}
	return result.Int64(), true
}

func csvContains(csv, val string) bool {
	for _, s := range strings.Split(csv, ",") {
		if strings.TrimSpace(s) == val {
			return true
		}
	}
	return false
}

// isSafeReturnPath 校验 SysConfig.yifut_return_path 仅为站内绝对路径，
// 拒绝外部 URL/协议相对 URL/控制字符，防 open redirect。
func isSafeReturnPath(p string) bool {
	s := strings.TrimSpace(p)
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "\r\n\t") {
		return false
	}
	if strings.HasPrefix(s, "//") {
		return false
	}
	if !strings.HasPrefix(s, "/") {
		return false
	}
	// 含 scheme（http://...）的字符串"恰好以 /"开头几乎不可能，但兜底用 url.Parse 拒绝
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return false
	}
	return true
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// parseRMBStringToFen 把 "12.34" / "12" / "12.3" 这类 RMB 元字符串解析为 fen 整数。
//
// 设计原因（fix CRITICAL 多模型审计第二十五轮）：
//   - 易付通回调金额是字符串，原实现 float 解析 + approxEqual(0.001) 容差有精度漏洞
//   - 直接整数化后与 order.MoneyRMB（fen int64）做严格 == 比较彻底消除浮点误差
//
// 规则：
//   - 至多 2 位小数；超过返回 false
//   - 拒绝负数 / 空 / 非数字字符 / 多个小数点 / 尾随小数点（"12."）
//   - "12" → 1200; "12.3" → 1230; "12.34" → 1234; "12.345" → false; "12." → false; ".5" → false
func parseRMBStringToFen(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") {
		return 0, false
	}
	// 分离整数 / 小数部分
	intPart := s
	fracPart := ""
	if idx := strings.Index(s, "."); idx >= 0 {
		intPart = s[:idx]
		fracPart = s[idx+1:]
		if strings.Contains(fracPart, ".") {
			return 0, false
		}
		// fix MINOR（多模型审计第二十五轮 P2）：拒绝尾随小数点（"12."）
		// 严格金额格式：要么没小数点，要么小数点后必须有 1-2 位数字
		if fracPart == "" {
			return 0, false
		}
	}
	if intPart == "" {
		return 0, false
	}
	// 整数部分必须全数字
	for _, ch := range intPart {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	// 小数部分至多 2 位且全数字
	if len(fracPart) > 2 {
		return 0, false
	}
	for _, ch := range fracPart {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	// 补齐到 2 位（"3" → "30"）
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	combined := intPart + fracPart
	v, err := strconv.ParseInt(combined, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// proratedTopupRefundMicro 返回"累计退款到 refundedFen 时"应累计扣回的 micro_usd。
//
// 充值订单的 USD 入账金额已经在 TopupOrder.AmountUSD 中锁定。退款时必须围绕这个
// 金额做比例分摊，不能每笔重新按汇率换算并四舍五入，否则多次部分退款会和原始入账
// 金额对不上。调用方用 newTarget - oldTarget 得到本次扣回额，最终全额退款时天然等于
// order.AmountUSD。
func proratedTopupRefundMicro(amountMicro, moneyFen, refundedFen int64) (int64, bool) {
	if amountMicro <= 0 || moneyFen <= 0 || refundedFen < 0 || refundedFen > moneyFen {
		return 0, false
	}
	if refundedFen == 0 {
		return 0, true
	}
	if refundedFen == moneyFen {
		return amountMicro, true
	}

	numerator := new(big.Int).Mul(big.NewInt(amountMicro), big.NewInt(refundedFen))
	// positive half-up rounding: floor((2*numerator + denominator) / (2*denominator))
	numerator.Mul(numerator, big.NewInt(2))
	denominator := big.NewInt(moneyFen)
	numerator.Add(numerator, denominator)
	denominator.Mul(denominator, big.NewInt(2))
	quotient := new(big.Int).Quo(numerator, denominator)
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}
