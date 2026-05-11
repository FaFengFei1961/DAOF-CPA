// Package proxy / content_moderation.go
//
// 内容审核第二层：调用 OpenAI Moderation API（omni-moderation-latest 支持多语言 + 图片）。
//
// 设计（codex 第二十三轮反馈全部吸收）：
//   - HMAC-SHA256(secret, prompt+modelVer+policyVer) 作为缓存 key（防侧信道猜测）
//   - bounded LRU（max_entries 限内存）+ TTL（5 min 默认）
//   - 长 prompt 分块审核（每块 ≤ 28K rune），最大 8 块（防 DoS）
//   - 任一分块 flagged 即整体拒绝
//   - 失败仅记录在 ModerationResult.Err；调用方根据 ChannelModel.ModerationFailMode 决定 open/closed
//   - **不在响应里透传 category/score**（rejectBySourceFormat 兜底）
package proxy

import (
	"bytes"
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/utils"
)

// ModerationResult 一次审核的最终结论
type ModerationResult struct {
	Flagged      bool               // 任一类别命中
	Categories   map[string]float64 // category → score（仅写审计，不透传客户端）
	HighestCat   string             // 命中最高分的类别
	HighestScore float64
	FromCache    bool   // 是否命中本地缓存
	Err          error  // 远程调用失败时设置（与 Flagged=false 同时发生）
	Endpoint     string // 实际请求的 endpoint（审计用）
}

// ModerationConfig 全局共享配置（从 SysConfigCache 读）
type ModerationConfig struct {
	APIKey         string
	Endpoint       string
	Model          string
	Threshold      float64
	CacheTTLSec    int
	CacheMaxItems  int
	MaxChars       int    // 单次审核 prompt 最大 rune 数
	ChunkChars     int    // 分块大小
	MaxChunks      int    // 最大分块数
	ImagePolicy    string // skip / submit / reject
	BlockMessageZh string
	BlockMessageEn string
	UnavailZh      string
	UnavailEn      string
}

// LoadModerationConfig 从 SysConfigCache 拉一份配置快照（每次调用都重读，保证 admin 改动 ≤ 5s 生效）。
// 失败字段使用零值——调用方根据 IsConfigured() 判断是否可执行。
func LoadModerationConfig() ModerationConfig {
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()
	getStr := func(k, def string) string {
		v := strings.TrimSpace(SysConfigCache[k])
		if v == "" {
			return def
		}
		return v
	}
	getFloat := func(k string, def float64) float64 {
		v := strings.TrimSpace(SysConfigCache[k])
		if v == "" {
			return def
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return def
		}
		return f
	}
	getInt := func(k string, def int) int {
		v := strings.TrimSpace(SysConfigCache[k])
		if v == "" {
			return def
		}
		i, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return i
	}
	return ModerationConfig{
		APIKey:         getStr("moderation_openai_key", ""),
		Endpoint:       getStr("moderation_openai_endpoint", "https://api.openai.com/v1/moderations"),
		Model:          getStr("moderation_openai_model", "omni-moderation-latest"),
		Threshold:      getFloat("moderation_threshold", 0.8),
		CacheTTLSec:    getInt("moderation_cache_ttl_sec", 300),
		CacheMaxItems:  getInt("moderation_cache_max_entries", 10000),
		MaxChars:       getInt("moderation_max_chars", 262144),
		ChunkChars:     getInt("moderation_chunk_chars", 28672),
		MaxChunks:      getInt("moderation_max_chunks", 8),
		ImagePolicy:    getStr("moderation_image_policy", "submit"),
		BlockMessageZh: getStr("moderation_block_message_zh", "您的请求包含违规内容，已被系统拦截。"),
		BlockMessageEn: getStr("moderation_block_message_en", "Your request was blocked by content moderation."),
		UnavailZh:      getStr("moderation_unavailable_message_zh", "内容审核服务暂时不可用，请稍后重试。"),
		UnavailEn:      getStr("moderation_unavailable_message_en", "Content moderation is temporarily unavailable. Please retry later."),
	}
}

// IsConfigured Moderation 是否可调（API key 必填）
func (c ModerationConfig) IsConfigured() bool {
	return c.APIKey != ""
}

// ─── 缓存（HMAC + bounded LRU + TTL）────────────────────────────────

type moderationCacheEntry struct {
	result    ModerationResult
	expiresAt time.Time
	elem      *list.Element // LRU 双向链表节点
	key       string
}

var (
	moderationCacheMu       sync.Mutex
	moderationCacheMap      = make(map[string]*moderationCacheEntry)
	moderationCacheLRU      = list.New() // 队头 = 最近用过；队尾 = 最久没用
	moderationCacheSecret   []byte
	moderationCacheSecretMu sync.RWMutex
)

