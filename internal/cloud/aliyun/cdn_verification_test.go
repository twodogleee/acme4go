// 本文件验证 CDN 证书身份兼容、状态门槛及诊断中的敏感数据边界。
//
// @author Andersen
package aliyun

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/cloud"
)

// cdnTestCertificate 生成独立证书，只返回公开 PEM 和 SHA-256 指纹。
func cdnTestCertificate(t *testing.T, serial int64) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{"cdn.example.com"}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), hex.EncodeToString(digest[:])
}

// TestCDNVerificationIdentityAndStates 验证不同 ID 可由叶证书证明身份，但不能绕过状态或冲突内容。
func TestCDNVerificationIdentityAndStates(t *testing.T) {
	content, fingerprint := cdnTestCertificate(t, 1)
	other, _ := cdnTestCertificate(t, 2)
	tests := []struct {
		name        string         // name 说明核验边界。
		changes     map[string]any // changes 覆盖有效响应字段。
		fingerprint string         // fingerprint 为目标证书指纹。
		id          string         // id 为目标证书中心编号。
		verified    bool           // verified 是预期核验结果。
		message     string         // message 是应显示的诊断片段。
	}{
		{"不同 ID 相同证书", map[string]any{"CertId": "999", "CertType": "upload"}, fingerprint, "123", true, "指纹一致"},
		{"缺失 ID 相同证书", map[string]any{"CertId": nil}, fingerprint, "123", true, "指纹一致"},
		{"指纹格式兼容", nil, " \n" + strings.ToUpper(fingerprint[:32]) + ":" + fingerprint[32:] + "\t", "123", true, "指纹一致"},
		{"域名规范化", map[string]any{"DomainName": "CDN.EXAMPLE.COM."}, fingerprint, "123", true, "指纹一致"},
		{"ID 一致但证书不同", map[string]any{"ServerCertificate": other}, fingerprint, "123", false, "指纹与目标证书不一致"},
		{"链中其他证书相同", map[string]any{"ServerCertificate": other + content}, fingerprint, "123", false, "指纹与目标证书不一致"},
		{"损坏首证书不能跳过", map[string]any{"ServerCertificate": "-----BEGIN CERTIFICATE-----\ninvalid!\n-----END CERTIFICATE-----\n" + content}, fingerprint, "123", false, "不是有效的 PEM"},
		{"私钥不能混入证书链", map[string]any{"ServerCertificate": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret-should-not-appear")})) + content}, fingerprint, "123", false, "不是有效的 PEM"},
		{"证书格式错误", map[string]any{"ServerCertificate": "secret-should-not-appear"}, fingerprint, "123", false, "不是有效的 PEM"},
		{"证书字段类型错误", map[string]any{"ServerCertificate": 123}, fingerprint, "123", false, "内容格式异常"},
		{"无效 DER", map[string]any{"ServerCertificate": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid")}))}, fingerprint, "123", false, "无法解析"},
		{"目标无指纹", nil, "", "123", false, "缺少有效"},
		{"目标为 SHA1", nil, strings.Repeat("A", 40), "123", false, "缺少有效"},
		{"未返回 PEM 兼容 ID", map[string]any{"ServerCertificate": nil, "CertId": json.Number("123")}, fingerprint, "123", true, "证书 ID 一致"},
		{"空 PEM 不同 ID", map[string]any{"ServerCertificate": "", "CertId": "999"}, fingerprint, "123", false, "ID 不一致或缺失"},
		{"空 ID 不匹配", map[string]any{"ServerCertificate": nil, "CertId": nil}, fingerprint, "", false, "ID 不一致或缺失"},
		{"域名不同", map[string]any{"DomainName": "other.example.com"}, fingerprint, "123", false, "未返回目标加速域名"},
		{"HTTPS 关闭", map[string]any{"ServerCertificateStatus": "off"}, fingerprint, "123", false, "未开启（off）"},
		{"HTTPS 缺失", map[string]any{"ServerCertificateStatus": nil}, fingerprint, "123", false, "HTTPS：未返回"},
		{"状态检测中", map[string]any{"Status": "checking"}, fingerprint, "123", false, "检测中（checking）"},
		{"状态失败", map[string]any{"Status": "failed"}, fingerprint, "123", false, "异常（failed）"},
		{"状态 null 指纹匹配", map[string]any{"Status": nil}, fingerprint, "123", true, "已通过证书指纹和 HTTPS 配置确认"},
		{"状态空串指纹匹配", map[string]any{"Status": ""}, fingerprint, "123", true, "已通过证书指纹和 HTTPS 配置确认"},
		{"状态空白指纹匹配", map[string]any{"Status": " \t"}, fingerprint, "123", true, "已通过证书指纹和 HTTPS 配置确认"},
		{"状态缺失不同 ID 指纹匹配", map[string]any{"Status": nil, "CertId": "999"}, fingerprint, "123", true, "已通过证书指纹和 HTTPS 配置确认"},
		{"状态缺失仅 ID 相同", map[string]any{"Status": nil, "ServerCertificate": nil}, fingerprint, "123", false, "证书状态：未返回"},
		{"状态缺失证书不同", map[string]any{"Status": nil, "ServerCertificate": other}, fingerprint, "123", false, "指纹与目标证书不一致"},
		{"状态缺失 HTTPS 关闭", map[string]any{"Status": nil, "ServerCertificateStatus": "off"}, fingerprint, "123", false, "未开启（off）"},
		{"状态缺失 HTTPS 缺失", map[string]any{"Status": nil, "ServerCertificateStatus": nil}, fingerprint, "123", false, "HTTPS：未返回"},
		{"状态缺失证书损坏", map[string]any{"Status": nil, "ServerCertificate": "invalid"}, fingerprint, "123", false, "不是有效的 PEM"},
		{"状态缺失目标无指纹", map[string]any{"Status": nil}, "", "123", false, "缺少有效"},
		{"状态类型错误", map[string]any{"Status": map[string]any{"secret": "secret-should-not-appear"}}, fingerprint, "123", false, "格式异常"},
		{"状态类型布尔值", map[string]any{"Status": false}, fingerprint, "123", false, "格式异常"},
		{"未知状态不泄露", map[string]any{"Status": "secret-should-not-appear"}, fingerprint, "123", false, "未知状态"},
	}
	for _, tt := range tests {
		// 对每种证书状态分别验证诊断和结果。
		t.Run(tt.name, func(t *testing.T) {
			item := map[string]any{"DomainName": "cdn.example.com", "CertId": "123", "ServerCertificate": content, "ServerCertificateStatus": "on", "Status": "success"}
			for key, value := range tt.changes {
				item[key] = value
			}
			// 限定查询范围并返回当前场景构造的证书信息。
			d := &CDN{RPC: rpcFunc(func(action string, values map[string]string) (map[string]any, error) {
				if action != "DescribeDomainCertificateInfo" || values["DomainName"] != "cdn.example.com" || len(values) != 1 {
					t.Fatalf("核验只能查询指定域名：%s %v", action, values)
				}
				return map[string]any{"CertInfos": map[string]any{"CertInfo": []any{item}}}, nil
			})}
			result, err := cloud.VerifyDeployment(context.Background(), d, cloud.Credentials{}, json.RawMessage(`{"domain":"cdn.example.com"}`), cloud.Certificate{ID: tt.id, Fingerprint: tt.fingerprint})
			if err != nil || result.Verified != tt.verified || !strings.Contains(result.Message, tt.message) {
				t.Fatalf("核验结论错误：%+v %v", result, err)
			}
			if strings.Contains(result.Message, "BEGIN CERTIFICATE") || strings.Contains(result.Message, "secret-should-not-appear") {
				t.Fatal("诊断不应包含原始响应或证书内容")
			}
		})
	}
}

// TestCDNVerificationIncompleteResponse 验证缺失和畸形结构不会被误判为成功。
func TestCDNVerificationIncompleteResponse(t *testing.T) {
	for _, body := range []map[string]any{nil, {"CertInfos": "invalid"}, {"CertInfos": map[string]any{}}, {"CertInfos": map[string]any{"CertInfo": []any{nil, "invalid"}}}} {
		// 返回缺失或畸形的证书信息以验证诊断。
		d := &CDN{RPC: rpcFunc(func(string, map[string]string) (map[string]any, error) { return body, nil })}
		result, err := d.VerifyDetailed(context.Background(), cloud.Credentials{}, json.RawMessage(`{"domain":"cdn.example.com"}`), cloud.Certificate{ID: "123"})
		if err != nil || result.Verified || result.Message == "" {
			t.Fatalf("畸形响应应提供诊断：%+v %v", result, err)
		}
	}
}

// TestCDNVerificationRPCFailure 验证 API 错误仍被保留，不伪装为证书尚未生效。
func TestCDNVerificationRPCFailure(t *testing.T) {
	want := errors.New("模拟 API 查询失败")
	// 返回原始 API 错误，验证调用方保留错误身份。
	d := &CDN{RPC: rpcFunc(func(string, map[string]string) (map[string]any, error) { return nil, want })}
	_, err := d.VerifyDetailed(context.Background(), cloud.Credentials{}, json.RawMessage(`{"domain":"cdn.example.com"}`), cloud.Certificate{ID: "123"})
	if !errors.Is(err, want) {
		t.Fatalf("未保留查询错误：%v", err)
	}
}
