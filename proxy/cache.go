package proxy

import (
	"log"
	"sync"

	"daof-cpa/database"
	"daof-cpa/utils"
)

// fix CRITICAL Sprint4-M2：合并 channel/route 锁 + 合并 auth/authToken 锁
// 原 4 把 RWMutex 分别保护 4 个 map，导致跨 cache 读取存在 race window：
//   reader: lockA.RLock → mapA（旧）→ lockA.RUnlock
//   writer:                          lockA.Lock → mapA 换新 → Unlock
//                                    lockB.Lock → mapB 换新 → Unlock
//   reader: lockB.RLock → mapB（新）→ lockB.RUnlock
// 结果：reader 拿到旧 mapA + 新 mapB，使用了失效的组合（例如旧 route + 新 channel）。
//
// 修复：用 gatewayMutex 同时守护 ChannelMapCache + RouteCache，authSnapshotMutex
// 同时守护 AuthCache + AuthTokenCache。SyncCacheConfig 一次 Lock 同时 swap 双 map，
// reader 同一 RLock 段内读双 map 保证一致快照。
//
// 性能影响：读路径多读一个 map 在同一 RLock 段内，无额外锁开销；写路径只取一次锁
// 同时 swap 两个 map，比旧"取两次锁"更短的临界区。
var (
	// ChannelMapCache + RouteCache 共享 gatewayMutex，保证跨 cache 读取一致性
	ChannelMapCache map[uint]*database.Channel             // key: channel_id
	RouteCache      map[string][]*database.ChannelModel    // key: model_name
	gatewayMutex    sync.RWMutex

	// AuthCache + AuthTokenCache 共享 authSnapshotMutex
	AuthCache         map[string]*database.User        // key: token
	AuthTokenCache    map[string]*database.AccessToken // key: token key
	authSnapshotMutex sync.RWMutex

	// SysConfigCache key: config_key, value: decrypted value
	SysConfigCache map[string]string
	SysConfigMutex sync.RWMutex
)

func init() {
	// 系统级变量提早构建
	ChannelMapCache = make(map[uint]*database.Channel)
	RouteCache = make(map[string][]*database.ChannelModel)
	AuthCache = make(map[string]*database.User)
	AuthTokenCache = make(map[string]*database.AccessToken)
	SysConfigCache = make(map[string]string)
}

// LookupUserByToken 安全地通过 token 查询缓存中的 User 指针（线程安全）
func LookupUserByToken(token string) *database.User {
	authSnapshotMutex.RLock()
	defer authSnapshotMutex.RUnlock()
	return AuthCache[token]
}

// AddUserToAuthCache 增量加新用户到 AuthCache（注册路径用，避免全量 SyncCacheConfig 的 N+1）
func AddUserToAuthCache(user *database.User) {
	if user == nil || user.Token == "" {
		return
	}
	authSnapshotMutex.Lock()
	AuthCache[user.Token] = user
	authSnapshotMutex.Unlock()
}

// EvictUserToken 精准从 AuthCache 移除某个 token 的 entry。
// 用于即时封禁场景：admin 把 user.Status 改 2 之后立刻调用，
// 让被封用户的 token 立即失效，无需等下次 SyncCacheConfig 全量刷新。
//
// 注意：调用方应负责把 user 从 DB 角度也标记为 banned，
// 这里只管 cache 一致性。重复调用安全（map delete 不存在 key 也是 no-op）。
func EvictUserToken(token string) {
	if token == "" {
		return
	}
	authSnapshotMutex.Lock()
	delete(AuthCache, token)
	authSnapshotMutex.Unlock()
}

// RefreshUserAuth 精确刷新某个用户在 AuthCache 中的实例。
// 用于充值到账、订阅退款、quota 调整等修改 user.Quota 的场景，避免 GetSelfData 返回陈旧值。
//
// 设计：从 DB 重新查 user，按 token 替换 AuthCache 里的对象。AuthTokenCache（子凭证）
// 不需要刷新——它存的是 *AccessToken，不含 user.Quota。
func RefreshUserAuth(userID uint) {
	if userID == 0 {
		return
	}
	var user database.User
	if err := database.DB.First(&user, userID).Error; err != nil {
		log.Printf("[AUTH-REFRESH] user=%d not found: %v", userID, err)
		return
	}
	if user.Token == "" {
		return
	}

	// fix CRITICAL Sprint4-M2：合并锁简化前后一致性。
	// 旧实现需要嵌套两层锁（auth token + auth user）来同时改两个 map；
	// 合并 authSnapshotMutex 后单 Lock 内同时读 AuthTokenCache + 写 AuthCache。
	//
	// 历史风险（已自然消除）：
	//  1. 双层 RLock→Lock 升级窗口里 SyncCacheConfig 写者插入 → 写回死键
	//  2. delete(AuthCache,...) 与 delete(AuthTokenCache,...) 不同步 → map race
	// 现在 authSnapshotMutex.Lock() 同时排他双 map，race 与升级窗口都关闭。

	authSnapshotMutex.Lock()
	defer authSnapshotMutex.Unlock()

	subKeys := make([]string, 0, 4)
	for k, t := range AuthTokenCache {
		if t.UserID == userID {
			subKeys = append(subKeys, k)
		}
	}

	if user.Status != 1 {
		// 封禁/异常：清主 token + 所有子 token（双 map 同步驱逐）
		delete(AuthCache, user.Token)
		for _, k := range subKeys {
			delete(AuthTokenCache, k)
			delete(AuthCache, k)
		}
		log.Printf("[AUTH-REFRESH] user=%d status=%d → evicted main + %d sub tokens", userID, user.Status, len(subKeys))
		return
	}

	// 正常路径：刷新主 token + 所有子 token 都指向新 user 对象（含最新 Quota）
	AuthCache[user.Token] = &user
	for _, k := range subKeys {
		AuthCache[k] = &user
	}
}

