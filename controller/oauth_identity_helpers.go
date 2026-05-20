// Package controller / oauth_identity_helpers.go
//
// OAuth identity 查找 / 创建 helper。Phase H-3（2026-05-20）。
//
// 这层把 oauth_identities 表的查询封装起来，让 callback / risk / profile handler 不
// 直接接触 GORM SQL。同时保留对旧 User.GithubID 列的"双写"，让 admin UI（按 github_id
// 搜索、显示）继续工作；H-3b/H-5 阶段会移除 User.GithubID 并把 admin UI 切到读
// oauth_identities。
package controller

import (
	"errors"
	"fmt"
	"time"

	"daof-cpa/database"

	"gorm.io/gorm"
)

// lookupActiveUserByOAuthIdentity 通过 (provider, external_id) 找当前**活跃**绑定的 user。
// 不命中 unlinked 行（unlinked_at != NULL）—— 已解绑的旧 identity 不算登录凭据。
//
// 返回：
//   - user, true, nil → 找到活跃绑定
//   - nil, false, nil → 该 identity 没人绑（或者只有 unlinked 历史行）
//   - nil, false, err → DB 错误
func lookupActiveUserByOAuthIdentity(provider, externalID string) (*database.User, bool, error) {
	if provider == "" || externalID == "" {
		return nil, false, nil
	}
	var id database.OAuthIdentity
	err := database.DB.
		Where("provider = ? AND external_id = ? AND unlinked_at IS NULL", provider, externalID).
		First(&id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lookup oauth_identity: %w", err)
	}
	var user database.User
	if err := database.DB.First(&user, id.UserID).Error; err != nil {
		// identity 存在但 user 找不到 → DB 不一致；当作"不存在"处理（caller 会走新注册）
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("load user by identity: %w", err)
	}
	return &user, true, nil
}

// linkOAuthIdentityTx 在 tx 内创建一条 active oauth_identities 行。
// caller 应在事务内调；若 (provider, external_id) 已被其他用户占用（partial unique index 拦），
// 返回包装错误，caller 应展开判定为"identity 被他人持有"业务错误。
//
// 同 user 同 provider 不允许重复 active 行（应用层语义）；本 helper 不校验，
// caller 应先 lookupOAuthIdentitiesForUser 判断。
func linkOAuthIdentityTx(tx *gorm.DB, userID uint, data OAuthIdentityData) error {
	row := database.OAuthIdentity{
		UserID:         userID,
		Provider:       data.Provider,
		ExternalID:     data.ExternalID,
		EmailAtLink:    data.Email,
		UsernameAtLink: data.Username,
		LinkedAt:       time.Now(),
	}
	if err := tx.Create(&row).Error; err != nil {
		return fmt.Errorf("create oauth_identity: %w", err)
	}
	return nil
}

// lookupOAuthIdentitiesForUser 列出 user 的所有 active 绑定。
// 用于"用户已绑了哪些 provider" 的设置页 / 重复绑定校验。
func lookupOAuthIdentitiesForUser(userID uint) ([]database.OAuthIdentity, error) {
	var rows []database.OAuthIdentity
	if err := database.DB.
		Where("user_id = ? AND unlinked_at IS NULL", userID).
		Order("linked_at ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list user identities: %w", err)
	}
	return rows, nil
}

// hasActiveOAuthIdentity 判断 user 是否已绑过该 provider（不论 external_id）。
// 用于：一个 GitHub 账号只能绑一次到一个 user。
func hasActiveOAuthIdentity(userID uint, provider string) (bool, error) {
	var n int64
	if err := database.DB.
		Model(&database.OAuthIdentity{}).
		Where("user_id = ? AND provider = ? AND unlinked_at IS NULL", userID, provider).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("count user provider identities: %w", err)
	}
	return n > 0, nil
}
