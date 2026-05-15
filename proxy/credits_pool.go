// Package proxy / credits_pool.go
//
// 平台号池额度采集模块。
//
// 职责：
//   - 周期性向 CLIProxyAPI 拉取所有凭证清单（GET /v0/management/auth-files）
//   - 对每个凭证按 provider 通过 CPA 通用代理（POST /v0/management/api-call）
//     调对应上游配额查询接口（Anthropic / Antigravity / Codex / Gemini-CLI / Kimi）
//   - 解析响应为统一 Credit 结构存内存缓存
//   - 失败的进入重试队列，按管理员配置的间隔（指数退避）重试到耗尽 max_retries
//   - admin 看板拉详情，普通用户看模型维度的聚合 summary
//
// 配置项（通过 SysConfig 表，admin UI 维护）：
//   - cliproxy_url: CPA 服务地址，默认 http://127.0.0.1:8080
//   - cliproxy_key: CPA management key
//   - credits_refresh_interval: 全量刷新周期（分钟），默认 15
//   - credits_max_retries: 单凭证最大失败重试次数，默认 3，0=无限重试（仍带指数退避封顶）
//   - credits_retry_interval: 失败后基础重试间隔（分钟），默认 5；指数退避封顶 60 分钟
//   （Antigravity 的 GCP project_id 不再走 SysConfig：由 syncCPACredentials
//    自动从 CPA 凭证文件 cloudaicompanionProject 字段提取，按凭证粒度缓存到
//    cpa_credentials 表）

package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"daof-ai-hub/database"

	"github.com/tidwall/gjson"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ─── 数据结构 ─────────────────────────────────────────────────────────────

// CreditWindow 是单个滚动配额窗口的数据。
// 例如 Anthropic 的 "five_hour" / "seven_day_sonnet"，Codex 的 "weekly"，等。
type CreditWindow struct {
	ID               string    `json:"id"`                  // 窗口标识："five-hour"、"seven-day"、"weekly" 等
	Label            string    `json:"label"`               // 中文展示名
	UsedPercent      float64   `json:"used_percent"`        // 已用百分比（0-100）
	RemainingPercent float64   `json:"remaining_percent"`   // 剩余百分比（0-100）
	ResetsAt         time.Time `json:"resets_at,omitempty"` // 重置时间
	HasNumeric       bool      `json:"has_numeric"`         // 是否有数字额度（Antigravity / Kimi 才有）
	CreditAmount     float64   `json:"credit_amount,omitempty"`
	CreditMin        float64   `json:"credit_min,omitempty"`
}

// CreditEntry 是某个凭证的完整额度信息
type CreditEntry struct {
	AuthID    string `json:"auth_id"`
	AuthIndex string `json:"auth_index"`
	FileName  string `json:"file_name"`
	Provider  string `json:"provider"`            // "claude" / "antigravity" / "codex" / "gemini-cli" / "kimi"
	PlanType  string `json:"plan_type,omitempty"` // Codex: "pro" / "plus" / "free"; Anthropic: "Max" / "Pro"
	Status    string `json:"status"`              // "active" / "disabled" / "unavailable"
	Email     string `json:"email,omitempty"`
	// ProjectID 仅 antigravity / gemini-cli 有值，从 cpa_credentials 表注入。
	// fix Go-HIGH1：必须缓存到 entry 才能让 retryFailedCredits 重建 authFileLite
	// 时不丢，否则 antigravity / gemini-cli 重试永远 "project_id 缺失" 失败。
	ProjectID   string         `json:"-"` // 不对外暴露
	Models      []string       `json:"models,omitempty"`
	Windows     []CreditWindow `json:"windows"`
	LastRefresh time.Time      `json:"last_refresh"`
	LastError   string         `json:"last_error,omitempty"`
	RetryCount  int            `json:"retry_count"`
	NextRetryAt time.Time      `json:"next_retry_at,omitempty"`
	Healthy     bool           `json:"healthy"`
}

// ─── 全局缓存 + 运行状态 ──────────────────────────────────────────────────

var (
	creditsMu       sync.RWMutex
	creditsCache    = map[string]*CreditEntry{} // key=AuthID
	creditsLastFull time.Time                   // 最近一次全量刷新完成时间

	// 用 atomic.Bool 替代裸 bool，让 controller 层能在不持有 map 锁的情况下做 in-flight 检查
	creditsRefreshing atomic.Bool

	creditsCtx    context.Context
	creditsCancel context.CancelFunc
	// fix MAJOR M23-A5（codex 第二十三轮）：让 StopCreditsPool 真正等到 goroutine 退出
	creditsWG sync.WaitGroup

	startOnce sync.Once
	stopOnce  sync.Once


	// CPA 通用代理调用：30s 超时，连接池避免高频刷新时端口耗尽
	cpaHTTPClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			MaxConnsPerHost:     50,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	// CPA auth-files 列表查询：相对轻量，单独 client
	cpaAuthFilesClient = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 5,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	// Bearer token 脱敏正则
	bearerRe = regexp.MustCompile(`(?i)Bearer\s+\S+`)
	// 通用 token 关键字脱敏：覆盖 access/refresh/id_token、api_key、secret、authorization
	// download 凭证 JSON 失败路径里若错误体回显原文，需要这层防御
	apiKeyRe = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|secret|authorization)["'\s:=]+[A-Za-z0-9._/+=-]{8,}`)
	// fix Sec-M1: bare JWT pattern (eyJ.<base64url>.<base64url>) 兜底——
	// 上游错误体偶尔会裸吐 token 没带 key 名（HTTP 401 body 等），需要专门正则捕获。
	jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)
)

// 单条错误响应 / 日志体最大字节，超出会被截断
const errorBodyMaxBytes = 4096

// 单条响应体读取上限：防止恶意上游返回 GB 级 body 把内存吃光
const responseBodyMaxBytes = 1 * 1024 * 1024 // 1 MiB

// ─── 启动 / 停止 ──────────────────────────────────────────────────────────

// StartCreditsPool 在后台启动周期刷新 goroutine。在 main.go 启动时调用一次。
// 用 sync.Once 保证多次调用安全。
//
// fix MAJOR M23-A5（codex 第二十三轮）：用 WaitGroup 让 StopCreditsPool 真正等到 goroutine 退出。
// 原实现只 cancel context 立即返回，goroutine 可能还在执行 refreshAllCreditsSafe（HTTP 调用 + DB 写）
// 中途被进程杀死，导致 cpa_credentials 表中状态损坏 / HTTP 连接被强制 reset。
func StartCreditsPool() {
	startOnce.Do(func() {
		creditsCtx, creditsCancel = context.WithCancel(context.Background())

		creditsWG.Add(2)
		go func() {
			defer creditsWG.Done()
			// fix Go-H1：用 NewTimer 替代 time.After，否则 refreshAllCreditsSafe
			// 比 interval 长时（100+ 凭证场景下完全可能），上一轮的 timer goroutine
			// 还活着新一轮就启动新 timer —— 长期运行下计时器 goroutine 累积泄漏。
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-timer.C:
					refreshAllCreditsSafe(creditsCtx)
					timer.Reset(getRefreshIntervalDuration())
				case <-creditsCtx.Done():
					return
				}
			}
		}()

		// 独立的重试 goroutine：每分钟扫一次重试队列
		go func() {
			defer creditsWG.Done()
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					retryFailedCredits(creditsCtx)
				case <-creditsCtx.Done():
					return
				}
			}
		}()

		log.Println("📊 号池额度采集器已启动")
	})
}

// StopCreditsPool 在 server 关闭时调用，等待 goroutine 真正退出。
//
// fix MAJOR M23-A5（codex 第二十三轮）：cancel + WaitGroup.Wait()，
// 让 SIGTERM 路径在 main 退出前确认所有 credits_pool goroutine 已 drain。
func StopCreditsPool() {
	stopOnce.Do(func() {
		if creditsCancel != nil {
			creditsCancel()
		}
		creditsWG.Wait()
	})
}

// IsRefreshing 暴露给 controller 层做 in-flight 检查，避免重复启动 goroutine
func IsRefreshing() bool {
	return creditsRefreshing.Load()
}

// ─── SysConfig 配置读取 ──────────────────────────────────────────────────

func getRefreshIntervalDuration() time.Duration {
	mins := getIntConfig("credits_refresh_interval", 15)
	if mins < 1 {
		mins = 15
	}
	return time.Duration(mins) * time.Minute
}

func getRetryIntervalDuration() time.Duration {
	mins := getIntConfig("credits_retry_interval", 5)
	if mins < 1 {
		mins = 5
	}
	return time.Duration(mins) * time.Minute
}

func getMaxRetries() int {
	return getIntConfig("credits_max_retries", 3) // 0 = unlimited (仍带指数退避封顶)
}

// 注：原导出 GetCliproxyURL 已删除（grep 确认无外部调用方）。
// 包内统一走小写 getCliproxyURL()，避免无意义扩大配置读取面。
// 未来如需外部 controller 用于诊断，应该返回脱敏版（去 userinfo）。

// 注：原来的 public GetCliproxyKey 已删除——没有外部调用方。
// 包内调用统一走小写 getCliproxyKey() 即可，避免无意义扩大密钥泄露面。

