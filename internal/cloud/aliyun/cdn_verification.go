// 本文件核对 CDN 返回的叶证书身份与启用状态，兼容证书中心和 CDN 使用不同 ID 的情况。
//
// @author Andersen
package aliyun

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"strings"

	"github.com/twodogleee/acme4go/internal/cloud"
)

// VerifyDetailed 核对域名、证书及 HTTPS；缺少 Status 时须用实际叶证书指纹确认绑定。
func (d *CDN) VerifyDetailed(ctx context.Context, credential cloud.Credentials, raw json.RawMessage, cert cloud.Certificate) (cloud.Verification, error) {
	cfg, _, err := parseConfig(raw, false)
	if err != nil {
		return cloud.Verification{}, err
	}
	body, err := d.RPC.Call(ctx, credential, "cdn.aliyuncs.com", "2018-05-10", "DescribeDomainCertificateInfo", map[string]string{"DomainName": cfg.Domain})
	if err != nil {
		return cloud.Verification{}, err
	}
	infos, ok := body["CertInfos"].(map[string]any)
	if !ok {
		return cloud.Verification{Message: "CDN 未返回有效的 CertInfos，无法确认域名证书配置"}, nil
	}
	items, ok := infos["CertInfo"].([]any)
	if !ok {
		return cloud.Verification{Message: "CDN 未返回有效的 CertInfo 列表，无法确认域名证书配置"}, nil
	}
	result := cloud.Verification{Message: "CDN 未返回目标加速域名的证书配置"}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || strings.TrimSuffix(strings.ToLower(strings.TrimSpace(text(item["DomainName"]))), ".") != cfg.Domain {
			continue
		}
		identityOK, identityMessage := cdnCertificateIdentity(item, cert)
		https := strings.TrimSpace(text(item["ServerCertificateStatus"]))
		statusValue, statusIsString := item["Status"].(string)
		status := strings.TrimSpace(statusValue)
		statusOK := status == "success"
		statusMessage := cdnStatusLabel(status)
		if item["Status"] != nil && !statusIsString {
			statusMessage = "格式异常"
		} else if status == "" && identityOK && https == "on" && strings.TrimSpace(text(item["ServerCertificate"])) != "" {
			// 已上传证书的实际响应可能没有 Status。仅在真实叶证书已匹配时兼容，
			// 不把缺字段等同于 success，也不放过明确的 checking、failed 等状态。
			statusOK = true
			statusMessage = "未返回（已通过证书指纹和 HTTPS 配置确认）"
		}
		result = cloud.Verification{
			Verified: identityOK && https == "on" && statusOK,
			Message:  identityMessage + "；HTTPS：" + cdnStatusLabel(https) + "；证书状态：" + statusMessage,
		}
		if result.Verified {
			return result, nil
		}
	}
	return result, nil
}

// cdnCertificateIdentity 优先核对真实叶证书；只有未返回证书内容时才回退到非空 ID。
func cdnCertificateIdentity(item map[string]any, cert cloud.Certificate) (bool, string) {
	content, isString := item["ServerCertificate"].(string)
	if value := item["ServerCertificate"]; value != nil && !isString {
		return false, "CDN 证书内容格式异常"
	}
	content = strings.TrimSpace(content)
	if content != "" {
		fingerprint := normalizeFingerprint(cert.Fingerprint)
		if fingerprint == "" {
			return false, "目标证书缺少有效的 SHA-256 指纹，无法比对 CDN 证书内容"
		}
		// 限定首个 PEM 块，避免 pem.Decode 跳过损坏的叶证书后误用链中后续证书。
		endMarker := "-----END CERTIFICATE-----"
		end := strings.Index(content, endMarker)
		if !strings.HasPrefix(content, "-----BEGIN CERTIFICATE-----") || end < 0 {
			return false, "CDN 返回的证书内容不是有效的 PEM 证书"
		}
		block, _ := pem.Decode([]byte(content[:end+len(endMarker)]))
		if block == nil || block.Type != "CERTIFICATE" {
			return false, "CDN 返回的证书内容不是有效的 PEM 证书"
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return false, "CDN 返回的证书内容无法解析"
		}
		// 只核对第一张叶证书，不能因为链中的其他证书匹配而接受错误绑定。
		digest := sha256.Sum256(leaf.Raw)
		if !strings.EqualFold(hex.EncodeToString(digest[:]), fingerprint) {
			return false, "CDN 当前证书 SHA-256 指纹与目标证书不一致"
		}
		return true, "证书 SHA-256 指纹一致"
	}
	if cert.ID != "" && strings.TrimSpace(text(item["CertId"])) == cert.ID {
		return true, "证书 ID 一致（CDN 未返回证书内容）"
	}
	return false, "CDN 证书 ID 不一致或缺失，且未返回证书内容供 SHA-256 比对"
}

// cdnStatusLabel 仅展示文档定义的状态，避免将任意 API 返回内容写入任务和告警。
func cdnStatusLabel(status string) string {
	switch status {
	case "on":
		return "已开启（on）"
	case "off":
		return "未开启（off）"
	case "success":
		return "成功（success）"
	case "checking":
		return "检测中（checking）"
	case "applying":
		return "申请中（applying）"
	case "cname_error", "top_domain_cname_error", "domain_invalid", "unsupport_wildcard", "get_token_timeout", "check_token_timeout", "get_cert_timeout", "failed":
		return "异常（" + status + "）"
	case "":
		return "未返回"
	default:
		return "未知状态"
	}
}
