// Package proxy / prompt_extract.go
//
// 从代理请求体抽取文本与图片。全文抽取用于诊断；内容审核入口会优先使用
// source-aware segments，避免把客户端系统提示、工具 schema、工具结果和用户真实输入
// 混成一个大 prompt 造成误判。
//
// 设计原则（codex 第二十三轮反馈）：
//   - 覆盖 5 种入口格式：OpenAI ChatCompletion / OpenAI Responses / Anthropic Messages / Gemini / Codex
//   - 抽取 tool / function / schema 相关字段（攻击者可能把 jailbreak 藏在 tool description 或 result）
//   - 多模态：image_url 的 URL 不提取（无文本可审）；alt-text / caption 提取
//   - **失败 fail-closed**：如果 body 不是有效 JSON，返回空字符串 + error，调用方决定是否拒绝
//
// 不做：
//   - base64 image OCR（image_policy=submit 预留给可审核图片的供应商；当前 CPA 分类器不透传外部 image_url）
//   - 不主动 normalize（保留原文，让审核拿到攻击原貌）
package proxy

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// PromptExtractResult 提取结果
type PromptExtractResult struct {
	Text       string   // 拼接后的所有文本（用 \n---\n 分隔片段）
	ImageURLs  []string // image_url（http/https/data URL）—— 给 image_policy 决策用
	HasContent bool     // 是否提取到任何 text 或 image
}

// ModerationSegmentKind 标记文本来源。审核流水线会按来源采用不同策略：
// user_message 是主审对象；tool_result/function_output 是外部数据；system/tool_schema
// 更多用于诊断或低风险审计，不直接送智能审核模型。
type ModerationSegmentKind string

const (
	SegmentUserMessage       ModerationSegmentKind = "user_message"
	SegmentToolResult        ModerationSegmentKind = "tool_result"
	SegmentFunctionOutput    ModerationSegmentKind = "function_output"
	SegmentToolCall          ModerationSegmentKind = "tool_call"
	SegmentAssistantMessage  ModerationSegmentKind = "assistant_message"
	SegmentSystemInstruction ModerationSegmentKind = "system_instruction"
	SegmentToolSchema        ModerationSegmentKind = "tool_schema"
	SegmentClientContext     ModerationSegmentKind = "client_context"
	SegmentMetadata          ModerationSegmentKind = "metadata"
)

type ModerationSegment struct {
	Kind ModerationSegmentKind `json:"kind"`
	Role string                `json:"role,omitempty"`
	Path string                `json:"path,omitempty"`
	Text string                `json:"text"`
}

type ModerationReview struct {
	Segments   []ModerationSegment `json:"segments"`
	ImageURLs  []string            `json:"image_urls,omitempty"`
	HasContent bool                `json:"has_content"`
}

type syntheticUserBlockSpec struct {
	Tag        string
	Kind       ModerationSegmentKind
	RequireAny []string
}

var syntheticUserBlockSpecs = []syntheticUserBlockSpec{
	{
		Tag:        "environment_context",
		Kind:       SegmentClientContext,
		RequireAny: []string{"<cwd>", "<shell>", "<current_date>", "<timezone>"},
	},
	{
		Tag:        "skills_instructions",
		Kind:       SegmentClientContext,
		RequireAny: []string{"## skills", "available skills", "how to use skills"},
	},
	{
		Tag:        "plugins_instructions",
		Kind:       SegmentClientContext,
		RequireAny: []string{"## plugins", "available plugins", "plugin is a local bundle"},
	},
	{
		Tag:        "mcp_context",
		Kind:       SegmentToolResult,
		RequireAny: []string{"mcp", "resources", "tools"},
	},
	{
		Tag:        "mcp_resource",
		Kind:       SegmentToolResult,
		RequireAny: []string{"mcp", "resource", "uri="},
	},
	{
		Tag:        "hook_output",
		Kind:       SegmentToolResult,
		RequireAny: []string{"hook", "stdout", "stderr", "exit"},
	},
	{
		Tag:        "tool_result",
		Kind:       SegmentToolResult,
		RequireAny: []string{"tool", "result", "output"},
	},
	{
		Tag:        "command_output",
		Kind:       SegmentToolResult,
		RequireAny: []string{"stdout", "stderr", "exit code", "powershell", "bash"},
	},
}

