package database

import (
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const DefaultModelPricingVersion = "official-2026-05-18"

type defaultModelSeed struct {
	ProviderKey     string
	ProviderName    string
	ModelID         string
	DisplayName     string
	OfficialModelID string
	OfficialStatus  string
	Category        string
	BillingMode     string
	Supported       bool
	Public          bool
	DefaultEnabled  bool
	SourceURL       string
	Notes           string
	Token           defaultTokenPrice
	Media           []defaultMediaPrice
	MaxContext      int
	EndpointPolicy  string
}

type defaultTokenPrice struct {
	Input      string
	Output     string
	Cached     string
	CacheWrite string
	Cache1h    string
	HighAt     int
	HighInput  string
	HighCache  string
	HighOutput string
}

type defaultMediaPrice struct {
	Unit        string
	Direction   string
	Quality     string
	Size        string
	Resolution  string
	AspectRatio string
	Price       string
	Notes       string
}

func SeedModelRuntimeDefaults() {
	if DB == nil {
		return
	}
	createdCatalog := int64(0)
	createdPricing := int64(0)
	createdChannels := int64(0)
	createdBindings := int64(0)
	if err := DB.Transaction(func(tx *gorm.DB) error {
		for _, spec := range defaultModelSeeds() {
			catalog := ModelCatalog{
				ProviderKey:     spec.ProviderKey,
				ProviderName:    spec.ProviderName,
				ModelID:         spec.ModelID,
				DisplayName:     spec.DisplayName,
				OfficialModelID: firstNonEmpty(spec.OfficialModelID, spec.ModelID),
				OfficialStatus:  firstNonEmpty(spec.OfficialStatus, "official_exact"),
				Category:        NormalizeModelCategory(spec.Category, spec.ModelID),
				BillingMode:     NormalizeBillingMode(spec.BillingMode, spec.Category),
				Supported:       spec.Supported,
				Public:          spec.Public,
				DefaultEnabled:  spec.DefaultEnabled,
				SourceURL:       spec.SourceURL,
				Notes:           spec.Notes,
			}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&catalog)
			if res.Error != nil {
				return fmt.Errorf("seed model_catalog %s: %w", spec.ModelID, res.Error)
			}
			createdCatalog += res.RowsAffected

			rules := pricingRulesForSpec(spec)
			for i := range rules {
				if err := ValidateModelPricingRule(&rules[i]); err != nil {
					return fmt.Errorf("invalid pricing rule %s/%s/%s: %w", spec.ModelID, rules[i].Unit, rules[i].Direction, err)
				}
			}
			if len(rules) > 0 {
				res = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rules)
				if res.Error != nil {
					return fmt.Errorf("seed model_pricing_rules %s: %w", spec.ModelID, res.Error)
				}
				createdPricing += res.RowsAffected
			}
		}

		ch, madeChannel, err := firstOrCreateDefaultCLIProxyChannel(tx)
		if err != nil {
			return err
		}
		if madeChannel {
			createdChannels++
		}
		for _, spec := range defaultModelSeeds() {
			made, err := firstOrCreateDefaultChannelModel(tx, ch.ID, spec)
			if err != nil {
				return err
			}
			if made {
				createdBindings++
			}
		}
		return backfillChannelModelRuntimeMetadata(tx)
	}); err != nil {
		log.Printf("[MODEL-SEED] default runtime seed failed: %v", err)
		return
	}
	if createdCatalog > 0 || createdPricing > 0 || createdChannels > 0 || createdBindings > 0 {
		log.Printf("🌱 模型运行时默认值：新增 catalog=%d pricing=%d channels=%d bindings=%d",
			createdCatalog, createdPricing, createdChannels, createdBindings)
	}
}

func firstOrCreateDefaultCLIProxyChannel(tx *gorm.DB) (Channel, bool, error) {
	var ch Channel
	res := tx.Where("name = ?", "CLIProxyAPI Local").First(&ch)
	if res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		return ch, false, fmt.Errorf("load default cliproxy channel: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		return ch, false, nil
	}
	ch = Channel{
		Type:    "cliproxy",
		Name:    "CLIProxyAPI Local",
		Key:     "",
		BaseURL: "http://127.0.0.1:8317",
		Weight:  1,
		Status:  2,
	}
	if err := tx.Create(&ch).Error; err != nil {
		return ch, false, fmt.Errorf("create default cliproxy channel: %w", err)
	}
	return ch, true, nil
}

