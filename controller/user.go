package controller

import (
	"daof-cpa/database"
	"daof-cpa/proxy"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// MaxAdminQuotaUSD admin 直改用户额度的上限：1e9 USD（实际业务远不会到这个量级，
// 但有限上限可防 admin 误填 1e308 导致整个 quota 字段被科学记数污染财务汇总）。
//
// fix MAJOR M23-A6（codex 第二十三轮）：标准 JSON 不接 NaN/Inf，但接 1e308 这种超大有限数。
// 业务上 admin 改额度通常 ≤ $10000，10亿美元已远超合理范围，作为保护性上限。
const MaxAdminQuotaUSD = 1e9
const bulkQuotaPreviewUserLimit = 500

var (
	errLastActiveAdmin       = errors.New("last active admin cannot be disabled or deleted")
	errAdminStateRaced       = errors.New("admin state changed concurrently")
	errQuotaBelowPaidBalance = errors.New("quota cannot be reduced below paid quota")
	errOfflineTopupDuplicate = errors.New("offline topup payment reference already used")
	errOfflineTopupTarget    = errors.New("offline topup target must be normal user")
)

// validateAdminQuotaInput 校验 admin 输入的 quota / amount 值。
//
//   - NaN / Inf → 拒绝
//   - |v| > MaxAdminQuotaUSD → 拒绝（防误填超大数污染汇总）
//
// 不在此校验是否 ≥ 0 —— 部分场景允许负数（如 set quota=-10 用于客服补偿），由调用方决定语义。
func validateAdminQuotaInput(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("额度必须为有限数（NaN/Inf 非法）")
	}
	if math.Abs(v) > MaxAdminQuotaUSD {
		return fmt.Errorf("额度绝对值超过上限 %v USD，请检查输入", MaxAdminQuotaUSD)
	}
	return nil
}

func protectsPaidQuotaReduction(before database.User, nextQuotaMicro int64) bool {
	return nextQuotaMicro < before.Quota && nextQuotaMicro < before.PaidQuota
}

func GetUsers(c *fiber.Ctx) error {
	searchQuery := strings.TrimSpace(c.Query("search", ""))
	sortBy := c.Query("sort", "id_desc")

	// fix Major M2（claude security 第十五轮）：原实现无分页 + search 无长度限制
	// → admin 大用户量平台一次查询全表 OOM；恶意 search="%aaa…(10000 字符)…aaa%" 触发
	// 全表扫描 + WAL 单写者锁竞争。对照 AdminListSubscriptions 已有 ≥2 ≤64 字符校验。
	if len(searchQuery) > 64 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "search 不能超过 64 字符", "message_code": "ERR_SEARCH_TOO_LONG"})
	}
	if searchQuery != "" && len(searchQuery) < 2 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "search 至少 2 字符", "message_code": "ERR_SEARCH_TOO_SHORT"})
	}
	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.Query("page_size", "50"))
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}

	db := database.DB.Model(&database.User{})

	if searchQuery != "" {
		// 模糊匹配：Username / Phone / OAuth external_id（任一 active 第三方绑定的外部 ID）
		// 转义 LIKE 通配符 + ESCAPE 子句（codex 第十六轮）：SQLite/Postgres LIKE 默认不识别 \ 转义，
		// 必须显式 ESCAPE '\\' 才能让 \% 匹配字面 %。
		//
		// Phase H-3b：原直接 LIKE users.github_id 已删；改用子查询命中 oauth_identities，
		// 这样不论 provider 是 github / google 都能搜到。
		escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(searchQuery)
		searchParam := "%" + escaped + "%"
		db = db.Where(
			"username LIKE ? ESCAPE '\\' OR phone LIKE ? ESCAPE '\\' OR "+
				"id IN (SELECT user_id FROM oauth_identities WHERE external_id LIKE ? ESCAPE '\\' AND unlinked_at IS NULL)",
			searchParam, searchParam, searchParam,
		)
	}

	switch sortBy {
	case "id_asc":
		db = db.Order("id asc")
	case "id_desc":
		db = db.Order("id desc")
	case "quota_desc":
		db = db.Order("quota desc")
	case "quota_asc":
		db = db.Order("quota asc")
	case "status_desc":
		db = db.Order("status desc") // 2在前(封禁)
	case "status_asc":
		db = db.Order("status asc") // 1在前(活跃)
	default:
		db = db.Order("id desc")
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		log.Printf("[USERS-LIST] count failed: %v", err)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_QUERY"})
	}

	var users []database.User
	if err := db.Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "获取数据失败", "message_code": "ERR_FETCH_DATA_MATRIX"})
	}
	// fix CRITICAL Sprint2-M1：admin bulk 视图 scrub 敏感字段。
	// 旧实现外传完整 User struct（含 Token / PasswordHash），admin 可看到所有用户的
	// API token 明文。token 一旦被任意 admin 看到，等同于横向越权能调任意用户配额。
	// PasswordHash / Token 一并清零，仅保留 admin 决策所需字段。
	for i := range users {
		users[i].PasswordHash = ""
		users[i].Token = ""
	}
	// Phase H-3b：把每个 user 的活跃 OAuth identities 一并 attach，让 admin UI 显示
	// "已绑 GitHub / Google" 之类标签。
	// H-Audit M6：DB 加载失败时返回 identities_load_failed=true，admin UI 可显示警告 banner。
	identitiesByUser, identitiesLoadFailed := loadActiveOAuthIdentitiesForUsers(users)
	out := make([]AdminUserListItem, len(users))
	for i, u := range users {
		out[i] = AdminUserListItem{User: u, OAuthIdentities: identitiesByUser[u.ID]}
	}
	resp := fiber.Map{
		"success": true,
		"data":    out,
		"meta":    fiber.Map{"page": page, "page_size": pageSize, "total": total},
	}
	if identitiesLoadFailed {
		resp["identities_load_failed"] = true
	}
	return c.JSON(resp)
}

// AdminUserListItem 是 admin 用户列表的 wire 投影：嵌入 User 字段 + 附带活跃 OAuth 绑定。
type AdminUserListItem struct {
	database.User
	OAuthIdentities []AdminOAuthIdentitySummary `json:"oauth_identities"`
}

// AdminOAuthIdentitySummary 是 admin 视角下单条绑定的最小信息。
type AdminOAuthIdentitySummary struct {
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
}

