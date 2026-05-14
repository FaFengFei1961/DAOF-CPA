// Package database / marshaling.go
//
// API JSON 序列化时把内部 int64 micro_usd 转成 USD float（前端友好）。
//
// 设计动机（fix MAJOR M22-A1 Phase 3）：
//   - DB 列 / Go 字段 / 内部算术：int64 micro_usd（无浮点漂移）
//   - JSON wire format：USD float（前端展示直观；JS Number.MAX_SAFE_INTEGER 之内）
//
// 通过自定义 MarshalJSON 实现"边界单位转换 + 内部精度无损"。仅写出（Marshal）路径转换，
// 输入（Unmarshal）路径仍走专用 DTO（如 controller.parsePackagePayload）以保留显式审计点。
//
// 模式：embedded type alias + 字段同名 JSON tag shadow，让编码器只见到外层 USD float 字段。
//
// 注意：
//   - GORM 用 reflect 直接读字段，不走 encoding/json，所以 MarshalJSON 不影响 DB 写入
//   - PackageSnapshot 序列化用 *独立* 的 snap 结构（subscription.go），同样不受影响
//   - 仅"API JSON 输出"路径生效
package database

import (
	"encoding/json"
)

// ─── User ────────────────────────────────────────────────────────────

// MarshalJSON 把 User 的金钱字段从 int64 micro_usd 转成 USD float 输出。
//
// 涉及字段：Quota / BalanceConsumeLimitUSD / BalanceConsumedInWindow。
func (u User) MarshalJSON() ([]byte, error) {
	type userAlias User
	return json.Marshal(&struct {
		*userAlias
		Quota                   float64 `json:"quota"`
		BalanceConsumeLimitUSD  float64 `json:"balance_consume_limit_usd"`
		BalanceConsumedInWindow float64 `json:"balance_consumed_in_window"`
	}{
		userAlias:               (*userAlias)(&u),
		Quota:                   MicroToUSD(u.Quota),
		BalanceConsumeLimitUSD:  MicroToUSD(u.BalanceConsumeLimitUSD),
		BalanceConsumedInWindow: MicroToUSD(u.BalanceConsumedInWindow),
	})
}

// ─── AccessToken ─────────────────────────────────────────────────────

func (a AccessToken) MarshalJSON() ([]byte, error) {
	type accessTokenAlias AccessToken
	return json.Marshal(&struct {
		*accessTokenAlias
		UsedQuota  float64 `json:"used_quota"`
		QuotaLimit float64 `json:"quota_limit"`
	}{
		accessTokenAlias: (*accessTokenAlias)(&a),
		UsedQuota:        MicroToUSD(a.UsedQuota),
		QuotaLimit:       MicroToUSD(a.QuotaLimit),
	})
}

// ─── ApiLog ──────────────────────────────────────────────────────────

func (l ApiLog) MarshalJSON() ([]byte, error) {
	type apiLogAlias ApiLog
	chargedCost := l.ChargedCost
	if chargedCost == 0 && l.Cost > 0 {
		chargedCost = l.Cost
	}
	modelWeight := l.ModelWeight
	if modelWeight == 0 {
		modelWeight = 1
	}
	healthMultiplier := l.HealthMultiplier
	if healthMultiplier == 0 {
		healthMultiplier = 1
	}
	requestedModel := l.RequestedModel
	if requestedModel == "" {
		requestedModel = l.ModelName
	}
	servedModel := l.ServedModel
	if servedModel == "" {
		servedModel = l.ModelName
	}
	return json.Marshal(&struct {
		*apiLogAlias
		Cost                   float64 `json:"cost"`
		RawCost                float64 `json:"raw_cost"`
		ChargedCost            float64 `json:"charged_cost"`
		PlatformCostEstimate   float64 `json:"platform_cost_estimate"`
		PrecheckRawCost        float64 `json:"precheck_raw_cost"`
		PrecheckChargedCost    float64 `json:"precheck_charged_cost"`
		PrecheckQuotaLimit     float64 `json:"precheck_quota_limit"`
		PrecheckQuotaUsed      float64 `json:"precheck_quota_used"`
		PrecheckQuotaRemaining float64 `json:"precheck_quota_remaining"`
		ModelWeight            float64 `json:"model_weight"`
		HealthMultiplier       float64 `json:"health_multiplier"`
		RequestedModel         string  `json:"requested_model"`
		ServedModel            string  `json:"served_model"`
	}{
		apiLogAlias:            (*apiLogAlias)(&l),
		Cost:                   MicroToUSD(l.Cost),
		RawCost:                MicroToUSD(l.Cost),
		ChargedCost:            MicroToUSD(chargedCost),
		PlatformCostEstimate:   MicroToUSD(l.PlatformCostEstimate),
		PrecheckRawCost:        MicroToUSD(l.PrecheckRawCost),
		PrecheckChargedCost:    MicroToUSD(l.PrecheckChargedCost),
		PrecheckQuotaLimit:     MicroToUSD(l.PrecheckQuotaLimit),
		PrecheckQuotaUsed:      MicroToUSD(l.PrecheckQuotaUsed),
		PrecheckQuotaRemaining: MicroToUSD(l.PrecheckQuotaRemaining),
		ModelWeight:            modelWeight,
		HealthMultiplier:       healthMultiplier,
		RequestedModel:         requestedModel,
		ServedModel:            servedModel,
	})
}

