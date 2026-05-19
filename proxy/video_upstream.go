// Package proxy / video_upstream.go
//
// M-R6 重构（2026-05-19）：从 video_generation.go 1131 行抽出 upstream 相关
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
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

func callVideoUpstream(c *fiber.Ctx, modelName string, body []byte, routes []*database.ChannelModel, channelMapRef map[uint]*database.Channel, endpoint string) (*selectedImageUpstream, *upstreamImageError) {
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
			return nil, imageErr(502, "backend_exhausted", "All video upstream channels exhausted or failing")
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
			last = imageErr(502, "channel_misconfigured", "video generation is only supported through CLIProxyAPI channels")
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
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+ch.Key)
		if key := strings.TrimSpace(c.Get("x-idempotency-key")); key != "" {
			httpReq.Header.Set("x-idempotency-key", key)
		}
		if ch.Headers != "" {
			var customHeaders map[string]string
			if err := json.Unmarshal([]byte(ch.Headers), &customHeaders); err == nil {
				for k, v := range customHeaders {
					httpReq.Header.Set(k, v)
				}
			} else {
				log.Printf("[VIDEO] channel %d invalid Headers json: %v (raw=%q)", ch.ID, err, ch.Headers)
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
			log.Printf("[VIDEO-UPSTREAM-DIAL] channel=%d err=%s", selected.ChannelID, sanitizeError(err.Error(), 256))
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
			log.Printf("[VIDEO-UPSTREAM-RATE-LIMIT] channel=%d status=%d body=%q", selected.ChannelID, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(http.StatusTooManyRequests, "upstream_rate_limited", "all upstream channels are rate limited")
		case StatusActionConfigError:
			failedChannels[selected.ChannelID] = true
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			markChannelModelUnhealthy(selected.ChannelID, modelName)
			log.Printf("[VIDEO-UPSTREAM-CONFIG] channel=%d model=%s status=%d body=%q", selected.ChannelID, modelName, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(resp.StatusCode, "channel_model_unhealthy", "upstream returned config error for configured video model")
		default:
			failedChannels[selected.ChannelID] = true
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			upstreamCancel()
			MarkChannelFailure(selected.ChannelID, resp.StatusCode)
			log.Printf("[VIDEO-UPSTREAM-ERR] channel=%d status=%d body=%q", selected.ChannelID, resp.StatusCode, truncForLog(bodyBytes, 256))
			last = imageErr(resp.StatusCode, "upstream_error", fmt.Sprintf("upstream returned %d (channel rotated)", resp.StatusCode))
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, imageErr(502, "backend_exhausted", "All video upstream channels exhausted or failing")
}
