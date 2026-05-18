package database

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupReferralRewardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&User{}, &BillingEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestApplyReferralPaidSpendRewardTx_RewardsOnlyPaidSpendInsideWindow(t *testing.T) {
	db := setupReferralRewardTestDB(t)
	referredAt := time.Now().Add(-24 * time.Hour)
	referrer := User{Username: "referrer", Token: "sk-referrer", Role: "user", Status: 1}
	referee := User{
		Username:         "referee",
		Token:            "sk-referee",
		Role:             "user",
		Status:           1,
		Quota:            0,
		PaidQuota:        5 * MicroPerUSD,
		ReferredAt:       &referredAt,
		ReferredByUserID: 1,
	}
	if err := db.Create(&referrer).Error; err != nil {
		t.Fatalf("create referrer: %v", err)
	}
	referee.ReferredByUserID = referrer.ID
	if err := db.Create(&referee).Error; err != nil {
		t.Fatalf("create referee: %v", err)
	}

	var got ReferralPaidSpendRewardResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		got, err = ApplyReferralPaidSpendRewardTx(
			tx, referee.ID, 8*MicroPerUSD, 1000, 30*24*60*60,
			time.Now(), "subscription", 42, "购买套餐",
		)
		return err
	}); err != nil {
		t.Fatalf("apply reward: %v", err)
	}

	if got.EligibleSpendMicroUSD != 5*MicroPerUSD {
		t.Fatalf("eligible spend=%d, want %d", got.EligibleSpendMicroUSD, 5*MicroPerUSD)
	}
	if got.RewardMicroUSD != 500_000 {
		t.Fatalf("reward=%d, want 500000", got.RewardMicroUSD)
	}
	if !got.WithinRewardWindow {
		t.Fatal("expected spend inside reward window")
	}

	var freshReferee, freshReferrer User
	if err := db.First(&freshReferee, referee.ID).Error; err != nil {
		t.Fatalf("load referee: %v", err)
	}
	if freshReferee.PaidQuota != 0 {
		t.Fatalf("referee paid_quota=%d, want 0", freshReferee.PaidQuota)
	}
	if err := db.First(&freshReferrer, referrer.ID).Error; err != nil {
		t.Fatalf("load referrer: %v", err)
	}
	if freshReferrer.Quota != 500_000 {
		t.Fatalf("referrer quota=%d, want 500000", freshReferrer.Quota)
	}

	var entry BillingEntry
	if err := db.Where("user_id = ? AND entry_type = ?", referrer.ID, BillingTypeBonusCredit).First(&entry).Error; err != nil {
		t.Fatalf("reward billing missing: %v", err)
	}
	if entry.AmountUSD != 500_000 || entry.RelatedType != "subscription" || entry.RelatedID != 42 {
		t.Fatalf("unexpected reward billing: %+v", entry)
	}
}

func TestApplyReferralPaidSpendRewardTx_ConsumesBonusBeforePaidQuota(t *testing.T) {
	db := setupReferralRewardTestDB(t)
	referredAt := time.Now().Add(-24 * time.Hour)
	referrer := User{Username: "bonus-referrer", Token: "sk-bonus-referrer", Role: "user", Status: 1}
	if err := db.Create(&referrer).Error; err != nil {
		t.Fatalf("create referrer: %v", err)
	}
	referee := User{
		Username:         "bonus-referee",
		Token:            "sk-bonus-referee",
		Role:             "user",
		Status:           1,
		Quota:            12 * MicroPerUSD, // after an $8 spend from $20 total
		PaidQuota:        5 * MicroPerUSD,  // $15 bonus existed before spend, so paid quota is untouched
		ReferredByUserID: referrer.ID,
		ReferredAt:       &referredAt,
	}
	if err := db.Create(&referee).Error; err != nil {
		t.Fatalf("create referee: %v", err)
	}

	var got ReferralPaidSpendRewardResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		got, err = ApplyReferralPaidSpendRewardTx(
			tx, referee.ID, 8*MicroPerUSD, 1000, 30*24*60*60,
			time.Now(), "subscription", 43, "购买套餐",
		)
		return err
	}); err != nil {
		t.Fatalf("apply reward: %v", err)
	}
	if got.EligibleSpendMicroUSD != 0 || got.RewardMicroUSD != 0 {
		t.Fatalf("bonus spend should not be rewardable: %+v", got)
	}

	var freshReferee, freshReferrer User
	if err := db.First(&freshReferee, referee.ID).Error; err != nil {
		t.Fatalf("load referee: %v", err)
	}
	if freshReferee.PaidQuota != 5*MicroPerUSD {
		t.Fatalf("paid_quota=%d, want unchanged %d", freshReferee.PaidQuota, 5*MicroPerUSD)
	}
	if err := db.First(&freshReferrer, referrer.ID).Error; err != nil {
		t.Fatalf("load referrer: %v", err)
	}
	if freshReferrer.Quota != 0 {
		t.Fatalf("referrer quota=%d, want 0", freshReferrer.Quota)
	}
	var count int64
	db.Model(&BillingEntry{}).Where("user_id = ?", referrer.ID).Count(&count)
	if count != 0 {
		t.Fatalf("bonus spend should not write reward billing, got %d rows", count)
	}
}

