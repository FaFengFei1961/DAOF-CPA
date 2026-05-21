// Package controller / user_oauth_identities.go
//
// 用户视角的 OAuth identity 管理 API。Phase H-5（2026-05-20）。
//
// 路由（均挂 UserGuard + CSRFGuard for writes）：
//   - GET  /api/user/oauth/identities                  列出当前用户的活跃 OAuth 绑定
//   - POST /api/user/oauth/:provider/link/prepare      申请新 link：返回 state + code_challenge
//                                                      (后续走 /oauth/{provider} 跳转 + /api/auth/oauth/{provider}/callback)
//   - POST /api/user/oauth/:provider/unlink            解绑（软删 + auth method 安全检查）
//
// 安全约束（"至少保留一个 auth method"）：
//   一个 user 必须始终持有以下至少一种凭据：
//     (a) 至少一条 active OAuth identity
//     (b) 邮箱 + 密码 (email != "" && email_verified_at != nil && password_hash != "")
//     (c) 手机号 (phone != "")
//   解绑会让 user 失去最后一个凭据时拒绝；返回 ERR_CANNOT_UNLINK_LAST_AUTH。
package controller

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// GetMyOAuthIdentities GET /api/user/oauth/identities
// 返回当前用户的活跃绑定列表。
func GetMyOAuthIdentities(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	identities, err := lookupOAuthIdentitiesForUser(user.ID)
	if err != nil {
		log.Printf("[OAUTH-IDS] list failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	// fix H-Audit M4（2026-05-20）：用户侧 API 不再回显 email_at_link / username_at_link。
	// 这两个字段是 link 时刻的"快照"——provider 侧改名 / 改邮箱后 DAOF 不会回拉，
	// 用户面 UI 只需要 provider + linked_at + 一个稳定的不可枚举 external_id 提示即可。
	// Admin 端审计需要可保留完整字段，因此这里仅约束 user 面。
	out := make([]fiber.Map, 0, len(identities))
	for _, id := range identities {
		out = append(out, fiber.Map{
			"provider":    id.Provider,
			"external_id": id.ExternalID,
			"linked_at":   id.LinkedAt,
		})
	}
	return c.JSON(fiber.Map{
		"success":    true,
		"identities": out,
	})
}

// PrepareOAuthLink POST /api/user/oauth/:provider/link/prepare
// 让已登录用户开启"link 新 provider"流程。
//
//   1. 校验 :provider 已注册且已 IsConfigured
//   2. 校验该用户尚未 link 过此 provider（一个 user 同 provider 最多一条 active identity）
//   3. 生成 state + code_verifier，存 state 时记上 LinkUserID = user.ID
//   4. 返回 state + code_challenge 给前端
//
// 前端拿到后跳转 https://{provider}/oauth/authorize?redirect_uri={server}/oauth/{provider}&...
// 跳回后 OAuthCallback 会读 state.LinkUserID 走 link-to-existing-user 分支。
func PrepareOAuthLink(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	providerKey := strings.ToLower(strings.TrimSpace(c.Params("provider")))
	if providerKey == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_PROVIDER_UNKNOWN"})
	}
	provider, ok := GetOAuthProvider(providerKey)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_PROVIDER_UNKNOWN"})
	}
	if !provider.IsConfigured() {
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_PROVIDER_NOT_CONFIGURED", "provider": providerKey})
	}

	// 一个 user 同 provider 最多一条 active identity
	already, err := hasActiveOAuthIdentity(user.ID, providerKey)
	if err != nil {
		log.Printf("[OAUTH-LINK] check existing failed user=%d provider=%s: %v", user.ID, providerKey, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if already {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_OAUTH_PROVIDER_ALREADY_LINKED",
			"message":      "已绑定该第三方账号，请先解绑再切换",
			"provider":     providerKey,
		})
	}

	// fix H-Audit M11（2026-05-20）：state 生成走共享 issueOAuthState helper。
	stateValue, challenge, done := issueOAuthState(c, user.ID, "link")
	if done {
		return nil
	}
	// fix H-Audit M8：补 OAUTH_LINK_PREPARE 审计日志。原版本只对完成 link 写
	// OAUTH_LINK，prepare 阶段静默——用户中途放弃 / 攻击者反复探测 state store 无迹可循。
	LogOperationBy(user.ID, user.ID, "user", "OAUTH_LINK_PREPARE", c.IP(),
		fmt.Sprintf(`[{"type":"OAUTH_LINK_PREPARE","provider":%q}]`, providerKey))
	// fix H-Audit M2：不下发 link_user_id（防泄露内部 ID）。
	return c.JSON(fiber.Map{
		"success":               true,
		"state":                 stateValue,
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
		"provider":              providerKey,
	})
}

