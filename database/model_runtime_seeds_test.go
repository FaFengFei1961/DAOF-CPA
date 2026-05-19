package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSeedModelRuntimeDefaults_ReproducibleFactoryModelPool(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })

	if err := DB.AutoMigrate(&Channel{}, &ChannelModel{}, &ModelCatalog{}, &ModelPricingRule{}, &ApiLogUsageLine{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	SeedModelRuntimeDefaults()
	SeedModelRuntimeDefaults()

	var channelCount int64
	if err := DB.Model(&Channel{}).Where("name = ?", "CLIProxyAPI Local").Count(&channelCount).Error; err != nil {
		t.Fatalf("count channel: %v", err)
	}
	if channelCount != 1 {
		t.Fatalf("default channel count=%d want 1", channelCount)
	}

	var ch Channel
	if err := DB.Where("name = ?", "CLIProxyAPI Local").First(&ch).Error; err != nil {
		t.Fatalf("load default channel: %v", err)
	}
	if ch.Status != 2 || ch.Type != "cliproxy" || ch.BaseURL != "http://127.0.0.1:8317" {
		t.Fatalf("unexpected default channel: %#v", ch)
	}

	var gpt ChannelModel
	if err := DB.Where("channel_id = ? AND model_id = ?", ch.ID, "gpt-5.5").First(&gpt).Error; err != nil {
		t.Fatalf("load gpt seed: %v", err)
	}
	if gpt.Status != 1 || gpt.ModelCategory != ModelCategoryText || gpt.BillingMode != BillingModeToken {
		t.Fatalf("unexpected gpt runtime metadata: %#v", gpt)
	}
	if gpt.InputPricePicoPerToken != 5*PicoPerTokenPerUSDPerMTok ||
		gpt.OutputPricePicoPerToken != 30*PicoPerTokenPerUSDPerMTok ||
		gpt.EndpointPolicy != EndpointPolicyNoChatNonStream {
		t.Fatalf("unexpected gpt pricing/policy: %#v", gpt)
	}

	var opus41 ChannelModel
	if err := DB.Where("channel_id = ? AND model_id = ?", ch.ID, "claude-opus-4-1-20250805").First(&opus41).Error; err != nil {
		t.Fatalf("load opus 4.1 seed: %v", err)
	}
	if opus41.InputPricePicoPerToken != 15*PicoPerTokenPerUSDPerMTok ||
		opus41.OutputPricePicoPerToken != 75*PicoPerTokenPerUSDPerMTok ||
		opus41.CachedInputPricePicoPerToken != 1500*PicoPerTokenPerUSDPerMTok/1000 {
		t.Fatalf("unexpected opus 4.1 legacy pricing: %#v", opus41)
	}

	var grok ChannelModel
	if err := DB.Where("channel_id = ? AND model_id = ?", ch.ID, "grok-4.3").First(&grok).Error; err != nil {
		t.Fatalf("load grok seed: %v", err)
	}
	if grok.ContextPriceThreshold != 0 || grok.HighInputPricePicoPerToken != 0 || grok.HighOutputPricePicoPerToken != 0 {
		t.Fatalf("xAI seed should not invent a high-context tier without public exact prices: %#v", grok)
	}

	var image ChannelModel
	if err := DB.Where("channel_id = ? AND model_id = ?", ch.ID, "gpt-image-2").First(&image).Error; err != nil {
		t.Fatalf("load image seed: %v", err)
	}
	if image.Status != 2 || image.ModelCategory != ModelCategoryImage || image.BillingMode != BillingModeToken {
		t.Fatalf("token-billed image model should be preseeded disabled and token-billed: %#v", image)
	}
	var imageCatalog ModelCatalog
	if err := DB.Where("model_id = ?", "gpt-image-2").First(&imageCatalog).Error; err != nil {
		t.Fatalf("load gpt image catalog: %v", err)
	}
	if !imageCatalog.Supported || !imageCatalog.Public || imageCatalog.Category != ModelCategoryImage {
		t.Fatalf("gpt-image-2 catalog should now be supported/public: %#v", imageCatalog)
	}

	var xaiImage ChannelModel
	if err := DB.Where("channel_id = ? AND model_id = ?", ch.ID, "grok-imagine-image").First(&xaiImage).Error; err != nil {
		t.Fatalf("load xai image seed: %v", err)
	}
	if xaiImage.Status != 2 || xaiImage.ModelCategory != ModelCategoryImage || xaiImage.BillingMode != BillingModeImage {
		t.Fatalf("xAI image model should be preseeded disabled and image-billed: %#v", xaiImage)
	}

	var video ChannelModel
	if err := DB.Where("channel_id = ? AND model_id = ?", ch.ID, "grok-imagine-video").First(&video).Error; err != nil {
		t.Fatalf("load video seed: %v", err)
	}
	if video.Status != 2 || video.ModelCategory != ModelCategoryVideo || video.BillingMode != BillingModeVideoSecond {
		t.Fatalf("video model should be preseeded disabled and video-second billed: %#v", video)
	}
	if video.AllowedEndpoints != `["/v1/videos/generations"]` {
		t.Fatalf("video default endpoint too broad: %q", video.AllowedEndpoints)
	}
	var videoCatalog ModelCatalog
	if err := DB.Where("model_id = ?", "grok-imagine-video").First(&videoCatalog).Error; err != nil {
		t.Fatalf("load video catalog: %v", err)
	}
	if !videoCatalog.Supported || !videoCatalog.Public || videoCatalog.Category != ModelCategoryVideo {
		t.Fatalf("video catalog should now be supported/public: %#v", videoCatalog)
	}

	// Gemini image runtime is not exposed through CLIProxyAPI's /v1/images/*
	// surface yet, so no Gemini image catalog row should be seeded.
	var geminiImageCount int64
	if err := DB.Model(&ModelCatalog{}).Where("provider_key = ? AND category = ?", "google", ModelCategoryImage).Count(&geminiImageCount).Error; err != nil {
		t.Fatalf("count gemini image catalog rows: %v", err)
	}
	if geminiImageCount != 0 {
		t.Fatalf("gemini image runtime not wired upstream; should not be seeded (got %d rows)", geminiImageCount)
	}

	var pricingCount int64
	if err := DB.Model(&ModelPricingRule{}).Where("pricing_version = ?", DefaultModelPricingVersion).Count(&pricingCount).Error; err != nil {
		t.Fatalf("count pricing rules: %v", err)
	}
	if pricingCount == 0 {
		t.Fatal("expected seeded pricing rules")
	}
	var distinctRuleKeys int64
	if err := DB.Model(&ModelPricingRule{}).Distinct("rule_key").Count(&distinctRuleKeys).Error; err != nil {
		t.Fatalf("count distinct pricing rule keys: %v", err)
	}
	if pricingCount != distinctRuleKeys {
		t.Fatalf("pricing seed should be idempotent: rows=%d distinct_rule_keys=%d", pricingCount, distinctRuleKeys)
	}

	var codexRule ModelPricingRule
	if err := DB.Where("model_id = ? AND unit = ?", "gpt-5.3-codex", "token").First(&codexRule).Error; err != nil {
		t.Fatalf("load codex pricing rule: %v", err)
	}
	if codexRule.OfficialStatus != "official_exact" || codexRule.InputPricePicoPerToken != 1750*PicoPerTokenPerUSDPerMTok/1000 {
		t.Fatalf("unexpected codex pricing rule: %#v", codexRule)
	}
}

func TestValidateChannelModelActivation_BlocksUnsupportedMediaAndUnpricedText(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	if err := DB.AutoMigrate(&ModelPricingRule{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	image := ChannelModel{ModelID: "gpt-image-2", BillingMode: BillingModeToken, Status: 1}
	if err := ValidateChannelModelActivation(&image); err != ErrImageModelRequiresPricing {
		t.Fatalf("token image activation err=%v want %v", err, ErrImageModelRequiresPricing)
	}
	image.InputPricePicoPerToken = 5 * PicoPerTokenPerUSDPerMTok
	image.OutputPricePicoPerToken = 30 * PicoPerTokenPerUSDPerMTok
	image.CachedInputPricePicoPerToken = 125 * PicoPerTokenPerUSDPerMTok / 100
	if err := ValidateChannelModelActivation(&image); err != nil {
		t.Fatalf("priced token image activation should pass: %v", err)
	}

	xaiImage := ChannelModel{ModelID: "grok-imagine-image", Status: 1}
	if err := ValidateChannelModelActivation(&xaiImage); err != ErrImageModelRequiresPricing {
		t.Fatalf("unpriced xai image activation err=%v want %v", err, ErrImageModelRequiresPricing)
	}
	if err := DB.Create(&ModelPricingRule{
		RuleKey:         "test|grok-imagine-image|image|output|1k",
		PricingVersion:  "test",
		ProviderKey:     "xai",
		ModelID:         "grok-imagine-image",
		OfficialModelID: "grok-imagine-image",
		BillingMode:     BillingModeImage,
		Unit:            "image",
		Direction:       "output",
		Resolution:      "1K",
		PriceMicroUSD:   20_000,
	}).Error; err != nil {
		t.Fatalf("seed image pricing: %v", err)
	}
	if err := ValidateChannelModelActivation(&xaiImage); err != nil {
		t.Fatalf("priced xai image activation should pass: %v", err)
	}

	video := ChannelModel{ModelID: "grok-imagine-video", Status: 1}
	if err := ValidateChannelModelActivation(&video); err != ErrVideoModelRequiresPricing {
		t.Fatalf("unpriced video activation err=%v want %v", err, ErrVideoModelRequiresPricing)
	}
	if err := DB.Create(&ModelPricingRule{
		RuleKey:         "test|grok-imagine-video|video_second|output|720p",
		PricingVersion:  "test",
		ProviderKey:     "xai",
		ModelID:         "grok-imagine-video",
		OfficialModelID: "grok-imagine-video",
		BillingMode:     BillingModeVideoSecond,
		Unit:            "video_second",
		Direction:       "output",
		Resolution:      "720p",
		PriceMicroUSD:   70_000,
	}).Error; err != nil {
		t.Fatalf("seed video pricing: %v", err)
	}
	if err := ValidateChannelModelActivation(&video); err != nil {
		t.Fatalf("priced video activation should pass: %v", err)
	}

	text := ChannelModel{ModelID: "gpt-5.5", Status: 1}
	if err := ValidateChannelModelActivation(&text); err != ErrTextModelRequiresTokenPricing {
		t.Fatalf("unpriced text activation err=%v want %v", err, ErrTextModelRequiresTokenPricing)
	}

	text.InputPricePicoPerToken = PicoPerTokenPerUSDPerMTok
	if err := ValidateChannelModelActivation(&text); err != nil {
		t.Fatalf("priced text activation should pass: %v", err)
	}
}

func TestCanonicalRuntimeImageModel_AllowsCLIProxyPrefixes(t *testing.T) {
	for _, in := range []string{
		"grok-imagine-image",
		"xai/grok-imagine-image",
		"x-ai/grok-imagine-image",
		"grok/grok-imagine-image",
	} {
		got, ok := CanonicalRuntimeImageModel(in)
		if !ok || got != "grok-imagine-image" {
			t.Fatalf("CanonicalRuntimeImageModel(%q)=(%q,%v), want grok-imagine-image,true", in, got, ok)
		}
	}
	if got, ok := CanonicalRuntimeImageModel("codex/grok-imagine-image"); ok || got != "" {
		t.Fatalf("codex prefix should be rejected, got (%q,%v)", got, ok)
	}
	for _, in := range []string{"gpt-image-2", "openai/gpt-image-2"} {
		got, ok := CanonicalRuntimeImageModel(in)
		if !ok || got != "gpt-image-2" {
			t.Fatalf("CanonicalRuntimeImageModel(%q)=(%q,%v), want gpt-image-2,true", in, got, ok)
		}
	}
	if got, ok := CanonicalRuntimeImageModel("codex/gpt-image-2"); ok || got != "" {
		t.Fatalf("codex-prefixed gpt-image-2 should be rejected, got (%q,%v)", got, ok)
	}
}

func TestCanonicalRuntimeVideoModel_AllowsCLIProxyPrefixes(t *testing.T) {
	for _, in := range []string{
		"grok-imagine-video",
		"xai/grok-imagine-video",
		"x-ai/grok-imagine-video",
		"grok/grok-imagine-video",
	} {
		got, ok := CanonicalRuntimeVideoModel(in)
		if !ok || got != "grok-imagine-video" {
			t.Fatalf("CanonicalRuntimeVideoModel(%q)=(%q,%v), want grok-imagine-video,true", in, got, ok)
		}
	}
	if got, ok := CanonicalRuntimeVideoModel("codex/grok-imagine-video"); ok || got != "" {
		t.Fatalf("codex prefix should be rejected, got (%q,%v)", got, ok)
	}
}
