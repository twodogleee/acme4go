// 本文件验证阿里云证书指纹字段兼容、格式校验及已上传证书的幂等恢复。
//
// @author Andersen
package aliyun

import (
	"context"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/cloud"
)

// TestCertificateFingerprintFields 验证详情字段优先级和旧格式兼容，拒绝缺失、SHA-1 或非法指纹。
func TestCertificateFingerprintFields(t *testing.T) {
	fingerprint := strings.Repeat("AB", 32)
	cases := []struct {
		name string         // name 是响应场景。
		body map[string]any // body 是不含私钥的云端元数据。
		want string         // want 是期望指纹；空串表示应拒绝响应。
	}{
		{name: "CertSha2", body: map[string]any{"CertSha2": fingerprint}, want: fingerprint},
		{name: "旧版Sha2", body: map[string]any{"Sha2": fingerprint}, want: fingerprint},
		{name: "详情优先", body: map[string]any{"CertSha2": fingerprint, "Sha2": strings.Repeat("CD", 32)}, want: fingerprint},
		{name: "格式兼容", body: map[string]any{"CertSha2": " \n" + strings.Repeat("ab:", 31) + "ab\t"}, want: fingerprint},
		{name: "空字段回退", body: map[string]any{"CertSha2": " ", "Sha2": fingerprint}, want: fingerprint},
		{name: "缺失", body: map[string]any{}},
		{name: "SHA1不能代替SHA256", body: map[string]any{"Fingerprint": strings.Repeat("AB", 20)}},
		{name: "截断", body: map[string]any{"Sha2": "ABCD"}},
		{name: "非十六进制", body: map[string]any{"CertSha2": strings.Repeat("XY", 32)}},
		{name: "非法详情不能回退绕过", body: map[string]any{"CertSha2": "invalid", "Sha2": fingerprint}},
	}
	for _, tc := range cases {
		// 每个响应都经真实 Get 解析，验证私钥过滤条件仍然生效。
		t.Run(tc.name, func(t *testing.T) {
			tc.body["Id"] = "123"
			// 返回场景元数据，并确保查询不携带下载私钥的选项。
			catalog := Catalog{RPC: rpcFunc(func(action string, params map[string]string) (map[string]any, error) {
				if action != "GetUserCertificateDetail" || params["CertId"] != "123" || params["CertFilter"] != "true" {
					t.Fatal("校验证书必须使用过滤私钥的详情查询")
				}
				return tc.body, nil
			})}
			cert, err := catalog.Get(context.Background(), cloud.Credentials{}, "123")
			if tc.want == "" {
				if err == nil || !strings.Contains(err.Error(), "未返回有效的 SHA-256 指纹") {
					t.Fatalf("无效指纹未被明确拒绝：%v", err)
				}
				return
			}
			if err != nil || cert.Fingerprint != tc.want {
				t.Fatalf("解析指纹失败：%q，错误：%v", cert.Fingerprint, err)
			}
		})
	}
}

// TestUploadRecoveryChecksDetail 验证同名证书经详情校验后才能复用，真正不一致时不得重复上传。
func TestUploadRecoveryChecksDetail(t *testing.T) {
	fingerprint := strings.Repeat("AB", 32)
	for _, matches := range []bool{true, false} {
		calls := 0
		// 模拟旧列表摘要与详情不一致，仅详情中的证书指纹具有最终决定权。
		catalog := Catalog{RPC: rpcFunc(func(action string, params map[string]string) (map[string]any, error) {
			calls++
			switch action {
			case "ListUserCertificateOrder":
				return map[string]any{"TotalCount": 1, "CertificateOrderList": []any{map[string]any{"CertificateId": "123", "Name": "same", "Sha2": strings.Repeat("CD", 32)}}}, nil
			case "GetUserCertificateDetail":
				if params["CertFilter"] != "true" {
					t.Fatal("不得下载私钥")
				}
				remote := strings.Repeat("EF", 32)
				if matches {
					remote = fingerprint
				}
				return map[string]any{"Id": "123", "CertSha2": remote}, nil
			default:
				t.Fatalf("已有同名证书不应再次上传：%s", action)
				return nil, nil
			}
		})}
		cert, err := catalog.Upload(context.Background(), cloud.Credentials{}, cloud.Material{Name: "same", Fingerprint: fingerprint})
		if calls != 2 {
			t.Fatalf("恢复必须查询列表和详情，实际次数：%d", calls)
		}
		if matches && (err != nil || cert.ID != "123") {
			t.Fatalf("未复用同一证书：%v", err)
		}
		if !matches && (err == nil || !strings.Contains(err.Error(), "指纹不同")) {
			t.Fatalf("不同证书未被阻止：%v", err)
		}
	}
}
