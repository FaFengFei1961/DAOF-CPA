package database

import (
	"testing"
	"time"
)

func TestBackfillApiLogRevenues_GrantedSubscriptionZeroRevenue(t *testing.T) {
	db := openInMemoryDBWithModels(t, "file:revenue_backfill_grant?mode=memory&cache=shared",
		&ApiLog{}, &ApiLogRevenue{}, &BillingEntry{}, &UserSubscription{})
	now := time.Now()

	paidSub := UserSubscription{UserID: 1, Status: "active", StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour)}
	grantSub := UserSubscription{UserID: 2, Status: "active", StartAt: now.Add(-time.Hour), EndAt: now.Add(time.Hour), IsGranted: true}
	if err := db.Create(&paidSub).Error; err != nil {
		t.Fatalf("seed paid sub: %v", err)
	}
	if err := db.Create(&grantSub).Error; err != nil {
		t.Fatalf("seed granted sub: %v", err)
	}

	paidLog := ApiLog{UserID: 1, ModelName: "gpt-5.4", Cost: 100, ChargedCost: 250, Status: 200, CreatedAt: now}
	grantLog := ApiLog{UserID: 2, ModelName: "gpt-5.4", Cost: 100, ChargedCost: 250, Status: 200, CreatedAt: now}
	if err := db.Create(&paidLog).Error; err != nil {
		t.Fatalf("seed paid api log: %v", err)
	}
	if err := db.Create(&grantLog).Error; err != nil {
		t.Fatalf("seed grant api log: %v", err)
	}

	paidSubID := paidSub.ID
	grantSubID := grantSub.ID
	entries := []BillingEntry{
		{
			UserID: 1, OccurredAt: now, EntryType: BillingTypeApiUsageSub, AmountUSD: 0,
			RelatedType: "api_log", RelatedID: paidLog.ID, SourceSubscriptionID: &paidSubID,
		},
		{
			UserID: 2, OccurredAt: now, EntryType: BillingTypeApiUsageSub, AmountUSD: 0,
			RelatedType: "api_log", RelatedID: grantLog.ID, SourceSubscriptionID: &grantSubID,
		},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatalf("seed billing entries: %v", err)
	}

	backfillApiLogRevenues()

	var paidRevenue ApiLogRevenue
	if err := db.Where("api_log_id = ?", paidLog.ID).First(&paidRevenue).Error; err != nil {
		t.Fatalf("paid revenue missing: %v", err)
	}
	if paidRevenue.EffectiveRevenueMicroUSD != paidLog.ChargedCost {
		t.Fatalf("paid revenue = %d, want %d", paidRevenue.EffectiveRevenueMicroUSD, paidLog.ChargedCost)
	}

	var grantRevenue ApiLogRevenue
	if err := db.Where("api_log_id = ?", grantLog.ID).First(&grantRevenue).Error; err != nil {
		t.Fatalf("grant revenue missing: %v", err)
	}
	if grantRevenue.EffectiveRevenueMicroUSD != 0 {
		t.Fatalf("granted subscription revenue = %d, want 0", grantRevenue.EffectiveRevenueMicroUSD)
	}
	if grantRevenue.SubscriptionID != grantSub.ID {
		t.Fatalf("grant revenue sub id = %d, want %d", grantRevenue.SubscriptionID, grantSub.ID)
	}
}
