// Package dnsnames 提供 DNS 名称规范化和凭据域名范围匹配，供受理与持久化校验共用。
//
// @author Andersen
package dnsnames

import (
	"fmt"
	"strings"
)

// ValidName 校验 ASCII DNS 名称的长度、标签和连字符位置；不接受通配符。
func ValidName(value string) bool {
	if value == "" || len(value) > 253 || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

// NormalizeZones 规范化并去重显式根域名，保留输入顺序；空列表不代表无限授权。
func NormalizeZones(values []string) ([]string, error) {
	if len(values) > 100 {
		return nil, fmt.Errorf("一条凭据最多配置 100 个根域名")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		zone := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if !ValidName(zone) {
			return nil, fmt.Errorf("根域名 %q 格式无效，请填写 example.com，不要包含协议、路径或通配符", raw)
		}
		if !seen[zone] {
			seen[zone] = true
			result = append(result, zone)
		}
	}
	return result, nil
}

// CoversDomain 按 DNS 标签边界匹配根域名、子域名和证书通配符，禁止字符串后缀越权。
func CoversDomain(zones []string, domain string) bool {
	base := strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), "."), "*.")
	if !ValidName(base) {
		return false
	}
	for _, zone := range zones {
		if ValidName(zone) && (base == zone || strings.HasSuffix(base, "."+zone)) {
			return true
		}
	}
	return false
}