// ExtractPromptText 按 srcFormat 抽取文本和 image。失败返回 (空结果, error)。
//
// 调用方根据 ImagePolicy 决定：
//   - "skip"   → 忽略 ImageURLs
//   - "submit" → 把 ImageURLs 交给审核层（当前 CPA 分类器会按不可达处理）
//   - "reject" → 直接拒绝带图请求
func ExtractPromptText(srcFormat string, body []byte) (PromptExtractResult, error) {
	if len(body) == 0 {
		return PromptExtractResult{}, fmt.Errorf("empty body")
	}
	if !gjson.ValidBytes(body) {
		return PromptExtractResult{}, fmt.Errorf("body not valid json")
	}
	var parts []string
	var images []string

	switch srcFormat {
	case "openai", "OpenAI", "FormatOpenAI", "":
		// OpenAI ChatCompletion / Responses 共用入口（先走 ChatCompletion，再扩展 Responses 字段）
		parts, images = extractOpenAI(body, parts, images)
		// Responses API 字段（input/instructions/function_call_output）
		parts, images = extractOpenAIResponses(body, parts, images)
	case "anthropic", "Anthropic", "FormatClaude":
		parts, images = extractAnthropic(body, parts, images)
	case "gemini", "Gemini", "FormatGemini", "FormatGeminiCLI":
		parts, images = extractGemini(body, parts, images)
	case "codex", "Codex", "FormatCodex":
		// Codex 走 OpenAI 兼容格式
		parts, images = extractOpenAI(body, parts, images)
		parts, images = extractOpenAIResponses(body, parts, images)
	default:
		// 未知格式：尽力按 OpenAI 通用结构提取
		parts, images = extractOpenAI(body, parts, images)
	}

	text := strings.Join(parts, "\n---\n")
	return PromptExtractResult{
		Text:       text,
		ImageURLs:  images,
		HasContent: text != "" || len(images) > 0,
	}, nil
}

// ExtractModerationReviewText extracts only end-user / externally retrieved
// content for LLM-based moderation. Full prompt extraction intentionally keeps
// system prompts and tool schemas for diagnostics and local risk rules, but
// sending first-party client instructions/tool definitions to the classifier
// causes false prompt-injection blocks for clients such as Codex.
func ExtractModerationReviewText(srcFormat string, body []byte) (PromptExtractResult, error) {
	review, err := ExtractModerationSegments(srcFormat, body)
	if err != nil {
		return PromptExtractResult{}, err
	}
	out := review.ResultForKinds(SegmentUserMessage, SegmentToolResult, SegmentFunctionOutput)
	return out, nil
}

// ExtractModerationSegments 抽取带来源标签的审核片段。它比 ExtractPromptText 更适合
// 运行时风控，因为同一句 “ignore previous instructions” 出现在用户消息、网页内容、
// 工具 schema 或客户端系统提示中的风险含义完全不同。
func ExtractModerationSegments(srcFormat string, body []byte) (ModerationReview, error) {
	if len(body) == 0 {
		return ModerationReview{}, fmt.Errorf("empty body")
	}
	if !gjson.ValidBytes(body) {
		return ModerationReview{}, fmt.Errorf("body not valid json")
	}
	var segments []ModerationSegment
	var images []string

	switch srcFormat {
	case "openai", "OpenAI", "FormatOpenAI", "":
		segments, images = extractOpenAISegments(body, segments, images)
		segments, images = extractOpenAIResponsesSegments(body, segments, images)
	case "anthropic", "Anthropic", "FormatClaude":
		segments, images = extractAnthropicSegments(body, segments, images)
	case "gemini", "Gemini", "FormatGemini", "FormatGeminiCLI":
		segments, images = extractGeminiSegments(body, segments, images)
	case "codex", "Codex", "FormatCodex":
		segments, images = extractOpenAISegments(body, segments, images)
		segments, images = extractOpenAIResponsesSegments(body, segments, images)
	default:
		segments, images = extractOpenAISegments(body, segments, images)
		segments, images = extractOpenAIResponsesSegments(body, segments, images)
	}

	return ModerationReview{
		Segments:   segments,
		ImageURLs:  images,
		HasContent: len(segments) > 0 || len(images) > 0,
	}, nil
}

func (r ModerationReview) ResultForKinds(kinds ...ModerationSegmentKind) PromptExtractResult {
	allowed := make(map[ModerationSegmentKind]struct{}, len(kinds))
	for _, k := range kinds {
		allowed[k] = struct{}{}
	}
	parts := make([]string, 0, len(r.Segments))
	for _, seg := range r.Segments {
		if _, ok := allowed[seg.Kind]; ok {
			parts = append(parts, seg.Text)
		}
	}
	text := strings.Join(parts, "\n---\n")
	return PromptExtractResult{
		Text:       text,
		ImageURLs:  append([]string(nil), r.ImageURLs...),
		HasContent: text != "" || len(r.ImageURLs) > 0,
	}
}

