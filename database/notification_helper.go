// Package database / notification_helper.go
//
// 站内通知创建的唯一权威实现。proxy 包和 controller 包都通过这个 helper 写库，
// 杜绝原本三套并行实现（CreateNotification / createNotificationDirect / createNotificationWithDedup）。
package database

import (
	"log"
	"time"
)

// CreateNotificationRecord 内部 helper：创建一条站内通知。
// dedupKey 传 nil 表示无去重；传非 nil 时利用 DedupKey 唯一索引跨实例去重。
//
// dedupKey 唯一冲突时静默忽略（多实例 cron 预期行为），仅记日志。
func CreateNotificationRecord(userID uint, category, severity, title, body, actionURL, actionText, relatedType string, relatedID uint, dedupKey *string) {
	if DB == nil {
		return
	}
	n := Notification{
		UserID:      userID,
		Category:    category,
		Severity:    severity,
		Title:       title,
		Body:        body,
		ActionURL:   actionURL,
		ActionText:  actionText,
		RelatedType: relatedType,
		RelatedID:   relatedID,
		DedupKey:    dedupKey,
		CreatedAt:   time.Now(),
	}
	if err := DB.Create(&n).Error; err != nil {
		// 唯一约束冲突（多实例去重的预期行为）+ 网络瞬断都走这里
		if dedupKey != nil {
			log.Printf("[NOTIFY] create (dedup=%s) skipped: %v", *dedupKey, err)
		} else {
			log.Printf("[NOTIFY] create user=%d cat=%s failed: %v", userID, category, err)
		}
	}
}
