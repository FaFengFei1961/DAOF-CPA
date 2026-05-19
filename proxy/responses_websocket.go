// Package proxy — responses_websocket.go
//
// 实现 Codex Responses WebSocket v2 反向代理。客户端（Codex CLI / 桌面端 /
// 任何 OpenAI-Responses SDK）通过 `GET /v1/responses` 或
// `GET /backend-api/codex/responses` 以 WebSocket Upgrade 方式拨号 DAOF，
// DAOF 在握手阶段完成鉴权 + 渠道选择，然后透明地把整个连接桥接到上游 CPA 的同
// 名 WebSocket 端点。
//
// 分发架构定位（参见 memory/public_beta_no_backcompat.md / project_snapshot.md）：
//   DAOF-CPA = 前台分发层（鉴权 / 计费 / 限流 / 审计）
//   CLIProxyAPI = 后台协议层（Codex 会话状态 / tool 缓存 / transcript 重放）
// 我们不在 DAOF 复现 CPA 的协议状态机；我们只做帧透传 + 嗅探计费。
//
// 计费路径：
//   - 握手期：检查用户处于活跃订阅或余额 > 0，否则 403。
//   - 单连接一个上游 Channel pinning（首个允许 EndpointResponsesWebsocket 的健康渠道）。
//   - 每收到一个 upstream→client 的 `response.completed` 事件 → 抽 usage →
//     按 (modelName, pinned channel) 的 ChannelModel 价格表算成本 → 走与
//     ChatCompletion 同一套订阅/余额 fallback 提交（commitResponsesWebsocketTurn）。
//   - usage 缺失 / 上游零计费 → pending_reconcile 审计行。
//   - 连接异常断开后还未抽到任何 usage 的"已交付未结算" → pending_reconcile。
//
// 默认 disabled：admin 在 ChannelModel.AllowedEndpoints 中加 "/v1/responses/ws"
// 才允许该模型经 WS 提供服务（与图像/视频/Gemini native 同一开关策略）。
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"daof-cpa/database"

	fiberws "github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
	gorillaws "github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
)

const (
	wsResponsesPath        = "/v1/responses"
	wsCodexResponsesPath   = "/backend-api/codex/responses"
	wsUpstreamDialTimeout  = 30 * time.Second
	wsWriteDeadline        = 30 * time.Second
	wsClientReadLimit      = 16 * 1024 * 1024 // 16MB / frame；与 CPA 上游一致
	wsCompletedEventType   = "response.completed"
	wsResponseCreateType   = "response.create"
	// SEC-FIX-M3：WS session 硬上限，防止 idle 连接长期占用 goroutine + 上游 socket
	wsMaxSessionDuration = 1 * time.Hour
	// SEC-FIX-M3：客户端 read idle 上限——5 分钟没收到任何帧（心跳 pong / 业务帧）→ 主动关
	wsReadIdleTimeout = 5 * time.Minute
	// SEC-FIX-H2：单 WS 连接每分钟最多接受这么多客户端帧（response.create / append 等）
	// 60/min ≈ 1/sec，远超合理 Codex CLI / 桌面端使用；超过 → 主动关连接
	wsClientFramesPerMinute = 60
)

// SEC-FIX-H1：上游 WS 拨号必须走 safeDialContext，防 channel.BaseURL 经 DNS rebinding
// 解析到 169.254.169.254（云元数据服务）→ 凭证窃取。gorilla/websocket Dialer 通过
// NetDialContext 字段允许注入自定义 dialer；这里复用 url_safety.go 同一份防御逻辑。
var wsUpstreamDialer = &gorillaws.Dialer{
	NetDialContext: safeDialContext,
	HandshakeTimeout: wsUpstreamDialTimeout,
	ReadBufferSize:   4096,
	WriteBufferSize:  4096,
}

// Locals keys to pass pre-upgrade state into the upgraded handler.
const (
	wsLocalsUser         = "ws_user"
	wsLocalsToken        = "ws_token"
	wsLocalsSubToken     = "ws_sub_token"
	wsLocalsIsSubToken   = "ws_is_sub_token"
	wsLocalsSelectedChan = "ws_selected_chan"
	wsLocalsPath         = "ws_path"
	wsLocalsClientIP     = "ws_client_ip"
	wsLocalsStartTime    = "ws_start_time"
	wsLocalsAuthHeader   = "ws_auth_header"
)