// loadActiveOAuthIdentitiesForUsers 批量加载一组 user 的活跃 OAuth 绑定。
// 一次查询返回 map[user_id] -> []summary，按 (provider, external_id) 排序保持稳定。
// 失败时 log + 返回 (空 map, true)，让 caller 在响应中标记 identities_load_failed=true，
// admin UI 可以渲染"OAuth 列加载失败"banner 而不是误以为"用户全都没绑 OAuth"。
//
// fix H-Audit M6（2026-05-20）：原 best-effort 返空 map 让 admin 看到错误信息——
// 用户没绑 vs DB 失败无法区分，引发决策错误（如忽略 Google-only 用户当作密码用户）。
func loadActiveOAuthIdentitiesForUsers(users []database.User) (result map[uint][]AdminOAuthIdentitySummary, loadFailed bool) {
	result = map[uint][]AdminOAuthIdentitySummary{}
	if len(users) == 0 {
		return result, false
	}
	userIDs := make([]uint, len(users))
	for i, u := range users {
		userIDs[i] = u.ID
	}
	var rows []database.OAuthIdentity
	if err := database.DB.
		Where("user_id IN ? AND unlinked_at IS NULL", userIDs).
		Order("user_id ASC, provider ASC, linked_at ASC").
		Find(&rows).Error; err != nil {
		log.Printf("[USERS-LIST] load oauth_identities failed: %v", err)
		return result, true
	}
	for _, r := range rows {
		result[r.UserID] = append(result[r.UserID], AdminOAuthIdentitySummary{
			Provider:   r.Provider,
			ExternalID: r.ExternalID,
		})
	}
	return result, false
}

// 用户增量操作 Payload。Quota 是 API wire 层 USD float，handler 内转 micro_usd。
type UserPayload struct {
	Username  string  `json:"username"`
	Quota     float64 `json:"quota"`
	Status    *int    `json:"status,omitempty"`
	BanReason string  `json:"ban_reason"`
}

type offlineTopupPayload struct {
	AmountUSD        float64 `json:"amount_usd"`         // 本次入账的 USD 额度
	MoneyFen         int64   `json:"money_fen"`          // 线下实际收款金额，CNY/RMB 时单位为分
	CurrencyOriginal string  `json:"currency_original"`  // CNY/RMB/USD，默认 CNY
	PaymentMethod    string  `json:"payment_method"`     // wechat/alipay/bank/paypal/other
	ExternalTradeRef string  `json:"external_trade_ref"` // 线下收款凭证号，全局幂等
	Reason           string  `json:"reason"`             // 可选备注
}

func normalizeOfflineTopupMethod(method string) (string, bool) {
	method = strings.ToLower(strings.TrimSpace(method))
	if method == "" {
		method = "wechat"
	}
	switch method {
	case "wechat", "alipay", "bank", "paypal", "other":
		return method, true
	default:
		return "", false
	}
}

func offlineTopupMethodLabel(method string) string {
	switch method {
	case "wechat":
		return "微信转账"
	case "alipay":
		return "支付宝转账"
	case "bank":
		return "银行转账"
	case "paypal":
		return "PayPal 转账"
	default:
		return "其他线下收款"
	}
}

func validateAdminReason(reason string) error {
	if runeLen := len([]rune(reason)); runeLen > topupManualPaidReasonMaxLen {
		return fmt.Errorf("reason 长度不能超过 %d 字符（当前 %d）", topupManualPaidReasonMaxLen, runeLen)
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return errors.New("reason contains control char")
		}
	}
	return nil
}

