// Package proxy / image_stream.go
//
// M-R6 重构（2026-05-19）：从 image_generation.go 1892 行单体抽出 stream 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
)

func handleStreamingImageResponse(
	c *fiber.Ctx,
	user *database.User,
	token string,
	subToken *database.AccessToken,
	isSubToken bool,
	req imageGenerationRequest,
	body []byte,
	upstream *selectedImageUpstream,
	prePrice imagePriceResolution,
	fallbackUserOptIn bool,
	clientIP, path string,
	startTime time.Time,
	unlockBalance func(),
) error {
	statusCode := upstream.resp.StatusCode
	if statusCode < 200 || statusCode >= 300 {
		// 非 2xx 通常不是 SSE，按非流式错误回退处理
		defer upstream.resp.Body.Close()
		if upstream.cancel != nil {
			defer upstream.cancel()
		}
		if unlockBalance != nil {
			unlockBalance()
		}
		bodyBytes, _ := io.ReadAll(upstream.resp.Body)
		log.Printf("[IMAGE-STREAM-UPSTREAM-ERR] channel=%d status=%d body=%s", upstream.route.ChannelID, statusCode, sanitizeError(truncForLog(bodyBytes, 1024), 1024))
		recordProxyApiLog(user.ID, token, req.Model, statusCode, clientIP, startTime, path, "upstream_error", string(bodyBytes))
		c.Set("Content-Type", "application/json")
		return c.Status(statusCode).JSON(fiber.Map{"error": fiber.Map{
			"message": fmt.Sprintf("upstream returned %d", statusCode),
			"type":    "upstream_error",
		}})
	}

	copyImageResponseHeaders(c, upstream.resp.Header)
	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	setModelAuditHeaders(c, req.Model, req.Model, fallbackUserOptIn, "")
	c.Status(statusCode)

	selectedChannelType := ""
	if upstream.channel != nil {
		selectedChannelType = upstream.channel.Type
	}

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[IMAGE-STREAM-PANIC] user=%d model=%s recovered: %v", user.ID, req.Model, r)
			}
			_ = upstream.resp.Body.Close()
			if upstream.cancel != nil {
				upstream.cancel()
			}
			if unlockBalance != nil {
				unlockBalance()
			}
		}()

		scanner := bufio.NewScanner(upstream.resp.Body)
		// 图像 b64 chunk（特别是 partial_image）可能很大，默认 16MB，可由 SysConfig 调整
		bufLimit := 16 * 1024 * 1024
		SysConfigMutex.RLock()
		if v := SysConfigCache["image_stream_scanner_buffer_bytes"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 256*1024 {
				bufLimit = n
			}
		}
		SysConfigMutex.RUnlock()
		scanner.Buffer(make([]byte, 64*1024), bufLimit)

		flushOrBail := func() bool {
			if err := w.Flush(); err != nil {
				log.Printf("[IMAGE-STREAM-CLIENT-DISCONNECT] user=%d model=%s err=%v", user.ID, req.Model, err)
				return false
			}
			return true
		}

		var (
			currentEvent       string
			completedDataJSON  []byte
			sawCompleted       bool
			clientDisconnected bool
		)

		for scanner.Scan() {
			line := scanner.Bytes()

			// 透传上游字节给客户端（保留 SSE 帧结构）
			if len(line) > 0 {
				w.Write(line)
			}
			w.Write([]byte("\n"))
			if !flushOrBail() {
				clientDisconnected = true
				break
			}

			// 解析 SSE 行（仅 inspect，不破坏透传）
			trimmed := bytes.TrimRight(line, "\r")
			if len(trimmed) == 0 {
				currentEvent = ""
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("event: ")) {
				currentEvent = string(bytes.TrimPrefix(trimmed, []byte("event: ")))
				continue
			}
			if bytes.HasPrefix(trimmed, []byte("data: ")) {
				if currentEvent == "image_generation.completed" || currentEvent == "image_edit.completed" {
					dataBytes := bytes.TrimPrefix(trimmed, []byte("data: "))
					if len(dataBytes) > 0 && dataBytes[0] == '{' {
						completedDataJSON = append([]byte(nil), dataBytes...)
						sawCompleted = true
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("[IMAGE-STREAM-SCANNER-ERR] user=%d model=%s err=%v (consider raising image_stream_scanner_buffer_bytes)", user.ID, req.Model, err)
		}

		performStreamingImageBilling(streamingImageBillingInput{
			User:                user,
			Token:               token,
			SubToken:            subToken,
			IsSubToken:          isSubToken,
			Req:                 req,
			Body:                body,
			Upstream:            upstream,
			PrePrice:            prePrice,
			FallbackUserOptIn:   fallbackUserOptIn,
			ClientIP:            clientIP,
			Path:                path,
			StartTime:           startTime,
			SelectedChannelType: selectedChannelType,
			CompletedData:       completedDataJSON,
			SawCompleted:        sawCompleted,
			ClientDisconnected:  clientDisconnected,
			StatusCode:          statusCode,
		})
	})

	return nil
}

type streamingImageBillingInput struct {
	User                *database.User
	Token               string
	SubToken            *database.AccessToken
	IsSubToken          bool
	Req                 imageGenerationRequest
	Body                []byte
	Upstream            *selectedImageUpstream
	PrePrice            imagePriceResolution
	FallbackUserOptIn   bool
	ClientIP            string
	Path                string
	StartTime           time.Time
	SelectedChannelType string
	CompletedData       []byte
	SawCompleted        bool
	ClientDisconnected  bool
	StatusCode          int
}

