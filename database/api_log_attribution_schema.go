package database

import (
	"time"

	"gorm.io/gorm"
)

// ApiLogAttribution stores mutable upstream matching facts outside api_logs.
// The table is append-only: a log can receive at most one attribution row.
type ApiLogAttribution struct {
	ID                       uint      `gorm:"primaryKey;<-:create" json:"id"`
	ApiLogID                 uint      `gorm:"uniqueIndex;not null;<-:create" json:"api_log_id"`
	UpstreamUsageRecordID    uint      `gorm:"index;<-:create" json:"upstream_usage_record_id"`
	UpstreamProvider         string    `gorm:"index;size:64;default:'';<-:create" json:"upstream_provider"`
	UpstreamAccountAuthIndex string    `gorm:"index;size:64;default:'';<-:create" json:"upstream_account_auth_index"`
	UpstreamAuthType         string    `gorm:"size:64;default:'';<-:create" json:"upstream_auth_type"`
	UpstreamSource           string    `gorm:"size:255;default:'';<-:create" json:"upstream_source"`
	UpstreamRequestID        string    `gorm:"index;size:64;default:'';<-:create" json:"upstream_request_id"`
	MatchReason              string    `gorm:"size:64;default:'';<-:create" json:"match_reason"`
	MatchedAt                time.Time `gorm:"index;<-:create" json:"matched_at"`
	CreatedAt                time.Time `gorm:"<-:create" json:"created_at"`
}

func (a *ApiLogAttribution) BeforeUpdate(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

func (a *ApiLogAttribution) BeforeDelete(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}