// responsesWebsocketSelection 是握手阶段选定的上游通道快照。
// 不存 RouteCache 引用——per-turn 计费时按 (model, pinned channel) 重新查表，
// 保证 admin 在长连期间热更新价格 / 禁用模型时能立即生效。
type responsesWebsocketSelection struct {
	Channel *database.Channel
}

// ResponsesWebsocketProxyHandler 是 `GET /v1/responses` 与
// `GET /backend-api/codex/responses` 的 fiber 入口。完成预升级鉴权 + 渠道选择，
// 再升级为 WebSocket 桥接。
func ResponsesWebsocketProxyHandler(c *fiber.Ctx) error {
	startTime := time.Now()
	clientIP := c.IP()
	path := strings.Clone(c.Path())

	if !fiberws.IsWebSocketUpgrade(c) {
		return c.Status(http.StatusUpgradeRequired).JSON(fiber.Map{"error": fiber.Map{
			"message":      "WebSocket upgrade required for " + path,
			"type":         "upgrade_required",
			"message_code": "ERR_RESPONSES_WEBSOCKET_REQUIRED",
		}})
	}

	// 1. 鉴权（与 ChatCompletionProxyHandler 同一套）
	authHeader := string([]byte(c.Get("Authorization")))
	token := ""
	if strings.HasPrefix(strings.TrimSpace(authHeader), "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authHeader), "Bearer "))
	}

	authSnapshotMutex.RLock()
	user, exists := AuthCache[token]
	subToken, isSubToken := AuthTokenCache[token]
	authSnapshotMutex.RUnlock()

	if !exists {
		if shouldRecordInvalidAuthApiLog(clientIP) {
			recordProxyApiLog(0, token, "unknown", 401, clientIP, startTime, path, "auth_error", "Invalid API Key")
		}
		return c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "Invalid API Key", "type": "auth_error"}})
	}
	if user.Status != 1 {
		authSnapshotMutex.Lock()
		delete(AuthCache, token)
		authSnapshotMutex.Unlock()
		recordProxyApiLog(user.ID, token, "unknown", 403, clientIP, startTime, path, "auth_error", "Account suspended")
		return c.Status(403).JSON(fiber.Map{"error": fiber.Map{"message": "Account suspended", "type": "auth_error"}})
	}
	if isSubToken {
		if subToken.Status != 1 {
			recordProxyApiLog(user.ID, token, "unknown", 401, clientIP, startTime, path, "auth_error", "API Key is disabled or frozen")
			return c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "API Key is disabled or frozen", "type": "auth_error"}})
		}
		if subToken.ExpiredAt != nil && time.Now().After(*subToken.ExpiredAt) {
			recordProxyApiLog(user.ID, token, "unknown", 401, clientIP, startTime, path, "auth_error", "API Key has expired")
			return c.Status(401).JSON(fiber.Map{"error": fiber.Map{"message": "API Key has expired", "type": "auth_error"}})
		}
		if subToken.QuotaLimit > 0 && subToken.UsedQuota >= subToken.QuotaLimit {
			recordProxyApiLog(user.ID, token, "unknown", 403, clientIP, startTime, path, "quota_exceeded", "API Key has reached its quota limit")
			return c.Status(403).JSON(fiber.Map{"error": fiber.Map{"message": "API Key has reached its quota limit", "type": "quota_exceeded"}})
		}
	}

	// 2. 握手期最低额度检查：用户必须能至少支付 1 turn 的悲观成本估算。
	// WS 是长连，无法在握手阶段做 per-turn precheck（model 在每个 response.create 里）。
	// 这里只验证"有任意可用付费来源"——订阅在窗口内 OR 余额 > 0。
	if !userHasAnyPayCapacity(user) {
		recordProxyApiLog(user.ID, token, "unknown", 402, clientIP, startTime, path, "subscription_required", "no active subscription or balance")
		return c.Status(402).JSON(fiber.Map{"error": fiber.Map{
			"message":      "您的订阅额度已用尽且余额为零，请购买套餐或充值后再使用 WebSocket 通道",
			"type":         "subscription_required",
			"message_code": "ERR_QUOTA_EXHAUSTED_NO_BALANCE",
		}})
	}

	// 3. 选择支持 WS 的健康通道（任意一条 ChannelModel.AllowedEndpoints 含
	// EndpointResponsesWebsocket 且 channel 健康）。
	selection, err := selectResponsesWebsocketChannel()
	if err != nil {
		recordProxyApiLog(user.ID, token, "unknown", 503, clientIP, startTime, path, "no_websocket_channel", err.Error())
		return c.Status(503).JSON(fiber.Map{"error": fiber.Map{
			"message":      "暂无可用 WebSocket 通道，请联系管理员启用 /v1/responses/ws 端点",
			"type":         "service_unavailable",
			"message_code": "ERR_RESPONSES_WEBSOCKET_NO_CHANNEL",
		}})
	}

	// 4. 把握手期状态写到 Locals，供升级后的处理函数读取
	c.Locals(wsLocalsUser, user)
	c.Locals(wsLocalsToken, token)
	c.Locals(wsLocalsSubToken, subToken)
	c.Locals(wsLocalsIsSubToken, isSubToken)
	c.Locals(wsLocalsSelectedChan, selection)
	c.Locals(wsLocalsPath, path)
	c.Locals(wsLocalsClientIP, clientIP)
	c.Locals(wsLocalsStartTime, startTime)
	c.Locals(wsLocalsAuthHeader, authHeader)

	return fiberws.New(runResponsesWebsocketBridge, fiberws.Config{
		EnableCompression: false,
		ReadBufferSize:    4096,
		WriteBufferSize:   4096,
		// CheckOrigin 默认接受所有来源——与 CPA 上游策略一致；CSRF 风险靠 token
		// 鉴权拦截，origin 不携带认证语义。
	})(c)
}

