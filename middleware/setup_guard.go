package middleware

import (
	"sync"
	"sync/atomic"
	"time"

	"daof-ai-hub/database"
	"daof-ai-hub/utils"

	"github.com/gofiber/fiber/v2"
)

// setupStateCache 缓存 "是否处于初始 setup 态"，避免每个请求都跑 bcrypt（~70-250ms）。
// 状态变迁：
//   - 启动后第一次访问触发一次 DB 查询 + bcrypt
//   - 之后 30s 内复用缓存
//   - admin 修改密码后调 InvalidateSetupGuardCache() 立刻失效
//
// 0 = 未知，1 = 已 setup，2 = 处于初始态
//
// 慢路径用 setupRefreshMu 互斥，防止冷启动时 N 个并发请求各跑一次 bcrypt（race detector 也会报）。
var (
	setupCacheState  atomic.Int32
	setupCacheExpire atomic.Int64 // unix nano
	setupRefreshMu   sync.Mutex
)

const setupCacheTTL = 30 * time.Second

// InvalidateSetupGuardCache 在管理员修改用户名/密码后调用，确保下次请求重新评估
func InvalidateSetupGuardCache() {
	setupCacheState.Store(0)
	setupCacheExpire.Store(0)
}

// SetupGuard 拒绝公开访问直到管理员把默认 root/123456 改掉。
// 返回 503 + setup_required=true 给前端展示引导。
func SetupGuard(c *fiber.Ctx) error {
	if state, ok := readSetupState(); ok {
		if state == 2 {
			return blockSetup(c)
		}
		return c.Next()
	}

	// 慢路径：拿互斥锁，再次 double-check（避免重复 bcrypt）
	setupRefreshMu.Lock()
	defer setupRefreshMu.Unlock()
	if state, ok := readSetupState(); ok {
		if state == 2 {
			return blockSetup(c)
		}
		return c.Next()
	}

	var admin database.User
	if err := database.DB.Where("role = ?", "admin").First(&admin).Error; err != nil {
		return c.Status(500).SendString("Fatal: System integrity compromised. No administrators found.")
	}

	isInitial := admin.Username == "root" && utils.CheckHash("123456", admin.PasswordHash)
	if isInitial {
		setupCacheState.Store(2)
	} else {
		setupCacheState.Store(1)
	}
	setupCacheExpire.Store(time.Now().Add(setupCacheTTL).UnixNano())

	if isInitial {
		return blockSetup(c)
	}
	return c.Next()
}

// readSetupState 返回 (state, valid)。state 仅在 valid=true 时有意义。
func readSetupState() (int32, bool) {
	state := setupCacheState.Load()
	if state == 0 {
		return 0, false
	}
	if time.Now().UnixNano() >= setupCacheExpire.Load() {
		return 0, false
	}
	return state, true
}

func blockSetup(c *fiber.Ctx) error {
	return c.Status(503).JSON(fiber.Map{
		"success":        false,
		"setup_required": true,
		"message":        "服务未就绪(503 Service Unavailable)",
		"message_code":   "ERR_SYSTEM_NOT_READY",
	})
}