func firstOrCreateDefaultChannelModel(tx *gorm.DB, channelID uint, spec defaultModelSeed) (bool, error) {
	var existing ChannelModel
	res := tx.Where("channel_id = ? AND model_id = ?", channelID, spec.ModelID).First(&existing)
	if res.Error != nil && !errors.Is(res.Error, gorm.ErrRecordNotFound) {
		return false, fmt.Errorf("load default channel_model %s: %w", spec.ModelID, res.Error)
	}
	if res.RowsAffected > 0 {
		return false, nil
	}
	cm := ChannelModel{
		ChannelID:          channelID,
		ModelID:            spec.ModelID,
		DisplayName:        firstNonEmpty(spec.DisplayName, spec.ModelID),
		OfficialModelID:    firstNonEmpty(spec.OfficialModelID, spec.ModelID),
		ModelCategory:      NormalizeModelCategory(spec.Category, spec.ModelID),
		BillingMode:        NormalizeBillingMode(spec.BillingMode, spec.Category),
		AllowedEndpoints:   DefaultAllowedEndpointsForCategory(spec.Category),
		MaxContextLength:   spec.MaxContext,
		Weight:             1,
		Status:             2,
		EndpointPolicy:     firstNonEmpty(spec.EndpointPolicy, DefaultEndpointPolicyForModel(spec.ModelID, "")),
		ModerationLevel:    "off",
		ModerationFailMode: "open",
	}
	if spec.DefaultEnabled && spec.Category == ModelCategoryText {
		cm.Status = 1
	}
	if IsOpenAIGPTTextModelID(spec.ModelID) {
		cm.ModerationLevel = "moderation"
		cm.ModerationFailMode = "closed"
	}
	applyTokenPriceToChannelModel(&cm, spec.Token)
	NormalizeChannelModelMetadata(&cm)
	if err := ValidateChannelModelPricing(&cm); err != nil {
		return false, fmt.Errorf("default channel_model %s price invalid: %w", spec.ModelID, err)
	}
	if err := ValidateChannelModelActivation(&cm); err != nil {
		cm.Status = 2
	}
	if err := tx.Create(&cm).Error; err != nil {
		return false, fmt.Errorf("create default channel_model %s: %w", spec.ModelID, err)
	}
	return true, nil
}

func backfillChannelModelRuntimeMetadata(tx *gorm.DB) error {
	var rows []ChannelModel
	if err := tx.Find(&rows).Error; err != nil {
		return fmt.Errorf("load channel_models for metadata backfill: %w", err)
	}
	specByID := map[string]defaultModelSeed{}
	for _, spec := range defaultModelSeeds() {
		specByID[spec.ModelID] = spec
	}
	for _, row := range rows {
		spec, ok := specByID[row.ModelID]
		if !ok {
			continue
		}
		update := map[string]any{}
		if strings.TrimSpace(row.OfficialModelID) == "" {
			update["official_model_id"] = firstNonEmpty(spec.OfficialModelID, spec.ModelID)
		}
		if strings.TrimSpace(row.ModelCategory) == "" {
			update["model_category"] = NormalizeModelCategory(spec.Category, spec.ModelID)
		}
		if strings.TrimSpace(row.BillingMode) == "" {
			update["billing_mode"] = NormalizeBillingMode(spec.BillingMode, spec.Category)
		}
		if strings.TrimSpace(row.AllowedEndpoints) == "" {
			update["allowed_endpoints"] = DefaultAllowedEndpointsForCategory(spec.Category)
		}
		if len(update) > 0 {
			if err := tx.Model(&ChannelModel{}).Where("id = ?", row.ID).Updates(update).Error; err != nil {
				return fmt.Errorf("backfill channel_model %d metadata: %w", row.ID, err)
			}
		}
	}
	return nil
}

