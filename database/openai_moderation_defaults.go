package database

import (
	"log"
	"strings"

	"gorm.io/gorm"
)

const openAIGPTModerationDefaultVersion = "2026-05-18-gpt-mod-closed"

// EnforceModelEndpointDefaults normalizes model-specific endpoint policy defaults.
func EnforceModelEndpointDefaults() {
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
		log.Printf("[MODEL-SEED] enforce model endpoint defaults failed: %v", err)
		return
	}
	if changed > 0 {
		log.Printf("[MODEL-SEED] model endpoint defaults enforced: %d rows", changed)
	}
}

// EnforceOpenAIGPTModerationDefaults migrates the old GPT "keyword+closed" seed
// to "moderation+closed" once. Keyword-only remains available for admins, but the
// default GPT posture should use the gpt-5.4-mini semantic classifier to avoid
// hard-blocking benign security or API-key configuration questions on word match.
func EnforceOpenAIGPTModerationDefaults() {
	if DB == nil {
		return
	}
	changed := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		if readPlainSysConfigInTx(tx, "openai_gpt_moderation_default_version") == openAIGPTModerationDefaultVersion {
			return nil
		}
		var models []ChannelModel
		if err := tx.Find(&models).Error; err != nil {
			return err
		}
		for _, m := range models {
			if !shouldMigrateOpenAIGPTModerationDefault(m) {
				continue
			}
			if err := tx.Model(&ChannelModel{}).
				Where("id = ?", m.ID).
				Updates(map[string]any{
					"moderation_level":     "moderation",
					"moderation_fail_mode": "closed",
				}).Error; err != nil {
				return err
			}
			changed++
		}
		return upsertPlainSysConfigInTx(tx, "openai_gpt_moderation_default_version", openAIGPTModerationDefaultVersion)
	})
	if err != nil {
		log.Printf("[MODEL-SEED] enforce GPT moderation defaults failed: %v", err)
		return
	}
	if changed > 0 {
		log.Printf("[MODEL-SEED] GPT moderation defaults migrated to moderation+closed: %d rows", changed)
	}
}

func shouldMigrateOpenAIGPTModerationDefault(m ChannelModel) bool {
	if m.Status != 1 {
		return false
	}
	if !IsOpenAIGPTTextModelID(m.ModelID) {
		return false
	}
	return strings.ToLower(strings.TrimSpace(m.ModerationLevel)) == "keyword" &&
		strings.ToLower(strings.TrimSpace(m.ModerationFailMode)) == "closed"
}
