// Package database / cpa_credential_schema.go
//
// CPA 凭证元数据本地缓存表。
//
// 设计意图：
//
//	daof-ai-hub 通过 CPA 拿凭证清单（GET /v0/management/auth-files），
//	首次发现新凭证时下载完整 JSON 提取静态字段（最重要的是 antigravity 的
//	project_id），写入本表持久化。后续每个刷新周期只做"清单 diff"：
//
//	  - 新增 auth_id  → 下载 + 入库
//	  - 消失 auth_id  → 软删除（disabled=true 即可，避免 quota fetcher 仍调用）
//	  - 状态变化      → UPDATE 单字段
//
//	平时查 quota 时直接从本表读 project_id，无需重复下载凭证文件。
//
// 注意：本表不存任何 token / refresh_token —— 只缓存"静态、长期不变"的字段。
// access_token 由 CPA 内部管理（自动刷新），daof-ai-hub 走 api-call 透明代理时
// CPA 会自己注入最新 token。
package database

import "time"

// CPACredential 是 CPA 上每个凭证在本地的元数据缓存。
type CPACredential struct {
	// AuthID 是 CPA 给每个凭证的稳定唯一 ID（即 auth-files 返回里的 "id"）
	// 用作主键——同一个文件被禁用/重启用都保持同一个 AuthID
	AuthID string `gorm:"primaryKey;size:128" json:"auth_id"`

	// FileName 凭证文件名（auth-files 返回的 "name"，如 antigravity-foo@gmail.com.json）
	FileName string `gorm:"index;not null" json:"file_name"`

	// Provider claude | antigravity | codex | gemini-cli | kimi（已小写）
	Provider string `gorm:"index;not null;size:32" json:"provider"`

	// Email 凭证绑定的账号邮箱（admin 监控面板上方便识别）
	Email string `gorm:"size:255" json:"email"`

	// ProjectID antigravity 和 gemini-cli 凭证均非空 —— 从凭证 JSON 的
	// cloudaicompanionProject 字段提取。两个 provider 都调 google
	// cloudcode-pa.googleapis.com 的 project-scoped 端点：
	//   - antigravity: fetchAvailableModels
	//   - gemini-cli:  retrieveUserQuota（需 body 带 project）
	// 其他 provider 该字段为空即可。
	ProjectID string `gorm:"size:128" json:"project_id"`

	// Disabled CPA 端的禁用标志。本字段镜像 CPA 的 disabled 字段，
	// quota fetcher 跳过 disabled=true 的凭证。
	Disabled bool `gorm:"index;default:false" json:"disabled"`

	// Status CPA 端的运行时状态文本（如 active / failed / refreshing）
	Status string `gorm:"size:32" json:"status"`

	// LastSeenAt 最近一次在 CPA 清单里"看到"该凭证的时间
	// 用于检测"消失的凭证"——超过 N 个周期没出现就软删。
	LastSeenAt time.Time `gorm:"not null" json:"last_seen_at"`

	// LastDownloadedAt 最近一次下载该凭证文件的时间
	// 用于判断 project_id 是否值得重新探测（凭证文件如果旋转过，project_id 也可能变）。
	LastDownloadedAt time.Time `gorm:"not null" json:"last_downloaded_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