// AdminCreateOfflineTopup POST /api/admin/users/:id/offline-topup
//
// 用于“用户没有走平台充值通道，而是通过微信/支付宝/银行等线下方式真实付款”的人工入账。
// 该入口和普通管理员调额不同：它代表真实收款，必须同时增加 quota 与 paid_quota，
// 并写入 topup 财务账单，使后续套餐购买/API 余额消费可以按自充来源参与拉新返佣。
func AdminCreateOfflineTopup(c *fiber.Ctx) error {
	op := loadAdminUser(c)
	if op == nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": "ERR_NO_AUTH"})
	}
	id, parseErr := strconv.Atoi(c.Params("id"))
	if parseErr != nil || id <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	var req offlineTopupPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_BAD_REQUEST"})
	}
	if err := validateAdminQuotaInput(req.AmountUSD); err != nil || req.AmountUSD <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OFFLINE_TOPUP_AMOUNT_REQUIRED"})
	}
	amountMicro, ok := database.USDToMicro(req.AmountUSD)
	if !ok || amountMicro <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OFFLINE_TOPUP_AMOUNT_REQUIRED"})
	}
	currency := strings.ToUpper(strings.TrimSpace(req.CurrencyOriginal))
	if currency == "" {
		currency = "CNY"
	}
	if currency == "RMB" {
		currency = "CNY"
	}
	if currency != "CNY" && currency != "USD" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_CURRENCY_INVALID"})
	}
	if currency == "CNY" && req.MoneyFen <= 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OFFLINE_TOPUP_ORIGINAL_AMOUNT_REQUIRED"})
	}
	if req.MoneyFen < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_OFFLINE_TOPUP_ORIGINAL_AMOUNT_REQUIRED"})
	}
	method, methodOK := normalizeOfflineTopupMethod(req.PaymentMethod)
	if !methodOK {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PAYMENT_METHOD_INVALID"})
	}
	externalRef := sanitizeExternalRef(strings.TrimSpace(req.ExternalTradeRef))
	if externalRef == "" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_EXTERNAL_REF_REQUIRED"})
	}
	reason := strings.TrimSpace(req.Reason)
	if err := validateAdminReason(reason); err != nil {
		if strings.Contains(err.Error(), "control") {
			return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_REASON_CTRL_CHAR"})
		}
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_REASON_TOO_LONG"})
	}

	now := time.Now()
	var target database.User
	var opLogID uint
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := lockUserForUpdate(tx, uint(id)); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		if err := tx.Select("id, username, role, status, quota, paid_quota").First(&target, uint(id)).Error; err != nil {
			return fmt.Errorf("read user: %w", err)
		}
		if target.Role != "user" {
			return errOfflineTopupTarget
		}
		nextQuota, ok := database.CheckedAddInt64(target.Quota, amountMicro)
		if !ok {
			return errPriceOverflow
		}
		nextPaidQuota, ok := database.CheckedAddInt64(target.PaidQuota, amountMicro)
		if !ok {
			return errPriceOverflow
		}

		outTradeNo := fmt.Sprintf("off:u%d:%d", target.ID, now.UnixNano())
		receipt := database.PaymentWebhookReceipt{
			Provider:      manualPaidReceiptProvider,
			Nonce:         externalRef,
			SignatureHash: signatureHash("offline-paid:" + outTradeNo + ":" + externalRef),
			OutTradeNo:    outTradeNo,
			RemoteIP:      c.IP(),
			Status:        "accepted_manual",
			Reason:        reason,
			ReceivedAt:    now,
		}
		if err := tx.Create(&receipt).Error; err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return errOfflineTopupDuplicate
			}
			return fmt.Errorf("insert offline receipt: %w", err)
		}

		if err := tx.Model(&database.User{}).
			Where("id = ?", target.ID).
			Updates(map[string]any{
				"quota":      nextQuota,
				"paid_quota": nextPaidQuota,
			}).Error; err != nil {
			return fmt.Errorf("update paid quota: %w", err)
		}

		amountOriginal := amountMicro
		if currency == "CNY" {
			amountOriginal = req.MoneyFen
		}
		details, _ := json.Marshal([]map[string]any{
			{
				"type":                 "OFFLINE_TOPUP",
				"target":               target.Username,
				"amount":               database.MicroToUSD(amountMicro),
				"amount_micro_usd":     amountMicro,
				"currency_original":    currency,
				"amount_original":      amountOriginal,
				"payment_method":       method,
				"external_trade_ref":   externalRef,
				"old_quota_micro":      target.Quota,
				"new_quota_micro":      nextQuota,
				"old_paid_quota_micro": target.PaidQuota,
				"new_paid_quota_micro": nextPaidQuota,
				"old":                  database.MicroToUSD(target.Quota),
				"new":                  database.MicroToUSD(nextQuota),
				"old_paid_quota":       database.MicroToUSD(target.PaidQuota),
				"new_paid_quota":       database.MicroToUSD(nextPaidQuota),
				"reason":               reason,
			},
		})
		var err error
		opLogID, err = LogOperationByTxReturning(tx, op.ID, target.ID, "admin", "OFFLINE_TOPUP", c.IP(), string(details))
		if err != nil {
			return fmt.Errorf("write audit: %w", err)
		}

		desc := fmt.Sprintf("线下收款入账 · %s · 凭证 %s · 入账 $%s",
			offlineTopupMethodLabel(method), externalRef, database.FormatMicroUSD(amountMicro))
		if currency == "CNY" {
			desc += " · 实收 ¥" + database.FormatFen(req.MoneyFen)
		}
		if reason != "" {
			desc += " · " + reason
		}
		if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
			UserID:           target.ID,
			OccurredAt:       now,
			EntryType:        database.BillingTypeTopup,
			AmountUSD:        amountMicro,
			BalanceAfterUSD:  nextQuota,
			RelatedType:      "operation_log",
			RelatedID:        opLogID,
			Description:      desc,
			CurrencyOriginal: currency,
			AmountOriginal:   amountOriginal,
		}); err != nil {
			return fmt.Errorf("write billing entry: %w", err)
		}
		return nil
	})
	if errors.Is(txErr, errOfflineTopupDuplicate) {
		return c.Status(409).JSON(fiber.Map{"success": false, "message_code": "ERR_OFFLINE_TOPUP_REF_DUPLICATE"})
	}
	if errors.Is(txErr, errOfflineTopupTarget) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_TARGET_NOT_USER"})
	}
	if errors.Is(txErr, errPriceOverflow) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PRICE_OVERFLOW"})
	}
	if txErr != nil {
		log.Printf("[OFFLINE-TOPUP] tx failed user=%d admin=%d err=%v", id, op.ID, txErr)
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_DB_TRANSACTION"})
	}

	proxy.RefreshUserAuth(target.ID)
	proxy.InvalidateUserSubscriptionCache(target.ID)

	title := readSysConfigCached("notif_topup_title", "充值成功")
	bodyTpl := readSysConfigCached("notif_topup_body", "您充值的 ¥{amount_rmb} 已到账，等额 {amount_usd} USD 已加入余额。")
	amountRMB := "-"
	if strings.EqualFold(req.CurrencyOriginal, "CNY") || strings.EqualFold(req.CurrencyOriginal, "RMB") || strings.TrimSpace(req.CurrencyOriginal) == "" {
		amountRMB = database.FormatFen(req.MoneyFen)
	}
	body := strings.ReplaceAll(bodyTpl, "{amount_rmb}", amountRMB)
	body = strings.ReplaceAll(body, "{amount_usd}", database.FormatMicroUSD(amountMicro))
	dedupKey := fmt.Sprintf("offline_topup:%d:%d", target.ID, opLogID)
	proxy.Dispatch(target.ID, "topup", "success", title, body,
		proxy.LinkBills("topup"), "查看账单", "operation_log", opLogID, &dedupKey)

	log.Printf("[OFFLINE-TOPUP] OK user=%d admin=%d ref=%q usd_micro=%d", target.ID, op.ID, externalRef, amountMicro)
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_OFFLINE_TOPUP_RECORDED"})
}