// loadOrGenerateCacheSecret 启动时从 SysConfig 读 moderation_cache_secret；空则随机生成 256bit + 写回。
// secret 用于 HMAC 防侧信道（攻击者无法构造特定 prompt 来探测缓存）。
func loadOrGenerateCacheSecret() []byte {
	SysConfigMutex.RLock()
	cur := strings.TrimSpace(SysConfigCache["moderation_cache_secret"])
	SysConfigMutex.RUnlock()
	if cur != "" {
		return []byte(cur)
	}
	// 随机生成 + 加密入库（入库失败仍返回随机值用，下次启动再写）
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("[MODERATION] rand secret failed: %v (using static fallback)", err)
		return []byte("daof-moderation-fallback-secret-please-set")
	}
	hexed := hex.EncodeToString(buf)
	enc, err := utils.Encrypt(hexed)
	if err == nil {
		_ = database.DB.Where("key = ?", "moderation_cache_secret").
			Assign(database.SysConfig{Key: "moderation_cache_secret", Value: enc}).
			FirstOrCreate(&database.SysConfig{}).Error
		// SysConfigCache 下一次刷新时会读到新值；当前内存 secret 用本地 hexed
		SysConfigMutex.Lock()
		SysConfigCache["moderation_cache_secret"] = hexed
		SysConfigMutex.Unlock()
	}
	return []byte(hexed)
}

func moderationSecret() []byte {
	moderationCacheSecretMu.RLock()
	s := moderationCacheSecret
	moderationCacheSecretMu.RUnlock()
	if s != nil {
		return s
	}
	moderationCacheSecretMu.Lock()
	defer moderationCacheSecretMu.Unlock()
	if moderationCacheSecret == nil {
		moderationCacheSecret = loadOrGenerateCacheSecret()
	}
	return moderationCacheSecret
}

// computeCacheKey HMAC-SHA256(secret, prompt + ":" + model + ":" + policyVer + ":" + threshold)
// policyVer 由 keywords + image_policy 的哈希组成（策略变化时缓存自动失效）
func computeCacheKey(prompt, model string, threshold float64) string {
	mac := hmac.New(sha256.New, moderationSecret())
	policyVer := computePolicyVersion()
	fmt.Fprintf(mac, "%s\x00%s\x00%s\x00%.4f", prompt, model, policyVer, threshold)
	return hex.EncodeToString(mac.Sum(nil))
}

// computePolicyVersion 把影响审核结果的 SysConfig 字段哈希成 8 字符短串。
// 任何字段变更会让缓存 key 自动作废。
//
// fix MAJOR R23-M5（codex 审查）：endpoint 必须纳入 —— admin 切换审核服务（如从
// OpenAI 官方换到自部署兼容 API），同 prompt 在两个 endpoint 的判定结果可能不同。
// 不加入 endpoint 会让旧结论被复用到 TTL 结束。
func computePolicyVersion() string {
	SysConfigMutex.RLock()
	parts := []string{
		SysConfigCache["moderation_keywords"],
		SysConfigCache["moderation_image_policy"],
		SysConfigCache["moderation_openai_model"],
		SysConfigCache["moderation_openai_endpoint"], // M5
		SysConfigCache["moderation_threshold"],       // 阈值变化也应让缓存失效
	}
	SysConfigMutex.RUnlock()
	h := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(h[:4])
}

// cacheGet 命中返回 (result, true)；未命中或过期返回零值
func cacheGet(key string) (ModerationResult, bool) {
	moderationCacheMu.Lock()
	defer moderationCacheMu.Unlock()
	entry, ok := moderationCacheMap[key]
	if !ok {
		return ModerationResult{}, false
	}
	if time.Now().After(entry.expiresAt) {
		// 过期 → 删除
		moderationCacheLRU.Remove(entry.elem)
		delete(moderationCacheMap, key)
		return ModerationResult{}, false
	}
	// 移到队头（最近用）
	moderationCacheLRU.MoveToFront(entry.elem)
	res := entry.result
	res.FromCache = true
	return res, true
}

// cachePut 写入（必要时驱逐最旧）
func cachePut(key string, result ModerationResult, ttl time.Duration, maxItems int) {
	moderationCacheMu.Lock()
	defer moderationCacheMu.Unlock()
	if existing, ok := moderationCacheMap[key]; ok {
		// 更新已有
		existing.result = result
		existing.expiresAt = time.Now().Add(ttl)
		moderationCacheLRU.MoveToFront(existing.elem)
		return
	}
	entry := &moderationCacheEntry{
		result:    result,
		expiresAt: time.Now().Add(ttl),
		key:       key,
	}
	entry.elem = moderationCacheLRU.PushFront(entry)
	moderationCacheMap[key] = entry
	// 驱逐
	for moderationCacheLRU.Len() > maxItems {
		oldest := moderationCacheLRU.Back()
		if oldest == nil {
			break
		}
		oldEntry := oldest.Value.(*moderationCacheEntry)
		moderationCacheLRU.Remove(oldest)
		delete(moderationCacheMap, oldEntry.key)
	}
}