func TestApplyReferralPaidSpendRewardTx_ExpiredWindowConsumesPaidQuotaWithoutReward(t *testing.T) {
	db := setupReferralRewardTestDB(t)
	referredAt := time.Now().Add(-31 * 24 * time.Hour)
	referrer := User{Username: "expired-referrer", Token: "sk-exp-referrer", Role: "user", Status: 1}
	if err := db.Create(&referrer).Error; err != nil {
		t.Fatalf("create referrer: %v", err)
	}
	referee := User{
		Username:         "expired-referee",
		Token:            "sk-exp-referee",
		Role:             "user",
		Status:           1,
		Quota:            MicroPerUSD,
		PaidQuota:        2 * MicroPerUSD,
		ReferredByUserID: referrer.ID,
		ReferredAt:       &referredAt,
	}
	if err := db.Create(&referee).Error; err != nil {
		t.Fatalf("create referee: %v", err)
	}

	var got ReferralPaidSpendRewardResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		got, err = ApplyReferralPaidSpendRewardTx(
			tx, referee.ID, MicroPerUSD, 1000, 30*24*60*60,
			time.Now(), "api_log", 7, "余额扣费",
		)
		return err
	}); err != nil {
		t.Fatalf("apply reward: %v", err)
	}
	if got.RewardMicroUSD != 0 || got.WithinRewardWindow {
		t.Fatalf("expired spend should not reward: %+v", got)
	}
	var freshReferee, freshReferrer User
	_ = db.First(&freshReferee, referee.ID).Error
	_ = db.First(&freshReferrer, referrer.ID).Error
	if freshReferee.PaidQuota != MicroPerUSD {
		t.Fatalf("paid_quota=%d, want %d", freshReferee.PaidQuota, MicroPerUSD)
	}
	if freshReferrer.Quota != 0 {
		t.Fatalf("referrer quota=%d, want 0", freshReferrer.Quota)
	}
	var count int64
	db.Model(&BillingEntry{}).Where("user_id = ?", referrer.ID).Count(&count)
	if count != 0 {
		t.Fatalf("expired reward should not write billing, got %d rows", count)
	}
}

func TestApplyReferralPaidSpendRewardTx_DisabledRateStillConsumesPaidQuota(t *testing.T) {
	db := setupReferralRewardTestDB(t)
	referredAt := time.Now()
	referrer := User{Username: "disabled-referrer", Token: "sk-dis-referrer", Role: "user", Status: 1}
	if err := db.Create(&referrer).Error; err != nil {
		t.Fatalf("create referrer: %v", err)
	}
	referee := User{
		Username:         "disabled-referee",
		Token:            "sk-dis-referee",
		Role:             "user",
		Status:           1,
		Quota:            2 * MicroPerUSD,
		PaidQuota:        3 * MicroPerUSD,
		ReferredByUserID: referrer.ID,
		ReferredAt:       &referredAt,
	}
	if err := db.Create(&referee).Error; err != nil {
		t.Fatalf("create referee: %v", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := ApplyReferralPaidSpendRewardTx(
			tx, referee.ID, MicroPerUSD, 0, 30*24*60*60,
			time.Now(), "api_log", 9, "余额扣费",
		)
		return err
	}); err != nil {
		t.Fatalf("apply reward: %v", err)
	}
	var fresh User
	if err := db.First(&fresh, referee.ID).Error; err != nil {
		t.Fatalf("load referee: %v", err)
	}
	if fresh.PaidQuota != 2*MicroPerUSD {
		t.Fatalf("paid_quota=%d, want %d", fresh.PaidQuota, 2*MicroPerUSD)
	}
}
