// Package proxy / video_edits.go
//
// /v1/videos/edits + /v1/videos/extensions 端点的请求解析 + handler。
//
// xAI 上游对 generations / edits / extensions **三个 handler 共用同一个**
// handleXAIVideosNativePost（CPA@feebe6c7 sdk/api/handlers/openai/openai_videos_handlers.go），
// 即完全透传 client native JSON。DAOF 因此也透传 client 原始 body，仅做：
//   - 模型白名单（grok-imagine-video）
//   - file_id 拒绝（任意位置）
//   - multipart 视频文件 ≤ 100MB，最多 7 张参考图
//   - 流式 stream=true 拒绝（xAI 视频生成异步，无 SSE 协议）
//
// 计费仍走 upstream cost_in_usd_ticks（精确，含输入秒数 + 输出秒数）。
// precheck 估算用保守 15s × 720p 上限，避免用户在配额紧张时被绕过限额。
package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"strconv"
	"strings"

	"daof-cpa/database"

	"github.com/gofiber/fiber/v2"
	"github.com/tidwall/gjson"
)

const (
	// maxVideoEditFileBytes 单个视频文件上限 100MB，避免一次请求 OOM。
	maxVideoEditFileBytes = 100 * 1024 * 1024
	// maxVideoEditReferences 与 CPA / xAI 上限对齐：edits 最多 7 张参考图。
	maxVideoEditReferences = 7
	// videoEditMaxPrecheckSeconds edits/extensions 端点 precheck 估算用的保守上限秒数，
	// 实际计费走 cost_in_usd_ticks。
	videoEditMaxPrecheckSeconds = int64(15)
)

// VideoEditProxyHandler 处理 POST /v1/videos/edits。
func VideoEditProxyHandler(c *fiber.Ctx) error {
	return processVideoRequest(c, database.EndpointVideosEdits, parseVideoEditRequest)
}

// VideoExtensionProxyHandler 处理 POST /v1/videos/extensions。
func VideoExtensionProxyHandler(c *fiber.Ctx) error {
	return processVideoRequest(c, database.EndpointVideosExtensions, parseVideoExtensionRequest)
}

