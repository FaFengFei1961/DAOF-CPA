package utils

// safe_dialer.go
//
// fix B-M1 (2026-05-19)：utils 包独立的 SSRF-safe HTTP dialer，用于 aliyun_sms.go
// 等 utils 层的外发 HTTP 调用。proxy/yifut_client.go / proxy/url_safety.go 有更
// 完整的 SSRF 防御层，但 utils 不能 import proxy（循环依赖），故在此提供一个
// 自包含的最小化版本：解析后检查每个 IP，命中私网 / 回环 / 链路本地 / 元数据
// 段就拒绝拨号。覆盖 DNS rebinding 攻击（攻击者控制本机 hosts 或内网 DNS）。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

var (
	safeHTTPClientOnce sync.Once
	safeHTTPClient     *http.Client
)

// SafeHTTPClient 返回一个进程级共享的、带 SSRF 防护的 http.Client。
// timeout = 15s，DialContext 拒绝任何解析到私网/回环/链路本地/元数据 IP 的主机。
// 适用于：utils/aliyun_sms.go 等 utils 层外发调用。
func SafeHTTPClient() *http.Client {
	safeHTTPClientOnce.Do(func() {
		safeHTTPClient = &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext:       safeUtilsDialContext,
				DisableKeepAlives: false,
				MaxIdleConns:      10,
				IdleConnTimeout:   90 * time.Second,
			},
		}
	})
	return safeHTTPClient
}

// safeUtilsDialContext 解析 host → 对每个 IP 检查安全性 → 全部通过才拨号。
func safeUtilsDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("safe-dial: invalid addr %q: %w", addr, err)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("safe-dial: dns lookup %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("safe-dial: no IP resolved for %s", host)
	}
	// 任一 IP 命中黑名单都拒绝（防 DNS round-robin 混入内网 IP）
	for _, ipa := range ips {
		if isUtilsUnsafeIP(ipa.IP) {
			return nil, fmt.Errorf("safe-dial: refused unsafe IP %s for host %s (private/loopback/link-local/metadata)", ipa.IP, host)
		}
	}
	// 拨第一个解析到的 IP（一般是 IPv6 优先）
	d := &net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

// isUtilsUnsafeIP 返回 true 表示该 IP 命中黑名单。
// 与 proxy/yifut_client.go::isUnsafeIP 同口径，但本地化在 utils 包避免循环依赖。
func isUtilsUnsafeIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	// 云元数据 IP（AWS / GCP / Azure / Alicloud 等都用同一段）
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return true
	}
	// IPv6 link-local fe80::/10、ULA fc00::/7 已被 IsLinkLocalUnicast / IsPrivate 覆盖
	return false
}
