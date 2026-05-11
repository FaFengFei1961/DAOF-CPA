// Package proxy / notification_links.go
//
// 通知 action_url 集中 builder。
//
// fix Suggestion Codex UX 审查（第二十五轮）：原来多处 controller / cron 手写 "/subscriptions"、
// "/account#customer-messages" 等字符串，已出现死链事故（前端无 /account view、subscriptions
// 已并入 upgrade）。改为白名单常量 + builder 函数，杜绝字符串散落 + 后续重命名一处生效。
//
// 前端 NotificationCenter.jsx 解析 action_url 时识别这些 view 名做内部路由切换；
// 同源 URL 通过 isSafeNavigateURL 校验后才允许跳转。
//
// 使用规则：
//   - 业务代码用 LinkXxx 系列 helper 拼链接，禁止裸字符串
//   - 新增 view 必须先加到此处 + 前端 App.jsx 的 allowedViews 白名单
package proxy

import "fmt"

// 与 ui/src/App.jsx 的 allowedViews 严格对齐。
//
// 新增 / 重命名 view 必须同步两端，否则通知跳转死链。
const (
	ViewUpgrade  = "upgrade"  // 产品中心（订阅/增量包/购买）
	ViewTopup    = "topup"    // 充值
	ViewBills    = "bills"    // 账单流水
	ViewTickets  = "tickets"  // 工单
	ViewSettings = "settings" // 设置
)

// LinkUpgradeMine 跳到产品中心-我的 tab。
// 通知里"查看订阅 / 续费 / 退款查看"等动作的统一入口。
func LinkUpgradeMine() string {
	return "/" + ViewUpgrade + "?pane=mine"
}

// LinkUpgradeStore 跳到产品中心-商店 tab。
// 用于"快去看看新套餐"营销类通知。
func LinkUpgradeStore() string {
	return "/" + ViewUpgrade + "?pane=store"
}

// LinkTopup 跳到充值页。
func LinkTopup() string {
	return "/" + ViewTopup
}

// LinkBills 跳到账单页。可选 ?filter=topup|sub|refund 等。
func LinkBills(filter string) string {
	if filter == "" {
		return "/" + ViewBills
	}
	return fmt.Sprintf("/%s?filter=%s", ViewBills, filter)
}

// LinkTickets 跳到工单页。用于"联系客服 / 退款申请"类通知。
func LinkTickets() string {
	return "/" + ViewTickets
}

// LinkSettingsTab 跳到设置某个 tab。tab 必须是 Settings.jsx 已注册的 id。
func LinkSettingsTab(tab string) string {
	if tab == "" {
		return "/" + ViewSettings
	}
	return fmt.Sprintf("/%s?tab=%s", ViewSettings, tab)
}
