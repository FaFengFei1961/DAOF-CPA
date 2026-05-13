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
			if !IsOpenAIModelID(m.ModelID) {
				continue
			}
			level := strings.ToLower(strings.TrimSpace(m.ModerationLevel))
			failMode := strings.ToLower(strings.TrimSpace(m.ModerationFailMode))
			if level == OpenAIModelModerationLevel && failMode == OpenAIModelModerationFailMode {
				continue
			}
			if err := tx.Model(&ChannelModel{}).
				Where("id = ?", m.ID).
				Updates(map[string]any{
					"moderation_level":     OpenAIModelModerationLevel,
					"moderation_fail_mode": OpenAIModelModerationFailMode,
				}).Error; err != nil {
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
		log.Printf("[MODERATION-SEED] OpenAI/Codex-family models forced to %s+%s: %d rows",
			OpenAIModelModerationLevel, OpenAIModelModerationFailMode, changed)
	}
}