// IsCliproxyConfigured 判断 admin 是否已经把 CPA 配置填齐。
// 这里不强制要求 key 必须非空（CPA 允许无鉴权部署），仅校验 URL 可被解析。
func IsCliproxyConfigured() bool {
	// fix Codex-M5：必须持读锁。SyncCacheConfig 会持写锁整体替换 map，
	// 这里裸读会触发 Go map data race（go race detector 必报）
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["cliproxy_url"])
	SysConfigMutex.RUnlock()
	return v != ""
}

// PingCliproxy 同步探测 CPA 是否可达。返回 nil 表示连通；error 文本可直接展示给 admin。
// 超时短（5s）便于 UI 等待；只 GET /v0/management/auth-files 头部，不读取 body。
func PingCliproxy(ctx context.Context) error {
	url := getCliproxyURL() + "/v0/management/auth-files"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	if k := getCliproxyKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	cli := &http.Client{Timeout: 5 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 CPA (%s): %w", getCliproxyURL(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("CPA 鉴权失败 (HTTP %d)，请检查 cliproxy_key", resp.StatusCode)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("CPA 上游异常 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("CPA 响应非预期 (HTTP %d)", resp.StatusCode)
	}
	return nil
}

func getCliproxyURL() string {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["cliproxy_url"])
	SysConfigMutex.RUnlock()
	if v == "" {
		v = "http://127.0.0.1:8080"
	}
	return strings.TrimRight(v, "/")
}

func getCliproxyKey() string {
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()
	return SysConfigCache["cliproxy_key"]
}

// 注：旧的 getAntigravityProjectID 已删除。
// project_id 现在从 cpa_credentials 表（CPACredential.ProjectID）按凭证读，
// 由 syncCPACredentials 在每个刷新周期自动同步。详见上文。

func getIntConfig(key string, def int) int {
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache[key])
	SysConfigMutex.RUnlock()
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// ─── 工具：安全限读 + 错误脱敏 ────────────────────────────────────────────

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

// sanitizeError 把上游返回的错误体里的 Bearer token / api_key 抹掉再截断。
// 即使 admin 是可信角色，错误也会进日志，避免敏感字段写盘。
// SanitizeErrorMessage 是 sanitizeError 的导出版本，给 controller 层
// 在写 HTTP 响应 body 之前清洗错误消息（防把内部错误细节回显给客户端）。
func SanitizeErrorMessage(s string, maxLen int) string { return sanitizeError(s, maxLen) }

func sanitizeError(s string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = errorBodyMaxBytes
	}
	s = bearerRe.ReplaceAllString(s, "Bearer ***")
	s = apiKeyRe.ReplaceAllString(s, "$1=***")
	s = jwtRe.ReplaceAllString(s, "[JWT]")
	if len(s) > maxLen {
		s = s[:maxLen] + "...(truncated)"
	}
	return s
}

// ─── 主刷新流程 ───────────────────────────────────────────────────────────

func refreshAllCreditsSafe(ctx context.Context) {
	// CompareAndSwap：原子地占位，失败说明已有刷新在跑
	if !creditsRefreshing.CompareAndSwap(false, true) {
		return
	}
	defer creditsRefreshing.Store(false)

	defer func() {
		if r := recover(); r != nil {
			// fix Sec-HIGH2：万一 panic 值携带凭证字节（理论上不应该，但防御纵深）
			// 经 sanitizeError 过滤 Bearer / token / api_key 等敏感字符串后再写日志
			log.Printf("[CREDITS] panic in refreshAllCredits: %s", sanitizeError(fmt.Sprintf("%v", r), 500))
		}
	}()

	authFiles, err := fetchAuthFiles(ctx)
	if err != nil {
		log.Printf("[CREDITS] fetch auth files failed: %s", sanitizeError(err.Error(), 500))
		return
	}

	// 同步元数据到 DB（首次见到的凭证 download 提 project_id；本轮消失的软删；状态变化更新）
	credentialsByID := syncCPACredentials(ctx, authFiles)

	// 仅刷新启用的凭证（CPA disabled=true 跳过，避免对失效凭证做无意义的 quota 探测）
	activeAuthFiles := make([]authFileLite, 0, len(authFiles))
	for _, af := range authFiles {
		if af.Disabled {
			continue
		}
		activeAuthFiles = append(activeAuthFiles, af)
	}

	log.Printf("[CREDITS] 开始全量刷新，CPA 共 %d 个凭证（启用 %d / 禁用 %d）",
		len(authFiles), len(activeAuthFiles), len(authFiles)-len(activeAuthFiles))

	// 记录本轮见到的 ID（仅启用），用于回收 cache 中已被 CPA 删除/禁用的条目
	seen := make(map[string]bool, len(activeAuthFiles))

	for _, af := range activeAuthFiles {
		if ctx.Err() != nil {
			return
		}
		// 把本地 cache 里的 project_id 注入到 af（fetchAntigravityQuota 直接读，不再查 DB）
		if cred, ok := credentialsByID[af.ID]; ok && cred != nil {
			af.ProjectID = cred.ProjectID
		}
		// 读取上一次的累积重试计数，用于失败时保留指数退避状态
		creditsMu.RLock()
		prev, hadPrev := creditsCache[af.ID]
		var prevRetryCount int
		if hadPrev {
			prevRetryCount = prev.RetryCount
		}
		creditsMu.RUnlock()

		entry := refreshOneAuthCredits(ctx, af)
		// 失败时累加重试计数，避免每轮全量刷新把 retry_count 重置为 1 破坏指数退避
		if entry.LastError != "" {
			entry.RetryCount = prevRetryCount + 1
			entry.NextRetryAt = computeNextRetryAt(entry.RetryCount)
		}
		creditsMu.Lock()
		creditsCache[entry.AuthID] = entry
		creditsMu.Unlock()
		seen[entry.AuthID] = true
	}

	// 清理已被 CPA 删除的旧条目
	creditsMu.Lock()
	staleCount := 0
	for k := range creditsCache {
		if !seen[k] {
			delete(creditsCache, k)
			staleCount++
		}
	}
	creditsLastFull = time.Now()
	creditsMu.Unlock()

	log.Printf("[CREDITS] 全量刷新完成，已回收 %d 个废弃条目", staleCount)
}

// RefreshAllCreditsNow 同步触发一次全量刷新（admin 手动按钮调用）。
// 阻塞直到完成（或 ctx cancel）。已有刷新进行中时直接返回，不阻塞调用方。
func RefreshAllCreditsNow(ctx context.Context) {
	refreshAllCreditsSafe(ctx)
}

// retryFailedCredits 扫描失败队列，按指数退避重试。
// 即使 max_retries=0（无限），单凭证间隔也会指数退避封顶 60 分钟，避免雪崩。
//
// 并发安全：全量刷新进行中时跳过本轮重试，避免两个 goroutine 并发写同一 entry
// 互相覆盖刷新结果（refreshAllCreditsSafe 的循环也会重新尝试这些失败 entry）。
func retryFailedCredits(ctx context.Context) {
	if creditsRefreshing.Load() {
		// 全量刷新中，让出。它会顺便处理失败队列里的 entry。
		return
	}

	maxRetries := getMaxRetries()
	now := time.Now()

	creditsMu.RLock()
	// 复制 entry 快照而非持有指针，避免读锁释放后被并发修改
	type pendingItem struct {
		af         authFileLite
		retryCount int
	}
	pending := []pendingItem{}
	for _, e := range creditsCache {
		if e.LastError == "" {
			continue
		}
		if !e.NextRetryAt.IsZero() && now.Before(e.NextRetryAt) {
			continue
		}
		if maxRetries > 0 && e.RetryCount >= maxRetries {
			continue // 已耗尽，等下次全量周期
		}
		pending = append(pending, pendingItem{
			af: authFileLite{
				ID:        e.AuthID,
				AuthIndex: e.AuthIndex,
				FileName:  e.FileName,
				Provider:  e.Provider,
				Status:    e.Status,
				Email:     e.Email,
				// fix Go-HIGH1：必须复制 ProjectID，否则 antigravity / gemini-cli
				// 的 fetcher 立刻 "project_id 缺失" 失败，重试永远不可能成功
				ProjectID: e.ProjectID,
			},
			retryCount: e.RetryCount,
		})
	}
	creditsMu.RUnlock()

	if len(pending) == 0 {
		return
	}

	log.Printf("[CREDITS] 重试 %d 个失败凭证", len(pending))
	for _, p := range pending {
		if ctx.Err() != nil {
			return
		}
		// 中途若全量刷新启动，立即让出
		if creditsRefreshing.Load() {
			return
		}
		newEntry := refreshOneAuthCredits(ctx, p.af)
		// 复用旧的重试计数累加，配合指数退避
		if newEntry.LastError != "" {
			newEntry.RetryCount = p.retryCount + 1
			newEntry.NextRetryAt = computeNextRetryAt(newEntry.RetryCount)
		}
		creditsMu.Lock()
		// 写回前再次检查 cache 里该条目是否已被全量刷新更新；
		// 如果已经成功（LastError == ""），不要回滚成功状态。
		if existing, ok := creditsCache[newEntry.AuthID]; ok {
			if existing.LastError == "" && existing.LastRefresh.After(newEntry.LastRefresh) {
				creditsMu.Unlock()
				continue
			}
		}
		creditsCache[newEntry.AuthID] = newEntry
		creditsMu.Unlock()
	}
}

