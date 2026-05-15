package controller

import (
	"encoding/json"
	"net/http"
	"testing"

	"daof-ai-hub/database"
	"daof-ai-hub/proxy"

	"github.com/gofiber/fiber/v2"
)

type channelCircuitAdminTestResponse struct {
	Success     bool                         `json:"success"`
	Data        []channelCircuitAdminTestRow `json:"data"`
	MessageCode string                       `json:"message_code"`
}

type channelCircuitAdminTestRow struct {
	ChannelID           uint    `json:"channel_id"`
	ChannelName         string  `json:"channel_name"`
	ChannelType         string  `json:"channel_type"`
	BaseURL             string  `json:"base_url"`
	State               string  `json:"state"`
	ConsecutiveFailures int32   `json:"consecutive_failures"`
	CurrentCooldownSec  int64   `json:"current_cooldown_sec"`
	OpenUntil           *string `json:"open_until"`
}

func setupChannelCircuitAdminTestApp() *fiber.App {
	app := initializeMegaTestDB()
	app.Get("/api/admin/channels/circuits", AdminListChannelCircuits)
	app.Post("/api/admin/channels/:id/circuit-reset", func(c *fiber.Ctx) error {
		c.Locals("admin_user_id", uint(1))
		return AdminForceResetChannelCircuit(c)
	})
	return app
}

func seedChannelCircuitAdminChannel(t *testing.T, id uint, name, channelType, baseURL string) {
	t.Helper()
	if err := database.DB.Create(&database.Channel{
		ID:      id,
		Name:    name,
		Type:    channelType,
		Key:     "sk-test",
		BaseURL: baseURL,
		Status:  1,
		Weight:  1,
	}).Error; err != nil {
		t.Fatalf("seed channel %d: %v", id, err)
	}
	proxy.SyncCacheConfig()
}

func openChannelCircuitForTest(channelID uint) {
	for i := 0; i < 5; i++ {
		proxy.MarkChannelFailure(channelID, http.StatusInternalServerError)
	}
}

func findChannelCircuitAdminRow(t *testing.T, rows channelCircuitAdminTestResponse, channelID uint) (channelCircuitAdminTestRow, bool) {
	t.Helper()
	for _, row := range rows.Data {
		if row.ChannelID == channelID {
			return row, true
		}
	}
	return channelCircuitAdminTestRow{}, false
}

func TestAdminListChannelCircuits_ReturnsSnapshot(t *testing.T) {
	app := setupChannelCircuitAdminTestApp()
	const openID uint = 60101
	const closedID uint = 60102
	const unknownID uint = 60103

	seedChannelCircuitAdminChannel(t, openID, "primary-openai", "openai", "https://api.openai.example")
	seedChannelCircuitAdminChannel(t, closedID, "backup-claude", "anthropic", "https://api.anthropic.example")

	openChannelCircuitForTest(openID)
	proxy.MarkChannelFailure(closedID, http.StatusBadGateway)
	proxy.MarkChannelFailure(unknownID, http.StatusServiceUnavailable)

	resp := sendRequest(app, http.MethodGet, "/api/admin/channels/circuits", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload channelCircuitAdminTestResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Success {
		t.Fatalf("success=false payload: %#v", payload)
	}

	openRow, ok := findChannelCircuitAdminRow(t, payload, openID)
	if !ok {
		t.Fatalf("missing open channel row %d", openID)
	}
	if openRow.ChannelName != "primary-openai" || openRow.ChannelType != "openai" || openRow.BaseURL != "https://api.openai.example" {
		t.Fatalf("unexpected open channel metadata: %#v", openRow)
	}
	if openRow.State != "open" || openRow.ConsecutiveFailures != 5 || openRow.CurrentCooldownSec != 30 || openRow.OpenUntil == nil {
		t.Fatalf("unexpected open channel circuit state: %#v", openRow)
	}

	closedRow, ok := findChannelCircuitAdminRow(t, payload, closedID)
	if !ok {
		t.Fatalf("missing closed channel row %d", closedID)
	}
	if closedRow.State != "closed" || closedRow.ConsecutiveFailures != 1 || closedRow.OpenUntil != nil {
		t.Fatalf("unexpected closed channel circuit state: %#v", closedRow)
	}

	unknownRow, ok := findChannelCircuitAdminRow(t, payload, unknownID)
	if !ok {
		t.Fatalf("missing unknown channel row %d", unknownID)
	}
	if unknownRow.ChannelName != "unknown_channel" {
		t.Fatalf("unknown channel should be marked unknown_channel, got %#v", unknownRow)
	}
}

func TestAdminForceResetChannelCircuit_ResetsState(t *testing.T) {
	app := setupChannelCircuitAdminTestApp()
	const channelID uint = 60201

	seedChannelCircuitAdminChannel(t, channelID, "reset-target", "openai", "https://reset.example")
	openChannelCircuitForTest(channelID)
	if !proxy.IsChannelCircuitOpen(channelID) {
		t.Fatalf("setup: expected channel circuit open")
	}

	resp := sendRequest(app, http.MethodPost, "/api/admin/channels/60201/circuit-reset", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var payload channelCircuitAdminTestResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Success || payload.MessageCode != "SUCCESS_CIRCUIT_RESET" {
		t.Fatalf("unexpected response: %#v", payload)
	}

	snaps := proxy.GetChannelCircuitSnapshot()
	stateByID := make(map[uint]string, len(snaps))
	for _, snap := range snaps {
		stateByID[snap.ChannelID] = snap.State
	}
	if stateByID[channelID] != "closed" {
		t.Fatalf("state after force reset = %q, want closed", stateByID[channelID])
	}

	var count int64
	if err := database.DB.Model(&database.OperationLog{}).
		Where("action_type = ?", actionCircuitForceReset).
		Count(&count).Error; err != nil {
		t.Fatalf("count operation logs: %v", err)
	}
	if count != 1 {
		t.Fatalf("operation log count for %s = %d, want 1", actionCircuitForceReset, count)
	}
}