// performStreamingImageBilling 在 SetBodyStreamWriter callback 内执行，复用非流式同款
// 计费决策链路（commit / 套餐 / 余额 / referral）。
//
// 三种入口：
//  1. 客户端断连 → pending reconcile（按 precheck estimate）
//  2. 流结束但没见 completed event → pending reconcile（按 precheck estimate）
//  3. 完整收到 completed event + 有 usage → resolveImageActualPrice 真实计费
func performStreamingImageBilling(in streamingImageBillingInput) {
	needsPending := false
	reconcileReason := ""

	if in.ClientDisconnected {
		needsPending = true
		reconcileReason = "client disconnected before stream completed"
	} else if !in.SawCompleted {
		needsPending = true
		reconcileReason = "stream ended without completed event"
	}

	var (
		actualPrice imagePriceResolution
		priceErr    error
	)
	if !needsPending {
		actualPrice, priceErr = resolveImageActualPrice(in.Req, in.CompletedData, in.Upstream.route)
		if priceErr != nil {
			if errors.Is(priceErr, errImageTokenUsageUnavailable) {
				needsPending = true
				reconcileReason = "completed event omitted billable usage"
			} else {
				log.Printf("[IMAGE-STREAM-BILLING-CRITICAL] user=%d model=%s stream completed price resolve failed: %v", in.User.ID, in.Req.Model, priceErr)
				// 计费失败但已交付：仍记 pending reconcile，避免免费消耗
				needsPending = true
				reconcileReason = fmt.Sprintf("price resolve failed: %v", priceErr)
			}
		}
	}

	if needsPending {
		pendingPrice := imagePriceResolution{
			BillingMode:    database.BillingModeToken,
			Quantity:       1,
			AmountMicroUSD: in.PrePrice.AmountMicroUSD,
			ResponseImages: 1,
			CostSource:     "pending_reconcile",
		}
		billingResolution := ResolveBillingRules(in.Req.Model, in.Body, 0, in.SelectedChannelType, in.FallbackUserOptIn).WithCosts(pendingPrice.AmountMicroUSD)
		apiLogID := recordImagePendingReconcile(in.User, in.Token, in.Req, pendingPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, reconcileReason)
		if apiLogID == 0 {
			log.Printf("[IMAGE-STREAM-BILLING-CRITICAL] user=%d model=%s streamed but pending reconcile write failed", in.User.ID, in.Req.Model)
		}
		return
	}

	billingResolution := ResolveBillingRules(in.Req.Model, in.Body, 0, in.SelectedChannelType, in.FallbackUserOptIn).WithCosts(actualPrice.AmountMicroUSD)
	chargedCostMicroUSD := billingResolution.ChargedCostMicroUSD

	commitDecision := Decide(EngineRequest{
		UserID:       in.User.ID,
		ModelName:    in.Req.Model,
		InputTokens:  imageDecisionInputUnits(actualPrice),
		OutputTokens: imageDecisionOutputUnits(actualPrice),
		CostMicroUSD: chargedCostMicroUSD,
		IsPrecheck:   false,
	})
	if commitDecision.NeedsRetry {
		recordImagePendingReconcile(in.User, in.Token, in.Req, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, "subscription commit failed")
		return
	}
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	if !commitOK && !in.User.BalanceConsumeEnabled {
		recordImagePendingReconcile(in.User, in.Token, in.Req, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, "subscription commit fell back to disabled balance")
		return
	}

	var (
		apiLogID                 uint
		effectiveRevenueMicroUSD int64
		referralReward           database.ReferralPaidSpendRewardResult
	)
	if commitOK {
		apiLogID = createImageApiLog(in.User.ID, in.Token, in.Req, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime)
		subID := commitDecision.SubscriptionID
		if billErr := database.WriteBillingEntryNonFatal(database.BillingEntryInput{
			UserID:               in.User.ID,
			EntryType:            database.BillingTypeApiUsageSub,
			AmountUSD:            0,
			BalanceAfterUSD:      in.User.Quota,
			ModelName:            in.Req.Model,
			TokensTotal:          imageTokenTotal(actualPrice),
			SourceSubscriptionID: &subID,
			RelatedType:          relatedTypeForApiLog(apiLogID),
			RelatedID:            apiLogID,
			Description:          fmt.Sprintf("套餐 · %s · %s · %s · stream", in.Req.Model, imageUsageDescription(actualPrice), FormatChargedCostForDescription(actualPrice.AmountMicroUSD, chargedCostMicroUSD)),
		}); billErr != nil {
			log.Printf("[IMAGE-STREAM-BILLING-AUDIT-FAIL] user=%d sub=%d model=%s: %v", in.User.ID, subID, in.Req.Model, billErr)
		}
		effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
		if apiLogID != 0 {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
		}
	} else {
		apiLogID, effectiveRevenueMicroUSD, referralReward = deductImageBalanceAndLog(in.User, in.Token, in.Req, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime)
	}

	if in.IsSubToken && effectiveRevenueMicroUSD > 0 {
		incrementSubTokenUsedQuota(in.Token, in.SubToken, effectiveRevenueMicroUSD)
	}
	if referralReward.ReferrerID != 0 && referralReward.RewardMicroUSD > 0 {
		RefreshUserAuth(referralReward.ReferrerID)
	}
	if apiLogID == 0 {
		log.Printf("[IMAGE-STREAM-BILLING-CRITICAL] user=%d model=%s streamed but api_log missing", in.User.ID, in.Req.Model)
	}
}
