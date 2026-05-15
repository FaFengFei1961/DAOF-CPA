package controller

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
)

// ProxyCLIProxyUsage 安全代理转发 CLIProxyAPI /v0/management/usage 请求
// Management Key 存储在服务端加密 SysConfig 中，绝不下发到前端
//
// fix Major（codex 第五轮）：
//  1. cliproxy_url 必须经 ValidateChannelURL 防 SSRF（拒绝云元数据 / 链路本地 / 非 http(s)）
//  2. HTTP client 必须使用 SafeTransport，dial 时再次校验解析 IP 防 DNS rebinding
//  3. 限制响应体大小（4MB），避免上游异常时 OOM
//  4. 不把上游 body 原样返回（含 Management Key 错误也不外泄）
func ProxyCLIProxyUsage(c *fiber.Ctx) error {
	// 从加密配置库读取 CLIProxyAPI 连接信息
	cliproxyURL := getDecryptedConfig("cliproxy_url")
	cliproxyKey := getDecryptedConfig("cliproxy_key")

	if cliproxyURL == "" {
		cliproxyURL = "http://127.0.0.1:8080"
	}

	// SSRF 防护：scheme 白名单 + 拒绝云元数据/链路本地/userinfo
	if err := proxy.ValidateChannelURL(cliproxyURL); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "cliproxy_url 配置不合法：" + err.Error(),
			"message_code": "ERR_CLIPROXY_URL_UNSAFE",
		})
	}

	// 拼接目标 URL
	targetURL := strings.TrimRight(cliproxyURL, "/") + "/v0/management/usage"

	// 构造安全代理请求 + SafeTransport 防 DNS rebinding
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: proxy.SafeTransport(),
	}
	req, err := http.NewRequestWithContext(c.Context(), "GET", targetURL, nil)
	if err != nil {
		log.Printf("[CLIPROXY] build request failed: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message":      "构造代理请求失败",
			"message_code": "ERR_CLIPROXY_BUILD",
		})
	}

	// 将 Management Key 仅在服务端注入，前端永远不接触该值
	if cliproxyKey != "" {
		req.Header.Set("Authorization", "Bearer "+cliproxyKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		// fix Minor（gemini 第六轮）：原 message 直接拼 err.Error()，会泄露
		// "connection refused" / 内网拓扑等内部细节给前端。详细 err 仅记日志，对外通用消息。
		log.Printf("[CLIPROXY] connect failed: %v", err)
		return c.Status(502).JSON(fiber.Map{
			"success":      false,
			"message":      "上游服务连接失败",
			"message_code": "ERR_CLIPROXY_UNREACHABLE",
		})
	}
	defer resp.Body.Close()

	// 限制响应体大小，防上游异常时 OOM
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		log.Printf("[CLIPROXY] read body failed: %v", err)
		return c.Status(502).JSON(fiber.Map{
			"success":      false,
			"message":      "读取上游响应失败",
			"message_code": "ERR_CLIPROXY_READ",
		})
	}

	if resp.StatusCode != 200 {
		// 不把上游 body 原样返回（可能含 Management Key 回显或敏感栈信息），仅暴露状态码
		return c.Status(resp.StatusCode).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("CLIProxyAPI 返回错误状态码 %d", resp.StatusCode),
			"message_code": "ERR_CLIPROXY_UPSTREAM",
		})
	}

	// 直接透传 JSON 响应
	c.Set("Content-Type", "application/json")
	return c.Status(200).Send(body)
}

// getDecryptedConfig 从加密 SysConfig 表读取并解密一个配置值
func getDecryptedConfig(key string) string {
	var config database.SysConfig
	if err := database.DB.Where("`key` = ?", key).First(&config).Error; err != nil {
		return ""
	}
	val, err := utils.Decrypt(config.Value)
	if err != nil {
		return ""
	}
	return val
}
