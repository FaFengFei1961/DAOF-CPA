// Package database / notification_email_channel_test.go
//
// Phase G-1.7 单元测试：邮件 channel 偏好（EnabledEmailCategories + IsEmailCategoryEnabled）。
package database

import (
	"testing"
)

func TestIsEmailCategoryEnabled(t *testing.T) {
	tests := []struct {
		name     string
		view     *PreferenceView
		category string
		want     bool
	}{
		{"nil view → false", nil, "refund", false},
		{"nil map → false", &PreferenceView{EnabledEmailCategories: nil}, "refund", false},
		{"empty map → false (opt-in)", &PreferenceView{EnabledEmailCategories: map[string]bool{}}, "refund", false},
		{"missing key → false (opt-in)", &PreferenceView{EnabledEmailCategories: map[string]bool{"other": true}}, "refund", false},
		{"explicit true → true", &PreferenceView{EnabledEmailCategories: map[string]bool{"refund": true}}, "refund", true},
		{"explicit false → false", &PreferenceView{EnabledEmailCategories: map[string]bool{"refund": false}}, "refund", false},
		// 强制送达类（security/system/broadcast/refund 在 forceDeliverCategories）
		// 在邮件 channel 仍按用户偏好 —— 这是有意设计：用户可关闭邮件版本
		{"forceDeliver category still respects opt-in", &PreferenceView{EnabledEmailCategories: map[string]bool{}}, "security", false},
		{"forceDeliver category explicit true", &PreferenceView{EnabledEmailCategories: map[string]bool{"security": true}}, "security", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEmailCategoryEnabled(tc.view, tc.category); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}
