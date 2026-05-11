// Package proxy / notification_dispatcher.go
//
// 通知分发的唯一对外入口。所有业务模块（cron / controller / engine / 触发器）
// 都通过 Dispatch 写通知，不直接调 database.CreateNotificationRecord。
//
// Dispatch 内部流程：
//  1. 强制送达类（security / system / broadcast）→ 直接异步入库，跳过偏好检查
//  2. 普通类别 → 查 PrefCache → 若 IsCategoryEnabled 为 false 直接 return（屏蔽）
//  3. 否则 go database.CreateNotificationRecord（异步落库，不阻塞调用方）
//
// 设计要点：调用方 **永远不阻塞**——即使 PrefCache miss 也是同步内存操作，
// 写库这一步永远在 goroutine 中。
package proxy

import (
	"log"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"daof-ai-hub/database"
)

// fix Major（codex 第四轮）：原 Dispatch 每条通知 `go func()`，无队列、无并发上限、无背压。
// 大量预警/退款/工单同时触发时瞬间产生上百个 goroutine + DB 写入，拖垮 SQLite 与连接池。
//
// 改为有界 worker pool：
//   - 容量 cap=8 的 channel 队列
//   - 启动 4 个常驻 worker 串行写库
//   - 队列满 → 丢弃并 log（业务上可接受小幅丢失，远好于把整个进程 OOM）
const (
	dispatchQueueCap = 256
	dispatchWorkers  = 4
)

type dispatchTask func()

var (
	dispatchQueue   chan dispatchTask
	dispatchOnce    sync.Once
	dispatchWG      sync.WaitGroup
	dispatchStopped atomic.Bool // 标记队列已关闭，避免 send-on-closed-channel panic
)

func ensureDispatchPool() {
	dispatchOnce.Do(func() {
		dispatchQueue = make(chan dispatchTask, dispatchQueueCap)
		for i := 0; i < dispatchWorkers; i++ {
			dispatchWG.Add(1)
			go func() {
				defer dispatchWG.Done()
				for task := range dispatchQueue {
					func() {
						defer func() {
							if r := recover(); r != nil {
								// fix LOW（silent-failure 第十八轮）：附带 stack trace 便于定位 panic 来源
								log.Printf("[DISPATCH] worker panic recovered: %v\n%s", r, debug.Stack())
							}
						}()
						task()
					}()
				}
			}()
		}
	})
}

// StopDispatchPool 优雅停止：关闭队列让 workers 排空后退出。
//
// fix Minor（自审第六轮）：原 worker 是"daemon 类型"无 shutdown 协调，graceful drain 时
// 队列里 in-flight 通知会被静默丢弃。本函数让 main.go 的 OS signal handler 能调用它。
//
// fix Major（codex 第七轮）：先翻 stopped=true，让后续 dispatchAsync 拒绝新任务；
// 再 close channel。in-flight 已入队的任务依然被 worker 排干净。
// 这与 send guard 配合保证不会出现"close 后又 send"的 panic 路径。
func StopDispatchPool() {
	if dispatchQueue == nil {
		return
	}
	if !dispatchStopped.CompareAndSwap(false, true) {
		return // 已经停过
	}
	close(dispatchQueue)
	dispatchWG.Wait()
}

