package controller

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"daof-cpa/database"
	"daof-cpa/proxy"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
	"gorm.io/gorm/clause"
)

// ─── 内容审核字段校验（fix CRITICAL R23）──────────────────────────────────
//
// 防止 admin 配置失误导致直连官方上游的渠道在审核全关时透传 jailbreak 内容
// 引发账号被封禁。Strategy: enforce fail-closed by default for OFFICIAL channels.
//
// 触发条件：
//   - channel.Type ∈ {openai, anthropic, gemini}
//   - channel.BaseURL 解析后 host 是该家官方 host（或 BaseURL 为空 → 默认走官方）
//   - ChannelModel.ModerationLevel="off"
//
// 行为：
//   - 默认拒绝（返回 400 ERR_OFFICIAL_NEEDS_MODERATION）
//   - admin 在 body 里显式带 `confirm_official_no_moderation: true` → 绕开校验
//     （写 OperationLog 留痕）

// officialChannelHosts 各家官方 API 域名（小写）。
var officialChannelHosts = map[string]map[string]bool{
	"openai": {
		"api.openai.com": true,
	},
	"anthropic": {
		"api.anthropic.com": true,
	},
	"gemini": {
		"generativelanguage.googleapis.com": true,
	},
}

// allowedModerationLevels / allowedModerationFailModes 服务端 enum 校验。
var allowedModerationLevels = map[string]bool{
	"off":        true,
	"keyword":    true,
	"moderation": true,
	"strict":     true,
}
var allowedModerationFailModes = map[string]bool{
	"open":   true,
	"closed": true,
}

type channelModelPayload struct {
	ModelID                            string `json:"model_id"`
	DisplayName                        string `json:"display_name"`
	InputPricePicoPerToken             int64  `json:"input_price_pico_per_token"`
	OutputPricePicoPerToken            int64  `json:"output_price_pico_per_token"`
	CachedInputPricePicoPerToken       int64  `json:"cached_input_price_pico_per_token"`
	CacheWriteInputPricePicoPerToken   int64  `json:"cache_write_input_price_pico_per_token"`
	CacheWrite1hInputPricePicoPerToken int64  `json:"cache_write_1h_input_price_pico_per_token"`
	ContextPriceThreshold              int    `json:"context_price_threshold"`
	HighInputPricePicoPerToken         int64  `json:"high_input_price_pico_per_token"`
	HighCachedInputPricePicoPerToken   int64  `json:"high_cached_input_price_pico_per_token"`
	HighOutputPricePicoPerToken        int64  `json:"high_output_price_pico_per_token"`
	MaxContextLength                   int    `json:"max_context_length"`
	Weight                             int    `json:"weight"`
	Status                             int    `json:"status"`
	EndpointPolicy                     string `json:"endpoint_policy"`
	ModerationLevel                    string `json:"moderation_level"`
	ModerationFailMode                 string `json:"moderation_fail_mode"`
}

type channelModelResponse struct {
	ID                                 uint      `json:"id"`
	ChannelID                          uint      `json:"channel_id"`
	ModelID                            string    `json:"model_id"`
	DisplayName                        string    `json:"display_name"`
	InputPricePicoPerToken             int64     `json:"input_price_pico_per_token"`
	OutputPricePicoPerToken            int64     `json:"output_price_pico_per_token"`
	CachedInputPricePicoPerToken       int64     `json:"cached_input_price_pico_per_token"`
	CacheWriteInputPricePicoPerToken   int64     `json:"cache_write_input_price_pico_per_token"`
	CacheWrite1hInputPricePicoPerToken int64     `json:"cache_write_1h_input_price_pico_per_token"`
	ContextPriceThreshold              int       `json:"context_price_threshold"`
	HighInputPricePicoPerToken         int64     `json:"high_input_price_pico_per_token"`
	HighCachedInputPricePicoPerToken   int64     `json:"high_cached_input_price_pico_per_token"`
	HighOutputPricePicoPerToken        int64     `json:"high_output_price_pico_per_token"`
	MaxContextLength                   int       `json:"max_context_length"`
	Weight                             int       `json:"weight"`
	Status                             int       `json:"status"`
	EndpointPolicy                     string    `json:"endpoint_policy"`
	ModerationLevel                    string    `json:"moderation_level"`
	ModerationFailMode                 string    `json:"moderation_fail_mode"`
	CreatedAt                          time.Time `json:"created_at"`
	UpdatedAt                          time.Time `json:"updated_at"`
}

