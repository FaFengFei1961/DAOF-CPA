package database

import "time"

// UserSession 用户会话。Bearer token 失效查这个表，不再依赖"持有 token 字符串"作为唯一凭证。
//
// fix CRITICAL Sprint5-M1：原 user.Token 是长期不变的 API key（设计为 SDK 凭证），
// 但用户浏览器 session 也复用同 token → 浏览器关闭后 token 仍可被滥用，logout 不能撤销。
// 新设计：浏览器 session 独立 token + 服务端 session 表，logout 即时撤销。
type UserSession struct {
	ID         uint       `gorm:"primaryKey"`
	UserID     uint       `gorm:"<-:create;index;not null"`
	SessionID  string     `gorm:"<-:create;uniqueIndex;not null;size:64"` // crypto/rand 32 bytes hex
	UserAgent  string     `gorm:"<-:create;size:255"`                     // 审计：何种客户端
	IPAddress  string     `gorm:"<-:create;size:64"`                      // 审计：登录 IP
	CreatedAt  time.Time  `gorm:"<-:create;index"`
	LastUsedAt time.Time  `gorm:"index"`           // 每次 auth 校验更新
	ExpiresAt  time.Time  `gorm:"<-:create;index"` // 超过此时间视作失效
	RevokedAt  *time.Time `gorm:"index"`           // logout 时设置；非 nil = 已吊销
}
