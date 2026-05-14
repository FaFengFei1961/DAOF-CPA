package controller

import (
	"strings"
	"testing"

	"daof-ai-hub/database"
)

func TestPublicBillingDescriptionHidesInternalAdminDetails(t *testing.T) {
	row := database.BillingEntry{
		EntryType:   database.BillingTypeAdminAdjust,
		AmountUSD:   49 * database.MicroPerUSD,
		Description: `管理员调整额度 · admin#1 · [{"new":49,"new_micro":49000000,"old":0,"old_micro":0,"target":"FaFengFei1961","type":"QUOTA"}]`,
	}
	got := publicBillingDescription(row)
	if got != "管理员调整额度 · 余额增加 $49.00" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "new_micro") || strings.Contains(got, "admin#") || strings.Contains(got, "[{") {
		t.Fatalf("description still leaks internals: %q", got)
	}
}

func TestStripInternalBillingFragments(t *testing.T) {
	got := stripInternalBillingFragments("管理员赠送「Combo Pro」#1 · admin#7 · 内测账户")
	if got != "管理员赠送「Combo Pro」#1 · 内测账户" {
		t.Fatalf("got %q", got)
	}
}