func UpdateUser(c *fiber.Ctx) error {
	id := c.Params("id")
	var req UserPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "数据解析异常", "message_code": "ERR_PARSE_EXCEPTION"})
	}
	// fix MAJOR M23-A6（codex 第二十三轮）：admin 改额度必须 finite + 上限校验。
	// 即使标准 JSON 不接受 NaN，超大有限数（1e308）仍可进入 quota 污染财务汇总。
	if err := validateAdminQuotaInput(req.Quota); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_INVALID_QUOTA"})
	}
	reqQuotaMicro, ok := database.USDToMicro(req.Quota)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "quota 非法", "message_code": "ERR_INVALID_QUOTA"})
	}

	var user database.User
	if err := database.DB.First(&user, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message": "未找到相关记录", "message_code": "ERR_NODE_GONE"})
	}
	effectiveStatus := user.Status
	if req.Status != nil {
		if *req.Status < 0 || *req.Status > 99 {
			return c.Status(400).JSON(fiber.Map{
				"success":      false,
				"message_code": "ERR_INVALID_USER_STATUS",
			})
		}
		effectiveStatus = *req.Status
	}

	var changes []map[string]interface{}
	if user.Username != req.Username {
		changes = append(changes, map[string]interface{}{"type": "USERNAME", "target": req.Username, "old": user.Username, "new": req.Username})
	}
	if user.Quota != reqQuotaMicro {
		// audit 日志字段：old/new 用 USD float（前端 formatCurrency 直接消费）；
		// 同时附加 *_micro 用于精确审计回溯
		changes = append(changes, map[string]interface{}{
			"type":      "QUOTA",
			"target":    req.Username,
			"old":       database.MicroToUSD(user.Quota),
			"new":       database.MicroToUSD(reqQuotaMicro),
			"old_micro": user.Quota,
			"new_micro": reqQuotaMicro,
		})
	}
	if req.Status != nil && user.Status != effectiveStatus {
		changes = append(changes, map[string]interface{}{"type": "STATUS", "target": req.Username, "old": user.Status, "new": effectiveStatus})
		if effectiveStatus == 2 {
			changes = append(changes, map[string]interface{}{"type": "BAN_REASON", "target": req.Username, "old": "", "new": req.BanReason})
		}
	} else if user.BanReason != req.BanReason {
		changes = append(changes, map[string]interface{}{"type": "BAN_REASON", "target": req.Username, "old": user.BanReason, "new": req.BanReason})
	}

	changelog := "[]"
	if len(changes) > 0 {
		b, _ := json.Marshal(changes)
		changelog = string(b)
	}

	// fix CRITICAL C2（codex+claude 第十五轮）：UpdateUser 改 quota 时必须同时写 BillingEntry。
	// fix CRITICAL C19-1（codex 第十九轮）：原实现 delta = req.Quota - user.Quota（user.Quota 是
	// 事务**外**快照）→ 如果 admin 编辑期间 user 并发充值/扣费，事务内 quota 已变化但 delta 仍按
	// admin 看到的旧值计算，账单与 quota 漂移。修复：事务内 lockUserForUpdate + 重读 before。
	op := loadAdminUser(c)
	adminID := uint(0)
	if op != nil {
		adminID = op.ID
	}
	txErr := database.DB.Transaction(func(tx *gorm.DB) error {
		// 锁 user 行 + 事务内重读 before（与购买/退款路径用同一锁路径）
		if err := lockUserForUpdate(tx, user.ID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		var before database.User
		if err := tx.Select("id, quota, paid_quota, role, status").First(&before, user.ID).Error; err != nil {
			return fmt.Errorf("read before: %w", err)
		}
		if protectsPaidQuotaReduction(before, reqQuotaMicro) {
			return errQuotaBelowPaidBalance
		}
		updateQ := tx.Model(&database.User{}).Where("id = ?", user.ID)
		if before.Role == "admin" && before.Status == 1 && effectiveStatus != 1 {
			var activeAdminCount int64
			if err := tx.Model(&database.User{}).
				Where("role = ? AND status = ? AND deleted_at IS NULL", "admin", 1).
				Count(&activeAdminCount).Error; err != nil {
				return fmt.Errorf("count active admins: %w", err)
			}
			if activeAdminCount <= 1 {
				return errLastActiveAdmin
			}
			updateQ = updateQ.Where("role = ? AND status = ?", "admin", 1)
		}
		res := updateQ.Updates(map[string]interface{}{
			"username":   req.Username,
			"quota":      reqQuotaMicro,
			"status":     effectiveStatus,
			"ban_reason": req.BanReason,
		})
		if res.Error != nil {
			return fmt.Errorf("update user: %w", res.Error)
		}
		if before.Role == "admin" && before.Status == 1 && effectiveStatus != 1 && res.RowsAffected == 0 {
			return errAdminStateRaced
		}
		// fix Major（codex 第十五轮）：审计入事务，并填真实 admin id（旧实现 operatorID=0 + tx 外）
		// 这把"用户更新 + 账单 + 审计"绑成同一原子单元；任一失败一起回滚，admin 必可追溯。
		//
		// fix MAJOR（多模型审计第二十五轮）：先写 OperationLog 拿到 ID，再让 BillingEntry.RelatedID
		// 关联到具体审计行（旧实现写 0 让账务追溯断流）。顺序：log → billing。
		opLogID, err := LogOperationByTxReturning(tx, adminID, user.ID, "admin", "UPDATE", c.IP(), changelog)
		if err != nil {
			return fmt.Errorf("write audit: %w", err)
		}
		// 用事务内 before 判断是否真改了 quota（admin 看到的旧值与 DB 实际可能不同）
		if before.Quota != reqQuotaMicro {
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          user.ID,
				EntryType:       database.BillingTypeAdminAdjust,
				AmountUSD:       reqQuotaMicro - before.Quota,
				BalanceAfterUSD: reqQuotaMicro,
				RelatedType:     "operation_log",
				RelatedID:       opLogID,
				Description:     userFriendlyAdminAdjustDescription(reqQuotaMicro - before.Quota),
			}); err != nil {
				return fmt.Errorf("write billing: %w", err)
			}
		}
		return nil
	})
	if txErr != nil {
		log.Printf("[USER-UPDATE] tx failed user=%d: %v", user.ID, txErr)
		if errors.Is(txErr, errLastActiveAdmin) {
			return c.Status(403).JSON(fiber.Map{"success": false, "message": "操作遭拒：无法封禁唯一的系统管理员", "message_code": "ERR_SUICIDE_PROTECTION_SEAL"})
		}
		if errors.Is(txErr, errAdminStateRaced) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message": "管理员状态已变化，请刷新后重试", "message_code": "ERR_UPDATE_CONFLICT"})
		}
		if errors.Is(txErr, errQuotaBelowPaidBalance) {
			return c.Status(400).JSON(fiber.Map{"success": false, "message": "普通调额不能把余额扣到自充余额以下", "message_code": "ERR_QUOTA_BELOW_PAID_BALANCE"})
		}
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "数据更新失败，存在冲突", "message_code": "ERR_UPDATE_CONFLICT"})
	}
	// fix CRITICAL（codex review --uncommitted）：原先 ban 即 RevokeSessionsForUser →
	// 浏览器 session 被撤销 → LookupUserBySession 返回 false → middleware 401 →
	// UserGuardAllowBanned 设计的"banned 用户能查 /user/me、提工单、看账单"appeal 流程
	// 完全死路。保留 session 让 banned 用户能 UI 申诉；middleware 用 c.Locals("user_banned")
	// 标记，控制器自行决定是否拒绝写动作。EvictUserToken 仍然撤销 API token cache
	// 防 banned 用户继续调 LLM。

	// ZERO-TRUST 防御：无论状态改成什么，都强力同步到高速内存里，瞬间实现封号！
	proxy.SyncCacheConfig()
	// 被封禁时精准淘汰 API token cache（LLM 路径用 Bearer API token，不是 session）。
	if effectiveStatus == 2 && user.Token != "" {
		proxy.EvictUserToken(user.Token)
	}

	// 封禁通知（仅在状态变成 2=banned 时触发；同日 dedup 防 admin 反复改 ban_reason 重发）
	if effectiveStatus == 2 && user.Status != 2 {
		title := readSysConfigCached("notif_security_ban_title", "您的账户已被限制")
		bodyTpl := readSysConfigCached("notif_security_ban_body", "原因：{reason}。如有疑问请提交工单。")
		reason := req.BanReason
		if reason == "" {
			reason = "未提供"
		}
		body := strings.ReplaceAll(bodyTpl, "{reason}", reason)
		dedupKey := fmt.Sprintf("ban:%d:%s", user.ID, time.Now().Format("2006-01-02"))
		// security 类强制送达（不被偏好屏蔽）
		proxy.Dispatch(user.ID, "security", "error", title, body, "", "",
			"user", user.ID, &dedupKey)
	}

	return c.JSON(fiber.Map{"success": true, "message": "更新操作已成功保存", "message_code": "SUCCESS_SYNC_UPDATE"})
}

