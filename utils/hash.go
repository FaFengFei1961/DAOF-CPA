package utils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost bcrypt 工作因子。12 ≈ 250ms/次，足够抵抗离线暴破，
// 同时管理员登录还能承受。提升到 13+ 前请评估 SetupGuard 缓存命中率。
const BcryptCost = 12

// GenerateRandomToken 生成密码学安全随机 token，用于 user.Token / admin.Token。
// prefix 例如 "sk-daof"；返回 prefix + "-" + 32 hex 字符（128 位熵）。
//
// crypto/rand.Read 失败极其罕见（操作系统熵池异常），此时直接 panic 防止生成低熵 token。
// 调用方必须在启动期或 setup 期使用——运行期失败属于灾难级故障。
func GenerateRandomToken(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand.Read failed (entropy pool exhausted?): %v", err))
	}
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(b))
}

// GenerateHash 生成 bcrypt 密码哈希（cost = BcryptCost）。
// 输出形如 `$2a$12$...`，长度 60 字节。
//
// bcrypt 仅在密码超过 72 字节时才会失败，调用方应在更上层做长度校验；
// 如真发生失败，记录日志并返回空串，让登录路径自然走"密码错误"分支。
func GenerateHash(password string) string {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		log.Printf("[HASH] bcrypt failed (password too long?): %v", err)
		return ""
	}
	return string(hashed)
}

// GenerateHashForTest 是测试专用 bcrypt helper。固定 cost=bcrypt.MinCost (=4)，
// 单次约 1ms（vs 生产 GenerateHash 的 ~250ms）。
//
// 为什么独立函数而非降低 BcryptCost：
//   - BcryptCost=12 是生产安全要求；不允许通过 var/env 注入降级，避免误配置
//     在生产环境暴露弱 hash
//   - 函数名后缀 ForTest 让 code review 能立即识别误用
//   - bcrypt.CompareHashAndPassword 是 cost-agnostic（从 hash 字符串前缀读 cost），
//     所以测试用 cost=4 生成的 hash 在 handler 里 CheckHash 也以 ~1ms 验证完成
//
// 调用约束：**仅允许 _test.go 文件调用**。生产代码（含 controller / database /
// middleware / proxy）必须使用 GenerateHash。
//
// 测试收益：bcrypt setup + 验证从 ~500ms 降到 ~5ms，race detector 模式下也稳定
// 落在 Fiber app.Test 默认 1000ms 超时窗口内。
func GenerateHashForTest(password string) string {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		log.Printf("[HASH-TEST] bcrypt failed (password too long?): %v", err)
		return ""
	}
	return string(hashed)
}

// CheckHash 用 bcrypt 做恒定时间比较。空 hash 一律拒绝。
func CheckHash(password, storedHash string) bool {
	if storedHash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)) == nil
}
