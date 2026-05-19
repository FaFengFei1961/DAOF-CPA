// Package proxy / image_parse.go
//
// M-R6 重构（2026-05-19）：从 image_generation.go 1892 行单体抽出 parse 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
)

func parseImageGenerationRequest(body []byte) (imageGenerationRequest, []byte, error) {
	if len(body) == 0 {
		return imageGenerationRequest{}, nil, fmt.Errorf("request body is required")
	}
	if !gjson.ValidBytes(body) {
		return imageGenerationRequest{}, nil, fmt.Errorf("request body must be valid JSON")
	}
	for _, field := range []string{
		"image", "images", "image_url", "image_urls", "input_image", "input_images",
		"mask", "reference_image", "reference_images", "init_image", "video", "videos",
	} {
		if gjson.GetBytes(body, field).Exists() {
			return imageGenerationRequest{}, nil, fmt.Errorf("%s is not supported on /v1/images/generations", field)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req imageGenerationRequest
	if err := dec.Decode(&req); err != nil {
		return imageGenerationRequest{}, nil, fmt.Errorf("unsupported image request field or invalid body: %w", err)
	}
	canonicalModel, ok := database.CanonicalRuntimeImageModel(req.Model)
	req.Model = strings.TrimSpace(req.Model)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Size = strings.TrimSpace(req.Size)
	req.Quality = strings.TrimSpace(req.Quality)
	req.ResponseFormat = strings.TrimSpace(req.ResponseFormat)
	req.OutputFormat = strings.TrimSpace(req.OutputFormat)
	req.Background = strings.TrimSpace(req.Background)
	req.Moderation = strings.TrimSpace(req.Moderation)
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
		// xAI grok-imagine 不暴露稳定的流式 SSE 协议，仅 gpt-image-2 走 OpenAI 兼容的
		// image_generation.partial_image / image_generation.completed 流式事件序列。
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

	payload := map[string]any{
		"model":  req.Model,
		"prompt": req.Prompt,
	}
	if req.ResponseFormat != "" {
		payload["response_format"] = req.ResponseFormat
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
		if isStream {
			payload["stream"] = true
			if isPartialReq {
				payload["partial_images"] = *req.PartialImages
			}
		}
	} else {
		if req.Quality != "" || req.OutputFormat != "" || req.Background != "" || req.Moderation != "" || req.OutputCompression != nil {
			return imageGenerationRequest{}, nil, fmt.Errorf("quality/output_format/background/moderation/output_compression are not supported for xAI image models")
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
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return imageGenerationRequest{}, nil, fmt.Errorf("build sanitized request: %w", err)
	}
	return req, sanitized, nil
}

func normalizeImageResolution(resolution, size string) string {
	r := strings.ToUpper(strings.TrimSpace(resolution))
	if r == "" {
		r = strings.ToUpper(strings.TrimSpace(size))
	}
	switch r {
	case "1K", "1024X1024", "1024":
		return "1K"
	case "2K", "2048X2048", "2048":
		return "2K"
	default:
		return ""
	}
}

func normalizeImageAspectRatio(aspectRatio, size string) string {
	normalized := ""
	switch strings.ToLower(strings.TrimSpace(aspectRatio)) {
	case "square", "1:1":
		normalized = "1:1"
	case "landscape", "16:9":
		normalized = "16:9"
	case "portrait", "9:16":
		normalized = "9:16"
	case "4:3", "3:4", "3:2", "2:3":
		normalized = strings.ToLower(strings.TrimSpace(aspectRatio))
	}
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "1024x1024", "2048x2048", "1:1":
		return "1:1"
	case "1792x1024", "16:9":
		return "16:9"
	case "1024x1792", "9:16":
		return "9:16"
	case "1536x1024", "3:2":
		return "3:2"
	case "1024x1536", "2:3":
		return "2:3"
	default:
		return normalized
	}
}

func normalizeImageResponseFormat(responseFormat string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "":
		return "", nil
	case "url":
		return "url", nil
	case "b64_json":
		return "b64_json", nil
	default:
		return "", fmt.Errorf("response_format must be url or b64_json")
	}
}

func normalizeGPTImageSize(size string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(size)) {
	case "", "auto":
		return "auto", nil
	case "1024x1024", "1536x1024", "1024x1536":
		return strings.ToLower(strings.TrimSpace(size)), nil
	default:
		return "", fmt.Errorf("size must be auto, 1024x1024, 1536x1024, or 1024x1536 for gpt-image-2")
	}
}

func normalizeGPTImageQuality(quality string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "", "auto":
		return "auto", nil
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(quality)), nil
	default:
		return "", fmt.Errorf("quality must be auto, low, medium, or high for gpt-image-2")
	}
}

func normalizeGPTImageOutputFormat(format string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "png":
		return "png", nil
	case "jpeg", "webp":
		return strings.ToLower(strings.TrimSpace(format)), nil
	default:
		return "", fmt.Errorf("output_format must be png, jpeg, or webp for gpt-image-2")
	}
}

func normalizeGPTImageBackground(background string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(background)) {
	case "", "auto":
		return "auto", nil
	case "opaque", "transparent":
		return strings.ToLower(strings.TrimSpace(background)), nil
	default:
		return "", fmt.Errorf("background must be auto, opaque, or transparent for gpt-image-2")
	}
}

func normalizeGPTImageModeration(moderation string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(moderation)) {
	case "", "auto":
		return "auto", nil
	case "low":
		return "low", nil
	default:
		return "", fmt.Errorf("moderation must be auto or low for gpt-image-2")
	}
}

// filterImageRoutes 过滤可服务指定 endpoint 的 image route。P2 后接受 endpoint