// GetSelfData 路由必须挂 UserGuard，user 由中间件注入 Locals。
//
// API 边界单位约定：quota 是 USD float（前端友好）。内部存储为 int64 micro_usd，
// 这里在 JSON 序列化时通过 database.MicroToUSD 转回 USD float。
func GetSelfData(c *fiber.Ctx) error {
	user, err := getCurrentUser(c)
	if err != nil {
		return c.Status(401).JSON(fiber.Map{"success": false, "message_code": err.Error()})
	}
	// 封禁用户也要拿到 profile（含 status=2 + ban_reason），让前端能持续显示
	// 封禁横幅 + 引导提工单申诉。其它业务 controller 仍走 UserGuard 会被 403 拦截。
	// fix CRITICAL Sprint2-M1：自身路由也不暴露 token。
	// 旧实现每次 /api/user/me 都附带 token 明文 → 浏览器 devtools、Sentry/日志、
	// XSS 任意脚本都能采集。token 应通过专门的 reveal 接口 + 二次确认获取。
	return c.JSON(fiber.Map{
		"success": true,
		"data": map[string]interface{}{
			"id":         user.ID,
			"username":   user.Username,
			"role":       user.Role,
			"quota":      database.MicroToUSD(user.Quota),
			"status":     user.Status,
			"ban_reason": user.BanReason,
		},
	})
}

// BulkQuotaPayload 是批量调整额度的请求体。Amount 是 API wire 层 USD float。
type BulkQuotaPayload struct {
	UserIDs []uint  `json:"user_ids"`
	Mode    string  `json:"mode"`   // "add" / "sub" / "set"
	Amount  float64 `json:"amount"` // USD float
}

type BulkQuotaPreviewPayload struct {
	UserIDs   []int64 `json:"user_ids"`
	Action    string  `json:"action"`     // "add" / "subtract" / "set"
	AmountUSD float64 `json:"amount_usd"` // USD float
}

type bulkQuotaPreviewUser struct {
	ID           uint    `json:"id"`
	Username     string  `json:"username"`
	CurrentUSD   float64 `json:"current_usd"`
	PaidQuotaUSD float64 `json:"paid_quota_usd"`
	BonusUSD     float64 `json:"bonus_usd"`
	MinQuotaUSD  float64 `json:"min_quota_usd"`
	FutureUSD    float64 `json:"future_usd"`
	Protected    bool    `json:"protected"`
}

func bulkQuotaProtectedFloorMicro(currentMicro, paidQuotaMicro int64) int64 {
	if paidQuotaMicro <= 0 {
		return 0
	}
	if currentMicro < paidQuotaMicro {
		return currentMicro
	}
	return paidQuotaMicro
}

func bulkQuotaPreviewFutureMicro(currentMicro, paidQuotaMicro, amountMicro int64, action string) int64 {
	floorMicro := bulkQuotaProtectedFloorMicro(currentMicro, paidQuotaMicro)
	switch action {
	case "add":
		if amountMicro > 0 && currentMicro > math.MaxInt64-amountMicro {
			return math.MaxInt64
		}
		futureMicro := currentMicro + amountMicro
		if futureMicro < 0 {
			return 0
		}
		return futureMicro
	case "subtract":
		if currentMicro-amountMicro < floorMicro {
			return floorMicro
		}
		return currentMicro - amountMicro
	case "set":
		if amountMicro < floorMicro {
			return floorMicro
		}
		return amountMicro
	default:
		return currentMicro
	}
}

func dedupePositiveUserIDs(rawIDs []int64) ([]uint, error) {
	idSet := make(map[uint]struct{}, len(rawIDs))
	uniqIDs := make([]uint, 0, len(rawIDs))
	for _, rawID := range rawIDs {
		if rawID <= 0 {
			return nil, fmt.Errorf("bad user id")
		}
		id := uint(rawID)
		if _, ok := idSet[id]; !ok {
			idSet[id] = struct{}{}
			uniqIDs = append(uniqIDs, id)
		}
	}
	return uniqIDs, nil
}

// BulkAdjustQuotaPreview 批量额度调整预检，只读计算影响范围与未来余额。
func BulkAdjustQuotaPreview(c *fiber.Ctx) error {
	var req BulkQuotaPreviewPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "数据解析异常", "message_code": "ERR_PARSE_EXCEPTION"})
	}
	if len(req.UserIDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "未选择任何用户", "message_code": "ERR_EMPTY_SELECTION"})
	}
	if len(req.UserIDs) > bulkQuotaPreviewUserLimit {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": MessageCodeBulkPreviewLimit})
	}
	if req.Action != "add" && req.Action != "subtract" && req.Action != "set" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "无效的调整模式", "message_code": "ERR_INVALID_MODE"})
	}
	if err := validateAdminQuotaInput(req.AmountUSD); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_INVALID_QUOTA"})
	}
	if req.AmountUSD < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "金额不能为负", "message_code": "ERR_NEGATIVE_AMOUNT"})
	}
	amountMicro, ok := database.USDToMicro(req.AmountUSD)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "金额非法", "message_code": "ERR_INVALID_QUOTA"})
	}
	uniqIDs, err := dedupePositiveUserIDs(req.UserIDs)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "用户 ID 不合法", "message_code": "ERR_BAD_USER_ID"})
	}

	var users []database.User
	if err := database.DB.Select("id, username, quota, paid_quota").
		Where("id IN ? AND role = ?", uniqIDs, "user").
		Order("id ASC").
		Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "用户读取失败", "message_code": "ERR_READ_USERS"})
	}

	previewUsers := make([]bulkQuotaPreviewUser, 0, len(users))
	totalDeltaMicro := int64(0)
	for _, u := range users {
		floorMicro := bulkQuotaProtectedFloorMicro(u.Quota, u.PaidQuota)
		futureMicro := bulkQuotaPreviewFutureMicro(u.Quota, u.PaidQuota, amountMicro, req.Action)
		bonusMicro := u.Quota - u.PaidQuota
		if bonusMicro < 0 {
			bonusMicro = 0
		}
		totalDeltaMicro += futureMicro - u.Quota
		previewUsers = append(previewUsers, bulkQuotaPreviewUser{
			ID:           u.ID,
			Username:     u.Username,
			CurrentUSD:   database.MicroToUSD(u.Quota),
			PaidQuotaUSD: database.MicroToUSD(u.PaidQuota),
			BonusUSD:     database.MicroToUSD(bonusMicro),
			MinQuotaUSD:  database.MicroToUSD(floorMicro),
			FutureUSD:    database.MicroToUSD(futureMicro),
			Protected:    futureMicro != bulkQuotaPreviewFutureMicro(u.Quota, 0, amountMicro, req.Action),
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"affected_count":  len(previewUsers),
			"total_delta_usd": database.MicroToUSD(totalDeltaMicro),
			"users":           previewUsers,
		},
	})
}

