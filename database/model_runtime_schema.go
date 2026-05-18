package database

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// ModelCatalog is the platform-owned model directory. It records what DAOF-CPA
// recognizes, separately from whether a concrete upstream channel is enabled.
type ModelCatalog struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	ProviderKey     string    `gorm:"index;size:32;not null" json:"provider_key"`
	ProviderName    string    `gorm:"size:64;not null" json:"provider_name"`
	ModelID         string    `gorm:"uniqueIndex;size:160;not null" json:"model_id"`
	DisplayName     string    `gorm:"size:160;not null" json:"display_name"`
	OfficialModelID string    `gorm:"index;size:160;default:''" json:"official_model_id"`
	OfficialStatus  string    `gorm:"size:32;default:'official_exact'" json:"official_status"` // official_exact / official_family / alias_or_unofficial / not_found
	Category        string    `gorm:"size:16;default:'text'" json:"category"`                  // text / image / video
	BillingMode     string    `gorm:"size:24;default:'token'" json:"billing_mode"`             // token / image / video_second
	Supported       bool      `gorm:"default:false" json:"supported"`
	Public          bool      `gorm:"default:false" json:"public"`
	DefaultEnabled  bool      `gorm:"default:false" json:"default_enabled"`
	SourceURL       string    `gorm:"size:512;default:''" json:"source_url"`
	Notes           string    `gorm:"type:text;default:''" json:"notes"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ModelPricingRule stores official pricing snapshots in an auditable shape.
// Token models use *_pico_per_token fields; media models use price_micro_usd
// with dimensions such as size / quality / resolution.
type ModelPricingRule struct {
	ID                                 uint      `gorm:"primaryKey" json:"id"`
	RuleKey                            string    `gorm:"uniqueIndex;size:255;not null" json:"rule_key"`
	PricingVersion                     string    `gorm:"index;size:64;not null" json:"pricing_version"`
	ProviderKey                        string    `gorm:"index;size:32;not null" json:"provider_key"`
	ModelID                            string    `gorm:"index;size:160;not null" json:"model_id"`
	OfficialModelID                    string    `gorm:"index;size:160;default:''" json:"official_model_id"`
	OfficialStatus                     string    `gorm:"size:32;default:'official_exact'" json:"official_status"`
	BillingMode                        string    `gorm:"index;size:24;not null" json:"billing_mode"`
	Unit                               string    `gorm:"index;size:32;not null" json:"unit"` // token / image / video_second
	Direction                          string    `gorm:"index;size:16;default:''" json:"direction"`
	Quality                            string    `gorm:"index;size:32;default:''" json:"quality"`
	Size                               string    `gorm:"index;size:32;default:''" json:"size"`
	Resolution                         string    `gorm:"index;size:32;default:''" json:"resolution"`
	AspectRatio                        string    `gorm:"index;size:32;default:''" json:"aspect_ratio"`
	ContextMinTokens                   int       `gorm:"default:0" json:"context_min_tokens"`
	ContextMaxTokens                   int       `gorm:"default:0" json:"context_max_tokens"`
	InputPricePicoPerToken             int64     `gorm:"default:0" json:"input_price_pico_per_token"`
	OutputPricePicoPerToken            int64     `gorm:"default:0" json:"output_price_pico_per_token"`
	CachedInputPricePicoPerToken       int64     `gorm:"default:0" json:"cached_input_price_pico_per_token"`
	CacheWriteInputPricePicoPerToken   int64     `gorm:"default:0" json:"cache_write_input_price_pico_per_token"`
	CacheWrite1hInputPricePicoPerToken int64     `gorm:"column:cache_write_1h_input_price_pico_per_token;default:0" json:"cache_write_1h_input_price_pico_per_token"`
	PriceMicroUSD                      int64     `gorm:"default:0" json:"price_micro_usd"`
	Currency                           string    `gorm:"size:8;default:'USD'" json:"currency"`
	SourceURL                          string    `gorm:"size:512;default:''" json:"source_url"`
	Notes                              string    `gorm:"type:text;default:''" json:"notes"`
	CreatedAt                          time.Time `json:"created_at"`
	UpdatedAt                          time.Time `json:"updated_at"`
}

// ApiLogUsageLine stores the auditable metered facts behind an ApiLog.
// A single media request can have multiple chargeable lines: text input tokens,
// image input units, image output units, or future video seconds. Keeping these
// as lines instead of columns prevents image/video billing from being forced
// into a single hard-coded shape.
type ApiLogUsageLine struct {
	ID             uint      `gorm:"primaryKey;<-:create" json:"id"`
	ApiLogID       uint      `gorm:"index;<-:create" json:"api_log_id"`
	ModelName      string    `gorm:"index;size:160;<-:create" json:"model_name"`
	RequestPath    string    `gorm:"size:160;<-:create" json:"request_path"`
	Unit           string    `gorm:"index;size:32;<-:create" json:"unit"`      // token / image / video_second / request
	Direction      string    `gorm:"index;size:24;<-:create" json:"direction"` // input / output / text_input / image_input
	Quantity       int64     `gorm:"default:0;<-:create" json:"quantity"`
	UnitPriceMicro int64     `gorm:"default:0;<-:create" json:"unit_price_micro_usd"`
	AmountMicroUSD int64     `gorm:"default:0;<-:create" json:"amount_micro_usd"`
	PricingRuleID  uint      `gorm:"index;default:0;<-:create" json:"pricing_rule_id"`
	CostSource     string    `gorm:"size:48;default:'';<-:create" json:"cost_source"` // upstream_usage / official_matrix / pending_reconcile
	Quality        string    `gorm:"index;size:32;default:'';<-:create" json:"quality"`
	Size           string    `gorm:"index;size:32;default:'';<-:create" json:"size"`
	Resolution     string    `gorm:"index;size:32;default:'';<-:create" json:"resolution"`
	AspectRatio    string    `gorm:"index;size:32;default:'';<-:create" json:"aspect_ratio"`
	MetadataJSON   string    `gorm:"type:text;default:'';<-:create" json:"metadata_json"`
	CreatedAt      time.Time `gorm:"<-:create" json:"created_at"`
}

func (ApiLogUsageLine) BeforeUpdate(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

func (ApiLogUsageLine) BeforeDelete(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

// MediaGenerationJob binds an upstream async media request id to the DAOF user
// and channel that created it. Retrieval endpoints must consult this table
// instead of blindly proxying arbitrary request ids to upstream credentials.
type MediaGenerationJob struct {
	ID             uint      `gorm:"primaryKey;<-:create" json:"id"`
	RequestID      string    `gorm:"uniqueIndex;size:160;not null;<-:create" json:"request_id"`
	UserID         uint      `gorm:"index;not null;<-:create" json:"user_id"`
	ChannelID      uint      `gorm:"index;not null;<-:create" json:"channel_id"`
	ModelName      string    `gorm:"index;size:160;not null;<-:create" json:"model_name"`
	RequestPath    string    `gorm:"size:160;<-:create" json:"request_path"`
	CreateApiLogID uint      `gorm:"index;default:0;<-:create" json:"create_api_log_id"`
	CreatedAt      time.Time `gorm:"<-:create" json:"created_at"`
}

func (MediaGenerationJob) BeforeUpdate(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

func (MediaGenerationJob) BeforeDelete(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

func ValidateModelPricingRule(rule *ModelPricingRule) error {
	if rule == nil {
		return errors.New("model pricing rule is nil")
	}
	mode := NormalizeBillingMode(rule.BillingMode, "")
	switch mode {
	case BillingModeToken:
		if rule.InputPricePicoPerToken < 0 ||
			rule.OutputPricePicoPerToken < 0 ||
			rule.CachedInputPricePicoPerToken < 0 ||
			rule.CacheWriteInputPricePicoPerToken < 0 ||
			rule.CacheWrite1hInputPricePicoPerToken < 0 {
			return errors.New("token pricing values must be non-negative")
		}
	case BillingModeImage, BillingModeVideoSecond:
		if rule.PriceMicroUSD < 0 {
			return errors.New("media pricing value must be non-negative")
		}
	default:
		return errors.New("invalid billing mode")
	}
	return nil
}