func (r ModerationReview) TextForKinds(kinds ...ModerationSegmentKind) string {
	return r.ResultForKinds(kinds...).Text
}

func appendSegment(segments *[]ModerationSegment, kind ModerationSegmentKind, role, path, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if kind == SegmentUserMessage && appendSplitSyntheticUserSegments(segments, role, path, text) {
		return
	}
	*segments = append(*segments, ModerationSegment{
		Kind: kind,
		Role: strings.TrimSpace(role),
		Path: strings.TrimSpace(path),
		Text: text,
	})
}

func appendRawSegment(segments *[]ModerationSegment, kind ModerationSegmentKind, role, path, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	*segments = append(*segments, ModerationSegment{
		Kind: kind,
		Role: strings.TrimSpace(role),
		Path: strings.TrimSpace(path),
		Text: text,
	})
}

func appendSplitSyntheticUserSegments(segments *[]ModerationSegment, role, path, text string) bool {
	parts := splitSyntheticUserText(text)
	if len(parts) <= 1 && (len(parts) == 0 || parts[0].Kind == SegmentUserMessage) {
		return false
	}
	for i, part := range parts {
		appendRawSegment(segments, part.Kind, role, fmt.Sprintf("%s.synthetic[%d]", path, i), part.Text)
	}
	return true
}

func splitSyntheticUserText(text string) []ModerationSegment {
	remaining := text
	var out []ModerationSegment
	for {
		idx, spec := findNextSyntheticBlock(remaining)
		if idx < 0 {
			if strings.TrimSpace(remaining) != "" {
				out = append(out, ModerationSegment{Kind: SegmentUserMessage, Text: strings.TrimSpace(remaining)})
			}
			return out
		}
		if idx > 0 {
			prefix := strings.TrimSpace(remaining[:idx])
			if prefix != "" {
				out = append(out, ModerationSegment{Kind: SegmentUserMessage, Text: prefix})
			}
		}
		block, after, ok := extractSyntheticBlock(remaining[idx:], spec)
		if !ok {
			if strings.TrimSpace(remaining) != "" {
				out = append(out, ModerationSegment{Kind: SegmentUserMessage, Text: strings.TrimSpace(remaining)})
			}
			return out
		}
		out = append(out, ModerationSegment{Kind: spec.Kind, Text: strings.TrimSpace(block)})
		remaining = after
	}
}

func findNextSyntheticBlock(text string) (int, syntheticUserBlockSpec) {
	lower := strings.ToLower(text)
	best := -1
	var bestSpec syntheticUserBlockSpec
	for _, spec := range syntheticUserBlockSpecs {
		openPrefix := "<" + spec.Tag
		idx := strings.Index(lower, openPrefix)
		if idx < 0 {
			continue
		}
		if best < 0 || idx < best {
			best = idx
			bestSpec = spec
		}
	}
	return best, bestSpec
}

func extractSyntheticBlock(text string, spec syntheticUserBlockSpec) (block, after string, ok bool) {
	lower := strings.ToLower(text)
	openPrefix := "<" + spec.Tag
	if !strings.HasPrefix(lower, openPrefix) {
		return "", text, false
	}
	openEnd := strings.Index(lower, ">")
	if openEnd < 0 {
		return "", text, false
	}
	closeTag := "</" + spec.Tag + ">"
	closeIdx := strings.Index(lower[openEnd+1:], closeTag)
	if closeIdx < 0 {
		return "", text, false
	}
	end := openEnd + 1 + closeIdx + len(closeTag)
	block = text[:end]
	if !syntheticBlockLooksAuthentic(block, spec) {
		return "", text, false
	}
	return block, text[end:], true
}