func (p channelModelPayload) toChannelModel() (database.ChannelModel, error) {
	model := database.ChannelModel{
		ModelID:                            p.ModelID,
		DisplayName:                        p.DisplayName,
		InputPricePicoPerToken:             p.InputPricePicoPerToken,
		OutputPricePicoPerToken:            p.OutputPricePicoPerToken,
		CachedInputPricePicoPerToken:       p.CachedInputPricePicoPerToken,
		CacheWriteInputPricePicoPerToken:   p.CacheWriteInputPricePicoPerToken,
		CacheWrite1hInputPricePicoPerToken: p.CacheWrite1hInputPricePicoPerToken,
		ContextPriceThreshold:              p.ContextPriceThreshold,
		HighInputPricePicoPerToken:         p.HighInputPricePicoPerToken,
		HighCachedInputPricePicoPerToken:   p.HighCachedInputPricePicoPerToken,
		HighOutputPricePicoPerToken:        p.HighOutputPricePicoPerToken,
		MaxContextLength:                   p.MaxContextLength,
		Weight:                             p.Weight,
		Status:                             p.Status,
		EndpointPolicy:                     p.EndpointPolicy,
		ModerationLevel:                    p.ModerationLevel,
		ModerationFailMode:                 p.ModerationFailMode,
	}
	if err := database.ValidateChannelModelPricing(&model); err != nil {
		return database.ChannelModel{}, err
	}
	return model, nil
}

func newChannelModelResponse(cm database.ChannelModel) channelModelResponse {
	return channelModelResponse{
		ID:                                 cm.ID,
		ChannelID:                          cm.ChannelID,
		ModelID:                            cm.ModelID,
		DisplayName:                        cm.DisplayName,
		InputPricePicoPerToken:             cm.InputPricePicoPerToken,
		OutputPricePicoPerToken:            cm.OutputPricePicoPerToken,
		CachedInputPricePicoPerToken:       cm.CachedInputPricePicoPerToken,
		CacheWriteInputPricePicoPerToken:   cm.CacheWriteInputPricePicoPerToken,
		CacheWrite1hInputPricePicoPerToken: cm.CacheWrite1hInputPricePicoPerToken,
		ContextPriceThreshold:              cm.ContextPriceThreshold,
		HighInputPricePicoPerToken:         cm.HighInputPricePicoPerToken,
		HighCachedInputPricePicoPerToken:   cm.HighCachedInputPricePicoPerToken,
		HighOutputPricePicoPerToken:        cm.HighOutputPricePicoPerToken,
		MaxContextLength:                   cm.MaxContextLength,
		Weight:                             cm.Weight,
		Status:                             cm.Status,
		EndpointPolicy:                     cm.EndpointPolicy,
		ModerationLevel:                    cm.ModerationLevel,
		ModerationFailMode:                 cm.ModerationFailMode,
		CreatedAt:                          cm.CreatedAt,
		UpdatedAt:                          cm.UpdatedAt,
	}
}

func newChannelModelResponses(models []database.ChannelModel) []channelModelResponse {
	out := make([]channelModelResponse, 0, len(models))
	for _, model := range models {
		out = append(out, newChannelModelResponse(model))
	}
	return out
}

// validateChannelModelEndpointPolicy 校验并规范化模型端点兼容策略。
func validateChannelModelEndpointPolicy(cm *database.ChannelModel) (int, string, string) {
	policy := database.NormalizeEndpointPolicy(cm.EndpointPolicy)
	if !database.IsValidEndpointPolicy(policy) {
		return 400, "ERR_INVALID_ENDPOINT_POLICY",
			"endpoint_policy 取值非法（允许：all / no_chat_non_stream / responses_only）"
	}
	cm.EndpointPolicy = database.DefaultEndpointPolicyForModel(cm.ModelID, policy)
	return 0, "", ""
}

