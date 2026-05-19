package database

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// BillingRuleRevision 保存计费规则公开口径的历史快照。
//
// 它不是当前生效配置的来源；当前配置仍来自 sys_configs。该表只用于用户侧
// 公示和审计追溯，所以采用 append-only 语义：规则发版后不允许改写历史。
type BillingRuleRevision struct {
	ID                    uint       `gorm:"primaryKey;<-:create" json:"id"`
	Version               string     `gorm:"index;size:64;not null;<-:create" json:"version"`
	EffectiveSince        string     `gorm:"size:10;default:'';<-:create" json:"effective_since"`
	PublishedAt           *time.Time `gorm:"index;<-:create" json:"published_at"`
	EffectiveAt           *time.Time `gorm:"index;<-:create" json:"effective_at"`
	ModelWeightsJSON      string     `gorm:"type:text;not null;<-:create" json:"-"`
	HealthMultipliersJSON string     `gorm:"type:text;not null;<-:create" json:"-"`
	ModelCount            int        `gorm:"default:0;<-:create" json:"model_count"`
	HealthCount           int        `gorm:"default:0;<-:create" json:"health_count"`
	Source                string     `gorm:"size:32;default:'admin';<-:create" json:"source"`
	CreatedBy             uint       `gorm:"default:0;<-:create" json:"-"`
	CreatedAt             time.Time  `gorm:"index;<-:create" json:"created_at"`
}

var ErrBillingRuleRevisionAppendOnly = errors.New("billing_rule_revisions is append-only")

func (r *BillingRuleRevision) BeforeUpdate(tx *gorm.DB) error {
	return ErrBillingRuleRevisionAppendOnly
}

func (r *BillingRuleRevision) BeforeDelete(tx *gorm.DB) error {
	return ErrBillingRuleRevisionAppendOnly
}

// BillingRuleRevisionCancellation 记录未生效预发布版本的撤销事实。
//
// 不直接改 BillingRuleRevision，是为了保留"曾经预发布过什么"的审计证据；
// 运行时激活逻辑通过 NOT EXISTS cancellation 跳过被撤销的 revision。
type BillingRuleRevisionCancellation struct {
	ID         uint      `gorm:"primaryKey;<-:create" json:"id"`
	RevisionID uint      `gorm:"uniqueIndex;not null;<-:create" json:"revision_id"`
	Reason     string    `gorm:"size:500;<-:create" json:"reason"`
	CreatedBy  uint      `gorm:"default:0;<-:create" json:"-"`
	CreatedAt  time.Time `gorm:"index;<-:create" json:"created_at"`
}

var ErrBillingRuleRevisionCancellationAppendOnly = errors.New("billing_rule_revision_cancellations is append-only")

func (r *BillingRuleRevisionCancellation) BeforeUpdate(tx *gorm.DB) error {
	return ErrBillingRuleRevisionCancellationAppendOnly
}

func (r *BillingRuleRevisionCancellation) BeforeDelete(tx *gorm.DB) error {
	return ErrBillingRuleRevisionCancellationAppendOnly
}
