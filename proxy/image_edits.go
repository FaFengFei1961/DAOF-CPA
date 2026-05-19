// Package proxy / image_edits.go
//
// /v1/images/edits 端点（OpenAI Images API 编辑模式）的请求解析 + handler。
// 主流程复用 processImageRequest（image_generation.go）；仅 parse 差异：
//   - 必须含 image / images[]（至少 1 张参考图）
//   - 可选 mask（PNG/JPEG b64 data URL 或 http(s) URL）
//   - 接受 multipart/form-data 和 application/json 两种请求体（OpenAI SDK 默认 multipart）
//   - 严格拒绝 file_id（避免 SSRF / 跨用户 file_id 重放）
//
// sanitized body 始终转换为 OpenAI Images API 兼容的 JSON 形式发上游，
// 上游 CLIProxyAPI 自行识别 model 走 gpt-image-2 或 xAI executor。
package proxy

import (
	"bytes"
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
	// maxImageEditFileBytes 限制单个 multipart 文件大小，防止 admin 配置 BodyLimit
	// 过大时单请求占用过多内存。OpenAI 官方文档 image 最大 ~50MB，mask 同步限制。
	maxImageEditFileBytes = 50 * 1024 * 1024
	// maxImageEditFileCount 限制单次 edit 请求最多参考图数量，
	// 与 OpenAI Images API edits 上限对齐。
	maxImageEditFileCount = 16
)

// ImageEditProxyHandler 处理 POST /v1/images/edits。复用 processImageRequest 的
// auth / canonical / precheck / moderation / upstream / 计费链路；流式通过 P1 框架接管。
func ImageEditProxyHandler(c *fiber.Ctx) error {
	return processImageRequest(c, database.EndpointImagesEdits, parseImageEditRequest)
}