// channelTargetsOfficialHost 判断 channel 是否指向某家官方 API。
//
// 空 BaseURL 视为"使用 SDK 默认 host"（默认即官方），返回 true。
//
// fix MAJOR R23-M9（codex 审查）：host 规范化处理 trailing dot（`api.openai.com.` 也是
// 合法 DNS 名）+ 类型小写。否则攻击者可写 `https://api.openai.com.` 绕过检测。
func channelTargetsOfficialHost(ch *database.Channel) bool {
	chType := strings.ToLower(strings.TrimSpace(ch.Type))
	hosts, isFamily := officialChannelHosts[chType]
	if !isFamily {
		return false
	}
	base := strings.TrimSpace(ch.BaseURL)
	if base == "" {
		return true // 默认 host = 官方
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return false // 解析失败 → 不假定为官方（保守放行，让 admin 显式选）
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimSuffix(host, ".") // 兼容 trailing dot 写法
	return hosts[host]
}

// validateChannelModelModeration 校验审核字段。
// 返回 (httpStatus, messageCode, message)。httpStatus=0 表示通过。
//
// confirmOfficialNoMod 来自请求体 confirm_official_no_moderation 字段——admin 显式
// 同意"该官方渠道不审核"时为 true，跳过 fail-closed 校验。
func validateChannelModelModeration(cm *database.ChannelModel, ch *database.Channel, confirmOfficialNoMod bool) (int, string, string) {
	level := strings.ToLower(strings.TrimSpace(cm.ModerationLevel))
	failMode := strings.ToLower(strings.TrimSpace(cm.ModerationFailMode))

	// 兜底默认：未传 → off / open（与 GORM tag 一致）
	if level == "" {
		level = "off"
	}
	if failMode == "" {
		failMode = "open"
	}
	if !allowedModerationLevels[level] {
		return 400, "ERR_INVALID_MODERATION_LEVEL",
			"moderation_level 取值非法（允许：off / keyword / moderation / strict）"
	}
	if !allowedModerationFailModes[failMode] {
		return 400, "ERR_INVALID_MODERATION_FAIL_MODE",
			"moderation_fail_mode 取值非法（允许：open / closed）"
	}

	// OpenAI/Codex-family 模型一律实装内容审查。这里按 model_id 判定，而不是按
	// channel.Type 判定：openai 通道类型也承载 DeepSeek/国产/自部署等 OpenAI-compatible
	// 模型，不能误伤到整个兼容协议族。
	if database.IsOpenAIModelID(cm.ModelID) {
		level = database.OpenAIModelModerationLevel
		failMode = database.OpenAIModelModerationFailMode
	}

	// fix CRITICAL R23-C3（codex 审查）：官方渠道下"打开了审核但配 fail_mode=open"等同于
	// 没开审 —— 审核 API 不可达时 prompt 直接透传到官方 key 引发封号。强制策略：
	//   - level=off → 必须 confirm（与之前一致）
	//   - level=keyword/moderation/strict → fail_mode 必须是 closed，否则拒绝保存
	if channelTargetsOfficialHost(ch) {
		if level == "off" && !confirmOfficialNoMod {
			return 400, "ERR_OFFICIAL_NEEDS_MODERATION",
				"该渠道指向官方 API（" + ch.Type + "），关闭审核可能导致账号被封禁。请在 ChannelModel 表单中勾选「我了解风险，仍要关闭审核」后再保存。"
		}
		if level != "off" && failMode != "closed" {
			return 400, "ERR_OFFICIAL_NEEDS_FAIL_CLOSED",
				"该渠道指向官方 API（" + ch.Type + "），审核启用时 fail_mode 必须为 closed。否则审核服务不可达时违规 prompt 会直接透传到官方上游导致封号。"
		}
	}

	// 写回规范化值（去空白、统一小写）—— 让 DB 里始终是干净 enum
	cm.ModerationLevel = level
	cm.ModerationFailMode = failMode
	return 0, "", ""
}

// auditOfficialNoModerationConfirmed fix MAJOR R23-M7：官方渠道 + level=off 通过
// confirm 豁免时写一条高优先级 OperationLog，便于事后追责。
//
// action 是 "ADD" / "UPDATE"，区分入口；OperatorID 从 fiber locals 读（admin auth
// 中间件设置；读不到回退 0）。
func auditOfficialNoModerationConfirmed(c *fiber.Ctx, ch *database.Channel, cm *database.ChannelModel, action string) {
	var operatorID uint
	if v := c.Locals("admin_user_id"); v != nil {
		if id, ok := v.(uint); ok {
			operatorID = id
		}
	}
	details := fmt.Sprintf(
		`{"action":%q,"channel_id":%d,"channel_type":%q,"channel_base_url":%q,"model_id":%q,"override":"official_channel_moderation_off"}`,
		action, ch.ID, ch.Type, ch.BaseURL, cm.ModelID,
	)
	_ = LogOperationByTx(database.DB, operatorID, 0, "admin", "OFFICIAL_NO_MODERATION_CONFIRMED", c.IP(), details)
}

// fix Major（codex 第三轮）：旧 validateUpstreamURL 拒绝 localhost/127.*/private，
// 与本轮 channel.go 允许本地 BaseURL（Ollama / vLLM 自部署）矛盾，导致用户保存了
// http://127.0.0.1:11434 的 channel 后无法用模型探测功能。
// 现在统一使用 proxy.ValidateChannelURL（见 proxy/url_safety.go），
// 既允许本地部署，又拦截 file://、云元数据 IP 等真正危险的协议/地址。
// 模型探测请求本身在 stream.go 走 safeDialContext，做 DNS 重绑定防御。

// GetPublicModels 兼容 OpenAI 标准的 /v1/models 接口
func GetPublicModels(c *fiber.Ctx) error {
	var uniqueModels []string
	if err := database.DB.Model(&database.ChannelModel{}).
		Joins("JOIN channels ON channels.id = channel_models.channel_id").
		Where("channel_models.status = ? AND channels.status = ?", 1, 1).
		Distinct("channel_models.model_id").
		Order("channel_models.model_id ASC").
		Pluck("channel_models.model_id", &uniqueModels).Error; err != nil {
		log.Printf("Get public models error: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"error": fiber.Map{
				"message": "Internal server error connecting to proxy models",
				"type":    "server_error",
			},
		})
	}

	type OpenAIModel struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}

	var dataList []OpenAIModel
	for _, m := range uniqueModels {
		dataList = append(dataList, OpenAIModel{
			ID:      m,
			Object:  "model",
			Created: 1686935002, // 伪造统一时间戳
			OwnedBy: "daof-cpa",
		})
	}

	return c.JSON(fiber.Map{
		"object": "list",
		"data":   dataList,
	})
}

