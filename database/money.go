// Package database / money.go
//
// 财务金额单位转换 helper。
//
// fix MAJOR M22-A1（codex 第二十-二十三轮三轮重申）：项目所有金额字段从 float64 USD
// 切换为 int64 micro-USD（USD * 1e6），杜绝浮点累加误差与 NaN/Inf 防线散落。
//
// 单位约定：
//   - USD：1 USD = 1_000_000 micro_usd（精度到 0.000001 美元）
//   - RMB：1 RMB = 100 fen（人民币最小单位"分"，与现实业务一致）
//
// 设计权衡：
//   - 选 micro_usd（10^6）而非 cent（10^2）：保留 LLM token 计费需要的高精度
//     （单 token 成本 ~$0.000001，cent 单位精度不够）
//   - 内部所有运算用 int64：加减乘除全为整数算术，无累加误差
//   - DB schema 统一 int64：禁止再有 float64 金额列
//   - API 契约：JSON 仍序列化为字符串（如 "12.345600"）让前端能用 BigInt/Decimal.js 无损显示
package database

import (
	"fmt"
	"math"
)

// MicroPerUSD micro_usd 换算常量。
const MicroPerUSD = int64(1_000_000)

// FenPerRMB 分换算常量。
const FenPerRMB = int64(100)

// USDToMicro 将 USD float 转为 micro_usd int64（四舍五入）。
//
// 适用场景：
//   - 前端传入的 USD 数值（如 admin 改额度）入业务逻辑前归一化
//
// NaN / Inf 输入返回 0 + ok=false，调用方必须处理。
//
// fix MAJOR Phase 4-codex（第二十四轮）：边界检查必须用 `>=`/`<=` 而非 `>`/`<`。
// 因为 float64 表示 int64.MaxInt64 时会向上舍入到 9223372036854775808.0（>MaxInt64），
// `micro > math.MaxInt64` 永远 false → 转 int64 时溢出回绕。改用 `>=` 顶住舍入边界。
func USDToMicro(usd float64) (int64, bool) {
	if math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, false
	}
	micro := math.Round(usd * float64(MicroPerUSD))
	// int64 范围检查：float64(MaxInt64) 因 IEEE 754 精度会向上舍入，必须用 `>=`
	if micro >= float64(math.MaxInt64) || micro <= float64(math.MinInt64) {
		return 0, false
	}
	return int64(micro), true
}

// MicroToUSD 将 micro_usd int64 转回 USD float64（仅用于显示 / JSON 边界）。
//
// 内部计算路径不应调用此函数 —— 计算用 int64 micro 全程整数运算。
func MicroToUSD(micro int64) float64 {
	return float64(micro) / float64(MicroPerUSD)
}

// FormatMicroUSD 格式化 micro_usd 为定长字符串（保留 6 位小数，便于前端 BigInt/Decimal 解析）。
//
// 示例：FormatMicroUSD(12_345_678) → "12.345678"
//
//	FormatMicroUSD(-500) → "-0.000500"
//
// fix MAJOR Phase 4-codex（第二十四轮）：math.MinInt64 取负会溢出（-MinInt64 = MinInt64）。
// 改用 strconv.FormatInt + 字符串拼接处理符号，避免 negation overflow。
func FormatMicroUSD(micro int64) string {
	if micro == math.MinInt64 {
		// MinInt64 = -9223372036854775808 micro_usd = -9223372036854.775808 USD
		// hardcode 避免 -MinInt64 溢出
		return "-9223372036854.775808"
	}
	neg := micro < 0
	if neg {
		micro = -micro
	}
	whole := micro / MicroPerUSD
	frac := micro % MicroPerUSD
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%06d", sign, whole, frac)
}

// RMBToFen 将 RMB float 转为 fen int64（四舍五入）。
//
// 易付通回调金额是 string（如 "72.00"），调用方先 ParseFloat 再走此 helper。
//
// fix MAJOR Phase 4-codex：边界用 `>=`/`<=` 处理 float64(MaxInt64) 向上舍入问题。
func RMBToFen(rmb float64) (int64, bool) {
	if math.IsNaN(rmb) || math.IsInf(rmb, 0) {
		return 0, false
	}
	fen := math.Round(rmb * float64(FenPerRMB))
	if fen >= float64(math.MaxInt64) || fen <= float64(math.MinInt64) {
		return 0, false
	}
	return int64(fen), true
}

// FenToRMB 将 fen int64 转回 RMB float64（仅显示用）。
func FenToRMB(fen int64) float64 {
	return float64(fen) / float64(FenPerRMB)
}

// FormatFen 格式化 fen 为定长 RMB 字符串（保留 2 位小数）。
//
// 示例：FormatFen(7200) → "72.00"
//
// fix MAJOR Phase 4-codex：MinInt64 取负溢出处理。
func FormatFen(fen int64) string {
	if fen == math.MinInt64 {
		return "-92233720368547758.08"
	}
	neg := fen < 0
	if neg {
		fen = -fen
	}
	whole := fen / FenPerRMB
	frac := fen % FenPerRMB
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, whole, frac)
}

// CheckedMulInt64 安全的 int64 乘法（溢出返回 false）。
//
// fix CRITICAL Phase 4-codex：subscription.go 计算 price * qty 等金额乘法时无溢出守护，
// admin 设套餐价 1e15 + qty 1e6 → 溢出回绕成负数 → 用户净 deduct 变负 → 加余额白嫖。
// 用本 helper 在所有金额乘法路径前置 fail-closed 守护。
func CheckedMulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	c := a * b
	// 反算检查：如果 c/b == a 且 c/a == b（避免单边除 0/MinInt64 边界），则没溢出
	if a != 0 && c/a != b {
		return 0, false
	}
	// MinInt64 / -1 会溢出（结果应为 MaxInt64+1）；显式拒
	if (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
		return 0, false
	}
	return c, true
}

// CheckedAddInt64 安全的 int64 加法（溢出返回 false）。
//
// 用于累加金额列表等，避免溢出导致负值绕回。
func CheckedAddInt64(a, b int64) (int64, bool) {
	c := a + b
	// 同号相加可能溢出（异号永不溢出）
	if (a > 0 && b > 0 && c < 0) || (a < 0 && b < 0 && c >= 0) {
		return 0, false
	}
	return c, true
}

// CheckedSumInt64 累加 int64 切片，任一步溢出返回 false。
func CheckedSumInt64(xs []int64) (int64, bool) {
	var s int64
	for _, x := range xs {
		v, ok := CheckedAddInt64(s, x)
		if !ok {
			return 0, false
		}
		s = v
	}
	return s, true
}