// parseImageEditRequest 是 /v1/images/edits 的统一入口；按 Content-Type 走 JSON 或
// multipart 路径，最终返回 OpenAI 兼容的 sanitized JSON body。
func parseImageEditRequest(c *fiber.Ctx) (imageGenerationRequest, []byte, error) {
	contentType := strings.ToLower(strings.TrimSpace(c.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return parseImageEditMultipart(c)
	}
	rawBody := c.Body()
	body := make([]byte, len(rawBody))
	copy(body, rawBody)
	return parseImageEditJSON(body)
}

// parseImageEditJSON 解析 OpenAI Images API edits 的 JSON 请求体。
//
// 参数白名单与 generations 类似，并额外要求 image / images[]（至少 1 张）。
// 严格拒绝 file_id（数据来源不可控）。
func parseImageEditJSON(body []byte) (imageGenerationRequest, []byte, error) {
	if len(body) == 0 {
		return imageGenerationRequest{}, nil, fmt.Errorf("request body is required")
	}
	if !gjson.ValidBytes(body) {
		return imageGenerationRequest{}, nil, fmt.Errorf("request body must be valid JSON")
	}

	// file_id 检测：在任何位置出现都直接拒绝
	for _, path := range []string{"file_id", "mask.file_id", "input_image_mask.file_id"} {
		if gjson.GetBytes(body, path).Exists() {
			return imageGenerationRequest{}, nil, fmt.Errorf("file_id is not supported on /v1/images/edits; use image_url with data URL or HTTP(S) URL instead")
		}
	}
	if v := gjson.GetBytes(body, "images"); v.IsArray() {
		var rejectErr error
		v.ForEach(func(_, item gjson.Result) bool {
			if item.Get("file_id").Exists() {
				rejectErr = fmt.Errorf("images[].file_id is not supported on /v1/images/edits")
				return false
			}
			return true
		})
		if rejectErr != nil {
			return imageGenerationRequest{}, nil, rejectErr
		}
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req imageGenerationRequest
	if err := dec.Decode(&req); err != nil {
		return imageGenerationRequest{}, nil, fmt.Errorf("unsupported image edit request field or invalid body: %w", err)
	}

	// 兼容 OpenAI 单 image 字段（image: "data:..."）写法 — 在传入 JSON 里直接展开成 images[]
	if singleImageRaw := gjson.GetBytes(body, "image"); singleImageRaw.Exists() && !singleImageRaw.IsArray() && !singleImageRaw.IsObject() {
		singleURL := strings.TrimSpace(singleImageRaw.String())
		if singleURL != "" {
			req.Images = append([]imageReference{{ImageURL: singleURL}}, req.Images...)
		}
	} else if singleImageObj := gjson.GetBytes(body, "image"); singleImageObj.IsObject() {
		url := strings.TrimSpace(singleImageObj.Get("image_url").String())
		if url != "" {
			req.Images = append([]imageReference{{ImageURL: url}}, req.Images...)
		}
	}

	canonicalModel, ok := database.CanonicalRuntimeImageModel(req.Model)
	req.Model = strings.TrimSpace(req.Model)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Model == "" {
		return imageGenerationRequest{}, nil, fmt.Errorf("model is required")
	}
	if ok {
		req.Model = canonicalModel
	}
	if req.Prompt == "" {
		return imageGenerationRequest{}, nil, fmt.Errorf("prompt is required")
	}
	if len([]byte(req.Prompt)) > maxImagePromptBytes {
		return imageGenerationRequest{}, nil, fmt.Errorf("prompt is too large")
	}

	if err := validateImageEditInputs(&req); err != nil {
		return imageGenerationRequest{}, nil, err
	}

	if req.N == 0 {
		req.N = 1
	}
	if req.N < 1 || req.N > 10 {
		return imageGenerationRequest{}, nil, fmt.Errorf("n must be between 1 and 10")
	}

	responseFormat, err := normalizeImageResponseFormat(req.ResponseFormat)
	if err != nil {
		return imageGenerationRequest{}, nil, err
	}
	req.ResponseFormat = responseFormat

	isStream := req.Stream != nil && *req.Stream
	isPartialReq := req.PartialImages != nil && *req.PartialImages != 0
	if isStream && !database.IsRuntimeTokenBilledImageModel(req.Model) {
		return imageGenerationRequest{}, nil, fmt.Errorf("streaming is only supported for gpt-image-2")
	}
	if isPartialReq {
		if !isStream {
			return imageGenerationRequest{}, nil, fmt.Errorf("partial_images requires stream=true")
		}
		if *req.PartialImages < 1 || *req.PartialImages > 3 {
			return imageGenerationRequest{}, nil, fmt.Errorf("partial_images must be 1, 2, or 3")
		}
	}

	imagesPayload := make([]map[string]any, 0, len(req.Images))
	for _, ref := range req.Images {
		imagesPayload = append(imagesPayload, map[string]any{"image_url": ref.ImageURL})
	}

	payload := map[string]any{
		"model":  req.Model,
		"prompt": req.Prompt,
		"images": imagesPayload,
	}
	if req.ResponseFormat != "" {
		payload["response_format"] = req.ResponseFormat
	}
	if req.Mask != nil && strings.TrimSpace(req.Mask.ImageURL) != "" {
		payload["mask"] = map[string]any{"image_url": strings.TrimSpace(req.Mask.ImageURL)}
		req.MaskImageURL = strings.TrimSpace(req.Mask.ImageURL)
	}

	if database.IsRuntimeTokenBilledImageModel(req.Model) {
		if req.N != 1 {
			return imageGenerationRequest{}, nil, fmt.Errorf("n must be 1 for gpt-image-2")
		}
		if req.ResponseFormat == "url" {
			return imageGenerationRequest{}, nil, fmt.Errorf("response_format=url is not supported for gpt-image-2; use b64_json")
		}
		if req.Resolution != "" || req.AspectRatio != "" {
			return imageGenerationRequest{}, nil, fmt.Errorf("resolution/aspect_ratio are not supported for gpt-image-2; use size")
		}
		if req.Size != "" {
			size, err := normalizeGPTImageSize(req.Size)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.Size = size
			payload["size"] = req.Size
		}
		if req.Quality != "" {
			quality, err := normalizeGPTImageQuality(req.Quality)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.Quality = quality
			payload["quality"] = req.Quality
		}
		if req.OutputFormat != "" {
			outputFormat, err := normalizeGPTImageOutputFormat(req.OutputFormat)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.OutputFormat = outputFormat
			payload["output_format"] = req.OutputFormat
		}
		if req.Background != "" {
			background, err := normalizeGPTImageBackground(req.Background)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.Background = background
			if req.Background == "transparent" {
				return imageGenerationRequest{}, nil, fmt.Errorf("background=transparent is not supported for gpt-image-2")
			}
			payload["background"] = req.Background
		}
		if req.Moderation != "" {
			moderation, err := normalizeGPTImageModeration(req.Moderation)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.Moderation = moderation
			payload["moderation"] = req.Moderation
		}
		if req.OutputCompression != nil {
			if *req.OutputCompression < 0 || *req.OutputCompression > 100 {
				return imageGenerationRequest{}, nil, fmt.Errorf("output_compression must be between 0 and 100")
			}
			payload["output_compression"] = *req.OutputCompression
		}
		if req.InputFidelity != "" {
			fidelity, err := normalizeGPTImageInputFidelity(req.InputFidelity)
			if err != nil {
				return imageGenerationRequest{}, nil, err
			}
			req.InputFidelity = fidelity
			payload["input_fidelity"] = req.InputFidelity
		}
		if isStream {
			payload["stream"] = true
			if isPartialReq {
				payload["partial_images"] = *req.PartialImages
			}
		}
	} else {
		// xAI grok-imagine 编辑路径
		if req.Quality != "" || req.OutputFormat != "" || req.Background != "" || req.Moderation != "" || req.OutputCompression != nil || req.InputFidelity != "" {
			return imageGenerationRequest{}, nil, fmt.Errorf("quality/output_format/background/moderation/output_compression/input_fidelity are not supported for xAI image edit models")
		}
		req.Resolution = normalizeImageResolution(req.Resolution, req.Size)
		rawAspectRatio := strings.TrimSpace(req.AspectRatio)
		req.AspectRatio = normalizeImageAspectRatio(req.AspectRatio, req.Size)
		if req.Resolution == "" {
			req.Resolution = "1K"
		}
		if rawAspectRatio != "" && req.AspectRatio == "" {
			return imageGenerationRequest{}, nil, fmt.Errorf("aspect_ratio must be 1:1, 16:9, 9:16, 4:3, 3:4, 3:2, or 2:3 for xAI image models")
		}
		if req.AspectRatio == "" {
			req.AspectRatio = "1:1"
		}
		payload["n"] = req.N
		payload["resolution"] = strings.ToLower(req.Resolution)
		if req.AspectRatio != "" {
			payload["aspect_ratio"] = req.AspectRatio
		}
	}

	req.InputImageCount = len(req.Images)

	sanitized, err := json.Marshal(payload)
	if err != nil {
		return imageGenerationRequest{}, nil, fmt.Errorf("build sanitized request: %w", err)
	}
	return req, sanitized, nil
}

// parseImageEditMultipart 解析 multipart/form-data 请求，将文件转 b64 data URL 后
// 调用 parseImageEditJSON 完成统一 sanitize。
func parseImageEditMultipart(c *fiber.Ctx) (imageGenerationRequest, []byte, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return imageGenerationRequest{}, nil, fmt.Errorf("multipart parse failed: %w", err)
	}

	getValue := func(key string) string {
		if vals := form.Value[key]; len(vals) > 0 {
			return strings.TrimSpace(vals[0])
		}
		return ""
	}

	// 图片文件：image 单文件 / images[] 多文件 / image[] 别名
	var imageFiles []*multipart.FileHeader
	for _, key := range []string{"image", "image[]", "images", "images[]"} {
		if files, ok := form.File[key]; ok {
			imageFiles = append(imageFiles, files...)
		}
	}
	if len(imageFiles) == 0 {
		return imageGenerationRequest{}, nil, fmt.Errorf("at least one input image is required for /v1/images/edits")
	}
	if len(imageFiles) > maxImageEditFileCount {
		return imageGenerationRequest{}, nil, fmt.Errorf("too many input images (max %d)", maxImageEditFileCount)
	}

	imageRefs := make([]imageReference, 0, len(imageFiles))
	for _, fh := range imageFiles {
		dataURL, err := multipartFileToDataURL(fh)
		if err != nil {
			return imageGenerationRequest{}, nil, fmt.Errorf("read image upload: %w", err)
		}
		imageRefs = append(imageRefs, imageReference{ImageURL: dataURL})
	}

	var maskRef *imageReference
	if files, ok := form.File["mask"]; ok && len(files) > 0 {
		dataURL, err := multipartFileToDataURL(files[0])
		if err != nil {
			return imageGenerationRequest{}, nil, fmt.Errorf("read mask upload: %w", err)
		}
		maskRef = &imageReference{ImageURL: dataURL}
	} else if v := getValue("mask"); v != "" {
		maskRef = &imageReference{ImageURL: v}
	}

	// 构造等价 JSON 请求体
	payload := map[string]any{
		"model":  getValue("model"),
		"prompt": getValue("prompt"),
		"images": imageRefs,
	}
	if maskRef != nil {
		payload["mask"] = maskRef
	}
	for _, key := range []string{"response_format", "size", "quality", "output_format", "background", "moderation", "resolution", "aspect_ratio", "input_fidelity"} {
		if v := getValue(key); v != "" {
			payload[key] = v
		}
	}
	if v := getValue("n"); v != "" {
		if n, parseErr := strconv.Atoi(v); parseErr == nil {
			payload["n"] = n
		}
	}
	if v := getValue("output_compression"); v != "" {
		if n, parseErr := strconv.Atoi(v); parseErr == nil {
			payload["output_compression"] = n
		}
	}
	if v := getValue("partial_images"); v != "" {
		if n, parseErr := strconv.Atoi(v); parseErr == nil {
			payload["partial_images"] = n
		}
	}
	if v := getValue("stream"); v != "" {
		switch strings.ToLower(v) {
		case "true", "1", "yes":
			payload["stream"] = true
		}
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return imageGenerationRequest{}, nil, fmt.Errorf("build edits JSON from multipart: %w", err)
	}
	return parseImageEditJSON(jsonBody)
}

// validateImageEditInputs 校验解析后的 imageGenerationRequest 中 edit-only 字段的合法性。
func validateImageEditInputs(req *imageGenerationRequest) error {
	if len(req.Images) == 0 {
		return fmt.Errorf("at least one input image is required for /v1/images/edits")
	}
	if len(req.Images) > maxImageEditFileCount {
		return fmt.Errorf("too many input images (max %d)", maxImageEditFileCount)
	}
	for i := range req.Images {
		if req.Images[i].FileID != "" {
			return fmt.Errorf("images[%d].file_id is not supported; use image_url", i)
		}
		url := strings.TrimSpace(req.Images[i].ImageURL)
		if url == "" {
			return fmt.Errorf("images[%d].image_url is required", i)
		}
		if !isAcceptableImageURL(url) {
			return fmt.Errorf("images[%d].image_url must be a data URL or http(s) URL", i)
		}
		req.Images[i].ImageURL = url
	}
	if req.Mask != nil {
		if req.Mask.FileID != "" {
			return fmt.Errorf("mask.file_id is not supported; use mask.image_url")
		}
		maskURL := strings.TrimSpace(req.Mask.ImageURL)
		if maskURL != "" && !isAcceptableImageURL(maskURL) {
			return fmt.Errorf("mask.image_url must be a data URL or http(s) URL")
		}
		req.Mask.ImageURL = maskURL
	}
	return nil
}

// isAcceptableImageURL 仅接受 data: 或 http(s):// 前缀的 URL，且 http(s) URL 必须
// 经 ValidateChannelURL 复校验 host（拒绝 127.0.0.1 / 169.254.169.254 / 私网段 IP /
// userinfo / 控制字符等）。
//
// fix B-M2 (2026-05-19)：原实现仅 prefix 检查，把 `http://169.254.169.254/...` 类
// URL 透传给上游 CPA，由 CPA 去拉取 → 一旦 CPA 没做 SSRF 防护或被 DNS rebinding
// 绕过即可命中云元数据。现在 DAOF 入口本地校验，杜绝可疑 URL 进上游 fetch 链路。
func isAcceptableImageURL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "data:") {
		return true
	}
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	// http(s) → 复用渠道 URL 校验：scheme + host 形式 + IP 黑名单（含云元数据）
	return ValidateChannelURL(trimmed) == nil
}

