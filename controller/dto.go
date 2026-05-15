package controller

import (
	"time"

	"daof-ai-hub/database"
)

// BillingEntryView is the public billing wire contract.
// Internal DB fields keep int64 micro_usd/fen; HTTP responses expose human units.
type BillingEntryView struct {
	ID                   uint      `json:"id"`
	UserID               uint      `json:"user_id"`
	OccurredAt           time.Time `json:"occurred_at"`
	EntryType            string    `json:"entry_type"`
	BillingState         string    `json:"billing_state"`
	AmountUSD            float64   `json:"amount_usd"`
	BalanceAfterUSD      float64   `json:"balance_after_usd"`
	RelatedType          string    `json:"related_type"`
	RelatedID            uint      `json:"related_id"`
	ModelName            string    `json:"model_name,omitempty"`
	TokensTotal          int       `json:"tokens_total,omitempty"`
	RequestID            string    `json:"request_id,omitempty"`
	DeliveredBytes       int64     `json:"delivered_bytes,omitempty"`
	EstimatedInputTokens int       `json:"estimated_input_tokens,omitempty"`
	EstimatedCostUSD     float64   `json:"estimated_cost_usd,omitempty"`
	SourceSubscriptionID *uint     `json:"source_subscription_id,omitempty"`
	Description          string    `json:"description"`
	CurrencyOriginal     string    `json:"currency_original,omitempty"`
	AmountOriginal       float64   `json:"amount_original,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

// TopupOrderView is the public topup order wire contract.
type TopupOrderView struct {
	ID                   uint       `json:"id"`
	OutTradeNo           string     `json:"out_trade_no"`
	TradeNo              string     `json:"trade_no"`
	ApiTradeNo           string     `json:"api_trade_no"`
	UserID               uint       `json:"user_id"`
	PayType              string     `json:"pay_type"`
	Device               string     `json:"device"`
	MoneyRMB             float64    `json:"money_rmb"`
	AmountUSD            float64    `json:"amount_usd"`
	ExchangeRateSnapshot float64    `json:"exchange_rate_snapshot"`
	Name                 string     `json:"name"`
	ClientIP             string     `json:"client_ip"`
	Param                string     `json:"param"`
	GatewayPayType       string     `json:"gateway_pay_type"`
	PayInfo              string     `json:"pay_info"`
	PayMethod            string     `json:"pay_method"`
	Status               string     `json:"status"`
	RefundedAmountRMB    float64    `json:"refunded_amount_rmb"`
	RefundNo             string     `json:"refund_no"`
	OutRefundNo          string     `json:"out_refund_no"`
	CreatedAt            time.Time  `json:"created_at"`
	PaidAt               *time.Time `json:"paid_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

// CouponTemplateView is the admin coupon-template wire contract.
type CouponTemplateView struct {
	ID            uint      `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	DiscountType  string    `json:"discount_type"`
	DiscountValue float64   `json:"discount_value"`
	PackageIDs    string    `json:"package_ids"`
	ValidDays     int       `json:"valid_days"`
	Enabled       *bool     `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func microUSDToFloat(micro int64) float64 {
	cents := roundHalfEvenUnits(micro, database.MicroPerUSD/100)
	return float64(cents) / 100
}

func fenToRMBFloat(fen int64) float64 {
	return float64(fen) / 100
}

func roundHalfEvenUnits(v, unit int64) int64 {
	if unit <= 0 {
		return v
	}
	q := v / unit
	r := v % unit
	if r == 0 {
		return q
	}
	absR := r
	if absR < 0 {
		absR = -absR
	}
	half := unit / 2
	if absR < half {
		return q
	}
	sign := int64(1)
	if v < 0 {
		sign = -1
	}
	if absR > half || q%2 != 0 {
		return q + sign
	}
	return q
}

func billingEntryViewFrom(b database.BillingEntry) BillingEntryView {
	amountOriginal := float64(b.AmountOriginal)
	switch b.CurrencyOriginal {
	case "USD":
		amountOriginal = microUSDToFloat(b.AmountOriginal)
	case "CNY", "RMB":
		amountOriginal = fenToRMBFloat(b.AmountOriginal)
	}
	return BillingEntryView{
		ID:                   b.ID,
		UserID:               b.UserID,
		OccurredAt:           b.OccurredAt,
		EntryType:            b.EntryType,
		BillingState:         b.BillingState,
		AmountUSD:            microUSDToFloat(b.AmountUSD),
		BalanceAfterUSD:      microUSDToFloat(b.BalanceAfterUSD),
		RelatedType:          b.RelatedType,
		RelatedID:            b.RelatedID,
		ModelName:            b.ModelName,
		TokensTotal:          b.TokensTotal,
		RequestID:            b.RequestID,
		DeliveredBytes:       b.DeliveredBytes,
		EstimatedInputTokens: b.EstimatedInputTokens,
		EstimatedCostUSD:     microUSDToFloat(b.EstimatedCostUSD),
		SourceSubscriptionID: b.SourceSubscriptionID,
		Description:          b.Description,
		CurrencyOriginal:     b.CurrencyOriginal,
		AmountOriginal:       amountOriginal,
		CreatedAt:            b.CreatedAt,
	}
}

func billingEntryViewsFrom(rows []database.BillingEntry) []BillingEntryView {
	out := make([]BillingEntryView, 0, len(rows))
	for _, row := range rows {
		out = append(out, billingEntryViewFrom(row))
	}
	return out
}

func topupOrderViewFrom(o database.TopupOrder) TopupOrderView {
	return TopupOrderView{
		ID:                   o.ID,
		OutTradeNo:           o.OutTradeNo,
		TradeNo:              o.TradeNo,
		ApiTradeNo:           o.ApiTradeNo,
		UserID:               o.UserID,
		PayType:              o.PayType,
		Device:               o.Device,
		MoneyRMB:             fenToRMBFloat(o.MoneyRMB),
		AmountUSD:            microUSDToFloat(o.AmountUSD),
		ExchangeRateSnapshot: o.ExchangeRateSnapshot,
		Name:                 o.Name,
		ClientIP:             o.ClientIP,
		Param:                o.Param,
		GatewayPayType:       o.GatewayPayType,
		PayInfo:              o.PayInfo,
		PayMethod:            o.PayMethod,
		Status:               o.Status,
		RefundedAmountRMB:    fenToRMBFloat(o.RefundedAmountRMB),
		RefundNo:             o.RefundNo,
		OutRefundNo:          o.OutRefundNo,
		CreatedAt:            o.CreatedAt,
		PaidAt:               o.PaidAt,
		UpdatedAt:            o.UpdatedAt,
	}
}

func topupOrderViewsFrom(rows []database.TopupOrder) []TopupOrderView {
	out := make([]TopupOrderView, 0, len(rows))
	for _, row := range rows {
		out = append(out, topupOrderViewFrom(row))
	}
	return out
}

func couponTemplateViewFrom(t database.CouponTemplate) CouponTemplateView {
	discountValue := float64(t.DiscountValue)
	if t.DiscountType != "percent" {
		discountValue = microUSDToFloat(t.DiscountValue)
	}
	return CouponTemplateView{
		ID:            t.ID,
		Name:          t.Name,
		Description:   t.Description,
		DiscountType:  t.DiscountType,
		DiscountValue: discountValue,
		PackageIDs:    t.PackageIDs,
		ValidDays:     t.ValidDays,
		Enabled:       t.Enabled,
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
	}
}

func couponTemplateViewsFrom(rows []database.CouponTemplate) []CouponTemplateView {
	out := make([]CouponTemplateView, 0, len(rows))
	for _, row := range rows {
		out = append(out, couponTemplateViewFrom(row))
	}
	return out
}
