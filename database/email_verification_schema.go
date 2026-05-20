// Package database / email_verification_schema.go
//
// 邮箱验证 / 密码重置等"邮件令牌"事实表。append-only 设计（用过的 token 标 ConsumedAt，
// 不删；过期的 token 由 cron 定期清理）。
//
// Phase G-1.1（2026-05-19）。
//
// 安全约定：
//   - TokenHash 存原始 token 的 SHA-256 hex；DB 中绝不存明文 token
//   - Purpose: "verify"（邮箱所有权确认）/ "reset_password"（忘记密码）/ "set_password"
//     （OAuth 用户首次启用 email-login 设置密码，复用 reset 流程语义）
//   - 同一 (user_id, purpose) 任意时刻最多一条未消费且未过期的记录（caller 在事务里
//     先 UPDATE 已存在的 token 标 ConsumedAt='replaced'，再 INSERT 新的，保证不滥发）
//   - ConsumedAt 一次性消费：成功消费后立即 SET ConsumedAt = now，防 token replay
//   - TTL：verify 默认 1h（SysConfig email_verify_token_ttl_seconds）；
//          reset_password 默认 15min（更敏感 → 短 TTL）
package database

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// EmailVerification 是邮件 token 的事实表。
//
// 不可变（append-only）：
//   - BeforeUpdate/BeforeDelete 阻止物理修改/删除（与 BillingEntry 同样的 audit 设计）
//   - 唯一例外：消费时 SET ConsumedAt 在 GORM 看来是 update，所以这里允许更新 ConsumedAt
//     这一字段（在 BeforeUpdate 里放行该列），其他列任何改动都拒绝。
type EmailVerification struct {
	ID         uint   `gorm:"primaryKey;<-:create" json:"id"`
	UserID     uint   `gorm:"index;not null;<-:create" json:"user_id"`
	Email      string `gorm:"index;not null;size:254;<-:create" json:"email"` // 申请时填入的邮箱（小写规范化）
	TokenHash  string `gorm:"uniqueIndex;not null;size:64;<-:create" json:"-"` // SHA-256 hex，64 chars
	// Purpose: "verify" | "reset_password" | "set_password"
	Purpose    string     `gorm:"index;not null;size:32;<-:create" json:"purpose"`
	ExpiresAt  time.Time  `gorm:"index;not null;<-:create" json:"expires_at"`
	ConsumedAt *time.Time `gorm:"index" json:"consumed_at"`                  // nil 未消费；非 nil 已消费（一次性）
	ClientIP   string     `gorm:"size:64;<-:create" json:"client_ip"`        // 申请 token 的客户端 IP（审计 / 限流）
	UserAgent  string     `gorm:"size:255;<-:create" json:"user_agent"`      // 同上
	CreatedAt  time.Time  `gorm:"index;<-:create" json:"created_at"`
}

// EmailVerificationPurpose* 是 Purpose 字段的合法值。
const (
	EmailVerificationPurposeVerify        = "verify"         // 绑定邮箱时确认所有权
	EmailVerificationPurposeResetPassword = "reset_password" // 忘记密码 → 设置新密码
	EmailVerificationPurposeSetPassword   = "set_password"   // OAuth 用户首次启用 email-login → 设置密码
)

// ErrEmailVerificationImmutable 是表级 append-only 约束的 sentinel。
// 唯一允许的 update 是把 ConsumedAt 从 nil 改为 time（消费 token）；其他任何修改都返回此错误。
var ErrEmailVerificationImmutable = errors.New("email_verifications is append-only (only consumed_at may be updated)")

// BeforeUpdate 拦截 GORM Update / Save 调用：除非仅更新 ConsumedAt（消费 token 路径），
// 一律拒绝。caller 应：
//   - 写新 token：直接 db.Create(&row)
//   - 消费 token：db.Model(&row).Where(...).Update("consumed_at", time.Now())（仅这一列）
func (v *EmailVerification) BeforeUpdate(tx *gorm.DB) error {
	// 仅当 tx 里只更新了 consumed_at 一列时放行
	stmt := tx.Statement
	if stmt == nil || len(stmt.Selects) == 0 {
		// GORM Save() 或没指定 Select 的 Update 会试图写所有列 → 拒绝
		// 检查 ReflectValue 的零值变更是无效的；这里以"未指定 Select 列"等同"全列写"处理。
		if dest, ok := stmt.Dest.(map[string]interface{}); ok && len(dest) == 1 {
			if _, ok := dest["consumed_at"]; ok {
				return nil
			}
		}
		return ErrEmailVerificationImmutable
	}
	for _, col := range stmt.Selects {
		if col != "consumed_at" && col != "ConsumedAt" {
			return ErrEmailVerificationImmutable
		}
	}
	return nil
}

// BeforeDelete 永远拒绝物理删除。expired token 走"软清理"——cron 不删行，仅给 ConsumedAt
// 标记 sentinel time（如 1970-01-01）或不做任何操作（让 ExpiresAt 过滤即可）。
func (v *EmailVerification) BeforeDelete(tx *gorm.DB) error {
	return ErrEmailVerificationImmutable
}

// IsConsumed 返回 token 是否已消费（一次性）。
func (v *EmailVerification) IsConsumed() bool {
	return v.ConsumedAt != nil
}

// IsExpired 返回 token 是否已过期。
func (v *EmailVerification) IsExpired(now time.Time) bool {
	return now.After(v.ExpiresAt)
}

// IsUsable 返回 token 是否可以被消费（未过期、未消费）。
func (v *EmailVerification) IsUsable(now time.Time) bool {
	return !v.IsConsumed() && !v.IsExpired(now)
}
