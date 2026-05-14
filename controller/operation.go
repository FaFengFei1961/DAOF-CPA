package controller

import (
	stdlog "log"

	"daof-ai-hub/database"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// LogOperationBy 在已知操作者 ID 时使用——可追责到具体 admin。
func LogOperationBy(operatorID, targetUserID uint, operatorRole, actionType, ipAddress string, details string) {
	_ = LogOperationByTx(database.DB, operatorID, targetUserID, operatorRole, actionType, ipAddress, details)
}

// LogOperationByTx 在给定的数据库事务内记录操作。
// 如果日志记录失败，它会记录错误并返回该错误，允许调用者回滚事务。
func LogOperationByTx(tx *gorm.DB, operatorID, targetUserID uint, operatorRole, actionType, ipAddress string, details string) error {
	_, err := LogOperationByTxReturning(tx, operatorID, targetUserID, operatorRole, actionType, ipAddress, details)
	return err
}

// LogOperationByTxReturning 与 LogOperationByTx 行为一致，但额外返回插入行的主键 ID。
//
// 设计原因（fix MAJOR 多模型审计第二十五轮）：
//   admin 调额场景需要先写 OperationLog 拿到主键 ID，再把 BillingEntry.RelatedID 关联到它，
//   保证账务流水与审计日志双向可追溯（之前 RelatedID=0 让链路断流，admin 改额无法 join 回审计）。
func LogOperationByTxReturning(tx *gorm.DB, operatorID, targetUserID uint, operatorRole, actionType, ipAddress string, details string) (uint, error) {
	row := database.OperationLog{
		TargetUserID: targetUserID,
		OperatorID:   operatorID,
		OperatorRole: operatorRole,
		ActionType:   actionType,
		IPAddress:    ipAddress,
		Details:      details,
	}
	if err := tx.Create(&row).Error; err != nil {
		// 审计断流是高优告警——必须冒泡到运维日志，而非沉默吞咽
		stdlog.Printf("[AUDIT-LOG-FAILED] action=%s target_user=%d operator=%d err=%v", actionType, targetUserID, operatorID, err)
		return 0, err
	}
	return row.ID, nil
}

// GetUserOperations 获取特定目标用户的所有操作审计日志
func GetUserOperations(c *fiber.Ctx) error {
	id := c.Params("id")

	// 这里可以加上针对页码的分页，为了简单起见最多返回100条近期记录
	var logs []database.OperationLog
	if err := database.DB.Where("target_user_id = ?", id).Order("id desc").Limit(100).Find(&logs).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "获取用户审计信息失败", "message_code": "ERR_READ_AUDIT_LOGS"})
	}

	return c.JSON(fiber.Map{"success": true, "data": logs})
}