func syntheticBlockLooksAuthentic(block string, spec syntheticUserBlockSpec) bool {
	if len(spec.RequireAny) == 0 {
		return true
	}
	lower := strings.ToLower(block)
	for _, marker := range spec.RequireAny {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func openAISegmentKindForRole(role string) ModerationSegmentKind {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "user":
		return SegmentUserMessage
	case "tool":
		return SegmentToolResult
	case "function":
		return SegmentFunctionOutput
	case "assistant":
		return SegmentAssistantMessage
	case "system", "developer":
		return SegmentSystemInstruction
	default:
		return SegmentUserMessage
	}
}

func appendOpenAIContentSegments(content gjson.Result, kind ModerationSegmentKind, role, path string, segments *[]ModerationSegment, images *[]string) {
	if content.IsArray() {
		content.ForEach(func(idx, item gjson.Result) bool {
			itemPath := fmt.Sprintf("%s.content[%s]", path, idx.String())
			appendOpenAIContentSegments(item, kind, role, itemPath, segments, images)
			return true
		})
		return
	}
	if t := content.Get("text").String(); t != "" {
		appendSegment(segments, kind, role, path+".text", t)
	}
	if u := content.Get("image_url.url").String(); u != "" {
		*images = append(*images, u)
	}
	if u := content.Get("image_url").String(); u != "" && !strings.HasPrefix(u, "{") {
		*images = append(*images, u)
	}
	if content.Type == gjson.String {
		appendSegment(segments, kind, role, path, content.String())
	}
}

func extractOpenAISegments(body []byte, segments []ModerationSegment, images []string) ([]ModerationSegment, []string) {
	gjson.GetBytes(body, "messages").ForEach(func(i, msg gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		kind := openAISegmentKindForRole(role)
		path := fmt.Sprintf("messages[%s]", i.String())
		appendOpenAIContentSegments(msg.Get("content"), kind, role, path, &segments, &images)
		msg.Get("tool_calls").ForEach(func(j, tc gjson.Result) bool {
			if args := tc.Get("function.arguments").String(); args != "" {
				appendSegment(&segments, SegmentToolCall, role, fmt.Sprintf("%s.tool_calls[%s].function.arguments", path, j.String()), args)
			}
			return true
		})
		return true
	})
	if sys := gjson.GetBytes(body, "system").String(); sys != "" {
		appendSegment(&segments, SegmentSystemInstruction, "system", "system", sys)
	}
	gjson.GetBytes(body, "tools").ForEach(func(i, tool gjson.Result) bool {
		path := fmt.Sprintf("tools[%s]", i.String())
		if d := tool.Get("function.description").String(); d != "" {
			appendSegment(&segments, SegmentToolSchema, "", path+".function.description", d)
		}
		if d := tool.Get("description").String(); d != "" {
			appendSegment(&segments, SegmentToolSchema, "", path+".description", d)
		}
		if p := tool.Get("function.parameters").Raw; p != "" {
			appendSegment(&segments, SegmentToolSchema, "", path+".function.parameters", p)
		}
		if p := tool.Get("input_schema").Raw; p != "" {
			appendSegment(&segments, SegmentToolSchema, "", path+".input_schema", p)
		}
		return true
	})
	gjson.GetBytes(body, "functions").ForEach(func(i, fn gjson.Result) bool {
		if d := fn.Get("description").String(); d != "" {
			appendSegment(&segments, SegmentToolSchema, "", fmt.Sprintf("functions[%s].description", i.String()), d)
		}
		return true
	})
	tc := gjson.GetBytes(body, "tool_choice")
	if tc.Type == gjson.String {
		if s := tc.String(); s != "" && s != "auto" && s != "required" && s != "none" {
			appendSegment(&segments, SegmentMetadata, "", "tool_choice", s)
		}
	}
	return segments, images
}

func extractOpenAIResponsesSegments(body []byte, segments []ModerationSegment, images []string) ([]ModerationSegment, []string) {
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		input.ForEach(func(i, item gjson.Result) bool {
			role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
			itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
			path := fmt.Sprintf("input[%s]", i.String())
			kind := openAISegmentKindForRole(role)
			if itemType == "function_call_output" {
				kind = SegmentFunctionOutput
			}
			if t := item.Get("text").String(); t != "" {
				appendSegment(&segments, kind, role, path+".text", t)
			}
			item.Get("content").ForEach(func(j, c gjson.Result) bool {
				appendOpenAIContentSegments(c, kind, role, fmt.Sprintf("%s.content[%s]", path, j.String()), &segments, &images)
				return true
			})
			if out := item.Get("output").String(); out != "" {
				appendSegment(&segments, SegmentFunctionOutput, role, path+".output", out)
			}
			return true
		})
	} else if input.Type == gjson.String {
		appendSegment(&segments, SegmentUserMessage, "user", "input", input.String())
	}
	if ins := gjson.GetBytes(body, "instructions").String(); ins != "" {
		appendSegment(&segments, SegmentSystemInstruction, "system", "instructions", ins)
	}
	return segments, images
}

