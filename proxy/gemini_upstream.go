// Package proxy / gemini_upstream.go
//
// M-R6 重构（2026-05-19）：从 gemini_native.go 1319 行单体抽出 upstream 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

func callGeminiNativeUpstream(c *fiber.Ctx, modelName string, geminiReq geminiNativeRequest, body []byte, routes []*database.ChannelModel, channelMapRef map[uint]*database.Channel) (*selectedImageUpstream, *upstreamImageError) {
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
			return nil, imageErr(502, "backend_exhausted", "All Gemini upstream channels exhausted or failing")
		}
		selected := chooseWeightedImageRoute(available, totalWeight)
		ch := channelMapRef[selected.ChannelID]
		if ch == nil {
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "channel_unavailable", "channel was disabled or removed mid-flight")
			continue
		}
		if NormalizeChannelType(ch.Type) != ChannelTypeCLIProxy {
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "channel_misconfigured", "Gemini native is only supported through CLIProxyAPI channels")
			continue
		}
		// SEC-FIX-M2: modelName 经 DB 白名单校验，但纵深防御加 url.PathEscape；
		// SEC-FIX-M1: alt 经 url.QueryEscape 防 query 注入
		urlPath := fmt.Sprintf("%s/%s:%s", strings.TrimRight(ch.BaseURL, "/")+database.EndpointGeminiNative, url.PathEscape(modelName), geminiReq.Method)
		if geminiReq.Alt != "" {
			urlPath += "?alt=" + url.QueryEscape(geminiReq.Alt)
		}
		upstreamCtx, upstreamCancel := context.WithCancel(c.Context())
		httpReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, urlPath, bytes.NewReader(body))
		if err != nil {
			upstreamCancel()
			failedChannels[selected.ChannelID] = true
			last = imageErr(502, "bad_gateway", err.Error())
			continue
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if geminiReq.IsStream {
			httpReq.Header.Set("Accept", "text/event-stream")
		} else {
			httpReq.Header.Set("Accept", "application/json")
		}
		// CPA 用 Bearer 或 ?key=  — 走 Bearer
		httpReq.Header.Set("Authorization", "Bearer "+ch.Key)
		if ch.Headers != "" {
			var customHeaders map[string]string
			if err := json.Unmarshal([]byte(ch.Headers), &customHeaders); err == nil {
				for k, v := range customHeaders {
					httpReq.Header.Set(k, v)
				}
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
			resp.Body.Close()
			upstreamCancel()
			last = imageErr(http.StatusTooManyRequests, "upstream_rate_limited", "all upstream channels are rate limited")
		case StatusActionConfigError:
			failedChannels[selected.ChannelID] = true
			resp.Body.Close()
			upstreamCancel()
			markChannelModelUnhealthy(selected.ChannelID, modelName)
			last = imageErr(resp.StatusCode, "channel_model_unhealthy", "upstream returned config error for Gemini model")
		default:
			failedChannels[selected.ChannelID] = true
			resp.Body.Close()
			upstreamCancel()
			MarkChannelFailure(selected.ChannelID, resp.StatusCode)
			last = imageErr(resp.StatusCode, "upstream_error", fmt.Sprintf("upstream returned %d (channel rotated)", resp.StatusCode))
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, imageErr(502, "backend_exhausted", "All Gemini upstream channels exhausted or failing")
}

// forwardGeminiCountTokens countTokens 透传 — 不计费（只查 metadata）。
func forwardGeminiCountTokens(c *fiber.Ctx, user *database.User, token, modelName string, body []byte, routes []*database.ChannelModel, channelMapRef map[uint]*database.Channel, clientIP string, startTime time.Time, path, alt string) error {
	geminiReq := geminiNativeRequest{Model: modelName, Method: "countTokens", Alt: alt}
	upstream, upstreamErr := callGeminiNativeUpstream(c, modelName, geminiReq, body, routes, channelMapRef)
	if upstreamErr != nil {
		recordProxyApiLog(user.ID, token, modelName, upstreamErr.status, clientIP, startTime, path, upstreamErr.errorType, upstreamErr.message)
		c.Set("Content-Type", "application/json")
		return c.Status(upstreamErr.status).Send(upstreamErr.body)
	}
	defer upstream.resp.Body.Close()
	if upstream.cancel != nil {
		defer upstream.cancel()
	}
	statusCode := upstream.resp.StatusCode
	bodyCopy, _ := io.ReadAll(upstream.resp.Body)
	recordProxyApiLog(user.ID, token, modelName, statusCode, clientIP, startTime, path, "", "")
	copyImageResponseHeaders(c, upstream.resp.Header)
	return c.Status(statusCode).Send(bodyCopy)
}