// FlushModerationContentCache fix MAJOR R23-M2：admin 改 moderation_* 配置后调，
// 让所有缓存的审核结论立即失效（与 InvalidateKeywordFilterCache 同模式）。
func FlushModerationContentCache() {
	moderationCacheMu.Lock()
	defer moderationCacheMu.Unlock()
	moderationCacheMap = make(map[string]*moderationCacheEntry)
	moderationCacheLRU = list.New()
}

// ─── HTTP 客户端（复用 ssrfSafeDialContext）────────────────────────

var moderationHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext:     ssrfSafeDialContext, // 复用 yifut_client 的 SSRF 防御 Dialer
		IdleConnTimeout: 90 * time.Second,
	},
}

// openaiModerationRequest OpenAI Moderation API 请求体
type openaiModerationRequest struct {
	Model string `json:"model"`
	// Input 可以是 string 或 []map[string]any（multimodal）
	Input any `json:"input"`
}

// openaiModerationResponse OpenAI 返回（关心 results[0].categories + category_scores）
type openaiModerationResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Results []struct {
		Flagged        bool               `json:"flagged"`
		Categories     map[string]bool    `json:"categories"`
		CategoryScores map[string]float64 `json:"category_scores"`
	} `json:"results"`
}

// CheckContent 同步审核 prompt + 可选 image URLs。
//
// 输入：
//   - prompt: 已 ExtractPromptText 拼好的全文（可能很长）
//   - imageURLs: image_policy=submit 时一并送审；skip/reject 时调用方应在外层处理
//
// 行为：
//   - 长 prompt 按 ChunkChars 分块，每块单独调 API；任一块 flagged 即返回 flagged=true
//   - 缓存（HMAC key）：相同 prompt + 相同策略短期内直接命中
//   - 失败：返回 ModerationResult{Flagged:false, Err:err}；调用方根据 FailMode 决定
//   - cfg.IsConfigured()==false：直接 noop 通过（admin 没填 key）
func CheckContent(ctx context.Context, prompt string, imageURLs []string, cfg ModerationConfig) ModerationResult {
	if !cfg.IsConfigured() {
		return ModerationResult{Flagged: false, Err: errors.New("moderation not configured")}
	}
	// 上限保护：超过 max_chars 直接 fail-closed（让调用方拒绝）
	maxRunes := cfg.MaxChars
	if maxRunes <= 0 {
		maxRunes = 262144
	}
	if cnt := utf8RuneCountInString(prompt); cnt > maxRunes {
		return ModerationResult{
			Flagged: false,
			Err:     fmt.Errorf("prompt too long: %d runes > limit %d", cnt, maxRunes),
		}
	}

	// 分块（ChunkChars 一段，最多 MaxChunks 块）
	chunkSize := cfg.ChunkChars
	if chunkSize <= 0 {
		chunkSize = 28672
	}
	maxChunks := cfg.MaxChunks
	if maxChunks <= 0 {
		maxChunks = 8
	}
	chunks, truncated := splitIntoChunks(prompt, chunkSize, maxChunks)
	if truncated {
		// fix CRITICAL R23-C4：超过 chunkSize × maxChunks 的 prompt 不能"前 N 块过审就放行"——
		// 攻击者可把违规内容塞尾部绕过。runner 层 max_chars 已先拦一道，能到这里说明
		// admin 配置了不一致的值，给一个明确的错误信号让其 fail-closed 处理。
		return ModerationResult{
			Flagged:  false,
			Err:      fmt.Errorf("prompt too long: rune_count > chunk_chars (%d) × max_chunks (%d) — adjust moderation_max_chars / chunk / max", chunkSize, maxChunks),
			Endpoint: cfg.Endpoint,
		}
	}
	if len(chunks) == 0 && len(imageURLs) == 0 {
		// 空 prompt + 无图 → 直接通过
		return ModerationResult{Flagged: false}
	}

	threshold := cfg.Threshold
	if threshold <= 0 || threshold > 1 {
		threshold = 0.8
	}

	// 任一分块或 image flagged 即整体 flagged
	for i, chunk := range chunks {
		// 缓存优先
		key := computeCacheKey(chunk, cfg.Model, threshold)
		if cached, ok := cacheGet(key); ok {
			if cached.Flagged {
				return cached
			}
			continue // 已审过且未命中
		}
		// 调 API
		res, err := callOpenAIModeration(ctx, chunk, nil, cfg)
		if err != nil {
			return ModerationResult{
				Flagged: false,
				Err:     fmt.Errorf("chunk %d/%d: %w", i+1, len(chunks), err),
				Endpoint: cfg.Endpoint,
			}
		}
		// 命中阈值判定
		evalThreshold(&res, threshold)
		// 写缓存
		cachePut(key, res, time.Duration(cfg.CacheTTLSec)*time.Second, cfg.CacheMaxItems)
		if res.Flagged {
			return res
		}
	}

	// image_policy=submit 时审核 image_url（调一次，把所有图一起送）
	if cfg.ImagePolicy == "submit" && len(imageURLs) > 0 {
		key := computeCacheKey("__images__:"+strings.Join(imageURLs, "|"), cfg.Model, threshold)
		if cached, ok := cacheGet(key); ok {
			if cached.Flagged {
				return cached
			}
		} else {
			res, err := callOpenAIModeration(ctx, "", imageURLs, cfg)
			if err != nil {
				return ModerationResult{Flagged: false, Err: fmt.Errorf("image moderation: %w", err), Endpoint: cfg.Endpoint}
			}
			evalThreshold(&res, threshold)
			cachePut(key, res, time.Duration(cfg.CacheTTLSec)*time.Second, cfg.CacheMaxItems)
			if res.Flagged {
				return res
			}
		}
	}

	return ModerationResult{Flagged: false, Endpoint: cfg.Endpoint}
}

