// Package proxy / credits_pool_cpa.go
//
// H-R2 重构（2026-05-19）：原 credits_pool.go 2127 行单文件按职责拆为 5 个文件：
//   - credits_pool.go         核心：types / globals / lifecycle / refresh-all / 共享 helpers
//   - credits_pool_cpa.go     CPA management API 客户端 + auth files sync + refresh-one dispatch
//   - credits_pool_anthropic.go  Claude / Anthropic quota window fetcher
//   - credits_pool_google.go     Antigravity + Gemini CLI quota fetcher + Google 共享 helpers
//   - credits_pool_other.go      Codex + Kimi quota fetcher
//
// 业务逻辑零改动；仅按文件物理拆分。

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
	"time"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
	baseURL, err := getValidatedCliproxyURL()
	if err != nil {
		return fmt.Errorf("cliproxy_url 安全校验失败: %w", err)
	}
	url := baseURL + "/v0/management/auth-files"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}
	if k := getCliproxyKey(); k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := cpaHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 CPA (%s): %w", baseURL, err)
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

func getValidatedCliproxyURL() (string, error) {
	baseURL := getCliproxyURL()
	if err := ValidateChannelURL(baseURL); err != nil {
		return "", err
	}
	return baseURL, nil
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
	// fix CRITICAL Sprint2-M6：Cookie / Set-Cookie / URL query secrets / URL userinfo
	// 一并清洗，与 Bearer/api_key 同一入口（cliproxy_usage_sync.FailBody 等所有错误日志都走这里）。
	s = cookieRe.ReplaceAllStringFunc(s, func(match string) string {
		// 保留 header name + 冒号，截断 value
		if colonIdx := strings.Index(match, ":"); colonIdx >= 0 {
			return match[:colonIdx+1] + " ***"
		}
		return "***"
	})
	s = urlQuerySecretRe.ReplaceAllString(s, "$1***")
	s = urlUserinfoRe.ReplaceAllString(s, "$1***@")
	if len(s) > maxLen {
		s = s[:maxLen] + "...(truncated)"
	}
	return s
}

// ─── CPA 响应异常防御（Sprint4-M6） ───────────────────────────────────────

// validateAuthFilesResponse 判断 CPA `/v0/management/auth-files` 响应是否可信。
//
// fix CRITICAL Sprint4-M6：旧实现把任何合法 JSON 的 files 列表当作"权威快照"，
// 直接用 staleCount=N 清空 creditsCache + 软删全部 CPA 凭证。但 CPA 瞬时异常
// （例如内部 DB 重启 / 上游 OAuth 临时不可用 / 序列化错误）经常返回 files=[]，
// 一次响应就会让全平台号池失效，全用户 503。
//
// 防御策略：
//
//  1. 结构校验：过滤掉缺 id/provider 的 malformed 条目（CPA bug 兜底）
//  2. 异常收缩检测：若本轮 valid 数量 < 上轮 × shrink_threshold（默认 50%），
//     视作上游异常，整轮 abort，保留上一轮 cache 与 DB 行
//  3. 全空保护：上一轮有凭证但本轮全空 → 永远视作异常，绝不"自然过渡到 0"
//     （admin 真要清空号池，应在 CPA 后台逐个删除，自然走 shrink 阈值之上的多轮迁移）
//
// 返回 true 表示响应可信，调用方继续 sync；false 表示 abort 本轮。
func validateAuthFilesResponse(authFiles []authFileLite) bool {
	// 1) 校验结构：必须有 id 和 provider 才算可用条目
	validCount := 0
	malformed := 0
	for _, af := range authFiles {
		if strings.TrimSpace(af.ID) == "" || strings.TrimSpace(af.Provider) == "" {
			malformed++
			continue
		}
		validCount++
	}
	if malformed > 0 {
		log.Printf("[CREDITS] auth-files response has %d malformed entries (missing id/provider) of %d total; treating as potential upstream corruption",
			malformed, len(authFiles))
	}

	// 2) 与上一轮对比：拿到 in-memory cache 大小作为 baseline
	creditsMu.RLock()
	prevTotal := len(creditsCache)
	creditsMu.RUnlock()

	// 冷启动 + 空响应保护：进程刚启动时内存 cache 为空，CPA 空响应不能被当作
	// 权威快照提交，否则会把真实号池误初始化为空。
	if prevTotal == 0 && validCount == 0 {
		log.Printf("[CREDITS-ANOMALY] ABORT: cold start but auth-files is empty; refusing to commit empty snapshot")
		return false
	}
	if prevTotal == 0 {
		return true
	}

	// 3) 全空保护：上轮有 N 条，本轮零有效条目 → 永远视作上游异常
	if validCount == 0 {
		log.Printf("[CREDITS-ANOMALY] ABORT: auth-files response is empty but cache has %d credentials. "+
			"Treating as upstream transient failure; preserving previous cache to avoid full-pool wipeout. "+
			"If admin真要清空号池，请逐个删除走 shrink 阈值之上的多轮迁移。", prevTotal)
		return false
	}

	// 4) 异常收缩检测
	threshold := credentialsShrinkThreshold()
	minAcceptable := int64(prevTotal) * threshold / 100
	if int64(validCount) < minAcceptable {
		log.Printf("[CREDITS-ANOMALY] ABORT: auth-files response shrunk from %d → %d (below %d%% threshold = %d). "+
			"Treating as upstream transient failure; preserving previous cache. "+
			"调高/调低阈值可设 SysConfig credits_shrink_abort_threshold_pct (默认 50)。",
			prevTotal, validCount, threshold, minAcceptable)
		return false
	}

	return true
}