// GetPublicPricing 开放给前端侧边栏的业务聚合单价探针
func GetPublicPricing(c *fiber.Ctx) error {
	type PricingResult struct {
		ModelID              string  `gorm:"column:model_id" json:"model_id"`
		MinInputPrice        float64 `gorm:"column:min_input_price" json:"min_input_price"`
		MinOutputPrice       float64 `gorm:"column:min_output_price" json:"min_output_price"`
		MinCachePrice        float64 `gorm:"column:min_cache_price" json:"min_cache_price"`
		MinCacheWritePrice   float64 `gorm:"column:min_cache_write_price" json:"min_cache_write_price"`
		MinCacheWrite1hPrice float64 `gorm:"column:min_cache_write_1h_price" json:"min_cache_write_1h_price"`
		ContextThreshold     int     `gorm:"column:context_threshold" json:"context_threshold"`
		MinHighInPrice       float64 `gorm:"column:min_high_in_price" json:"min_high_in_price"`
		MinHighCachePrice    float64 `gorm:"column:min_high_cache_price" json:"min_high_cache_price"`
		MinHighOutPrice      float64 `gorm:"column:min_high_out_price" json:"min_high_out_price"`
		MaxContextLength     int     `gorm:"column:max_context_length" json:"max_context_length"`
	}

	var results []PricingResult
	if err := database.DB.Model(&database.ChannelModel{}).
		Joins("JOIN channels ON channels.id = channel_models.channel_id").
		Select(`model_id,
			COALESCE(MIN(NULLIF(input_price_pico_per_token, 0)), 0) / 1000000000.0 as min_input_price,
			COALESCE(MIN(NULLIF(output_price_pico_per_token, 0)), 0) / 1000000000.0 as min_output_price,
			COALESCE(MIN(NULLIF(cached_input_price_pico_per_token, 0)), 0) / 1000000000.0 as min_cache_price,
			COALESCE(MIN(NULLIF(cache_write_input_price_pico_per_token, 0)), 0) / 1000000000.0 as min_cache_write_price,
			COALESCE(MIN(NULLIF(cache_write_1h_input_price_pico_per_token, 0)), 0) / 1000000000.0 as min_cache_write_1h_price,
			MAX(context_price_threshold) as context_threshold,
			COALESCE(MIN(NULLIF(high_input_price_pico_per_token, 0)), 0) / 1000000000.0 as min_high_in_price,
			COALESCE(MIN(NULLIF(high_cached_input_price_pico_per_token, 0)), 0) / 1000000000.0 as min_high_cache_price,
			COALESCE(MIN(NULLIF(high_output_price_pico_per_token, 0)), 0) / 1000000000.0 as min_high_out_price,
			MAX(max_context_length) as max_context_length`).
		Where("channel_models.status = ? AND channels.status = ?", 1, 1).
		Group("channel_models.model_id").
		Scan(&results).Error; err != nil {
		log.Printf("Failed to aggregate model pricing: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "Failed to aggregate models"})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    results,
	})
}

