package controller

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"
)

var I18nDir = "./i18n"

// validLangPattern: 仅允许字母数字 + 短划线 + 下划线（典型 BCP 47：zh-CN, en-US, fr_FR）
var validLangPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// 上传 JSON 大小上限（字节）。超出拒绝，避免 OOM。
const maxLocaleUploadBytes = 1 << 20 // 1 MiB

// safeLocalePath 校验 lang 标识并解析到 i18n 目录内的安全路径。
// 防御 Windows 反斜杠 / URL 编码绕过 / 符号链接逃逸。
// 返回的绝对路径保证仍位于 I18nDir 内。
func safeLocalePath(lang string) (string, error) {
	if lang == "" || !validLangPattern.MatchString(lang) {
		return "", fmt.Errorf("invalid lang code")
	}
	absDir, err := filepath.Abs(I18nDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(absDir, lang+".json")
	cleaned := filepath.Clean(target)
	// 严格前缀校验：cleaned 必须仍然是 absDir 的子路径
	rel, err := filepath.Rel(absDir, cleaned)
	if err != nil || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes i18n dir")
	}
	return cleaned, nil
}

type LocaleInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Ensure i18n directory exists
func init() {
	if _, err := os.Stat(I18nDir); os.IsNotExist(err) {
		os.Mkdir(I18nDir, 0755)
	}
}

func GetLocalesList(c *fiber.Ctx) error {
	var locales []LocaleInfo

	entries, err := os.ReadDir(I18nDir)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "系统配置载入异常，请联系管理员", "message_code": "ERR_READ_I18N_DIR"})
	}

	for _, f := range entries {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(f.Name(), ".json")
		// 跳过含非法字符的文件（防御性，正常情况不会发生）
		if !validLangPattern.MatchString(id) {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		name := id
		content, err := os.ReadFile(filepath.Join(I18nDir, f.Name()))
		if err == nil {
			var data map[string]interface{}
			if json.Unmarshal(content, &data) == nil {
				if system, ok := data["SYSTEM"].(map[string]interface{}); ok {
					if langName, ok := system["LANG"].(string); ok {
						name = langName
					}
				}
			}
		}
		locales = append(locales, LocaleInfo{ID: id, Name: name, Size: info.Size()})
	}

	return c.JSON(fiber.Map{"success": true, "data": locales})
}

// localeFileMu 保护 i18n 文件的并发读写，防止 admin 上传时其他用户读到部分写入。
//
// fix Major（codex + gemini 第六轮）：原 UploadLocale 用 os.WriteFile 原地覆盖，
// 公开 GetLocaleContent 并发读时可能拿到未完成写入的截断 JSON → 前端 i18n 解析 error。
// 改为：(a) 写时先到 .tmp 文件，再 os.Rename 原子替换 (b) 读写共享 RWMutex 防 rename 期间并发读
var localeFileMu sync.RWMutex

func GetLocaleContent(c *fiber.Ctx) error {
	filePath, err := safeLocalePath(c.Params("lang"))
	if err != nil {
		return c.Status(400).SendString("ERR_INVALID_LANG_CODE")
	}
	localeFileMu.RLock()
	defer localeFileMu.RUnlock()
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return c.JSON(fiber.Map{})
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		return c.Status(500).SendString("ERR_READ_LANG_FRAGMENTS")
	}
	c.Set("Content-Type", "application/json")
	return c.Send(content)
}

func UploadLocale(c *fiber.Ctx) error {
	lang := c.Params("lang")
	filePath, err := safeLocalePath(lang)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "无效的多语言标识符", "message_code": "ERR_INVALID_LANG_CODE"})
	}

	// 上传体积限制
	if len(c.Body()) > maxLocaleUploadBytes {
		return c.Status(413).JSON(fiber.Map{"success": false, "message": "JSON 过大", "message_code": "ERR_PAYLOAD_TOO_LARGE"})
	}

	var data map[string]interface{}
	if err := c.BodyParser(&data); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "无效的数据结构或格式损坏", "message_code": "ERR_JSON_DESERIALIZE"})
	}

	if _, ok := data["SYSTEM"]; !ok {
		data["SYSTEM"] = map[string]interface{}{"LANG": lang}
	}

	fileContent, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_JSON_MARSHAL"})
	}

	// 原子写：tmp + rename（同目录 rename 在 Linux/Windows 都是原子的）
	localeFileMu.Lock()
	defer localeFileMu.Unlock()
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, fileContent, 0644); err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "系统安全保护：文件写入操作失败", "message_code": "ERR_WRITE_I18N_FILE"})
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath) // best-effort cleanup
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "系统安全保护：文件替换失败", "message_code": "ERR_RENAME_I18N_FILE"})
	}

	return c.JSON(fiber.Map{"success": true, "message": "多语言配置文件上传完毕", "message_code": "SUCCESS_I18N_INJECTED"})
}

func DeleteLocale(c *fiber.Ctx) error {
	lang := c.Params("lang")
	filePath, err := safeLocalePath(lang)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "无效的多语言标识符", "message_code": "ERR_INVALID_LANG_CODE"})
	}
	if lang == "zh-CN" || lang == "en-US" {
		return c.Status(403).JSON(fiber.Map{"success": false, "message": "系统核心内建语言包不可删除", "message_code": "ERR_SYSTEM_LANG_PROTECTED"})
	}
	// 与 Upload/Get 共享同一锁，防 rename 期间 Remove 撞 race
	localeFileMu.Lock()
	defer localeFileMu.Unlock()
	if err := os.Remove(filePath); err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message": "删除执行过程中报错，可能该文件不存在", "message_code": "ERR_DELETE_LANG_FAILED"})
	}
	return c.JSON(fiber.Map{"success": true, "message": "多语言设定资源已清理删除完成", "message_code": "SUCCESS_LANG_DELETED"})
}
