package controller

import (
	"encoding/csv"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

func newBillingTestApp(user *database.User) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user", user)
		return c.Next()
	})
	app.Get("/billing/mine", MyBillingEntries)
	app.Get("/billing/mine/summary", MyBillingSummary)
	app.Get("/billing/mine/export", MyBillingExport)
	return app
}

func newAdminBillingTestApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/admin/users/:id/billing", AdminListUserBilling)
	return app
}

func seedBillingEntries(t *testing.T, userID uint, n int) []database.BillingEntry {
	t.Helper()
	rows := make([]database.BillingEntry, 0, n)
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		entry := database.BillingEntry{
			UserID:           userID,
			OccurredAt:       base.Add(time.Duration(i) * time.Minute),
			EntryType:        database.BillingTypeTopup,
			BillingState:     database.BillingStateSettled,
			AmountUSD:        int64(i+1) * database.MicroPerUSD,
			BalanceAfterUSD:  int64(i+1) * database.MicroPerUSD,
			Description:      "seed billing entry " + strconv.Itoa(i+1),
			CurrencyOriginal: "USD",
			AmountOriginal:   int64(i+1) * database.MicroPerUSD,
		}
		if err := database.DB.Create(&entry).Error; err != nil {
			t.Fatalf("seed billing entry %d: %v", i+1, err)
		}
		rows = append(rows, entry)
	}
	return rows
}

func billingIDsFromResponse(t *testing.T, resp map[string]any) []int64 {
	t.Helper()
	raw, ok := resp["data"].([]any)
	if !ok {
		t.Fatalf("response data has type %T, want []any: %#v", resp["data"], resp)
	}
	ids := make([]int64, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("billing row has type %T, want map[string]any", item)
		}
		id, ok := row["id"].(float64)
		if !ok {
			t.Fatalf("billing row id has type %T, want number: %#v", row["id"], row)
		}
		ids = append(ids, int64(id))
	}
	return ids
}

func nextCursorFromResponse(t *testing.T, resp map[string]any) int64 {
	t.Helper()
	cursor, ok := resp["next_cursor"].(float64)
	if !ok {
		t.Fatalf("next_cursor has type %T, want number: %#v", resp["next_cursor"], resp)
	}
	return int64(cursor)
}

func billingListPath(pageSize int, cursor int64) string {
	path := "/billing/mine?page_size=" + strconv.Itoa(pageSize)
	if cursor > 0 {
		path += "&cursor=" + strconv.FormatInt(cursor, 10)
	}
	return path
}

func TestMyBillingEntries_KeysetPagination(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	seeded := seedBillingEntries(t, user.ID, 30)
	app := newBillingTestApp(user)

	code, resp := doJSON(t, app, "GET", billingListPath(10, 0), nil)
	if code != 200 {
		t.Fatalf("first page expected 200, got %d body=%v", code, resp)
	}
	firstIDs := billingIDsFromResponse(t, resp)
	if len(firstIDs) != 10 {
		t.Fatalf("first page len=%d, want 10", len(firstIDs))
	}
	nextCursor := nextCursorFromResponse(t, resp)
	if nextCursor != firstIDs[len(firstIDs)-1] {
		t.Fatalf("next_cursor=%d, want first page last id %d", nextCursor, firstIDs[len(firstIDs)-1])
	}

	code, resp = doJSON(t, app, "GET", billingListPath(10, nextCursor), nil)
	if code != 200 {
		t.Fatalf("second page expected 200, got %d body=%v", code, resp)
	}
	secondIDs := billingIDsFromResponse(t, resp)
	if len(secondIDs) != 10 {
		t.Fatalf("second page len=%d, want 10", len(secondIDs))
	}

	seen := map[int64]bool{}
	got := append(append([]int64{}, firstIDs...), secondIDs...)
	for _, id := range got {
		if seen[id] {
			t.Fatalf("duplicate billing id across pages: %d", id)
		}
		seen[id] = true
	}
	for i, id := range got {
		want := int64(seeded[len(seeded)-1-i].ID)
		if id != want {
			t.Fatalf("page item %d id=%d, want %d", i, id, want)
		}
	}
}

func TestMyBillingEntries_NoMorePagesReturnsZeroCursor(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	seedBillingEntries(t, user.ID, 15)
	app := newBillingTestApp(user)

	code, resp := doJSON(t, app, "GET", billingListPath(10, 0), nil)
	if code != 200 {
		t.Fatalf("first page expected 200, got %d body=%v", code, resp)
	}
	cursor := nextCursorFromResponse(t, resp)
	if cursor == 0 {
		t.Fatalf("first page next_cursor=0, want another page")
	}

	code, resp = doJSON(t, app, "GET", billingListPath(10, cursor), nil)
	if code != 200 {
		t.Fatalf("last page expected 200, got %d body=%v", code, resp)
	}
	ids := billingIDsFromResponse(t, resp)
	if len(ids) != 5 {
		t.Fatalf("last page len=%d, want 5", len(ids))
	}
	if got := nextCursorFromResponse(t, resp); got != 0 {
		t.Fatalf("last page next_cursor=%d, want 0", got)
	}
}

func TestMyBillingExport_StreamsLargeDataset(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	seedBillingEntries(t, user.ID, 1500)
	app := newBillingTestApp(user)

	req := httptest.NewRequest("GET", "/billing/mine/export", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test export: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("export expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read export body: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(body), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 1501 {
		t.Fatalf("csv record count=%d, want 1501 including header", len(records))
	}
	if got := records[0][0]; got != "发生时间" {
		t.Fatalf("csv header[0]=%q, want 发生时间", got)
	}
}

func TestAdminListBilling_IncludesIsReconciled(t *testing.T) {
	setupSubTestDB(t)
	user := seedTestUser(t, 100)
	rows := seedBillingEntries(t, user.ID, 2)
	if err := database.DB.Create(&database.BillingReconciliation{
		BillingEntryID: rows[1].ID,
		Result:         database.ReconcileResultAbsorbed,
		OperatorID:     1,
		OperatorRole:   "admin",
		Note:           "absorbed in test",
	}).Error; err != nil {
		t.Fatalf("create reconciliation: %v", err)
	}

	app := newAdminBillingTestApp()
	code, resp := doJSON(t, app, "GET", "/admin/users/"+itoaUint(user.ID)+"/billing?page_size=10", nil)
	if code != 200 {
		t.Fatalf("expected 200 got %d body=%v", code, resp)
	}
	data, ok := resp["data"].([]any)
	if !ok || len(data) == 0 {
		t.Fatalf("response data=%#v, want non-empty []any", resp["data"])
	}
	first, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("first row type=%T", data[0])
	}
	if first["is_reconciled"] != true {
		t.Fatalf("is_reconciled=%v, want true in newest reconciled row", first["is_reconciled"])
	}
	if first["reconcile_result"] != database.ReconcileResultAbsorbed {
		t.Fatalf("reconcile_result=%v, want %s", first["reconcile_result"], database.ReconcileResultAbsorbed)
	}
}
