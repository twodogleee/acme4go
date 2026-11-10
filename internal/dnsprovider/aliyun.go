// Package dnsprovider 封装控制平面支持的 DNS 服务商能力。
//
// @author Andersen
package dnsprovider

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- 阿里云旧版 RPC 签名协议固定要求 HMAC-SHA1。
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// aliyunEndpoint 是阿里云 DNS 旧版 RPC API 入口。
const aliyunEndpoint = "https://alidns.aliyuncs.com/"

// TestAliyunCredential 在指定根域名创建并清理临时 TXT 记录，验证实际签发所需写权限。
func TestAliyunCredential(ctx context.Context, accessKeyID, accessKeySecret, zone string) error {
	// 将当前凭据绑定到创建和清理记录所用的 RPC 调用。
	return testAliyunCredential(ctx, zone, func(callCtx context.Context, action map[string]string) (map[string]any, error) {
		return invokeAliyun(callCtx, accessKeyID, accessKeySecret, action)
	})
}

// testAliyunCredential 注入 API 调用以隔离测试，取消原请求后仍清理已创建的临时记录。
func testAliyunCredential(ctx context.Context, zone string, invoke func(context.Context, map[string]string) (map[string]any, error)) error {
	nonce := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	recordName := "_acme4go-permission-check-" + hex.EncodeToString(nonce)
	result, err := invoke(ctx, map[string]string{
		"Action": "AddDomainRecord", "DomainName": zone, "RR": recordName, "Type": "TXT", "Value": "acme4go-permission-check",
	})
	if err != nil {
		return err
	}
	recordID, _ := result["RecordId"].(string)
	if recordID == "" {
		return errors.New("阿里云 DNS 未返回临时记录编号")
	}
	// 清理不复用可能已达到单域名或总测试时限的上下文。
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := invoke(cleanupCtx, map[string]string{"Action": "DeleteDomainRecord", "RecordId": recordID}); err != nil {
		return fmt.Errorf("临时 DNS 验证记录创建成功但清理失败: %w", err)
	}
	return nil
}

// invokeAliyun 按阿里云 RPC 规范签名并执行一项 DNS API 操作。
func invokeAliyun(ctx context.Context, accessKeyID, accessKeySecret string, action map[string]string) (map[string]any, error) {
	nonce := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	params := map[string]string{
		"Format":           "JSON",
		"Version":          "2015-01-09",
		"AccessKeyId":      accessKeyID,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   hex.EncodeToString(nonce),
		"SignatureVersion": "1.0",
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	for key, value := range action {
		params[key] = value
	}
	canonical := canonicalQuery(params)
	stringToSign := "GET&%2F&" + percentEncode(canonical)
	mac := hmac.New(sha1.New, []byte(accessKeySecret+"&"))
	_, _ = mac.Write([]byte(stringToSign))
	params["Signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, aliyunEndpoint+"?"+canonicalQuery(params), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("连接阿里云 DNS: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, errors.New("阿里云 DNS 返回了无效 JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code, _ := result["Code"].(string)
		if code == "" {
			code = response.Status
		}
		return nil, fmt.Errorf("阿里云 DNS 调用失败（%s）", code)
	}
	if code, ok := result["Code"].(string); ok && code != "" {
		return nil, fmt.Errorf("阿里云 DNS 调用失败（%s）", code)
	}
	return result, nil
}

// canonicalQuery 按参数名排序并生成签名使用的规范查询字符串。
func canonicalQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, percentEncode(key)+"="+percentEncode(params[key]))
	}
	return strings.Join(parts, "&")
}

// percentEncode 将查询值转换为阿里云 RPC 要求的 RFC 3986 编码。
func percentEncode(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