// GetModelsByChannel 获取特定渠道下属所有的自定义定价模型配置
func GetModelsByChannel(c *fiber.Ctx) error {
	channelIDStr := c.Params("channelId")
	channelID, err := strconv.Atoi(channelIDStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "Invalid Channel ID parameter",
		})
	}

	var models []database.ChannelModel
	if err := database.DB.Where("channel_id = ?", channelID).Order("id desc").Find(&models).Error; err != nil {
		log.Printf("Read channel models error: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHMOD_READ_FAILED",
			"message":      "Failed to fetch channel binding models",
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    newChannelModelResponses(models),
	})
}

// AddChannelModel 为特定渠道独立挂载一颗带有专属定价策略的模型
func AddChannelModel(c *fiber.Ctx) error {
	channelIDStr := c.Params("channelId")
	channelID, err := strconv.Atoi(channelIDStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "Invalid Channel ID",
		})
	}

	var payload channelModelPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_BODY",
			"message":      "Invalid request body format",
		})
	}
	body, err := payload.toChannelModel()
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_LIMIT",
			"message":      err.Error(),
		})
	}

	if body.ModelID == "" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHMOD_MISSING_REQ",
			"message":      "ModelID is required required to bind a price matrix",
		})
	}

	// 加载渠道用于审核字段校验（官方 host 检测）
	var ch database.Channel
	if err := database.DB.First(&ch, channelID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHANNEL_NOT_FOUND",
			"message":      "Channel not found",
		})
	}

	// fix CRITICAL R23：服务端校验审核字段（不能信任前端 enum）+ 官方渠道 fail-closed
	confirmOfficialNoMod := gjson.GetBytes(c.Body(), "confirm_official_no_moderation").Bool()
	if status, code, msg := validateChannelModelModeration(&body, &ch, confirmOfficialNoMod); status != 0 {
		return c.Status(status).JSON(fiber.Map{
			"success":      false,
			"message_code": code,
			"message":      msg,
		})
	}
	if status, code, msg := validateChannelModelEndpointPolicy(&body); status != 0 {
		return c.Status(status).JSON(fiber.Map{
			"success":      false,
			"message_code": code,
			"message":      msg,
		})
	}
	if err := database.ValidateChannelModelPricing(&body); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_LIMIT",
			"message":      err.Error(),
		})
	}
	// fix MAJOR R23-M7（codex 审查）：官方渠道 + level=off 通过 confirm 豁免必须留痕
	if confirmOfficialNoMod && body.ModerationLevel == "off" && channelTargetsOfficialHost(&ch) {
		auditOfficialNoModerationConfirmed(c, &ch, &body, "ADD")
	}

	// 强制锁定给路由中的渠道ID
	body.ChannelID = uint(channelID)

	if err := database.DB.Create(&body).Error; err != nil {
		log.Printf("Create channel model error: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHMOD_CREATE_FAILED",
			"message":      "Failed to bind new model price tier to channel",
		})
	}

	// 审核策略缓存失效（30s TTL，admin 改完立即生效）
	proxy.InvalidateModerationPolicyCache(body.ModelID)

	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success": true,
		"data":    newChannelModelResponse(body),
	})
}

