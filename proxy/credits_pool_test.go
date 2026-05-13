package proxy

import "testing"

func TestClaudeBuildWindowTreatsUtilizationAsUsedPercent(t *testing.T) {
	def := claudeWindowDef{Key: "five_hour", ID: "five-hour", Label: "5 小时限额"}
	w := claudeBuildWindow(def, map[string]any{"utilization": 2.0})
	if w.UsedPercent != 2 || w.RemainingPercent != 98 {
		t.Fatalf("expected Claude utilization to mean 2%% used, got used=%.1f remaining=%.1f", w.UsedPercent, w.RemainingPercent)
	}

	w = claudeBuildWindow(def, map[string]any{"utilization": 76.0})
	if w.UsedPercent != 76 || w.RemainingPercent != 24 {
		t.Fatalf("expected Claude utilization to mean 76%% used, got used=%.1f remaining=%.1f", w.UsedPercent, w.RemainingPercent)
	}

	w = claudeBuildWindow(def, map[string]any{"utilization": 0.06})
	if w.UsedPercent != 6 || w.RemainingPercent != 94 {
		t.Fatalf("expected fractional utilization to be accepted as 6%% used, got used=%.1f remaining=%.1f", w.UsedPercent, w.RemainingPercent)
	}
}

func TestComputeHealthyTrustsFreshQuotaWindowsOverStaleStatus(t *testing.T) {
	entry := &CreditEntry{
		Provider: "claude",
		Status:   "error",
		Windows:  []CreditWindow{{ID: "five-hour", RemainingPercent: 63}},
	}

	if !computeHealthy(entry) {
		t.Fatal("expected fresh quota windows with remaining capacity to be healthy even when CPA status is stale error")
	}
}

func TestComputeHealthyKeepsDisabledAndExhaustedUnhealthy(t *testing.T) {
	t.Run("disabled status wins", func(t *testing.T) {
		entry := &CreditEntry{
			Provider: "claude",
			Status:   "disabled",
			Windows:  []CreditWindow{{ID: "five-hour", RemainingPercent: 63}},
		}
		if computeHealthy(entry) {
			t.Fatal("expected disabled credential to stay unhealthy")
		}
	})

	t.Run("all quota windows exhausted", func(t *testing.T) {
		entry := &CreditEntry{
			Provider: "claude",
			Status:   "active",
			Windows: []CreditWindow{
				{ID: "five-hour", RemainingPercent: 5},
				{ID: "weekly", RemainingPercent: 2},
			},
		}
		if computeHealthy(entry) {
			t.Fatal("expected credential with no window above threshold to be unhealthy")
		}
	})

	t.Run("empty windows are not healthy unless provider is allowlisted", func(t *testing.T) {
		entry := &CreditEntry{Provider: "claude", Status: "active"}
		if computeHealthy(entry) {
			t.Fatal("expected empty numeric windows to be unhealthy")
		}
	})

	t.Run("last error wins", func(t *testing.T) {
		entry := &CreditEntry{
			Provider:  "claude",
			Status:    "active",
			LastError: "quota fetch failed",
			Windows:   []CreditWindow{{ID: "five-hour", RemainingPercent: 63}},
		}
		if computeHealthy(entry) {
			t.Fatal("expected fetch error to be unhealthy")
		}
	})
}
