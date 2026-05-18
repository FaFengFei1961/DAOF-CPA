package database

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"os"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"daof-cpa/utils"
)

// DB 暴露出去供全局使用的超高速单例对象
var DB *gorm.DB

func InitDB() {
	var err error

	// fix Major（codex 第八轮）：测试场景里 InitDB 可能被重复调用（验证幂等性）。
	// 旧 *gorm.DB 不显式关闭就被新值覆盖 → 底层 SQLite 文件句柄被泄漏，
	// 测试后清理 t.TempDir 时 unlinkat 失败。重入时主动关旧连接，幂等且不影响首次启动。
	if DB != nil {
		if sqlDB, dbErr := DB.DB(); dbErr == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	}

	// SQLite 数据文件路径可通过 DAOF_DB_PATH 环境变量覆盖；默认 ./daofa-hub.db。
	// 推荐生产部署放 data/ 目录或独立挂载卷，便于备份/迁移。
	dbPath := os.Getenv("DAOF_DB_PATH")
	if dbPath == "" {
		dbPath = "daofa-hub.db"
	}

	// 这里使用 github.com/mattn/go-sqlite3 驱动，完美利用前面 CGO_ENABLED=1 的红利
	// 我们开启单文件的 foreign_keys 支持以策安全，也为了将所有数据打散存在一个物理文件中极大提升寻址能力。
	DB, err = gorm.Open(sqlite.Open(dbPath+"?_fk=1"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})

	if err != nil {
		log.Fatalf("数据库初始化失败: %v\n请确保已开启 CGO_ENABLED=1", err)
	}

	// SQLite 性能 PRAGMA：
	//   WAL 模式：读写并发不互斥（默认 DELETE journal 是全库锁）
	//   synchronous=NORMAL：WAL 下安全且更快（FULL 太保守）
	//   busy_timeout=5000：并发写时等待 5s 而非立即 SQLITE_BUSY 报错
	//   cache_size=-65536：LRU 页缓存 64MB（负数=KB）
	if sqlDB, dbErr := DB.DB(); dbErr == nil {
		_, _ = sqlDB.Exec("PRAGMA journal_mode=WAL")
		_, _ = sqlDB.Exec("PRAGMA synchronous=NORMAL")
		_, _ = sqlDB.Exec("PRAGMA busy_timeout=5000")
		_, _ = sqlDB.Exec("PRAGMA cache_size=-65536")

		// fix Minor（gemini 第四轮）：限制 Go 数据库连接池，避免突发洪峰下耗尽 fd / SQLite locks。
		// SQLite WAL：多读 + 单写串行；25 max open + 5 idle 是 Go+SQLite 主流配置。
		// busy_timeout=5000ms 会让写竞争场景内部排队，配合连接上限能挡住失控并发。
		sqlDB.SetMaxOpenConns(25)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}

	migrateChannelModelFixedPointPricing()

	// 初始化并迁移基础数据表
	err = DB.AutoMigrate(
		&User{}, &Channel{}, &ChannelModel{}, &SysConfig{}, &AccessToken{}, &ApiLog{},
		&ApiLogAttribution{}, &ApiLogCostEstimate{}, &ApiLogRevenue{}, &ApiLogUsageLine{}, &MediaGenerationJob{}, &UpstreamUsageRecord{}, &OperationLog{},
		// 模型目录 + 官方价格快照（文本 token / 图片 / 未来视频）
		&ModelCatalog{}, &ModelPricingRule{},
		// 套餐订阅系统
		&QuotaPlan{}, &Package{}, &PackagePlan{},
		&UserSubscription{}, &SubscriptionUsage{}, &Notification{},
		// 通知增强系统
		&NotificationPreference{}, &NotificationBroadcast{}, &NotificationBroadcastTarget{},
		// 充值订单（易付通对接）+ 退款事实表（Sprint1-P0-6 幂等）+ webhook 回执（Sprint4-M3 防重放）
		&TopupOrder{}, &TopupRefund{}, &PaymentWebhookReceipt{},
		// 工单系统（用户↔admin 多轮会话，关闭 15 天后 cron 物理清除）
		&Ticket{}, &TicketMessage{},
		// CPA 凭证元数据本地缓存（增量同步，避免每次查 quota 都下载凭证文件）
		&CPACredential{},
		// CPA usage auth_index → 真实账号成本映射（毛利核算基础）
		&UpstreamAccountCost{},
		// 账单流水（统一事实表，所有金钱进出落库）+ 对账事实表（Sprint5-M8 状态机闭环）
		&BillingEntry{}, &BillingReconciliation{},
		// 计费规则历史公示快照（append-only，用户侧可查历史版本）
		&BillingRuleRevision{}, &BillingRuleRevisionCancellation{},
		// 优惠券系统（admin 创建模板 → 发给用户 → 购买时使用）
		&CouponTemplate{}, &UserCoupon{},
		// Sprint5-M6 分布式锁（cliproxy_usage_sync 单实例化）+ Sprint5-M1 浏览器 session（OAuth + Logout 真吊销）
		&DistributedLock{}, &UserSession{},
	)
	if err != nil {
		log.Fatalf("数据库结构自动迁移失败: %v", err)
	}

	// 平台运行级默认 SysConfig：站点、OAuth/SMS 占位、CLIProxy、本机采集、熔断与缓存。
	SeedPlatformDefaults()
	// 模型目录 / 官方价格 / 默认本地 CLIProxy 渠道模板。重置后可复现出厂模型池。
	SeedModelRuntimeDefaults()
	// 套餐订阅系统：写入默认 SysConfig（不覆盖已存在的）
	SeedSubscriptionDefaults()
	// 计费规则历史：老库第一次升级时补一条当前规则快照，后续 admin 保存会追加版本。
	SeedCurrentBillingRuleRevision()
	// 计费规则当前活跃版本元数据：老库升级时从历史快照补 published_at / effective_at / revision_id。
	SeedBillingRuleActiveMetadata()
	// 通知增强系统：写入用户偏好默认值
	SeedNotificationDefaults()
	// 充值系统：写入易付通默认配置
	SeedTopupDefaults()
	// 内容审核系统：写入 CPA 模型池 / 关键字 / 阈值等全局共享配置
	SeedModerationDefaults()
	// 模型级端点策略默认值（例如 gpt-5.5 的非流式 chat 兼容性保护）。
	EnforceModelEndpointDefaults()
	// GPT 系列默认用智能审核二判，避免 keyword-only 误杀正常安全/凭证配置咨询。
	EnforceOpenAIGPTModerationDefaults()

	// 业务热点查询的联合索引（GORM tag 不支持多列联合，手动建）
	//
	// fix Minor Mi23-2（codex 第二十三轮）：原 DB.Exec 调用全部忽略 .Error，
	// 旧库索引创建失败时服务静默启动，性能退化不可见。
	// 改为统一调用 mustExecIndex —— 失败 log.Fatalf 阻止启动，运维必须排查。
	mustExecIndex := func(label, sql string) {
		if err := DB.Exec(sql).Error; err != nil {
			log.Fatalf("索引创建失败 %s: %v\nSQL: %s", label, err, sql)
		}
	}
	// 高频查询：UserSubscription 按 user_id + status + end_at 过滤后按 consumption_order 排序
	mustExecIndex("idx_usub_user_status_endAt", `CREATE INDEX IF NOT EXISTS idx_usub_user_status_endAt
		ON user_subscriptions(user_id, status, end_at, consumption_order)`)
	// fix MAJOR M-A8（codex 第二十一轮）：充值退款 reclaim_quota 守卫查询
	//   WHERE user_id = ? AND status != 'refunded' AND is_granted = false
	// 老 idx 不覆盖 is_granted；重用户（数百订阅）退款时扫全部行 → 拖住 SQLite 单写者。
	// 复合索引让该查询直接走 index scan。
	mustExecIndex("idx_usub_user_granted_status", `CREATE INDEX IF NOT EXISTS idx_usub_user_granted_status
		ON user_subscriptions(user_id, is_granted, status)`)
	// fix MAJOR M22-A6（codex 第二十二轮）：cron + admin package 统计/广播查询索引
	// (1) subscription_cron 按 (status, end_at) 扫描快过期订阅 → 标记 expired
	mustExecIndex("idx_usub_status_endat_id", `CREATE INDEX IF NOT EXISTS idx_usub_status_endat_id
		ON user_subscriptions(status, end_at, id)`)
	// (2) admin packages 列表统计 active count + broadcast 按 package 目标都查
	//     (package_id, status, end_at) 过滤后取 user_id distinct
	mustExecIndex("idx_usub_package_status_endat_user", `CREATE INDEX IF NOT EXISTS idx_usub_package_status_endat_user
		ON user_subscriptions(package_id, status, end_at, user_id)`)
	// 优惠券：用户"我的可用券"列表（user_id + status + expires_at），按 granted_at 倒序
	mustExecIndex("idx_user_coupon_user_status", `CREATE INDEX IF NOT EXISTS idx_user_coupon_user_status
		ON user_coupons(user_id, status, expires_at)`)
	// 高频查询：ApiLog 按 user_id + id desc 翻页
	mustExecIndex("idx_apilog_user_id_desc", `CREATE INDEX IF NOT EXISTS idx_apilog_user_id_desc
		ON api_logs(user_id, id DESC)`)
	// CPA usage queue 对账：按模型/时间窗口找未归因 ApiLog
	mustExecIndex("idx_apilog_upstream_match", `CREATE INDEX IF NOT EXISTS idx_apilog_upstream_match
		ON api_logs(upstream_usage_record_id, model_name, created_at)`)
	mustExecIndex("idx_apilog_attr_provider_auth", `CREATE INDEX IF NOT EXISTS idx_apilog_attr_provider_auth
		ON api_log_attributions(upstream_provider, upstream_account_auth_index)`)
	mustExecIndex("idx_apilog_attr_usage_record", `CREATE INDEX IF NOT EXISTS idx_apilog_attr_usage_record
		ON api_log_attributions(upstream_usage_record_id)`)
	mustExecIndex("idx_apilog_cost_computed", `CREATE INDEX IF NOT EXISTS idx_apilog_cost_computed
		ON api_log_cost_estimates(computed_at)`)
	mustExecIndex("idx_upusage_match_status", `CREATE INDEX IF NOT EXISTS idx_upusage_match_status
		ON upstream_usage_records(match_status, timestamp)`)
	mustExecIndex("idx_upusage_created_at", `CREATE INDEX IF NOT EXISTS idx_upusage_created_at
		ON upstream_usage_records(created_at ASC)`)
	// fix Major M7（claude perf 第十五轮）：cron 清理按 created_at < cutoff 扫描，
	// 没有该索引会全表扫；百万行级别下 100ms+ 阻塞写事务。
	mustExecIndex("idx_apilog_created_at", `CREATE INDEX IF NOT EXISTS idx_apilog_created_at
		ON api_logs(created_at ASC)`)
	// 高频查询：Notification 未读列表（部分索引）
	if err := DB.Exec(`DROP INDEX IF EXISTS idx_notif_user_unread`).Error; err != nil {
		log.Fatalf("索引重建失败 idx_notif_user_unread(drop): %v", err)
	}
	mustExecIndex("idx_notif_user_unread", `CREATE INDEX IF NOT EXISTS idx_notif_user_unread
		ON notifications(user_id, created_at DESC)
		WHERE read_at IS NULL AND revoked_at IS NULL`)
	// 高频查询：SubscriptionUsage 已有 idx_sub_plan_bucket（GORM uniqueIndex），无需补
	// 高频查询：admin broadcast 历史列表按状态 + 时间倒序
	mustExecIndex("idx_bcast_status_created", `CREATE INDEX IF NOT EXISTS idx_bcast_status_created
		ON notification_broadcasts(status, created_at DESC)`)
	// 高频查询：用户充值订单列表（按 user + created_at desc 翻页）
	mustExecIndex("idx_topup_user_created", `CREATE INDEX IF NOT EXISTS idx_topup_user_created
		ON topup_orders(user_id, created_at DESC)`)
	// 高频查询：admin 充值订单按状态 + 时间倒序
	mustExecIndex("idx_topup_status_created", `CREATE INDEX IF NOT EXISTS idx_topup_status_created
		ON topup_orders(status, created_at DESC)`)
	// 工单系统：用户工单列表按 user + last_message_at 排序
	mustExecIndex("idx_ticket_user_lastmsg", `CREATE INDEX IF NOT EXISTS idx_ticket_user_lastmsg
		ON tickets(user_id, last_message_at DESC)`)
	// admin 工单列表按状态 + last_message_at
	mustExecIndex("idx_ticket_status_lastmsg", `CREATE INDEX IF NOT EXISTS idx_ticket_status_lastmsg
		ON tickets(status, last_message_at DESC)`)
	// 工单消息按 ticket_id + created_at 翻页
	mustExecIndex("idx_ticket_msg_ticket_created", `CREATE INDEX IF NOT EXISTS idx_ticket_msg_ticket_created
		ON ticket_messages(ticket_id, created_at ASC)`)
	// CPA 凭证缓存：admin 面板按 provider + 启用状态过滤的高频组合
	mustExecIndex("idx_cpa_cred_provider_disabled", `CREATE INDEX IF NOT EXISTS idx_cpa_cred_provider_disabled
		ON cpa_credentials(provider, disabled)`)
	// cleanupStaleCPACredentials 周期性扫描 disabled=true AND last_seen_at < cutoff，
	// 加 (disabled, last_seen_at) 复合索引让该查询能直接走索引扫描而非全表
	mustExecIndex("idx_cpa_cred_disabled_last_seen", `CREATE INDEX IF NOT EXISTS idx_cpa_cred_disabled_last_seen
		ON cpa_credentials(disabled, last_seen_at)`)
	// 账单系统：用户/类型/时间三种组合查询都要走索引
	// (user_id, occurred_at DESC)：用户账单列表（最高频，无类型筛选）
	mustExecIndex("idx_billing_user_time", `CREATE INDEX IF NOT EXISTS idx_billing_user_time
		ON billing_entries(user_id, occurred_at DESC)`)
	// fix Major M8（codex+claude 第十四轮）：复合索引覆盖 (user_id, entry_type, occurred_at DESC)
	// 让"用户按类型筛选"查询直接走索引而非扫所有用户行后过滤。
	// 重 API 用户（数千条 api_usage_sub 行）筛选 types=topup 时性能差异显著。
	mustExecIndex("idx_billing_user_type_time", `CREATE INDEX IF NOT EXISTS idx_billing_user_type_time
		ON billing_entries(user_id, entry_type, occurred_at DESC)`)
	// (entry_type, occurred_at DESC)：admin 全局某类型流水
	mustExecIndex("idx_billing_type_time", `CREATE INDEX IF NOT EXISTS idx_billing_type_time
		ON billing_entries(entry_type, occurred_at DESC)`)
	// pending_reconcile / upstream_unmetered 后台对账列表
	mustExecIndex("idx_billing_state_time", `CREATE INDEX IF NOT EXISTS idx_billing_state_time
		ON billing_entries(billing_state, occurred_at DESC)`)
	// (related_type, related_id)：从原始记录反查账单条目
	mustExecIndex("idx_billing_related", `CREATE INDEX IF NOT EXISTS idx_billing_related
		ON billing_entries(related_type, related_id)`)
	mustExecIndex("idx_billing_related_unique", `CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_related_unique
		ON billing_entries(related_type, related_id, entry_type)
		WHERE related_id > 0`)
	mustExecIndex("idx_billing_summary", `CREATE INDEX IF NOT EXISTS idx_billing_summary
		ON billing_entries(user_id, entry_type, occurred_at, amount_usd)`)
	mustExecIndex("idx_session_user_revoked", `CREATE INDEX IF NOT EXISTS idx_session_user_revoked
		ON user_sessions(user_id) WHERE revoked_at IS NULL`)
	// fix MEDIUM M19-5（codex 第十九轮）：注册路径 registerMu 临界区里要做
	// COUNT(*) WHERE role='user' 检查注册总数上限——表大了之后 SQLite/PG 都会做全表扫描，
	// 单次几十毫秒。新部署 schema 标签生成索引；老库的"已存在表无索引"用 IF NOT EXISTS 兜底。
	mustExecIndex("idx_users_role", `CREATE INDEX IF NOT EXISTS idx_users_role ON users(role)`)

	// fix MAJOR M-B9 / M22-2（codex 第二十一/二十二轮）：GithubID/Phone 唯一性走 partial unique index。
	//
	// schema.go 已去掉 GORM `uniqueIndex` tag 改成 `index`，所以 AutoMigrate 不再创建普通 unique 索引。
	// 旧库可能仍有 GORM 早期版本生成的 `idx_users_github_id` / `idx_users_phone` unique 索引——
	// 用 DROP INDEX IF EXISTS 兜底清理。然后用 partial unique 替代（排除空串）。
	//
	// 效果：
	//   - 多个用户 github_id="" / phone="" 都允许（视为"未绑定"，与 NULL 同义）
	//   - 真实绑定值（如某 GitHub 用户名）仍唯一
	//   - NULL 仍允许多个共存（partial unique 默认行为）
	DB.Exec(`DROP INDEX IF EXISTS idx_users_github_id`)
	DB.Exec(`DROP INDEX IF EXISTS idx_users_phone`)
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uniq_users_phone_nonempty
		ON users(phone) WHERE phone IS NOT NULL AND phone <> ''`)
	DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uniq_users_github_id_nonempty
		ON users(github_id) WHERE github_id IS NOT NULL AND github_id <> ''`)

	// fix Suggestion Phase 4-codex（第二十四轮）：DB 层 partial index 兜底"零金额类型 invariant"。
	//
	// IsZeroAmountBillingType（billing_schema.go）的类型必须 amount_usd=0；应用层 helper
	// WriteBillingEntry 已校验，但 admin 直改 DB / raw SQL 会绕过。加一条 trigger-style
	// CHECK 在 DB 层兜底（partial unique index 实际上 SQLite 不支持 CHECK 约束 ALTER，
	// 改用 NOT EXISTS 触发器思路：违规行存在时启动报警）。
	//
	// SQLite 不支持 ALTER TABLE ADD CONSTRAINT，无法事后加 CHECK；改为启动时扫描，
	// 发现违规行立即 log.Fatalf 防"已损坏 schema 还继续跑"。
	{
		var violatingCount int64
		DB.Raw(`SELECT COUNT(*) FROM billing_entries
			WHERE entry_type IN ('api_usage_sub','api_usage_pending_reconcile',
			                     'admin_grant_sub','admin_revoke_grant')
			  AND amount_usd != 0`).Scan(&violatingCount)
		if violatingCount > 0 {
			log.Fatalf("[INVARIANT-VIOLATED] %d billing_entries 行 entry_type 为零金额类型但 amount_usd != 0；"+
				"DB 直改/历史数据损坏。运行 SELECT id, user_id, entry_type, amount_usd FROM billing_entries "+
				"WHERE entry_type IN (...) AND amount_usd != 0 排查后再启动。", violatingCount)
		}
	}

	// 自动植入默认的根管理员记录（如果整个星球还没有任何管理员）
	// 安全：admin token 使用密码学随机生成，不再硬编码
	var adminUser User
	DB.Where("role = ?", "admin").First(&adminUser)
	if adminUser.ID == 0 {
		adminUser = User{
			Username:     "root",
			PasswordHash: utils.GenerateHash("123456"), // Default root pass — 首次登录后必须 setup 改掉
			Role:         "admin",
			Token:        utils.GenerateRandomToken("sk-daof-root"),
			Quota:        99999 * MicroPerUSD, // 99999 USD（micro_usd）给 root 默认大额度
			Status:       1,
		}
		DB.Create(&adminUser)
		log.Println("🔑 默认管理员账户 [root] 创建成功。")
	}

	// 回填 quota_plans.limit_value_micro_usd：codex 加 int64 字段时只在 seed 路径写新值，
	// 早期创建的 plan 该列默认 0 → admin API 错把 limit=0 当作"不限"。
	// 一次性扫所有 api_cost_usd plan，把 limit_value(USD float) × 1e6 写入 limit_value_micro_usd。
	// 已有正确值的不动（limit_value_micro_usd > 0）。
	backfillQuotaPlanLimitMicroUSD()

	// fix CRITICAL（codex review --uncommitted）：backfill api_log_revenues 侧表，
	// 把"migration 前已落库但没有 revenue 行"的旧 api_logs 一次性补齐。否则跨越迁移点
	// 的毛利报表周期会显示 total_revenue=0（因为 LEFT JOIN api_log_revenues 全 NULL）。
	backfillApiLogRevenues()

	log.Println("⚡️ 数据库连接成功，数据库结构迁移完成。")
}