// callOpenAIModeration 单次调用 API（一个 chunk 或一组 image）
func callOpenAIModeration(ctx context.Context, text string, imageURLs []string, cfg ModerationConfig) (ModerationResult, error) {
	var input any
	if len(imageURLs) == 0 {
		input = text
	} else {
		// multimodal：input = [{type:"text",text:"..."}, {type:"image_url",image_url:{url:"..."}}]
		items := []map[string]any{}
		if text != "" {
			items = append(items, map[string]any{"type": "text", "text": text})
		}
		for _, u := range imageURLs {
			items = append(items, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
		}
		input = items
	}
	body, err := json.Marshal(openaiModerationRequest{Model: cfg.Model, Input: input})
	if err != nil {
		return ModerationResult{}, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return ModerationResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := moderationHTTPClient.Do(req)
	if err != nil {
		return ModerationResult{}, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return ModerationResult{}, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return ModerationResult{}, fmt.Errorf("api status %d: %s", resp.StatusCode, string(respBody))
	}
	var parsed openaiModerationResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ModerationResult{}, fmt.Errorf("parse response: %w", err)
	}
	if len(parsed.Results) == 0 {
		return ModerationResult{}, fmt.Errorf("empty results array")
	}
	r := parsed.Results[0]
	return ModerationResult{
		Flagged:    r.Flagged, // OpenAI 自己的 flagged，再用 threshold 二次判定
		Categories: r.CategoryScores,
		Endpoint:   cfg.Endpoint,
	}, nil
}

// evalThreshold 基于本地阈值二次判定 + 找最高分类别
func evalThreshold(res *ModerationResult, threshold float64) {
	if res.Categories == nil {
		return
	}
	hi := 0.0
	hiCat := ""
	for cat, score := range res.Categories {
		if score > hi {
			hi = score
			hiCat = cat
		}
	}
	res.HighestCat = hiCat
	res.HighestScore = hi
	// OpenAI flagged 优先；本地阈值兜底
	if !res.Flagged && hi >= threshold {
		res.Flagged = true
	}
}

// splitIntoChunks 按 rune 切分长 prompt（不切坏 UTF-8）。
//
// 返回 truncated=true 表示 prompt rune 数超过 chunkRunes × maxChunks，已截断。
// 调用方必须按 fail-closed 处理 —— 不能把截断后的部分送审就放行（攻击者可把违规
// 内容塞尾部，前 N 块过审拿到 flagged=false 后整条放行）。
func splitIntoChunks(s string, chunkRunes, maxChunks int) (chunks []string, truncated bool) {
	if s == "" || chunkRunes <= 0 {
		return nil, false
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i += chunkRunes {
		end := i + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
		if len(chunks) >= maxChunks {
			// 还有未消化的 rune → 真截断
			if end < len(runes) {
				truncated = true
			}
			break
		}
	}
	return chunks, truncated
}

func utf8RuneCountInString(s string) int {
	// 走标准库 utf8.RuneCountInString
	count := 0
	for range s {
		count++
	}
	return count
}
