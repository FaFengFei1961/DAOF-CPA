package database

import (
	"log"

	"gorm.io/gorm"
)

// EnforceModelEndpointDefaults normalizes model-specific endpoint policy defaults.
// Moderation is intentionally not changed here: whether moderation is mandatory
// depends on the upstream channel target, not on model_id alone.
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
