package database

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// User 代表核心的用户实体，集成 Oauth 与手机双重绑定体系
//
// fix MAJOR M22-2（codex 第二十二轮）：原 GithubID/Phone 标 `uniqueIndex` 让普通 unique
// 索引仍生效——空字符串两个用户都写 "" 后第二个 INSERT 撞约束。partial unique index
// 在 sqlite.go 加了，但**额外**索引而非替换 GORM 的，普通约束仍在。
// 修复：去掉 GORM uniqueIndex，让 sqlite.go 的 partial unique（WHERE x <> ”）成为唯一约束。
type User struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	GithubID     string `gorm:"index;default:null" json:"github_id"` // 唯一性由 sqlite.go partial unique index 保证
	Phone        string `gorm:"index;default:null" json:"phone"`     // 唯一性由 sqlite.go partial unique index 保证
	Username     string `gorm:"uniqueIndex;not null" json:"username"`
	PasswordHash string `json:"-"` // 作为管理员的特有降神凭证
	// fix MEDIUM M19-5（codex 第十九轮）：注册路径 registerMu 临界区里要做 COUNT(*) WHERE role='user'
	// 检查注册总数上限——表大了之后 SQLite/PG 都会做全表 scan，单次几十毫秒。`index` 让该 COUNT
	// 走 index-only 扫描或至少减少 IO。BulkAdjustQuota 也按 role='user' 过滤，同样受益。
	Role  string `gorm:"index;default:'user'" json:"role"`  // 'admin' 或 'user'
	Token string `gorm:"uniqueIndex;not null" json:"token"` // 直通代理鉴权令牌，如 sk-daof-xxx
	// fix MAJOR M22-A1（codex 第二十三轮）：所有金额字段统一为 int64 micro_usd（USD * 1e6）。
	// 原因：float64 在长尾累加（千万级 API 调用 × 微小 cost）下出现累加误差，账目对不上；
	// int64 全程整数运算杜绝浮点漂移。前端展示时除以 1e6 显示 USD。
	Quota        int64  `gorm:"default:0" json:"quota"`                 // 余额（micro_usd, USD * 1e6）
	PaidQuota    int64  `gorm:"default:0" json:"paid_quota"`            // 尚未被消费归因的充值通道余额（micro_usd，仅用于拉新消费返佣口径）
	Status       int    `gorm:"not null;default:1;index" json:"status"` // 1: 正常, 2: 封禁
	BanReason    string `gorm:"type:text;default:null" json:"ban_reason"`
	RegIP        string `gorm:"index" json:"reg_ip"`             // 原始探测 IP (防刷核查用)
	RegRiskScore int    `gorm:"default:0" json:"reg_risk_score"` // 风控热度判定打分

	// ReferredByUserID 记录永久推荐关系。一次性注册奖励可以为 0，但关系仍要保存，
	// 后续消费返佣等生命周期奖励依赖这个字段追溯推荐人。
	ReferredByUserID uint       `gorm:"index;default:0" json:"referred_by_user_id"`
	ReferredAt       *time.Time `gorm:"index" json:"referred_at"`

	// 余额消费控制（参照 Claude Extra usage 三段消费模型）：
	// 订阅 → 余额（user.Quota）。订阅用尽且 BalanceConsumeEnabled=true 才走余额扣费。
	BalanceConsumeEnabled       bool       `gorm:"default:false" json:"balance_consume_enabled"`
	BalanceConsumeLimitUSD      int64      `gorm:"default:0" json:"balance_consume_limit_usd"`            // micro_usd, 0=不限
	BalanceConsumeWindowSeconds int        `gorm:"default:2592000" json:"balance_consume_window_seconds"` // 默认 30 天
	BalanceConsumeWindowStartAt *time.Time `json:"balance_consume_window_start_at"`
	BalanceConsumedInWindow     int64      `gorm:"default:0" json:"balance_consumed_in_window"` // micro_usd

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// fix MAJOR M-B9（codex 第二十一轮）：GithubID / Phone 标记 uniqueIndex+default:null，
// 但 Go string 零值是 ""，任何 Save(&user) 写空串后第二个 INSERT 会撞 unique。
// 兜底方案见 sqlite.go：手工建 partial unique index 排除空串，让多个 ""（视为未绑定）共存。
// schema 长期应改 *string，当前先用 DB 层 partial index 防御。

