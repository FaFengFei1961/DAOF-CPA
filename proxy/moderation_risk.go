package proxy

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"

	"gorm.io/gorm"
)

const (
	ActionModerationBlockKeyword     = "MODERATION_BLOCK_KEYWORD"
	ActionModerationBlockRiskRule    = "MODERATION_BLOCK_RISK_RULE"
	ActionModerationRiskScore        = "MODERATION_RISK_SCORE"
	ActionModerationBlockPolicy      = "MODERATION_BLOCK_POLICY"
	ActionModerationBlockImagePolicy = "MODERATION_BLOCK_IMAGE_POLICY"
	ActionModerationBlockOversize    = "MODERATION_BLOCK_OVERSIZE"
	ActionSecurityAutoban            = "SECURITY_AUTOBAN"
)

type moderationAutobanConfig struct {
	Enabled            bool
	WindowSeconds      int
	KeywordThreshold   int
	RiskRuleThreshold  int
	RiskScoreThreshold int
	PolicyThreshold    int
	ImageThreshold     int
	OversizeThreshold  int
}

func loadModerationAutobanConfig() moderationAutobanConfig {
	SysConfigMutex.RLock()
	defer SysConfigMutex.RUnlock()

	getBool := func(k string, def bool) bool {
		v := strings.ToLower(strings.TrimSpace(SysConfigCache[k]))
		if v == "" {
			return def
		}
		switch v {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		default:
			return def
		}
	}
	getInt := func(k string, def int) int {
		v := strings.TrimSpace(SysConfigCache[k])
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return def
		}
		return n
	}

	// fix CRITICAL Sprint2-M7：autoban 默认阈值从 1 提升到 3，避免一次命中即封号误判。
	// 单次关键词/规则命中可能是合法学术讨论 / 测试 prompt，不应直接封禁正常用户。
	// admin 可在 SysConfig 显式降低阈值用于严格场景（如安全合规高危关键词）。
	// 阈值=0 仍表示禁用该类型 autoban（保持原语义）。
	cfg := moderationAutobanConfig{
		Enabled:            getBool("moderation_autoban_enabled", false),
		WindowSeconds:      getInt("moderation_autoban_window_seconds", 86400),
		KeywordThreshold:   getInt("moderation_autoban_keyword_threshold", 3),
		RiskRuleThreshold:  getInt("moderation_autoban_risk_rule_threshold", 3),
		RiskScoreThreshold: getInt("moderation_autoban_risk_score_threshold", 0),
		PolicyThreshold:    getInt("moderation_autoban_policy_threshold", 0),
		ImageThreshold:     getInt("moderation_autoban_image_threshold", 2),
		OversizeThreshold:  getInt("moderation_autoban_oversize_threshold", 0),
	}
	if cfg.WindowSeconds < 60 {
		cfg.WindowSeconds = 60
	}
	return cfg
}

func moderationActionThreshold(cfg moderationAutobanConfig, action string) (label string, threshold int, ok bool) {
	switch action {
	case ActionModerationBlockKeyword:
		return "keyword", cfg.KeywordThreshold, true
	case ActionModerationBlockRiskRule:
		return "risk_rule", cfg.RiskRuleThreshold, true
	case ActionModerationRiskScore:
		return "risk_score", cfg.RiskScoreThreshold, true
	case ActionModerationBlockPolicy:
		return "policy", cfg.PolicyThreshold, true
	case ActionModerationBlockImagePolicy:
		return "image_policy", cfg.ImageThreshold, true
	case ActionModerationBlockOversize:
		return "oversize", cfg.OversizeThreshold, true
	default:
		return "", 0, false
	}
}

func handleModerationRiskAfterAudit(evt ModerationAuditEvent, auditID uint) {
	if database.DB == nil || evt.UserID == 0 {
		return
	}
	cfg := loadModerationAutobanConfig()
	if !cfg.Enabled {
		return
	}
	label, threshold, ok := moderationActionThreshold(cfg, evt.ActionType)
	if !ok || threshold <= 0 {
		return
	}

	cutoff := time.Now().Add(-time.Duration(cfg.WindowSeconds) * time.Second)
	var hitCount int64
	if err := database.DB.Model(&database.OperationLog{}).
		Where("target_user_id = ? AND action_type = ? AND created_at >= ?", evt.UserID, evt.ActionType, cutoff).
		Count(&hitCount).Error; err != nil {
		log.Printf("[MODERATION-AUTOBAN] count failed user=%d action=%s err=%v", evt.UserID, evt.ActionType, err)
		return
	}
	if hitCount < int64(threshold) {
		return
	}

	reason := fmt.Sprintf("自动风控封禁：%d 秒内 %s 命中 %d/%d", cfg.WindowSeconds, label, hitCount, threshold)
	details, _ := json.Marshal(map[string]any{
		"action":           ActionSecurityAutoban,
		"trigger_action":   evt.ActionType,
		"trigger_audit_id": auditID,
		"trigger_model":    evt.ModelName,
		"trigger_reason":   evt.Reason,
		"trigger_keyword":  evt.Keyword,
		"highest_score":    evt.HighestScore,
		"hit_count":        hitCount,
		"threshold":        threshold,
		"window_seconds":   cfg.WindowSeconds,
		"ban_reason":       reason,
		"auto_ban_group":   label,
	})

	banned := false
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&database.User{}).
			Where("id = ? AND status = ? AND role <> ?", evt.UserID, 1, "admin").
			Updates(map[string]any{
				"status":     2,
				"ban_reason": reason,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		banned = true
		return tx.Create(&database.OperationLog{
			TargetUserID: evt.UserID,
			OperatorID:   0,
			OperatorRole: "system",
			ActionType:   ActionSecurityAutoban,
			IPAddress:    evt.IPAddress,
			Details:      string(details),
			CreatedAt:    time.Now(),
		}).Error
	})
	if err != nil {
		log.Printf("[MODERATION-AUTOBAN] tx failed user=%d action=%s err=%v", evt.UserID, evt.ActionType, err)
		return
	}
	if !banned {
		return
	}

	RefreshUserAuth(evt.UserID)
	dedupKey := fmt.Sprintf("moderation-autoban:%d:%d", evt.UserID, auditID)
	Dispatch(evt.UserID, "security", "error",
		"您的账户已被自动限制",
		reason+"。如认为这是误判，请提交工单。",
		"", "", "user", evt.UserID, &dedupKey)
	log.Printf("[MODERATION-AUTOBAN] user=%d action=%s count=%d threshold=%d audit=%d",
		evt.UserID, evt.ActionType, hitCount, threshold, auditID)
}
