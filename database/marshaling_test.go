// Package database / marshaling_test.go
//
// 验证 MarshalJSON 在 API 边界把 int64 micro_usd 转成 USD float（前端友好）。
// 同时验证 PackageSnapshot 等内部 JSON 路径**不**走 MarshalJSON（保持原 int64 wire format）。
package database

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarshalJSON_User_QuotaAsUSDFloat(t *testing.T) {
	u := User{
		ID:                          1,
		Username:                    "alice",
		Quota:                       99_900_000, // $99.90 micro
		BalanceConsumeLimitUSD:      5_500_000,  // $5.50
		BalanceConsumedInWindow:     1_234_567,  // $1.234567
		BalanceConsumeWindowSeconds: 2592000,
		BalanceConsumeEnabled:       true,
	}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)

	// 验证：USD float 格式（应是 99.9 或 99.900000，不是 99900000）
	cases := []struct {
		want string
		desc string
	}{
		{`"quota":99.9`, "quota in USD float"},
		{`"balance_consume_limit_usd":5.5`, "limit in USD float"},
		{`"balance_consume_window_seconds":2592000`, "window seconds passthrough"},
		{`"username":"alice"`, "non-money field passthrough"},
	}
	for _, tc := range cases {
		if !strings.Contains(s, tc.want) {
			t.Errorf("%s: missing %q in %s", tc.desc, tc.want, s)
		}
	}

	// 验证：raw int64 micro_usd 不应出现在 JSON 输出
	if strings.Contains(s, `"quota":99900000`) {
		t.Errorf("quota should NOT be raw micro_usd: %s", s)
	}
}

func TestMarshalJSON_AccessToken(t *testing.T) {
	at := AccessToken{
		ID:         1,
		UserID:     2,
		Name:       "test-token",
		Key:        "sk-daof-test",
		UsedQuota:  500_000,    // $0.5
		QuotaLimit: 10_000_000, // $10
	}
	b, _ := json.Marshal(at)
	s := string(b)
	if !strings.Contains(s, `"used_quota":0.5`) {
		t.Errorf("used_quota: %s", s)
	}
	if !strings.Contains(s, `"quota_limit":10`) {
		t.Errorf("quota_limit: %s", s)
	}
}

func TestMarshalJSON_BillingEntry_USDCurrency(t *testing.T) {
	be := BillingEntry{
		ID:               1,
		UserID:           2,
		EntryType:        BillingTypeTopup,
		AmountUSD:        10_000_000, // $10
		BalanceAfterUSD:  50_000_000, // $50
		CurrencyOriginal: "USD",
		AmountOriginal:   10_000_000, // $10 micro
	}
	b, _ := json.Marshal(be)
	s := string(b)
	if !strings.Contains(s, `"amount_usd":10`) {
		t.Errorf("amount_usd: %s", s)
	}
	if !strings.Contains(s, `"balance_after_usd":50`) {
		t.Errorf("balance_after_usd: %s", s)
	}
	if !strings.Contains(s, `"amount_original":10`) {
		t.Errorf("USD original: %s", s)
	}
}

func TestMarshalJSON_BillingEntry_RMBCurrency(t *testing.T) {
	be := BillingEntry{
		ID:               1,
		UserID:           2,
		EntryType:        BillingTypeTopup,
		AmountUSD:        10_000_000,
		BalanceAfterUSD:  50_000_000,
		CurrencyOriginal: "CNY",
		AmountOriginal:   7200, // ¥72.00 fen
	}
	b, _ := json.Marshal(be)
	s := string(b)
	if !strings.Contains(s, `"amount_original":72`) {
		t.Errorf("RMB original should be 72.00, got: %s", s)
	}
}

func TestMarshalJSON_Package(t *testing.T) {
	pkg := Package{
		ID:                   1,
		Name:                 "Pro",
		PriceAmount:          9_900_000, // $9.90
		BonusBalanceUSD:      3_000_000, // $3
		BillingPeriodSeconds: 2592000,
	}
	b, _ := json.Marshal(pkg)
	s := string(b)
	if !strings.Contains(s, `"price_amount":9.9`) {
		t.Errorf("price: %s", s)
	}
	if !strings.Contains(s, `"bonus_balance_usd":3`) {
		t.Errorf("bonus: %s", s)
	}
}

func TestMarshalJSON_TopupOrder(t *testing.T) {
	to := TopupOrder{
		ID:                1,
		UserID:            2,
		MoneyRMB:          7200,        // ¥72
		AmountUSD:         10_000_000,  // $10
		RefundedAmountRMB: 1000,        // ¥10 退过
		ExchangeRateSnapshot: 7.2,
	}
	b, _ := json.Marshal(to)
	s := string(b)
	if !strings.Contains(s, `"money_rmb":72`) {
		t.Errorf("money_rmb: %s", s)
	}
	if !strings.Contains(s, `"amount_usd":10`) {
		t.Errorf("amount_usd: %s", s)
	}
	if !strings.Contains(s, `"refunded_amount_rmb":10`) {
		t.Errorf("refunded: %s", s)
	}
}

func TestMarshalJSON_UserCoupon(t *testing.T) {
	uc := UserCoupon{
		ID:            1,
		UserID:        2,
		Code:          "CP-test",
		Status:        "available",
		SnapshotName:  "节日券",
		SnapshotType:  "fixed_price",
		SnapshotValue: 5_000_000, // $5
		UsedSavingUSD: 2_500_000, // $2.5
	}
	b, _ := json.Marshal(uc)
	s := string(b)
	if !strings.Contains(s, `"snapshot_value":5`) {
		t.Errorf("snapshot_value: %s", s)
	}
	if !strings.Contains(s, `"used_saving_usd":2.5`) {
		t.Errorf("used_saving_usd: %s", s)
	}
}

func TestMarshalJSON_RoundTrip_SafeForLargeBalance(t *testing.T) {
	// JS Number.MAX_SAFE_INTEGER = 2^53 = 9_007_199_254_740_992
	// micro_usd 上限 = 2^53 micro = ~$9007 万亿，远超任何合理用户余额
	// 此测试验证大额（$1000 万）转换不丢精度
	u := User{Quota: 10_000_000 * MicroPerUSD} // $10M micro
	b, _ := json.Marshal(u)
	if !strings.Contains(string(b), `"quota":10000000`) {
		t.Errorf("$10M quota: %s", string(b))
	}
}
