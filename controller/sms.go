// Package controller / sms.go
//
// 短信验证码发送 + 校验。
// 替换原 CompleteRisk 中硬编码 "1234" 的 mock。
//
// 流程：
//  1. 前端调 POST /api/auth/send-sms { phone }
//  2. 后端校验：手机号格式 / IP 限流 / 同号冷却（60s）
//  3. 生成 6 位随机码，存内存缓存（TTL 5 分钟）
//  4. 调阿里云 SMS 发送，模板参数 {"code":"123456"}
//  5. 前端用 phone+code 提交 CompleteRisk
//  6. CompleteRisk 调 verifySMSCode 校验，校验后立刻消费（防重放）
//
// 安全：
//   - 单 phone 60s 冷却，避免短信轰炸
//   - 单 IP 5 次/小时上限
//   - 验证码 6 位数字，5 分钟过期，校验后立刻删除
//   - 单 phone 最多 5 次错误尝试，超出立即作废本码（防 6 位空间暴破）
//   - 后台 sweeper 每 5 分钟清理过期条目，防止内存无界增长
package controller

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"

	"daof-cpa/proxy"
	"daof-cpa/utils"

	"github.com/gofiber/fiber/v2"
)

// 中国大陆手机号简单校验：1 开头 + 第 2 位 3-9 + 共 11 位
var chinaPhoneRegex = regexp.MustCompile(`^1[3-9]\d{9}$`)

const (
	smsCodeTTL        = 5 * time.Minute
	smsPhoneCooldown  = 60 * time.Second
	smsIPWindow       = 1 * time.Hour
	smsIPMaxPerWindow = 5
	smsMaxAttempts    = 5
	smsSweepInterval  = 5 * time.Minute
)

type smsCodeEntry struct {
	Code      string
	ExpiresAt time.Time
	Attempts  int // 失败计数，超 smsMaxAttempts 立刻作废
}

type smsRateEntry struct {
	count       int
	windowStart time.Time
}

var (
	smsCodeMu     sync.Mutex
	smsCodeCache  = map[string]*smsCodeEntry{} // key=phone
	smsCooldownMu sync.Mutex
	smsCooldown   = map[string]time.Time{} // key=phone, value=下一次允许发送时间
	smsIPRateMu   sync.Mutex
	smsIPRate     = map[string]*smsRateEntry{} // key=ip, 1小时窗口
)

// fix Minor（codex r5/r7 累积）：原 sweeper 仅 ticker loop 无 stop 通道。
// 加入 smsSweeperStopCh + StopSMSSweeper 让 main.go 的 SIGTERM 可以优雅停止 goroutine，
// 避免容器替换 / 测试 / 嵌入式启动时累积泄漏。
var smsSweeperStopCh = make(chan struct{})
var smsSweeperOnce sync.Once

// StartSMSSweeper 必须在 main.go 启动期调用一次。
// 后台清理过期条目，避免 map 无界增长（攻击者循环换号 / 换 IP 可耗尽内存）。
//
// fix Minor Mi-5（codex 第二十一轮）：用 sync.Once 防重复启动多 goroutine ——
// 测试套件多次调用 / 热重载场景下原实现会启动多个 sweeper goroutine 互相覆盖。
func StartSMSSweeper() {
	smsSweeperOnce.Do(func() {
		go startSMSSweeperLoop()
	})
}

func startSMSSweeperLoop() {
	ticker := time.NewTicker(smsSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-smsSweeperStopCh:
			return
		case <-ticker.C:
			now := time.Now()

			smsCodeMu.Lock()
			for k, v := range smsCodeCache {
				if now.After(v.ExpiresAt) {
					delete(smsCodeCache, k)
				}
			}
			smsCodeMu.Unlock()

			smsCooldownMu.Lock()
			for k, v := range smsCooldown {
				if now.After(v) {
					delete(smsCooldown, k)
				}
			}
			smsCooldownMu.Unlock()

			smsIPRateMu.Lock()
			for k, v := range smsIPRate {
				if now.Sub(v.windowStart) > smsIPWindow {
					delete(smsIPRate, k)
				}
			}
			smsIPRateMu.Unlock()
		}
	}
}

// smsSweeperStopOnce 独立 Once 保证 close(channel) 只触发一次（StartSMSSweeper 自己用 smsSweeperOnce）
var smsSweeperStopOnce sync.Once

// StopSMSSweeper 通知 sweeper goroutine 退出。幂等：重复调用安全。
func StopSMSSweeper() {
	smsSweeperStopOnce.Do(func() {
		close(smsSweeperStopCh)
	})
}

// SendSMSRequest 前端发送验证码请求
type SendSMSRequest struct {
	Phone string `json:"phone"`
}