// UpdateChannelModel 动态调优并修改某条渠道特定模型的权重或单价阶梯
func UpdateChannelModel(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "Invalid Matrix Binding ID",
		})
	}

	var payload channelModelPayload
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_BODY",
			"message":      "Invalid parser structure",
		})
	}
	body, err := payload.toChannelModel()
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_LIMIT",
			"message":      err.Error(),
		})
	}

	var chm database.ChannelModel
	if err := database.DB.First(&chm, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHMOD_NOT_FOUND",
			"message":      "Target channel model binding completely lost in DB",
		})
	}

	// 允许任意字段的灵活调价覆盖（仅限合法的覆盖模式）
	chm.DisplayName = body.DisplayName
	chm.InputPricePicoPerToken = body.InputPricePicoPerToken
	chm.OutputPricePicoPerToken = body.OutputPricePicoPerToken
	chm.CachedInputPricePicoPerToken = body.CachedInputPricePicoPerToken
	chm.CacheWriteInputPricePicoPerToken = body.CacheWriteInputPricePicoPerToken
	chm.CacheWrite1hInputPricePicoPerToken = body.CacheWrite1hInputPricePicoPerToken
	chm.ContextPriceThreshold = body.ContextPriceThreshold
	chm.HighInputPricePicoPerToken = body.HighInputPricePicoPerToken
	chm.HighCachedInputPricePicoPerToken = body.HighCachedInputPricePicoPerToken
	chm.HighOutputPricePicoPerToken = body.HighOutputPricePicoPerToken
	chm.MaxContextLength = body.MaxContextLength
	if body.Weight >= 0 {
		chm.Weight = body.Weight
	}
	if body.Status != 0 {
		chm.Status = body.Status
	}

	// fix CRITICAL R23：审核字段校验（更新路径）
	// 仅当请求体显式包含 moderation_level 才更新（gjson 探测原始字段是否出现，避免 zero-value 覆盖）
	rawBody := c.Body()
	if gjson.GetBytes(rawBody, "endpoint_policy").Exists() {
		chm.EndpointPolicy = body.EndpointPolicy
	}
	if gjson.GetBytes(rawBody, "moderation_level").Exists() {
		chm.ModerationLevel = body.ModerationLevel
	}
	if gjson.GetBytes(rawBody, "moderation_fail_mode").Exists() {
		chm.ModerationFailMode = body.ModerationFailMode
	}
	// 加载所属渠道用于官方 host 检测；找不到（orphan ChannelModel / 渠道被软删）→
	// 以"非家族"占位 channel 走 enum 校验，跳过官方 host 检查（更新仍允许，但 admin 看到 warn）
	var ch database.Channel
	if err := database.DB.Unscoped().First(&ch, chm.ChannelID).Error; err != nil {
		log.Printf("[CHMOD-UPDATE] orphan ChannelModel id=%d channel_id=%d not found: %v (treating as non-official for moderation validation)",
			chm.ID, chm.ChannelID, err)
		ch = database.Channel{Type: ""} // 空 type → channelTargetsOfficialHost 返回 false
	}
	confirmOfficialNoMod := gjson.GetBytes(rawBody, "confirm_official_no_moderation").Bool()
	if status, code, msg := validateChannelModelModeration(&chm, &ch, confirmOfficialNoMod); status != 0 {
		return c.Status(status).JSON(fiber.Map{
			"success":      false,
			"message_code": code,
			"message":      msg,
		})
	}
	if status, code, msg := validateChannelModelEndpointPolicy(&chm); status != 0 {
		return c.Status(status).JSON(fiber.Map{
			"success":      false,
			"message_code": code,
			"message":      msg,
		})
	}
	if err := database.ValidateChannelModelPricing(&chm); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_LIMIT",
			"message":      err.Error(),
		})
	}
	// fix MAJOR R23-M7：官方渠道 + level=off 通过 confirm 豁免必须留痕
	if confirmOfficialNoMod && chm.ModerationLevel == "off" && channelTargetsOfficialHost(&ch) {
		auditOfficialNoModerationConfirmed(c, &ch, &chm, "UPDATE")
	}

	if err := database.DB.Save(&chm).Error; err != nil {
		log.Printf("Update channel sub model error: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHMOD_UPDATE_FAILED",
			"message":      "Failed to synchronize multi-dimensional pricing to disk",
		})
	}

	// 审核策略缓存失效（modelID 维度，30s TTL 立即失效）
	proxy.InvalidateModerationPolicyCache(chm.ModelID)

	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success": true,
		"data":    newChannelModelResponse(chm),
	})
}

