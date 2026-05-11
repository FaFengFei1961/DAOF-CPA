package proxy

import (
	"log"
	"sync"

	"daof-ai-hub/database"
	"daof-ai-hub/utils"
)

var (
	// ChannelMapCache key: channel_id, value: Channel 指针 (提供URL和Key)
	ChannelMapCache map[uint]*database.Channel
	channelMutex    sync.RWMutex

	// RouteCache key: model_name, value: 支持此模型的所有渠道高维计价与权重集合
	RouteCache map[string][]*database.ChannelModel
	routeMutex sync.RWMutex

	// AuthCache key: token, value: User 指针，用于真正的秒级鉴权
	AuthCache map[string]*database.User
	authMutex sync.RWMutex

	// AuthTokenCache key: token key, value: AccessToken 指针，用于拦截子凭证超期和限额
	AuthTokenCache map[string]*database.AccessToken
	authTokenMutex sync.RWMutex

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
	authMutex.RLock()
	defer authMutex.RUnlock()
	return AuthCache[token]
}

// AddUserToAuthCache 增量加新用户到 AuthCache（注册路径用，避免全量 SyncCacheConfig 的 N+1）
func AddUserToAuthCache(user *database.User) {
	if user == nil || user.Token == "" {
		return
	}
	authMutex.Lock()
	AuthCache[user.Token] = user
	authMutex.Unlock()
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
	authMutex.Lock()
	delete(AuthCache, token)
	authMutex.Unlock()
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

	// fix CRITICAL（codex 第六轮）：原实现只刷新主 token 在 AuthCache 的指针，
	// 但 SyncCacheConfig 也把"子 token key → 父 user 指针"放进了 AuthCache（line ~169）。
	// 余额扣到负数后，子 token 仍指向**旧的** user 对象（含旧 Quota），
	// 子 token API 调用的 precheck 看到的是陈旧 Quota，可继续消费直到下次全量 SyncCacheConfig。
	// 必须把同一用户的所有子 token 在 AuthCache 里的映射也同步指到新 user 对象。
	//
	// fix Major（codex 第六轮）：原 evict 分支在 authTokenMutex 下做 delete(AuthCache,...)
	// 而未持 authMutex，并发读 AuthCache 会触发 map race / panic。
	// 改为：先在 authTokenMutex 下收集要操作的 sub keys，再在 authMutex 下做实际写。
	//
	// 同时，被封禁用户的旧 token 不能被刷新回 AuthCache。

	// fix CRITICAL（codex 第七轮）：原实现两阶段——先 RLock 收集 subKeys，再 Lock 写回。
	// 中间窗口里 DeleteToken/SyncCacheConfig 可能并发删除某个子 token，
	// 导致我们把"已被删除的 key"重新写回 AuthCache，让被禁/被删的子 token 被当主 token 放行。
	//
	// 修复：把 AuthTokenCache 的读 + AuthCache 的写放在同一个原子区——
	// 持有 authTokenMutex.RLock 收集 keys 之后立刻 Lock authMutex 写入，
	// 期间任何 SyncCacheConfig 写者必须等待。
	// 锁顺序统一为：authTokenMutex 先于 authMutex（与 SyncCacheConfig 一致）。

	if user.Status != 1 {
		// 封禁/异常：在 authTokenMutex Lock 内同时清 AuthCache 主+子 token
		authTokenMutex.Lock()
		subKeys := make([]string, 0, 4)
		for k, t := range AuthTokenCache {
			if t.UserID == userID {
				subKeys = append(subKeys, k)
				delete(AuthTokenCache, k)
			}
		}
		authMutex.Lock()
		delete(AuthCache, user.Token)
		for _, k := range subKeys {
			delete(AuthCache, k)
		}
		authMutex.Unlock()
		authTokenMutex.Unlock()
		log.Printf("[AUTH-REFRESH] user=%d status=%d → evicted main + %d sub tokens", userID, user.Status, len(subKeys))
		return
	}

	// 正常路径：在 authTokenMutex.RLock 持有期间收集 + 写 AuthCache，
	// 防止收集后并发删除导致写回死键。
	authTokenMutex.RLock()
	subKeys := make([]string, 0, 4)
	for k, t := range AuthTokenCache {
		if t.UserID == userID {
			subKeys = append(subKeys, k)
		}
	}
	authMutex.Lock()
	AuthCache[user.Token] = &user
	for _, k := range subKeys {
		// 双保险：写前再校验 AuthTokenCache 仍含该 key（虽然 RLock 内不会被删，但显式更安全）
		if _, ok := AuthTokenCache[k]; ok {
			AuthCache[k] = &user
		}
	}
	authMutex.Unlock()
	authTokenMutex.RUnlock()
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
	// 1. 缓存信道源点 (BaseURL + Key)
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
	channelMutex.Lock()
	ChannelMapCache = newChannelMap
	channelMutex.Unlock()

	// ====================
	// 2. 路由矩阵及各自计分阶梯 (model_name -> []ChannelModel)
	// ====================
	newRouteMap := make(map[string][]*database.ChannelModel)
	for i := range channelModels {
		chm := &channelModels[i]
		// 只有该信道存活时，这个模型挂载才有意义
		if _, exists := newChannelMap[chm.ChannelID]; exists {
			newRouteMap[chm.ModelID] = append(newRouteMap[chm.ModelID], chm)
		}
	}
	routeMutex.Lock()
	RouteCache = newRouteMap
	routeMutex.Unlock()

	// ====================
	// 3. User 鉴权秒通凭证 (聚合 Web Root Token 与全链路 子 Tokens)
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

	authMutex.Lock()
	AuthCache = newAuthMap
	authMutex.Unlock()

	authTokenMutex.Lock()
	AuthTokenCache = newAuthTokenMap
	authTokenMutex.Unlock()

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