func pricingRulesForSpec(spec defaultModelSeed) []ModelPricingRule {
	var out []ModelPricingRule
	officialID := firstNonEmpty(spec.OfficialModelID, spec.ModelID)
	status := firstNonEmpty(spec.OfficialStatus, "official_exact")
	if hasTokenPrice(spec.Token) {
		out = append(out, ModelPricingRule{
			RuleKey:                            pricingRuleKey(spec, "token", "", "", "", "", "", 0),
			PricingVersion:                     DefaultModelPricingVersion,
			ProviderKey:                        spec.ProviderKey,
			ModelID:                            spec.ModelID,
			OfficialModelID:                    officialID,
			OfficialStatus:                     status,
			BillingMode:                        BillingModeToken,
			Unit:                               "token",
			InputPricePicoPerToken:             mustPicoPerToken(spec.Token.Input),
			OutputPricePicoPerToken:            mustPicoPerToken(spec.Token.Output),
			CachedInputPricePicoPerToken:       mustPicoPerToken(spec.Token.Cached),
			CacheWriteInputPricePicoPerToken:   mustPicoPerToken(spec.Token.CacheWrite),
			CacheWrite1hInputPricePicoPerToken: mustPicoPerToken(spec.Token.Cache1h),
			ContextMinTokens:                   0,
			ContextMaxTokens:                   max(0, spec.Token.HighAt-1),
			SourceURL:                          spec.SourceURL,
			Notes:                              spec.Notes,
		})
		if spec.Token.HighAt > 0 && (spec.Token.HighInput != "" || spec.Token.HighOutput != "" || spec.Token.HighCache != "") {
			out = append(out, ModelPricingRule{
				RuleKey:                      pricingRuleKey(spec, "token", "long_context", "", "", "", "", spec.Token.HighAt),
				PricingVersion:               DefaultModelPricingVersion,
				ProviderKey:                  spec.ProviderKey,
				ModelID:                      spec.ModelID,
				OfficialModelID:              officialID,
				OfficialStatus:               status,
				BillingMode:                  BillingModeToken,
				Unit:                         "token",
				InputPricePicoPerToken:       mustPicoPerToken(spec.Token.HighInput),
				OutputPricePicoPerToken:      mustPicoPerToken(spec.Token.HighOutput),
				CachedInputPricePicoPerToken: mustPicoPerToken(spec.Token.HighCache),
				ContextMinTokens:             spec.Token.HighAt,
				ContextMaxTokens:             0,
				SourceURL:                    spec.SourceURL,
				Notes:                        "Long-context tier. " + spec.Notes,
			})
		}
	}
	for _, m := range spec.Media {
		out = append(out, ModelPricingRule{
			RuleKey:         pricingRuleKey(spec, firstNonEmpty(m.Unit, spec.BillingMode), m.Direction, m.Quality, m.Size, m.Resolution, m.AspectRatio, 0),
			PricingVersion:  DefaultModelPricingVersion,
			ProviderKey:     spec.ProviderKey,
			ModelID:         spec.ModelID,
			OfficialModelID: officialID,
			OfficialStatus:  status,
			BillingMode:     NormalizeBillingMode(spec.BillingMode, spec.Category),
			Unit:            firstNonEmpty(m.Unit, spec.BillingMode),
			Direction:       m.Direction,
			Quality:         m.Quality,
			Size:            m.Size,
			Resolution:      m.Resolution,
			AspectRatio:     m.AspectRatio,
			PriceMicroUSD:   mustMicroUSD(m.Price),
			SourceURL:       spec.SourceURL,
			Notes:           firstNonEmpty(m.Notes, spec.Notes),
		})
	}
	return out
}

func pricingRuleKey(spec defaultModelSeed, unit, direction, quality, size, resolution, aspectRatio string, contextMin int) string {
	parts := []string{
		DefaultModelPricingVersion,
		spec.ModelID,
		unit,
		direction,
		quality,
		size,
		resolution,
		aspectRatio,
		fmt.Sprintf("%d", contextMin),
	}
	return strings.Join(parts, "|")
}

func applyTokenPriceToChannelModel(cm *ChannelModel, p defaultTokenPrice) {
	cm.InputPricePicoPerToken = mustPicoPerToken(p.Input)
	cm.OutputPricePicoPerToken = mustPicoPerToken(p.Output)
	cm.CachedInputPricePicoPerToken = mustPicoPerToken(p.Cached)
	cm.CacheWriteInputPricePicoPerToken = mustPicoPerToken(p.CacheWrite)
	cm.CacheWrite1hInputPricePicoPerToken = mustPicoPerToken(p.Cache1h)
	cm.ContextPriceThreshold = p.HighAt
	cm.HighInputPricePicoPerToken = mustPicoPerToken(p.HighInput)
	cm.HighCachedInputPricePicoPerToken = mustPicoPerToken(p.HighCache)
	cm.HighOutputPricePicoPerToken = mustPicoPerToken(p.HighOutput)
}

