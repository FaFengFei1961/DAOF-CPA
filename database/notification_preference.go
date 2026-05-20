// Package database / notification_preference.go
//
// 用户通知偏好的加载/保存与判定 helper。
//
// 关键约定：
//  1. "lazy default"：未保存偏好的用户，LoadPreference 从 SysConfig 读默认值并返回内存对象，**不写表**
//     （避免每个新用户都 INSERT 一行；用户改过才写）
//  2. IsCategoryEnabled：security 永远 true（强制送达）；缺失的 key 视为启用
//  3. CrossedThresholds：传入 before/after 百分比，返回跨过的阈值列表（升序）
//
// 该文件不依赖 proxy 包（避免循环依赖）。读 SysConfig 直接查 DB + utils.Decrypt。
package database

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"daof-cpa/utils"

	"gorm.io/gorm/clause"
)

// PreferenceView 是 LoadPreference 返回的内存对象，已解析 JSON 字段。
// 修改它不会回写 DB；写库走 SavePreference。
type PreferenceView struct {
	UserID                 uint            `json:"user_id"`
	EnabledCategories      map[string]bool `json:"enabled_categories"`
	UsageThresholds        []int           `json:"usage_thresholds"`
	// EnabledEmailCategories：per-category 邮件 channel 开关。
	// 默认值（loadPreferenceDefaults）按 SysConfig notif_default_email_categories；
	// 缺失的 key 在 IsEmailCategoryEnabled 里视为**禁用**（保守 opt-in）。
	EnabledEmailCategories map[string]bool `json:"enabled_email_categories"`
}

// 强制送达类：永远忽略偏好。
//
// fix MAJOR M-A7（codex 第二十一轮）：与 proxy.forceDeliverDispatchCategories 保持一致 ——
// security / system / broadcast / refund 都强制送达。
// refund 属于真实金钱回执（涉及人民币 / 美元），用户偏好不能屏蔽，否则用户会以为没退款。
var forceDeliverCategories = map[string]bool{
	"security":  true,
	"system":    true,
	"broadcast": true,
	"refund":    true,
}

// LoadPreference 加载用户偏好。未保存的用户返回系统默认（不写库）。
func LoadPreference(userID uint) *PreferenceView {
	defaults := loadPreferenceDefaults()
	view := &PreferenceView{
		UserID:                 userID,
		EnabledCategories:      defaults.EnabledCategories,
		UsageThresholds:        defaults.UsageThresholds,
		EnabledEmailCategories: defaults.EnabledEmailCategories,
	}
	if DB == nil || userID == 0 {
		return view
	}

	var pref NotificationPreference
	if err := DB.Where("user_id = ?", userID).First(&pref).Error; err != nil {
		return view // 没保存过：返回 defaults
	}

	if pref.EnabledCategories != "" {
		var cats map[string]bool
		// fix LOW（codex 第十九轮）：原 if err==nil 只在成功时赋值——失败时默默退回 defaults，
		// 用户感觉"我配的偏好没生效"。改为 log 异常后仍退回 defaults（避免 admin 入库脏数据后阻塞读路径）。
		if err := json.Unmarshal([]byte(pref.EnabledCategories), &cats); err == nil {
			view.EnabledCategories = cats
		} else {
			log.Printf("[NOTIF-PREF] user=%d invalid EnabledCategories json: %v (raw=%q)", userID, err, pref.EnabledCategories)
		}
	}
	if pref.EnabledEmailCategories != "" {
		var cats map[string]bool
		if err := json.Unmarshal([]byte(pref.EnabledEmailCategories), &cats); err == nil {
			view.EnabledEmailCategories = cats
		} else {
			log.Printf("[NOTIF-PREF] user=%d invalid EnabledEmailCategories json: %v (raw=%q)", userID, err, pref.EnabledEmailCategories)
		}
	}
	if pref.UsageThresholds != "" {
		var thr []int
		if err := json.Unmarshal([]byte(pref.UsageThresholds), &thr); err == nil {
			sort.Ints(thr)
			view.UsageThresholds = thr
		} else {
			log.Printf("[NOTIF-PREF] user=%d invalid UsageThresholds json: %v (raw=%q)", userID, err, pref.UsageThresholds)
		}
	}
	return view
}

