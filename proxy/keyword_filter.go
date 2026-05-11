// Package proxy / keyword_filter.go
//
// 内容审核第一层：本地关键字快扫（毫秒级），拦截已知 jailbreak / fingerprint 模板。
//
// 设计选择（codex 第二十三轮反馈）：
//   - 用 strings.ToLower + strings.Contains 循环，**不引** github.com/cloudflare/ahocorasick
//     理由：100 词以内 O(n*m) ≈ 1ms（32K prompt × 100 词），ahocorasick 的并发安全 MatchThreadSafe
//     反而增加复杂度且依赖额外库
//   - sync.RWMutex 保护词库；admin 改 SysConfig 后调 Reload 重建（写锁）
//   - 命中**短路返回**首个关键字（不需要列出所有命中 — 拦下即可，详细命中由审计日志记录）
//
// 故意不做：
//   - NFKC normalization（全角/半角差异）—— OpenAI Moderation 兜底
//   - 同音字 / 形近字（"色情" vs "色!情"）—— 这是无底洞，让 Moderation 处理语义
package proxy

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
)

// KeywordFilter 本地关键字黑名单匹配器。所有方法并发安全。
type KeywordFilter struct {
	mu       sync.RWMutex
	keywords []string // 已 lowercase；admin 改 SysConfig 后 Reload
}

// 全局单例（与 SysConfigCache 同模式；启动时 LoadKeywordsFromConfig 初始化）
var globalKeywordFilter = &KeywordFilter{}

// LoadKeywordsFromConfig 从 SysConfig.moderation_keywords 解析 JSON 数组并加载。
// 失败时保留旧词库（不清空）+ log 告警。
func LoadKeywordsFromConfig() {
	SysConfigMutex.RLock()
	raw := strings.TrimSpace(SysConfigCache["moderation_keywords"])
	SysConfigMutex.RUnlock()
	if raw == "" {
		globalKeywordFilter.Reload(nil) // 空词库 → 关键字过滤等同 off
		return
	}
	var keywords []string
	if err := json.Unmarshal([]byte(raw), &keywords); err != nil {
		log.Printf("[KEYWORD-FILTER] invalid moderation_keywords JSON, keeping old list: %v", err)
		return
	}
	globalKeywordFilter.Reload(keywords)
}

// Reload 用新词库替换。词库会被 lowercase + 去重 + 去空白。
func (f *KeywordFilter) Reload(keywords []string) {
	clean := make([]string, 0, len(keywords))
	seen := make(map[string]struct{}, len(keywords))
	for _, k := range keywords {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		clean = append(clean, k)
	}
	f.mu.Lock()
	f.keywords = clean
	f.mu.Unlock()
}

// Match 返回首个命中的关键字（短路），空字符串 = 未命中。
// O(n*m) 但 100 词 × 32K prompt ≈ 3.2M ops，约 1ms 内返回。
func (f *KeywordFilter) Match(prompt string) string {
	if prompt == "" {
		return ""
	}
	lower := strings.ToLower(prompt)
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, kw := range f.keywords {
		if strings.Contains(lower, kw) {
			return kw
		}
	}
	return ""
}

// MatchKeyword 是 Match 的全局入口（业务层用）。
func MatchKeyword(prompt string) string {
	return globalKeywordFilter.Match(prompt)
}

// InvalidateKeywordFilterCache admin 改 SysConfig 后调，重新加载词库。
func InvalidateKeywordFilterCache() {
	LoadKeywordsFromConfig()
}