func extractAnthropicSegments(body []byte, segments []ModerationSegment, images []string) ([]ModerationSegment, []string) {
	sys := gjson.GetBytes(body, "system")
	if sys.IsArray() {
		sys.ForEach(func(i, item gjson.Result) bool {
			if t := item.Get("text").String(); t != "" {
				appendSegment(&segments, SegmentSystemInstruction, "system", fmt.Sprintf("system[%s].text", i.String()), t)
			}
			return true
		})
	} else if sys.Type == gjson.String {
		appendSegment(&segments, SegmentSystemInstruction, "system", "system", sys.String())
	}

	gjson.GetBytes(body, "messages").ForEach(func(i, msg gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		msgKind := SegmentAssistantMessage
		if role == "" || role == "user" {
			msgKind = SegmentUserMessage
		}
		path := fmt.Sprintf("messages[%s]", i.String())
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(j, item gjson.Result) bool {
				itemPath := fmt.Sprintf("%s.content[%s]", path, j.String())
				switch item.Get("type").String() {
				case "text":
					appendSegment(&segments, msgKind, role, itemPath+".text", item.Get("text").String())
				case "image":
					if src := item.Get("source.data").String(); src != "" {
						mime := item.Get("source.media_type").String()
						if mime == "" {
							mime = "image/jpeg"
						}
						images = append(images, "data:"+mime+";base64,"+src)
					}
					if u := item.Get("source.url").String(); u != "" {
						images = append(images, u)
					}
				case "tool_use":
					if input := item.Get("input").Raw; input != "" {
						appendSegment(&segments, SegmentToolCall, role, itemPath+".input", input)
					}
				case "tool_result":
					appendAnthropicToolResultSegments(item.Get("content"), role, itemPath+".content", &segments)
				}
				return true
			})
		} else if content.Type == gjson.String {
			appendSegment(&segments, msgKind, role, path+".content", content.String())
		}
		return true
	})

	gjson.GetBytes(body, "tools").ForEach(func(i, tool gjson.Result) bool {
		path := fmt.Sprintf("tools[%s]", i.String())
		if d := tool.Get("description").String(); d != "" {
			appendSegment(&segments, SegmentToolSchema, "", path+".description", d)
		}
		if p := tool.Get("input_schema").Raw; p != "" {
			appendSegment(&segments, SegmentToolSchema, "", path+".input_schema", p)
		}
		return true
	})
	return segments, images
}

func appendAnthropicToolResultSegments(content gjson.Result, role, path string, segments *[]ModerationSegment) {
	if content.IsArray() {
		content.ForEach(func(i, sub gjson.Result) bool {
			if t := sub.Get("text").String(); t != "" {
				appendSegment(segments, SegmentToolResult, role, fmt.Sprintf("%s[%s].text", path, i.String()), t)
			}
			return true
		})
		return
	}
	if content.Type == gjson.String {
		appendSegment(segments, SegmentToolResult, role, path, content.String())
	}
}

func extractGeminiSegments(body []byte, segments []ModerationSegment, images []string) ([]ModerationSegment, []string) {
	gjson.GetBytes(body, "contents").ForEach(func(i, c gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(c.Get("role").String()))
		msgKind := SegmentUserMessage
		switch role {
		case "model", "assistant":
			msgKind = SegmentAssistantMessage
		case "function", "tool":
			msgKind = SegmentFunctionOutput
		case "system":
			msgKind = SegmentSystemInstruction
		}
		path := fmt.Sprintf("contents[%s]", i.String())
		c.Get("parts").ForEach(func(j, p gjson.Result) bool {
			partPath := fmt.Sprintf("%s.parts[%s]", path, j.String())
			if t := p.Get("text").String(); t != "" {
				appendSegment(&segments, msgKind, role, partPath+".text", t)
			}
			if mime := p.Get("inline_data.mime_type").String(); strings.HasPrefix(mime, "image/") {
				if data := p.Get("inline_data.data").String(); data != "" {
					images = append(images, "data:"+mime+";base64,"+data)
				}
			}
			if u := p.Get("file_data.file_uri").String(); u != "" {
				images = append(images, u)
			}
			if args := p.Get("functionCall.args").Raw; args != "" {
				appendSegment(&segments, SegmentToolCall, role, partPath+".functionCall.args", args)
			}
			if resp := p.Get("functionResponse.response").Raw; resp != "" {
				appendSegment(&segments, SegmentFunctionOutput, role, partPath+".functionResponse.response", resp)
			}
			if code := p.Get("executableCode.code").String(); code != "" {
				appendSegment(&segments, SegmentToolCall, role, partPath+".executableCode.code", code)
			}
			return true
		})
		return true
	})

	gjson.GetBytes(body, "systemInstruction.parts").ForEach(func(i, p gjson.Result) bool {
		if t := p.Get("text").String(); t != "" {
			appendSegment(&segments, SegmentSystemInstruction, "system", fmt.Sprintf("systemInstruction.parts[%s].text", i.String()), t)
		}
		return true
	})
	gjson.GetBytes(body, "tools").ForEach(func(i, tool gjson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(j, fn gjson.Result) bool {
			path := fmt.Sprintf("tools[%s].functionDeclarations[%s]", i.String(), j.String())
			if d := fn.Get("description").String(); d != "" {
				appendSegment(&segments, SegmentToolSchema, "", path+".description", d)
			}
			if p := fn.Get("parameters").Raw; p != "" {
				appendSegment(&segments, SegmentToolSchema, "", path+".parameters", p)
			}
			return true
		})
		return true
	})
	return segments, images
}