// BulkAdjustQuota 批量调整额度。
// 安全约束：不允许调整 admin（避免误操作）；普通扣减不能越过 paid_quota 保护线。
func BulkAdjustQuota(c *fiber.Ctx) error {
	var req BulkQuotaPayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "数据解析异常", "message_code": "ERR_PARSE_EXCEPTION"})
	}
	if len(req.UserIDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "未选择任何用户", "message_code": "ERR_EMPTY_SELECTION"})
	}
	if req.Mode != "add" && req.Mode != "sub" && req.Mode != "set" {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "无效的调整模式", "message_code": "ERR_INVALID_MODE"})
	}
	// fix MAJOR M23-A6（codex 第二十三轮）：批量调整金额必须 finite + 上限校验
	if err := validateAdminQuotaInput(req.Amount); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": err.Error(), "message_code": "ERR_INVALID_QUOTA"})
	}
	if req.Amount < 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "金额不能为负", "message_code": "ERR_NEGATIVE_AMOUNT"})
	}
	amountMicro, ok := database.USDToMicro(req.Amount)
	if !ok {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "金额非法", "message_code": "ERR_INVALID_QUOTA"})
	}

	// 去重 user_ids（防止 admin 误传重复 ID 让计数虚高）
	idSet := make(map[uint]struct{}, len(req.UserIDs))
	uniqIDs := make([]uint, 0, len(req.UserIDs))
	for _, id := range req.UserIDs {
		if _, ok := idSet[id]; !ok {
			idSet[id] = struct{}{}
			uniqIDs = append(uniqIDs, id)
		}
	}

	var users []database.User
	if err := database.DB.Where("id IN ? AND role = ?", uniqIDs, "user").Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "用户读取失败", "message_code": "ERR_READ_USERS"})
	}

	// fix CRITICAL C2（codex+claude 第十五轮）：每用户的 quota 更新 + 审计 + 账单
	// 必须在同一事务内，否则任何一步失败都会让 BillingEntry 与 user.quota 不一致。
	// 之前实现：UPDATE → SELECT → LogOperation 三步独立，**完全不写 BillingEntry** →
	// 账单事实表与真实 quota 漂移。修复：每用户一笔事务，事务内写 admin_adjust 账单条目。
	//
	// fix Minor Phase 4-codex（第二十四轮）：原"单 user 失败 log + continue + 最终 success=true"
	// 让 admin 看到"成功"但部分 user 未生效。改为收集失败 user_ids + reason，
	// 任一失败返回 207 Multi-Status（HTTP 标准的 partial success），让前端能区分。
	op := loadAdminUser(c)
	updated := 0
	type bulkFailure struct {
		UserID   uint   `json:"user_id"`
		Username string `json:"username"`
		Reason   string `json:"reason"`
	}
	failures := make([]bulkFailure, 0)
	for _, u := range users {
		err := database.DB.Transaction(func(tx *gorm.DB) error {
			// fix CRITICAL C19-1（codex 第十九轮）：lockUserForUpdate + 事务内重读 before。
			// 之前 delta = after - u.Quota（u.Quota 来自事务外 Find），与并发充值/扣费交错时
			// before 可能已变化但仍按旧值算 delta，账单与 quota 漂移。
			if err := lockUserForUpdate(tx, u.ID); err != nil {
				return fmt.Errorf("lock user: %w", err)
			}
			var before database.User
			if err := tx.Select("id, quota, paid_quota").First(&before, u.ID).Error; err != nil {
				return fmt.Errorf("read before: %w", err)
			}
			if req.Mode == "set" && protectsPaidQuotaReduction(before, amountMicro) {
				return errQuotaBelowPaidBalance
			}

			// 用 SQL 表达式原子更新，避免"读-改-写"的 race 让并发额度操作互相覆盖。
			// SQLite 的 MAX 只能做聚合，所以用 CASE 表达"clamp 到 protected floor"。
			// fix MAJOR M22-A1 Phase 1：amountMicro 单位 micro_usd（与 quota 列一致）
			var expr interface{}
			switch req.Mode {
			case "add":
				expr = gorm.Expr("CASE WHEN quota + ? < 0 THEN 0 ELSE quota + ? END", amountMicro, amountMicro)
			case "sub":
				expr = gorm.Expr("CASE WHEN quota <= paid_quota THEN quota WHEN quota - ? < paid_quota THEN paid_quota ELSE quota - ? END", amountMicro, amountMicro)
			case "set":
				expr = amountMicro
			}
			if err := tx.Model(&database.User{}).Where("id = ?", u.ID).UpdateColumn("quota", expr).Error; err != nil {
				return fmt.Errorf("update quota: %w", err)
			}

			// 重新读最新 quota（DB 真值，不依赖应用层算）
			var after database.User
			if err := tx.Select("quota").First(&after, u.ID).Error; err != nil {
				return fmt.Errorf("re-select: %w", err)
			}

			// fix MAJOR（多模型审计第二十五轮）：先写 OperationLog 拿到 ID，再让
			// BillingEntry.RelatedID 关联到具体审计行（旧实现写 0 让账务追溯断流）。
			// 顺序：log → billing；同事务原子，任一失败一起回滚。
			delta := after.Quota - before.Quota
			adminID := uint(0)
			if op != nil {
				adminID = op.ID
			}

			// audit 日志字段：old/new/amount/delta 用 USD float（前端 formatCurrency 消费）；
			// 同时附加 *_micro 用于精确审计回溯
			change, _ := json.Marshal([]map[string]interface{}{
				{
					"type":         "BULK_QUOTA",
					"target":       u.Username,
					"mode":         req.Mode,
					"old":          database.MicroToUSD(before.Quota),
					"new":          database.MicroToUSD(after.Quota),
					"amount":       req.Amount,
					"delta":        database.MicroToUSD(delta),
					"old_micro":    before.Quota,
					"new_micro":    after.Quota,
					"amount_micro": amountMicro,
					"delta_micro":  delta,
				},
			})
			opLogID, err := LogOperationByTxReturning(tx, adminID, u.ID, "admin", "BULK_QUOTA", c.IP(), string(change))
			if err != nil {
				return err
			}

			// 写 BillingEntry(admin_adjust)：delta = after - before（都是事务内值，原子一致）
			if err := database.WriteBillingEntry(tx, database.BillingEntryInput{
				UserID:          u.ID,
				EntryType:       database.BillingTypeAdminAdjust,
				AmountUSD:       delta,
				BalanceAfterUSD: after.Quota,
				RelatedType:     "operation_log",
				RelatedID:       opLogID,
				Description:     userFriendlyAdminAdjustDescription(delta),
			}); err != nil {
				return fmt.Errorf("write billing: %w", err)
			}
			return nil
		})
		if err != nil {
			log.Printf("[BULK-QUOTA] user=%d failed: %v (collected as partial failure)", u.ID, err)
			reason := err.Error()
			if errors.Is(err, errQuotaBelowPaidBalance) {
				reason = "ERR_QUOTA_BELOW_PAID_BALANCE"
			}
			failures = append(failures, bulkFailure{
				UserID:   u.ID,
				Username: u.Username,
				Reason:   reason,
			})
			continue
		}
		updated++
	}

	proxy.SyncCacheConfig()

	// fix Minor Phase 4-codex：任一失败 → 207 Multi-Status；全成功 → 200。
	// 前端可读 failures 数组定位失败 user，提示 admin 处理。
	status := 200
	msgCode := "SUCCESS_BULK_QUOTA"
	if len(failures) > 0 {
		if updated == 0 {
			allPaidFloorFailures := true
			for _, f := range failures {
				if f.Reason != "ERR_QUOTA_BELOW_PAID_BALANCE" {
					allPaidFloorFailures = false
					break
				}
			}
			if allPaidFloorFailures {
				status = 400
				msgCode = "ERR_QUOTA_BELOW_PAID_BALANCE"
			} else {
				status = 500 // 全失败 → 500 提示严重故障
				msgCode = "ERR_BULK_QUOTA_ALL_FAILED"
			}
		} else {
			status = 207 // 部分成功 → Multi-Status
			msgCode = "PARTIAL_BULK_QUOTA"
		}
	}
	return c.Status(status).JSON(fiber.Map{
		"success":      len(failures) == 0,
		"updated":      updated,
		"failed":       len(failures),
		"failures":     failures,
		"message":      "批量额度调整完成",
		"message_code": msgCode,
	})
}