// userHasAnyPayCapacity 判断用户是否处于"至少能扣一次费"的状态。
// 订阅缓存命中且窗口内未限额：true；余额 > 0：true。
func userHasAnyPayCapacity(user *database.User) bool {
	if user == nil {
		return false
	}
	if user.Quota > 0 {
		return true
	}
	// 走一次 IsPrecheck=true 的极小成本试算（1 token）——能 commit 即代表订阅有空间
	probe := Decide(EngineRequest{
		UserID:       user.ID,
		ModelName:    "websocket-handshake-probe",
		InputTokens:  1,
		OutputTokens: 1,
		CostMicroUSD: 1, // 极小占位
		IsPrecheck:   true,
	})
	return probe.Allowed && !probe.FallbackToBalance
}

// selectResponsesWebsocketChannel 选一个支持 WS 的健康渠道。
// 算法：
//  1. 扫 RouteCache，收集 AllowedEndpoints 含 EndpointResponsesWebsocket 的
//     ChannelModel，按 channel 去重；
//  2. 过滤 channel.Type ∈ {cliproxy, codex}（OpenAI api.openai.com 不暴露 WS）；
//  3. 过滤 channel 健康（非 circuit_open / 非 rate_limited）；
//  4. 按 ChannelModel.Weight 加权随机挑一条。
func selectResponsesWebsocketChannel() (*responsesWebsocketSelection, error) {
	gatewayMutex.RLock()
	type candidate struct {
		channel *database.Channel
		weight  int
	}
	seen := make(map[uint]struct{}, 8)
	candidates := make([]candidate, 0, 8)
	for _, routes := range RouteCache {
		for _, route := range routes {
			if route == nil {
				continue
			}
			if !ChannelModelAllowsResponsesWebsocket(route) {
				continue
			}
			ch, ok := ChannelMapCache[route.ChannelID]
			if !ok || ch == nil {
				continue
			}
			channelType := NormalizeChannelType(ch.Type)
			if channelType != ChannelTypeCLIProxy && channelType != ChannelTypeCodex {
				continue
			}
			if _, dup := seen[ch.ID]; dup {
				continue
			}
			seen[ch.ID] = struct{}{}
			weight := route.Weight
			if weight <= 0 {
				weight = 1
			}
			candidates = append(candidates, candidate{channel: ch, weight: weight})
		}
	}
	gatewayMutex.RUnlock()

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no healthy channel allows %s on any model", database.EndpointResponsesWebsocket)
	}

	// 过滤 unhealthy（在锁外做，避免持锁触发 DB IO）
	healthy := make([]candidate, 0, len(candidates))
	totalWeight := 0
	for _, cand := range candidates {
		if IsChannelRateLimited(cand.channel.ID) {
			continue
		}
		if IsChannelCircuitOpen(cand.channel.ID) {
			continue
		}
		healthy = append(healthy, cand)
		totalWeight += cand.weight
	}
	if len(healthy) == 0 {
		return nil, fmt.Errorf("all websocket-capable channels are unhealthy")
	}

	// 加权随机
	pick := healthy[0].channel
	if totalWeight > 0 {
		r := wsRandIntN(totalWeight)
		acc := 0
		for _, cand := range healthy {
			acc += cand.weight
			if r < acc {
				pick = cand.channel
				break
			}
		}
	}
	return &responsesWebsocketSelection{Channel: pick}, nil
}

