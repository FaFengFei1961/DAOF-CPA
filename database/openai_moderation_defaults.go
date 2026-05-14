package database

import (
	"log"
	"strings"

	"gorm.io/gorm"
)

const (
	OpenAIModelModerationLevel    = "keyword"
	OpenAIModelModerationFailMode = "closed"
)

// EnforceOpenAIModelModerationDefaults keeps every OpenAI/Codex-family model
// on local high-confidence moderation. It is intentionally model-ID based rather than
// channel-type based because the "openai" channel type is also used for generic
// OpenAI-compatible upstreams such as domestic or self-hosted models.
func EnforceOpenAIModelModerationDefaults() {
	if DB == nil {
		return
	}
	changed := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		var models []ChannelModel
		if err := tx.Find(&models).Error; err != nil {
			return err
		}
		for _, m := range models {
			updates := map[string]any{}
			level := strings.ToLower(strings.TrimSpace(m.ModerationLevel))
			failMode := strings.ToLower(strings.TrimSpace(m.ModerationFailMode))

			if IsOpenAIModelID(m.ModelID) &&
				(level != OpenAIModelModerationLevel || failMode != OpenAIModelModerationFailMode) {
				updates["moderation_level"] = OpenAIModelModerationLevel
				updates["moderation_fail_mode"] = OpenAIModelModerationFailMode
			}

			endpointPolicy := NormalizeEndpointPolicy(m.EndpointPolicy)
			defaultEndpointPolicy := DefaultEndpointPolicyForModel(m.ModelID, endpointPolicy)
			if endpointPolicy != defaultEndpointPolicy {
				updates["endpoint_policy"] = defaultEndpointPolicy
			}

			if len(updates) == 0 {
				continue
			}
			if err := tx.Model(&ChannelModel{}).
				Where("id = ?", m.ID).
				Updates(updates).Error; err != nil {
				return err
			}
			changed++
		}
		return nil
	})
	if err != nil {
		log.Printf("[MODERATION-SEED] enforce OpenAI model moderation failed: %v", err)
		return
	}
	if changed > 0 {
		log.Printf("[MODERATION-SEED] OpenAI/Codex-family moderation / endpoint defaults enforced: %d rows", changed)
	}
}
