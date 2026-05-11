// Package proxy / token_log.go
//
// API Bearer token 日志脱敏工具。
//
// 场景：ApiLog.TokenName 在 stream.go 入口直接保存了 client 发来的明文 bearer token，
// 任何能读 api_logs 表的角色（admin、SQL 查询、备份）都能拿到 user 的真实 sk-* 凭证。
//
// 修复：日志层只记可关联但不可还原的 8 字节短哈希（前缀 hash:），保持可观测性同时杜绝凭证泄漏。
//
// 不可逆：sha256 抗碰撞 + 截断 8 字节，对手通过哈希反推 token 必须暴力枚举所有 sk-* 空间，
// 在不掌握 salt 的前提下没有差异化收益（admin DB read 时 salt 已绕过）。
package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashTokenForLog 把明文 bearer token 转成日志安全的指纹格式 "hash:abcdef0123456789".
//
// 设计要点：
//   - 输入空 → 返回 ""（直接保存，避免 hash 一个空串迷惑分析）
//   - 输出固定 21 字符（"hash:"+16 hex = 8 字节）；4 字节生日碰撞在 ~65k token 即 50%，
//     升到 8 字节后碰撞需 ~5.1B token，对实际部署足够（codex/gemini 第三轮 Minor）
//   - 不引入 salt：admin SQL 查询追踪同一 token 的请求需要哈希一致；
//     即使 salt 入库也会被 admin 一并读到，无安全增益
func HashTokenForLog(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(t))
	return "hash:" + hex.EncodeToString(sum[:8])
}