// credentialsShrinkThreshold 返回触发 abort 的最小占比（百分数 1-100）。
// 默认 50：本轮有效凭证少于上轮 50% 视作异常。
func credentialsShrinkThreshold() int64 {
	const defaultPct = 50
	const minPct = 1
	const maxPct = 99
	SysConfigMutex.RLock()
	v := strings.TrimSpace(SysConfigCache["credits_shrink_abort_threshold_pct"])
	SysConfigMutex.RUnlock()
	if v == "" {
		return defaultPct
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < minPct || n > maxPct {
		return defaultPct
	}
	return n
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
	baseURL, err := getValidatedCliproxyURL()
	if err != nil {
		return nil, fmt.Errorf("cliproxy_url 安全校验失败: %w", err)
	}
	url := baseURL + "/v0/management/auth-files"
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
// daof-cpa 走 api-call 透明代理调上游时 CPA 会自己注入最新 token。

// fetchAuthFileContent 从 CPA 下载某个凭证的完整 JSON 内容
func fetchAuthFileContent(ctx context.Context, name string) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("file name empty")
	}
	baseURL, err := getValidatedCliproxyURL()
	if err != nil {
		return nil, fmt.Errorf("cliproxy_url 安全校验失败: %w", err)
	}
	url := baseURL + "/v0/management/auth-files/download?name=" + urlQueryEscape(name)
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
	// 仅接受 canonical provider 名（"antigravity" / "gemini-cli"）。CPA 必须使用规范名，
	// 不再兼容 "gemini" / "gemini_cli" 等别名（项目未上线，禁止向后兼容）。
	needsProjectID := func(p string) bool {
		p = strings.ToLower(strings.TrimSpace(p))
		return p == "antigravity" || p == "gemini-cli"
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
	baseURL, err := getValidatedCliproxyURL()
	if err != nil {
		return nil, fmt.Errorf("cliproxy_url 安全校验失败: %w", err)
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
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v0/management/api-call", bytes.NewReader(buf))
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

	// 仅接受 canonical provider 名。CPA 必须使用规范名，
	// 不再兼容 "anthropic" / "gemini" 等别名（项目未上线，禁止向后兼容）。
	var err error
	switch af.Provider {
	case "claude":
		err = fetchClaudeQuota(ctx, af, entry)
	case "antigravity":
		err = fetchAntigravityQuota(ctx, af, entry)
	case "codex":
		err = fetchCodexQuota(ctx, af, entry)
	case "gemini-cli":
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