// backfillApiLogRevenues 一次性把"没有 api_log_revenues 对应行但有真实 BillingEntry 落账"
// 的旧 api_logs 补写到侧表，让跨越迁移点的毛利报表能查出 revenue。
//
// fix P1（codex review verify-1）：原实现 `CASE WHEN charged_cost > 0 THEN charged_cost ELSE cost END`
// 有两个严重错误：
//  1. 包含 pending_reconcile 请求（stream.go 故意不写 revenue，因为没收到钱）→ 欠款被算成 revenue
//  2. balance 路径的 ApiLog.charged_cost > 0 但实际扣的是 cost（rawCost）→ 报表口径错乱
//
// 修复：INNER JOIN billing_entries 区分真实计费类型：
//   - entry_type='api_usage_sub'       → revenue_source=subscription, effective=ApiLog.charged_cost；若命中赠送订阅则 effective=0
//   - entry_type='api_consume_balance' → revenue_source=balance,      effective=ApiLog.cost (raw)
//   - 其他类型（pending_reconcile / unmetered / 等）→ 完全跳过（保持 NULL 与正常 RecordApiLogRevenue 路径一致）
//
// 幂等：api_log_id 是 uniqueIndex，INSERT OR IGNORE 跳过冲突。
// 新部署无 BillingEntry 时 INNER JOIN 自然返回 0 行，无副作用。
func backfillApiLogRevenues() {
	if !DB.Migrator().HasTable(&ApiLog{}) || !DB.Migrator().HasTable(&ApiLogRevenue{}) || !DB.Migrator().HasTable(&BillingEntry{}) {
		return
	}
	// 按 BillingEntry 类型决定 source + effective：
	//   subscription：赠送订阅写 0；付费订阅优先用 charged_cost；为 0 时 fallback 到 cost（早期未写 charged_cost 的旧行）
	//   balance：     走 cost (raw)
	//
	// fix P2（codex review verify-final）：原实现对 charged_cost=0 的 api_usage_sub 行直接写 0
	// → 唯一索引导致 0-revenue 行永久存在 → 旧报表的 subscription revenue 被低估。
	sql := `INSERT OR IGNORE INTO api_log_revenues
		(api_log_id, revenue_source, effective_revenue_micro_usd, subscription_id, recorded_at, created_at)
		SELECT
			a.id,
			CASE be.entry_type
				WHEN ? THEN ?
				WHEN ? THEN ?
			END AS revenue_source,
			CASE be.entry_type
				WHEN ? THEN CASE
					WHEN COALESCE(us.is_granted, 0) = 1 THEN 0
					WHEN a.charged_cost > 0 THEN a.charged_cost
					ELSE a.cost
				END
				WHEN ? THEN a.cost
			END AS effective,
			COALESCE(be.source_subscription_id, 0) AS subscription_id,
			a.created_at AS recorded_at,
			a.created_at AS created_at
		FROM api_logs a
		INNER JOIN billing_entries be
			ON be.related_type = 'api_log' AND be.related_id = a.id
			AND be.entry_type IN (?, ?)
		LEFT JOIN user_subscriptions us
			ON us.id = be.source_subscription_id
		LEFT JOIN api_log_revenues r ON r.api_log_id = a.id
		WHERE r.id IS NULL
		  AND a.status >= 200 AND a.status < 400`
	res := DB.Exec(sql,
		BillingTypeApiUsageSub, RevenueSourceSubscription,
		BillingTypeApiConsumeBalance, RevenueSourceBalance,
		BillingTypeApiUsageSub,
		BillingTypeApiConsumeBalance,
		BillingTypeApiUsageSub, BillingTypeApiConsumeBalance,
	)
	if res.Error != nil {
		log.Printf("[BACKFILL-REVENUE] failed: %v", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		log.Printf("[BACKFILL-REVENUE] 补写 %d 行历史 api_log_revenues（按 BillingEntry 区分 subscription/balance）", res.RowsAffected)
	}
}

const (
	billingModelWeightsConfigKey      = "billing_model_weights_json"
	billingHealthMultipliersConfigKey = "billing_health_multipliers_json"
	billingRulesVersionConfigKey      = "billing_rules_version"
	billingRulesPublishedAtConfigKey  = "billing_rules_published_at"
	billingRulesEffectiveAtConfigKey  = "billing_rules_effective_at"
	billingRulesRevisionIDConfigKey   = "billing_rules_revision_id"
)

// SeedCurrentBillingRuleRevision 给升级前已经存在的库补一条当前规则快照。
//
// 之后每次 admin 保存规则都会在 controller.UpdateBillingRules 里追加新 revision；
// 这里仅在历史表为空时运行，避免启动时重复制造版本。
func SeedCurrentBillingRuleRevision() {
	if DB == nil || !DB.Migrator().HasTable(&BillingRuleRevision{}) {
		return
	}
	var count int64
	if err := DB.Model(&BillingRuleRevision{}).Count(&count).Error; err != nil {
		log.Printf("[BILLING-RULES-HISTORY] count failed: %v", err)
		return
	}
	if count > 0 {
		return
	}

	version := readPlainSysConfigForRevision(billingRulesVersionConfigKey, SubscriptionSysConfigDefaults[billingRulesVersionConfigKey])
	modelJSON := readPlainSysConfigForRevision(billingModelWeightsConfigKey, SubscriptionSysConfigDefaults[billingModelWeightsConfigKey])
	healthJSON := readPlainSysConfigForRevision(billingHealthMultipliersConfigKey, SubscriptionSysConfigDefaults[billingHealthMultipliersConfigKey])
	if version == "" || modelJSON == "" || healthJSON == "" {
		log.Printf("[BILLING-RULES-HISTORY] skip initial revision: incomplete config")
		return
	}

	now := time.Now().UTC()
	rev := BillingRuleRevision{
		Version:               version,
		EffectiveSince:        billingRevisionEffectiveSince(version),
		PublishedAt:           &now,
		EffectiveAt:           &now,
		ModelWeightsJSON:      modelJSON,
		HealthMultipliersJSON: healthJSON,
		ModelCount:            countBillingRuleJSONArray(modelJSON),
		HealthCount:           countBillingRuleJSONArray(healthJSON),
		Source:                "current_backfill",
	}
	if err := DB.Create(&rev).Error; err != nil {
		log.Printf("[BILLING-RULES-HISTORY] create initial revision failed: %v", err)
		return
	}
	log.Printf("[BILLING-RULES-HISTORY] backfilled current billing rules revision %q", version)
}

func SeedBillingRuleActiveMetadata() {
	if DB == nil || !DB.Migrator().HasTable(&BillingRuleRevision{}) || !DB.Migrator().HasTable(&SysConfig{}) {
		return
	}
	version := readPlainSysConfigForRevision(billingRulesVersionConfigKey, SubscriptionSysConfigDefaults[billingRulesVersionConfigKey])
	if version == "" {
		return
	}
	var rev BillingRuleRevision
	if err := DB.Where("version = ?", version).Order("created_at DESC, id DESC").First(&rev).Error; err != nil {
		log.Printf("[BILLING-RULES-HISTORY] active metadata backfill skipped: version=%q revision not found: %v", version, err)
		return
	}
	publishedAt := revisionTimeOrFallback(rev.PublishedAt, rev.CreatedAt)
	effectiveAt := revisionTimeOrFallback(rev.EffectiveAt, rev.CreatedAt)
	err := DB.Transaction(func(tx *gorm.DB) error {
		if readPlainSysConfigForRevisionInTx(tx, billingRulesRevisionIDConfigKey) == "" {
			if err := upsertPlainSysConfigInTx(tx, billingRulesRevisionIDConfigKey, fmt.Sprintf("%d", rev.ID)); err != nil {
				return err
			}
		}
		if readPlainSysConfigForRevisionInTx(tx, billingRulesPublishedAtConfigKey) == "" && !publishedAt.IsZero() {
			if err := upsertPlainSysConfigInTx(tx, billingRulesPublishedAtConfigKey, publishedAt.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
		}
		if readPlainSysConfigForRevisionInTx(tx, billingRulesEffectiveAtConfigKey) == "" && !effectiveAt.IsZero() {
			if err := upsertPlainSysConfigInTx(tx, billingRulesEffectiveAtConfigKey, effectiveAt.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("[BILLING-RULES-HISTORY] active metadata backfill failed: %v", err)
	}
}

func revisionTimeOrFallback(value *time.Time, fallback time.Time) time.Time {
	if value != nil && !value.IsZero() {
		return value.UTC()
	}
	return fallback.UTC()
}

func readPlainSysConfigForRevision(key, fallback string) string {
	var row SysConfig
	if err := DB.Where("key = ?", key).First(&row).Error; err == nil {
		plain, decErr := utils.Decrypt(row.Value)
		if decErr == nil && plain != "" {
			return plain
		}
		if decErr != nil {
			log.Printf("[BILLING-RULES-HISTORY] decrypt %s failed: %v", key, decErr)
		}
	}
	return fallback
}

func readPlainSysConfigForRevisionInTx(tx *gorm.DB, key string) string {
	var row SysConfig
	if err := tx.Where("key = ?", key).First(&row).Error; err != nil {
		return ""
	}
	plain, err := utils.Decrypt(row.Value)
	if err != nil {
		log.Printf("[BILLING-RULES-HISTORY] decrypt %s failed: %v", key, err)
		return ""
	}
	return plain
}

func countBillingRuleJSONArray(raw string) int {
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return 0
	}
	return len(rows)
}

func billingRevisionEffectiveSince(version string) string {
	if len(version) < 10 {
		return ""
	}
	tail := version[len(version)-10:]
	if tail[4] != '-' || tail[7] != '-' {
		return ""
	}
	for i, c := range tail {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return ""
		}
	}
	return tail
}

func migrateChannelModelFixedPointPricing() {
	if !sqliteTableExists("channel_models") {
		return
	}

	mappings := []struct {
		oldColumn string
		newColumn string
	}{
		{"input_price", "input_price_pico_per_token"},
		{"output_price", "output_price_pico_per_token"},
		{"cached_input_price", "cached_input_price_pico_per_token"},
		{"cache_write_input_price", "cache_write_input_price_pico_per_token"},
		{"cache_write_1h_input_price", "cache_write_1h_input_price_pico_per_token"},
		{"cache_write1h_input_price", "cache_write_1h_input_price_pico_per_token"},
		{"high_input_price", "high_input_price_pico_per_token"},
		{"high_cached_input_price", "high_cached_input_price_pico_per_token"},
		{"high_output_price", "high_output_price_pico_per_token"},
	}

	for _, m := range mappings {
		if sqliteColumnExists("channel_models", m.newColumn) {
			continue
		}
		if err := DB.Exec(fmt.Sprintf(`ALTER TABLE channel_models ADD COLUMN %s INTEGER DEFAULT 0`, m.newColumn)).Error; err != nil {
			log.Fatalf("[migrate] add channel_models.%s failed: %v", m.newColumn, err)
		}
	}

	for _, m := range mappings {
		if !sqliteColumnExists("channel_models", m.oldColumn) {
			continue
		}
		backfillChannelModelFixedPointColumn(m.oldColumn, m.newColumn)
	}

	for _, m := range mappings {
		if !sqliteColumnExists("channel_models", m.oldColumn) {
			continue
		}
		if err := DB.Exec(fmt.Sprintf(`ALTER TABLE channel_models DROP COLUMN %s`, m.oldColumn)).Error; err != nil {
			log.Fatalf("[migrate] drop legacy channel_models.%s failed: %v", m.oldColumn, err)
		}
	}
}

func sqliteTableExists(table string) bool {
	var count int64
	if err := DB.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count).Error; err != nil {
		log.Printf("[migrate] sqlite table lookup failed for %s: %v", table, err)
		return false
	}
	return count > 0
}

func sqliteColumnExists(table, column string) bool {
	var rows []struct {
		Name string
	}
	if err := DB.Raw(`PRAGMA table_info(` + table + `)`).Scan(&rows).Error; err != nil {
		log.Printf("[migrate] sqlite column lookup failed for %s.%s: %v", table, column, err)
		return false
	}
	for _, row := range rows {
		if row.Name == column {
			return true
		}
	}
	return false
}

func backfillQuotaPlanLimitMicroUSD() {
	if !sqliteTableExists("quota_plans") ||
		!sqliteColumnExists("quota_plans", "limit_value") ||
		!sqliteColumnExists("quota_plans", "limit_value_micro_usd") {
		return
	}

	type quotaPlanLimitRow struct {
		ID         uint
		LimitValue float64
	}
	var rows []quotaPlanLimitRow
	if err := DB.Raw(`SELECT id, limit_value
		FROM quota_plans
		WHERE limit_unit = 'api_cost_usd'
		  AND limit_value_micro_usd = 0
		  AND limit_value > 0`).Scan(&rows).Error; err != nil {
		log.Printf("[migrate] query quota_plans.limit_value_micro_usd backfill: %v", err)
		return
	}
	for _, row := range rows {
		micro, ok := int64FromScaledFloat(row.LimitValue, MicroPerUSD)
		if !ok || micro <= 0 {
			log.Fatalf("[migrate] invalid quota_plans.limit_value id=%d value=%v", row.ID, row.LimitValue)
		}
		if err := DB.Exec(`UPDATE quota_plans
			SET limit_value_micro_usd = ?
			WHERE id = ? AND limit_value_micro_usd = 0`, micro, row.ID).Error; err != nil {
			log.Fatalf("[migrate] backfill quota_plans.limit_value_micro_usd id=%d failed: %v", row.ID, err)
		}
	}

	var remaining int64
	if err := DB.Raw(`SELECT COUNT(*) FROM quota_plans
		WHERE limit_unit = 'api_cost_usd'
		  AND limit_value_micro_usd = 0
		  AND limit_value > 0`).Scan(&remaining).Error; err != nil {
		log.Printf("[migrate] verify quota_plans.limit_value_micro_usd backfill: %v", err)
		return
	}
	if remaining > 0 {
		log.Fatalf("[migrate] quota_plans.limit_value_micro_usd backfill left %d nonzero legacy rows", remaining)
	}
}

func backfillChannelModelFixedPointColumn(oldColumn, newColumn string) {
	type channelModelPriceRow struct {
		ID       uint
		OldValue float64
	}
	var rows []channelModelPriceRow
	if err := DB.Table("channel_models").
		Select(fmt.Sprintf("id, %s AS old_value", oldColumn)).
		Where(fmt.Sprintf("%s > 0 AND %s = 0", oldColumn, newColumn)).
		Scan(&rows).Error; err != nil {
		log.Fatalf("[migrate] query channel_models.%s backfill failed: %v", newColumn, err)
	}
	for _, row := range rows {
		pico, ok := int64FromScaledFloat(row.OldValue, PicoPerTokenPerUSDPerMTok)
		if !ok || pico <= 0 || pico > MaxChannelModelPricePicoPerToken {
			log.Fatalf("[migrate] invalid channel_models.%s id=%d value=%v", oldColumn, row.ID, row.OldValue)
		}
		if err := DB.Exec(fmt.Sprintf(`UPDATE channel_models
			SET %s = ?
			WHERE id = ? AND %s = 0`, newColumn, newColumn), pico, row.ID).Error; err != nil {
			log.Fatalf("[migrate] backfill channel_models.%s id=%d failed: %v", newColumn, row.ID, err)
		}
	}

	var remaining int64
	if err := DB.Raw(fmt.Sprintf(`SELECT COUNT(*) FROM channel_models
		WHERE %s > 0 AND %s = 0`, oldColumn, newColumn)).Scan(&remaining).Error; err != nil {
		log.Fatalf("[migrate] verify channel_models.%s backfill failed: %v", newColumn, err)
	}
	if remaining > 0 {
		log.Fatalf("[migrate] channel_models.%s backfill left %d nonzero legacy rows", newColumn, remaining)
	}
}

func int64FromScaledFloat(value float64, scale int64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || scale <= 0 {
		return 0, false
	}
	r := new(big.Rat).SetFloat64(value)
	if r == nil {
		return 0, false
	}
	r.Mul(r, new(big.Rat).SetInt64(scale))
	return roundedRatToInt64(r)
}

func roundedRatToInt64(r *big.Rat) (int64, bool) {
	if r == nil {
		return 0, false
	}
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.QuoRem(r.Num(), r.Denom(), remainder)

	absTwiceRemainder := new(big.Int).Abs(remainder)
	absTwiceRemainder.Mul(absTwiceRemainder, big.NewInt(2))
	if absTwiceRemainder.Cmp(r.Denom()) >= 0 {
		if r.Sign() >= 0 {
			quotient.Add(quotient, big.NewInt(1))
		} else {
			quotient.Sub(quotient, big.NewInt(1))
		}
	}
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}
