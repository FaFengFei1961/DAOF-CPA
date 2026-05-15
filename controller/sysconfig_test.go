package controller

import "testing"

func TestSysConfig_BalanceConsumeDefaultLimitMicroUSDValidation(t *testing.T) {
	setupSubTestDB(t)

	cases := []struct {
		name string
		raw  string
		code string
		ok   bool
	}{
		{name: "zero unlimited", raw: "0", ok: true},
		{name: "positive integer", raw: "1234567", ok: true},
		{name: "reject decimal usd", raw: "10.50", code: "ERR_LIMIT_INVALID"},
		{name: "reject negative", raw: "-1", code: "ERR_LIMIT_INVALID"},
		{name: "reject empty", raw: "", code: "ERR_LIMIT_INVALID"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, ok := validateSysConfigPayload(map[string]string{
				balanceConsumeDefaultLimitMicroUSDKey: tc.raw,
			})
			if ok != tc.ok || code != tc.code {
				t.Fatalf("validateSysConfigPayload() code=%q ok=%v, want code=%q ok=%v", code, ok, tc.code, tc.ok)
			}
		})
	}
}