// RemoveChannelModel 物理剥除模型和渠道的绑定，从此该渠道不再接应该模型的流量
func RemoveChannelModel(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_INVALID_PARAMS",
			"message":      "Invalid Matrix Binding ID",
		})
	}

	// 删除前抓 modelID（用于审核策略缓存失效；删除后再查不到）
	var existing database.ChannelModel
	if err := database.DB.First(&existing, id).Error; err == nil {
		// 软删除策略：不阻塞业务，找不到也直接删
		defer proxy.InvalidateModerationPolicyCache(existing.ModelID)
	}

	if err := database.DB.Delete(&database.ChannelModel{}, id).Error; err != nil {
		log.Printf("Delete channel model error: %v", err)
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_CHMOD_DELETE_FAILED",
			"message":      "Failed to purge DB linking node",
		})
	}

	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Model Binding Matrix completely wiped.",
	})
}

// FetchUpstreamModels 探测并拉取远程渠道实际支持的所有可用的模型列表
func FetchUpstreamModels(c *fiber.Ctx) error {
	channelIDStr := c.Params("channelId")
	channelID, err := strconv.Atoi(channelIDStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}

	var ch database.Channel
	if err := database.DB.First(&ch, channelID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_CHANNEL_NOT_FOUND"})
	}

	// SSRF 防护（与 channel.go 一致策略）：scheme 白名单 + 拒绝云元数据/链路本地。
	// 允许 localhost / RFC1918（Ollama / vLLM 自部署常见模式）。
	// 实际 dial 时 stream.go 的 safeDialContext 再做 DNS 重绑定防御。
	if err := proxy.ValidateChannelURL(ch.BaseURL); err != nil {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message":      "渠道 BaseURL 无效或指向受限地址：" + err.Error(),
			"message_code": "ERR_UNSAFE_BASEURL",
		})
	}

	upstreamURL := strings.TrimRight(ch.BaseURL, "/")

	// fix Major（codex 第四轮）：原 fasthttp.DoTimeout 不走 safeDialContext，DNS rebinding
	// 防御被绕过——用户配置 BaseURL 为受控域名，校验时解析合法 IP，dial 时换成 169.254.169.254。
	// 改用 net/http + safeDialContext，与 stream.go 同级保护。
	httpClient := &http.Client{
		Timeout:       15 * time.Second,
		Transport:     proxy.SafeTransport(),
		CheckRedirect: proxy.RedirectGuard,
	}
	method := http.MethodGet
	headers := map[string]string{}

	switch ch.Type {
	case "anthropic":
		upstreamURL += "/v1/models"
		headers["x-api-key"] = ch.Key
		headers["anthropic-version"] = "2023-06-01"
	case "gemini":
		upstreamURL += "/v1beta/models?key=" + ch.Key
	default:
		upstreamURL += "/v1/models"
		headers["Authorization"] = "Bearer " + ch.Key
	}

	httpReq, reqErr := http.NewRequestWithContext(c.Context(), method, upstreamURL, nil)
	if reqErr != nil {
		log.Printf("[FETCH-MODELS] build req failed channel=%d: %v", ch.ID, reqErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_BUILD_REQUEST"})
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, httpErr := httpClient.Do(httpReq)
	if httpErr != nil {
		// fix Major（codex 第八轮）：原直接拼 err.Error() 给客户端。
		// upstreamURL 在 Gemini 通道下含 ?key=APIKEY，连接错误信息会把 key 回显到 admin UI；
		// 即便 admin 自己看，也违反"reveal=1 二次鉴权"的密钥保护策略。
		// 详细 err 仅服务端日志，对外脱敏。
		log.Printf("[FETCH-MODELS] dial failed channel=%d: %s", ch.ID, proxy.SanitizeErrorMessage(httpErr.Error(), 256))
		return c.Status(502).JSON(fiber.Map{"success": false, "message_code": "ERR_UPSTREAM_UNREACHABLE"})
	}
	defer httpResp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4*1024*1024)) // 4MB cap on model list

	if httpResp.StatusCode != 200 {
		// fix Major（codex 第八轮）：上游 4xx/5xx body 可能回显请求 URL（含 ?key=）或内部细节，
		// 不能原样回给前端。脱敏 + 仅暴露状态码。
		log.Printf("[FETCH-MODELS] non-200 channel=%d status=%d body=%s", ch.ID, httpResp.StatusCode, proxy.SanitizeErrorMessage(string(bodyBytes), 1024))
		return c.Status(httpResp.StatusCode).JSON(fiber.Map{"success": false, "message_code": "ERR_UPSTREAM_STATUS", "status": httpResp.StatusCode})
	}

	var modelList []string

	if ch.Type == "gemini" {
		result := gjson.GetBytes(bodyBytes, "models")
		result.ForEach(func(key, value gjson.Result) bool {
			name := value.Get("name").String()
			name = strings.TrimPrefix(name, "models/")
			modelList = append(modelList, name)
			return true
		})
	} else {
		// handle standard schema {"data": [{"id": ...}]}
		result := gjson.GetBytes(bodyBytes, "data")
		result.ForEach(func(key, value gjson.Result) bool {
			modelList = append(modelList, value.Get("id").String())
			return true
		})
	}

	sort.Strings(modelList)

	return c.JSON(fiber.Map{
		"success": true,
		"data":    modelList,
	})
}