// UnlinkMyOAuthIdentity POST /api/user/oauth/:provider/unlink
// 解绑一条 OAuth identity（软删 unlinked_at）。
//
// 安全：必须保证用户至少保留一个 auth method（防完全失联）。
//
// fix HIGH H-Audit H-2（2026-05-20）：原版本 "userHasOtherAuthMethod check → Update
// unlinked_at" 两步无事务，双 tab 并发解绑可让两个 unlink 都通过检查 → 账号 stranded。
// 现在把"找 row + 检查 + 软删 + 审计"全部合并到 database.DB.Transaction，
// SQLite 串行写下保证检查 + 写入原子。
//
// 注意：proxy.RefreshUserAuth + LogOperationBy 在 tx 外面（前者写内存，后者写另一张表）
// —— 见 fix H-Audit M7：log 必须在 tx 提交后立即写，再 refresh，否则 cache 修正时
// 审计可能尚未持久化。
type unlinkOutcome struct {
	row     database.OAuthIdentity
	skipped bool   // not found
	blocked bool   // last auth method
	dberr   error
}

func UnlinkMyOAuthIdentity(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	providerKey := strings.ToLower(strings.TrimSpace(c.Params("provider")))
	if providerKey == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_PROVIDER_UNKNOWN"})
	}

	now := time.Now()
	outcome := unlinkOutcome{}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 1. 事务内重新读 active identity
		var row database.OAuthIdentity
		if err := tx.
			Where("user_id = ? AND provider = ? AND unlinked_at IS NULL", user.ID, providerKey).
			First(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				outcome.skipped = true
				return nil // 让 tx commit 但 handler 返 404
			}
			outcome.dberr = err
			return err
		}
		// 2. 事务内重新计算 hasOther（SQLite 串行写，这里独占）
		hasOther, err := userHasOtherAuthMethodTx(tx, user, providerKey)
		if err != nil {
			outcome.dberr = err
			return err
		}
		if !hasOther {
			outcome.blocked = true
			return nil // tx commit 但 handler 返 409；不实际 update
		}
		// 3. 软删
		if err := tx.Model(&database.OAuthIdentity{}).
			Where("id = ? AND unlinked_at IS NULL", row.ID).
			Update("unlinked_at", now).Error; err != nil {
			outcome.dberr = err
			return err
		}
		outcome.row = row
		return nil
	})
	if txErr != nil {
		log.Printf("[OAUTH-UNLINK] tx failed user=%d provider=%s: %v", user.ID, providerKey, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if outcome.skipped {
		return c.Status(404).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_OAUTH_IDENTITY_NOT_FOUND",
			"provider":     providerKey,
		})
	}
	if outcome.blocked {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CANNOT_UNLINK_LAST_AUTH",
			"message":      "这是您唯一的登录方式，请先绑定其它登录方式再解绑",
		})
	}

	// fix H-Audit M7：审计先于 cache 失效，确保 unlink 事件可追溯
	LogOperationBy(0, user.ID, "user", "OAUTH_UNLINK", c.IP(),
		fmt.Sprintf(`[{"type":"OAUTH_UNLINK","provider":%q,"external_id":%q}]`, providerKey, outcome.row.ExternalID))
	proxy.RefreshUserAuth(user.ID)
	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_OAUTH_UNLINKED",
		"provider":     providerKey,
	})
}

