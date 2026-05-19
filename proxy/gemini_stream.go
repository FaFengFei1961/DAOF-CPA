// Package proxy / gemini_stream.go
//
// M-R6 重构（2026-05-19）：从 gemini_native.go 1319 行单体抽出 stream 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
)

// handleStreamingGeminiResponse 处理 streamGenerateContent SSE 透传 + 流末 usage 抽取。
// 与 P1 image streaming 框架同样的 SetBodyStreamWriter 模式，但解析 Google SSE 格式。
func handleStreamingGeminiResponse(
	c *fiber.Ctx,
	user *database.User,
	token string,
	subToken *database.AccessToken,
	isSubToken bool,
	modelName string,
	geminiReq geminiNativeRequest,
	body []byte,
	upstream *selectedImageUpstream,
	prePrice geminiPriceResolution,
	fallbackUserOptIn bool,
	clientIP, path string,
	startTime time.Time,
	unlockBalance func(),
) error {
	statusCode := upstream.resp.StatusCode
	if statusCode < 200 || statusCode >= 300 {
		defer upstream.resp.Body.Close()
		if upstream.cancel != nil {
			defer upstream.cancel()
		}
		if unlockBalance != nil {
			unlockBalance()
		}
		bodyBytes, _ := io.ReadAll(upstream.resp.Body)
		log.Printf("[GEMINI-STREAM-UPSTREAM-ERR] channel=%d status=%d body=%s", upstream.route.ChannelID, statusCode, sanitizeError(truncForLog(bodyBytes, 1024), 1024))
		recordProxyApiLog(user.ID, token, modelName, statusCode, clientIP, startTime, path, "upstream_error", string(bodyBytes))
		c.Set("Content-Type", "application/json")
		return c.Status(statusCode).Send(bodyBytes)
	}

	copyImageResponseHeaders(c, upstream.resp.Header)
	if geminiReq.Alt == "" {
		c.Set("Content-Type", "text/event-stream")
	}
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	setModelAuditHeaders(c, modelName, modelName, fallbackUserOptIn, "")
	c.Status(statusCode)

	selectedChannelType := ""
	if upstream.channel != nil {
		selectedChannelType = upstream.channel.Type
	}

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[GEMINI-STREAM-PANIC] user=%d model=%s recovered: %v", user.ID, modelName, r)
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
		bufLimit := 16 * 1024 * 1024
		SysConfigMutex.RLock()
		if v := SysConfigCache["gemini_stream_scanner_buffer_bytes"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 256*1024 {
				bufLimit = n
			}
		}
		SysConfigMutex.RUnlock()
		scanner.Buffer(make([]byte, 64*1024), bufLimit)

		flushOrBail := func() bool {
			if err := w.Flush(); err != nil {
				log.Printf("[GEMINI-STREAM-CLIENT-DISCONNECT] user=%d model=%s err=%v", user.ID, modelName, err)
				return false
			}
			return true
		}

		var (
			lastChunkJSON      []byte
			imageCount         int
			clientDisconnected bool
			sawUsage           bool
		)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) > 0 {
				w.Write(line)
			}
			w.Write([]byte("\n"))
			if !flushOrBail() {
				clientDisconnected = true
				break
			}
			trimmed := bytes.TrimRight(line, "\r")
			payload := geminiSSEJsonPayload(trimmed)
			if len(payload) > 0 {
				lastChunkJSON = append(lastChunkJSON[:0], payload...)
				if gjson.GetBytes(payload, "usageMetadata").Exists() {
					sawUsage = true
				}
				imageCount += countGeminiInlineImages(payload)
			}
		}

		if err := scanner.Err(); err != nil {
			log.Printf("[GEMINI-STREAM-SCANNER-ERR] user=%d model=%s err=%v", user.ID, modelName, err)
		}

		performStreamingGeminiBilling(streamingGeminiBillingInput{
			User:                user,
			Token:               token,
			SubToken:            subToken,
			IsSubToken:          isSubToken,
			ModelName:           modelName,
			GeminiReq:           geminiReq,
			Body:                body,
			Upstream:            upstream,
			PrePrice:            prePrice,
			FallbackUserOptIn:   fallbackUserOptIn,
			ClientIP:            clientIP,
			Path:                path,
			StartTime:           startTime,
			SelectedChannelType: selectedChannelType,
			LastChunkJSON:       lastChunkJSON,
			ImageCount:          imageCount,
			SawUsage:            sawUsage,
			ClientDisconnected:  clientDisconnected,
			StatusCode:          statusCode,
		})
	})
	return nil
}

type streamingGeminiBillingInput struct {
	User                *database.User
	Token               string
	SubToken            *database.AccessToken
	IsSubToken          bool
	ModelName           string
	GeminiReq           geminiNativeRequest
	Body                []byte
	Upstream            *selectedImageUpstream
	PrePrice            geminiPriceResolution
	FallbackUserOptIn   bool
	ClientIP            string
	Path                string
	StartTime           time.Time
	SelectedChannelType string
	LastChunkJSON       []byte
	ImageCount          int
	SawUsage            bool
	ClientDisconnected  bool
	StatusCode          int
}

