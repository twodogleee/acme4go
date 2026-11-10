// 本文件验证证书域名语法、通配符规则与 DNS 凭据根域名授权边界。
//
// @author Andersen
package app

import "testing"

// TestNormalizeDomainsRejectsMalformedLabels 验证空标签、非法字符和错误连字符位置被拒绝。
func TestNormalizeDomainsRejectsMalformedLabels(t *testing.T) {
	for _, domains := range [][]string{{"api..example.com"}, {"-api.example.com"}, {"api_.example.com"}, {"foo.*.example.com"}} {
		if _, err := normalizeDomains(domains); err == nil {
			t.Fatalf("非法域名应被拒绝: %v", domains)
		}
	}
	values, err := normalizeDomains([]string{"example.com", "*.example.com", "api.example.com"})
	if err != nil || len(values) != 3 {
		t.Fatalf("合法域名组合应通过校验: values=%v err=%v", values, err)
	}
}

// TestValidateDomainsInZoneRequiresContainment 验证订单全部域名必须位于所选凭据根域名内。
func TestValidateDomainsInZoneRequiresContainment(t *testing.T) {
	if err := validateDomainsInZone([]string{"example.com", "*.example.com", "api.example.com"}, "example.com"); err != nil {
		t.Fatalf("根域名及其子域名应被凭据覆盖: %v", err)
	}
	if err := validateDomainsInZone([]string{"example.net"}, "example.com"); err == nil {
		t.Fatal("根域名之外的订单域名必须被拒绝")
	}
}