// extractOpenAI 抽 ChatCompletion 格式：messages[].content / tools[].function / tool_choice
func extractOpenAI(body []byte, parts []string, images []string) ([]string, []string) {
	// messages[].content (string OR array of {type, text/image_url})
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		if role != "" {
			parts = append(parts, "[role:"+role+"]")
		}
		if name := msg.Get("name").String(); name != "" {
			parts = append(parts, "[name:"+name+"]")
		}
		// content：可以是 string 也可以是数组
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, item gjson.Result) bool {
				if t := item.Get("text").String(); t != "" {
					parts = append(parts, t)
				}
				if u := item.Get("image_url.url").String(); u != "" {
					images = append(images, u)
				}
				if u := item.Get("image_url").String(); u != "" && !strings.HasPrefix(u, "{") {
					// image_url 直接是 string（少见）
					images = append(images, u)
				}
				return true
			})
		} else if content.Exists() {
			if s := content.String(); s != "" {
				parts = append(parts, s)
			}
		}
		// tool_calls[].function.arguments (assistant 调用工具时的入参)
		msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if args := tc.Get("function.arguments").String(); args != "" {
				parts = append(parts, "[tool_call_args]"+args)
			}
			return true
		})
		return true
	})

	// system 字段（OpenAI 旧版本 / Anthropic 兼容）
	if sys := gjson.GetBytes(body, "system").String(); sys != "" {
		parts = append(parts, "[system]"+sys)
	}

	// tools[].function.description / parameters
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		if d := tool.Get("function.description").String(); d != "" {
			parts = append(parts, "[tool_desc]"+d)
		}
		if d := tool.Get("description").String(); d != "" { // Anthropic 顶级 description
			parts = append(parts, "[tool_desc]"+d)
		}
		// parameters schema 也可能藏内容（admin 配置 tool 时填的描述）
		if p := tool.Get("function.parameters").Raw; p != "" {
			parts = append(parts, "[tool_schema]"+p)
		}
		if p := tool.Get("input_schema").Raw; p != "" { // Anthropic
			parts = append(parts, "[tool_schema]"+p)
		}
		return true
	})

	// 兼容老格式：functions[]
	gjson.GetBytes(body, "functions").ForEach(func(_, fn gjson.Result) bool {
		if d := fn.Get("description").String(); d != "" {
			parts = append(parts, "[fn_desc]"+d)
		}
		return true
	})

	// tool_choice 如果是 string 也提取（"auto" / "required" 等不算违规，但 admin 自定义可能含文本）
	tc := gjson.GetBytes(body, "tool_choice")
	if tc.Type == gjson.String {
		if s := tc.String(); s != "" && s != "auto" && s != "required" && s != "none" {
			parts = append(parts, "[tool_choice]"+s)
		}
	}

	return parts, images
}

func extractOpenAIReview(body []byte, parts []string, images []string) ([]string, []string) {
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		if role != "" && role != "user" && role != "tool" && role != "function" {
			return true
		}
		return appendReviewContent(msg.Get("content"), &parts, &images)
	})
	return parts, images
}

