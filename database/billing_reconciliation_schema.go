// Package database / billing_reconciliation_schema.go
//
// 账单对账事实表（Sprint5-M8）。append-only，与 OperationLog / TopupRefund 同范式。
//
// 用途：闭环 BillingState 状态机 (pending_reconcile / upstream_unmetered → 已对账)，
// 不修改原 BillingEntry（append-only 契约），通过新 row 关联表达。
package database

import "time"

// ReconcileResult 对账结果枚举。
const (
	// ReconcileResultAbsorbed 平台吸收：admin 决定承担成本，不动用户余额，原 entry 标记 reconciled。
	ReconcileResultAbsorbed = "absorbed"
	// ReconcileResultCharged admin 决定向用户补扣（写入反向 admin_adjust BillingEntry 实际扣 quota）。
	ReconcileResultCharged = "charged"
	// ReconcileResultVoided 该 pending entry 是无效记录（重复/误写），仅标记 reconciled。
	ReconcileResultVoided = "voided"
)

// IsKnownReconcileResult 验证 result 字段是否合法枚举。
func IsKnownReconcileResult(s string) bool {
	switch s {
	case ReconcileResultAbsorbed, ReconcileResultCharged, ReconcileResultVoided:
		return true
	}
	return false
}

// BillingReconciliation 单条对账记录。append-only。
// 一笔 BillingEntry 只能被对账一次（uniqueIndex on billing_entry_id 兜底）。
// 决策错误时 admin 应通过新的反向 BillingEntry 修正，而非修改本表。
type BillingReconciliation struct {
	ID                       uint      `gorm:"primaryKey" json:"id"`
	BillingEntryID           uint      `gorm:"<-:create;uniqueIndex;not null" json:"billing_entry_id"`
	Result                   string    `gorm:"<-:create;not null;size:32;index" json:"result"`
	AdjustmentBillingEntryID uint      `gorm:"<-:create;default:0" json:"adjustment_billing_entry_id,omitempty"`
	OperatorID               uint      `gorm:"<-:create;index;not null" json:"operator_id"`
	OperatorRole             string    `gorm:"<-:create;not null;size:32" json:"operator_role"`
	Note                     string    `gorm:"<-:create;type:text;not null" json:"note"`
	CreatedAt                time.Time `gorm:"<-:create;index" json:"created_at"`
}