// ─── BillingEntry ────────────────────────────────────────────────────

// BillingEntry 序列化：AmountUSD / BalanceAfterUSD 转 USD float；
// AmountOriginal 单位由 CurrencyOriginal 决定（USD → micro_usd, RMB → fen），
// 这里同样转换：USD → div 1e6；RMB → div 100；其他保留 raw int64。
func (b BillingEntry) MarshalJSON() ([]byte, error) {
	type billingEntryAlias BillingEntry
	originalDisplay := float64(b.AmountOriginal)
	switch b.CurrencyOriginal {
	case "USD":
		originalDisplay = MicroToUSD(b.AmountOriginal)
	case "CNY", "RMB":
		originalDisplay = FenToRMB(b.AmountOriginal)
	}
	return json.Marshal(&struct {
		*billingEntryAlias
		AmountUSD       float64 `json:"amount_usd"`
		BalanceAfterUSD float64 `json:"balance_after_usd"`
		AmountOriginal  float64 `json:"amount_original,omitempty"`
	}{
		billingEntryAlias: (*billingEntryAlias)(&b),
		AmountUSD:         MicroToUSD(b.AmountUSD),
		BalanceAfterUSD:   MicroToUSD(b.BalanceAfterUSD),
		AmountOriginal:    originalDisplay,
	})
}

// ─── Package ─────────────────────────────────────────────────────────

func (p Package) MarshalJSON() ([]byte, error) {
	type packageAlias Package
	return json.Marshal(&struct {
		*packageAlias
		PriceAmount float64 `json:"price_amount"`
	}{
		packageAlias: (*packageAlias)(&p),
		PriceAmount:  MicroToUSD(p.PriceAmount),
	})
}

// ─── UserSubscription ────────────────────────────────────────────────

func (us UserSubscription) MarshalJSON() ([]byte, error) {
	type userSubscriptionAlias UserSubscription
	return json.Marshal(&struct {
		*userSubscriptionAlias
		PurchasedUnitPriceUSD float64 `json:"purchased_unit_price_usd"`
	}{
		userSubscriptionAlias: (*userSubscriptionAlias)(&us),
		PurchasedUnitPriceUSD: MicroToUSD(us.PurchasedUnitPriceUSD),
	})
}

// ─── CouponTemplate ──────────────────────────────────────────────────

func (ct CouponTemplate) MarshalJSON() ([]byte, error) {
	type couponTemplateAlias CouponTemplate
	return json.Marshal(&struct {
		*couponTemplateAlias
		DiscountValue float64 `json:"discount_value"`
	}{
		couponTemplateAlias: (*couponTemplateAlias)(&ct),
		DiscountValue:       MicroToUSD(ct.DiscountValue),
	})
}

// ─── UserCoupon ──────────────────────────────────────────────────────

func (uc UserCoupon) MarshalJSON() ([]byte, error) {
	type userCouponAlias UserCoupon
	return json.Marshal(&struct {
		*userCouponAlias
		SnapshotValue float64 `json:"snapshot_value"`
		UsedSavingUSD float64 `json:"used_saving_usd"`
	}{
		userCouponAlias: (*userCouponAlias)(&uc),
		SnapshotValue:   MicroToUSD(uc.SnapshotValue),
		UsedSavingUSD:   MicroToUSD(uc.UsedSavingUSD),
	})
}

// ─── TopupOrder ──────────────────────────────────────────────────────

func (to TopupOrder) MarshalJSON() ([]byte, error) {
	type topupOrderAlias TopupOrder
	return json.Marshal(&struct {
		*topupOrderAlias
		MoneyRMB          float64 `json:"money_rmb"`
		AmountUSD         float64 `json:"amount_usd"`
		RefundedAmountRMB float64 `json:"refunded_amount_rmb"`
	}{
		topupOrderAlias:   (*topupOrderAlias)(&to),
		MoneyRMB:          FenToRMB(to.MoneyRMB),
		AmountUSD:         MicroToUSD(to.AmountUSD),
		RefundedAmountRMB: FenToRMB(to.RefundedAmountRMB),
	})
}
