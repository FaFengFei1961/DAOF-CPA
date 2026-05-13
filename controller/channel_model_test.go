package controller

import (
	"testing"

	"daof-ai-hub/database"
)

// TestChannelTargetsOfficialHost 覆盖 fix CRITICAL R23 的官方 host 检测：
//   - 空 base_url + 官方 type → 视为官方
//   - 官方 type + 官方 host → 视为官方
//   - 官方 type + 第三方 host → 视为非官方
//   - 非御三家 type → 永远不视为官方
func TestChannelTargetsOfficialHost(t *testing.T) {
	cases := []struct {
		name string
		ch   database.Channel
		want bool
	}{
		{"openai 空 base_url 视为官方", database.Channel{Type: "openai", BaseURL: ""}, true},
		{"openai 官方 host", database.Channel{Type: "openai", BaseURL: "https://api.openai.com/v1"}, true},
		{"openai 第三方 host", database.Channel{Type: "openai", BaseURL: "https://my-relay.example.com/v1"}, false},
		{"anthropic 空", database.Channel{Type: "anthropic", BaseURL: ""}, true},
		{"anthropic 官方", database.Channel{Type: "anthropic", BaseURL: "https://api.anthropic.com"}, true},
		{"anthropic 第三方", database.Channel{Type: "anthropic", BaseURL: "https://relay.example.com"}, false},
		{"gemini 官方", database.Channel{Type: "gemini", BaseURL: "https://generativelanguage.googleapis.com"}, true},
		{"gemini 第三方", database.Channel{Type: "gemini", BaseURL: "https://gemini-proxy.example.com"}, false},
		{"非家族 type", database.Channel{Type: "deepseek", BaseURL: ""}, false},
		{"非法 URL（保守不当作官方）", database.Channel{Type: "openai", BaseURL: "not-a-url"}, false},
		{"大小写 host", database.Channel{Type: "openai", BaseURL: "https://API.OpenAI.com/v1"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := channelTargetsOfficialHost(&tc.ch); got != tc.want {
				t.Errorf("got=%v want=%v ch=%+v", got, tc.want, tc.ch)
			}
		})
	}
}

// TestValidateChannelModelModeration 覆盖 enum 校验 + fail-closed 强制
func TestValidateChannelModelModeration_EnumChecks(t *testing.T) {
	thirdParty := &database.Channel{Type: "openai", BaseURL: "https://relay.example.com"}

	// 非法 level
	cm := database.ChannelModel{ModerationLevel: "EVIL", ModerationFailMode: "open"}
	if status, _, _ := validateChannelModelModeration(&cm, thirdParty, false); status != 400 {
		t.Errorf("non-enum level should 400, got %d", status)
	}

	// 非法 fail-mode
	cm = database.ChannelModel{ModerationLevel: "off", ModerationFailMode: "weird"}
	if status, _, _ := validateChannelModelModeration(&cm, thirdParty, false); status != 400 {
		t.Errorf("non-enum fail-mode should 400, got %d", status)
	}

	// 合法值
	cm = database.ChannelModel{ModerationLevel: "off", ModerationFailMode: "open"}
	if status, _, _ := validateChannelModelModeration(&cm, thirdParty, false); status != 0 {
		t.Errorf("valid third-party off should pass, got %d", status)
	}
}