// computeNextRetryAt：指数退避，基础间隔 * 2^(retryCount-1)，封顶 60 分钟。
// 即使 max_retries=0 也通过这个 cap 防止持续冲击上游。
func computeNextRetryAt(retryCount int) time.Time {
	base := getRetryIntervalDuration()
	const maxDelay = 60 * time.Minute

	// 防止 1 << retryCount 溢出
	if retryCount < 1 {
		retryCount = 1
	}
	if retryCount > 10 {
		retryCount = 10
	}
	delay := base * time.Duration(1<<(retryCount-1))
	if delay > maxDelay {
		delay = maxDelay
	}
	return time.Now().Add(delay)
}

// ─── 调用 CPA：拉凭证清单 ─────────────────────────────────────────────────

type authFileLite struct {
	ID        string
	AuthIndex string
	FileName  string
	Provider  string
	Status    string
	Disabled  bool
	Email     string
	IDToken   map[string]any // Codex 才有
	// ProjectID 仅 antigravity 有值（refreshAllCreditsSafe 从 cpa_credentials 表注入）
	// 不在这里直接查 DB——避免每轮多次 DB 查询；由 syncCPACredentials 一次拉好。
	ProjectID string
}

func fetchAuthFiles(ctx context.Context) ([]authFileLite, error) {
	url := getCliproxyURL() + "/v0/management/auth-files"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("CPA auth-files NewRequest 失败: %w", err)
	}
	if k := getCliproxyKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := cpaAuthFilesClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CPA auth-files 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := readLimited(resp.Body, errorBodyMaxBytes)
		return nil, fmt.Errorf("CPA auth-files HTTP %d: %s", resp.StatusCode, sanitizeError(string(body), errorBodyMaxBytes))
	}

	body, err := readLimited(resp.Body, responseBodyMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("读取 CPA auth-files 响应失败: %w", err)
	}

	var raw struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("解析 CPA auth-files JSON 失败: %w", err)
	}

	out := make([]authFileLite, 0, len(raw.Files))
	for _, f := range raw.Files {
		af := authFileLite{
			ID:        anyToStr(f["id"]),
			AuthIndex: anyToStr(f["auth_index"]),
			FileName:  anyToStr(f["name"]),
			Provider:  strings.ToLower(anyToStr(f["provider"])),
			Status:    anyToStr(f["status"]),
			Email:     anyToStr(f["email"]),
		}
		if d, ok := f["disabled"].(bool); ok {
			af.Disabled = d
		}
		if it, ok := f["id_token"].(map[string]any); ok {
			af.IDToken = it
		}
		out = append(out, af)
	}
	return out, nil
}

// ─── 凭证元数据增量同步（CPACredential 表） ─────────────────────────────
//
// 设计：fetchAuthFiles 拿到清单后立即调 syncCPACredentials 做 diff：
//   - 新出现的 auth_id（DB 里没有）→ download 凭证 JSON，提 project_id 入库
//   - DB 里有但本轮清单不见的 → UPDATE disabled=true（软删，不物理删，便于审计）
//   - 状态/disabled 变化 → UPDATE 单字段
//   - 已知 auth_id 且 LastDownloadedAt 较新 → 跳过 download（project_id 极少变）
//
// 这里只缓存 project_id 这种"静态长期不变"的字段。access_token 由 CPA 管理，
// daof-ai-hub 走 api-call 透明代理调上游时 CPA 会自己注入最新 token。

// fetchAuthFileContent 从 CPA 下载某个凭证的完整 JSON 内容
func fetchAuthFileContent(ctx context.Context, name string) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("file name empty")
	}
	url := getCliproxyURL() + "/v0/management/auth-files/download?name=" + urlQueryEscape(name)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("download %s NewRequest: %w", name, err)
	}
	if k := getCliproxyKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := cpaAuthFilesClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s 请求失败: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := readLimited(resp.Body, errorBodyMaxBytes)
		return nil, fmt.Errorf("download %s HTTP %d: %s", name, resp.StatusCode, sanitizeError(string(body), errorBodyMaxBytes))
	}
	body, err := readLimited(resp.Body, responseBodyMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("read download %s: %w", name, err)
	}
	return body, nil
}

// gcpProjectIDRe — GCP project ID 官方格式：6-30 字符，小写字母开头，仅字母数字 hyphen，不以 hyphen 结尾。
// 用作 sanity 检查防止恶意凭证注入污染 DB / 上游请求体。
var gcpProjectIDRe = regexp.MustCompile(`^[a-z][-a-z0-9]{4,28}[a-z0-9]$`)

// parseProjectIDFromAuthJSON 从 antigravity 凭证 JSON 里解析 GCP project_id。
// CPA 在凭证文件里把 project_id 存在多个位置（不同版本/不同登录路径）：
//   - top-level "cloudaicompanionProject" (object, 取 .id) ← 优先：避免 string 候选把整个 JSON 文本误认为 ID
//   - top-level "cloudaicompanionProject" (string)
//   - "metadata.project_id"
//   - "project_id"（兜底）
//
// fix Go-MEDIUM1：原本 .id 在第二位，第一位 string 候选若是 object，gjson String() 会
// 返回原始 JSON 文本（如 `{"id":"x"}`），把整段 JSON 当成 project_id 入库。
func parseProjectIDFromAuthJSON(data []byte) string {
	candidates := []string{
		"cloudaicompanionProject.id",
		"cloudaicompanionProject",
		"metadata.project_id",
		"project_id",
	}
	for _, k := range candidates {
		v := gjson.GetBytes(data, k)
		// 跳过 object/array：避免把容器节点 stringify 成原始 JSON 文本误入库
		if v.IsObject() || v.IsArray() {
			continue
		}
		if s := strings.TrimSpace(v.String()); s != "" {
			// fix Sec-M1：格式校验防止恶意凭证文件污染 DB / 上游请求体。
			// 不匹配 GCP project ID 规范的值视作无效（继续尝试下一个候选键）。
			if !gcpProjectIDRe.MatchString(s) {
				continue
			}
			return s
		}
	}
	return ""
}