// SendSMS 给前端调用，触发阿里云 SMS 发送
func SendSMS(c *fiber.Ctx) error {
	var req SendSMSRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"success": false, "message_code": "ERR_PARSE_PAYLOAD"})
	}
	req.Phone = strings.TrimSpace(req.Phone)
	if !chinaPhoneRegex.MatchString(req.Phone) {
		return c.Status(400).JSON(fiber.Map{"success": false, "message": "手机号格式不正确", "message_code": "ERR_PHONE_FORMAT"})
	}

	clientIP := utils.RealClientIP(c)

	// 1. 同号冷却 60s
	smsCooldownMu.Lock()
	if next, ok := smsCooldown[req.Phone]; ok && time.Now().Before(next) {
		smsCooldownMu.Unlock()
		secs := int(time.Until(next).Seconds())
		return c.Status(429).JSON(fiber.Map{
			"success":      false,
			"message":      fmt.Sprintf("请 %d 秒后再请求验证码", secs),
			"message_code": "ERR_SMS_COOLDOWN",
			"retry_after":  secs,
		})
	}
	smsCooldownMu.Unlock()

	// 2. 单 IP 1 小时 5 次上限
	smsIPRateMu.Lock()
	rate, ok := smsIPRate[clientIP]
	now := time.Now()
	if !ok || now.Sub(rate.windowStart) > smsIPWindow {
		smsIPRate[clientIP] = &smsRateEntry{count: 1, windowStart: now}
	} else {
		rate.count++
		if rate.count > smsIPMaxPerWindow {
			smsIPRateMu.Unlock()
			return c.Status(429).JSON(fiber.Map{
				"success":      false,
				"message":      "IP 请求过于频繁，请 1 小时后再试",
				"message_code": "ERR_SMS_IP_LIMIT",
			})
		}
	}
	smsIPRateMu.Unlock()

	// 3. 读 SysConfig 拿阿里云密钥
	proxy.SysConfigMutex.RLock()
	ak := strings.TrimSpace(proxy.SysConfigCache["aliyun_access_key"])
	sk := strings.TrimSpace(proxy.SysConfigCache["aliyun_access_secret"])
	signName := strings.TrimSpace(proxy.SysConfigCache["aliyun_sms_sign"])
	tplCode := strings.TrimSpace(proxy.SysConfigCache["aliyun_sms_template"])
	proxy.SysConfigMutex.RUnlock()

	if ak == "" || sk == "" || signName == "" || tplCode == "" {
		return c.Status(503).JSON(fiber.Map{
			"success":      false,
			"message":      "短信服务暂未配置，请联系管理员",
			"message_code": "ERR_SMS_NOT_CONFIGURED",
		})
	}

	// 4. 生成 6 位验证码
	code, err := generate6DigitCode()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"success": false, "message_code": "ERR_GEN_CODE"})
	}

	// 5. 调阿里云
	if err := utils.SendAliyunSMS(ak, sk, signName, tplCode, req.Phone, map[string]string{"code": code}); err != nil {
		// 不向客户端泄露 Aliyun 原始 err（含 SignName / TemplateCode 等）
		log.Printf("[SMS] send failed phone=%s err=%v", maskPhoneForLog(req.Phone), err)
		return c.Status(502).JSON(fiber.Map{
			"success":      false,
			"message":      "短信发送失败，请稍后重试",
			"message_code": "ERR_SMS_SEND_FAILED",
		})
	}

	// 6. 缓存验证码 + 设置冷却
	smsCodeMu.Lock()
	smsCodeCache[req.Phone] = &smsCodeEntry{
		Code:      code,
		ExpiresAt: time.Now().Add(smsCodeTTL),
	}
	smsCodeMu.Unlock()

	smsCooldownMu.Lock()
	smsCooldown[req.Phone] = time.Now().Add(smsPhoneCooldown)
	smsCooldownMu.Unlock()

	return c.JSON(fiber.Map{
		"success":      true,
		"message":      "验证码已发送，5 分钟内有效",
		"message_code": "SUCCESS_SMS_SENT",
	})
}

// verifySMSCode 校验 phone+code 是否匹配且未过期。
// 校验成功立刻消费（防重放）；连续失败 smsMaxAttempts 次立刻作废本码（防 6 位空间暴破）。
// 返回 true 表示通过，false 表示失败。
func verifySMSCode(phone, code string) bool {
	smsCodeMu.Lock()
	defer smsCodeMu.Unlock()
	entry, ok := smsCodeCache[phone]
	if !ok {
		return false
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(smsCodeCache, phone)
		return false
	}
	if entry.Code != code {
		entry.Attempts++
		if entry.Attempts >= smsMaxAttempts {
			// 超限直接作废，攻击者必须重新走 send-sms（受 60s phone 冷却 + 5/h IP 限流）
			delete(smsCodeCache, phone)
		}
		return false
	}
	// 一次性使用，立即删除
	delete(smsCodeCache, phone)
	return true
}

// generate6DigitCode 用 crypto/rand 生成 6 位数字验证码
func generate6DigitCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// maskPhoneForLog 日志中间号脱敏，避免明文泄露
func maskPhoneForLog(phone string) string {
	if len(phone) < 7 {
		return "***"
	}
	return phone[:3] + "****" + phone[len(phone)-4:]
}
