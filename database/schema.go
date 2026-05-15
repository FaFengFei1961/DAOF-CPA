package database

import (
	"fmt"
	"math"
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
	Quota        int64  `gorm:"default:0" json:"quota"`  // 余额（micro_usd, USD * 1e6）
	Status       int    `gorm:"default:1" json:"status"` // 1: 正常, 2: 封禁
	BanReason    string `gorm:"type:text;default:null" json:"ban_reason"`
	RegIP        string `gorm:"index" json:"reg_ip"`             // 原始探测 IP (防刷核查用)
	RegRiskScore int    `gorm:"default:0" json:"reg_risk_score"` // 风控热度判定打分

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
	//   "strict"     - 关键字 + 智能审核双层（推荐官方高风险模型）
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
	MaxChannelModelPricePerMTok      = 1_000_000.0
	PicoPerUSD                       = int64(1_000_000_000_000_000)
	PicoPerMicroUSD                  = int64(1_000_000_000)
	PicoPerTokenPerUSDPerMTok        = int64(1_000_000_000)
	MaxChannelModelPricePicoPerToken = int64(MaxChannelModelPricePerMTok) * PicoPerTokenPerUSDPerMTok
	MultiplierPPMBase                = int64(1_000_000)
	MaxBillingMultiplierPPM          = int64(1_000_000_000)
)

func PricePicoPerTokenFromUSDPerMTok(price float64) (int64, error) {
	if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 || price > MaxChannelModelPricePerMTok {
		return 0, fmt.Errorf("price must be finite and between 0 and %.0f USD/M tokens", MaxChannelModelPricePerMTok)
	}
	pico := math.Round(price * float64(PicoPerTokenPerUSDPerMTok))
	if pico < 0 || pico > float64(MaxChannelModelPricePicoPerToken) {
		return 0, fmt.Errorf("price exceeds fixed-point bounds")
	}
	return int64(pico), nil
}

func MustPricePicoPerTokenFromUSDPerMTok(price float64) int64 {
	pico, err := PricePicoPerTokenFromUSDPerMTok(price)
	if err != nil {
		panic(err)
	}
	return pico
}

func PriceUSDPerMTokFromPico(pico int64) float64 {
	if pico <= 0 {
		return 0
	}
	return float64(pico) / float64(PicoPerTokenPerUSDPerMTok)
}

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
	ID                     uint       `gorm:"primaryKey" json:"id"`
	UserID                 uint       `gorm:"index;not null" json:"user_id"`
	TokenName              string     `json:"token_name"`
	ModelName              string     `gorm:"index" json:"model_name"`
	RequestedModel         string     `gorm:"index;size:160;default:''" json:"requested_model"`
	ServedModel            string     `gorm:"index;size:160;default:''" json:"served_model"`
	PromptTokens           int        `json:"prompt_tokens"`
	CompletionTokens       int        `json:"completion_tokens"`
	CachedTokens           int        `json:"cached_tokens"`      // cache read tokens
	CacheWriteTokens       int        `json:"cache_write_tokens"` // cache creation/write tokens
	CacheWrite5mTokens     int        `gorm:"column:cache_write_5m_tokens" json:"cache_write_5m_tokens"`
	CacheWrite1hTokens     int        `gorm:"column:cache_write_1h_tokens" json:"cache_write_1h_tokens"`
	ReasoningTokens        int        `json:"reasoning_tokens"`
	Cost                   int64      `json:"cost"`                                    // 原始 API 等值成本（micro_usd, USD * 1e6）
	ChargedCost            int64      `gorm:"default:0" json:"charged_cost"`           // 套餐/credits 扣减成本（micro_usd），余额扣费仍使用 Cost
	PlatformCostEstimate   int64      `gorm:"default:0" json:"platform_cost_estimate"` // 平台真实账号成本估算（micro_usd，仅毛利分析）
	ModelWeight            float64    `gorm:"default:1" json:"model_weight"`           // 公开模型权重
	HealthMultiplier       float64    `gorm:"default:1" json:"health_multiplier"`      // 公开高峰/健康系数
	BillingRulesVersion    string     `gorm:"size:64;default:''" json:"billing_rules_version"`
	PrecheckInputTokens    int        `gorm:"default:0" json:"precheck_input_tokens"`    // 预检估算输入 tokens（失败请求不计入用量）
	PrecheckOutputTokens   int        `gorm:"default:0" json:"precheck_output_tokens"`   // 预检预留输出 tokens
	PrecheckRawCost        int64      `gorm:"default:0" json:"precheck_raw_cost"`        // 预检 API 等值成本（micro_usd）
	PrecheckChargedCost    int64      `gorm:"default:0" json:"precheck_charged_cost"`    // 预检套餐/credits 扣减成本（micro_usd）
	PrecheckQuotaPlanID    uint       `gorm:"default:0" json:"precheck_quota_plan_id"`   // 触发预检拒绝的 quota_plan
	PrecheckQuotaLimit     int64      `gorm:"default:0" json:"precheck_quota_limit"`     // 当前窗口限额（micro_usd，仅 api_cost_usd 计划）
	PrecheckQuotaUsed      int64      `gorm:"default:0" json:"precheck_quota_used"`      // 当前窗口已用（micro_usd，仅 api_cost_usd 计划）
	PrecheckQuotaRemaining int64      `gorm:"default:0" json:"precheck_quota_remaining"` // 当前窗口剩余（micro_usd，仅 api_cost_usd 计划）
	PrecheckWindowEndAt    *time.Time `json:"precheck_window_end_at"`                    // 当前窗口结束时间
	BlockReason            string     `gorm:"size:96;default:''" json:"block_reason"`    // 机器可读阻断原因
	FallbackUserOptIn      bool       `gorm:"default:false" json:"fallback_user_opt_in"`
	FallbackReason         string     `gorm:"size:160;default:''" json:"fallback_reason"`
	UpstreamProvider       string     `gorm:"index;size:64;default:''" json:"upstream_provider"`   // CPA usage/provider 归因
	UpstreamAuthIndex      string     `gorm:"index;size:64;default:''" json:"upstream_auth_index"` // CPA 稳定账号索引（不可逆）
	UpstreamAuthType       string     `gorm:"size:64;default:''" json:"upstream_auth_type"`        // oauth/api_key 等
	UpstreamSource         string     `gorm:"size:255;default:''" json:"upstream_source"`          // CPA source（admin-only，可辅助定位）
	UpstreamRequestID      string     `gorm:"index;size:64;default:''" json:"upstream_request_id"` // CPA 内部 request_id
	UpstreamUsageRecordID  uint       `gorm:"index;default:0" json:"upstream_usage_record_id"`     // 对应 upstream_usage_records.id
	UpstreamUsageMatch     string     `gorm:"size:64;default:''" json:"upstream_usage_match"`      // exact_tokens / single_candidate_zero_usage
	UpstreamUsageSyncedAt  *time.Time `json:"upstream_usage_synced_at"`
	Latency                int64      `gorm:"default:0" json:"latency"`           // ms延迟 (Parity)
	Status                 int        `gorm:"default:200" json:"status"`          // 状态码或结果记录 (Parity)
	IPAddress              string     `gorm:"index;default:''" json:"ip_address"` // 请求来源IP (Parity)
	RequestPath            string     `gorm:"size:160;default:''" json:"request_path"`
	ErrorType              string     `gorm:"size:64;default:''" json:"error_type"`
	ErrorMessage           string     `gorm:"size:512;default:''" json:"error_message"`
	CreatedAt              time.Time  `gorm:"index" json:"created_at"`
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
