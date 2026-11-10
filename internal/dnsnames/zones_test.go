// 本文件验证根域名规范化及域名授权边界。
//
// @author Andersen
package dnsnames

import (
	"reflect"
	"testing"
)

// TestZoneNormalizationAndBoundaries 验证规范化、去重、数量和标签边界，不把空列表解释为通配权限。
func TestZoneNormalizationAndBoundaries(t *testing.T) {
	zones, err := NormalizeZones([]string{" Example.COM. ", "example.net", "example.com"})
	if err != nil || !reflect.DeepEqual(zones, []string{"example.com", "example.net"}) {
		t.Fatalf("规范化失败: %v %v", zones, err)
	}
	for _, domain := range []string{"example.com", "*.example.net", "a.b.example.net"} {
		if !CoversDomain(zones, domain) {
			t.Fatalf("应允许 %s", domain)
		}
	}
	for _, domain := range []string{"example.org", "badexample.com", "example.com.evil.net", "*.*.example.com", ""} {
		if CoversDomain(zones, domain) {
			t.Fatalf("不应允许 %s", domain)
		}
	}
	if CoversDomain(nil, "example.com") {
		t.Fatal("空范围不应授权")
	}
	for _, zone := range []string{"", "*.example.com", "https://example.com", "example.com/path", "a..com", "-a.com"} {
		if _, err := NormalizeZones([]string{zone}); err == nil {
			t.Fatalf("非法根域名被接受: %s", zone)
		}
	}
	if _, err := NormalizeZones(make([]string, 101)); err == nil {
		t.Fatal("没有限制根域名数量")
	}
}
