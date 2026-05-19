// Package proxy / stream_path.go
//
// M-R2 重构（2026-05-19）：从 stream.go 抽出 path 相关 helper，纯文件物理拆分。
// 业务逻辑零改动；handler ChatCompletionProxyHandler 仍在 stream.go。

package proxy

import (
	"net/url"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func extractGeminiModelFromPath(path string) string {
	p, err := url.PathUnescape(path)
	if err != nil {
		p = path
	}
	lower := strings.ToLower(p)
	idx := strings.Index(lower, "/models/")
	if idx < 0 {
		return ""
	}
	modelAction := p[idx+len("/models/"):]
	if slash := strings.Index(modelAction, "/"); slash >= 0 {
		modelAction = modelAction[:slash]
	}
	if colon := strings.LastIndex(modelAction, ":"); colon >= 0 {
		modelAction = modelAction[:colon]
	}
	modelAction = strings.TrimSpace(strings.TrimPrefix(modelAction, "models/"))
	return modelAction
}

func isGeminiStreamPath(path string) bool {
	return strings.Contains(strings.ToLower(path), ":streamgeneratecontent")
}

func isClaudeCountTokensPath(path string) bool {
	return strings.Contains(strings.ToLower(path), "/messages/count_tokens")
}

func normalizeCLIProxyUpstreamPath(path string, srcFormat sdktranslator.Format) string {
	if srcFormat != sdktranslator.FormatClaude {
		return path
	}
	p := strings.TrimSpace(path)
	if p == "" {
		return path
	}
	if strings.HasPrefix(p, "//") {
		p = "/" + strings.TrimLeft(p, "/")
	}
	for strings.HasPrefix(p, "/v1/v1/") {
		p = "/v1/" + strings.TrimPrefix(p, "/v1/v1/")
	}
	return p
}