// ChannelModelAllowsResponsesWebsocket 判断 ChannelModel 是否允许 WebSocket 端点。
// Exported 供 admin 配置校验复用。
func ChannelModelAllowsResponsesWebsocket(cm *database.ChannelModel) bool {
	if cm == nil || cm.Status != 1 {
		return false
	}
	for _, ep := range database.ChannelModelAllowedEndpointsList(cm) {
		if ep == database.EndpointResponsesWebsocket {
			return true
		}
	}
	return false
}

// wsRandIntN 包一层 math/rand/v2，让测试可以 mock。
var wsRandIntN = defaultWSRandIntN

func defaultWSRandIntN(n int) int {
	if n <= 0 {
		return 0
	}
	return mrand.IntN(n)
}

// runResponsesWebsocketBridge 是升级后的桥接处理函数。
//
// 生命周期：
//  1. 从 Locals 恢复握手期选择的 channel + 用户
//  2. 拨号上游 CPA WebSocket（同一 path，带上游 channel key）
//  3. 启动两个 goroutine：client→upstream pump 与 upstream→client pump
//  4. upstream→client pump 嗅探 response.completed 事件，触发 commitResponsesWebsocketTurn
//  5. 任一方向出错 → cancel context，关闭两边
//  6. 收尾：未抽到 usage 的"已交付未结算" 写 pending_reconcile
func runResponsesWebsocketBridge(conn *fiberws.Conn) {
	defer conn.Close()

	user, _ := conn.Locals(wsLocalsUser).(*database.User)
	token, _ := conn.Locals(wsLocalsToken).(string)
	subToken, _ := conn.Locals(wsLocalsSubToken).(*database.AccessToken)
	isSubToken, _ := conn.Locals(wsLocalsIsSubToken).(bool)
	selection, _ := conn.Locals(wsLocalsSelectedChan).(*responsesWebsocketSelection)
	path, _ := conn.Locals(wsLocalsPath).(string)
	clientIP, _ := conn.Locals(wsLocalsClientIP).(string)
	startTime, _ := conn.Locals(wsLocalsStartTime).(time.Time)
	if user == nil || selection == nil || selection.Channel == nil {
		log.Printf("[WS-BRIDGE] missing locals; rejecting connection")
		writeWebsocketErrorAndClose(conn, "internal_error", "bridge initialization failed")
		return
	}
	channel := selection.Channel

	// 1. 拼上游 URL
	upstreamWSURL, err := buildUpstreamWebsocketURL(channel.BaseURL, path)
	if err != nil {
		log.Printf("[WS-BRIDGE] user=%d build upstream url failed: %v", user.ID, err)
		writeWebsocketErrorAndClose(conn, "channel_misconfigured", "upstream URL build failed")
		return
	}
	conn.SetReadLimit(wsClientReadLimit)

	// 2. 拨号上游（SEC-FIX-H1：用 wsUpstreamDialer with safeDialContext）
	dialCtx, dialCancel := context.WithTimeout(context.Background(), wsUpstreamDialTimeout)
	defer dialCancel()
	upstreamHeader := http.Header{}
	upstreamHeader.Set("Authorization", "Bearer "+channel.Key)
	if extraHeaders := parseChannelCustomHeaders(channel.Headers); len(extraHeaders) > 0 {
		for k, v := range extraHeaders {
			upstreamHeader.Set(k, v)
		}
	}

	upstreamConn, upstreamResp, err := wsUpstreamDialer.DialContext(dialCtx, upstreamWSURL, upstreamHeader)
	if err != nil {
		status := http.StatusBadGateway
		body := ""
		if upstreamResp != nil {
			status = upstreamResp.StatusCode
			defer upstreamResp.Body.Close()
		}
		log.Printf("[WS-BRIDGE] user=%d upstream dial failed status=%d url=%s err=%v", user.ID, status, upstreamWSURL, err)
		recordProxyApiLog(user.ID, token, "websocket-handshake", status, clientIP, startTime, path, "upstream_dial_failed", sanitizeError(err.Error(), 240)+body)
		writeWebsocketErrorAndClose(conn, "bad_gateway", "upstream WebSocket unavailable")
		return
	}
	defer upstreamConn.Close()

	// 3. 双向 pump（SEC-FIX-M3：会话硬上限 wsMaxSessionDuration，防长期挂连接）
	pumpCtx, pumpCancel := context.WithTimeout(context.Background(), wsMaxSessionDuration)
	defer pumpCancel()

	state := &websocketBridgeState{
		user:        user,
		token:       token,
		subToken:    subToken,
		isSubToken:  isSubToken,
		channel:     channel,
		path:        path,
		clientIP:    clientIP,
		startTime:   startTime,
		lastModel:   "",
		turnsBilled: 0,
		mu:          sync.Mutex{},
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer wg.Done()
		pumpClientToUpstream(pumpCtx, conn, upstreamConn, state, pumpCancel)
	}()
	go func() {
		defer wg.Done()
		pumpUpstreamToClient(pumpCtx, conn, upstreamConn, state, pumpCancel)
	}()
	wg.Wait()

	// 4. 收尾审计：如果整个会话没抽到任何 usage（即没有 response.completed），
	// 但客户端确实发过 response.create，按 pending_reconcile 兜底审计一行。
	state.mu.Lock()
	hadCreate := state.sawCreate
	billed := state.turnsBilled
	lastModel := state.lastModel
	state.mu.Unlock()
	if hadCreate && billed == 0 && lastModel != "" {
		log.Printf("[WS-BRIDGE-PENDING] user=%d model=%s session ended without any response.completed; recording pending_reconcile", user.ID, lastModel)
		writeWebsocketPendingReconcile(state, lastModel, "session_ended_without_completed")
	}
}

