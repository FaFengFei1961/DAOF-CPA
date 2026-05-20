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
	out := make([]fiber.Map, 0, len(identities))
	for _, id := range identities {
		out = append(out, fiber.Map{
			"provider":         id.Provider,
			"external_id":      id.ExternalID,
			"email_at_link":    id.EmailAtLink,
			"username_at_link": id.UsernameAtLink,
			"linked_at":        id.LinkedAt,
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

	// 防 state store 被刷爆（与 PrepareOAuthState 同保护）
	if currentOAuthStateCount() >= oauthStateMaxItems {
		log.Printf("[OAUTH-LINK] state count overflow user=%d provider=%s", user.ID, providerKey)
		return c.Status(503).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_OVERLOAD"})
	}

	stateValue, err := randomHex(32)
	if err != nil {
		log.Printf("[OAUTH-LINK] generate state failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_INTERNAL"})
	}
	verifier, err := generatePKCEVerifier()
	if err != nil {
		log.Printf("[OAUTH-LINK] generate PKCE verifier failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_INTERNAL"})
	}
	storeOAuthLinkState(stateValue, verifier, user.ID)
	return c.JSON(fiber.Map{
		"success":               true,
		"state":                 stateValue,
		"code_challenge":        pkceChallenge(verifier),
		"code_challenge_method": "S256",
		"link_user_id":          user.ID, // 仅作前端调试 hint，与 state 一致性无关
		"provider":              providerKey,
	})
}

// UnlinkMyOAuthIdentity POST /api/user/oauth/:provider/unlink
// 解绑一条 OAuth identity（软删 unlinked_at）。
//
// 安全：必须保证用户至少保留一个 auth method（防完全失联）。
func UnlinkMyOAuthIdentity(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	providerKey := strings.ToLower(strings.TrimSpace(c.Params("provider")))
	if providerKey == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OAUTH_PROVIDER_UNKNOWN"})
	}

	// 找到这个 user 在 providerKey 下的 active identity
	var row database.OAuthIdentity
	if err := database.DB.
		Where("user_id = ? AND provider = ? AND unlinked_at IS NULL", user.ID, providerKey).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.Status(404).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_OAUTH_IDENTITY_NOT_FOUND",
				"provider":     providerKey,
			})
		}
		log.Printf("[OAUTH-UNLINK] lookup failed user=%d provider=%s: %v", user.ID, providerKey, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	// "至少保留一个 auth method" 校验：解绑后用户至少还要有一种凭据
	hasOther, err := userHasOtherAuthMethod(user, providerKey)
	if err != nil {
		log.Printf("[OAUTH-UNLINK] auth method count failed user=%d: %v", user.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}
	if !hasOther {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CANNOT_UNLINK_LAST_AUTH",
			"message":      "这是您唯一的登录方式，请先绑定其它登录方式再解绑",
		})
	}

	// 软删：仅写 unlinked_at（append-only invariant 允许这一列 update）
	now := time.Now()
	if err := database.DB.Model(&database.OAuthIdentity{}).
		Where("id = ? AND unlinked_at IS NULL", row.ID).
		Update("unlinked_at", now).Error; err != nil {
		log.Printf("[OAUTH-UNLINK] update failed id=%d: %v", row.ID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_UPDATE"})
	}

	proxy.RefreshUserAuth(user.ID)
	LogOperationBy(0, user.ID, "user", "OAUTH_UNLINK", c.IP(),
		fmt.Sprintf(`[{"type":"OAUTH_UNLINK","provider":%q,"external_id":%q}]`, providerKey, row.ExternalID))
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
	if existing, found, lookupErr := lookupActiveUserByOAuthIdentity(providerKey, identity.ExternalID); lookupErr == nil && found && existing.ID != userID {
		return c.Status(409).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_OAUTH_ALREADY_REGISTERED",
			"message":      "该第三方账号已绑定其它账户",
			"provider":     providerKey,
		})
	}

	// 4. 写 oauth_identities 行
	if err := linkOAuthIdentityTx(database.DB, userID, *identity); err != nil {
		log.Printf("[OAUTH-LINK] link failed user=%d provider=%s ext=%s: %v",
			userID, providerKey, identity.ExternalID, err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_INSERT_FAILED"})
	}

	proxy.RefreshUserAuth(userID)
	LogOperationBy(0, userID, "user", "OAUTH_LINK", c.IP(),
		fmt.Sprintf(`[{"type":"OAUTH_LINK","provider":%q,"external_id":%q}]`, providerKey, identity.ExternalID))

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
func userHasOtherAuthMethod(user *database.User, excludedProvider string) (bool, error) {
	if user.Phone != "" {
		return true, nil
	}
	if user.Email != "" && user.EmailVerifiedAt != nil && user.PasswordHash != "" {
		return true, nil
	}
	var n int64
	q := database.DB.Model(&database.OAuthIdentity{}).
		Where("user_id = ? AND unlinked_at IS NULL", user.ID)
	if excludedProvider != "" {
		q = q.Where("provider <> ?", excludedProvider)
	}
	if err := q.Count(&n).Error; err != nil {
		return false, fmt.Errorf("count other identities: %w", err)
	}
	return n > 0, nil
}

// currentOAuthStateCount 是给本文件用的 atomic counter accessor，避免直接 import sync/atomic。
// 实现在 oauth.go 顶部的 oauthStateCount 变量。
func currentOAuthStateCount() int64 {
	return loadOAuthStateCount()
}
