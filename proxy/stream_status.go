// Package proxy / stream_status.go
//
// M-R2 重构（2026-05-19）：从 stream.go 抽出 status 相关 helper，纯文件物理拆分。
// 业务逻辑零改动；handler ChatCompletionProxyHandler 仍在 stream.go。

package proxy

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type StatusAction int

const (
	StatusActionSuccess StatusAction = iota
	StatusActionRetryableTransient
	StatusActionRateLimit
	StatusActionUpstreamFatal
	StatusActionConfigError
	StatusActionClientError
	StatusActionAuthError
	StatusActionUnknown
)


func classifyUpstreamStatus(status int) StatusAction {
	if status >= 200 && status <= 299 {
		return StatusActionSuccess
	}
	switch status {
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return StatusActionRetryableTransient
	case http.StatusTooManyRequests:
		return StatusActionRateLimit
	case http.StatusNotFound, http.StatusGone:
		return StatusActionConfigError
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return StatusActionClientError
	case http.StatusUnauthorized, http.StatusForbidden:
		return StatusActionAuthError
	}
	if status >= 500 {
		return StatusActionUpstreamFatal
	}
	return StatusActionUnknown
}

func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		d := time.Until(at)
		if d <= 0 {
			return 0
		}
		return d
	}
	return 0
}

// safeTransport 是 http.DefaultTransport 的派生，带 DNS-rebinding-resistant DialContext。
// 仅在没有 proxyURL 时使用（直连上游）；走 HTTP 代理时由代理服务器自己解析 host，

func isPlainCLIProxyRouteNotFound(channelType string, status int, body []byte) bool {
	if NormalizeChannelType(channelType) != ChannelTypeCLIProxy || status != http.StatusNotFound {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(string(body)))
	if msg == "404 page not found" {
		return true
	}
	if len(msg) <= 200 && strings.Contains(msg, "404 page not found") {
		return true
	}
	return false
}