// syncCPACredentials 把 CPA 当前的凭证清单与本地 cpa_credentials 表做 diff 同步。
// 在 refreshAllCreditsSafe 拿到 authFiles 后立即调用，先于具体 quota 拉取。
//
// 返回：authID → CPACredential 快照（quota fetcher 后续读这个，不再单独查 DB）
func syncCPACredentials(ctx context.Context, authFiles []authFileLite) map[string]*database.CPACredential {
	now := time.Now()
	out := make(map[string]*database.CPACredential, len(authFiles))

	// 1) 一次性拉取本地所有 CPA 凭证缓存到 map（避免 N 次 DB 查询）
	// fix Go-HIGH1：失败必须 abort——继续执行会让 localByID 为空，
	// 所有 antigravity 凭证 needDownload=true 形成 stampede，
	// 还会用空 ProjectID 通过 Save 全量 UPDATE 覆盖 DB 里的有效值。
	var local []database.CPACredential
	if err := database.DB.Find(&local).Error; err != nil {
		// fix Go-C3：GORM 错误可能含 SQL/连接串碎片
		log.Printf("[CPA-CRED] load local cache failed, skipping sync this cycle: %s", sanitizeError(err.Error(), 300))
		return out
	}
	localByID := make(map[string]*database.CPACredential, len(local))
	for i := range local {
		localByID[local[i].AuthID] = &local[i]
	}

	// 2) 遍历本轮清单，决定每个 auth_id 是 INSERT / UPDATE / 跳过
	// 2) 第一遍：决定哪些 antigravity 凭证需要 download（按"已知 + project_id 空"的退避规则）
	//    download 阶段并发执行（worker pool cap），避免冷启动 N 个串行下载阻塞整轮刷新
	type pending struct {
		af       authFileLite
		existing *database.CPACredential
	}
	toDownload := make([]pending, 0, len(authFiles))
	seen := make(map[string]bool, len(authFiles))

	// 需要 project_id 的 provider：antigravity 和 gemini-cli 都调
	// google `cloudcode-pa.googleapis.com` 的 project-scoped 端点（fetchAvailableModels /
	// retrieveUserQuota），都依赖 cloudaicompanionProject 字段。
	//
	// fix Codex-M1：兼容 CPA 上不同版本对 gemini-cli 的命名（"gemini" / "gemini-cli" / "gemini_cli"）。
	// 如果不兼容，CPA 返回 "gemini" 时不会下载 project_id，fetchGeminiCliQuota 永久报缺失。
	needsProjectID := func(p string) bool {
		p = strings.ToLower(strings.TrimSpace(p))
		return p == "antigravity" || p == "gemini-cli" || p == "gemini" || p == "gemini_cli"
	}

	for _, af := range authFiles {
		if af.ID == "" {
			continue
		}
		seen[af.ID] = true
		existing := localByID[af.ID]
		if !needsProjectID(af.Provider) {
			continue
		}
		// fix Major（codex 复审）：原仅在 ProjectID 为空时重 download，已知凭证一旦缓存
		// project_id 永远不刷新。如果用户在 CPA 后台换绑了 GCP project（重新登录 OAuth 会改
		// cloudaicompanionProject）或 file_name 改变（重命名凭证），本地 ProjectID 永远是
		// 旧值，发到上游会 403/404。
		//
		// 补两条触发：
		//   (a) file_name 变化 → 文件改名通常意味着内容也换了，立即 re-download
		//   (b) projectIDRefreshInterval（默认 24h）周期内的 lazy refresh，覆盖
		//       "原文件改了但文件名不变" 的边缘场景
		switch {
		case existing == nil:
			// 新凭证：必须 download 才能拿到 project_id
			toDownload = append(toDownload, pending{af: af, existing: nil})
		case existing.ProjectID == "" && now.Sub(existing.LastDownloadedAt) > 5*time.Minute:
			// 已知但 project_id 仍空：5 分钟退避后重试
			toDownload = append(toDownload, pending{af: af, existing: existing})
		case existing.FileName != af.FileName:
			// 文件名变化：极可能是新文件覆盖了同 auth_id（CPA 重登 OAuth 会换 file_name）
			toDownload = append(toDownload, pending{af: af, existing: existing})
		default:
			// TTL 周期 refresh + jitter：抓 cloudaicompanionProject 字段被换绑的情况
			// fix Major（codex 第三轮）：用 jitter 摊薄 N 凭证同步到期的热点，
			// 防 cap=5 worker pool 排成单轮 100+ batch 拖慢 sync。
			interval := projectIDRefreshInterval()
			ttl := interval + projectIDRefreshJitter(af.ID, interval)
			if now.Sub(existing.LastDownloadedAt) > ttl {
				toDownload = append(toDownload, pending{af: af, existing: existing})
			}
		}
	}

	// 3) 并发 download（fix Go-MEDIUM2：cold-start burst）
	//    bounded worker pool 防止冷启动一次性向 CPA 发 50+ 请求
	type downloadResult struct {
		authID    string
		projectID string // download 失败为空（保持已知 ProjectID 不变，由 upsert 阶段决定）
		ok        bool   // download 是否成功（用于决定是否更新 LastDownloadedAt）
	}
	const downloadConcurrency = 5
	results := make(map[string]downloadResult, len(toDownload))
	if len(toDownload) > 0 {
		var wg sync.WaitGroup
		var mu sync.Mutex
		sem := make(chan struct{}, downloadConcurrency)
		for _, p := range toDownload {
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(p pending) {
				defer wg.Done()
				defer func() { <-sem }()
				data, err := fetchAuthFileContent(ctx, p.af.FileName)
				res := downloadResult{authID: p.af.ID}
				if err != nil {
					log.Printf("[CPA-CRED] download %s failed: %s", p.af.FileName, sanitizeError(err.Error(), 200))
				} else {
					res.ok = true
					res.projectID = parseProjectIDFromAuthJSON(data)
					// data 用完即弃；Go GC 后回收。本进程不持久化任何凭证字节
				}
				mu.Lock()
				results[p.af.ID] = res
				mu.Unlock()
			}(p)
		}
		wg.Wait()
	}

	// 4) 单一事务内批量 upsert（fix DB-HIGH2：N 次 Save 无事务）
	//    fix DB-HIGH1：用 OnConflict + AssignmentColumns 显式列出要更新的字段，
	//    避免 Save 全量 UPDATE 把空 Status / 错误零值覆盖掉 DB 里的有效值
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		for _, af := range authFiles {
			if af.ID == "" {
				continue
			}
			existing := localByID[af.ID]

			// 决定 ProjectID：优先用本轮 download 的成功结果，否则保留 existing
			projectID := ""
			if existing != nil {
				projectID = existing.ProjectID
			}
			if r, ok := results[af.ID]; ok && r.ok && r.projectID != "" {
				projectID = r.projectID
			}

			// LastDownloadedAt 现在表示 "最近一次尝试 download 的时刻"——成功或失败都更新。
			// fix Minor（gemini 第三轮）：原实现失败时保留旧值，导致 5 分钟退避永久失效——
			// 下次循环 `now.Sub(LastDownloadedAt) > 5min` 仍为真，对挂掉的 CPA 形成无退避刷屏。
			// 改为统一更新到 now：失败也算"刚试过"，5 分钟内不再重试同一凭证。
			// fix DB-MEDIUM3：非 antigravity 凭证从不 download，保持零值/existing 旧值
			lastDownloadedAt := time.Time{} // 零值表示从未尝试 download
			if existing != nil {
				lastDownloadedAt = existing.LastDownloadedAt
			}
			if _, ok := results[af.ID]; ok {
				// 本轮尝试过 download（成功或失败都算）→ 更新时间戳实现退避
				lastDownloadedAt = now
			}

			row := &database.CPACredential{
				AuthID:           af.ID,
				FileName:         af.FileName,
				Provider:         strings.ToLower(af.Provider),
				Email:            af.Email,
				ProjectID:        projectID,
				Disabled:         af.Disabled,
				Status:           af.Status,
				LastSeenAt:       now,
				LastDownloadedAt: lastDownloadedAt,
			}

			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "auth_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"file_name", "provider", "email", "project_id",
					"disabled", "status", "last_seen_at", "last_downloaded_at", "updated_at",
				}),
			}).Create(row).Error; err != nil {
				return fmt.Errorf("upsert %s: %w", af.ID, err)
			}
			out[af.ID] = row
		}

		// 5) 本轮清单里没出现的本地条目 → 软删（disabled=true）
		staleIDs := make([]string, 0)
		for id, row := range localByID {
			if !seen[id] && !row.Disabled {
				staleIDs = append(staleIDs, id)
			}
		}
		if len(staleIDs) > 0 {
			if err := tx.Model(&database.CPACredential{}).
				Where("auth_id IN ?", staleIDs).
				Update("disabled", true).Error; err != nil {
				return fmt.Errorf("soft-disable stale: %w", err)
			}
			log.Printf("[CPA-CRED] soft-disabled %d stale credentials no longer in CPA", len(staleIDs))
		}
		return nil
	})
	if txErr != nil {
		// fix Go-H2/Codex-MINOR：事务失败时降级到"上一轮的 cache"——
		// 用 localByID 构造 fallback result（已有 ProjectID 的旧记录），让 antigravity/
		// gemini-cli quota fetcher 继续用上轮已知 project_id 工作，避免本轮 DB 抖动
		// 把所有这两类凭证标记 unhealthy。
		// 同时 sanitize 错误信息——GORM 错误偶尔回显 SQL 片段
		log.Printf("[CPA-CRED] sync transaction failed, falling back to last cycle cache: %s",
			sanitizeError(txErr.Error(), 300))
		fallback := make(map[string]*database.CPACredential, len(localByID))
		for id, row := range localByID {
			if seen[id] {
				fallback[id] = row // 仅保留本轮 CPA 仍存在的凭证
			}
		}
		return fallback
	}

	return out
}

// urlQueryEscape 用标准库做 query 转义（凭证名可能含 @ : 等需要编码）
func urlQueryEscape(s string) string {
	return url.QueryEscape(s)
}

// projectIDRefreshInterval 控制已知凭证 ProjectID 的 TTL 刷新周期。
// 默认 24h —— 平衡 (1) CPA 凭证文件 IO 成本 (2) admin 换绑 GCP project 后的延迟可见性。
//
// 通过 SysConfig "cpa_project_id_refresh_seconds" 覆盖（最小 5 分钟，防止误填 0 触发风暴）。
func projectIDRefreshInterval() time.Duration {
	const defaultSec = 24 * 60 * 60
	const minSec = 5 * 60
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["cpa_project_id_refresh_seconds"])
	SysConfigMutex.RUnlock()
	if v == "" {
		return time.Duration(defaultSec) * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minSec {
		return time.Duration(defaultSec) * time.Second
	}
	return time.Duration(n) * time.Second
}

// projectIDRefreshJitter 给每个凭证一个**确定性**的过期偏移，把 N 凭证的同步到期
// 摊到 [interval, interval + 25%] 区间内，避免冷启动后 24h 整点 N 个并发 download。
//
// fix Major（codex 第三轮）：原实现所有凭证用同一 interval，500 个凭证会在同一轮全部
// 进入 toDownload，worker pool cap=5 意味着 100 个串行 batch，单轮 sync 时间剧增。
// 现在通过 sha256(auth_id) → 偏移百分比，把热点摊薄。
//
// 设计：
//   - 偏移确定性（同 auth_id 永远同偏移）→ 不会因为运气好/坏导致某个凭证永远不刷新
//   - 区间是 [0, interval/4]，加在 interval 之上 → 最坏情况 30h 才刷一次（24h * 1.25），可接受
func projectIDRefreshJitter(authID string, interval time.Duration) time.Duration {
	if authID == "" || interval <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(authID))
	// 取前 4 字节 → uint32 → 落到 [0, interval/4) 区间
	n := uint32(sum[0])<<24 | uint32(sum[1])<<16 | uint32(sum[2])<<8 | uint32(sum[3])
	jitterRangeSec := int64(interval/time.Second) / 4
	if jitterRangeSec <= 0 {
		return 0
	}
	return time.Duration(int64(n)%jitterRangeSec) * time.Second
}