// websocketBridgeState 桥接两个 goroutine 共享的状态。
type websocketBridgeState struct {
	user        *database.User
	token       string
	subToken    *database.AccessToken
	isSubToken  bool
	channel     *database.Channel
	path        string
	clientIP    string
	startTime   time.Time
	lastModel   string
	turnsBilled int
	sawCreate   bool
	mu          sync.Mutex
}

// pumpClientToUpstream 把客户端帧透传到上游，并提取首个 response.create 的 model。
func pumpClientToUpstream(ctx context.Context, client *fiberws.Conn, upstream *gorillaws.Conn, state *websocketBridgeState, cancel context.CancelFunc) {
	defer cancel()
	// SEC-FIX-H2: 简单 sliding-window 帧限流——超过 wsClientFramesPerMinute 的客户端直接断开。
	// 用环形数组记最近 N 帧时间戳；新帧来时把超过 1min 的踢掉，再判余量。
	frameTimestamps := make([]time.Time, 0, wsClientFramesPerMinute+1)
	for {
		if ctx.Err() != nil {
			return
		}
		// SEC-FIX-M3: 客户端 read idle 上限——5min 无任何帧 → 触发 timeout 关连接
		_ = client.SetReadDeadline(time.Now().Add(wsReadIdleTimeout))
		msgType, payload, err := client.ReadMessage()
		if err != nil {
			if !gorillaws.IsCloseError(err, gorillaws.CloseNormalClosure, gorillaws.CloseGoingAway, gorillaws.CloseNoStatusReceived) {
				log.Printf("[WS-CLIENT-IN] user=%d read err=%v", state.user.ID, err)
			}
			return
		}
		if msgType != gorillaws.TextMessage && msgType != gorillaws.BinaryMessage {
			continue
		}

		// SEC-FIX-H2: per-frame 限流（HTTP limiter 只在升级时计数，升级后帧速率无防御）
		now := time.Now()
		cutoff := now.Add(-1 * time.Minute)
		// 踢掉过期 timestamps
		trimmed := frameTimestamps[:0]
		for _, t := range frameTimestamps {
			if t.After(cutoff) {
				trimmed = append(trimmed, t)
			}
		}
		frameTimestamps = trimmed
		if len(frameTimestamps) >= wsClientFramesPerMinute {
			log.Printf("[WS-CLIENT-IN-RATELIMIT] user=%d exceeded %d frames/min — closing", state.user.ID, wsClientFramesPerMinute)
			// 礼貌通知客户端再关
			frame, _ := json.Marshal(fiber.Map{"type": "error", "error": fiber.Map{"type": "rate_limit_exceeded", "message": fmt.Sprintf("frame rate exceeded %d/min", wsClientFramesPerMinute)}})
			_ = client.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
			_ = client.WriteMessage(gorillaws.TextMessage, frame)
			return
		}
		frameTimestamps = append(frameTimestamps, now)

		// 嗅探 response.create 的 model 字段（仅用于审计 + pending_reconcile 兜底）
		eventType := gjson.GetBytes(payload, "type").String()
		if eventType == wsResponseCreateType {
			model := strings.TrimSpace(gjson.GetBytes(payload, "model").String())
			state.mu.Lock()
			state.sawCreate = true
			if model != "" {
				state.lastModel = model
			}
			state.mu.Unlock()
		}

		_ = upstream.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
		if err := upstream.WriteMessage(msgType, payload); err != nil {
			log.Printf("[WS-CLIENT-IN] user=%d upstream write err=%v", state.user.ID, err)
			return
		}
	}
}

