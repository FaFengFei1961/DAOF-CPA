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
	"gorm.io/gorm"
)

const (
	wsResponsesPath        = "/v1/responses"
	wsCodexResponsesPath   = "/backend-api/codex/responses"
	wsUpstreamDialTimeout  = 30 * time.Second
	wsWriteDeadline        = 30 * time.Second
	wsClientReadLimit      = 16 * 1024 * 1024 // 16MB / frame；与 CPA 上游一致
	wsCompletedEventType   = "response.completed"
	wsResponseCreateType   = "response.create"
)

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

	// 2. 拨号上游
	dialCtx, dialCancel := context.WithTimeout(context.Background(), wsUpstreamDialTimeout)
	defer dialCancel()
	upstreamHeader := http.Header{}
	upstreamHeader.Set("Authorization", "Bearer "+channel.Key)
	if extraHeaders := parseChannelCustomHeaders(channel.Headers); len(extraHeaders) > 0 {
		for k, v := range extraHeaders {
			upstreamHeader.Set(k, v)
		}
	}

	upstreamConn, upstreamResp, err := gorillaws.DefaultDialer.DialContext(dialCtx, upstreamWSURL, upstreamHeader)
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

	// 3. 双向 pump
	pumpCtx, pumpCancel := context.WithCancel(context.Background())
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
	for {
		if ctx.Err() != nil {
			return
		}
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
			model := strings.TrimSpace(gjson.GetBytes(payload, "response.model").String())
			if model == "" {
				state.mu.Lock()
				model = state.lastModel
				state.mu.Unlock()
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

// commitResponsesWebsocketTurn 完成单个 response.completed 事件的计费。
// 与 ChatCompletionProxyHandler.deductQuota 同步设计原则：
//   - 失败请求（status<200 || >=400）不扣费
//   - cost 用 selectedPath 价格表 × usage 算
//   - 订阅命中扣额度（不动 quota），否则 fallback 余额 CAS 扣
//   - 全程写 ApiLog + BillingEntry + RevenueAttribution
//   - 任何写失败 → pending_reconcile 兜底审计
func commitResponsesWebsocketTurn(state *websocketBridgeState, modelName string, usage usageTokenCounts, payload []byte) {
	if modelName == "" {
		modelName = "unknown"
	}

	// 1. 找当前 pinned channel 下该 model 的价格表
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

	promptTokens := usage.PromptTokens
	completionTokens := usage.CompletionTokens
	cachedTokens := usage.CachedTokens
	cacheWriteTokens := usage.CacheWriteTokens
	cacheWrite5mTokens := usage.CacheWrite5mTokens
	cacheWrite1hTokens := usage.CacheWrite1hTokens
	reasoningTokens := usage.ReasoningTokens

	// 2. clamp / 一致性（与 deductQuota 同一组防御）
	if promptTokens < 0 {
		promptTokens = 0
	}
	if completionTokens < 0 {
		completionTokens = 0
	}
	if cachedTokens < 0 {
		cachedTokens = 0
	}
	if cacheWrite5mTokens < 0 {
		cacheWrite5mTokens = 0
	}
	if cacheWrite1hTokens < 0 {
		cacheWrite1hTokens = 0
	}
	if reasoningTokens < 0 {
		reasoningTokens = 0
	}
	cacheWriteTokens = cacheWrite5mTokens + cacheWrite1hTokens
	if cachedTokens > promptTokens {
		cachedTokens = promptTokens
	}
	if cachedTokens+cacheWriteTokens > promptTokens {
		cacheWriteTokens = promptTokens - cachedTokens
		if cacheWriteTokens < 0 {
			cacheWriteTokens = 0
		}
	}
	if reasoningTokens > completionTokens {
		reasoningTokens = completionTokens
	}

	// 3. 计算 raw cost
	inputPricePico := selectedPath.InputPricePicoPerToken
	outputPricePico := selectedPath.OutputPricePicoPerToken
	cachedInputPricePico := selectedPath.CachedInputPricePicoPerToken
	if selectedPath.ContextPriceThreshold > 0 && promptTokens >= selectedPath.ContextPriceThreshold {
		if selectedPath.HighInputPricePicoPerToken > 0 {
			inputPricePico = selectedPath.HighInputPricePicoPerToken
		}
		if selectedPath.HighCachedInputPricePicoPerToken > 0 {
			cachedInputPricePico = selectedPath.HighCachedInputPricePicoPerToken
		}
		if selectedPath.HighOutputPricePicoPerToken > 0 {
			outputPricePico = selectedPath.HighOutputPricePicoPerToken
		}
	}
	cacheWriteInputPricePico := selectedPath.CacheWriteInputPricePicoPerToken
	if cacheWriteInputPricePico <= 0 {
		cacheWriteInputPricePico = inputPricePico
	}
	cacheWrite1hInputPricePico := selectedPath.CacheWrite1hInputPricePicoPerToken
	if cacheWrite1hInputPricePico <= 0 {
		cacheWrite1hInputPricePico = inputPricePico * 2
	}
	nonReasoningCompletion := completionTokens - reasoningTokens
	if nonReasoningCompletion < 0 {
		nonReasoningCompletion = 0
	}
	standardInputTokens := promptTokens - cachedTokens - cacheWriteTokens
	if standardInputTokens < 0 {
		standardInputTokens = 0
	}
	costMicroUSD, costOK := checkedCostMicroUSD(
		standardInputTokens, inputPricePico,
		cachedTokens, cachedInputPricePico,
		cacheWrite5mTokens, cacheWriteInputPricePico,
		cacheWrite1hTokens, cacheWrite1hInputPricePico,
		nonReasoningCompletion, outputPricePico,
		reasoningTokens, outputPricePico,
	)
	if !costOK {
		log.Printf("[WS-BILLING-CRITICAL] user=%d model=%s cost overflow/invalid; writing pending_reconcile", state.user.ID, modelName)
		writeWebsocketPendingReconcile(state, modelName, "cost_calc_failed")
		return
	}

	// 4. ResolveBillingRules — modelWeight × healthMultiplier，调用 helper
	channelType := NormalizeChannelType(state.channel.Type)
	billingResolution := ResolveBillingRules(modelName, payload, reasoningTokens, channelType, false).WithCosts(costMicroUSD)
	chargedCostMicroUSD := billingResolution.ChargedCostMicroUSD

	// 5. 写 ApiLog
	apiLog := database.ApiLog{
		UserID:              state.user.ID,
		TokenName:           HashTokenForLog(state.token),
		ModelName:           modelName,
		RequestedModel:      billingResolution.RequestedModel,
		ServedModel:         billingResolution.ServedModel,
		PromptTokens:        promptTokens,
		CompletionTokens:    completionTokens,
		CachedTokens:        cachedTokens,
		CacheWriteTokens:    cacheWriteTokens,
		CacheWrite5mTokens:  cacheWrite5mTokens,
		CacheWrite1hTokens:  cacheWrite1hTokens,
		ReasoningTokens:     reasoningTokens,
		Cost:                costMicroUSD,
		ChargedCost:         chargedCostMicroUSD,
		ModelWeight:         billingResolution.ModelWeight,
		HealthMultiplier:    billingResolution.HealthMultiplier,
		BillingRulesVersion: billingResolution.BillingRulesVersion,
		UpstreamProvider:    sanitizeError(strings.ToLower(channelType), 64),
		Latency:             time.Since(state.startTime).Milliseconds(),
		Status:              200,
		IPAddress:           state.clientIP,
		RequestPath:         sanitizeError(state.path, 160),
		CreatedAt:           time.Now(),
	}
	apiLogPersisted := true
	if err := database.DB.Create(&apiLog).Error; err != nil {
		log.Printf("[WS-BILLING-CRITICAL] user=%d model=%s api_log create failed: %v", state.user.ID, modelName, err)
		apiLogPersisted = false
	}

	// 6. 写 ApiLogUsageLine（token 输入 / 输出）
	if apiLogPersisted && (promptTokens > 0 || completionTokens > 0) {
		writeWebsocketUsageLines(apiLog.ID, modelName, state.path, promptTokens, completionTokens, inputPricePico, outputPricePico)
	}

	// 7. 订阅扣额度 OR 余额扣费 fallback
	commitDecision := Decide(EngineRequest{
		UserID:       state.user.ID,
		ModelName:    modelName,
		InputTokens:  promptTokens,
		OutputTokens: completionTokens,
		CostMicroUSD: chargedCostMicroUSD,
		IsPrecheck:   false,
	})
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	var effectiveRevenueMicroUSD int64

	if commitDecision.NeedsRetry {
		log.Printf("[WS-BILLING-DB-RETRY] user=%d model=%s charged_cost_micro=%d sub-load failed", state.user.ID, modelName, chargedCostMicroUSD)
		writeWebsocketPendingReconcileEntry(state, apiLog.ID, apiLogPersisted, modelName, "DB-RETRY", costMicroUSD, chargedCostMicroUSD, promptTokens, completionTokens)
		return
	}

	if commitOK {
		entryType := database.BillingTypeApiUsageSub
		subID := commitDecision.SubscriptionID
		tokensTotal := promptTokens + completionTokens
		relatedID := uint(0)
		relatedType := ""
		if apiLogPersisted {
			relatedID = apiLog.ID
			relatedType = "api_log"
		}
		if billErr := database.WriteBillingEntryNonFatal(database.BillingEntryInput{
			UserID:               state.user.ID,
			EntryType:            entryType,
			AmountUSD:            0,
			BalanceAfterUSD:      state.user.Quota,
			ModelName:            modelName,
			TokensTotal:          tokensTotal,
			SourceSubscriptionID: &subID,
			RelatedType:          relatedType,
			RelatedID:            relatedID,
			Description:          fmt.Sprintf("套餐 · %s · %d tokens · %s · WS", modelName, tokensTotal, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
		}); billErr != nil {
			log.Printf("[WS-BILLING-AUDIT-FAIL] user=%d sub=%d: %v", state.user.ID, subID, billErr)
		}
		effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
		if apiLogPersisted {
			RecordApiLogRevenue(apiLog.ID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
		}
	} else if chargedCostMicroUSD > 0 {
		if !state.user.BalanceConsumeEnabled {
			log.Printf("[WS-BILLING-PENDING-DEBT] user=%d model=%s charged_cost_micro=%d UNAUTHORIZED-FALLBACK (balance disabled)", state.user.ID, modelName, chargedCostMicroUSD)
			writeWebsocketPendingReconcileEntry(state, apiLog.ID, apiLogPersisted, modelName, "UNAUTHORIZED-FALLBACK", costMicroUSD, chargedCostMicroUSD, promptTokens, completionTokens)
			return
		}
		effectiveRevenueMicroUSD = websocketDeductBalance(state, apiLog.ID, apiLogPersisted, modelName, costMicroUSD, chargedCostMicroUSD, promptTokens, completionTokens)
	}

	// 8. 子 token UsedQuota 累加
	if state.isSubToken && state.subToken != nil && effectiveRevenueMicroUSD > 0 {
		res := database.DB.Model(&database.AccessToken{}).
			Where("id = ?", state.subToken.ID).
			UpdateColumn("used_quota", gorm.Expr("used_quota + ?", effectiveRevenueMicroUSD))
		if res.Error != nil {
			log.Printf("[WS-SUB-TOKEN-CRITICAL] token_id=%d effective_revenue_micro=%d UsedQuota-UPDATE-FAILED: %v", state.subToken.ID, effectiveRevenueMicroUSD, res.Error)
		} else {
			authSnapshotMutex.Lock()
			if existing, ok := AuthTokenCache[state.token]; ok {
				updated := *existing
				updated.UsedQuota += effectiveRevenueMicroUSD
				AuthTokenCache[state.token] = &updated
			}
			authSnapshotMutex.Unlock()
		}
	}
}

// websocketDeductBalance 走原子 CAS 余额扣费，余额不足时写 pending_reconcile。
// 返回实际扣到的 micro_usd（CAS 成功），失败/不足返回 0。
func websocketDeductBalance(state *websocketBridgeState, apiLogID uint, apiLogPersisted bool, modelName string, costMicroUSD, chargedCostMicroUSD int64, promptTokens, completionTokens int) int64 {
	balanceConsumed := false
	var consumed int64
	referralRewardBPS, referralRewardWindowSeconds := readReferralPaidSpendRewardConfig()

	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if !TryConsumeBalanceTx(tx, state.user.ID, costMicroUSD, true) {
			log.Printf("[WS-BILLING-WINDOW-TRACK-FAIL] user=%d model=%s raw_cost_micro=%d forceTrack failed", state.user.ID, modelName, costMicroUSD)
		}
		res := tx.Model(&database.User{}).
			Where("id = ? AND quota >= ?", state.user.ID, costMicroUSD).
			UpdateColumn("quota", gorm.Expr("quota - ?", costMicroUSD))
		if res.Error != nil {
			return fmt.Errorf("quota deduct: %w", res.Error)
		}
		tokensTotal := promptTokens + completionTokens
		relatedID := uint(0)
		relatedType := ""
		if apiLogPersisted {
			relatedID = apiLogID
			relatedType = "api_log"
		}
		if res.RowsAffected == 0 {
			var u database.User
			if err := tx.Select("id, quota").First(&u, state.user.ID).Error; err != nil {
				return fmt.Errorf("user row missing: %w", err)
			}
			log.Printf("[WS-BILLING-INSUFFICIENT-BALANCE] user=%d model=%s raw_cost_micro=%d current_quota=%d — pending_reconcile",
				state.user.ID, modelName, costMicroUSD, u.Quota)
			return database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:               state.user.ID,
				EntryType:            database.BillingTypeApiUsagePendingReconcile,
				BillingState:         database.BillingStatePendingReconcile,
				AmountUSD:            0,
				BalanceAfterUSD:      u.Quota,
				ModelName:            modelName,
				TokensTotal:          tokensTotal,
				EstimatedInputTokens: promptTokens,
				EstimatedCostUSD:     costMicroUSD,
				RelatedType:          relatedType,
				RelatedID:            relatedID,
				Description: fmt.Sprintf("[INSUFFICIENT-BALANCE] %s · %d tokens · WebSocket 已交付，余额不足待对账（按 raw 上游成本计 $%s）",
					modelName, tokensTotal, database.FormatMicroUSD(costMicroUSD)),
			})
		}
		var freshUser database.User
		if err := tx.Select("id, quota").First(&freshUser, state.user.ID).Error; err != nil {
			return fmt.Errorf("re-select quota: %w", err)
		}
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:          state.user.ID,
			EntryType:       database.BillingTypeApiConsumeBalance,
			AmountUSD:       -costMicroUSD,
			BalanceAfterUSD: freshUser.Quota,
			ModelName:       modelName,
			TokensTotal:     tokensTotal,
			RelatedType:     relatedType,
			RelatedID:       relatedID,
			Description:     fmt.Sprintf("余额扣费 · WS · %s · %d tokens · %s", modelName, tokensTotal, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
		}); err != nil {
			return fmt.Errorf("write billing: %w", err)
		}
		if _, err := database.ApplyReferralPaidSpendRewardTx(
			tx,
			state.user.ID,
			costMicroUSD,
			referralRewardBPS,
			referralRewardWindowSeconds,
			time.Now(),
			relatedType,
			relatedID,
			fmt.Sprintf("余额扣费 · WS · %s", modelName),
		); err != nil {
			return fmt.Errorf("apply referral reward: %w", err)
		}
		balanceConsumed = true
		consumed = costMicroUSD
		return nil
	})
	if txErr != nil {
		log.Printf("[WS-BILLING-CRITICAL] user=%d model=%s tx failed: %v", state.user.ID, modelName, txErr)
		return 0
	}
	if balanceConsumed && apiLogPersisted {
		RecordApiLogRevenue(apiLogID, database.RevenueSourceBalance, consumed, 0)
	}
	RefreshUserAuth(state.user.ID)
	return consumed
}

