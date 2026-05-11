// Package proxy / prompt_extract.go
//
// 从代理请求体抽取所有用户可控文本，拼成单个 string 给关键字 / Moderation 审核。
//
// 设计原则（codex 第二十三轮反馈）：
//   - 覆盖 5 种入口格式：OpenAI ChatCompletion / OpenAI Responses / Anthropic Messages / Gemini / Codex
//   - 抽取 tool / function / schema 相关字段（攻击者可能把 jailbreak 藏在 tool description 或 result）
//   - 多模态：image_url 的 URL 不提取（无文本可审）；alt-text / caption 提取
//   - **失败 fail-closed**：如果 body 不是有效 JSON，返回空字符串 + error，调用方决定是否拒绝
//
// 不做：
//   - base64 image OCR（image_policy=submit 时把 image_url 透传给 OpenAI Moderation）
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

// ExtractPromptText 按 srcFormat 抽取文本和 image。失败返回 (空结果, error)。
//
// 调用方根据 ImagePolicy 决定：
//   - "skip"   → 忽略 ImageURLs
//   - "submit" → 把 ImageURLs 也送 OpenAI Moderation（omni-moderation-latest 接受）
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