// Channel 代表底层的请求上游通道
type Channel struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	Type      string         `gorm:"index;not null" json:"type"` // openai / anthropic / gemini / google-cli / codex / cliproxy
	Name      string         `gorm:"index;not null" json:"name"` // 渠道备注名称，e.g. "官方 Azure", "第三方中转站"
	Key       string         `gorm:"not null" json:"key"`
	BaseURL   string         `json:"base_url"`                              // 自定义上游网关代理地址
	ProxyURL  string         `gorm:"default:null" json:"proxy_url"`         // 自定义 HTTP/SOCKS 代理跳板
	Headers   string         `gorm:"type:text;default:null" json:"headers"` // 附加的自定义 JSON 头部
	Weight    int            `gorm:"default:1" json:"weight"`               // 并发路由负载权重
	Status    int            `gorm:"default:1" json:"status"`               // 1: 启用, 2: 禁用
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// ChannelModel 绑定渠道对某一个特定模型的单价和权重配置
type ChannelModel struct {
	ID                                 uint   `gorm:"primaryKey" json:"id"`
	ChannelID                          uint   `gorm:"index;not null" json:"channel_id"` // 所属渠道
	ModelID                            string `gorm:"index;not null" json:"model_id"`   // e.g., "gpt-4o"
	DisplayName                        string `json:"display_name"`
	OfficialModelID                    string `gorm:"index;size:160;default:''" json:"official_model_id"` // 官方模型 ID；兼容层别名需指向真实官方 ID
	ModelCategory                      string `gorm:"size:16;default:'text'" json:"model_category"`       // text / image / video
	BillingMode                        string `gorm:"size:24;default:'token'" json:"billing_mode"`        // token / image / video_second
	AllowedEndpoints                   string `gorm:"type:text;default:''" json:"allowed_endpoints"`      // JSON array；空时按 category 默认端点
	InputPricePicoPerToken             int64  `gorm:"default:0" json:"input_price_pico_per_token"`
	OutputPricePicoPerToken            int64  `gorm:"default:0" json:"output_price_pico_per_token"`
	CachedInputPricePicoPerToken       int64  `gorm:"default:0" json:"cached_input_price_pico_per_token"`
	CacheWriteInputPricePicoPerToken   int64  `gorm:"default:0" json:"cache_write_input_price_pico_per_token"`
	CacheWrite1hInputPricePicoPerToken int64  `gorm:"column:cache_write_1h_input_price_pico_per_token;default:0" json:"cache_write_1h_input_price_pico_per_token"`
	ContextPriceThreshold              int    `gorm:"default:0" json:"context_price_threshold"`
	HighInputPricePicoPerToken         int64  `gorm:"default:0" json:"high_input_price_pico_per_token"`
	HighCachedInputPricePicoPerToken   int64  `gorm:"default:0" json:"high_cached_input_price_pico_per_token"`
	HighOutputPricePicoPerToken        int64  `gorm:"default:0" json:"high_output_price_pico_per_token"`
	MaxContextLength                   int    `gorm:"default:0" json:"max_context_length"`
	Weight                             int    `gorm:"default:1" json:"weight"` // 同模型多渠道的路由比重
	Status                             int    `gorm:"default:1" json:"status"` // 针对当前渠道的此模型一键封锁

	// EndpointPolicy 控制该渠道模型可接受的客户端端点形态。
	//
	//   "all"                - 不限制端点
	//   "no_chat_non_stream" - 禁止非流式 /v1/chat/completions（gpt-5.5 + CLIProxyAPI 当前需要）
	//   "responses_only"     - 仅允许 /v1/responses
	EndpointPolicy string `gorm:"size:32;default:'all'" json:"endpoint_policy"`

	// 风控配置（per channel + per model 粒度）
	//
	// fix CRITICAL R23（codex 第二十三轮）：御三家中 OpenAI 最易因 jailbreak 封号；Claude 自带强拒答 +
	// CLIProxyAPI cloaking 兜底；Gemini 有 safety filter。所以风控要按"渠道+模型"精确配置，
	// 而不是全局一刀切。同 modelName 多候选时 LookupModerationPolicy 取最严策略防御。
	//
	// ModerationLevel 取值：
	//   "off"        - 完全不审（适合 Claude/Gemini cloaked 路径）
	//   "keyword"    - 仅本地关键字快扫（<1ms，拦 Kiro/DAN 模板）
	//   "moderation" - 仅智能审核服务（CPA 模型池）
	//   "strict"     - 关键字/风险规则先做信号，智能审核二判后再决定是否拦截
	ModerationLevel string `gorm:"size:16;default:'off'" json:"moderation_level"`

	// ModerationFailMode 智能审核服务不可达时的策略
	//   "open"   - 放行（cloaked 路径——CLIProxyAPI 兜底）
	//   "closed" - 拒绝（直连官方时——审核失败时不能让违规 prompt 直达上游）
	ModerationFailMode string `gorm:"size:8;default:'open'" json:"moderation_fail_mode"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"` // 软删除：与 Channel 的 DeletedAt 配对，channel.go DeleteChannel 时一并 Delete
}