// BulkDeletePayload 是批量删除的请求体
type BulkDeletePayload struct {
	UserIDs []uint `json:"user_ids"`
}

// BulkDeleteUsers 批量删除普通用户。这里保留账务源表，只标记 users.deleted_at。
// 安全约束：admin 不可被批量删除（即便 ID 出现在请求里也会跳过）。
func BulkDeleteUsers(c *fiber.Ctx) error {
	var req BulkDeletePayload
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "数据解析异常", "message_code": "ERR_PARSE_EXCEPTION"})
	}
	if len(req.UserIDs) == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "未选择任何用户", "message_code": "ERR_EMPTY_SELECTION"})
	}

	var users []database.User
	if err := database.DB.Where("id IN ? AND role = ?", req.UserIDs, "user").Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "用户读取失败", "message_code": "ERR_READ_USERS"})
	}

	op := loadAdminUser(c)
	adminID := uint(0)
	if op != nil {
		adminID = op.ID
	}
	deleted := 0
	for _, u := range users {
		change, _ := json.Marshal([]map[string]interface{}{
			{"type": "BULK_DELETE", "target": u.Username, "user_id": u.ID},
		})
		err := database.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Delete(&database.User{}, u.ID).Error; err != nil {
				return fmt.Errorf("delete user: %w", err)
			}
			return LogOperationByTx(tx, adminID, u.ID, "admin", "BULK_DELETE", c.IP(), string(change))
		})
		if err != nil {
			continue
		}
		// fix Minor Mi22-4（codex 第二十二轮）：每个删除成功的用户都要清订阅缓存
		proxy.InvalidateUserSubscriptionCache(u.ID)
		deleted++
	}

	proxy.SyncCacheConfig()

	return c.JSON(fiber.Map{
		"success":      true,
		"deleted":      deleted,
		"skipped":      len(req.UserIDs) - deleted,
		"message":      "批量删除完成",
		"message_code": "SUCCESS_BULK_DELETE",
	})
}

func DeleteUser(c *fiber.Ctx) error {
	id := c.Params("id")
	var user database.User
	if err := database.DB.First(&user, id).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message": "未找到相关记录或已被删除", "message_code": "ERR_NOT_FOUND"})
	}

	op := loadAdminUser(c)
	adminID := uint(0)
	if op != nil {
		adminID = op.ID
	}
	delData := []map[string]interface{}{{"type": "DELETE", "target": user.Username}}
	delBytes, _ := json.Marshal(delData)
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		var current database.User
		if err := tx.Select("id, role, status").First(&current, user.ID).Error; err != nil {
			return fmt.Errorf("read current user: %w", err)
		}
		conditionalDeleteActiveAdmin := current.Role == "admin" && current.Status == 1
		if conditionalDeleteActiveAdmin {
			var activeAdminCount int64
			if err := tx.Model(&database.User{}).
				Where("role = ? AND status = ? AND deleted_at IS NULL", "admin", 1).
				Count(&activeAdminCount).Error; err != nil {
				return fmt.Errorf("count active admins: %w", err)
			}
			if activeAdminCount <= 1 {
				return errLastActiveAdmin
			}
		}
		deleteQ := tx.Where("id = ?", user.ID)
		if conditionalDeleteActiveAdmin {
			deleteQ = deleteQ.Where("role = ? AND status = ?", "admin", 1)
		}
		res := deleteQ.Delete(&database.User{})
		if res.Error != nil {
			return fmt.Errorf("delete user: %w", res.Error)
		}
		if conditionalDeleteActiveAdmin && res.RowsAffected == 0 {
			return errAdminStateRaced
		}
		// fix Major（codex 第十五轮）：审计入事务 + 真实 admin id
		return LogOperationByTx(tx, adminID, user.ID, "admin", "DELETE", c.IP(), string(delBytes))
	}); err != nil {
		if errors.Is(err, errLastActiveAdmin) {
			return c.Status(403).JSON(fiber.Map{"success": false, "message": "操作拦截：不可删除系统唯一的管理员", "message_code": "ERR_ADMIN_REQUIRED"})
		}
		if errors.Is(err, errAdminStateRaced) {
			return c.Status(409).JSON(fiber.Map{"success": false, "message": "管理员状态已变化，请刷新后重试", "message_code": "ERR_UPDATE_CONFLICT"})
		}
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "删除失败", "message_code": "ERR_DB_TRANSACTION"})
	}

	proxy.SyncCacheConfig()
	// fix Minor Mi22-4（codex 第二十二轮）：删除事务成功后，主动清该用户的订阅缓存。
	// SyncCacheConfig 不动 SubscriptionCache，短窗口内在途请求仍可拿到旧订阅快照 →
	// stream/billing 路径继续按已删用户的旧订阅做扣费决策，造成 silent 错误扣费。
	proxy.InvalidateUserSubscriptionCache(user.ID)

	return c.JSON(fiber.Map{"success": true, "message": "数据已删除", "message_code": "APP.DELETE_SUCCESS"})
}