// SyncCacheConfig 钩子：查询 DB，并在并行锁的保护下暴力覆写高速内存池，耗时远 < 2ms
func SyncCacheConfig() {
	var channels []database.Channel
	var channelModels []database.ChannelModel
	var users []database.User
	var sysConfigs []database.SysConfig
	var accessTokens []database.AccessToken

	// CRITICAL: 任一查询失败立即 abort 整次同步，绝不能用部分/空结果覆盖活缓存
	// （否则会触发全站 401 / 路由失效，且无任何告警）
	if err := database.DB.Where("status = ?", 1).Find(&channels).Error; err != nil {
		log.Printf("[CACHE] FATAL load channels failed, abort sync: %v", err)
		return
	}
	if err := database.DB.Where("status = ?", 1).Find(&channelModels).Error; err != nil {
		log.Printf("[CACHE] FATAL load channel_models failed, abort sync: %v", err)
		return
	}
	if err := database.DB.Where("status = ?", 1).Find(&users).Error; err != nil {
		log.Printf("[CACHE] FATAL load users failed, abort sync: %v", err)
		return
	}
	if err := database.DB.Find(&sysConfigs).Error; err != nil {
		log.Printf("[CACHE] FATAL load sys_configs failed, abort sync: %v", err)
		return
	}
	if err := database.DB.Where("status = ?", 1).Find(&accessTokens).Error; err != nil {
		log.Printf("[CACHE] FATAL load access_tokens failed, abort sync: %v", err)
		return
	}

	// ====================
	// 1+2. Gateway 快照（channel + route）原子发布
	// ====================
	// fix Major SSRF：纵深防御——即使 controller 校验被绕过（旧数据/SQL 注入），
	// 在 cache 层也拒绝向不安全 URL 的 channel 发出请求。坏 channel 不进缓存即等同被禁用。
	newChannelMap := make(map[uint]*database.Channel)
	for i := range channels {
		ch := &channels[i]
		if err := ValidateChannelURL(ch.BaseURL); err != nil {
			log.Printf("[CACHE] QUARANTINE channel id=%d name=%q base_url 安全校验失败: %v", ch.ID, ch.Name, err)
			continue
		}
		if err := ValidateChannelURL(ch.ProxyURL); err != nil {
			log.Printf("[CACHE] QUARANTINE channel id=%d name=%q proxy_url 安全校验失败: %v", ch.ID, ch.Name, err)
			continue
		}
		newChannelMap[ch.ID] = ch
	}
	newRouteMap := make(map[string][]*database.ChannelModel)
	for i := range channelModels {
		chm := &channelModels[i]
		if err := database.ValidateChannelModelPricing(chm); err != nil {
			log.Printf("[CACHE] QUARANTINE channel_model id=%d model=%q price validation failed: %v", chm.ID, chm.ModelID, err)
			continue
		}
		// 只有该信道存活时，这个模型挂载才有意义
		if _, exists := newChannelMap[chm.ChannelID]; exists {
			newRouteMap[chm.ModelID] = append(newRouteMap[chm.ModelID], chm)
		}
	}
	// fix CRITICAL Sprint4-M2：单次 Lock 内同时 swap channel + route，杜绝 reader 拿到
	// 旧 route + 新 channel 的不一致快照
	gatewayMutex.Lock()
	ChannelMapCache = newChannelMap
	RouteCache = newRouteMap
	gatewayMutex.Unlock()

	// ====================
	// 3. Auth 快照（user + access_token）原子发布
	// ====================
	newAuthMap := make(map[string]*database.User)
	newAuthTokenMap := make(map[string]*database.AccessToken)
	userFastFind := make(map[uint]*database.User)
	for i := range users {
		usr := &users[i]
		newAuthMap[usr.Token] = usr
		userFastFind[usr.ID] = usr
	}
	// 折叠导入子凭证
	for i := range accessTokens {
		tk := &accessTokens[i]
		if parentUsr, exists := userFastFind[tk.UserID]; exists {
			newAuthMap[tk.Key] = parentUsr
			newAuthTokenMap[tk.Key] = tk
		}
	}
	// fix CRITICAL Sprint4-M2：单次 Lock 内同时 swap auth + access token，杜绝新子 token
	// 短暂绕过 AuthTokenCache 检查的 race window
	authSnapshotMutex.Lock()
	AuthCache = newAuthMap
	AuthTokenCache = newAuthTokenMap
	authSnapshotMutex.Unlock()

	// ====================
	// 4. 重构底层明文配置缓存
	// ====================
	newSysConfigMap := make(map[string]string)
	for _, sc := range sysConfigs {
		// Decrypt values as they come out of the DB
		decrypted, err := utils.Decrypt(sc.Value)
		if err != nil {
			log.Printf("[CACHE] WARN decrypt SysConfig key=%q failed: %v (fallback 默认值)", sc.Key, err)
			continue
		}
		newSysConfigMap[sc.Key] = decrypted
	}
	SysConfigMutex.Lock()
	SysConfigCache = newSysConfigMap
	SysConfigMutex.Unlock()

	log.Printf("🚀 内存缓存加载完成: 同步了 %d 个活跃渠道, %d 个模型路由配置, %d 个授权用户凭证", len(channels), len(channelModels), len(users))
}