const (
	PicoPerUSD                       = int64(1_000_000_000_000_000)
	PicoPerMicroUSD                  = int64(1_000_000_000)
	PicoPerTokenPerUSDPerMTok        = int64(1_000_000_000)
	MaxChannelModelPricePicoPerToken = int64(1_000_000) * PicoPerTokenPerUSDPerMTok
	MultiplierPPMBase                = int64(1_000_000)
	MaxBillingMultiplierPPM          = int64(1_000_000_000)
)

// ValidateChannelModelPricing rejects values that can make cost calculation
// non-finite, negative, or operationally absurd before they enter route cache.
func ValidateChannelModelPricing(cm *ChannelModel) error {
	if cm == nil {
		return fmt.Errorf("channel model is nil")
	}
	for name, v := range map[string]int64{
		"input_price_pico_per_token":                cm.InputPricePicoPerToken,
		"output_price_pico_per_token":               cm.OutputPricePicoPerToken,
		"cached_input_price_pico_per_token":         cm.CachedInputPricePicoPerToken,
		"cache_write_input_price_pico_per_token":    cm.CacheWriteInputPricePicoPerToken,
		"cache_write_1h_input_price_pico_per_token": cm.CacheWrite1hInputPricePicoPerToken,
		"high_input_price_pico_per_token":           cm.HighInputPricePicoPerToken,
		"high_cached_input_price_pico_per_token":    cm.HighCachedInputPricePicoPerToken,
		"high_output_price_pico_per_token":          cm.HighOutputPricePicoPerToken,
	} {
		if v < 0 || v > MaxChannelModelPricePicoPerToken {
			return fmt.Errorf("%s must be between 0 and %d pico_usd/token", name, MaxChannelModelPricePicoPerToken)
		}
	}
	if cm.ContextPriceThreshold < 0 {
		return fmt.Errorf("context_price_threshold must be >= 0")
	}
	if cm.Weight < 0 {
		return fmt.Errorf("weight must be >= 0")
	}
	return nil
}

// SysConfig 存储全息网关的底层鉴权机密，如 Github Oauth / 阿里云信息等
type SysConfig struct {
	Key   string `gorm:"primaryKey" json:"key"`
	Value string `gorm:"not null" json:"value"` // 该字段在代码层进入数据库前会被 AES 强加密
}

