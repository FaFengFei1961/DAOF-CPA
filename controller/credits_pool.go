// Package controller / credits_pool.go
//
// 平台号池额度采集 HTTP 端点。
//
// 两个 endpoint：
//   - GET  /api/admin/credits-pool          -> admin 全量明细 (auth_index、邮箱等敏感字段)
//   - POST /api/admin/credits-pool/refresh  -> 立即触发一轮全量刷新（异步，立刻返回）
//
// 实际数据采集 / 缓存 / 重试逻辑在 proxy/credits_pool.go 内常驻 goroutine。
package controller

import (
	"context"
	"time"

	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

// GetAdminCreditsPool 返回完整的号池快照，供 admin 监控看板使用。
// 包含 auth_index / email 等敏感字段，必须挂在 adminApi 路由组下。
func GetAdminCreditsPool(c *fiber.Ctx) error {
	entries, lastFull := proxy.SnapshotAdmin()

	healthy := 0
	total := len(entries)
	byProvider := map[string]int{}
	for _, e := range entries {
		if e.Healthy {
			healthy++
		}
		byProvider[e.Provider]++
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"entries":       entries,
			"total_count":   total,
			"healthy_count": healthy,
			"by_provider":   byProvider,
			"last_full":     lastFull,
			"refreshing":    proxy.IsRefreshing(),
			"server_time":   time.Now(),
		},
	})
}

// RefreshAdminCreditsPool 触发一轮全量刷新。
//
// 在 spawn 后台 goroutine 之前做三道前置检查，**没通过就不报告"成功"**：
//  1. 配置存在（cliproxy_url）
//  2. CPA 可达（5s 同步 ping）
//  3. 没有正在进行的刷新（避免重复 spawn）
//
// 任何一关失败都返回明确的错误状态码 + message_code，让 UI 显示真实原因。
func RefreshAdminCreditsPool(c *fiber.Ctx) error {
	if !proxy.IsCliproxyConfigured() {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "CPA 服务地址未配置（请在「常规偏好」填写 cliproxy_url）",
			"message_code": "ERR_CPA_NOT_CONFIGURED",
		})
	}
	if proxy.IsRefreshing() {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message":      "已有刷新任务在进行中，请等待当前任务完成",
			"message_code": "ERR_CREDITS_REFRESHING",
		})
	}

	// 同步预检 CPA 连通性，5s 超时；不通就拒绝并把真实原因返回给 admin
	pingCtx, pingCancel := context.WithTimeout(c.Context(), 5*time.Second)
	defer pingCancel()
	if err := proxy.PingCliproxy(pingCtx); err != nil {
		// fix Sec-H1：err.Error() 可能包含完整 cliproxy_url（含 token）+ 系统级 socket 错误。
		// 先过 sanitizeError 抹掉 Bearer / token / authorization 等模式再回响应体。
		return c.Status(502).JSON(fiber.Map{
			"success":      false,
			"message":      proxy.SanitizeErrorMessage(err.Error(), 300),
			"message_code": "ERR_CPA_UNREACHABLE",
		})
	}

	go func() {
		// 独立 ctx，请求生命周期结束后仍能跑完
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		proxy.RefreshAllCreditsNow(ctx)
	}()

	return c.JSON(fiber.Map{
		"success":      true,
		"message":      "已触发后台刷新，预计数十秒后完成",
		"message_code": "SUCCESS_CREDITS_REFRESH_TRIGGERED",
	})
}
