package controller

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// GetAdminChannels 查询系统内所有上游渠道配置。
//
// 这是 admin-only 接口，渠道页需要明文 key 来区分平台 key / 个人 key 等运维场景。
// 因此这里有意返回完整 key；公网部署时应继续依赖 admin 鉴权、Cloudflare 隧道与本机访问边界。
func GetAdminChannels(c *fiber.Ctx) error {
	var channels []database.Channel
	if err := database.DB.Order("id desc").Find(&channels).Error; err != nil {
		log.Printf("Read channels error: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_READ_FAILED",
			"message":      "Failed to fetch channel list",
		})
	}
	return c.JSON(fiber.Map{
		"success": true,
		"data":    channels,
	})
}

// ResetChannelKeyPayload 是重置 channel.key 的请求体
type ResetChannelKeyPayload struct {
	Key string `json:"key"`
}

// ResetChannelKey 单独的"重置 channel.key"接口。
// 与 UpdateChannel 的差异：本接口只更新 key 字段，避免在主编辑表单里来回传送 key 增加
// 暴露面；同时强制 admin 显式确认操作，并写一条审计日志。
func ResetChannelKey(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS", "message": "Invalid ID parameter"})
	}

	var req ResetChannelKeyPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_BODY", "message": "Invalid request body format"})
	}
	req.Key = strings.TrimSpace(req.Key)
	if req.Key == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EMPTY_KEY", "message": "新 Key 不能为空"})
	}

	var ch database.Channel
	if err := database.DB.First(&ch, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_CHANNEL_NOT_FOUND", "message": "Target channel not found"})
	}

	if err := database.DB.Model(&ch).Update("key", req.Key).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_CHANNEL_UPDATE_FAILED", "message": "Failed to reset channel key"})
	}

	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success":      true,
		"message":      "Channel Key 已重置",
		"message_code": "SUCCESS_CHANNEL_KEY_RESET",
	})
}

// CreateChannel 添加一条全新的高阶暗网管道配置
func CreateChannel(c *fiber.Ctx) error {
	var body database.Channel
	if err := c.BodyParser(&body); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_BODY",
			"message":      "Invalid request body format",
		})
	}

	if body.Type == "" || body.Key == "" || body.Name == "" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_MISSING_REQ",
			"message":      "Channel Type, Name and Key are required",
		})
	}
	body.Type = proxy.NormalizeChannelType(body.Type)
	if !proxy.IsAllowedChannelType(body.Type) {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_TYPE_INVALID",
			"message":      "Channel Type is not supported",
		})
	}

	// fix Major SSRF：BaseURL/ProxyURL 必须 http(s) 且非云元数据/链路本地段
	if err := proxy.ValidateChannelURL(body.BaseURL); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_BASE_URL_INVALID",
			"message":      "BaseURL invalid: " + err.Error(),
		})
	}
	if err := proxy.ValidateChannelURL(body.ProxyURL); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_PROXY_URL_INVALID",
			"message":      "ProxyURL invalid: " + err.Error(),
		})
	}

	if err := database.DB.Create(&body).Error; err != nil {
		log.Printf("Create channel error: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_CREATE_FAILED",
			"message":      "Failed to insert new channel",
		})
	}

	// 热更新网关内存路由
	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success": true,
		"data":    body,
	})
}