func AdminPurgeUser(c *fiber.Ctx) error {
	if c.Query("confirm") != "YES_DELETE_ALL" {
		return c.Status(400).JSON(fiber.Map{
			"success":      false,
			"message_code": "ERR_PURGE_CONFIRM_REQUIRED",
		})
	}
	rawID, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil || rawID == 0 {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_INVALID_PARAMS"})
	}
	userID := uint(rawID)

	var user database.User
	if err := database.DB.Unscoped().First(&user, userID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"success": false, "message": "未找到相关记录或已被删除", "message_code": "ERR_NOT_FOUND"})
	}

	adminID := getOperatorID(c)
	details, _ := json.Marshal(map[string]any{
		"user_id":       user.ID,
		"previous_role": user.Role,
		"confirmed":     true,
	})
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := purgeUserDependents(tx, user.ID); err != nil {
			return err
		}
		if err := tx.Unscoped().Delete(&database.User{}, user.ID).Error; err != nil {
			return fmt.Errorf("purge user: %w", err)
		}
		return LogOperationByTx(tx, adminID, user.ID, "admin", "USER_PURGE_GDPR", c.IP(), string(details))
	}); err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "彻底删除失败", "message_code": "ERR_DB_TRANSACTION"})
	}

	proxy.SyncCacheConfig()
	proxy.InvalidateUserSubscriptionCache(user.ID)
	return c.JSON(fiber.Map{"success": true, "message_code": "SUCCESS_PURGED"})
}

func purgeDeleteWhere(tx *gorm.DB, model any, query string, args ...any) error {
	if !tx.Migrator().HasTable(model) {
		return nil
	}
	return tx.Unscoped().Where(query, args...).Delete(model).Error
}

func purgeExecIfTableExists(tx *gorm.DB, table string, sql string, args ...any) error {
	if !tx.Migrator().HasTable(table) {
		return nil
	}
	return tx.Exec(sql, args...).Error
}

// purgeUserDependents 在给定事务内清掉用户的衍生记录。OperationLog 保留并由调用方写入
// USER_PURGE_GDPR 专用审计；ApiLog 必须 raw SQL 删除以绕过 append-only hook。
func purgeUserDependents(tx *gorm.DB, userID uint) error {
	if err := purgeDeleteWhere(tx, &database.AccessToken{}, "user_id = ?", userID); err != nil {
		return err
	}
	if err := purgeDeleteWhere(tx, &database.UserSession{}, "user_id = ?", userID); err != nil {
		return err
	}
	if tx.Migrator().HasTable(&database.ApiLog{}) {
		if err := purgeExecIfTableExists(tx, "api_log_attributions",
			"DELETE FROM api_log_attributions WHERE api_log_id IN (SELECT id FROM api_logs WHERE user_id = ?)", userID); err != nil {
			return err
		}
		if err := purgeExecIfTableExists(tx, "api_log_cost_estimates",
			"DELETE FROM api_log_cost_estimates WHERE api_log_id IN (SELECT id FROM api_logs WHERE user_id = ?)", userID); err != nil {
			return err
		}
		// fix CRITICAL（codex audit-integrity）：补漏新加的 api_log_revenues 侧表清理。
		// 若漏删，GDPR purge 后侧表保留指向已删 api_logs 的孤儿行（含 effective_revenue_micro_usd
		// 金额），既违反 GDPR 又污染毛利报表。
		if err := purgeExecIfTableExists(tx, "api_log_revenues",
			"DELETE FROM api_log_revenues WHERE api_log_id IN (SELECT id FROM api_logs WHERE user_id = ?)", userID); err != nil {
			return err
		}
		if err := purgeExecIfTableExists(tx, "api_log_usage_lines",
			"DELETE FROM api_log_usage_lines WHERE api_log_id IN (SELECT id FROM api_logs WHERE user_id = ?)", userID); err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM api_logs WHERE user_id = ?", userID).Error; err != nil {
			return err
		}
	}
	if err := purgeExecIfTableExists(tx, "media_generation_jobs",
		"DELETE FROM media_generation_jobs WHERE user_id = ?", userID); err != nil {
		return err
	}
	if tx.Migrator().HasTable(&database.UserSubscription{}) {
		if err := purgeExecIfTableExists(tx, "subscription_usages",
			"DELETE FROM subscription_usages WHERE subscription_id IN (SELECT id FROM user_subscriptions WHERE user_id = ?)", userID); err != nil {
			return err
		}
		if err := purgeDeleteWhere(tx, &database.UserSubscription{}, "user_id = ?", userID); err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&database.TopupOrder{}) {
		// fix MEDIUM（codex audit-integrity）：payment_webhook_receipts 按 out_trade_no 关联，
		// 必须在 topup_orders 被删之前清理（子查询依赖父表）。
		if err := purgeExecIfTableExists(tx, "payment_webhook_receipts",
			"DELETE FROM payment_webhook_receipts WHERE out_trade_no IN (SELECT out_trade_no FROM topup_orders WHERE user_id = ?)", userID); err != nil {
			return err
		}
		if err := purgeExecIfTableExists(tx, "payment_webhook_receipts",
			"DELETE FROM payment_webhook_receipts WHERE provider = ? AND out_trade_no LIKE ?",
			manualPaidReceiptProvider, fmt.Sprintf("off:u%d:%%", userID)); err != nil {
			return err
		}
		if err := purgeExecIfTableExists(tx, "topup_refunds",
			"DELETE FROM topup_refunds WHERE topup_order_id IN (SELECT id FROM topup_orders WHERE user_id = ?)", userID); err != nil {
			return err
		}
		if err := purgeDeleteWhere(tx, &database.TopupOrder{}, "user_id = ?", userID); err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable(&database.BillingEntry{}) {
		if err := purgeExecIfTableExists(tx, "billing_reconciliations",
			"DELETE FROM billing_reconciliations WHERE billing_entry_id IN (SELECT id FROM billing_entries WHERE user_id = ?) OR adjustment_billing_entry_id IN (SELECT id FROM billing_entries WHERE user_id = ?)",
			userID, userID); err != nil {
			return err
		}
		if err := purgeDeleteWhere(tx, &database.BillingEntry{}, "user_id = ?", userID); err != nil {
			return err
		}
	}
	if err := purgeDeleteWhere(tx, &database.UserCoupon{}, "user_id = ?", userID); err != nil {
		return err
	}
	if tx.Migrator().HasTable(&database.Notification{}) {
		if err := purgeExecIfTableExists(tx, "notification_broadcast_targets",
			"DELETE FROM notification_broadcast_targets WHERE notification_id IN (SELECT id FROM notifications WHERE user_id = ?)", userID); err != nil {
			return err
		}
		if err := purgeDeleteWhere(tx, &database.Notification{}, "user_id = ?", userID); err != nil {
			return err
		}
	}
	if err := purgeDeleteWhere(tx, &database.NotificationPreference{}, "user_id = ?", userID); err != nil {
		return err
	}
	if tx.Migrator().HasTable(&database.Ticket{}) {
		if err := purgeExecIfTableExists(tx, "ticket_messages",
			"DELETE FROM ticket_messages WHERE ticket_id IN (SELECT id FROM tickets WHERE user_id = ?)", userID); err != nil {
			return err
		}
		if err := purgeDeleteWhere(tx, &database.Ticket{}, "user_id = ?", userID); err != nil {
			return err
		}
	}
	return nil
}