// normalizeGPTImageInputFidelity 校验 gpt-image-2 的 input_fidelity 取值（OpenAI 官方
// 文档：auto / low / high；保守起见 P2 阶段仅放行明确枚举）。
func normalizeGPTImageInputFidelity(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return "auto", nil
	case "low":
		return "low", nil
	case "high":
		return "high", nil
	default:
		return "", fmt.Errorf("input_fidelity must be auto, low, or high")
	}
}

// multipartFileToDataURL 读取 multipart 文件并转换为 data URL（含 base64）。
// 超过 maxImageEditFileBytes 上限拒绝，避免一次请求占用过多内存。
func multipartFileToDataURL(fh *multipart.FileHeader) (string, error) {
	if fh == nil {
		return "", fmt.Errorf("nil file header")
	}
	if fh.Size > maxImageEditFileBytes {
		return "", fmt.Errorf("file %s size %d exceeds limit %d", fh.Filename, fh.Size, maxImageEditFileBytes)
	}
	file, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("open upload: %w", err)
	}
	defer file.Close()

	limited := io.LimitReader(file, maxImageEditFileBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read upload: %w", err)
	}
	if int64(len(raw)) > maxImageEditFileBytes {
		return "", fmt.Errorf("file %s size exceeds limit %d", fh.Filename, maxImageEditFileBytes)
	}

	mediaType := fh.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}