// TestValidateChannelModelModeration_OfficialFailClosed 覆盖官方渠道关审核需要 confirm
func TestValidateChannelModelModeration_OfficialFailClosed(t *testing.T) {
	official := &database.Channel{Type: "openai", BaseURL: "https://api.openai.com/v1"}

	// 官方 + off + 没 confirm → 拒绝
	cm := database.ChannelModel{ModerationLevel: "off", ModerationFailMode: "open"}
	status, code, _ := validateChannelModelModeration(&cm, official, false)
	if status != 400 {
		t.Errorf("official+off+no-confirm should 400, got %d", status)
	}
	if code != "ERR_OFFICIAL_NEEDS_MODERATION" {
		t.Errorf("expected ERR_OFFICIAL_NEEDS_MODERATION, got %q", code)
	}

	// 官方 + off + confirm → 放行
	cm = database.ChannelModel{ModerationLevel: "off", ModerationFailMode: "open"}
	status, _, _ = validateChannelModelModeration(&cm, official, true)
	if status != 0 {
		t.Errorf("official+off+confirm should pass, got %d", status)
	}

	// 官方 + moderation + closed → 不需要 confirm（非 off）
	cm = database.ChannelModel{ModerationLevel: "moderation", ModerationFailMode: "closed"}
	status, _, _ = validateChannelModelModeration(&cm, official, false)
	if status != 0 {
		t.Errorf("official+moderation+closed should pass, got %d", status)
	}

	// fix CRITICAL R23-C3：官方 + moderation + open → 拒绝
	cm = database.ChannelModel{ModerationLevel: "moderation", ModerationFailMode: "open"}
	status, code, _ = validateChannelModelModeration(&cm, official, false)
	if status != 400 {
		t.Errorf("official+moderation+open must be rejected, got %d", status)
	}
	if code != "ERR_OFFICIAL_NEEDS_FAIL_CLOSED" {
		t.Errorf("expected ERR_OFFICIAL_NEEDS_FAIL_CLOSED, got %q", code)
	}

	// 官方 + strict + open → 拒绝（同样的 fail-closed 强制）
	cm = database.ChannelModel{ModerationLevel: "strict", ModerationFailMode: "open"}
	status, _, _ = validateChannelModelModeration(&cm, official, false)
	if status != 400 {
		t.Errorf("official+strict+open must be rejected, got %d", status)
	}

	// 官方 + keyword + open → 拒绝
	cm = database.ChannelModel{ModerationLevel: "keyword", ModerationFailMode: "open"}
	status, _, _ = validateChannelModelModeration(&cm, official, false)
	if status != 400 {
		t.Errorf("official+keyword+open must be rejected, got %d", status)
	}

	// 第三方 + moderation + open → 通过（非官方不强制）
	thirdParty := &database.Channel{Type: "openai", BaseURL: "https://relay.example.com"}
	cm = database.ChannelModel{ModerationLevel: "moderation", ModerationFailMode: "open"}
	status, _, _ = validateChannelModelModeration(&cm, thirdParty, false)
	if status != 0 {
		t.Errorf("third-party+moderation+open should pass, got %d", status)
	}
}

// fix MAJOR R23-M9：trailing dot 规范化测试
func TestChannelTargetsOfficialHost_TrailingDot(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"https://api.openai.com./v1", true}, // trailing dot → 应识别为官方
		{"https://API.OpenAI.com.", true},    // trailing dot + 大小写
		{"https://api.openai.com..", false},  // 双 dot 非法
		{"https://api.openai.com/v1", true},  // 标准
	}
	for _, tc := range cases {
		ch := database.Channel{Type: "openai", BaseURL: tc.base}
		if got := channelTargetsOfficialHost(&ch); got != tc.want {
			t.Errorf("base=%q got=%v want=%v", tc.base, got, tc.want)
		}
	}
}

// TestValidateChannelModelModeration_NormalizesValues 校验规范化（去空白 + 小写）
func TestValidateChannelModelModeration_NormalizesValues(t *testing.T) {
	thirdParty := &database.Channel{Type: "openai", BaseURL: "https://relay.example.com"}
	cm := database.ChannelModel{ModerationLevel: "  STRICT ", ModerationFailMode: "  CLOSED  "}
	status, _, _ := validateChannelModelModeration(&cm, thirdParty, false)
	if status != 0 {
		t.Errorf("expected pass after normalize, got %d", status)
	}
	if cm.ModerationLevel != "strict" {
		t.Errorf("level not normalized: %q", cm.ModerationLevel)
	}
	if cm.ModerationFailMode != "closed" {
		t.Errorf("fail_mode not normalized: %q", cm.ModerationFailMode)
	}
}

// TestValidateChannelModelModeration_DefaultsWhenEmpty 空字符串走默认 off/open
func TestValidateChannelModelModeration_DefaultsWhenEmpty(t *testing.T) {
	thirdParty := &database.Channel{Type: "openai", BaseURL: "https://relay.example.com"}
	cm := database.ChannelModel{}
	status, _, _ := validateChannelModelModeration(&cm, thirdParty, false)
	if status != 0 {
		t.Errorf("empty values on third-party should default to off/open and pass, got %d", status)
	}
	if cm.ModerationLevel != "off" {
		t.Errorf("expected default off, got %q", cm.ModerationLevel)
	}
	if cm.ModerationFailMode != "open" {
		t.Errorf("expected default open, got %q", cm.ModerationFailMode)
	}
}

func TestValidateChannelModelModeration_OpenAIModelForcedStrictClosed(t *testing.T) {
	thirdParty := &database.Channel{Type: "openai", BaseURL: "https://relay.example.com"}
	cm := database.ChannelModel{
		ModelID:            "gpt-5.4-mini",
		ModerationLevel:    "off",
		ModerationFailMode: "open",
	}
	status, _, _ := validateChannelModelModeration(&cm, thirdParty, true)
	if status != 0 {
		t.Fatalf("OpenAI-family model should normalize instead of reject, got %d", status)
	}
	if cm.ModerationLevel != database.OpenAIModelModerationLevel || cm.ModerationFailMode != database.OpenAIModelModerationFailMode {
		t.Fatalf("moderation=%s/%s want %s/%s",
			cm.ModerationLevel, cm.ModerationFailMode,
			database.OpenAIModelModerationLevel, database.OpenAIModelModerationFailMode)
	}
}