// pumpUpstreamToClient 把上游帧透传给客户端，并嗅探 response.completed 触发计费。
func pumpUpstreamToClient(ctx context.Context, client *fiberws.Conn, upstream *gorillaws.Conn, state *websocketBridgeState, cancel context.CancelFunc) {
	defer cancel()
	for {
		if ctx.Err() != nil {
			return
		}
		msgType, payload, err := upstream.ReadMessage()
		if err != nil {
			if !gorillaws.IsCloseError(err, gorillaws.CloseNormalClosure, gorillaws.CloseGoingAway, gorillaws.CloseNoStatusReceived) {
				log.Printf("[WS-UPSTREAM-IN] user=%d read err=%v", state.user.ID, err)
			}
			return
		}
		if msgType != gorillaws.TextMessage && msgType != gorillaws.BinaryMessage {
			continue
		}

		_ = client.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
		if err := client.WriteMessage(msgType, payload); err != nil {
			log.Printf("[WS-UPSTREAM-IN] user=%d client write err=%v", state.user.ID, err)
			return
		}

		// 嗅探 response.completed
		eventType := gjson.GetBytes(payload, "type").String()
		if eventType == wsCompletedEventType {
			// fix M1 (2026-05-19)：upstream 的 response.model 是计费唯一可信源。
			// 之前 fallback 到 state.lastModel（来自客户端 response.create 帧），允许
			// 恶意客户端用 "model:gpt-4o" 触发但 upstream 跑 "claude-opus-4-5"，按 gpt-4o
			// 廉价档计费。现保留 fallback 但每次走 fallback 都 log 警告便于运维监控。
			model := strings.TrimSpace(gjson.GetBytes(payload, "response.model").String())
			if model == "" {
				state.mu.Lock()
				model = state.lastModel
				state.mu.Unlock()
				if model != "" {
					log.Printf("[WS-MODEL-FALLBACK] user=%d using client-supplied model=%q (upstream omitted response.model) — verify CPA is forwarding model field", state.user.ID, model)
				}
			}
			usageBlock := gjson.GetBytes(payload, "response.usage")
			if !usageBlock.Exists() {
				usageBlock = gjson.GetBytes(payload, "usage")
			}
			if !usageBlock.Exists() || strings.TrimSpace(model) == "" {
				log.Printf("[WS-COMPLETED-NO-USAGE] user=%d model=%q payload=%s", state.user.ID, model, websocketTrimPayloadForLog(payload))
				writeWebsocketPendingReconcile(state, model, "upstream_completed_without_usage")
				state.mu.Lock()
				state.turnsBilled++
				if model != "" {
					state.lastModel = model
				}
				state.mu.Unlock()
				continue
			}
			usage := extractUsageTokenCounts(usageBlock)
			commitResponsesWebsocketTurn(state, model, usage, payload)
			state.mu.Lock()
			state.turnsBilled++
			state.lastModel = model
			state.mu.Unlock()
		}
	}
}