// AccessToken 是独立于 User 主账号外的纯 API 调用凭证，支持一对多
type AccessToken struct {
	ID         uint           `gorm:"primaryKey" json:"id"`
	UserID     uint           `gorm:"index;not null" json:"user_id"`
	Name       string         `json:"name"`
	Key        string         `gorm:"uniqueIndex;not null" json:"key"` // e.g. "sk-daof-xxxx"
	UsedQuota  int64          `gorm:"default:0" json:"used_quota"`     // 累计消耗（micro_usd, USD * 1e6）
	QuotaLimit int64          `gorm:"default:0" json:"quota_limit"`    // 令牌限额（micro_usd），0 表示无限制
	ExpiredAt  *time.Time     `json:"expired_at"`                      // 令牌过期时间，null 表示无限期
	Status     int            `gorm:"default:1" json:"status"`         // 1: 启用, 2: 禁用
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"-"`
}

// ApiLog 记录每一条流过系统的对话指纹
type ApiLog struct {
	ID                     uint       `gorm:"primaryKey;<-:create" json:"id"`
	UserID                 uint       `gorm:"index;index:idx_api_logs_created_at_user_id,priority:2;not null;<-:create" json:"user_id"`
	TokenName              string     `gorm:"<-:create" json:"token_name"`
	ModelName              string     `gorm:"index;<-:create" json:"model_name"`
	RequestedModel         string     `gorm:"index;size:160;default:'';<-:create" json:"requested_model"`
	ServedModel            string     `gorm:"index;size:160;default:'';<-:create" json:"served_model"`
	PromptTokens           int        `gorm:"<-:create" json:"prompt_tokens"`
	CompletionTokens       int        `gorm:"<-:create" json:"completion_tokens"`
	CachedTokens           int        `gorm:"<-:create" json:"cached_tokens"`      // cache read tokens
	CacheWriteTokens       int        `gorm:"<-:create" json:"cache_write_tokens"` // cache creation/write tokens
	CacheWrite5mTokens     int        `gorm:"column:cache_write_5m_tokens;<-:create" json:"cache_write_5m_tokens"`
	CacheWrite1hTokens     int        `gorm:"column:cache_write_1h_tokens;<-:create" json:"cache_write_1h_tokens"`
	ReasoningTokens        int        `gorm:"<-:create" json:"reasoning_tokens"`
	Cost                   int64      `gorm:"<-:create" json:"cost"`                        // 原始 API 等值成本（micro_usd, USD * 1e6），= rawCost
	ChargedCost            int64      `gorm:"default:0;<-:create" json:"charged_cost"`      // 订阅扣减口径（rawCost × modelWeight × healthMultiplier），实际营收去 ApiLogRevenue
	ModelWeight            float64    `gorm:"default:1;<-:create" json:"model_weight"`      // 公开模型权重
	HealthMultiplier       float64    `gorm:"default:1;<-:create" json:"health_multiplier"` // 公开高峰/健康系数
	BillingRulesVersion    string     `gorm:"size:64;default:'';<-:create" json:"billing_rules_version"`
	PrecheckInputTokens    int        `gorm:"default:0;<-:create" json:"precheck_input_tokens"`    // 预检估算输入 tokens（失败请求不计入用量）
	PrecheckOutputTokens   int        `gorm:"default:0;<-:create" json:"precheck_output_tokens"`   // 预检预留输出 tokens
	PrecheckRawCost        int64      `gorm:"default:0;<-:create" json:"precheck_raw_cost"`        // 预检 API 等值成本（micro_usd）
	PrecheckChargedCost    int64      `gorm:"default:0;<-:create" json:"precheck_charged_cost"`    // 预检套餐/credits 扣减成本（micro_usd）
	PrecheckQuotaPlanID    uint       `gorm:"default:0;<-:create" json:"precheck_quota_plan_id"`   // 触发预检拒绝的 quota_plan
	PrecheckQuotaLimit     int64      `gorm:"default:0;<-:create" json:"precheck_quota_limit"`     // 当前窗口限额（micro_usd，仅 api_cost_usd 计划）
	PrecheckQuotaUsed      int64      `gorm:"default:0;<-:create" json:"precheck_quota_used"`      // 当前窗口已用（micro_usd，仅 api_cost_usd 计划）
	PrecheckQuotaRemaining int64      `gorm:"default:0;<-:create" json:"precheck_quota_remaining"` // 当前窗口剩余（micro_usd，仅 api_cost_usd 计划）
	PrecheckWindowEndAt    *time.Time `gorm:"<-:create" json:"precheck_window_end_at"`             // 当前窗口结束时间
	BlockReason            string     `gorm:"size:96;default:'';<-:create" json:"block_reason"`    // 机器可读阻断原因
	FallbackUserOptIn      bool       `gorm:"default:false;<-:create" json:"fallback_user_opt_in"`
	FallbackReason         string     `gorm:"size:160;default:'';<-:create" json:"fallback_reason"`
	UpstreamProvider       string     `gorm:"index;size:64;default:'';<-:create" json:"upstream_provider"`   // CPA usage/provider 归因
	UpstreamAuthIndex      string     `gorm:"index;size:64;default:'';<-:create" json:"upstream_auth_index"` // CPA 稳定账号索引（不可逆）
	UpstreamAuthType       string     `gorm:"size:64;default:'';<-:create" json:"upstream_auth_type"`        // oauth/api_key 等
	UpstreamSource         string     `gorm:"size:255;default:'';<-:create" json:"upstream_source"`          // CPA source（admin-only，可辅助定位）
	UpstreamRequestID      string     `gorm:"index;size:64;default:'';<-:create" json:"upstream_request_id"` // CPA 内部 request_id
	UpstreamUsageRecordID  uint       `gorm:"index;default:0;<-:create" json:"upstream_usage_record_id"`     // 对应 upstream_usage_records.id
	UpstreamUsageMatch     string     `gorm:"size:64;default:'';<-:create" json:"upstream_usage_match"`      // exact_tokens / single_candidate_zero_usage
	UpstreamUsageSyncedAt  *time.Time `gorm:"<-:create" json:"upstream_usage_synced_at"`
	Latency                int64      `gorm:"default:0;<-:create" json:"latency"`           // ms延迟 (Parity)
	Status                 int        `gorm:"default:200;<-:create" json:"status"`          // 状态码或结果记录 (Parity)
	IPAddress              string     `gorm:"index;default:'';<-:create" json:"ip_address"` // 请求来源IP (Parity)
	RequestPath            string     `gorm:"size:160;default:'';<-:create" json:"request_path"`
	ErrorType              string     `gorm:"size:64;default:'';<-:create" json:"error_type"`
	ErrorMessage           string     `gorm:"size:512;default:'';<-:create" json:"error_message"`
	// fix P-H1 (2026-05-19)：单列 `idx_api_logs_created_at` + 单列 user_id 索引下
	// admin 报表 `WHERE created_at >= ? GROUP BY user_id SUM(...)` 仍走全表 scan。
	// 加 composite 索引 (created_at, user_id) 让 cutoff 段 + 分组都走索引，N=50M
	// 行从 300-800ms 降至 ~20ms。GORM 标签 `index:idx_api_logs_created_at_user_id`
	// 会自动 emit `CREATE INDEX ... ON api_logs(created_at, user_id)`。
	CreatedAt              time.Time  `gorm:"index;index:idx_api_logs_created_at_user_id,priority:1;<-:create" json:"created_at"`
}

var ErrApiLogAppendOnly = errors.New("api_logs is append-only; write mutable attribution or estimates to side tables")

// fix HIGH（codex audit-integrity）：原实现注入 WHERE 1=0 让 SQL 静默命中 0 行，
// 调用方不检查 RowsAffected 会误以为 update 成功。改为 return Err，与 3 张侧表
// (ApiLogAttribution / ApiLogCostEstimate / ApiLogRevenue) 保持一致的 loud reject 策略。
// GDPR purge 用 raw SQL `tx.Exec("DELETE FROM api_logs ...")`，不走 GORM hook，不受影响。
func (l *ApiLog) BeforeUpdate(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

func (l *ApiLog) BeforeDelete(tx *gorm.DB) error {
	return ErrApiLogAppendOnly
}

// UpstreamUsageRecord 保存从 CLIProxyAPI usage queue 拉取到的原始用量事实。
//
// /v0/management/usage-queue 是 pop 语义：读出来后 CPA 队列就没了。
// 所以 DAOFA 必须先落库，再尝试匹配 api_logs；匹配不上也不能丢，后续可人工/任务补对账。
type UpstreamUsageRecord struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	Provider            string    `gorm:"index;size:64;default:''" json:"provider"`
	Model               string    `gorm:"index;size:160;default:''" json:"model"`
	Alias               string    `gorm:"index;size:160;default:''" json:"alias"`
	Endpoint            string    `gorm:"size:160;default:''" json:"endpoint"`
	AuthType            string    `gorm:"size:64;default:''" json:"auth_type"`
	AuthIndex           string    `gorm:"index;size:64;default:''" json:"auth_index"`
	Source              string    `gorm:"size:255;default:''" json:"source"`
	APIKeyHash          string    `gorm:"size:64;default:''" json:"api_key_hash"`
	RequestID           string    `gorm:"index;size:64;default:''" json:"request_id"`
	Timestamp           time.Time `gorm:"index" json:"timestamp"`
	Latency             int64     `gorm:"default:0" json:"latency_ms"`
	InputTokens         int       `gorm:"default:0" json:"input_tokens"`
	OutputTokens        int       `gorm:"default:0" json:"output_tokens"`
	ReasoningTokens     int       `gorm:"default:0" json:"reasoning_tokens"`
	CachedTokens        int       `gorm:"default:0" json:"cached_tokens"`
	CacheReadTokens     int       `gorm:"default:0" json:"cache_read_tokens"`
	CacheCreationTokens int       `gorm:"default:0" json:"cache_creation_tokens"`
	TotalTokens         int       `gorm:"default:0" json:"total_tokens"`
	Failed              bool      `gorm:"default:false" json:"failed"`
	Status              int       `gorm:"default:0" json:"status"`
	FailBody            string    `gorm:"size:512;default:''" json:"fail_body"`
	ResponseHeadersJSON string    `gorm:"type:text;default:''" json:"response_headers_json"`
	MatchedApiLogID     uint      `gorm:"index;default:0" json:"matched_api_log_id"`
	MatchStatus         string    `gorm:"index;size:64;default:'pending'" json:"match_status"`
	MatchReason         string    `gorm:"size:255;default:''" json:"match_reason"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// OperationLog 操作审计事实表。一旦写入不可修改、不可删除（append-only）。
//
// fix CRITICAL Sprint1-P0-7：所有业务字段加 `gorm:"<-:create"` 防止 GORM 层 UPDATE。
// 配合：
//  1. purgeUserDependents 不再删除 OperationLog（保留审计链）
//  2. 未来可加 DB 层 BEFORE UPDATE/DELETE trigger 兜底
//
// CreatedAt 同样 `<-:create`，防止 admin 篡改时间戳掩盖追溯链。
type OperationLog struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TargetUserID uint      `gorm:"<-:create;index;not null" json:"target_user_id"` // 被操作的用户
	OperatorID   uint      `gorm:"<-:create;index;default:0" json:"operator_id"`   // 发起操作的用户，0表示System
	OperatorRole string    `gorm:"<-:create" json:"operator_role"`                 // "admin", "system", "user"
	ActionType   string    `gorm:"<-:create;index;not null" json:"action_type"`    // e.g. "BAN", "UPDATE_QUOTA", "DELETE", "FORCE_CREATE"
	IPAddress    string    `gorm:"<-:create" json:"ip_address"`
	Details      string    `gorm:"<-:create;type:text" json:"details"` // JSON-encoded string or plain text detail
	CreatedAt    time.Time `gorm:"<-:create;index" json:"created_at"`
}