// SavePreference 保存用户偏好（upsert）。caller 写完应调 proxy.InvalidatePrefCache(uid)。
//
// fix Phase G-1.7（2026-05-20）：新增 enabledEmailCategories 参数。nil 表示
// "不修改邮箱 channel 偏好"（保留 DB 现有值或默认 {}）；非 nil（含空 map）
// 覆盖整个邮件类别开关 map。
func SavePreference(userID uint, enabledCategories map[string]bool, usageThresholds []int, enabledEmailCategories map[string]bool) error {
	if DB == nil {
		return fmt.Errorf("database not initialized")
	}
	if userID == 0 {
		return fmt.Errorf("invalid user_id")
	}
	// 阈值规范化：去重、过滤超界、排序
	cleaned := make([]int, 0, len(usageThresholds))
	seen := map[int]bool{}
	for _, v := range usageThresholds {
		if v <= 0 || v > 100 || seen[v] {
			continue
		}
		seen[v] = true
		cleaned = append(cleaned, v)
	}
	sort.Ints(cleaned)

	catsJSON, err := json.Marshal(enabledCategories)
	if err != nil {
		return fmt.Errorf("marshal categories: %w", err)
	}
	thrJSON, err := json.Marshal(cleaned)
	if err != nil {
		return fmt.Errorf("marshal thresholds: %w", err)
	}

	pref := NotificationPreference{
		UserID:            userID,
		EnabledCategories: string(catsJSON),
		UsageThresholds:   string(thrJSON),
	}
	updateCols := []string{
		"enabled_categories",
		"usage_thresholds",
		"updated_at",
	}
	if enabledEmailCategories != nil {
		emailJSON, err := json.Marshal(enabledEmailCategories)
		if err != nil {
			return fmt.Errorf("marshal email categories: %w", err)
		}
		pref.EnabledEmailCategories = string(emailJSON)
		updateCols = append(updateCols, "enabled_email_categories")
	}
	if err := DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns(updateCols),
	}).Create(&pref).Error; err != nil {
		return fmt.Errorf("upsert preference: %w", err)
	}
	return nil
}

// IsCategoryEnabled 检查某类别是否启用。
//   - security 等强制送达类永远返回 true
//   - 偏好里缺失的 key 视为启用（默认开启）
//   - 显式 false 才屏蔽
func IsCategoryEnabled(view *PreferenceView, category string) bool {
	if forceDeliverCategories[category] {
		return true
	}
	if view == nil || view.EnabledCategories == nil {
		return true
	}
	enabled, exists := view.EnabledCategories[category]
	if !exists {
		return true // 未明确配置=启用
	}
	return enabled
}

// IsEmailCategoryEnabled 检查某类别是否在用户偏好里启用邮件 channel。
// 与 in-app 不同：邮件 channel 默认**保守 opt-in**——缺失的 key 视为禁用，
// 用户必须显式打开（避免一注册就被邮件轰炸）。
//
// security / system 等强制送达类**也受此偏好控制**——即使 in-app 强制送达，
// 邮件 channel 仍按用户偏好。这是有意设计：用户可能不想被 security 邮件吵醒，
// 但 in-app 必须可见。
//
// Phase G-1.7（2026-05-20）。
func IsEmailCategoryEnabled(view *PreferenceView, category string) bool {
	if view == nil || view.EnabledEmailCategories == nil {
		return false // 没配 → 不发邮件
	}
	enabled, exists := view.EnabledEmailCategories[category]
	if !exists {
		return false // 缺失 → 不发邮件（保守）
	}
	return enabled
}

// CrossedThresholds 计算 before→after 跨过的阈值（升序）。
// 例：before=78, after=82, thresholds=[80,100] → [80]
//
//	before=78, after=100.5, thresholds=[80,100] → [80, 100]
func CrossedThresholds(view *PreferenceView, beforePct, afterPct float64) []int {
	if view == nil || len(view.UsageThresholds) == 0 {
		return nil
	}
	if afterPct <= beforePct {
		return nil // 用量没增长（理论上不该发生）
	}
	crossed := make([]int, 0, len(view.UsageThresholds))
	for _, thr := range view.UsageThresholds {
		t := float64(thr)
		if beforePct < t && afterPct >= t {
			crossed = append(crossed, thr)
		}
	}
	return crossed
}

// loadPreferenceDefaults 从 SysConfig 读取系统默认偏好。
// 不依赖 proxy 缓存——直接查 DB + Decrypt（被 LoadPreference 调用，PrefCache 已挡在前面）。
func loadPreferenceDefaults() *PreferenceView {
	view := &PreferenceView{
		EnabledCategories: map[string]bool{
			"subscription_expiring":   true,
			"subscription_usage_warn": true,
			"refund":                  true,
		},
		UsageThresholds: []int{80, 100},
		// 默认所有邮件类别都关：用户必须显式 opt-in 才收邮件
		EnabledEmailCategories: map[string]bool{},
	}
	if DB == nil {
		return view
	}

	if raw := readSysConfigPlain("notif_default_categories"); raw != "" {
		var cats map[string]bool
		if err := json.Unmarshal([]byte(raw), &cats); err == nil {
			view.EnabledCategories = cats
		}
	}
	if raw := readSysConfigPlain("notif_default_thresholds_csv"); raw != "" {
		view.UsageThresholds = parseThresholdCSV(raw)
	}
	if raw := readSysConfigPlain("notif_default_email_categories"); raw != "" {
		var cats map[string]bool
		if err := json.Unmarshal([]byte(raw), &cats); err == nil {
			view.EnabledEmailCategories = cats
		}
	}
	return view
}

// readSysConfigPlain 从 SysConfig 表读单个 key 并解密。失败返回空字符串。
func readSysConfigPlain(key string) string {
	if DB == nil {
		return ""
	}
	var sc SysConfig
	if err := DB.Where("key = ?", key).First(&sc).Error; err != nil {
		return ""
	}
	val, err := utils.Decrypt(sc.Value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(val)
}

// parseThresholdCSV 解析 "80,100" → [80, 100]。空字符串/全无效返回 []。
func parseThresholdCSV(s string) []int {
	if s == "" {
		return []int{}
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	seen := map[int]bool{}
	for _, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v <= 0 || v > 100 || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Ints(out)
	return out
}