// websocketTrimPayloadForLog 截断 payload 用于日志（不带敏感数据，仅用于排查）。
func websocketTrimPayloadForLog(payload []byte) string {
	const maxLen = 240
	if len(payload) <= maxLen {
		return string(payload)
	}
	return string(payload[:maxLen]) + "...(truncated)"
}




// commitResponsesWebsocketTurn 为单个 response.completed 事件做计费。
// P8.6 后变薄：找 pinned channel 下该 model 的 ChannelModel 路由 → 组装
// CommitTextContext → 委托 proxy/text_billing.go CommitTextTurn。
//
// 路由查找失败（admin 改 ChannelModel 配置导致 model 不在该 channel）→ 写
// pending_reconcile 兜底；不进入 commit pipeline（避免拿错价格扣费）。
//
// 注意：WS 路径没有 precheck（precheck 在 chat handler 入口；WS 接收每帧都不重做），
// 所以 CommitTextContext.EngineDecision 传零值；
// CommitTextContext.IsStream=true 让 cost 算不出的 ReConcile 走 pending 而非 502。
// CommitTextContext.UpstreamHeaders=nil（WS 无 HTTP 响应头）。
func commitResponsesWebsocketTurn(state *websocketBridgeState, modelName string, usage usageTokenCounts, payload []byte) {
	if modelName == "" {
		modelName = "unknown"
	}
	gatewayMutex.RLock()
	var selectedPath *database.ChannelModel
	for _, r := range RouteCache[modelName] {
		if r == nil || r.ChannelID != state.channel.ID {
			continue
		}
		selectedPath = r
		break
	}
	gatewayMutex.RUnlock()
	if selectedPath == nil {
		log.Printf("[WS-COMMIT-NO-ROUTE] user=%d model=%s channel=%d — usage received but no route registered; pending_reconcile", state.user.ID, modelName, state.channel.ID)
		writeWebsocketPendingReconcile(state, modelName, "no_pricing_route_for_model")
		return
	}
	// fix H1 (2026-05-19)：WS 长连里 state.user 是 handshake 时的 snapshot，admin
	// 中途改余额、或前一个 turn 扣费后 quota 不会同步。每个 turn commit 前重读
	// 一次 quota，避免 BillingEntry.BalanceAfterUSD 写错。失败 fallback 用 stale。
	refreshUserQuotaFromDB(state.user)
	ctx := CommitTextContext{
		User:              state.user,
		Token:             state.token,
		SubToken:          state.subToken,
		IsSubToken:        state.isSubToken,
		ModelName:         modelName,
		Body:              payload,
		Path:              state.path,
		ClientIP:          state.clientIP,
		StartTime:         state.startTime,
		IsStream:          true,
		FallbackUserOptIn: false,
		SelectedPath:      selectedPath,
		SelectedChan:      state.channel,
		EngineDecision:    EngineDecision{},
		UpstreamHeaders:   nil,
	}
	CommitTextTurn(ctx, usage, 200, 0, "", "")
}

