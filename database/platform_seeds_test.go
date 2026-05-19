package database

import (
	"testing"

	"daof-cpa/utils"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSeedPlatformDefaults_InsertsMissingAndPreservesExisting(t *testing.T) {
	utils.InitCrypto()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=private"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	oldDB := DB
	DB = db
	t.Cleanup(func() { DB = oldDB })

	if err := DB.AutoMigrate(&SysConfig{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	insertEncryptedSysConfigForPlatformSeedTest(t, "reg_ip_limit", "1")
	insertEncryptedSysConfigForPlatformSeedTest(t, "cliproxy_url", "http://localhost:8317")

	SeedPlatformDefaults()
	SeedPlatformDefaults()

	var count int64
	if err := DB.Model(&SysConfig{}).Count(&count).Error; err != nil {
		t.Fatalf("count sysconfig: %v", err)
	}
	if count != int64(len(PlatformSysConfigDefaults)) {
		t.Fatalf("sysconfig count=%d want %d", count, len(PlatformSysConfigDefaults))
	}

	if got := readDecryptedSysConfigForPlatformSeedTest(t, "reg_ip_limit"); got != "1" {
		t.Fatalf("reg_ip_limit was overwritten: %q", got)
	}
	if got := readDecryptedSysConfigForPlatformSeedTest(t, "cliproxy_url"); got != "http://localhost:8317" {
		t.Fatalf("cliproxy_url was overwritten: %q", got)
	}

	expect := map[string]string{
		"server_address":                           "",
		"github_client_secret":                     "",
		"aliyun_access_secret":                     "",
		"moderation_cliproxy_api_key":              "",
		"reg_strategy":                             "dynamic",
		"signup_bonus":                             "1000000",
		"credits_refresh_interval":                 "15",
		"credits_retry_interval":                   "5",
		"cpa_project_id_refresh_seconds":           "86400",
		"credits_shrink_abort_threshold_pct":       "50",
		"channel_circuit_open_threshold":           "5",
		"channel_circuit_initial_cooldown_seconds": "30",
		"channel_circuit_max_cooldown_seconds":     "300",
		"stream_scanner_buffer_bytes":              "4194304",
		"subscription_cache_max_users":             "50000",
	}
	for key, want := range expect {
		if got := readDecryptedSysConfigForPlatformSeedTest(t, key); got != want {
			t.Fatalf("%s=%q want %q", key, got, want)
		}
	}
}

func insertEncryptedSysConfigForPlatformSeedTest(t *testing.T, key, value string) {
	t.Helper()
	encrypted, err := utils.Encrypt(value)
	if err != nil {
		t.Fatalf("encrypt %s: %v", key, err)
	}
	if err := DB.Create(&SysConfig{Key: key, Value: encrypted}).Error; err != nil {
		t.Fatalf("insert %s: %v", key, err)
	}
}

func readDecryptedSysConfigForPlatformSeedTest(t *testing.T, key string) string {
	t.Helper()
	var row SysConfig
	if err := DB.Where("key = ?", key).First(&row).Error; err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	value, err := utils.Decrypt(row.Value)
	if err != nil {
		t.Fatalf("decrypt %s: %v", key, err)
	}
	return value
}
