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
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

	// CPA 通用代理调用统一走 SafeTransport，避免 DNS rebinding 绕过 URL 校验。
	cpaHTTPClient = &http.Client{
		Timeout:       10 * time.Second,
		Transport:     SafeTransport(),
		CheckRedirect: redirectGuard,
	}
	// CPA auth-files 列表/下载查询：同样必须走 SafeTransport + redirect 复校验。
	cpaAuthFilesClient = &http.Client{
		Timeout:       30 * time.Second,
		Transport:     SafeTransport(),
		CheckRedirect: redirectGuard,
	}

	// Bearer token 脱敏正则
	bearerRe = regexp.MustCompile(`(?i)Bearer\s+\S+`)
	// 通用 token 关键字脱敏：覆盖 access/refresh/id_token、api_key、secret、authorization
	// download 凭证 JSON 失败路径里若错误体回显原文，需要这层防御
	apiKeyRe = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|secret|authorization)["'\s:=]+[A-Za-z0-9._/+=-]{8,}`)
	// fix Sec-M1: bare JWT pattern (eyJ.<base64url>.<base64url>) 兜底——
	// 上游错误体偶尔会裸吐 token 没带 key 名（HTTP 401 body 等），需要专门正则捕获。
	jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)
	// fix CRITICAL Sprint2-M6：Cookie / Set-Cookie header 内容脱敏
	// CPA 错误响应有时会原样回传客户端 Cookie，落库前必须清洗：
	//   "Cookie: session=xxx; auth=yyy"  → "Cookie: ***"
	//   "Set-Cookie: token=abc; Path=/" → "Set-Cookie: ***"
	cookieRe = regexp.MustCompile(`(?i)(set-cookie|cookie)\s*:\s*[^\r\n]+`)
	// URL query 中的敏感参数脱敏：
	//   "?api_key=xxx&token=yyy"  → "?api_key=***&token=***"
	urlQuerySecretRe = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|secret|password|token|code|signature)=)[^&\s"'<>]+`)
	// URL userinfo 段（user:pass@host）脱敏：
	//   "https://user:secret@host" → "https://***@host"
	urlUserinfoRe = regexp.MustCompile(`(?i)([a-z]{2,8}://)[^/@\s]+:[^/@\s]+@`)
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

	// fix CRITICAL Sprint4-M6：异常空/收缩快照防御。
	// CPA 瞬时异常（合法 JSON 但 files=[] 或字段缺失大量）会让原实现 staleCount=N 清空全部凭证
	// + syncCPACredentials 软删全部行。必须先识别"上游异常"并 abort，保留上一轮 cache。
	if !validateAuthFilesResponse(authFiles) {
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

// parseUsedPercent 解析 Anthropic OAuth usage 的 utilization 字段。
//
// Anthropic API 契约：utilization 始终是 0-100 百分数（不是 0-1 比例）。
// 例：utilization=2  → 已用 2%
//
//	utilization=1  → 已用 1%（不是 100%！）
//	utilization=99 → 已用 99%
//
// 与 CPAMC `parseClaudeUsagePayload` 直接 `normalizeNumberValue(utilization)`
// 行为对齐（quotaConfigs.ts:924）。之前的 "f ≤ 1.0 视作比例 × 100" 启发式
// 会把 utilization=1（=1%）误判为 100%，导致 5h 在 ≤1% 使用率下显示 0%
// remaining，与 CPAMC / Anthropic 真实显示不一致。
func parseUsedPercent(v any) float64 {
	f, ok := v.(float64)
	if !ok {
		return 0
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
