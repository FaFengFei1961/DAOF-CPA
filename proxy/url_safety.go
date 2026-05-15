// Package proxy / url_safety.go
//
// Channel BaseURL / ProxyURL 安全校验。
//
// 防御场景：admin 误填 / SQL 注入 / 配置入侵导致 channel.BaseURL 指向
//   - 非 HTTP scheme（file:// gopher:// dict:// jar:// data:// 等）→ 协议级 SSRF / 文件读取
//   - 元数据服务（169.254.169.254 / metadata.google.internal）→ 云凭证窃取
//   - 解析后包含控制字符 / 用户名密码 / 片段 → 异常注入
//
// 注意：本系统**允许** localhost/RFC1918 作为 BaseURL —— 自部署 CPA / vLLM / Ollama
// 通常跑在 127.0.0.1:8317 等本地端口。这是正常使用模式，不是漏洞。
// 真正要拦的是 (1) 非 HTTP 协议（敏感）和 (2) 云元数据/链路本地等永远不该访问的特殊段。
package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var (
	lookupIPAddr = net.DefaultResolver.LookupIPAddr

	forbiddenDestPrefixes = []netip.Prefix{
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("168.63.129.16/32"),
		netip.MustParsePrefix("100.100.100.200/32"),
		netip.MustParsePrefix("fd00:ec2::254/128"),
		netip.MustParsePrefix("fe80::/10"),
	}
)

// isForbiddenDestIP 判定 TCP 连接前已解析的对端 IP 是否落入禁飞名单。
// fix Major（gemini 复审）：DNS rebinding 防御——配置时 host 解析合法，
// 实际 dial 时 DNS A 记录可能被换成 169.254.169.254。
// 在 DialContext 里再校验一次解析后的 IP 才能挡住此攻击。
func isForbiddenDestIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, p := range forbiddenDestPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// safeDialContext 是带 DNS 重绑定防御的 net.Dial 包装。
// 流程：用 ctx 上的 resolver 解析 host → 校验每条 IP → 全部安全才 dial。
//
// 不把未校验的二次 DNS 结果交给 net.Dialer，而是对已校验 IP 做小型 Happy-Eyeballs：
//   - 任何一条解析结果命中禁飞名单 → 立即拒绝（不拉一个再 fallback 另一个）
//   - 解析失败 → 直接拒绝（避免 fallthrough 到无校验路径）
//   - 已校验地址交错拨号，首个成功连接胜出
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host port %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip != nil {
		if isForbiddenDestIP(ip) {
			return nil, fmt.Errorf("blocked dest IP %s (cloud metadata / link-local / multicast)", ip)
		}
		// 字面量 IP，直接 dial；标准库会跳过 DNS 解析
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	// 域名：先 LookupIPAddr（用 ctx 的 resolver，遵守 timeout），再校验所有解析结果
	addrs, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, a := range addrs {
		if isForbiddenDestIP(a.IP) {
			return nil, fmt.Errorf("DNS rebinding guard: %s resolved to forbidden IP %s", host, a.IP)
		}
	}
	// 全部 IP 都安全，手写 Happy-Eyeballs：对预校验地址交错拨号，首个成功胜出。
	return dialPrevalidatedAddrs(ctx, network, port, addrs, 0)
}

func dialPrevalidatedAddrs(ctx context.Context, network, port string, addrs []net.IPAddr, timeout time.Duration) (net.Conn, error) {
	type dialResult struct {
		conn   net.Conn
		err    error
		target string
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan dialResult, len(addrs))
	attempts := 0
	for _, addr := range addrs {
		ip := addr.IP
		if ip == nil {
			continue
		}
		if network == "tcp4" && ip.To4() == nil {
			continue
		}
		if network == "tcp6" && ip.To4() != nil {
			continue
		}
		target := net.JoinHostPort(ip.String(), port)
		delay := time.Duration(attempts) * 50 * time.Millisecond
		attempts++
		go func(target string, delay time.Duration) {
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					results <- dialResult{target: target, err: ctx.Err()}
					return
				}
			}
			dialer := net.Dialer{Timeout: timeout}
			conn, err := dialer.DialContext(ctx, network, target)
			results <- dialResult{conn: conn, err: err, target: target}
		}(target, delay)
	}
	if attempts == 0 {
		return nil, fmt.Errorf("no resolved addresses usable for network %s", network)
	}

	errs := make([]string, 0, attempts)
	for i := 0; i < attempts; i++ {
		select {
		case result := <-results:
			if result.err == nil {
				cancel()
				return result.conn, nil
			}
			errs = append(errs, fmt.Sprintf("%s: %v", result.target, result.err))
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("dial resolved addresses failed: %s", strings.Join(errs, "; "))
}

// ValidateChannelURL 校验 Channel.BaseURL 或 ProxyURL（允许空字符串）。
//
// 接受：空字符串（不配置）、http://*、https://*；含 localhost / RFC1918（自部署常见）。
// 拒绝：非 http(s) scheme；解析错误；用户名密码（防偷渡凭证）；云元数据服务；链路本地多播。
//
// fix Major：codex 复审指出 channel 创建/更新缺 BaseURL 白名单/scheme 校验，
// 攻击者（拿到 admin token 或入侵 SQL）可让代理 SSRF 任意协议（file:// → 读 /etc/passwd）。
func ValidateChannelURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if strings.ContainsAny(raw, "\r\n\t") {
		return fmt.Errorf("URL contains control characters")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	// scheme 仅允许 http/https；显式拒绝其他高危协议
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL must include host")
	}
	// 不允许嵌入凭证：http://user:pass@host 会让审计日志泄露 / 也是钓鱼形式
	if u.User != nil {
		return fmt.Errorf("URL must not contain userinfo (user:pass@host)")
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("URL host empty")
	}

	// 拒绝云元数据服务（即使是 IP 形式或域名形式）
	switch host {
	case "metadata.google.internal", "metadata.goog", "metadata":
		return fmt.Errorf("metadata service hosts not allowed")
	}

	// IP 形式额外检查
	if ip := net.ParseIP(host); ip != nil {
		if isForbiddenDestIP(ip) {
			return fmt.Errorf("cloud metadata / link-local / multicast IP not allowed")
		}
	}

	return nil
}

// redirectGuard re-runs URL safety checks for every redirect target.
// This blocks a public upstream from redirecting the client to cloud metadata
// or other forbidden special-purpose destinations.
func redirectGuard(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return http.ErrUseLastResponse
	}
	if req == nil || req.URL == nil {
		return fmt.Errorf("redirect blocked: missing target URL")
	}
	if err := ValidateChannelURL(req.URL.String()); err != nil {
		return fmt.Errorf("redirect blocked: %w", err)
	}
	if len(via) > 0 {
		prev := via[0].URL
		if prev == nil {
			return fmt.Errorf("redirect blocked: missing previous URL")
		}
		if req.URL.Host != prev.Host || req.URL.Scheme != prev.Scheme {
			return fmt.Errorf("redirect blocked: cross-host/scheme not allowed (%s -> %s)", prev.Host, req.URL.Host)
		}
	}
	return nil
}

// RedirectGuard exposes the shared redirect policy to controller-side clients.
func RedirectGuard(req *http.Request, via []*http.Request) error {
	return redirectGuard(req, via)
}
