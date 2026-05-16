package controller

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
)

const (
	actionCircuitForceReset = "CIRCUIT_FORCE_RESET"
	// 直接使用常量字面量，i18n 覆盖测试可通过 AST 扫描捕获，避免遗漏翻译。
	messageCodeCircuitResetSuccess   = "SUCCESS_CIRCUIT_RESET"
	messageCodeCircuitResetAuditFail = "ERR_CIRCUIT_RESET_AUDIT_FAILED"
)

type channelCircuitAdminRow struct {
	ChannelID           uint       `json:"channel_id"`
	ChannelName         string     `json:"channel_name"`
	ChannelType         string     `json:"channel_type"`
	BaseURL             string     `json:"base_url"`
	State               string     `json:"state"`
	ConsecutiveFailures int32      `json:"consecutive_failures"`
	CurrentCooldownSec  int64      `json:"current_cooldown_sec"`
	OpenUntil           *time.Time `json:"open_until"`
}

// AdminListChannelCircuits returns in-memory per-channel circuit breaker state for admin monitoring.
func AdminListChannelCircuits(c *fiber.Ctx) error {
	snaps := proxy.GetChannelCircuitSnapshot()
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].ChannelID < snaps[j].ChannelID
	})

	rows := make([]channelCircuitAdminRow, 0, len(snaps))
	for _, snap := range snaps {
		row := channelCircuitAdminRow{
			ChannelID:           snap.ChannelID,
			ChannelName:         "unknown_channel",
			State:               snap.State,
			ConsecutiveFailures: snap.ConsecutiveFailures,
			CurrentCooldownSec:  snap.CurrentCooldownSec,
			OpenUntil:           snap.OpenUntil,
		}
		if ch := proxy.ChannelMapCache[snap.ChannelID]; ch != nil {
			row.ChannelName = ch.Name
			row.ChannelType = ch.Type
			row.BaseURL = ch.BaseURL
		}
		rows = append(rows, row)
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    rows,
	})
}

// AdminForceResetChannelCircuit force-closes one channel's circuit breaker state and writes an audit log.
func AdminForceResetChannelCircuit(c *fiber.Ctx) error {
	rawID := c.Params("id")
	parsedID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil || parsedID == 0 {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "Invalid channel ID parameter",
		})
	}
	channelID := uint(parsedID)

	proxy.ForceCloseChannelCircuit(channelID)

	details, _ := json.Marshal(fiber.Map{
		"action":     actionCircuitForceReset,
		"channel_id": channelID,
	})
	if err := LogOperationByTx(database.DB, getOperatorID(c), 0, "admin", actionCircuitForceReset, c.IP(), string(details)); err != nil {
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": messageCodeCircuitResetAuditFail,
			"message":      "Failed to write circuit reset audit log",
		})
	}

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": messageCodeCircuitResetSuccess,
	})
}