// parseVideoEditRequest 是 /v1/videos/edits 的统一入口；按 Content-Type 走 multipart 或 JSON。
func parseVideoEditRequest(c *fiber.Ctx) (videoGenerationRequest, []byte, error) {
	contentType := strings.ToLower(strings.TrimSpace(c.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return parseVideoEditMultipart(c)
	}
	rawBody := c.Body()
	body := make([]byte, len(rawBody))
	copy(body, rawBody)
	return parseVideoEditJSON(body)
}

// parseVideoExtensionRequest 是 /v1/videos/extensions 的统一入口。
// extensions 不需要 input 视频文件（基于已有 request_id 扩展），但仍可能带 multipart
// 元数据（rare），为了一致性走相同路径。
func parseVideoExtensionRequest(c *fiber.Ctx) (videoGenerationRequest, []byte, error) {
	contentType := strings.ToLower(strings.TrimSpace(c.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return parseVideoExtensionMultipart(c)
	}
	rawBody := c.Body()
	body := make([]byte, len(rawBody))
	copy(body, rawBody)
	return parseVideoExtensionJSON(body)
}

// parseVideoEditJSON 解析 /v1/videos/edits 的 JSON 请求体。
// 不严格 sanitize 字段（xAI native 协议变化快），仅拒绝 file_id + stream，要求 input 媒体。
func parseVideoEditJSON(body []byte) (videoGenerationRequest, []byte, error) {
	if len(body) == 0 {
		return videoGenerationRequest{}, nil, fmt.Errorf("request body is required")
	}
	if !gjson.ValidBytes(body) {
		return videoGenerationRequest{}, nil, fmt.Errorf("request body must be valid JSON")
	}

	if err := rejectVideoFileIDFields(body); err != nil {
		return videoGenerationRequest{}, nil, err
	}

	req, err := decodeVideoEditRequest(body)
	if err != nil {
		return videoGenerationRequest{}, nil, err
	}
	if req.Stream != nil && *req.Stream {
		return videoGenerationRequest{}, nil, fmt.Errorf("streaming video edit is not supported")
	}

	if !videoEditHasInputMedia(body, req) {
		return videoGenerationRequest{}, nil, fmt.Errorf("/v1/videos/edits requires at least one input media (video / image / image_url / reference_images / input_reference)")
	}

	// edits 路径 precheck 用保守 15s 上限；实际计费走 cost_in_usd_ticks
	req.DurationSeconds = videoEditMaxPrecheckSeconds
	// edits 输出默认 720p（高保 precheck 估算），实际可能由上游决定
	if strings.TrimSpace(req.Resolution) == "" {
		req.Resolution = "720p"
	}
	// 透传原始 body 给上游（上游 CPA 完全透传至 xAI native）
	return req, body, nil
}

// parseVideoExtensionJSON 解析 /v1/videos/extensions 的 JSON 请求体。
// 必填：request_id（被扩展的源视频 id）。
func parseVideoExtensionJSON(body []byte) (videoGenerationRequest, []byte, error) {
	if len(body) == 0 {
		return videoGenerationRequest{}, nil, fmt.Errorf("request body is required")
	}
	if !gjson.ValidBytes(body) {
		return videoGenerationRequest{}, nil, fmt.Errorf("request body must be valid JSON")
	}

	if err := rejectVideoFileIDFields(body); err != nil {
		return videoGenerationRequest{}, nil, err
	}

	req, err := decodeVideoEditRequest(body)
	if err != nil {
		return videoGenerationRequest{}, nil, err
	}
	if req.Stream != nil && *req.Stream {
		return videoGenerationRequest{}, nil, fmt.Errorf("streaming video extension is not supported")
	}

	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(gjson.GetBytes(body, "request_id").String())
	}
	if requestID == "" {
		return videoGenerationRequest{}, nil, fmt.Errorf("/v1/videos/extensions requires request_id of the source video")
	}
	if !validVideoRequestID(requestID) {
		return videoGenerationRequest{}, nil, fmt.Errorf("invalid request_id format")
	}
	req.RequestID = requestID

	req.DurationSeconds = videoEditMaxPrecheckSeconds
	if strings.TrimSpace(req.Resolution) == "" {
		req.Resolution = "720p"
	}
	return req, body, nil
}

// parseVideoEditMultipart 解析 multipart/form-data：把视频/参考图文件转 b64 data URL，
// 构造等价 JSON 然后调 parseVideoEditJSON。
func parseVideoEditMultipart(c *fiber.Ctx) (videoGenerationRequest, []byte, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return videoGenerationRequest{}, nil, fmt.Errorf("multipart parse failed: %w", err)
	}

	getValue := func(key string) string {
		if vals := form.Value[key]; len(vals) > 0 {
			return strings.TrimSpace(vals[0])
		}
		return ""
	}

	payload := map[string]any{
		"model": getValue("model"),
	}
	if v := getValue("prompt"); v != "" {
		payload["prompt"] = v
	}

	// 视频文件：video 字段（单文件）。
	var hasInput bool
	if files, ok := form.File["video"]; ok && len(files) > 0 {
		dataURL, err := multipartVideoFileToDataURL(files[0])
		if err != nil {
			return videoGenerationRequest{}, nil, fmt.Errorf("read video upload: %w", err)
		}
		payload["video"] = map[string]any{"video_url": dataURL}
		hasInput = true
	}

	// 输入参考图（image-to-video）：image / images / reference_images。
	for _, key := range []string{"image", "image[]"} {
		if files, ok := form.File[key]; ok && len(files) > 0 {
			dataURL, err := multipartVideoFileToDataURL(files[0])
			if err != nil {
				return videoGenerationRequest{}, nil, fmt.Errorf("read image upload: %w", err)
			}
			payload["image"] = map[string]any{"image_url": dataURL}
			hasInput = true
			break
		}
	}

	for _, key := range []string{"reference_images", "reference_images[]"} {
		if files, ok := form.File[key]; ok && len(files) > 0 {
			if len(files) > maxVideoEditReferences {
				return videoGenerationRequest{}, nil, fmt.Errorf("too many reference_images (max %d)", maxVideoEditReferences)
			}
			refs := make([]map[string]any, 0, len(files))
			for _, fh := range files {
				dataURL, err := multipartVideoFileToDataURL(fh)
				if err != nil {
					return videoGenerationRequest{}, nil, fmt.Errorf("read reference_images upload: %w", err)
				}
				refs = append(refs, map[string]any{"image_url": dataURL})
			}
			payload["reference_images"] = refs
			hasInput = true
			break
		}
	}

	// 文本字段 fallback（client 用 form value 传 URL 而不是文件）
	for _, key := range []string{"image_url", "video_url"} {
		if v := getValue(key); v != "" && !hasInput {
			payload[key] = v
			hasInput = true
		}
	}

	if !hasInput {
		return videoGenerationRequest{}, nil, fmt.Errorf("/v1/videos/edits requires at least one input media (video / image / reference_images)")
	}

	for _, key := range []string{"aspect_ratio", "resolution", "size", "seconds", "duration"} {
		if v := getValue(key); v != "" {
			payload[key] = v
		}
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return videoGenerationRequest{}, nil, fmt.Errorf("build edits JSON from multipart: %w", err)
	}
	return parseVideoEditJSON(jsonBody)
}

