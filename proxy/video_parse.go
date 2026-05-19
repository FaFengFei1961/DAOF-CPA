// Package proxy / video_parse.go
//
// M-R6 重构（2026-05-19）：从 video_generation.go 1131 行抽出 parse 相关
// helper，纯文件物理拆分。业务逻辑零改动。

package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"daof-cpa/database"

	"github.com/tidwall/gjson"
)

func parseVideoGenerationRequest(body []byte) (videoGenerationRequest, []byte, error) {
	if len(body) == 0 {
		return videoGenerationRequest{}, nil, fmt.Errorf("request body is required")
	}
	if !gjson.ValidBytes(body) {
		return videoGenerationRequest{}, nil, fmt.Errorf("request body must be valid JSON")
	}
	for _, field := range []string{
		"image", "images", "image_url", "image_urls", "input_image", "input_images",
		"input_reference", "reference_image", "reference_images", "reference_image_urls",
		"file_id", "mask", "video", "videos",
	} {
		if gjson.GetBytes(body, field).Exists() {
			return videoGenerationRequest{}, nil, fmt.Errorf("%s is not supported on /v1/videos/generations", field)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req videoGenerationRequest
	if err := dec.Decode(&req); err != nil {
		return videoGenerationRequest{}, nil, fmt.Errorf("unsupported video request field or invalid body: %w", err)
	}
	canonicalModel, ok := database.CanonicalRuntimeVideoModel(req.Model)
	req.Model = strings.TrimSpace(req.Model)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Size = strings.TrimSpace(req.Size)
	req.AspectRatio = strings.TrimSpace(req.AspectRatio)
	req.Resolution = strings.TrimSpace(req.Resolution)
	if req.Model == "" {
		return videoGenerationRequest{}, nil, fmt.Errorf("model is required")
	}
	if ok {
		req.Model = canonicalModel
	}
	if req.Prompt == "" {
		return videoGenerationRequest{}, nil, fmt.Errorf("prompt is required")
	}
	if len([]byte(req.Prompt)) > maxVideoPromptBytes {
		return videoGenerationRequest{}, nil, fmt.Errorf("prompt is too large")
	}
	if req.Stream != nil && *req.Stream {
		return videoGenerationRequest{}, nil, fmt.Errorf("streaming video generation is not supported")
	}
	duration, err := normalizeVideoDuration(req.Seconds, req.Duration)
	if err != nil {
		return videoGenerationRequest{}, nil, err
	}
	req.DurationSeconds = duration

	size, aspectRatio, resolution, err := normalizeVideoSizeOptions(req.Size)
	if err != nil {
		return videoGenerationRequest{}, nil, err
	}
	if req.AspectRatio != "" {
		aspectRatio, err = normalizeVideoAspectRatio(req.AspectRatio)
		if err != nil {
			return videoGenerationRequest{}, nil, err
		}
	}
	if req.Resolution != "" {
		resolution, err = normalizeVideoResolution(req.Resolution)
		if err != nil {
			return videoGenerationRequest{}, nil, err
		}
	}
	req.Size = size
	req.AspectRatio = aspectRatio
	req.Resolution = resolution

	payload := map[string]any{
		"model":        req.Model,
		"prompt":       req.Prompt,
		"duration":     req.DurationSeconds,
		"aspect_ratio": req.AspectRatio,
		"resolution":   req.Resolution,
	}
	sanitized, err := json.Marshal(payload)
	if err != nil {
		return videoGenerationRequest{}, nil, fmt.Errorf("build sanitized request: %w", err)
	}
	return req, sanitized, nil
}

func normalizeVideoDuration(secondsRaw, durationRaw json.RawMessage) (int64, error) {
	seconds, hasSeconds, err := parseVideoInteger(secondsRaw, "seconds")
	if err != nil {
		return 0, err
	}
	duration, hasDuration, err := parseVideoInteger(durationRaw, "duration")
	if err != nil {
		return 0, err
	}
	if hasSeconds && hasDuration && seconds != duration {
		return 0, fmt.Errorf("seconds and duration conflict")
	}
	value := defaultVideoDuration
	if hasSeconds {
		value = seconds
	} else if hasDuration {
		value = duration
	}
	if value < 1 {
		value = 1
	}
	if value > 15 {
		value = 15
	}
	return value, nil
}

func parseVideoInteger(raw json.RawMessage, field string) (int64, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false, fmt.Errorf("%s must be an integer", field)
	}
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return 0, false, fmt.Errorf("%s must be an integer", field)
		}
		return n, true, nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("%s must be an integer", field)
		}
		return n, true, nil
	default:
		return 0, false, fmt.Errorf("%s must be an integer", field)
	}
}

func normalizeVideoSizeOptions(raw string) (size string, aspectRatio string, resolution string, err error) {
	size = strings.TrimSpace(raw)
	if size == "" {
		return defaultVideoSize, defaultVideoAspectRatio, defaultVideoResolution, nil
	}
	switch strings.ToLower(size) {
	case "720x1280":
		return size, "9:16", "720p", nil
	case "1280x720":
		return size, "16:9", "720p", nil
	default:
		return "", "", "", fmt.Errorf("size must be 720x1280 or 1280x720")
	}
}

func normalizeVideoAspectRatio(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1:1", "square":
		return "1:1", nil
	case "16:9", "landscape":
		return "16:9", nil
	case "9:16", "portrait":
		return "9:16", nil
	case "4:3":
		return "4:3", nil
	case "3:4":
		return "3:4", nil
	case "3:2":
		return "3:2", nil
	case "2:3":
		return "2:3", nil
	default:
		return "", fmt.Errorf("aspect_ratio is invalid")
	}
}

func normalizeVideoResolution(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "480p":
		return "480p", nil
	case "720p":
		return "720p", nil
	default:
		return "", fmt.Errorf("resolution must be 480p or 720p")
	}
}

// filterVideoRoutes 过滤可服务指定 endpoint 的 video route。P3 后接 endpoint 参数
