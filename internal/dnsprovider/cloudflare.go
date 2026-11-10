// 本文件通过 Cloudflare API Token 验证目标区域的查询、TXT 写入和清理权限。
//
// @author Andersen
package dnsprovider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// cloudflareEndpoint 是唯一允许使用生产令牌访问的 Cloudflare API 入口。
const cloudflareEndpoint = "https://api.cloudflare.com/client/v4"

// cloudflareZone 是权限验证所需的最小区域描述。
type cloudflareZone struct {
	ID   string `json:"id"`   // ID 是服务商区域主键。
	Name string `json:"name"` // Name 是区域根域名。
}

// cloudflareRecord 仅承载本次临时创建的 TXT 记录编号。
type cloudflareRecord struct {
	ID string `json:"id"` // ID 是随后仅删除本次记录所需的标识。
}

// TestCloudflareCredential 验证单区域 API Token，不读取或返回任何令牌明文。
func TestCloudflareCredential(ctx context.Context, token, zone string) error {
	return testCloudflareCredential(ctx, cloudflareHTTPClient(), cloudflareEndpoint, token, zone)
}

// testCloudflareCredential 显式注入传输边界供隔离测试使用，生产入口固定。
func testCloudflareCredential(ctx context.Context, client *http.Client, endpoint, token, zone string) error {
	var zones []cloudflareZone
	if err := invokeCloudflare(ctx, client, http.MethodGet, endpoint+"/zones?name="+url.QueryEscape(zone), token, nil, &zones); err != nil {
		return err
	}
	if len(zones) != 1 || !strings.EqualFold(zones[0].Name, zone) {
		return fmt.Errorf("Cloudflare 未找到唯一匹配区域，请检查 Zone Read 权限")
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	var record cloudflareRecord
	body := map[string]any{"type": "TXT", "name": "_acme4go-permission-check-" + hex.EncodeToString(nonce) + "." + zone, "content": "acme4go-permission-check", "ttl": 120}
	recordsURL := endpoint + "/zones/" + url.PathEscape(zones[0].ID) + "/dns_records"
	if err := invokeCloudflare(ctx, client, http.MethodPost, recordsURL, token, body, &record); err != nil {
		return err
	}
	if record.ID == "" {
		return fmt.Errorf("Cloudflare 未返回临时记录编号")
	}
	// 原请求取消后仍尝试清理已经创建的临时记录。
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := invokeCloudflare(cleanupCtx, client, http.MethodDelete, recordsURL+"/"+url.PathEscape(record.ID), token, nil, nil); err != nil {
		return fmt.Errorf("临时 TXT 记录已创建但清理失败，请检查 DNS Write 权限")
	}
	return nil
}

// invokeCloudflare 执行带 Bearer 令牌的固定服务商请求，并只返回安全错误分类。
func invokeCloudflare(ctx context.Context, client *http.Client, method, endpoint, token string, input, output any) error {
	var body io.Reader
	if input != nil {
		content, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(content)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("Cloudflare 请求地址无效")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("Cloudflare API 连接失败或超时")
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool            `json:"success"` // Success 是服务商成功标识。
		Result  json.RawMessage `json:"result"`  // Result 是延迟解码的业务结果。
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("Cloudflare API 返回无效响应")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		return fmt.Errorf("Cloudflare API 权限验证失败（HTTP %d）", response.StatusCode)
	}
	if output != nil {
		return json.Unmarshal(envelope.Result, output)
	}
	return nil
}