// AddChannelModelsBatch 引擎级底层批量安全插入模型列表，忽略重复项
func AddChannelModelsBatch(c *fiber.Ctx) error {
	channelIDStr := c.Params("channelId")
	channelID, err := strconv.Atoi(channelIDStr)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}

	var payload struct {
		Models []string `json:"models"`
	}
	if err := c.BodyParser(&payload); err != nil || len(payload.Models) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_BODY"})
	}

	// fix CRITICAL R23：批量探测插入时为官方渠道默认开启"moderation+closed"，
	// 防止 admin "Fetch from api.openai.com → Add All" 后所有模型默认裸奔。
	// 非官方渠道（cloaked / 自部署）保持 "off+open" 默认（与 GORM tag 一致）。
	var ch database.Channel
	if err := database.DB.First(&ch, channelID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message_code": "ERR_CHANNEL_NOT_FOUND"})
	}
	defaultLevel := "off"
	defaultFailMode := "open"
	if channelTargetsOfficialHost(&ch) {
		defaultLevel = "moderation"
		defaultFailMode = "closed"
	}

	var toInsert []database.ChannelModel
	for _, m := range payload.Models {
		level := defaultLevel
		failMode := defaultFailMode
		if database.IsOpenAIModelID(m) {
			level = database.OpenAIModelModerationLevel
			failMode = database.OpenAIModelModerationFailMode
		}
		toInsert = append(toInsert, database.ChannelModel{
			ChannelID:          uint(channelID),
			ModelID:            m,
			DisplayName:        m,
			Weight:             1,
			Status:             1,
			ModerationLevel:    level,
			ModerationFailMode: failMode,
		})
	}
	modelIDs := make([]string, 0, len(toInsert))
	for _, m := range toInsert {
		modelIDs = append(modelIDs, m.ModelID)
	}
	var existing []database.ChannelModel
	if err := database.DB.Select("model_id").Where("channel_id = ? AND model_id IN ?", channelID, modelIDs).Find(&existing).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_DB_QUERY",
			"message":      err.Error(),
		})
	}
	existSet := make(map[string]bool, len(existing))
	for _, e := range existing {
		existSet[e.ModelID] = true
	}
	filtered := make([]database.ChannelModel, 0, len(toInsert)-len(existing))
	seenNew := make(map[string]bool, len(toInsert))
	for _, m := range toInsert {
		if !existSet[m.ModelID] {
			if seenNew[m.ModelID] {
				continue
			}
			seenNew[m.ModelID] = true
			filtered = append(filtered, m)
		}
	}
	inserted := int64(0)
	if len(filtered) > 0 {
		res := database.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&filtered)
		if res.Error != nil {
			log.Printf("[CHANNEL-MODEL-BATCH] insert failed channel=%d count=%d: %v", channelID, len(filtered), res.Error)
			return c.Status(500).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_DB_INSERT",
			})
		}
		inserted = res.RowsAffected
		// 审核策略缓存批量失效：每个新插入的 modelID 都清一次
		for _, m := range filtered {
			proxy.InvalidateModerationPolicyCache(m.ModelID)
		}
	}

	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success":   true,
		"message":   "Models successfully synchronized",
		"inserted":  inserted,
		"requested": len(toInsert),
	})
}