// extractOpenAIResponses 抽 Responses API 独有字段（/v1/responses）
func extractOpenAIResponses(body []byte, parts []string, images []string) ([]string, []string) {
	// input：string OR array of message-like items
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			if t := item.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
			// content 可能是嵌套数组
			item.Get("content").ForEach(func(_, c gjson.Result) bool {
				if t := c.Get("text").String(); t != "" {
					parts = append(parts, t)
				}
				if u := c.Get("image_url.url").String(); u != "" {
					images = append(images, u)
				}
				return true
			})
			// function_call_output.output（工具调用结果）
			if out := item.Get("output").String(); out != "" {
				parts = append(parts, "[fn_output]"+out)
			}
			return true
		})
	} else if input.Type == gjson.String {
		if s := input.String(); s != "" {
			parts = append(parts, s)
		}
	}

	// instructions（系统级指令）
	if ins := gjson.GetBytes(body, "instructions").String(); ins != "" {
		parts = append(parts, "[instructions]"+ins)
	}

	return parts, images
}

func extractOpenAIResponsesReview(body []byte, parts []string, images []string) ([]string, []string) {
	input := gjson.GetBytes(body, "input")
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
			itemType := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
			includeMessage := role == "" || role == "user" || role == "tool" || role == "function"
			if itemType == "function_call_output" {
				if out := item.Get("output").String(); out != "" {
					parts = append(parts, out)
				}
				return true
			}
			if !includeMessage {
				return true
			}
			if t := item.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
			item.Get("content").ForEach(func(_, c gjson.Result) bool {
				return appendReviewContent(c, &parts, &images)
			})
			return true
		})
	} else if input.Type == gjson.String {
		if s := input.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return parts, images
}

// extractAnthropic 抽 Anthropic Messages 格式
func extractAnthropic(body []byte, parts []string, images []string) ([]string, []string) {
	// system 可以是 string 或 array of {type:"text", text:"..."}
	sys := gjson.GetBytes(body, "system")
	if sys.IsArray() {
		sys.ForEach(func(_, item gjson.Result) bool {
			if t := item.Get("text").String(); t != "" {
				parts = append(parts, "[system]"+t)
			}
			return true
		})
	} else if sys.Type == gjson.String {
		if s := sys.String(); s != "" {
			parts = append(parts, "[system]"+s)
		}
	}

	// messages[].content 同 OpenAI 也可能是 string 或 array
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if role := msg.Get("role").String(); role != "" {
			parts = append(parts, "[role:"+role+"]")
		}
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, item gjson.Result) bool {
				itemType := item.Get("type").String()
				switch itemType {
				case "text":
					if t := item.Get("text").String(); t != "" {
						parts = append(parts, t)
					}
				case "image":
					if src := item.Get("source.data").String(); src != "" {
						// base64 数据用 data:image URL 形式表示
						mime := item.Get("source.media_type").String()
						if mime == "" {
							mime = "image/jpeg"
						}
						images = append(images, "data:"+mime+";base64,"+src)
					}
					if u := item.Get("source.url").String(); u != "" {
						images = append(images, u)
					}
				case "tool_use":
					if name := item.Get("name").String(); name != "" {
						parts = append(parts, "[tool_use:"+name+"]")
					}
					if input := item.Get("input").Raw; input != "" {
						parts = append(parts, "[tool_use_input]"+input)
					}
				case "tool_result":
					tc := item.Get("content")
					if tc.IsArray() {
						tc.ForEach(func(_, sub gjson.Result) bool {
							if t := sub.Get("text").String(); t != "" {
								parts = append(parts, "[tool_result]"+t)
							}
							return true
						})
					} else if tc.Type == gjson.String {
						parts = append(parts, "[tool_result]"+tc.String())
					}
				}
				return true
			})
		} else if content.Type == gjson.String {
			parts = append(parts, content.String())
		}
		return true
	})

	// tools[].description / input_schema
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		if d := tool.Get("description").String(); d != "" {
			parts = append(parts, "[tool_desc]"+d)
		}
		if p := tool.Get("input_schema").Raw; p != "" {
			parts = append(parts, "[tool_schema]"+p)
		}
		return true
	})

	return parts, images
}

func extractAnthropicReview(body []byte, parts []string, images []string) ([]string, []string) {
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		if role != "" && role != "user" {
			return true
		}
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, item gjson.Result) bool {
				itemType := item.Get("type").String()
				switch itemType {
				case "text":
					if t := item.Get("text").String(); t != "" {
						parts = append(parts, t)
					}
				case "image":
					if src := item.Get("source.data").String(); src != "" {
						mime := item.Get("source.media_type").String()
						if mime == "" {
							mime = "image/jpeg"
						}
						images = append(images, "data:"+mime+";base64,"+src)
					}
					if u := item.Get("source.url").String(); u != "" {
						images = append(images, u)
					}
				case "tool_result":
					tc := item.Get("content")
					if tc.IsArray() {
						tc.ForEach(func(_, sub gjson.Result) bool {
							if t := sub.Get("text").String(); t != "" {
								parts = append(parts, t)
							}
							return true
						})
					} else if tc.Type == gjson.String {
						parts = append(parts, tc.String())
					}
				}
				return true
			})
		} else if content.Type == gjson.String {
			parts = append(parts, content.String())
		}
		return true
	})
	return parts, images
}