func performStreamingGeminiBilling(in streamingGeminiBillingInput) {
	needsPending := false
	reason := ""
	if in.ClientDisconnected {
		needsPending = true
		reason = "client disconnected before stream completed"
	} else if !in.SawUsage && in.ImageCount == 0 {
		needsPending = true
		reason = "stream ended without usageMetadata or image data"
	}

	var (
		actualPrice geminiPriceResolution
		priceErr    error
	)
	if !needsPending {
		actualPrice, priceErr = resolveGeminiActualPrice(in.ModelName, in.LastChunkJSON, in.Upstream.route)
		if priceErr != nil {
			needsPending = true
			reason = fmt.Sprintf("price resolve failed: %v", priceErr)
		}
	}

	billingResolution := ResolveBillingRules(in.ModelName, in.Body, actualPrice.ReasoningTokens, in.SelectedChannelType, in.FallbackUserOptIn).WithCosts(actualPrice.AmountMicroUSD)
	if needsPending {
		// 用 precheck estimate 写 pending
		pendingPrice := in.PrePrice
		pendingPrice.CostSource = "pending_reconcile"
		fallbackBilling := ResolveBillingRules(in.ModelName, in.Body, 0, in.SelectedChannelType, in.FallbackUserOptIn).WithCosts(pendingPrice.AmountMicroUSD)
		if id := recordGeminiPendingReconcile(in.User, in.Token, in.ModelName, in.GeminiReq, pendingPrice, fallbackBilling, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, reason); id == 0 {
			log.Printf("[GEMINI-STREAM-BILLING-CRITICAL] user=%d model=%s streamed but pending reconcile write failed", in.User.ID, in.ModelName)
		}
		return
	}

	chargedCostMicroUSD := billingResolution.ChargedCostMicroUSD
	commitDecision := Decide(EngineRequest{
		UserID:       in.User.ID,
		ModelName:    in.ModelName,
		InputTokens:  actualPrice.PromptTokens,
		OutputTokens: actualPrice.CompletionTokens,
		CostMicroUSD: chargedCostMicroUSD,
		IsPrecheck:   false,
	})
	if commitDecision.NeedsRetry {
		recordGeminiPendingReconcile(in.User, in.Token, in.ModelName, in.GeminiReq, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, "subscription commit failed")
		return
	}
	commitOK := commitDecision.Allowed && !commitDecision.FallbackToBalance
	if !commitOK && !in.User.BalanceConsumeEnabled {
		recordGeminiPendingReconcile(in.User, in.Token, in.ModelName, in.GeminiReq, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime, "subscription commit fell back to disabled balance")
		return
	}

	var (
		apiLogID                 uint
		effectiveRevenueMicroUSD int64
		referralReward           database.ReferralPaidSpendRewardResult
	)
	if commitOK {
		apiLogID = createGeminiApiLog(in.User.ID, in.Token, in.ModelName, in.GeminiReq, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime)
		subID := commitDecision.SubscriptionID
		if billErr := database.WriteBillingEntryNonFatal(database.BillingEntryInput{
			UserID:               in.User.ID,
			EntryType:            database.BillingTypeApiUsageSub,
			AmountUSD:            0,
			BalanceAfterUSD:      in.User.Quota,
			ModelName:            in.ModelName,
			TokensTotal:          actualPrice.PromptTokens + actualPrice.CompletionTokens,
			SourceSubscriptionID: &subID,
			RelatedType:          relatedTypeForApiLog(apiLogID),
			RelatedID:            apiLogID,
			Description:          fmt.Sprintf("套餐 · %s · gemini stream · %s", in.ModelName, FormatChargedCostForDescription(actualPrice.AmountMicroUSD, chargedCostMicroUSD)),
		}); billErr != nil {
			log.Printf("[GEMINI-STREAM-BILLING-AUDIT-FAIL] user=%d sub=%d model=%s: %v", in.User.ID, subID, in.ModelName, billErr)
		}
		effectiveRevenueMicroUSD = subscriptionRevenueMicroUSD(chargedCostMicroUSD, commitDecision.SubscriptionIsGranted)
		if apiLogID != 0 {
			RecordApiLogRevenue(apiLogID, database.RevenueSourceSubscription, effectiveRevenueMicroUSD, subID)
		}
	} else {
		apiLogID, effectiveRevenueMicroUSD, referralReward = deductGeminiBalanceAndLog(in.User, in.Token, in.ModelName, in.GeminiReq, actualPrice, billingResolution, in.SelectedChannelType, in.StatusCode, in.ClientIP, in.Path, in.StartTime)
	}

	if in.IsSubToken && effectiveRevenueMicroUSD > 0 {
		incrementSubTokenUsedQuota(in.Token, in.SubToken, effectiveRevenueMicroUSD)
	}
	if referralReward.ReferrerID != 0 && referralReward.RewardMicroUSD > 0 {
		RefreshUserAuth(referralReward.ReferrerID)
	}
	if apiLogID == 0 {
		log.Printf("[GEMINI-STREAM-BILLING-CRITICAL] user=%d model=%s streamed but api_log missing", in.User.ID, in.ModelName)
	}
}

// geminiSSEJsonPayload 从 SSE 一行抽 JSON payload（剥 "data: " 前缀，跳过 event/空行）。
func geminiSSEJsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte(":")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' && trimmed[0] != '[' {
		return nil
	}
	return trimmed
}
