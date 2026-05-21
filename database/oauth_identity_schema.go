// Package database / oauth_identity_schema.go
//
// OAuth 第三方身份绑定事实表。Phase H-1（2026-05-20）。
//
// 设计目标：从单 provider（旧 User.GithubID 列）解耦到 N provider。
//
// 一条 OAuthIdentity = "DAOF user X 经由 provider P 拥有外部账号 external_id"
//   - 同一 user 可以有多条（一个 user 绑 GitHub + Google + ...）
//   - 同一 (provider, external_id) 全局只能属于一个 user（防账号被同时绑给两个 DAOF 用户）
//
// **append-only 范式**（与 BillingEntry / EmailVerification 同）：
//   - BeforeUpdate 拦截：仅允许更新 unlinked_at 一列（unlink 软删）
//   - BeforeDelete 拦截：永远拒物理删除
//   - 业务列加 `<-:create` GORM tag 防 UPDATE 篡改
//
// **登录解析**：
//   - Login by external identity → SELECT user_id FROM oauth_identities
//                                   WHERE provider=? AND external_id=? AND unlinked_at IS NULL
//   - 拒绝 unlinked 的 identity（unlink 后即使外部 provider 仍返回该 external_id，DAOF 也不再认）
//
// **重新绑定语义**（unlinked → relink）：
//   - 用户解绑 GitHub 后又想重新绑 → 新行 INSERT（旧行保留作审计），
//     而不是 reactivate 旧行。旧行的 unique (provider, external_id) 仍占用，
//     **但 caller 在新 INSERT 前要先 hard-set 旧行 unlinked_at 到一个 sentinel 远期时间**
//     避免唯一索引冲突 —— 实施时需要 partial unique index "WHERE unlinked_at IS NULL"。
package database

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// OAuthIdentity 记录一条"DAOF user ↔ 第三方账号"的绑定关系。
//
// 注：GORM 默认会把 `OAuthIdentity` 命名成 `o_auth_identities`（拆 OAuth → O Auth）。
// 用 TableName() 显式强制 `oauth_identities`，与 sqlite.go 里 partial unique index 的
// 表名保持一致。
type OAuthIdentity struct {
	ID     uint `gorm:"primaryKey;<-:create" json:"id"`
	UserID uint `gorm:"index;not null;<-:create" json:"user_id"`

	// Provider 第三方标识：'github' / 'google' / 'microsoft' / ...
	// 与 ExternalID 的复合唯一索引由 sqlite.go 的 partial unique index 保证
	// （WHERE unlinked_at IS NULL），让"已解绑 + 重新绑"的 DB 行可以共存。
	Provider string `gorm:"index;not null;size:32;<-:create" json:"provider"`

	// ExternalID provider 内部的用户唯一 ID（GitHub 数字串 / Google sub UUID / etc.）
	// 不存 email/username 作 ID —— provider 内部 ID 是唯一稳定锚点。
	ExternalID string `gorm:"index;not null;size:128;<-:create" json:"external_id"`

	// EmailAtLink / UsernameAtLink 是 link 当时 provider 返回的快照，用作：
	//   1. 审计（事后调查用户是怎么 link 的）
	//   2. fallback display（provider 账号被删后 UI 仍能显示"曾经绑过 alice@github.com"）
	// 不参与任何活跃查询。
	EmailAtLink    string    `gorm:"size:254;<-:create" json:"email_at_link"`
	UsernameAtLink string    `gorm:"size:64;<-:create" json:"username_at_link"`
	LinkedAt       time.Time `gorm:"index;<-:create" json:"linked_at"`

	// LinkMethod（H-Audit L7，2026-05-20）：标记本行是怎么写入的，用于审计追溯。
	// 取值见 LinkMethod* 常量：
	//   - "oauth_flow"：OAuth callback 注册新用户路径（CompleteRisk / CompleteProfile）
	//   - "user_link"：已登录用户通过 /api/user/oauth/:provider/link/prepare 绑定
	//   - "backfill"：H-1 启动 migration 从 User.GithubID 回填（历史）
	//   - "admin_grant"：admin 手动协助绑定（未来支持）
	// 空字符串视为 "oauth_flow"（向后兼容老数据；本字段在 H-Audit 后新增）。
	LinkMethod string `gorm:"size:16;<-:create" json:"link_method,omitempty"`

	// UnlinkedAt：nil = active link；非 nil = 用户手动解绑（软删）。
	// 解绑的 identity 不再用于登录，但行本身保留作审计。
	UnlinkedAt *time.Time `gorm:"index" json:"unlinked_at,omitempty"`
}

// ErrOAuthIdentityImmutable append-only sentinel：
// 业务字段写后不可改；仅 unlinked_at 列允许更新。
var ErrOAuthIdentityImmutable = errors.New("oauth_identities is append-only (only unlinked_at may be updated)")

// BeforeUpdate 拦截 GORM update：除非仅写 unlinked_at 一列，否则拒。
// caller 应用 db.Model(&row).Where(...).Update("unlinked_at", time.Now()) pattern。
func (i *OAuthIdentity) BeforeUpdate(tx *gorm.DB) error {
	stmt := tx.Statement
	if stmt == nil {
		return ErrOAuthIdentityImmutable
	}
	// Updates(map{"unlinked_at": ...}) 路径
	if dest, ok := stmt.Dest.(map[string]interface{}); ok && len(dest) == 1 {
		if _, ok := dest["unlinked_at"]; ok {
			return nil
		}
		return ErrOAuthIdentityImmutable
	}
	// Save() / Updates(struct{...}) 等没指定 Select → 默认全列写 → 拒
	if len(stmt.Selects) == 0 {
		return ErrOAuthIdentityImmutable
	}
	// fix H-Audit L3（2026-05-20）：Select("*") / Select(clause.Associations) 等
	// 含 GORM 通配符的写都视作"全列更新"，一律拒。这里只显式允许 unlinked_at
	// 字面列名（GORM struct 字段名两种写法都接受）；其它任何 token（含 "*"）→ 拒。
	for _, col := range stmt.Selects {
		if col != "unlinked_at" && col != "UnlinkedAt" {
			return ErrOAuthIdentityImmutable
		}
	}
	return nil
}

// BeforeDelete 永远拒绝物理删除（与 BillingEntry / EmailVerification 同）。
func (i *OAuthIdentity) BeforeDelete(tx *gorm.DB) error {
	return ErrOAuthIdentityImmutable
}

// IsActive 是否仍为有效绑定（可用于登录）。
func (i *OAuthIdentity) IsActive() bool {
	return i.UnlinkedAt == nil
}

// TableName 强制表名 oauth_identities（避免 GORM 默认 o_auth_identities）。
func (OAuthIdentity) TableName() string { return "oauth_identities" }

// Provider key 常量。值持久化到 DB，**永不修改**（修改会让旧行查不到）。
const (
	OAuthProviderGitHub = "github"
	OAuthProviderGoogle = "google"
)

// LinkMethod 常量。值持久化到 DB，**永不修改**。
// fix H-Audit L7（2026-05-20）：用于审计追溯一条 identity 是怎么写入的。
const (
	LinkMethodOAuthFlow  = "oauth_flow"  // OAuth callback 新注册路径
	LinkMethodUserLink   = "user_link"   // 已登录用户主动 link
	LinkMethodBackfill   = "backfill"    // H-1 migration 回填
	LinkMethodAdminGrant = "admin_grant" // admin 协助绑定（未来支持）
)
