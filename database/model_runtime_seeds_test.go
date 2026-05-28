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
	// UX fix（2026-05-21）：默认 channel 之前 seed 为 Status=2 是早期"小心放量"兜底，
	// 但前端 ChannelManagement 没有 channel 级 status 开关 —— 导致 admin 也无法启用，
	// 整个 /pricing 永远空。channel 是"上游网关连通性"标志，模型的真正 gating 在
	// channel_model.status 上（媒体仍默认 status=2），channel 应 default-enabled。
	if ch.Status != 1 || ch.Type != "cliproxy" || ch.BaseURL != "http://127.0.0.1:8317" {
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

	var opus48 ChannelModel
	if err := DB.Where("channel_id = ? AND model_id = ?", ch.ID, "claude-opus-4-8").First(&opus48).Error; err != nil {
		t.Fatalf("load opus 4.8 seed: %v", err)
	}
	if opus48.InputPricePicoPerToken != 5*PicoPerTokenPerUSDPerMTok ||
		opus48.OutputPricePicoPerToken != 25*PicoPerTokenPerUSDPerMTok ||
		opus48.CachedInputPricePicoPerToken != PicoPerTokenPerUSDPerMTok/2 ||
		opus48.CacheWriteInputPricePicoPerToken != 25*PicoPerTokenPerUSDPerMTok/4 ||
		opus48.CacheWrite1hInputPricePicoPerToken != 10*PicoPerTokenPerUSDPerMTok {
		t.Fatalf("unexpected opus 4.8 pricing: %#v", opus48)
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
	// 2026-05-19 业务决策：图片/视频 catalog 默认 Supported=false / Public=false，
	// handler / pricing 代码仍保留，admin 后续如要重启在 admin UI 切回 Supported=true。
	if imageCatalog.Supported || imageCatalog.Public || imageCatalog.Category != ModelCategoryImage {
		t.Fatalf("gpt-image-2 catalog must be Supported=false + Public=false: %#v", imageCatalog)
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
	if videoCatalog.Supported || videoCatalog.Public || videoCatalog.Category != ModelCategoryVideo {
		t.Fatalf("grok-imagine-video catalog must be Supported=false + Public=false: %#v", videoCatalog)
	}

	// Gemini image runtime via CPA antigravity 路径 DAOF 当前不支持，但为了 admin UI 显示
	// 完整的 CPA 暴露列表，仍 seed catalog row——必须 Supported=false 且 DefaultEnabled=false，
	// admin 启用前会被 ValidateChannelModelActivation 拒绝（IsRuntimeImageModelSupported 返 false）。
	var geminiImageCatalogs []ModelCatalog
	if err := DB.Where("provider_key = ? AND category = ?", "google", ModelCategoryImage).Find(&geminiImageCatalogs).Error; err != nil {
		t.Fatalf("load gemini image catalog rows: %v", err)
	}
	for _, c := range geminiImageCatalogs {
		if c.Supported {
			t.Fatalf("gemini image catalog %q must keep Supported=false until CPA exposes via /v1/images/*", c.ModelID)
		}
		if c.DefaultEnabled {
			t.Fatalf("gemini image catalog %q must keep DefaultEnabled=false", c.ModelID)
		}
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

func TestSeedModelRuntimeDefaults_BackfillsOpus48ZeroPrice(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })

	if err := DB.AutoMigrate(&Channel{}, &ChannelModel{}, &ModelCatalog{}, &ModelPricingRule{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ch := Channel{
		Type:    "cliproxy",
		Name:    "CLIProxyAPI Local",
		BaseURL: "http://127.0.0.1:8317",
		Weight:  1,
		Status:  1,
	}
	if err := DB.Create(&ch).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := DB.Create(&ChannelModel{
		ChannelID:        ch.ID,
		ModelID:          "claude-opus-4-8",
		DisplayName:      "claude-opus-4-8",
		ModelCategory:    ModelCategoryText,
		BillingMode:      BillingModeToken,
		AllowedEndpoints: "",
		Weight:           1,
		Status:           2,
	}).Error; err != nil {
		t.Fatalf("seed zero-priced opus 4.8: %v", err)
	}

	SeedModelRuntimeDefaults()

	var cm ChannelModel
	if err := DB.Where("channel_id = ? AND model_id = ?", ch.ID, "claude-opus-4-8").First(&cm).Error; err != nil {
		t.Fatalf("load opus 4.8 channel model: %v", err)
	}
	if cm.Status != 2 {
		t.Fatalf("backfill should not auto-enable existing opus 4.8 row: %#v", cm)
	}
	if cm.InputPricePicoPerToken != 5*PicoPerTokenPerUSDPerMTok ||
		cm.OutputPricePicoPerToken != 25*PicoPerTokenPerUSDPerMTok ||
		cm.CachedInputPricePicoPerToken != PicoPerTokenPerUSDPerMTok/2 ||
		cm.CacheWriteInputPricePicoPerToken != 25*PicoPerTokenPerUSDPerMTok/4 ||
		cm.CacheWrite1hInputPricePicoPerToken != 10*PicoPerTokenPerUSDPerMTok {
		t.Fatalf("opus 4.8 zero price was not backfilled: %#v", cm)
	}

	var cat ModelCatalog
	if err := DB.Where("model_id = ?", "claude-opus-4-8").First(&cat).Error; err != nil {
		t.Fatalf("load opus 4.8 catalog: %v", err)
	}
	if !cat.Supported || !cat.Public || !cat.DefaultEnabled || cat.OfficialStatus != "official_exact" {
		t.Fatalf("unexpected opus 4.8 catalog: %#v", cat)
	}

	var rule ModelPricingRule
	if err := DB.Where("model_id = ? AND unit = ?", "claude-opus-4-8", "token").First(&rule).Error; err != nil {
		t.Fatalf("load opus 4.8 pricing rule: %v", err)
	}
	if rule.InputPricePicoPerToken != 5*PicoPerTokenPerUSDPerMTok ||
		rule.OutputPricePicoPerToken != 25*PicoPerTokenPerUSDPerMTok ||
		rule.CachedInputPricePicoPerToken != PicoPerTokenPerUSDPerMTok/2 ||
		rule.CacheWriteInputPricePicoPerToken != 25*PicoPerTokenPerUSDPerMTok/4 ||
		rule.CacheWrite1hInputPricePicoPerToken != 10*PicoPerTokenPerUSDPerMTok {
		t.Fatalf("unexpected opus 4.8 pricing rule: %#v", rule)
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

	// fix H3：单独的 input 价不足以激活，必须 input + output 都 >0（每个文本请求
	// 都会有 input+output token，缺一就会零成本）。
	text.InputPricePicoPerToken = PicoPerTokenPerUSDPerMTok
	if err := ValidateChannelModelActivation(&text); err != ErrTextModelRequiresTokenPricing {
		t.Fatalf("text with only InputPrice should still fail: %v", err)
	}
	text.OutputPricePicoPerToken = PicoPerTokenPerUSDPerMTok
	if err := ValidateChannelModelActivation(&text); err != nil {
		t.Fatalf("text with input+output priced should pass: %v", err)
	}
}

func TestSeedModelRuntimeDefaults_TotalCount(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	if err := DB.AutoMigrate(&Channel{}, &ChannelModel{}, &ModelCatalog{}, &ModelPricingRule{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	SeedModelRuntimeDefaults()

	// DAOF seed 总数 = 45（2026-05-28 对齐 CPA /v1/models 实际暴露列表）：
	//   - Anthropic 12 / OpenAI 7 / Google 17 / xAI 9
	//   - 含已确认 pricing 的内置 + 11 个 alias_or_unofficial（admin 启用前
	//     必须手填 pricing 并切 Supported=true）
	//
	// 调整该常数前先用 `curl http://127.0.0.1:8317/v1/models` 核对 CPA 当前
	// 暴露的模型清单——seed 必须是 CPA 列表的精确镜像，不能有 DAOF 单方面
	// 多出来 / 少掉的。Moonshot Kimi / Imagen 全系列 / Gemini *-latest alias
	// / claude-opus-4-6-thinking / gpt-5.3-codex-spark / gpt-oss-120b-medium
	// 已从 seed 移除（CPA 当前不暴露），handler / pricing / calibration
	// 代码仍保留——admin 后续如有需求可在 admin UI 手动配回。
	const expectedSeedCount = int64(45)
	var got int64
	if err := DB.Model(&ModelCatalog{}).Count(&got).Error; err != nil {
		t.Fatalf("count catalog: %v", err)
	}
	if got != expectedSeedCount {
		t.Fatalf("seed catalog count=%d want %d", got, expectedSeedCount)
	}

	// 锁定 11 个 alias_or_unofficial 模型必须 Supported=false + DefaultEnabled=false +
	// OfficialStatus=alias_or_unofficial（admin 启用前手动确认 pricing + 切 Supported=true）。
	// 2026-05-19 调整：对齐 CPA /v1/models 实际暴露，移除 CPA 不再暴露的 alias，新增
	// CPA 新出现的 antigravity alias (gemini-3.5-flash-low / gemini-3-flash-agent)。
	// 2026-05-20 增量：gemini-3.5-flash 官方 pricing 已查实 → 已搬到 official_exact 启用区，
	// 不再归 uncommitted。
	uncommitted := []string{
		// Gemini text alias (CPA antigravity 路径暴露)
		"gemini-3-flash", "gemini-3-flash-agent",
		"gemini-3-pro-low", "gemini-3-pro-high", "gemini-3-pro-preview",
		"gemini-3.1-pro-low", "gemini-3.5-flash-low",
		"gemini-pro-agent",
		// Gemini image (CPA antigravity 路径，DAOF /v1beta/models 接通)
		"gemini-3.1-flash-image",
		// xAI alias (CPA registry 暴露但 docs.x.ai 当前列表不含)
		"grok-3-mini", "grok-3-mini-fast",
	}
	for _, id := range uncommitted {
		var cat ModelCatalog
		if err := DB.Where("model_id = ?", id).First(&cat).Error; err != nil {
			t.Fatalf("alias model %q missing from seed: %v", id, err)
		}
		if cat.Supported {
			t.Fatalf("alias model %q must keep Supported=false until pricing is confirmed", id)
		}
		if cat.DefaultEnabled {
			t.Fatalf("alias model %q must keep DefaultEnabled=false", id)
		}
		if cat.OfficialStatus != "alias_or_unofficial" {
			t.Fatalf("alias model %q official_status=%q want alias_or_unofficial", id, cat.OfficialStatus)
		}
	}

	// 2026-05-19 业务决策：当前业务全部聚焦 text，全部 image/video catalog 必须
	// Supported=false（admin UI 不展示为"已支持"，handler 代码仍保留以便后续切回）。
	var mediaCatalogs []ModelCatalog
	if err := DB.Where("category IN ?", []string{ModelCategoryImage, ModelCategoryVideo}).Find(&mediaCatalogs).Error; err != nil {
		t.Fatalf("load image/video catalogs: %v", err)
	}
	for _, c := range mediaCatalogs {
		if c.Supported {
			t.Fatalf("image/video catalog %q must be Supported=false (当前业务聚焦 text): %+v", c.ModelID, c)
		}
		if c.DefaultEnabled {
			t.Fatalf("image/video catalog %q must be DefaultEnabled=false: %+v", c.ModelID, c)
		}
	}

	// 锁定最终启用/停用分布：29 启用 + 16 停用（catalog Supported=true count = enabled）。
	// 任何一次 seed 调整都应让这条断言保持，否则就要解释为什么变。
	var enabledChannelCount int64
	if err := DB.Model(&ChannelModel{}).Where("status = ?", 1).Count(&enabledChannelCount).Error; err != nil {
		t.Fatalf("count enabled channel_models: %v", err)
	}
	if enabledChannelCount != 29 {
		t.Fatalf("enabled channel_models=%d want 29 (text+price 全启用，image/video+无价 alias 全停用)", enabledChannelCount)
	}
	var disabledChannelCount int64
	if err := DB.Model(&ChannelModel{}).Where("status = ?", 2).Count(&disabledChannelCount).Error; err != nil {
		t.Fatalf("count disabled channel_models: %v", err)
	}
	if disabledChannelCount != 16 {
		t.Fatalf("disabled channel_models=%d want 16", disabledChannelCount)
	}
}

func TestValidateChannelModelActivation_EditsRequiresInputPricing(t *testing.T) {
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

	cm := &ChannelModel{
		ModelID:          "grok-imagine-image-quality",
		Status:           1,
		ModelCategory:    ModelCategoryImage,
		BillingMode:      BillingModeImage,
		AllowedEndpoints: `["/v1/images/generations","/v1/images/edits"]`,
	}
	// 仅 output pricing：edits 启用应被拒绝
	if err := DB.Create(&ModelPricingRule{
		RuleKey: "test-out-1k", PricingVersion: "test",
		ProviderKey: "xai", ModelID: cm.ModelID, OfficialModelID: cm.ModelID,
		BillingMode: BillingModeImage, Unit: "image", Direction: "output",
		Resolution: "1K", PriceMicroUSD: 50_000,
	}).Error; err != nil {
		t.Fatalf("seed output pricing: %v", err)
	}
	if err := ValidateChannelModelActivation(cm); err != ErrImageEditMissingInputPricing {
		t.Fatalf("err=%v want ErrImageEditMissingInputPricing", err)
	}

	// 加 input pricing 后激活成功
	if err := DB.Create(&ModelPricingRule{
		RuleKey: "test-in", PricingVersion: "test",
		ProviderKey: "xai", ModelID: cm.ModelID, OfficialModelID: cm.ModelID,
		BillingMode: BillingModeImage, Unit: "image", Direction: "input",
		PriceMicroUSD: 10_000,
	}).Error; err != nil {
		t.Fatalf("seed input pricing: %v", err)
	}
	if err := ValidateChannelModelActivation(cm); err != nil {
		t.Fatalf("activation with input+output pricing should pass: %v", err)
	}

	// 仅 generations 时不要求 input pricing：移除 input rule，预期仍激活成功
	if err := DB.Where("rule_key = ?", "test-in").Delete(&ModelPricingRule{}).Error; err != nil {
		t.Fatalf("delete input pricing: %v", err)
	}
	cm.AllowedEndpoints = `["/v1/images/generations"]`
	if err := ValidateChannelModelActivation(cm); err != nil {
		t.Fatalf("generations-only activation without input pricing should still pass: %v", err)
	}
}

func TestValidateChannelModelActivation_RejectsForeignImageEndpoint(t *testing.T) {
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
	if err := DB.Create(&ModelPricingRule{
		RuleKey: "test-out", PricingVersion: "test",
		ProviderKey: "xai", ModelID: "grok-imagine-image-quality", OfficialModelID: "grok-imagine-image-quality",
		BillingMode: BillingModeImage, Unit: "image", Direction: "output",
		Resolution: "1K", PriceMicroUSD: 50_000,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	cm := &ChannelModel{
		ModelID:          "grok-imagine-image-quality",
		Status:           1,
		ModelCategory:    ModelCategoryImage,
		BillingMode:      BillingModeImage,
		AllowedEndpoints: `["/v1/images/generations","/v1/images/extensions"]`,
	}
	if err := ValidateChannelModelActivation(cm); err != ErrImageModelRequiresEndpoint {
		t.Fatalf("err=%v want ErrImageModelRequiresEndpoint (extensions not in image whitelist)", err)
	}
}

func TestValidateChannelModelActivation_VideoEditsRequiresInputPricing(t *testing.T) {
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

	cm := &ChannelModel{
		ModelID:          "grok-imagine-video",
		Status:           1,
		ModelCategory:    ModelCategoryVideo,
		BillingMode:      BillingModeVideoSecond,
		AllowedEndpoints: `["/v1/videos/generations","/v1/videos/edits"]`,
	}
	if err := DB.Create(&ModelPricingRule{
		RuleKey: "video-test-out", PricingVersion: "test",
		ProviderKey: "xai", ModelID: cm.ModelID, OfficialModelID: cm.ModelID,
		BillingMode: BillingModeVideoSecond, Unit: "video_second", Direction: "output",
		Resolution: "720p", PriceMicroUSD: 70_000,
	}).Error; err != nil {
		t.Fatalf("seed output pricing: %v", err)
	}
	if err := ValidateChannelModelActivation(cm); err != ErrVideoEditMissingInputPricing {
		t.Fatalf("err=%v want ErrVideoEditMissingInputPricing", err)
	}
	// 加 input pricing 后 OK
	if err := DB.Create(&ModelPricingRule{
		RuleKey: "video-test-in", PricingVersion: "test",
		ProviderKey: "xai", ModelID: cm.ModelID, OfficialModelID: cm.ModelID,
		BillingMode: BillingModeVideoSecond, Unit: "video_second", Direction: "input",
		PriceMicroUSD: 10_000,
	}).Error; err != nil {
		t.Fatalf("seed input pricing: %v", err)
	}
	if err := ValidateChannelModelActivation(cm); err != nil {
		t.Fatalf("activation with both directions should pass: %v", err)
	}
	// extensions 同样要求 input pricing
	if err := DB.Where("rule_key = ?", "video-test-in").Delete(&ModelPricingRule{}).Error; err != nil {
		t.Fatalf("delete input pricing: %v", err)
	}
	cm.AllowedEndpoints = `["/v1/videos/generations","/v1/videos/extensions"]`
	if err := ValidateChannelModelActivation(cm); err != ErrVideoEditMissingInputPricing {
		t.Fatalf("extensions also requires input pricing: err=%v", err)
	}
}

func TestCanonicalRuntimeImageModel_AcceptsAdminRegisteredThirdParty(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	if err := DB.AutoMigrate(&ModelCatalog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 1. 未注册：拒绝
	if _, ok := CanonicalRuntimeImageModel("sd-3.5-large"); ok {
		t.Fatalf("unregistered model should be rejected before admin adds catalog row")
	}

	// 2. admin 加 catalog row（Supported=true）：接受
	if err := DB.Create(&ModelCatalog{
		ProviderKey: "fal", ProviderName: "fal.ai",
		ModelID: "sd-3.5-large", DisplayName: "Stable Diffusion 3.5 Large",
		Category: ModelCategoryImage, BillingMode: BillingModeImage,
		Supported: true,
	}).Error; err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	got, ok := CanonicalRuntimeImageModel("sd-3.5-large")
	if !ok || got != "sd-3.5-large" {
		t.Fatalf("CanonicalRuntimeImageModel=(%q,%v) want sd-3.5-large/true after admin registration", got, ok)
	}

	// 3. 客户端带前缀也能命中（剥前缀查 base）
	got, ok = CanonicalRuntimeImageModel("fal/sd-3.5-large")
	if !ok || got != "sd-3.5-large" {
		t.Fatalf("prefixed client model lookup=(%q,%v) want sd-3.5-large/true", got, ok)
	}

	// 4. admin 直接用带前缀注册也支持
	if err := DB.Create(&ModelCatalog{
		ProviderKey: "replicate", ProviderName: "Replicate",
		ModelID: "replicate/flux-1.1", DisplayName: "FLUX 1.1",
		Category: ModelCategoryImage, BillingMode: BillingModeImage,
		Supported: true,
	}).Error; err != nil {
		t.Fatalf("seed prefixed catalog: %v", err)
	}
	got, ok = CanonicalRuntimeImageModel("replicate/flux-1.1")
	if !ok || got != "replicate/flux-1.1" {
		t.Fatalf("admin-registered prefixed model=(%q,%v) want replicate/flux-1.1/true", got, ok)
	}

	// 5. Supported=false：仍拒绝（admin 软关 model 时立即生效）
	if err := DB.Create(&ModelCatalog{
		ProviderKey: "fal", ProviderName: "fal.ai",
		ModelID: "unsupported-model", DisplayName: "Disabled",
		Category: ModelCategoryImage, BillingMode: BillingModeImage,
		Supported: false,
	}).Error; err != nil {
		t.Fatalf("seed unsupported: %v", err)
	}
	if _, ok := CanonicalRuntimeImageModel("unsupported-model"); ok {
		t.Fatalf("Supported=false catalog row should be rejected")
	}

	// 6. 静态白名单仍优先（fast path）— 内置模型即使不在 DB 也工作
	if _, ok := CanonicalRuntimeImageModel("gpt-image-2"); !ok {
		t.Fatalf("static built-in gpt-image-2 must work even without catalog row")
	}
}

func TestIsRuntimeTokenBilledImageModel_RespectsAdminBillingMode(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })
	if err := DB.AutoMigrate(&ModelCatalog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 内置 gpt-image-2 → token-billed（static fast path，即使无 DB row）
	if !IsRuntimeTokenBilledImageModel("gpt-image-2") {
		t.Fatalf("built-in gpt-image-2 must be token-billed via static fast path")
	}

	// admin 注册 token-billed 第三方 image model
	if err := DB.Create(&ModelCatalog{
		ProviderKey: "openai", ProviderName: "OpenAI compat",
		ModelID: "gpt-image-3-preview", DisplayName: "GPT Image 3 Preview",
		Category: ModelCategoryImage, BillingMode: BillingModeToken,
		Supported: true,
	}).Error; err != nil {
		t.Fatalf("seed token-billed: %v", err)
	}
	if !IsRuntimeTokenBilledImageModel("gpt-image-3-preview") {
		t.Fatalf("admin-registered token-billed model should be detected via DB")
	}

	// admin 注册 image-billed（per-image）第三方
	if err := DB.Create(&ModelCatalog{
		ProviderKey: "fal", ProviderName: "fal.ai",
		ModelID: "sdxl", DisplayName: "SDXL",
		Category: ModelCategoryImage, BillingMode: BillingModeImage,
		Supported: true,
	}).Error; err != nil {
		t.Fatalf("seed image-billed: %v", err)
	}
	if IsRuntimeTokenBilledImageModel("sdxl") {
		t.Fatalf("admin-registered image-billed model must NOT report token-billed")
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
