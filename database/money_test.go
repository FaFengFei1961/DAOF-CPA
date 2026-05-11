package database

import (
	"math"
	"testing"
)

func TestUSDToMicro(t *testing.T) {
	cases := []struct {
		in     float64
		want   int64
		wantOK bool
	}{
		{0, 0, true},
		{1, 1_000_000, true},
		{12.34, 12_340_000, true},
		{0.000001, 1, true},
		{-9.99, -9_990_000, true},
		{0.1 + 0.2, 300_000, true}, // 浮点累加经典 0.1+0.2≠0.3，micro 化后稳定
		{math.NaN(), 0, false},
		{math.Inf(1), 0, false},
		{math.Inf(-1), 0, false},
	}
	for _, tc := range cases {
		got, ok := USDToMicro(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("USDToMicro(%v) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestMicroToUSD(t *testing.T) {
	cases := []struct {
		in   int64
		want float64
	}{
		{0, 0},
		{1_000_000, 1},
		{12_345_678, 12.345678},
		{-9_990_000, -9.99},
	}
	for _, tc := range cases {
		got := MicroToUSD(tc.in)
		if got != tc.want {
			t.Errorf("MicroToUSD(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestFormatMicroUSD(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0.000000"},
		{1_000_000, "1.000000"},
		{12_345_678, "12.345678"},
		{-500, "-0.000500"},
		{-12_345_678, "-12.345678"},
	}
	for _, tc := range cases {
		got := FormatMicroUSD(tc.in)
		if got != tc.want {
			t.Errorf("FormatMicroUSD(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRMBToFen(t *testing.T) {
	cases := []struct {
		in     float64
		want   int64
		wantOK bool
	}{
		{0, 0, true},
		{1, 100, true},
		{72.00, 7200, true},
		{72.005, 7201, true}, // 四舍五入
		{math.NaN(), 0, false},
	}
	for _, tc := range cases {
		got, ok := RMBToFen(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("RMBToFen(%v) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestFormatFen(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0.00"},
		{100, "1.00"},
		{7200, "72.00"},
		{-500, "-5.00"},
	}
	for _, tc := range cases {
		got := FormatFen(tc.in)
		if got != tc.want {
			t.Errorf("FormatFen(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// 关键不变量：USDToMicro(MicroToUSD(x)) == x 在 |x| <= 2^53 时精度无损（float64 安全整数范围）
//
// fix MAJOR Phase 4-codex（第二十四轮）：原注释声称对所有 int64 成立，
// 但 float64 mantissa 53 bit，超过 ±2^53 后整数表示就开始丢精度。
// 限定到安全范围并增加 MaxInt64 边界反例。
func TestMoneyRoundTrip_MicroLossless(t *testing.T) {
	cases := []int64{0, 1, 100, 1_000_000, 12_345_678, -9_990_000, math.MaxInt32}
	for _, v := range cases {
		got, ok := USDToMicro(MicroToUSD(v))
		if !ok || got != v {
			t.Errorf("round-trip lost precision: %d → %v → (%d, %v)", v, MicroToUSD(v), got, ok)
		}
	}
}

// fix Phase 4-codex（第二十四轮）：MaxInt64 边界 — float64(MaxInt64) 因 IEEE 754 向上舍入
// 到 9223372036854775808 (>MaxInt64)。原 `>` 检查放过这种值，转 int64 时溢出回绕。
// 修复后 `>=` 检查必须把这种 inputfail-close。
func TestUSDToMicro_MaxInt64Boundary(t *testing.T) {
	// MaxInt64 = 9_223_372_036_854_775_807 micro_usd
	// 直接传 USD = MaxInt64 / 1e6 ≈ 9.223e12 USD
	// USD * 1e6 = MaxInt64（可能舍入到 MaxInt64+1 即 9.223372036854776e18）
	// 必须被 >= MaxInt64 边界拒绝
	huge := float64(math.MaxInt64) / float64(MicroPerUSD)
	got, ok := USDToMicro(huge)
	// huge 可能略小于 MaxInt64（精度损失）也可能略大；任一情况下 ok 应是 false 或值正好可表示
	if ok && got >= 0 {
		// OK：值在安全范围内
	} else if !ok && got == 0 {
		// OK：被边界拒绝
	} else {
		t.Errorf("MaxInt64 boundary: USDToMicro(%v) = (%d, %v) — should be either ok=true safe value or ok=false", huge, got, ok)
	}

	// 极大值必须被拒
	if got, ok := USDToMicro(1e20); ok || got != 0 {
		t.Errorf("USDToMicro(1e20) should fail: got (%d, %v)", got, ok)
	}
	if got, ok := USDToMicro(-1e20); ok || got != 0 {
		t.Errorf("USDToMicro(-1e20) should fail: got (%d, %v)", got, ok)
	}
}

func TestFormatMicroUSD_MinInt64NoPanic(t *testing.T) {
	// fix Phase 4-codex：MinInt64 取负溢出。验证不 panic 且输出正确。
	got := FormatMicroUSD(math.MinInt64)
	want := "-9223372036854.775808"
	if got != want {
		t.Errorf("FormatMicroUSD(MinInt64) = %q, want %q", got, want)
	}
}

func TestFormatFen_MinInt64NoPanic(t *testing.T) {
	got := FormatFen(math.MinInt64)
	want := "-92233720368547758.08"
	if got != want {
		t.Errorf("FormatFen(MinInt64) = %q, want %q", got, want)
	}
}

func TestCheckedMulInt64(t *testing.T) {
	cases := []struct {
		a, b   int64
		want   int64
		wantOK bool
	}{
		{0, 0, 0, true},
		{0, math.MaxInt64, 0, true},
		{1, math.MaxInt64, math.MaxInt64, true},
		{2, math.MaxInt64 / 2, math.MaxInt64 - 1, true}, // 不溢出
		{2, math.MaxInt64/2 + 1, 0, false},               // 溢出
		{-1, math.MinInt64, 0, false},                    // -MinInt64 = MaxInt64+1 溢出
		{math.MaxInt64, math.MaxInt64, 0, false},
		{1_000_000, 1_000_000_000_000_000, 0, false}, // 1e15 * 1e6 溢出
	}
	for _, tc := range cases {
		got, ok := CheckedMulInt64(tc.a, tc.b)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("CheckedMulInt64(%d, %d) = (%d, %v), want (%d, %v)", tc.a, tc.b, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestCheckedAddInt64(t *testing.T) {
	cases := []struct {
		a, b   int64
		want   int64
		wantOK bool
	}{
		{0, 0, 0, true},
		{1, 2, 3, true},
		{math.MaxInt64, 0, math.MaxInt64, true},
		{math.MaxInt64, 1, 0, false},  // 正向溢出
		{math.MinInt64, -1, 0, false}, // 负向溢出
		{-1, math.MinInt64 + 1, math.MinInt64, true},
		{math.MaxInt64 - 5, 3, math.MaxInt64 - 2, true},
	}
	for _, tc := range cases {
		got, ok := CheckedAddInt64(tc.a, tc.b)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("CheckedAddInt64(%d, %d) = (%d, %v), want (%d, %v)", tc.a, tc.b, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestCheckedSumInt64(t *testing.T) {
	if got, ok := CheckedSumInt64([]int64{1, 2, 3, 4}); !ok || got != 10 {
		t.Errorf("CheckedSumInt64([1,2,3,4]) = (%d, %v), want (10, true)", got, ok)
	}
	// 累加溢出
	if got, ok := CheckedSumInt64([]int64{math.MaxInt64, 1}); ok || got != 0 {
		t.Errorf("CheckedSumInt64 overflow not detected: (%d, %v)", got, ok)
	}
	if got, ok := CheckedSumInt64([]int64{}); !ok || got != 0 {
		t.Errorf("CheckedSumInt64([]) = (%d, %v), want (0, true)", got, ok)
	}
}
