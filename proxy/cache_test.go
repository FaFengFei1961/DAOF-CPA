package proxy

import (
	"daof-cpa/database"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSyncCacheConfig(t *testing.T) {
	// Setup DB
	var err error
	database.DB, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	database.DB.AutoMigrate(&database.User{}, &database.Channel{}, &database.ChannelModel{}, &database.SysConfig{}, &database.AccessToken{})

	database.DB.Exec("DELETE FROM users")
	database.DB.Exec("DELETE FROM channels")
	database.DB.Exec("DELETE FROM channel_models")
	database.DB.Exec("DELETE FROM sys_configs")
	database.DB.Exec("DELETE FROM access_tokens")

	// Insert mock data；BaseURL 必须通过 ValidateChannelURL（http/https + 有 host），
	// 否则会被 SyncCacheConfig 隔离不进缓存
	database.DB.Create(&database.Channel{ID: 1, Type: "openai", BaseURL: "https://example.com", Key: "sk-c", Status: 1})
	database.DB.Create(&database.ChannelModel{
		ChannelID:               1,
		ModelID:                 "gpt-mock",
		Status:                  1,
		InputPricePicoPerToken:  database.PicoPerTokenPerUSDPerMTok,
		OutputPricePicoPerToken: database.PicoPerTokenPerUSDPerMTok,
		ModerationFailMode:      "closed",
		ModerationLevel:         "moderation",
	})
	database.DB.Create(&database.User{ID: 1, Username: "cachetest", Token: "parent-token", Status: 1})
	database.DB.Create(&database.AccessToken{UserID: 1, Key: "child-token", Status: 1})
	// Sys config skips encrypt for fast test
	database.DB.Create(&database.SysConfig{Key: "theme", Value: "light"})

	SyncCacheConfig()

	// Verify Auth
	authSnapshotMutex.RLock()
	parentUser, ok1 := AuthCache["parent-token"]
	childUser, ok2 := AuthCache["child-token"]
	authSnapshotMutex.RUnlock()

	if !ok1 || parentUser.ID != 1 {
		t.Errorf("AuthCache parent token failed")
	}
	if !ok2 || childUser.ID != 1 {
		t.Errorf("AuthCache child token failed")
	}

	// Verify Map/Routes
	gatewayMutex.RLock()
	_, chOk := ChannelMapCache[1]
	gatewayMutex.RUnlock()
	if !chOk {
		t.Errorf("ChannelMapCache failed")
	}

	gatewayMutex.RLock()
	routes, rOk := RouteCache["gpt-mock"]
	gatewayMutex.RUnlock()
	if !rOk || len(routes) == 0 {
		t.Errorf("RouteCache failed")
	}
}
