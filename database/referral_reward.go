package database

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ReferralPaidSpendRewardBPSConfigKey = "referral_paid_spend_reward_bps"
const ReferralPaidSpendRewardWindowSecondsConfigKey = "referral_paid_spend_reward_window_seconds"
const DefaultReferralPaidSpendRewardWindowSeconds = int64(30 * 24 * 60 * 60)

type ReferralPaidSpendRewardResult struct {
	ReferrerID            uint
	RefereeID             uint
	RefereeUsername       string
	EligibleSpendMicroUSD int64
	RewardMicroUSD        int64
	RateBPS               int64
	WindowSeconds         int64
	WithinRewardWindow    bool
	RelatedType           string
	RelatedID             uint
}

func NormalizeReferralRewardBPS(bps int64) int64 {
	if bps < 0 {
		return 0
	}
	if bps > 10_000 {
		return 10_000
	}
	return bps
}

func NormalizeReferralRewardWindowSeconds(seconds int64) int64 {
	if seconds < 60 {
		return DefaultReferralPaidSpendRewardWindowSeconds
	}
	max := int64(365 * 24 * 60 * 60)
	if seconds > max {
		return max
	}
	return seconds
}

func ApplyBasisPointsFloor(amountMicro, bps int64) int64 {
	if amountMicro <= 0 || bps <= 0 {
		return 0
	}
	product := new(big.Int).Mul(big.NewInt(amountMicro), big.NewInt(bps))
	result := new(big.Int).Quo(product, big.NewInt(10_000))
	if !result.IsInt64() {
		return math.MaxInt64
	}
	return result.Int64()
}

func FormatBasisPointsPercent(bps int64) string {
	bps = NormalizeReferralRewardBPS(bps)
	if bps == 0 {
		return "0%"
	}
	whole := bps / 100
	frac := bps % 100
	if frac == 0 {
		return fmt.Sprintf("%d%%", whole)
	}
	fracText := strings.TrimRight(fmt.Sprintf("%02d", frac), "0")
	return fmt.Sprintf("%d.%s%%", whole, fracText)
}

// ApplyReferralPaidSpendRewardTx records a referrer reward when a referred user
// spends balance that came from their own payment-channel top-ups.
//
// It deliberately excludes signup bonuses, referral bonuses, admin adjustments,
// grants, and any other non-top-up credit. The caller invokes this after the
// main quota deduction has already happened, so we treat paid_quota as a
// protected floor: non-paid credit is consumed first, and only the amount by
// which the post-spend quota falls below the previous paid_quota counts as
// paid spend.
func ApplyReferralPaidSpendRewardTx(tx *gorm.DB, refereeUserID uint, spendMicroUSD int64, rateBPS int64, windowSeconds int64, occurredAt time.Time, relatedType string, relatedID uint, spendLabel string) (ReferralPaidSpendRewardResult, error) {
	rateBPS = NormalizeReferralRewardBPS(rateBPS)
	windowSeconds = NormalizeReferralRewardWindowSeconds(windowSeconds)
	if tx == nil || refereeUserID == 0 || spendMicroUSD <= 0 {
		return ReferralPaidSpendRewardResult{}, nil
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	var referee User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id, username, quota, referred_by_user_id, referred_at, paid_quota").
		First(&referee, refereeUserID).Error; err != nil {
		return ReferralPaidSpendRewardResult{}, fmt.Errorf("load referee: %w", err)
	}
	if referee.PaidQuota <= 0 {
		return ReferralPaidSpendRewardResult{}, nil
	}

	remainingPaidQuota := referee.PaidQuota
	if referee.Quota < remainingPaidQuota {
		remainingPaidQuota = referee.Quota
	}
	if remainingPaidQuota < 0 {
		remainingPaidQuota = 0
	}
	paidQuotaReduction := referee.PaidQuota - remainingPaidQuota
	eligibleSpend := paidQuotaReduction
	if eligibleSpend > spendMicroUSD {
		eligibleSpend = spendMicroUSD
	}
	if eligibleSpend < 0 {
		eligibleSpend = 0
	}

	if paidQuotaReduction > 0 {
		if err := tx.Model(&User{}).
			Where("id = ?", referee.ID).
			UpdateColumn("paid_quota", remainingPaidQuota).Error; err != nil {
			return ReferralPaidSpendRewardResult{}, fmt.Errorf("consume paid quota: %w", err)
		}
	}

	if eligibleSpend <= 0 {
		return ReferralPaidSpendRewardResult{
			RefereeID:             referee.ID,
			RefereeUsername:       referee.Username,
			EligibleSpendMicroUSD: 0,
			RateBPS:               rateBPS,
			WindowSeconds:         windowSeconds,
			RelatedType:           relatedType,
			RelatedID:             relatedID,
		}, nil
	}

	result := ReferralPaidSpendRewardResult{
		RefereeID:             referee.ID,
		RefereeUsername:       referee.Username,
		EligibleSpendMicroUSD: eligibleSpend,
		RateBPS:               rateBPS,
		WindowSeconds:         windowSeconds,
		RelatedType:           relatedType,
		RelatedID:             relatedID,
	}
	if referee.ReferredByUserID == 0 || referee.ReferredByUserID == referee.ID || referee.ReferredAt == nil {
		return result, nil
	}
	result.ReferrerID = referee.ReferredByUserID
	result.WithinRewardWindow = !occurredAt.After(referee.ReferredAt.Add(time.Duration(windowSeconds) * time.Second))
	if !result.WithinRewardWindow || rateBPS <= 0 {
		return result, nil
	}

	rewardMicro := ApplyBasisPointsFloor(eligibleSpend, rateBPS)
	if rewardMicro <= 0 {
		return result, nil
	}

	res := tx.Model(&User{}).
		Where("id = ? AND role = ? AND status = 1", referee.ReferredByUserID, "user").
		UpdateColumn("quota", gorm.Expr("quota + ?", rewardMicro))
	if res.Error != nil {
		return ReferralPaidSpendRewardResult{}, fmt.Errorf("update referrer quota: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return result, nil
	}

	var freshReferrer User
	if err := tx.Select("id, quota").First(&freshReferrer, referee.ReferredByUserID).Error; err != nil {
		return ReferralPaidSpendRewardResult{}, fmt.Errorf("load referrer fresh quota: %w", err)
	}
	if strings.TrimSpace(spendLabel) == "" {
		spendLabel = "消费"
	}
	if err := WriteBillingEntry(tx, BillingEntryInput{
		UserID:          referee.ReferredByUserID,
		OccurredAt:      occurredAt,
		EntryType:       BillingTypeBonusCredit,
		AmountUSD:       rewardMicro,
		BalanceAfterUSD: freshReferrer.Quota,
		RelatedType:     relatedType,
		RelatedID:       relatedID,
		Description: fmt.Sprintf("拉新消费奖励：用户 %s %s，其中 %s 来自自充余额，按 %s 奖励",
			referee.Username, spendLabel, FormatMicroUSD(eligibleSpend), FormatBasisPointsPercent(rateBPS)),
	}); err != nil {
		return ReferralPaidSpendRewardResult{}, fmt.Errorf("write referral spend billing: %w", err)
	}
	result.RewardMicroUSD = rewardMicro
	return result, nil
}