// writeWebsocketPendingReconcile 写一条审计 ApiLog + pending_reconcile 账单。
// 用于 upstream 没返 usage / cost 算不出 / commit 失败等"已交付但无法常规计费"的情况。
func writeWebsocketPendingReconcile(state *websocketBridgeState, modelName, reasonTag string) {
	if modelName == "" {
		modelName = "unknown"
	}
	apiLog := database.ApiLog{
		UserID:       state.user.ID,
		TokenName:    HashTokenForLog(state.token),
		ModelName:    modelName,
		Status:       200,
		IPAddress:    state.clientIP,
		RequestPath:  sanitizeError(state.path, 160),
		ErrorType:    "websocket_unmetered",
		ErrorMessage: sanitizeError(reasonTag, 240),
		Latency:      time.Since(state.startTime).Milliseconds(),
		CreatedAt:    time.Now(),
	}
	apiLogPersisted := database.DB.Create(&apiLog).Error == nil
	relatedID := uint(0)
	relatedType := ""
	if apiLogPersisted {
		relatedID = apiLog.ID
		relatedType = "api_log"
	}
	// fix H1：写 pending_reconcile 前重读 quota，避免 BalanceAfterUSD 是 handshake stale
	refreshUserQuotaFromDB(state.user)
	_ = database.WriteBillingEntryNonFatal(database.BillingEntryInput{
		UserID:          state.user.ID,
		EntryType:       database.BillingTypeApiUsagePendingReconcile,
		BillingState:    database.BillingStatePendingReconcile,
		AmountUSD:       0,
		BalanceAfterUSD: state.user.Quota,
		ModelName:       modelName,
		RelatedType:     relatedType,
		RelatedID:       relatedID,
		Description:     fmt.Sprintf("[%s] WS %s · 待对账（无 usage）", reasonTag, modelName),
	})
}

// refreshUserQuotaFromDB 重读 user.Quota 字段（其余字段不变），用于 WS 长连
// 在每个 turn commit 前刷新 BalanceAfterUSD 写入值。失败保留 stale 值不致 panic。
func refreshUserQuotaFromDB(user *database.User) {
	if user == nil || user.ID == 0 {
		return
	}
	var fresh database.User
	if err := database.DB.Select("id, quota").First(&fresh, user.ID).Error; err != nil {
		log.Printf("[WS-USER-REFRESH-FAIL] user=%d: %v (keep stale snapshot)", user.ID, err)
		return
	}
	user.Quota = fresh.Quota
}


// buildUpstreamWebsocketURL 把 http(s):// channel base url 转为 ws(s)://，
// 并追加 client 实际请求的 path（保留 `/v1/responses` 与 `/backend-api/codex/responses` 两种）。
func buildUpstreamWebsocketURL(baseURL, requestPath string) (string, error) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		return "", fmt.Errorf("empty channel base URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// already correct
	default:
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	// 拼路径
	clean := strings.TrimSpace(requestPath)
	if clean == "" {
		clean = wsResponsesPath
	}
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	u.Path = strings.TrimRight(u.Path, "/") + clean
	return u.String(), nil
}

// parseChannelCustomHeaders 解析 channel.Headers 的 JSON 字符串为 map。
// 与 stream.go 同口径——错误日志已由 chat 路径覆盖，这里静默返回 nil。
func parseChannelCustomHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// writeWebsocketErrorAndClose 写一条 error 帧并关闭。
// 用于升级后但拨号失败 / 初始化错误。客户端会看到 type=error 的 JSON 帧。
func writeWebsocketErrorAndClose(conn *fiberws.Conn, errType, message string) {
	frame := fiber.Map{
		"type": "error",
		"error": fiber.Map{
			"type":    errType,
			"message": message,
		},
	}
	body, _ := json.Marshal(frame)
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline))
	_ = conn.WriteMessage(gorillaws.TextMessage, body)
	_ = conn.WriteMessage(gorillaws.CloseMessage, gorillaws.FormatCloseMessage(gorillaws.CloseInternalServerErr, message))
}
