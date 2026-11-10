// 本文件验证多根域名凭据兼容性、范围保护和测试结果脱敏。
//
// @author Andersen
package app

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestMultipleZoneCredentialCompatibility 验证真实加密保存、旧接口编辑保护、跨根域名申请及移除引用保护。
func TestMultipleZoneCredentialCompatibility(t *testing.T) {
	s, _, _, _, _ := cloudFixture(t)
	ctx := context.Background()
	input := CredentialInput{DNSCredentialInput: DNSCredentialInput{Name: "多域名", Provider: "ALIYUN", Zones: []string{"Example.COM.", "example.net", "example.com"}, AccessKeyID: "multi-zone-key", AccessKeySecret: "multi-zone-secret", Enabled: true}, DNSEnabled: true}
	v, err := s.SaveCredential(ctx, 1, 0, input)
	if err != nil || !reflect.DeepEqual(v.Zones, []string{"example.com", "example.net"}) || v.Zone != "example.com" {
		t.Fatalf("保存失败: %v", err)
	}
	input.Zones = nil
	input.Zone = v.Zone
	input.AccessKeyID, input.AccessKeySecret = "", ""
	v, err = s.SaveCredential(ctx, 1, v.ID, input)
	if err != nil || len(v.Zones) != 2 {
		t.Fatal("旧接口误删了额外根域名", err)
	}
	decrypted, err := s.DecryptedDNSCredential(ctx, v.ID)
	if err != nil || decrypted.AccessKeySecret != "multi-zone-secret" {
		t.Fatal("编辑丢失了秘密")
	}
	order, err := s.CreateOrder(ctx, nil, CreateOrderInput{Name: "跨域证书", Domains: []string{"api.example.com", "*.example.net"}, Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CredentialID: v.ID, AgreeTerms: true})
	if err != nil {
		t.Fatal("多根域名申请被拒绝", err)
	}
	input.Zones = []string{"example.com"}
	if _, err := s.SaveCredential(ctx, 1, v.ID, input); err == nil || !strings.Contains(err.Error(), "跨域证书") {
		t.Fatalf("没有说明受影响系列: %v", err)
	}
	input.Zones = []string{}
	if _, err := s.SaveCredential(ctx, 1, v.ID, input); err == nil {
		t.Fatal("DNS 空范围应拒绝")
	}
	input.Zones = []string{"example.com", "example.net", "example.org"}
	if _, err := s.SaveCredential(ctx, 1, v.ID, input); err != nil {
		t.Fatal("扩大范围应被允许", err)
	}
	if err := validateDomainsInZone([]string{"example.com", "*.example.net"}, "example.com", "example.net"); err != nil {
		t.Fatal(err)
	}
	if err := validateDomainsInZone([]string{"badexample.com"}, "example.com", "example.net"); err == nil {
		t.Fatal("后缀相似域名越权")
	}
	if order.SeriesID == "" {
		t.Fatal("没有生成证书系列")
	}
}

// TestCredentialZoneTestsContinueAndRedact 验证一项失败不跳过后续域名，且错误中的秘密不进入结果。
func TestCredentialZoneTestsContinueAndRedact(t *testing.T) {
	calls := []string{}
	// 记录全部调用并模拟第二个根域名失败。
	result := testCredentialZones(context.Background(), []string{"a.example.com", "b.example.com", "c.example.com"}, func(_ context.Context, zone string) error {
		calls = append(calls, zone)
		if strings.HasPrefix(zone, "b.") {
			return errors.New("secret-must-not-leak")
		}
		return nil
	})
	if result.Success || len(calls) != 3 || len(result.Results) != 3 || !result.Results[2].Success || result.Results[1].Success {
		t.Fatal("部分失败处理错误")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "secret-must-not-leak") {
		t.Fatal("泄露服务商原始错误")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 已取消时不允许继续调用服务商。
	result = testCredentialZones(ctx, []string{"example.com"}, func(context.Context, string) error { t.Fatal("取消后仍发起外部调用"); return nil })
	if result.Success || len(result.Results) != 1 || result.Results[0].Success {
		t.Fatal("取消被误判成功")
	}
}