// UpdateChannel 实时校准并修改某条渠道的核心网络坐标与秘钥
func UpdateChannel(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "Invalid ID parameter",
		})
	}

	var body database.Channel
	if err := c.BodyParser(&body); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_BODY",
			"message":      "Invalid request body format",
		})
	}

	var ch database.Channel
	if err := database.DB.First(&ch, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_NOT_FOUND",
			"message":      "Target channel entity evaporated or not found",
		})
	}

	// 应用更新字段
	if body.Type != "" {
		nextType := proxy.NormalizeChannelType(body.Type)
		if !proxy.IsAllowedChannelType(nextType) {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_CHANNEL_TYPE_INVALID",
				"message":      "Channel Type is not supported",
			})
		}
		ch.Type = nextType
	}
	if body.Name != "" {
		ch.Name = body.Name
	}
	// 仅在传入真实 key 时才更新；空值或历史掩码（含 ********）一律忽略，避免旧前端把脱敏字符串写回 DB。
	if body.Key != "" && !strings.Contains(body.Key, "********") {
		ch.Key = body.Key
	}
	// fix Major SSRF：BaseURL/ProxyURL 必须 http(s) 且非云元数据/链路本地段
	if err := proxy.ValidateChannelURL(body.BaseURL); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_BASE_URL_INVALID",
			"message":      "BaseURL invalid: " + err.Error(),
		})
	}
	if err := proxy.ValidateChannelURL(body.ProxyURL); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_PROXY_URL_INVALID",
			"message":      "ProxyURL invalid: " + err.Error(),
		})
	}
	ch.BaseURL = body.BaseURL
	ch.ProxyURL = body.ProxyURL
	ch.Headers = body.Headers
	if body.Weight > 0 {
		ch.Weight = body.Weight
	}
	if body.Status != 0 {
		ch.Status = body.Status
	}

	// fix CRITICAL R23-C2（codex 审查）：检测渠道是否"官方化"——之前指向第三方/cloaked、
	// 现在被改成官方 host。攻击路径：admin 先建第三方 channel + 子 ChannelModel(level=off)
	// 通过校验，再把 channel.BaseURL 改成 api.openai.com，绕过 fail-closed。
	//
	// 处置：发现官方化时，扫描所有子 ChannelModel——若仍有 level=off 或 level≠off+fail≠closed
	// 的"裸奔"行 → 拒绝保存，列出受影响的 model_id 供 admin 先去修。
	if channelTargetsOfficialHost(&ch) {
		var unsafeModels []database.ChannelModel
		database.DB.Where("channel_id = ?", ch.ID).Find(&unsafeModels)
		var blocking []string
		for _, m := range unsafeModels {
			lvl := strings.ToLower(strings.TrimSpace(m.ModerationLevel))
			fm := strings.ToLower(strings.TrimSpace(m.ModerationFailMode))
			if lvl == "" {
				lvl = "off"
			}
			if fm == "" {
				fm = "open"
			}
			// 官方下：off 永远拒（admin 先单独去模型编辑里勾 confirm 才能维持 off）；非 off 必须 closed
			if lvl == "off" {
				blocking = append(blocking, m.ModelID+":off(needs explicit confirm)")
			} else if fm != "closed" {
				blocking = append(blocking, m.ModelID+":"+lvl+"+"+fm+"(needs closed)")
			}
		}
		if len(blocking) > 0 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_CHANNEL_OFFICIALIZE_UNSAFE_MODELS",
				"message": "本次更新会让该渠道指向官方 API（" + ch.Type +
					"），但下列子模型当前风控配置不安全。请先到「模型 & 定价」里调整后再保存渠道更新：" +
					strings.Join(blocking, ", "),
			})
		}
	}

	if err := database.DB.Save(&ch).Error; err != nil {
		log.Printf("Update channel error: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_UPDATE_FAILED",
			"message":      "Database synchronization failure during channel update",
		})
	}

	// fix MAJOR R23-M4：渠道任何字段变更（status / base_url / type）都可能影响
	// LookupModerationPolicy 的"取最严"结果。统一全表 flush，30s 内的策略缓存立即失效。
	proxy.FlushAllModerationPolicyCache()

	// 热更新网关内存路由
	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success": true,
		"data":    ch,
	})
}

// DeleteChannel 物理摧毁并拔除某条越权或废弃渠道
func DeleteChannel(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "Invalid ID parameter",
		})
	}
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", id).Delete(&database.ChannelModel{}).Error; err != nil {
			return fmt.Errorf("ERR_CHANNEL_MODEL_CASCADE_DELETE_FAILED: %w", err)
		}
		if err := tx.Delete(&database.Channel{}, id).Error; err != nil {
			return fmt.Errorf("ERR_CHANNEL_DELETE_FAILED: %w", err)
		}
		return nil
	})
	if err != nil {
		log.Printf("[CHANNEL-DELETE] id=%d failed: %v", id, err)
		code := "ERR_CHANNEL_DELETE_FAILED"
		if strings.Contains(err.Error(), "ERR_CHANNEL_MODEL_CASCADE_DELETE_FAILED") {
			code = "ERR_CHANNEL_MODEL_CASCADE_DELETE_FAILED"
		}
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": code,
		})
	}
	// fix MAJOR R23-M4：删除渠道后 moderation policy cache 可能还指向已删 modelID
	proxy.FlushAllModerationPolicyCache()

	// 热更新网关内存路由
	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Channel and its matrix have been completely obliterated.",
	})
}