// writeWebsocketUsageLines 写一对 ApiLogUsageLine（input + output token）。
func writeWebsocketUsageLines(apiLogID uint, modelName, requestPath string, promptTokens, completionTokens int, inputPricePico, outputPricePico int64) {
	now := time.Now()
	if promptTokens > 0 {
		amountMicro := int64(promptTokens) * inputPricePico / int64(1_000_000_000)
		_ = database.DB.Create(&database.ApiLogUsageLine{
			ApiLogID:       apiLogID,
			ModelName:      modelName,
			RequestPath:    sanitizeError(requestPath, 160),
			Unit:           "token",
			Direction:      "input",
			Quantity:       int64(promptTokens),
			UnitPriceMicro: inputPricePico / int64(1_000_000_000),
			AmountMicroUSD: amountMicro,
			CostSource:     "upstream_usage",
			CreatedAt:      now,
		}).Error
	}
	if completionTokens > 0 {
		amountMicro := int64(completionTokens) * outputPricePico / int64(1_000_000_000)
		_ = database.DB.Create(&database.ApiLogUsageLine{
			ApiLogID:       apiLogID,
			ModelName:      modelName,
			RequestPath:    sanitizeError(requestPath, 160),
			Unit:           "token",
			Direction:      "output",
			Quantity:       int64(completionTokens),
			UnitPriceMicro: outputPricePico / int64(1_000_000_000),
			AmountMicroUSD: amountMicro,
			CostSource:     "upstream_usage",
			CreatedAt:      now,
		}).Error
	}
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

// writeWebsocketPendingReconcileEntry 写一条带 cost 估算的 pending_reconcile 账单
// （用于 commit 阶段订阅 DB 加载失败 / 余额消费被禁用等异常路径，区别于完全无 usage）。
func writeWebsocketPendingReconcileEntry(state *websocketBridgeState, apiLogID uint, apiLogPersisted bool, modelName, reasonTag string, costMicroUSD, chargedCostMicroUSD int64, promptTokens, completionTokens int) {
	relatedID := uint(0)
	relatedType := ""
	if apiLogPersisted {
		relatedID = apiLogID
		relatedType = "api_log"
	}
	_ = database.WriteBillingEntryNonFatal(database.BillingEntryInput{
		UserID:               state.user.ID,
		EntryType:            database.BillingTypeApiUsagePendingReconcile,
		BillingState:         database.BillingStatePendingReconcile,
		AmountUSD:            0,
		BalanceAfterUSD:      state.user.Quota,
		ModelName:            modelName,
		TokensTotal:          promptTokens + completionTokens,
		EstimatedInputTokens: promptTokens,
		EstimatedCostUSD:     costMicroUSD,
		RelatedType:          relatedType,
		RelatedID:            relatedID,
		Description: fmt.Sprintf("[%s] WS %s · %d+%d tokens · %s 待对账",
			reasonTag, modelName, promptTokens, completionTokens, FormatChargedCostForDescription(costMicroUSD, chargedCostMicroUSD)),
	})
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
