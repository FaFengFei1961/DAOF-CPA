// Package proxy / image_upstream.go
//
// M-R6 重构（2026-05-19）：从 image_generation.go 1892 行单体抽出 upstream 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	mrand "math/rand/v2"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
)

func costTicksFromImageResponse(body []byte) int64 {
	for _, path := range []string{"usage.cost_in_usd_ticks", "usage.costInUsdTicks", "cost_in_usd_ticks"} {
		v := gjson.GetBytes(body, path)
		if v.Exists() && v.Int() > 0 {
			return v.Int()
		}
	}
	return 0
}

func countGeneratedImages(body []byte) int {
	data := gjson.GetBytes(body, "data")
	if data.IsArray() {
		count := 0
		data.ForEach(func(_, _ gjson.Result) bool {
			count++
			return true
		})
		return count
	}
	// SSE completed event 不带 `data` 数组：gpt-image-2 流式响应直接在根上挂
	// b64_json / image_url，按单图处理（与 OpenAI 官方 image_generation.completed
	// 事件结构对齐）。
	if gjson.GetBytes(body, "b64_json").Exists() ||
		gjson.GetBytes(body, "image_url").Exists() ||
		gjson.GetBytes(body, "url").Exists() {
		return 1
	}
	return 0
}

func lockImageBalance(userID uint) func() {
	v, _ := imageBalanceLocks.LoadOrStore(userID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	// fix P1-stream: 返回的 unlock 必须幂等——非流式路径用 defer 释放，流式路径
	// 把所有权移交给 SetBodyStreamWriter callback 在最末释放；两路都可能调用，
	// 用 sync.Once 包装保证 mu.Unlock 不会被调用第二次（panic）。
	var once sync.Once
	return func() { once.Do(mu.Unlock) }
}

func loadFreshUserForImageBalance(userID uint) (*database.User, error) {
	var user database.User
	if err := database.DB.Select("id, username, role, token, quota, paid_quota, status, balance_consume_enabled, balance_consume_limit_usd, balance_consume_window_seconds, balance_consume_window_start_at, balance_consumed_in_window").
		First(&user, userID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func callImageUpstream(c *fiber.Ctx, modelName string, body []byte, routes []*database.ChannelModel, channelMapRef map[uint]*database.Channel, isStream bool, endpoint string) (*selectedImageUpstream, *upstreamImageError) {
	failedChannels := make(map[uint]bool)
	maxRetries := len(routes)
	if maxRetries > 5 {
		maxRetries = 5
	}
	var last *upstreamImageError
	for attempt := 0; attempt < maxRetries; attempt++ {
		if backoff := computeRetryBackoff(attempt); backoff > 0 {
			select {
			case <-time.After(backoff):
			case <-c.Context().Done():
				return nil, imageErr(499, "client_disconnect_during_retry", "client disconnected during retry backoff")
			}
		}
		available, totalWeight := availableImageRoutes(routes, failedChannels, modelName)
		if len(available) == 0 {
			if last != nil {
				return nil, last
			}
			return nil, imageErr(502, "backend_exhausted", "All image upstream channels exhausted or failing")
		}
		selected := chooseWeightedImageRoute(available, totalWeight)
		ch := channelMapRef[selected.ChannelID]
		if ch == nil {
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "channel_unavailable", "channel was disabled or removed mid-flight")
			continue
		}
		channelType := NormalizeChannelType(ch.Type)
		if channelType != ChannelTypeCLIProxy {
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "channel_misconfigured", "image generation is only supported through CLIProxyAPI channels")
			continue
		}
		upstreamURL := strings.TrimRight(ch.BaseURL, "/") + endpoint
		upstreamCtx, upstreamCancel := context.WithCancel(c.Context())
		httpReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, upstreamURL, bytes.NewReader(body))
		if err != nil {
			upstreamCancel()
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "bad_gateway", err.Error())
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if isStream {
			httpReq.Header.Set("Accept", "text/event-stream")
		} else {
			httpReq.Header.Set("Accept", "application/json")
		}
		httpReq.Header.Set("Authorization", "Bearer "+ch.Key)
		if ch.Headers != "" {
			var customHeaders map[string]string
			if err := json.Unmarshal([]byte(ch.Headers), &customHeaders); err == nil {
				for k, v := range customHeaders {
					httpReq.Header.Set(k, v)
				}
			} else {
				log.Printf("[IMAGE] channel %d invalid Headers json: %v (raw=%q)", ch.ID, err, ch.Headers)
			}
		}
		httpClient := &http.Client{
			Transport: getTransport(ch.ProxyURL),
			Timeout:   nonStreamUpstreamTimeout(),
		}
		resp, err := httpClient.Do(httpReq)
		if err != nil {
			upstreamCancel()
			failedChannels[selected.ChannelID] = true
			MarkChannelFailure(selected.ChannelID, 0)
			log.Printf("[IMAGE-UPSTREAM-DIAL] channel=%d err=%s", selected.ChannelID, sanitizeError(err.Error(), 256))
			last = imageErr(502, "bad_gateway", "upstream connection failed (channel rotated)")
			continue
		}
		action := classifyUpstreamStatus(resp.StatusCode)
		switch action {
		case StatusActionSuccess, StatusActionClientError:
			MarkChannelSuccess(selected.ChannelID)
			return &selectedImageUpstream{resp: resp, route: selected, channel: ch, cancel: upstreamCancel}, nil
		case StatusActionRateLimit:
			failedChannels[selected.ChannelID] = true
			setChannelRateLimitCooldown(selected.ChannelID, parseRetryAfter(resp.Header.Get("Retry-After")))
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			log.Printf("[IMAGE-UPSTREAM-RATE-LIMIT] channel=%d status=%d body=%q", selected.ChannelID, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(http.StatusTooManyRequests, "upstream_rate_limited", "all upstream channels are rate limited")
		case StatusActionConfigError:
			failedChannels[selected.ChannelID] = true
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			markChannelModelUnhealthy(selected.ChannelID, modelName)
			log.Printf("[IMAGE-UPSTREAM-CONFIG] channel=%d model=%s status=%d body=%q", selected.ChannelID, modelName, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(resp.StatusCode, "channel_model_unhealthy", "upstream returned config error for configured image model")
		default:
			failedChannels[selected.ChannelID] = true
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			MarkChannelFailure(selected.ChannelID, resp.StatusCode)
			log.Printf("[IMAGE-UPSTREAM-ERR] channel=%d status=%d body=%q", selected.ChannelID, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(resp.StatusCode, "upstream_error", fmt.Sprintf("upstream returned %d (channel rotated)", resp.StatusCode))
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, imageErr(502, "backend_exhausted", "All image upstream channels exhausted or failing")
}

func imageErr(status int, typ, message string) *upstreamImageError {
	if status <= 0 {
		status = 502
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": typ}})
	return &upstreamImageError{status: status, errorType: typ, message: message, body: body}
}

func availableImageRoutes(routes []*database.ChannelModel, failed map[uint]bool, modelName string) ([]*database.ChannelModel, int) {
	out := make([]*database.ChannelModel, 0, len(routes))
	totalWeight := 0
	for _, r := range routes {
		if r == nil || failed[r.ChannelID] || IsChannelRateLimited(r.ChannelID) || IsChannelCircuitOpen(r.ChannelID) || IsChannelModelUnhealthy(r.ChannelID, modelName) {
			continue
		}
		out = append(out, r)
		totalWeight += r.Weight
	}
	return out, totalWeight
}

func chooseWeightedImageRoute(routes []*database.ChannelModel, totalWeight int) *database.ChannelModel {
	if len(routes) == 1 || totalWeight <= 0 {
		return routes[0]
	}
	rNum := mrand.IntN(totalWeight)
	acc := 0
	for _, r := range routes {
		acc += r.Weight
		if rNum < acc {
			return r
		}
	}
	return routes[0]
}