// parseVideoExtensionMultipart 解析 /v1/videos/extensions multipart。
// extensions 不需要文件上传，只需要 request_id + extend_seconds。但保留 multipart 入口
// 以保证 SDK 兼容性。
func parseVideoExtensionMultipart(c *fiber.Ctx) (videoGenerationRequest, []byte, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return videoGenerationRequest{}, nil, fmt.Errorf("multipart parse failed: %w", err)
	}

	getValue := func(key string) string {
		if vals := form.Value[key]; len(vals) > 0 {
			return strings.TrimSpace(vals[0])
		}
		return ""
	}

	payload := map[string]any{
		"model":      getValue("model"),
		"request_id": getValue("request_id"),
	}
	if v := getValue("extend_seconds"); v != "" {
		if n, parseErr := strconv.ParseInt(v, 10, 64); parseErr == nil {
			payload["extend_seconds"] = n
		}
	}
	if v := getValue("prompt"); v != "" {
		payload["prompt"] = v
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return videoGenerationRequest{}, nil, fmt.Errorf("build extension JSON from multipart: %w", err)
	}
	return parseVideoExtensionJSON(jsonBody)
}

// decodeVideoEditRequest 把 JSON body decode 到 videoGenerationRequest，归一化模型 ID。
// 故意不用 DisallowUnknownFields：xAI 视频 edits/extensions 协议演化快，DAOF 透传给
// 上游 CPA（同样透传给 xAI），不强行 schema 校验防止字段名漂移导致 DAOF 拒绝合法请求。
// 安全边界由 rejectVideoFileIDFields + stream 拒绝 + canonical model 校验提供。
func decodeVideoEditRequest(body []byte) (videoGenerationRequest, error) {
	var req videoGenerationRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return videoGenerationRequest{}, fmt.Errorf("invalid video edit request body: %w", err)
	}
	canonical, ok := database.CanonicalRuntimeVideoModel(req.Model)
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		return videoGenerationRequest{}, fmt.Errorf("model is required")
	}
	if ok {
		req.Model = canonical
	}
	return req, nil
}

// rejectVideoFileIDFields 检查请求 body 中是否含有 file_id（任何位置），有则拒绝。
func rejectVideoFileIDFields(body []byte) error {
	for _, path := range []string{
		"file_id", "video.file_id", "image.file_id", "input_reference.file_id",
	} {
		if gjson.GetBytes(body, path).Exists() {
			return fmt.Errorf("file_id is not supported; use data URL or http(s) URL")
		}
	}
	if v := gjson.GetBytes(body, "reference_images"); v.IsArray() {
		var rejectErr error
		v.ForEach(func(_, item gjson.Result) bool {
			if item.Get("file_id").Exists() {
				rejectErr = fmt.Errorf("reference_images[].file_id is not supported")
				return false
			}
			return true
		})
		if rejectErr != nil {
			return rejectErr
		}
	}
	return nil
}

// videoEditHasInputMedia 校验请求至少含一种输入媒体引用。
func videoEditHasInputMedia(body []byte, req videoGenerationRequest) bool {
	if v := gjson.GetBytes(body, "video"); v.Exists() && (v.IsObject() || len(strings.TrimSpace(v.String())) > 0) {
		return true
	}
	if v := gjson.GetBytes(body, "image"); v.Exists() && (v.IsObject() || len(strings.TrimSpace(v.String())) > 0) {
		return true
	}
	if strings.TrimSpace(req.ImageURL) != "" {
		return true
	}
	if v := gjson.GetBytes(body, "reference_images"); v.IsArray() && len(v.Array()) > 0 {
		return true
	}
	if v := gjson.GetBytes(body, "input_reference"); v.Exists() {
		return true
	}
	return false
}

// multipartVideoFileToDataURL 读取 multipart 视频/图片文件，转 base64 data URL。
// 限 100MB/文件，避免一次请求 OOM。
func multipartVideoFileToDataURL(fh *multipart.FileHeader) (string, error) {
	if fh == nil {
		return "", fmt.Errorf("nil file header")
	}
	if fh.Size > maxVideoEditFileBytes {
		return "", fmt.Errorf("file %s size %d exceeds limit %d", fh.Filename, fh.Size, maxVideoEditFileBytes)
	}
	file, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("open upload: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(file, maxVideoEditFileBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read upload: %w", err)
	}
	if int64(len(raw)) > maxVideoEditFileBytes {
		return "", fmt.Errorf("file %s size exceeds limit %d", fh.Filename, maxVideoEditFileBytes)
	}

	mediaType := fh.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}
