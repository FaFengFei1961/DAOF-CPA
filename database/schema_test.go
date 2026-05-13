package database

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCacheBillingColumnNames(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&ChannelModel{}, &ApiLog{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	channelModelColumns := sqliteColumnSet(t, db, "channel_models")
	if !channelModelColumns["cache_write_1h_input_price"] {
		t.Fatalf("channel_models missing cache_write_1h_input_price: %#v", channelModelColumns)
	}
	if channelModelColumns["cache_write1h_input_price"] {
		t.Fatalf("channel_models should not create legacy cache_write1h_input_price")
	}

	apiLogColumns := sqliteColumnSet(t, db, "api_logs")
	for _, name := range []string{"cache_write_5m_tokens", "cache_write_1h_tokens"} {
		if !apiLogColumns[name] {
			t.Fatalf("api_logs missing %s: %#v", name, apiLogColumns)
		}
	}
	for _, name := range []string{"cache_write5m_tokens", "cache_write1h_tokens"} {
		if apiLogColumns[name] {
			t.Fatalf("api_logs should not create legacy %s", name)
		}
	}
}

func sqliteColumnSet(t *testing.T, db *gorm.DB, table string) map[string]bool {
	t.Helper()
	var rows []struct {
		Name string
	}
	if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&rows).Error; err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		out[row.Name] = true
	}
	return out
}