// ─── 通过 CPA api-call 调上游 ─────────────────────────────────────────────

type apiCallResult struct {
	StatusCode int
	Body       []byte
	// CPA 返回的 header 字段在 daof 这边没有任何消费者（所有 fetcher 只读 Body+StatusCode）。
	// 移除该字段以减少每次调用的内存分配；如果将来需要可再加回来。
}

func cpaAPICall(ctx context.Context, authIndex, method, url string, headers map[string]string, body string) (*apiCallResult, error) {
	if authIndex == "" {
		return nil, fmt.Errorf("auth_index empty")
	}
	payload := map[string]any{
		"auth_index": authIndex,
		"method":     method,
		"url":        url,
		"header":     headers,
		"data":       body,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("CPA api-call marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", getCliproxyURL()+"/v0/management/api-call", bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("CPA api-call NewRequest 失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if k := getCliproxyKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := cpaHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("CPA api-call 请求失败: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := readLimited(resp.Body, responseBodyMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("读取 CPA api-call 响应失败: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("CPA api-call HTTP %d: %s", resp.StatusCode, sanitizeError(string(respBody), errorBodyMaxBytes))
	}

	var parsed struct {
		StatusCode int             `json:"status_code"`
		Body       json.RawMessage `json:"body"`
		BodyText   string          `json:"body_text"`
		// header 字段虽然 CPA 返回，但 daof 这边无消费者；为了让 json.Unmarshal 不因
		// 类型不匹配（map[string][]string）报错，仍然解析但用 RawMessage 占位丢弃
		Header json.RawMessage `json:"header"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("解析 CPA api-call JSON 失败: %w", err)
	}

	resBody := []byte(parsed.BodyText)
	if len(parsed.Body) > 0 && string(parsed.Body) != "null" {
		// body 字段可能是 JSON 对象也可能是 string
		var asStr string
		if err := json.Unmarshal(parsed.Body, &asStr); err == nil {
			resBody = []byte(asStr)
		} else {
			resBody = parsed.Body
		}
	}

	return &apiCallResult{
		StatusCode: parsed.StatusCode,
		Body:       resBody,
	}, nil
}

// ─── 单凭证刷新分发 ───────────────────────────────────────────────────────

func refreshOneAuthCredits(ctx context.Context, af authFileLite) *CreditEntry {
	entry := &CreditEntry{
		AuthID:      af.ID,
		AuthIndex:   af.AuthIndex,
		FileName:    af.FileName,
		Provider:    af.Provider,
		Status:      af.Status,
		Email:       af.Email,
		ProjectID:   af.ProjectID, // fix Go-HIGH1：让 retryFailedCredits 能拿到此值
		LastRefresh: time.Now(),
	}
	if af.Disabled || strings.EqualFold(af.Status, "disabled") {
		entry.Status = "disabled"
		entry.Healthy = false
		return entry
	}

	var err error
	switch af.Provider {
	case "claude", "anthropic":
		entry.Provider = "claude"
		err = fetchClaudeQuota(ctx, af, entry)
	case "antigravity":
		err = fetchAntigravityQuota(ctx, af, entry)
	case "codex":
		err = fetchCodexQuota(ctx, af, entry)
	case "gemini-cli", "gemini":
		entry.Provider = "gemini-cli"
		err = fetchGeminiCliQuota(ctx, af, entry)
	case "kimi":
		err = fetchKimiQuota(ctx, af, entry)
	default:
		err = fmt.Errorf("provider %q 暂不支持额度查询", af.Provider)
	}

	if err != nil {
		entry.LastError = sanitizeError(err.Error(), errorBodyMaxBytes)
		entry.Healthy = false
		// 注意：不在此处设置 RetryCount / NextRetryAt。
		// 由调用方决定累加策略：
		//   - refreshAllCreditsSafe：首次失败设为 1，保留之前累积的 RetryCount
		//   - retryFailedCredits：基于上一次的 RetryCount + 1
	} else {
		entry.LastError = ""
		entry.RetryCount = 0
		entry.NextRetryAt = time.Time{}
		entry.Healthy = computeHealthy(entry)
	}
	return entry
}

// providersWithoutNumericWindows 列出本来就没有数字配额窗口的 provider。
// 这些 provider 拉到 200 OK + token 有效 = healthy；其他 provider 必须有窗口
// 数据（否则视为"上游协议变化导致解析失败"，更安全的兜底是 unhealthy）。
//
// 当前没有任何 provider 真正属于这一类：
//   - claude / codex / antigravity / gemini-cli / kimi 都返回数字窗口
//
// 留空集 + 走严格判定即可。如果将来加 OpenRouter 之类无 quota API 的 provider，
// 在这里加白名单。
var providersWithoutNumericWindows = map[string]bool{}

// computeHealthy: 额度查询成功 + 至少一个窗口剩余 > 5%。
// CPA auth-file 的运行态 status 可能滞后（例如列表仍是 error，但 quota API
// 已经成功返回了窗口数据），因此这里以本次额度查询结果为准；disabled 仍然是硬
// 不健康。空窗口不再无条件 healthy=true——上游协议变化导致零解析时，应判
// unhealthy，让 admin 能看到异常而不是被默默过滤掉。
func computeHealthy(e *CreditEntry) bool {
	if strings.EqualFold(e.Status, "disabled") {
		return false
	}
	if e.LastError != "" {
		return false
	}
	if len(e.Windows) == 0 {
		// 严格判定：除非 provider 在白名单（无数字窗口契约），否则零窗口视为不健康
		return providersWithoutNumericWindows[strings.ToLower(e.Provider)]
	}
	for _, w := range e.Windows {
		if w.RemainingPercent > 5 {
			return true
		}
	}
	return false
}

// normalizeGoogleTierBadge 把 Google Cloud Code Assist 返回的 raw tier id
// 归一为 PRO / ULTRA / FREE / UNKNOWN，对齐 cockpit-tools 显示风格。
// 参考 src/types/gemini.ts:resolveGeminiPlanBucket
func normalizeGoogleTierBadge(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return "UNKNOWN"
	}
	if strings.Contains(lower, "ultra") {
		return "ULTRA"
	}
	if lower == "standard-tier" {
		return "FREE"
	}
	if strings.Contains(lower, "pro") || strings.Contains(lower, "premium") {
		return "PRO"
	}
	if lower == "free-tier" || strings.Contains(lower, "free") {
		return "FREE"
	}
	return "UNKNOWN"
}

// pickGoogleCodeAssistTier 从 loadCodeAssist 响应中按优先级抽取套餐：
// 1) paidTier.id（已付费）
// 2) currentTier.id（当前激活）
// 3) allowedTiers 中 isDefault=true 的 id（账号被授权使用的默认 tier）
// 返回经过 normalizeGoogleTierBadge 标准化后的字符串。
func pickGoogleCodeAssistTier(body []byte) string {
	var resp struct {
		PaidTier    *struct{ ID string `json:"id"` } `json:"paidTier"`
		CurrentTier *struct{ ID string `json:"id"` } `json:"currentTier"`
		AllowedTiers []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		} `json:"allowedTiers"`
	}
	if json.Unmarshal(body, &resp) != nil {
		return ""
	}
	var raw string
	switch {
	case resp.PaidTier != nil && resp.PaidTier.ID != "":
		raw = resp.PaidTier.ID
	case resp.CurrentTier != nil && resp.CurrentTier.ID != "":
		raw = resp.CurrentTier.ID
	default:
		for _, t := range resp.AllowedTiers {
			if t.IsDefault && t.ID != "" {
				raw = t.ID
				break
			}
		}
	}
	if raw == "" {
		return ""
	}
	return normalizeGoogleTierBadge(raw)
}

// ─── Provider Fetcher: Claude / Anthropic ────────────────────────────────

const (
	claudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
	claudeUsageURL   = "https://api.anthropic.com/api/oauth/usage"
)

// claudeWindowOrder 严格定义 Claude 窗口的展示顺序：
// 5 小时窗口最贴近用户当下的使用体验，必须排第一；之后是各种 7 天周期窗口。
// （之前用 map 遍历导致顺序随机，5h 可能掉到末尾——参照 CPA UI 的固定顺序。）
type claudeWindowDef struct {
	Key   string
	ID    string
	Label string
}

var claudeWindowOrder = []claudeWindowDef{
	{"five_hour", "five-hour", "5 小时限额"},
	{"seven_day", "seven-day", "7 天限额"},
	{"seven_day_oauth_apps", "seven-day-oauth-apps", "7 天 OAuth 应用"},
	{"seven_day_opus", "seven-day-opus", "7 天 Opus"},
	{"seven_day_sonnet", "seven-day-sonnet", "7 天 Sonnet"},
	{"seven_day_cowork", "seven-day-cowork", "7 天 Cowork"},
	{"iguana_necktie", "iguana-necktie", "Iguana Necktie"},
}

func fetchClaudeQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	headers := map[string]string{
		"Authorization":  "Bearer $TOKEN$",
		"Content-Type":   "application/json",
		"anthropic-beta": "oauth-2025-04-20",
	}
	// 1. 拉 profile（plan_type）
	// profile 失败不影响主流程（usage 才是核心数据），但要打日志便于排查 token 权限降级
	if pr, err := cpaAPICall(ctx, af.AuthIndex, "GET", claudeProfileURL, headers, ""); err != nil {
		log.Printf("[CREDITS] Claude profile auth=%s 失败: %s", af.AuthIndex, sanitizeError(err.Error(), 200))
	} else if pr.StatusCode != 200 {
		log.Printf("[CREDITS] Claude profile auth=%s HTTP %d", af.AuthIndex, pr.StatusCode)
	} else {
		// 同步 Cli-Proxy-API-Management-Center quotaConfigs.ts:resolveClaudePlanType
		// 优先级：has_claude_max → Max；has_claude_pro → Pro；
		//        organization_type=claude_team && subscription_status=active → Team；
		//        else → Free。
		// 旧实现读 account.plan_type / organization.plan_type — 这两个字段在
		// Anthropic /api/oauth/profile 响应里实际不存在，永远拿到空字符串 →
		// 前端 CreditsMonitor plan_type 角标完全不显示。
		var profile struct {
			Account struct {
				HasClaudeMax bool   `json:"has_claude_max"`
				HasClaudePro bool   `json:"has_claude_pro"`
				EmailAddress string `json:"email_address"`
			} `json:"account"`
			Organization struct {
				OrganizationType   string `json:"organization_type"`
				SubscriptionStatus string `json:"subscription_status"`
			} `json:"organization"`
		}
		if json.Unmarshal(pr.Body, &profile) == nil {
			switch {
			case profile.Account.HasClaudeMax:
				entry.PlanType = "Max"
			case profile.Account.HasClaudePro:
				entry.PlanType = "Pro"
			case strings.EqualFold(profile.Organization.OrganizationType, "claude_team") &&
				strings.EqualFold(profile.Organization.SubscriptionStatus, "active"):
				entry.PlanType = "Team"
			default:
				entry.PlanType = "Free"
			}
			if entry.Email == "" && profile.Account.EmailAddress != "" {
				entry.Email = profile.Account.EmailAddress
			}
		}
	}

	// 2. 拉 usage
	r, err := cpaAPICall(ctx, af.AuthIndex, "GET", claudeUsageURL, headers, "")
	if err != nil {
		return err
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("Claude usage HTTP %d: %s", r.StatusCode, sanitizeError(string(r.Body), errorBodyMaxBytes))
	}

	var usage map[string]any
	if err := json.Unmarshal(r.Body, &usage); err != nil {
		// fix MEDIUM M19-3（codex 第十九轮）：%v 丢失原始 error 类型 → 上层 errors.Is/As 判断失效。
		// 改 %w 让调用链可以根据 *json.SyntaxError 等具体类型做差异化处理。
		return fmt.Errorf("解析 Claude usage 失败: %w", err)
	}

	entry.Windows = nil
	// fix：按 claudeWindowOrder 固定顺序遍历，保证 5h 窗口永远排第一
	for _, def := range claudeWindowOrder {
		raw, ok := usage[def.Key].(map[string]any)
		if !ok {
			continue
		}
		entry.Windows = append(entry.Windows, claudeBuildWindow(def, raw))
	}
	entry.Models = []string{"claude-opus-4-5", "claude-sonnet-4-5", "claude-haiku-4-5"}
	return nil
}

func claudeBuildWindow(def claudeWindowDef, raw map[string]any) CreditWindow {
	w := CreditWindow{ID: def.ID, Label: def.Label}
	// Anthropic OAuth usage 的 utilization 表示已用百分比。管理页展示的是剩余，
	// 因此这里保留 used/remaining 两个字段，前端统一展示 remaining_percent。
	w.UsedPercent = parseUsedPercent(raw["utilization"])
	w.RemainingPercent = clampPct(100 - w.UsedPercent)
	if v, ok := raw["resets_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			w.ResetsAt = t
		}
	}
	return w
}

// parseUsedPercent 兼容两种 utilization 表示：
//   - 0~100 百分数（Anthropic OAuth 实测返回 `2.0` 表示已用 2%）
//   - 0~1 比例（防御兼容：`0.83` 表示已用 83%）
//
// 规则：
//   - f ∈ [0, 1]   → 比例形态：1.0 = 100%（满）
//   - f ∈ (1, 100] → 百分数形态：50 = 50%
//   - f > 100      → 超限百分数（少见，clampPct 会兜底）
func parseUsedPercent(v any) float64 {
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	if f <= 1.0 {
		return f * 100
	}
	return f
}

// clampPct 将值限定到 [0, 100]
func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// ─── Provider Fetcher: Antigravity ───────────────────────────────────────

var antigravityURLs = []string{
	"https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
	"https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
	"https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
}

type antigravityGroup struct {
	ID          string
	Label       string
	Identifiers []string
}

var antigravityGroups = []antigravityGroup{
	{"claude-gpt", "Claude/GPT", []string{"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium"}},
	{"gemini-3-pro", "Gemini 3 Pro", []string{"gemini-3-pro-high", "gemini-3-pro-low"}},
	{"gemini-3-1-pro-series", "Gemini 3.1 Pro Series", []string{"gemini-3.1-pro-high", "gemini-3.1-pro-low"}},
	{"gemini-2-5-flash", "Gemini 2.5 Flash", []string{"gemini-2.5-flash", "gemini-2.5-flash-thinking"}},
	{"gemini-2-5-flash-lite", "Gemini 2.5 Flash Lite", []string{"gemini-2.5-flash-lite"}},
	{"gemini-2-5-cu", "Gemini 2.5 CU", []string{"rev19-uic3-1p"}},
	{"gemini-3-flash", "Gemini 3 Flash", []string{"gemini-3-flash"}},
	{"gemini-image", "Gemini 3.1 Flash Image", []string{"gemini-3.1-flash-image"}},
}

func fetchAntigravityQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	// project_id 从 cpa_credentials 表注入到 af.ProjectID（每个凭证独立、自动同步）
	// 替代旧的 SysConfig 全局值——支持多账号、admin 零配置
	projectID := strings.TrimSpace(af.ProjectID)
	if projectID == "" {
		return fmt.Errorf("Antigravity 凭证 %s 的 project_id 缺失（CPA 凭证文件未含 cloudaicompanionProject 字段；尝试在 CLIProxyAPI 重新登录该凭证或检查文件完整性）", af.FileName)
	}

	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    "antigravity/1.11.5 windows/amd64",
	}
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return fmt.Errorf("Antigravity marshal payload: %w", err)
	}

	var lastErr error
	var models map[string]any
	for _, url := range antigravityURLs {
		r, err := cpaAPICall(ctx, af.AuthIndex, "POST", url, headers, string(body))
		if err != nil {
			lastErr = err
			continue
		}
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			lastErr = fmt.Errorf("Antigravity %s HTTP %d", url, r.StatusCode)
			continue
		}
		var payload struct {
			Models map[string]any `json:"models"`
		}
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			lastErr = err
			continue
		}
		if len(payload.Models) > 0 {
			models = payload.Models
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("Antigravity %s 返回空 models", url)
	}
	if models == nil {
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("Antigravity 所有 URL 都失败")
	}

	entry.Windows = nil
	allModelNames := make([]string, 0, 16)
	for _, g := range antigravityGroups {
		minRem := 200.0
		var resetsAt time.Time
		hit := false
		for _, ident := range g.Identifiers {
			m, ok := models[ident].(map[string]any)
			if !ok {
				continue
			}
			hit = true
			allModelNames = append(allModelNames, ident)
			used := antigravityModelUsedPct(m)
			rem := 100 - used
			if rem < minRem {
				minRem = rem
			}
			if r := antigravityModelReset(m); !r.IsZero() && (resetsAt.IsZero() || r.Before(resetsAt)) {
				resetsAt = r
			}
		}
		if !hit {
			continue
		}
		entry.Windows = append(entry.Windows, CreditWindow{
			ID:               g.ID,
			Label:            g.Label,
			UsedPercent:      clampPct(100 - minRem),
			RemainingPercent: clampPct(minRem),
			ResetsAt:         resetsAt,
		})
	}
	entry.Models = allModelNames

	// 拉 paidTier.id / currentTier.id 作为套餐级别（PRO / ULTRA / FREE / UNKNOWN）。
	// 同步 jlcodes99/cockpit-tools quota.rs:fetch_project_id_with_context 的实现。
	// 失败不影响 windows 主数据，仅 log。
	caHeaders := map[string]string{
		"Authorization":    "Bearer $TOKEN$",
		"Content-Type":     "application/json",
		"User-Agent":       "antigravity/1.11.5 windows/amd64",
		"x-goog-api-client": "gl-node/22.10.0",
	}
	caPayload, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"ideName":       "antigravity",
			"ideType":       "ANTIGRAVITY",
			"ideVersion":    "1.11.5",
			"pluginVersion": "1.0.0",
			"platform":      "WINDOWS_AMD64",
			"duetProject":   projectID,
		},
		"mode":                    "FULL_ELIGIBILITY_CHECK",
		"cloudaicompanionProject": projectID,
	})
	r, err := cpaAPICall(ctx, af.AuthIndex, "POST",
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		caHeaders, string(caPayload))
	if err == nil && r != nil && r.StatusCode == 200 {
		if tier := pickGoogleCodeAssistTier(r.Body); tier != "" {
			entry.PlanType = tier
		}
	} else if err != nil {
		log.Printf("[CREDITS] Antigravity loadCodeAssist auth=%s 失败: %s", af.AuthIndex, sanitizeError(err.Error(), 200))
	}

	return nil
}

func antigravityModelUsedPct(m map[string]any) float64 {
	if q, ok := m["quota"].(map[string]any); ok {
		if v, ok := q["utilization"]; ok {
			return parseUsedPercent(v)
		}
		if cons, ok := q["consumed"].(float64); ok {
			if lim, ok := q["limit"].(float64); ok && lim > 0 {
				return clampPct(cons / lim * 100)
			}
		}
	}
	if v, ok := m["utilization"]; ok {
		return parseUsedPercent(v)
	}
	return 0
}

func antigravityModelReset(m map[string]any) time.Time {
	candidates := []string{"resets_at", "resetAt", "reset_at"}
	if q, ok := m["quota"].(map[string]any); ok {
		for _, k := range candidates {
			if v, ok := q[k].(string); ok {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					return t
				}
			}
		}
	}
	for _, k := range candidates {
		if v, ok := m[k].(string); ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// ─── Provider Fetcher: Codex / OpenAI ────────────────────────────────────

const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// Codex usage 响应结构（解析逻辑参照 CPA management center 前端代码）：
//
//	{
//	  "rate_limit": {                         // 主限额（普通对话）
//	    "primary_window":   { "limit_window_seconds": 18000,  "used_percent": ?, "resets_at": ... },
//	    "secondary_window": { "limit_window_seconds": 604800, "used_percent": ?, "resets_at": ... },
//	    "limit_reached": bool,
//	    "allowed": bool
//	  },
//	  "code_review_rate_limit": { ... 同上结构 }
//	}
//
// 关键发现：
//   - free 用户响应里 used_percent 字段缺失 → 但 limit_reached=true 时 UI 显示"100% 已耗尽"
//   - primary/secondary 的语义由 limit_window_seconds 决定（18000=5h, 604800=7d），不是字段名
func fetchCodexQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	if af.IDToken != nil {
		if pt, ok := af.IDToken["plan_type"].(string); ok {
			entry.PlanType = pt
		}
	}

	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
		"User-Agent":    "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
	}
	r, err := cpaAPICall(ctx, af.AuthIndex, "GET", codexUsageURL, headers, "")
	if err != nil {
		return err
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("Codex usage HTTP %d: %s", r.StatusCode, sanitizeError(string(r.Body), errorBodyMaxBytes))
	}
	var p struct {
		RateLimit           map[string]any `json:"rate_limit"`
		CodeReviewRateLimit map[string]any `json:"code_review_rate_limit"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return err
	}
	entry.Windows = nil
	// 主限额（编程对话）
	if win := codexPickWindow(p.RateLimit, 18000); win != nil {
		entry.Windows = append(entry.Windows, codexBuildWindow("five-hour", "5 小时限额", win, p.RateLimit))
	}
	if win := codexPickWindow(p.RateLimit, 604800); win != nil {
		entry.Windows = append(entry.Windows, codexBuildWindow("weekly", "周限额", win, p.RateLimit))
	}
	// Code Review 副限额（如果存在）
	if win := codexPickWindow(p.CodeReviewRateLimit, 18000); win != nil {
		entry.Windows = append(entry.Windows, codexBuildWindow("code-review-five-hour", "5 小时限额（Code Review）", win, p.CodeReviewRateLimit))
	}
	if win := codexPickWindow(p.CodeReviewRateLimit, 604800); win != nil {
		entry.Windows = append(entry.Windows, codexBuildWindow("code-review-weekly", "周限额（Code Review）", win, p.CodeReviewRateLimit))
	}
	entry.Models = []string{"gpt-5", "gpt-5-mini", "codex-mini-latest"}
	return nil
}

// codexPickWindow 从 rate_limit 容器里**严格按 limit_window_seconds** 挑窗口。
// 没匹配到 expectedSec 直接返回 nil——参照 CPA management UI 的实现（`if (t===18e3 && !o) o=w`），
// 不再做"primary→5h / secondary→weekly"的位置兜底，否则 Free 用户（只有 weekly 在 primary_window）
// 会被误识别为"5 小时限额"。
func codexPickWindow(rl map[string]any, expectedSec float64) map[string]any {
	if rl == nil {
		return nil
	}
	primary, _ := rl["primary_window"].(map[string]any)
	if primary == nil {
		primary, _ = rl["primaryWindow"].(map[string]any)
	}
	secondary, _ := rl["secondary_window"].(map[string]any)
	if secondary == nil {
		secondary, _ = rl["secondaryWindow"].(map[string]any)
	}
	for _, w := range []map[string]any{primary, secondary} {
		if w == nil {
			continue
		}
		if sec, ok := codexWindowSeconds(w); ok && sec == expectedSec {
			return w
		}
	}
	return nil
}

func codexWindowSeconds(w map[string]any) (float64, bool) {
	for _, k := range []string{"limit_window_seconds", "limitWindowSeconds"} {
		switch v := w[k].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		}
	}
	return 0, false
}

// codexBuildWindow 组装一个 CreditWindow。rl 是其所属的 rate_limit 容器，
// 用来读取 limit_reached / allowed 这种"limit-level"字段（兜底显示 100%）。
//
// CPA UI 完整条件（参照源码）：
//
//	used_percent ?? ((limit_reached || allowed===false) && resetLabel !== '-' ? 100 : null)
//
// 注意 resetLabel !== '-' 的守卫——必须有重置时间才显示 100%，
// 否则永久封禁 / 配置错误的凭证会被误识别为"已耗尽"。
func codexBuildWindow(id, label string, w map[string]any, rl map[string]any) CreditWindow {
	out := CreditWindow{ID: id, Label: label}
	// 先解析重置时间——后续 100% 兜底逻辑要用它做守卫
	for _, k := range []string{"resets_at", "resetAt", "reset_at"} {
		if v, ok := w[k].(string); ok && v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				out.ResetsAt = t
				break
			}
		}
	}

	usedSet := false
	if v, ok := w["used_percent"].(float64); ok {
		out.UsedPercent = clampPct(v)
		usedSet = true
	} else if v, ok := w["usedPercent"].(float64); ok {
		out.UsedPercent = clampPct(v)
		usedSet = true
	} else if v, ok := w["utilization"]; ok {
		out.UsedPercent = parseUsedPercent(v)
		usedSet = true
	}
	// fix Go-HIGH2：参照 CPA UI 完整条件——必须既"已耗尽信号"且"有重置时间"才能显示 100%。
	// 仅 limit_reached/allowed=false 但无重置时间 → 是永久封禁/配置错误，UsedPercent 留 0
	// 让上层判定为"无数据"而不是"已用完"，避免误导。
	if !usedSet && rl != nil && !out.ResetsAt.IsZero() {
		exhausted := false
		if v, ok := rl["limit_reached"].(bool); ok && v {
			exhausted = true
		} else if v, ok := rl["limitReached"].(bool); ok && v {
			exhausted = true
		}
		if v, ok := rl["allowed"].(bool); ok && !v {
			exhausted = true
		}
		if exhausted {
			out.UsedPercent = 100
		}
	}
	out.RemainingPercent = clampPct(100 - out.UsedPercent)
	return out
}

// ─── Provider Fetcher: Gemini CLI ────────────────────────────────────────

const geminiCliQuotaURL = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"

// Gemini-CLI 模型 series（参照 CPA management center 前端代码）
// 同一 series 里取 minRemaining 作为该 series 的剩余额度，与 Antigravity 同款"短板原理"
type geminiCliSeries struct {
	ID, Label string
	ModelIDs  []string
}

var geminiCliSeriesList = []geminiCliSeries{
	{"gemini-flash-lite-series", "Gemini Flash Lite", []string{"gemini-2.5-flash-lite"}},
	{"gemini-flash-series", "Gemini Flash", []string{"gemini-3-flash-preview", "gemini-2.5-flash"}},
	{"gemini-pro-series", "Gemini Pro", []string{"gemini-3.1-pro-preview", "gemini-3-pro-preview", "gemini-2.5-pro"}},
}

// retrieveUserQuota 真实协议（参照 CPA management center 的 Pb fetcher）：
//
//	请求体：POST {"project": "<gcp-project-id>"}     ← 必须传 project_id（与 Antigravity 同源）
//	响应：
//	  {
//	    "buckets": [
//	      {
//	        "modelId": "gemini-2.5-pro",
//	        "tokenType": "input",                    // 可空
//	        "remainingFraction": 0.83,               // 剩余比例 0~1
//	        "remainingAmount": 1234,                 // 剩余原始数量
//	        "resetTime": "2025-..."                  // ISO8601
//	      },
//	      ...
//	    ],
//	    "tier": {...}                                // 套餐等元数据
//	  }
//
// 关键 corner case（CPA UI 同步实现）：
//   - remainingAmount==0 → 视为已耗尽（remainingFraction=0）
//   - remainingAmount==null && resetTime!=null → 视为已耗尽（remainingFraction=0）
//   - 同 series 多 model 取 min(remainingFraction)（短板原理）
func fetchGeminiCliQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	projectID := strings.TrimSpace(af.ProjectID)
	if projectID == "" {
		return fmt.Errorf("Gemini-CLI 凭证 %s 的 project_id 缺失（请确保该凭证在 CLIProxyAPI 已完成 OAuth 登录）", af.FileName)
	}
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
		"Content-Type":  "application/json",
	}
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return fmt.Errorf("Gemini-CLI marshal payload: %w", err)
	}
	r, err := cpaAPICall(ctx, af.AuthIndex, "POST", geminiCliQuotaURL, headers, string(body))
	if err != nil {
		return err
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("Gemini-CLI quota HTTP %d: %s", r.StatusCode, sanitizeError(string(r.Body), errorBodyMaxBytes))
	}
	var p struct {
		Buckets []map[string]any `json:"buckets"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return fmt.Errorf("Gemini-CLI quota 解析失败: %w", err)
	}

	// modelId → series 的反向索引（参照 CPA UI 的 iy map）
	modelToSeries := make(map[string]int, 8)
	for idx, s := range geminiCliSeriesList {
		for _, mid := range s.ModelIDs {
			modelToSeries[mid] = idx
		}
	}

	// 按 series 聚合：取每个 series 内所有 bucket 的 min(remaining)
	type seriesAgg struct {
		minRem   float64
		resetsAt time.Time
		hit      bool
	}
	aggs := make([]seriesAgg, len(geminiCliSeriesList))
	for i := range aggs {
		aggs[i].minRem = 2.0 // 哨兵：大于 1 表示未命中
	}
	allModelIDs := make([]string, 0, 8)
	seenModel := make(map[string]bool, 8)

	for _, b := range p.Buckets {
		modelID := geminiCliStripVertex(geminiCliStr(b["modelId"], b["model_id"]))
		if modelID == "" {
			continue
		}
		idx, ok := modelToSeries[modelID]
		if !ok {
			continue // 未识别的 model 跳过
		}
		// remainingFraction 优先，否则按 remainingAmount==0 / resetTime 启发式补 0
		rem := geminiCliFraction(b)
		if rem < 0 {
			continue
		}

		if !seenModel[modelID] {
			seenModel[modelID] = true
			allModelIDs = append(allModelIDs, modelID)
		}
		a := &aggs[idx]
		a.hit = true
		if rem < a.minRem {
			a.minRem = rem
		}
		if rs := geminiCliResetTime(b); !rs.IsZero() && (a.resetsAt.IsZero() || rs.Before(a.resetsAt)) {
			a.resetsAt = rs
		}
	}

	entry.Windows = nil
	for i, s := range geminiCliSeriesList {
		a := aggs[i]
		if !a.hit {
			continue
		}
		used := clampPct((1 - a.minRem) * 100)
		entry.Windows = append(entry.Windows, CreditWindow{
			ID:               s.ID,
			Label:            s.Label,
			UsedPercent:      used,
			RemainingPercent: clampPct(100 - used),
			ResetsAt:         a.resetsAt,
		})
	}
	if len(allModelIDs) > 0 {
		entry.Models = allModelIDs
	} else {
		entry.Models = []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"}
	}

	// 拉 paidTier.id / currentTier.id 作为 Gemini CLI 套餐级别（FREE / LEGACY / STANDARD /
	// PRO / ULTRA 等，来自 Google Cloud Code Assist）。逻辑与 Antigravity 一致，但 metadata
	// 标 ideName=gemini-cli 让上游区分。
	caHeaders := map[string]string{
		"Authorization":     "Bearer $TOKEN$",
		"Content-Type":      "application/json",
		"User-Agent":        "GeminiCLI/0.5.0 (linux; x64)",
		"x-goog-api-client": "gl-node/22.10.0",
	}
	caPayload, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"ideName":       "gemini-cli",
			"ideType":       "IDE_UNSPECIFIED",
			"ideVersion":    "0.5.0",
			"pluginVersion": "0.5.0",
			"platform":      "LINUX_AMD64",
			"duetProject":   projectID,
		},
		"mode":                    "FULL_ELIGIBILITY_CHECK",
		"cloudaicompanionProject": projectID,
	})
	r2, err2 := cpaAPICall(ctx, af.AuthIndex, "POST",
		"https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		caHeaders, string(caPayload))
	if err2 == nil && r2 != nil && r2.StatusCode == 200 {
		if tier := pickGoogleCodeAssistTier(r2.Body); tier != "" {
			entry.PlanType = tier
		}
	} else if err2 != nil {
		log.Printf("[CREDITS] Gemini-CLI loadCodeAssist auth=%s 失败: %s", af.AuthIndex, sanitizeError(err2.Error(), 200))
	}

	return nil
}

// geminiCliStr 多候选键取第一个非空字符串。
func geminiCliStr(vs ...any) string {
	for _, v := range vs {
		if s, ok := v.(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

// geminiCliStripVertex 兼容 CPA UI 的 _y 函数：去掉 "_vertex" 后缀
func geminiCliStripVertex(s string) string {
	const sfx = "_vertex"
	if strings.HasSuffix(s, sfx) {
		return s[:len(s)-len(sfx)]
	}
	return s
}

// geminiCliFraction 从一个 bucket 算出剩余比例 [0,1]，按 CPA UI 启发式：
//
//	remainingFraction 有值 → 用它（clamp [0,1]）
//	否则 remainingAmount==0 → 0
//	否则 remainingAmount<=0 但 null && resetTime → 视为 0
//	否则返回 -1（不参与聚合）
func geminiCliFraction(b map[string]any) float64 {
	if rem, ok := geminiCliFloat(b, "remainingFraction", "remaining_fraction"); ok {
		if rem < 0 {
			rem = 0
		}
		if rem > 1 {
			rem = 1
		}
		return rem
	}
	amt, hasAmt := geminiCliFloat(b, "remainingAmount", "remaining_amount")
	if hasAmt && amt <= 0 {
		return 0
	}
	if !hasAmt {
		// 没数量但有重置时间 → 已耗尽
		if t := geminiCliResetTime(b); !t.IsZero() {
			return 0
		}
	}
	return -1
}

func geminiCliFloat(b map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch v := b[k].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case string:
			s := strings.TrimSpace(v)
			if s == "" {
				continue
			}
			if strings.HasSuffix(s, "%") {
				if x, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64); err == nil {
					return x / 100, true
				}
			} else if x, err := strconv.ParseFloat(s, 64); err == nil {
				return x, true
			}
		}
	}
	return 0, false
}

func geminiCliResetTime(b map[string]any) time.Time {
	for _, k := range []string{"resetTime", "reset_time", "resetsAt", "resets_at"} {
		if s, ok := b[k].(string); ok && strings.TrimSpace(s) != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}

// ─── Provider Fetcher: Kimi ──────────────────────────────────────────────

const kimiUsageURL = "https://api.kimi.com/coding/v1/usages"

func fetchKimiQuota(ctx context.Context, af authFileLite, entry *CreditEntry) error {
	headers := map[string]string{
		"Authorization": "Bearer $TOKEN$",
	}
	r, err := cpaAPICall(ctx, af.AuthIndex, "GET", kimiUsageURL, headers, "")
	if err != nil {
		return err
	}
	if r.StatusCode != 200 {
		return fmt.Errorf("Kimi usage HTTP %d: %s", r.StatusCode, sanitizeError(string(r.Body), errorBodyMaxBytes))
	}
	var p struct {
		WeeklyLimit float64 `json:"weekly_limit"`
		WeeklyUsed  float64 `json:"weekly_used"`
		ResetsAt    string  `json:"resets_at"`
	}
	if err := json.Unmarshal(r.Body, &p); err != nil {
		return err
	}
	w := CreditWindow{ID: "weekly", Label: "周限额"}
	if p.WeeklyLimit > 0 {
		w.UsedPercent = clampPct(p.WeeklyUsed / p.WeeklyLimit * 100)
		w.RemainingPercent = clampPct(100 - w.UsedPercent)
		w.HasNumeric = true
		w.CreditAmount = p.WeeklyLimit - p.WeeklyUsed
	}
	if t, err := time.Parse(time.RFC3339, p.ResetsAt); err == nil {
		w.ResetsAt = t
	}
	entry.Windows = []CreditWindow{w}
	entry.Models = []string{"kimi-k2"}
	return nil
}

// ─── 公开查询接口（被 controller 调用） ───────────────────────────────────

// SnapshotAdmin 返回完整 admin 视角的所有凭证
func SnapshotAdmin() (entries []*CreditEntry, lastFull time.Time) {
	creditsMu.RLock()
	defer creditsMu.RUnlock()
	out := make([]*CreditEntry, 0, len(creditsCache))
	for _, e := range creditsCache {
		ec := *e
		if len(e.Windows) > 0 {
			ec.Windows = make([]CreditWindow, len(e.Windows))
			copy(ec.Windows, e.Windows)
		}
		if len(e.Models) > 0 {
			ec.Models = make([]string, len(e.Models))
			copy(ec.Models, e.Models)
		}
		out = append(out, &ec)
	}
	return out, creditsLastFull
}

// ─── 杂项工具 ────────────────────────────────────────────────────────────

func anyToStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case bool:
		return strconv.FormatBool(x)
	default:
		return ""
	}
}
