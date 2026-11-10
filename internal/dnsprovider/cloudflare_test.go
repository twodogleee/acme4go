// 本文件验证 Cloudflare Token 权限测试只清理本次记录，并使用受控服务商配置。
//
// @author Andersen
package dnsprovider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// cloudflareTestTransport 将服务商请求转换为内存响应。
type cloudflareTestTransport func(*http.Request) (*http.Response, error)

// RoundTrip 提供无需实际 Token 或网络的传输边界。
func (f cloudflareTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestCloudflarePermissionCreatesAndCleansOwnRecord 验证区域查询、临时记录创建和精确删除契约。
func TestCloudflarePermissionCreatesAndCleansOwnRecord(t *testing.T) {
	var calls []string
	client := cloudflareHTTPClient()
	// 所有结果仅包含测试区域和记录编号，不请求外部服务。
	client.Transport = cloudflareTestTransport(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("请求缺少 API Token")
		}
		calls = append(calls, request.Method+" "+request.URL.Path)
		body := `{"success":true,"result":{"id":"created-record"}}`
		if request.Method == http.MethodGet {
			if request.URL.Query().Get("name") != "example.com" {
				t.Error("查询了错误区域")
			}
			body = `{"success":true,"result":[{"id":"zone-id","name":"example.com"}]}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if err := testCloudflareCredential(context.Background(), client, cloudflareEndpoint, "synthetic-token", "example.com"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[2] != "DELETE /client/v4/zones/zone-id/dns_records/created-record" {
		t.Fatalf("没有精确清理本次创建记录：%v", calls)
	}
}

// TestCloudflareRefusesUnknownZoneAndUnsupportedProvider 验证区域不匹配时不产生 DNS 写操作。
func TestCloudflareRefusesUnknownZoneAndUnsupportedProvider(t *testing.T) {
	calls := 0
	client := cloudflareHTTPClient()
	// 权限不足时模拟返回空区域集合。
	client.Transport = cloudflareTestTransport(func(_ *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true,"result":[]}`))}, nil
	})
	if err := testCloudflareCredential(context.Background(), client, cloudflareEndpoint, "synthetic-token", "example.com"); err == nil || calls != 1 {
		t.Fatal("找不到目标区域时应立即失败")
	}
	if _, err := NewChallengeProvider("CLOUDFLARE", "", "synthetic-token", time.Minute, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := NewChallengeProvider("UNKNOWN", "", "", time.Minute, time.Second); err == nil {
		t.Fatal("未知服务商不能回退到其他凭据")
	}
}
