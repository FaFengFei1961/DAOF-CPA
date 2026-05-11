// Package utils / aliyun_sms.go
//
// 阿里云短信发送 - HTTP 直调 dysmsapi.aliyuncs.com，不依赖外部 SDK。
// 使用 v1 RPC 风格 + HMAC-SHA1 签名（2017-05-25 API 版本）。
// 注意：阿里云已推荐迁移到 v3 ACS3-HMAC-SHA256，本实现保留 v1 兼容已配置的 SignName/Template。
//
// admin 在 SysConfig 配置：
//   - aliyun_access_key
//   - aliyun_access_secret
//   - aliyun_sms_sign      (例: "DAOF网关")
//   - aliyun_sms_template  (例: "SMS_123456789")
//
// 模板必须用 ${code} 占位符，调用方传 code 即可。
package utils

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SendAliyunSMS 调用阿里云短信 API 发送验证码。
// templateParam 形如 {"code":"123456"}，必须与模板占位匹配。
//
// 错误：网络失败 / 阿里云返回非 OK / 配置缺失 → 返回带上下文的 error。
func SendAliyunSMS(accessKey, accessSecret, signName, templateCode, phone string, templateParam map[string]string) error {
	if accessKey == "" || accessSecret == "" {
		return fmt.Errorf("aliyun SMS access key/secret 未配置")
	}
	if signName == "" || templateCode == "" {
		return fmt.Errorf("aliyun SMS sign/template 未配置")
	}
	if phone == "" {
		return fmt.Errorf("phone empty")
	}

	tplJSON, err := json.Marshal(templateParam)
	if err != nil {
		return fmt.Errorf("marshal template param: %w", err)
	}

	// 公共 + 业务参数（v1 RPC POST application/x-www-form-urlencoded）
	params := map[string]string{
		"AccessKeyId":      accessKey,
		"Action":           "SendSms",
		"Format":           "JSON",
		"PhoneNumbers":     phone,
		"SignName":         signName,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   strconv.FormatInt(time.Now().UnixNano(), 10),
		"SignatureVersion": "1.0",
		"TemplateCode":     templateCode,
		"TemplateParam":    string(tplJSON),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2017-05-25",
	}

	signature := signAliyunRPC("POST", params, accessSecret)
	params["Signature"] = signature

	// 拼 form body
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	httpReq, err := http.NewRequest("POST", "https://dysmsapi.aliyuncs.com/", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build req: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do req: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))

	var parsed struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("aliyun SMS unparseable response (status=%d, body_prefix=%.200q): %w", resp.StatusCode, string(body), err)
	}
	if parsed.Code != "OK" {
		return fmt.Errorf("aliyun SMS code=%s msg=%s", parsed.Code, parsed.Message)
	}
	return nil
}

// signAliyunRPC 阿里云 v1 RPC 签名算法
//
//	StringToSign = HTTPMethod + "&" + percentEncode("/") + "&" + percentEncode(canonicalQueryString)
//	HMAC-SHA1(StringToSign, accessSecret + "&")
func signAliyunRPC(method string, params map[string]string, accessSecret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var pairs []string
	for _, k := range keys {
		pairs = append(pairs, percentEncode(k)+"="+percentEncode(params[k]))
	}
	canonical := strings.Join(pairs, "&")
	stringToSign := method + "&" + percentEncode("/") + "&" + percentEncode(canonical)

	mac := hmac.New(sha1.New, []byte(accessSecret+"&"))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// percentEncode 阿里云 v1 签名要求的特殊百分号编码（与 url.QueryEscape 略有差异）
func percentEncode(s string) string {
	encoded := url.QueryEscape(s)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "*", "%2A")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}