func hasTokenPrice(p defaultTokenPrice) bool {
	return p.Input != "" || p.Output != "" || p.Cached != "" || p.CacheWrite != "" || p.Cache1h != "" || p.HighInput != "" || p.HighOutput != ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func mustPicoPerToken(v string) int64 {
	return mustDecimalScaled(v, PicoPerTokenPerUSDPerMTok)
}

func mustMicroUSD(v string) int64 {
	return mustDecimalScaled(v, MicroPerUSD)
}

func mustDecimalScaled(v string, scale int64) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	r, ok := new(big.Rat).SetString(v)
	if !ok {
		panic("invalid decimal seed: " + v)
	}
	r.Mul(r, big.NewRat(scale, 1))
	if !r.IsInt() {
		panic("non-integer scaled decimal seed: " + v)
	}
	return r.Num().Int64()
}

func defaultModelSeeds() []defaultModelSeed {
	anthropicPricing := "https://docs.anthropic.com/en/docs/about-claude/pricing"
	openAIDeveloperPricing := "https://developers.openai.com/api/docs/pricing"
	openAIModelGPT52 := "https://developers.openai.com/api/docs/models/gpt-5.2"
	googlePricing := "https://ai.google.dev/gemini-api/docs/pricing"
	xaiPricing := "https://docs.x.ai/developers/pricing"
	return []defaultModelSeed{
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-3-5-haiku-20241022", DisplayName: "Claude 3.5 Haiku", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "0.8", Output: "4", Cached: "0.08", CacheWrite: "1", Cache1h: "1.6"}, Notes: "Older Haiku snapshot; enabled with full official pricing."},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-3-7-sonnet-20250219", DisplayName: "Claude 3.7 Sonnet", OfficialStatus: "official_family", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "3", Output: "15", Cached: "0.3", CacheWrite: "3.75", Cache1h: "6"}, Notes: "Older Sonnet snapshot; enabled with full official pricing."},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-haiku-4-5-20251001", DisplayName: "Claude Haiku 4.5", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "1", Output: "5", Cached: "0.1", CacheWrite: "1.25", Cache1h: "2"}},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-sonnet-4-20250514", DisplayName: "Claude Sonnet 4", OfficialStatus: "official_family", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "3", Output: "15", Cached: "0.3", CacheWrite: "3.75", Cache1h: "6"}},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-sonnet-4-5-20250929", DisplayName: "Claude Sonnet 4.5", OfficialStatus: "official_family", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "3", Output: "15", Cached: "0.3", CacheWrite: "3.75", Cache1h: "6"}},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "3", Output: "15", Cached: "0.3", CacheWrite: "3.75", Cache1h: "6"}},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-opus-4-20250514", DisplayName: "Claude Opus 4", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "15", Output: "75", Cached: "1.5", CacheWrite: "18.75", Cache1h: "30"}, Notes: "Legacy Opus 4 price tier; higher than Opus 4.5+."},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-opus-4-1-20250805", DisplayName: "Claude Opus 4.1", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "15", Output: "75", Cached: "1.5", CacheWrite: "18.75", Cache1h: "30"}, Notes: "Legacy Opus 4.1 price tier; higher than Opus 4.5+."},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-opus-4-5-20251101", DisplayName: "Claude Opus 4.5", OfficialStatus: "official_family", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "5", Output: "25", Cached: "0.5", CacheWrite: "6.25", Cache1h: "10"}},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "5", Output: "25", Cached: "0.5", CacheWrite: "6.25", Cache1h: "10"}},
		{ProviderKey: "anthropic", ProviderName: "Anthropic", ModelID: "claude-opus-4-7", DisplayName: "Claude Opus 4.7", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: anthropicPricing, Token: defaultTokenPrice{Input: "5", Output: "25", Cached: "0.5", CacheWrite: "6.25", Cache1h: "10"}},

		{ProviderKey: "openai", ProviderName: "OpenAI", ModelID: "gpt-5.2", DisplayName: "GPT-5.2", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: openAIModelGPT52, Token: defaultTokenPrice{Input: "1.75", Output: "14", Cached: "0.175"}},
		{ProviderKey: "openai", ProviderName: "OpenAI", ModelID: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: openAIDeveloperPricing, Token: defaultTokenPrice{Input: "1.75", Output: "14", Cached: "0.175"}},
		{ProviderKey: "openai", ProviderName: "OpenAI", ModelID: "gpt-5.4", DisplayName: "GPT-5.4", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: openAIDeveloperPricing, Token: defaultTokenPrice{Input: "2.5", Output: "15", Cached: "0.25", HighAt: 272001, HighInput: "5", HighOutput: "22.5"}},
		{ProviderKey: "openai", ProviderName: "OpenAI", ModelID: "gpt-5.4-mini", DisplayName: "GPT-5.4 mini", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: openAIDeveloperPricing, Token: defaultTokenPrice{Input: "0.75", Output: "4.5", Cached: "0.075"}},
		{ProviderKey: "openai", ProviderName: "OpenAI", ModelID: "gpt-5.5", DisplayName: "GPT-5.5", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: openAIDeveloperPricing, Token: defaultTokenPrice{Input: "5", Output: "30", Cached: "0.5", HighAt: 272001, HighInput: "10", HighOutput: "45"}, EndpointPolicy: EndpointPolicyNoChatNonStream},
		{ProviderKey: "openai", ProviderName: "OpenAI", ModelID: "gpt-image-2", DisplayName: "GPT Image 2", OfficialStatus: "official_exact", Category: ModelCategoryImage, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: openAIDeveloperPricing, Token: defaultTokenPrice{Input: "5", Output: "30", Cached: "1.25"}, Notes: "Text-to-image runtime via /v1/images/generations。当前业务全部聚焦 text，2026-05-19 起 catalog 标 Supported=false 让 admin UI 不展示；handler / pricing 代码保留，admin 后续如需要可在 UI 切回 Supported=true。"},
		{ProviderKey: "openai", ProviderName: "OpenAI", ModelID: "codex-auto-review", DisplayName: "codex-auto-review", OfficialStatus: "not_found", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: openAIDeveloperPricing, Notes: "Internal/unpriced model; never enable by default."},

		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: googlePricing, Token: defaultTokenPrice{Input: "0.3", Output: "2.5", Cached: "0.03"}},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-2.5-flash-lite", DisplayName: "Gemini 2.5 Flash Lite", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: googlePricing, Token: defaultTokenPrice{Input: "0.1", Output: "0.4", Cached: "0.01"}},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: googlePricing, Token: defaultTokenPrice{Input: "1.25", Output: "10", Cached: "0.125", HighAt: 200001, HighInput: "2.5", HighOutput: "15"}},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3-flash-preview", DisplayName: "Gemini 3 Flash Preview", OfficialStatus: "official_family", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: googlePricing, Token: defaultTokenPrice{Input: "0.5", Output: "3", Cached: "0.05"}},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3.1-flash-lite", DisplayName: "Gemini 3.1 Flash Lite", OfficialStatus: "official_family", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: googlePricing, Token: defaultTokenPrice{Input: "0.25", Output: "1.5", Cached: "0.025"}},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3.1-flash-lite-preview", DisplayName: "Gemini 3.1 Flash Lite Preview", OfficialStatus: "official_family", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: googlePricing, Token: defaultTokenPrice{Input: "0.25", Output: "1.5", Cached: "0.025"}},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3.1-pro-preview", DisplayName: "Gemini 3.1 Pro Preview", OfficialStatus: "official_family", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: googlePricing, Token: defaultTokenPrice{Input: "2", Output: "12", Cached: "0.2", HighAt: 200001, HighInput: "4", HighOutput: "18"}},

		// CPA 上游暴露但 DAOF 未确认官方 pricing 的 Google alias —— 仅 seed
		// catalog row（Supported=false / DefaultEnabled=false / 无 pricing），让 admin
		// UI 能看到完整 CPA 列表。admin 想启用必须先在 admin UI 填真实 pricing，并
		// 把 Supported / Status 切到启用态。与 [[media_endpoint_allowlist]] 一致。
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3-flash", DisplayName: "Gemini 3 Flash", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Notes: "CPA antigravity/直连路径暴露的 alias；DAOF 未确认官方 pricing，admin 启用前请补 token price 并切 Supported=true。"},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3-flash-agent", DisplayName: "Gemini 3 Flash Agent", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Notes: "CPA antigravity executor 暴露的 agent 变体；DAOF 未确认官方 pricing。"},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3-pro-low", DisplayName: "Gemini 3 Pro Low", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Notes: "CPA antigravity/直连路径暴露的 alias；DAOF 未确认官方 pricing，admin 启用前请补 token price 并切 Supported=true。"},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3-pro-high", DisplayName: "Gemini 3 Pro High", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Notes: "CPA antigravity/直连路径暴露的 alias；DAOF 未确认官方 pricing，admin 启用前请补 token price 并切 Supported=true。"},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3-pro-preview", DisplayName: "Gemini 3 Pro Preview", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Notes: "CPA antigravity/直连路径暴露的 alias；DAOF 未确认官方 pricing，admin 启用前请补 token price 并切 Supported=true。"},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3.1-pro-low", DisplayName: "Gemini 3.1 Pro Low", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Notes: "CPA antigravity 路径暴露的 Pro low quality tier；DAOF 未确认官方 pricing，admin 启用前请补 token price 并切 Supported=true。"},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3.5-flash-low", DisplayName: "Gemini 3.5 Flash Low", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Notes: "CPA antigravity 路径暴露的新 alias（2026-05-19 出现）；DAOF 未确认官方 pricing，admin 启用前请补 token price 并切 Supported=true。"},
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-pro-agent", DisplayName: "Gemini Pro Agent", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Notes: "CPA antigravity executor 暴露的 agent 变体；DAOF 未确认官方 pricing。"},
		// Gemini 图像类（CPA Gemini executor 暴露 generateContent / Imagen 由 CPA 内部
		// :predict → generateContent 翻译）：P6 后 DAOF 通过 /v1beta/models/<model>:generateContent
		// 路径完整接通（含 token-billed Gemini image + per-image-count Imagen 双模式）。
		// admin 启用步骤：(1) ChannelModel.AllowedEndpoints 加 "/v1beta/models"
		// (2) 切 Supported=true。计费由 gemini_native.go 按 BillingMode 自动分支：
		// token 模式用 usageMetadata；image 模式按 candidates[].inlineData 数量 × Media[output] 单价。
		// Gemini image models 改用 BillingMode=Token (S3, real-call calibration 2026-05-19)
		// 真实响应（gemini-3.1-flash-image antigravity）：
		//   default size: usageMetadata.candidatesTokenCount = 1469
		//   2K size:      usageMetadata.candidatesTokenCount = 2036
		// Token 模式让 candidatesTokenCount × outputRate 自动 cover resolution 变化；
		// 同时也 cover Google direct vs antigravity 的 token 计数差异（实测 antigravity 比
		// Google 公开档 token 数高 ~30%，token-mode 自动跟踪 actual usage）。
		// Output rate 沿 Google 公开口径：3.1 flash $60/Mtok，2.5 flash $30/Mtok，3 pro $120/Mtok
		// Input rate 是 text prompt 价：3.1 flash $0.50/Mtok，2.5 flash $0.30/Mtok，3 pro $2.00/Mtok
		// Gemini image 类（CPA antigravity 路径暴露 gemini-3.1-flash-image）：
		// P6 后 DAOF 通过 /v1beta/models/<model>:generateContent 接通，按 token 计费
		// (S3, real-call calibration 2026-05-19 — candidatesTokenCount × outputRate
		// 自动 cover resolution 变化 + antigravity vs Google direct 的 token 差异)。
		// admin 启用：AllowedEndpoints 加 "/v1beta/models" 后切 Supported=true。
		{ProviderKey: "google", ProviderName: "Google Gemini", ModelID: "gemini-3.1-flash-image", DisplayName: "Gemini 3.1 Flash Image", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryImage, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: googlePricing, Token: defaultTokenPrice{Input: "0.50", Output: "60"}, Notes: "Gemini 3 flash image（antigravity alias）；token-billed via usageMetadata.candidatesTokenCount × $60/Mtok。接通路径 /v1beta/models/gemini-3.1-flash-image:generateContent。admin 启用：AllowedEndpoints 加 /v1beta/models 后切 Supported=true。"},

		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-4.3", DisplayName: "Grok 4.3", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: xaiPricing, Token: defaultTokenPrice{Input: "1.25", Output: "2.5", Cached: "0.2"}, Notes: "Seed only the public base price; exact higher-context pricing must come from upstream usage tickets until xAI exposes a public matrix."},
		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-3-mini", DisplayName: "Grok 3 Mini", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: xaiPricing, Notes: "CPA 暴露的旧版 Grok 3 mini alias；DAOF 未确认官方 pricing（docs.x.ai 当前列表不含此型号），admin 启用前请确认 + 补 token price。"},
		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-3-mini-fast", DisplayName: "Grok 3 Mini Fast", OfficialStatus: "alias_or_unofficial", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: false, Public: false, DefaultEnabled: false, SourceURL: xaiPricing, Notes: "CPA 暴露的旧版 Grok 3 mini fast alias；DAOF 未确认官方 pricing（docs.x.ai 当前列表不含此型号），admin 启用前请确认 + 补 token price。"},
		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-4.20-0309-non-reasoning", DisplayName: "Grok 4.20 Non-reasoning", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: xaiPricing, Token: defaultTokenPrice{Input: "1.25", Output: "2.5", Cached: "0.2"}, Notes: "Seed only the public base price; exact higher-context pricing must come from upstream usage tickets until xAI exposes a public matrix."},
		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-4.20-0309-reasoning", DisplayName: "Grok 4.20 Reasoning", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: xaiPricing, Token: defaultTokenPrice{Input: "1.25", Output: "2.5", Cached: "0.2"}, Notes: "Seed only the public base price; exact higher-context pricing must come from upstream usage tickets until xAI exposes a public matrix."},
		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-4.20-multi-agent-0309", DisplayName: "Grok 4.20 Multi-agent", OfficialStatus: "official_exact", Category: ModelCategoryText, BillingMode: BillingModeToken, Supported: true, Public: true, DefaultEnabled: true, SourceURL: xaiPricing, Token: defaultTokenPrice{Input: "1.25", Output: "2.5", Cached: "0.2"}, Notes: "Seed only the public base price; exact higher-context pricing must come from upstream usage tickets until xAI exposes a public matrix."},
		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-imagine-image", DisplayName: "Grok Imagine Image", OfficialStatus: "official_exact", Category: ModelCategoryImage, BillingMode: BillingModeImage, Supported: false, Public: false, DefaultEnabled: false, SourceURL: xaiPricing, Media: []defaultMediaPrice{{Unit: "image", Direction: "input", Price: "0.002", Notes: "edits 输入图 precheck 估算价 $0.002（real-call calibration 2026-05-19）"}, {Unit: "image", Direction: "output", Resolution: "1K", Price: "0.02"}, {Unit: "image", Direction: "output", Resolution: "2K", Price: "0.02"}}, Notes: "Text-to-image runtime via /v1/images/generations。当前业务聚焦 text，2026-05-19 起 catalog 标 Supported=false；handler / pricing 代码保留，admin 后续可在 UI 切回。"},
		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-imagine-image-quality", DisplayName: "Grok Imagine Image Quality", OfficialStatus: "official_exact", Category: ModelCategoryImage, BillingMode: BillingModeImage, Supported: false, Public: false, DefaultEnabled: false, SourceURL: xaiPricing, Media: []defaultMediaPrice{{Unit: "image", Direction: "input", Price: "0.01", Notes: "edits 输入图 precheck 估算价 $0.01（real-call calibration 2026-05-19）"}, {Unit: "image", Direction: "output", Resolution: "1K", Price: "0.05"}, {Unit: "image", Direction: "output", Resolution: "2K", Price: "0.07"}}, Notes: "Text-to-image runtime via /v1/images/generations。当前业务聚焦 text，2026-05-19 起 catalog 标 Supported=false；handler / pricing 代码保留，admin 后续可在 UI 切回。"},
		{ProviderKey: "xai", ProviderName: "xAI", ModelID: "grok-imagine-video", DisplayName: "Grok Imagine Video", OfficialStatus: "official_exact", Category: ModelCategoryVideo, BillingMode: BillingModeVideoSecond, Supported: false, Public: false, DefaultEnabled: false, SourceURL: xaiPricing, Media: []defaultMediaPrice{{Unit: "video_second", Direction: "input", Price: "0.01", Notes: "edits/extensions 输入视频每秒 $0.01（fal.ai 公开口径）"}, {Unit: "video_second", Direction: "output", Resolution: "480p", Price: "0.05"}, {Unit: "video_second", Direction: "output", Resolution: "720p", Price: "0.07"}}, Notes: "Text-to-video runtime via /v1/videos/generations。当前业务聚焦 text，2026-05-19 起 catalog 标 Supported=false；handler / pricing 代码保留，admin 后续可在 UI 切回。"},
	}
}