// extractGemini 抽 Gemini 格式：contents[].parts[].text + systemInstruction + tools.functionDeclarations
func extractGemini(body []byte, parts []string, images []string) ([]string, []string) {
	// contents[].parts[]
	gjson.GetBytes(body, "contents").ForEach(func(_, c gjson.Result) bool {
		if role := c.Get("role").String(); role != "" {
			parts = append(parts, "[role:"+role+"]")
		}
		c.Get("parts").ForEach(func(_, p gjson.Result) bool {
			if t := p.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
			// inline_data：base64 image
			if mime := p.Get("inline_data.mime_type").String(); strings.HasPrefix(mime, "image/") {
				if data := p.Get("inline_data.data").String(); data != "" {
					images = append(images, "data:"+mime+";base64,"+data)
				}
			}
			// file_data：URL 引用
			if u := p.Get("file_data.file_uri").String(); u != "" {
				images = append(images, u)
			}
			// functionCall.args（工具调用参数）
			if args := p.Get("functionCall.args").Raw; args != "" {
				parts = append(parts, "[fn_call_args]"+args)
			}
			// functionResponse.response（工具结果）
			if resp := p.Get("functionResponse.response").Raw; resp != "" {
				parts = append(parts, "[fn_response]"+resp)
			}
			// executableCode.code（代码执行 prompt）
			if code := p.Get("executableCode.code").String(); code != "" {
				parts = append(parts, "[code]"+code)
			}
			return true
		})
		return true
	})

	// systemInstruction
	gjson.GetBytes(body, "systemInstruction.parts").ForEach(func(_, p gjson.Result) bool {
		if t := p.Get("text").String(); t != "" {
			parts = append(parts, "[system_instruction]"+t)
		}
		return true
	})

	// tools[].functionDeclarations[].description / parameters
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		tool.Get("functionDeclarations").ForEach(func(_, fn gjson.Result) bool {
			if d := fn.Get("description").String(); d != "" {
				parts = append(parts, "[fn_desc]"+d)
			}
			if p := fn.Get("parameters").Raw; p != "" {
				parts = append(parts, "[fn_schema]"+p)
			}
			return true
		})
		return true
	})

	return parts, images
}

func extractGeminiReview(body []byte, parts []string, images []string) ([]string, []string) {
	gjson.GetBytes(body, "contents").ForEach(func(_, c gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(c.Get("role").String()))
		if role != "" && role != "user" && role != "function" {
			return true
		}
		c.Get("parts").ForEach(func(_, p gjson.Result) bool {
			if t := p.Get("text").String(); t != "" {
				parts = append(parts, t)
			}
			if mime := p.Get("inline_data.mime_type").String(); strings.HasPrefix(mime, "image/") {
				if data := p.Get("inline_data.data").String(); data != "" {
					images = append(images, "data:"+mime+";base64,"+data)
				}
			}
			if u := p.Get("file_data.file_uri").String(); u != "" {
				images = append(images, u)
			}
			if resp := p.Get("functionResponse.response").Raw; resp != "" {
				parts = append(parts, resp)
			}
			return true
		})
		return true
	})
	return parts, images
}

func appendReviewContent(content gjson.Result, parts *[]string, images *[]string) bool {
	if content.IsArray() {
		content.ForEach(func(_, item gjson.Result) bool {
			appendReviewContent(item, parts, images)
			return true
		})
		return true
	}
	if t := content.Get("text").String(); t != "" {
		*parts = append(*parts, t)
	}
	if u := content.Get("image_url.url").String(); u != "" {
		*images = append(*images, u)
	}
	if u := content.Get("image_url").String(); u != "" && !strings.HasPrefix(u, "{") {
		*images = append(*images, u)
	}
	if content.Type == gjson.String {
		if s := content.String(); s != "" {
			*parts = append(*parts, s)
		}
	}
	return true
}

// SafeTruncateRunes 按 rune 边界截断到 maxRunes（不切坏 UTF-8）。
// fix codex 第二十二轮：之前用 prompt[:32768] 字节截断会切坏中文字符。
func SafeTruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes])
}