// finishOAuthLinkToExistingUser 是 OAuthCallback 在 link-mode 时调用的分支。
// 在 oauth.go 的 OAuthCallback 路径里被引用。
func finishOAuthLinkToExistingUser(c *fiber.Ctx, userID uint, providerKey string, identity *OAuthIdentityData) error {
	// 1. 确认 user 仍存在 + status=1
	var user database.User
	if err := database.DB.First(&user, userID).Error; err != nil {
		log.Printf("[OAUTH-LINK] target user not found id=%d: %v", userID, err)
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_LINK_TARGET_NOT_FOUND"})
	}
	if user.Status != 1 {
		return c.Status(403).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_LINK_USER_INACTIVE"})
	}

	// 2. 验该用户当前没有同 provider 的活跃 identity（双重检查；prepare 时也检过一次，
	//    但中途用户可能在另一个 tab 刚绑了）
	already, err := hasActiveOAuthIdentity(userID, providerKey)
	if err != nil {
		log.Printf("[OAUTH-LINK] check existing failed user=%d provider=%s: %v", userID, providerKey, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if already {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_OAUTH_PROVIDER_ALREADY_LINKED",
			"provider":     providerKey,
		})
	}

	// 3. 验 (provider, external_id) 没被其它 user 占用
	//
	// fix CRITICAL H-Audit C-3（2026-05-20）：原 `lookupErr == nil && found && existing.ID != userID`
	// 在 DB 故障时 lookupErr != nil → 整个条件直接跳过 → identity 占用检查被旁路 →
	// 攻击者可在 DB 抖动窗口期把别人的 OAuth 绑到自己账号（auth hijack vector）。
	// 现在 lookupErr != nil 时 fail-closed 返 500。
	{
		existing, found, lookupErr := lookupActiveUserByOAuthIdentity(providerKey, identity.ExternalID)
		if lookupErr != nil {
			log.Printf("[OAUTH-LINK] identity conflict check failed user=%d provider=%s ext=%s: %v",
				userID, providerKey, identity.ExternalID, lookupErr)
			return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
		}
		if found && existing.ID != userID {
			return c.Status(409).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_OAUTH_ALREADY_REGISTERED",
				"message":      "该第三方账号已绑定其它账户",
				"provider":     providerKey,
			})
		}
	}

	// 4. 写 oauth_identities 行
	if err := linkOAuthIdentityTx(database.DB, userID, *identity); err != nil {
		log.Printf("[OAUTH-LINK] link failed user=%d provider=%s ext=%s: %v",
			userID, providerKey, identity.ExternalID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
	}

	// fix H-Audit M7：audit log 先于 cache 失效，确保 link 事件可追溯
	// 即便后续 RefreshUserAuth 失败或进程崩溃，事件已在 operation_logs 中持久化
	LogOperationBy(0, userID, "user", "OAUTH_LINK", c.IP(),
		fmt.Sprintf(`[{"type":"OAUTH_LINK","provider":%q,"external_id":%q}]`, providerKey, identity.ExternalID))
	proxy.RefreshUserAuth(userID)

	return c.JSON(fiber.Map{
		"success":      true,
		"message_code": "SUCCESS_OAUTH_LINKED",
		"provider":     providerKey,
		"external_id":  identity.ExternalID,
	})
}

// userHasOtherAuthMethod 判定除 excludedProvider 之外，user 是否还有其它登录凭据。
//
// 算法：枚举所有 auth method 类型
//   (a) phone（SMS 路径）
//   (b) email + password verified
//   (c) 至少一条其它 provider 的 active identity
//
// fix H-Audit H-2：从原 userHasOtherAuthMethod 改写为接受 *gorm.DB 参数的 tx 版本，
// 让 UnlinkMyOAuthIdentity 能在事务内调用，避免 TOCTOU race。
// 保留 user struct 入参（phone/email 来自 user 表的 lock-snapshot，不在 oauth_identities 表内）。
func userHasOtherAuthMethodTx(tx *gorm.DB, user *database.User, excludedProvider string) (bool, error) {
	if user.Phone != "" {
		return true, nil
	}
	if user.Email != "" && user.EmailVerifiedAt != nil && user.PasswordHash != "" {
		return true, nil
	}
	var n int64
	q := tx.Model(&database.OAuthIdentity{}).
		Where("user_id = ? AND unlinked_at IS NULL", user.ID)
	if excludedProvider != "" {
		q = q.Where("provider <> ?", excludedProvider)
	}
	if err := q.Count(&n).Error; err != nil {
		return false, fmt.Errorf("count other identities: %w", err)
	}
	return n > 0, nil
}

// fix H-Audit L5 / M11（2026-05-20）：currentOAuthStateCount 包装层删除——
// state 颁发已统一走 issueOAuthState helper（oauth.go），caller 不再需要直接读 counter。