// safeAsync 保护 goroutine 不被 panic 击穿主进程。
// 对 Dispatch 走有界队列；其它业务调用（如 USAGE-WARN）继续用 raw goroutine。
func safeAsync(label string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[%s] goroutine panic recovered: %v\n%s", label, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// dispatchAsync 通过有界队列调度通知写库任务。队列满时丢弃 + 告警。
//
// fix Major（codex 第七轮）：StopDispatchPool close(channel) 后，
// 仍在 in-flight 的 cron / handler 调 Dispatch 会向 closed channel send → panic。
// 用 atomic.Bool guard：stopped 后直接 drop + log，不再尝试 send。
func dispatchAsync(fn func()) {
	if dispatchStopped.Load() {
		log.Printf("[DISPATCH-DROP] dispatcher stopped, dropping notification write")
		return
	}
	ensureDispatchPool()
	// 双重检查：ensureDispatchPool 与 Stop 之间有窄竞态，但 Stop 设 stopped=true 后再 close，
	// 这里 select 的 default 分支也会收住 send → 不会 panic。
	defer func() {
		// 如果 Stop 在 select case <- 之前 close 了 channel，send 会 panic。
		// 用 recover 兜底（极端情况下日志即可，不让进程挂）。
		if r := recover(); r != nil {
			log.Printf("[DISPATCH-DROP] post-shutdown send recovered: %v", r)
		}
	}()
	select {
	case dispatchQueue <- fn:
	default:
		log.Printf("[DISPATCH-DROP] queue full (cap=%d), notification write dropped to protect DB", dispatchQueueCap)
	}
}

// IsSafeActionURL 校验通知里的 action_url：必须是站内绝对路径（以单 `/` 开头，不以 `//` 开头），
// 或为空。拒绝 javascript:/data:/blob:/外部 origin/协议相对 URL（//evil.com）。
//
// fix Major（codex 第四轮）：admin 广播的 action_url 不被验证 → 钓鱼/XSS 风险。
// 本函数对外暴露，供 controller/notification_broadcast.go 等直写通知的入口共用。
func IsSafeActionURL(actionURL string) bool {
	s := strings.TrimSpace(actionURL)
	if s == "" {
		return true
	}
	if strings.ContainsAny(s, "\r\n\t") {
		return false
	}
	// 协议相对 URL 要拒绝（//evil.com 会被浏览器解为外部跳转）
	if strings.HasPrefix(s, "//") {
		return false
	}
	// 必须以 `/` 开头表示站内路径
	if !strings.HasPrefix(s, "/") {
		return false
	}
	// 用 url.Parse 进一步过滤异常 scheme/userinfo
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	// 站内路径解析后 scheme 应为空
	if u.Scheme != "" {
		return false
	}
	if u.User != nil {
		return false
	}
	return true
}

// 强制送达类：永远绕开用户偏好。
//
//   - security  ：账户被封禁等账户安全消息（封号/异地登录）
//   - system    ：系统级管理员公告（默认强制送达）
//   - broadcast ：与 system 同义，用于 admin 群发
//   - refund    ：退款回执（涉及真实金钱），用户即便关闭通知也必须收到 ——
//     fix MAJOR M-A7（codex 第二十一轮）：原配置漏掉 refund，
//     用户关 notification_pref 后退款通知会丢，与"退款已确认"产品文案不符
var forceDeliverDispatchCategories = map[string]bool{
	"security":  true,
	"system":    true,
	"broadcast": true,
	"refund":    true,
}

// Dispatch 对外通知分发入口。
//
// 参数：
//
//	userID      ── 收件人；0 时直接 return（系统消息走 broadcast 表 + 显式 user_ids 解开）
//	category    ── 类别字符串，参见 plan 文档；强制送达类见 forceDeliverDispatchCategories
//	severity    ── info | success | warning | error
//	title/body  ── UI 显示
//	actionURL   ── 点击跳转（可空）
//	actionText  ── 跳转按钮文案（可空）
//	relatedType ── 关联实体类型（subscription/refund/topup/...，可空）
//	relatedID   ── 关联实体 ID（可空 0）
//	dedupKey    ── 跨进程/重复触发去重；nil 表示不去重
//
// 永不返回错误：所有失败（DB 不可用、JSON 解析失败、唯一冲突）走 log.Printf 而非 panic。
func Dispatch(userID uint, category, severity, title, body, actionURL, actionText, relatedType string, relatedID uint, dedupKey *string) {
	if userID == 0 {
		return
	}

	// fix Major（codex 第四轮）：action_url 校验——任何未通过白名单的 URL（外部域、javascript: 等）
	// 在写库前清空，避免钓鱼/脚本注入随通知投递到前端。
	if !IsSafeActionURL(actionURL) {
		log.Printf("[DISPATCH] reject unsafe action_url=%q user=%d category=%s", actionURL, userID, category)
		actionURL = ""
		actionText = ""
	}

	// 强制送达：跳过偏好检查
	//
	// fix MAJOR M-B8（codex 第二十一轮）：原走 dispatchAsync → 队列满（cap=256）时被丢弃。
	// security/system/broadcast 类承诺"强制送达"，丢失会让封号 / 系统警告 / 退款通知
	// 在用户最关心的瞬间消失。改为**同步写库**，保证落库后才返回。代价是调用方阻塞一次 DB 写
	// 入的时间（≤几 ms）；对调用频率（封号 / admin 广播 / 强警报）来说完全可接受。
	// 注意：CreateNotificationRecord 内部失败仅 log，所以这里不会抛 panic 或返回 error。
	if forceDeliverDispatchCategories[category] {
		database.CreateNotificationRecord(
			userID, category, severity, title, body,
			actionURL, actionText, relatedType, relatedID, dedupKey,
		)
		return
	}

	// 普通类别：查偏好
	view := GetPrefCached(userID)
	if !database.IsCategoryEnabled(view, category) {
		return
	}

	dispatchAsync(func() {
		database.CreateNotificationRecord(
			userID, category, severity, title, body,
			actionURL, actionText, relatedType, relatedID, dedupKey,
		)
	})
}
