// 本文件验证云证书校验失败后的原证书复用，以及真实指纹不一致时的部署阻断。
//
// @author Andersen
package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/cloud"
	"github.com/twodogleee/acme4go/internal/cloud/aliyun"
)

// fingerprintRPC 将真实阿里云适配器接入内存响应，避免测试访问云账号。
type fingerprintRPC func(string, map[string]string) (map[string]any, error)

// Call 将 API 名称和参数转交场景回调。
func (f fingerprintRPC) Call(_ context.Context, _ cloud.Credentials, _, _, action string, values map[string]string) (map[string]any, error) {
	return f(action, values)
}

// TestCloudFingerprintRetryReusesUpload 验证上传成功后校验失败不会丢失映射，修复详情响应后可直接重试部署。
func TestCloudFingerprintRetryReusesUpload(t *testing.T) {
	s, account, local, _, product := cloudFixture(t)
	uploads := 0
	fingerprintReady := false
	// 首轮模拟缺少 SHA-256 元数据，次轮仅 CertSha2 返回正确指纹。
	s.Cloud.Catalog = &aliyun.Catalog{RPC: fingerprintRPC(func(action string, values map[string]string) (map[string]any, error) {
		switch action {
		case "ListUserCertificateOrder":
			return map[string]any{"TotalCount": 0, "CertificateOrderList": []any{}}, nil
		case "UploadUserCertificate":
			uploads++
			return map[string]any{"CertId": "123"}, nil
		case "GetUserCertificateDetail":
			if values["CertFilter"] != "true" || values["CertId"] != "123" {
				t.Fatal("重试必须复用原 ID 且不下载私钥")
			}
			body := map[string]any{"Id": "123", "Sans": "api.example.com", "EndDate": "2099-01-01", "Fingerprint": strings.Repeat("A", 40)}
			if fingerprintReady {
				body["CertSha2"] = " \n" + local.Fingerprint + "\t"
				body["Sha2"] = strings.Repeat("B", 64)
			}
			return body, nil
		default:
			t.Fatalf("意外 API：%s", action)
			return nil, nil
		}
	})}
	plan, err := s.SaveCloudPlan(context.Background(), 1, 0, CloudPlanInput{Name: "指纹恢复", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", SeriesID: &local.SeriesID, Config: json.RawMessage(`{"domain":"api.example.com"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	failed := runCloudTestJob(t, s, plan.ID)
	if failed.Status != "FAILED" || failed.Stage != "CHECK_CERTIFICATE" || failed.RemoteCertificateID != "123" || !strings.Contains(failed.Message, "未返回有效的 SHA-256 指纹") || uploads != 1 || product.Deploys != 0 {
		t.Fatalf("校验失败的阶段或副作用错误：%+v，上传 %d 次，部署 %d 次", failed, uploads, product.Deploys)
	}
	fingerprintReady = true
	retried := runCloudTestJob(t, s, plan.ID)
	if retried.ID == failed.ID || retried.Status != "SUCCESS" || retried.RemoteCertificateID != "123" || uploads != 1 || product.Deploys != 1 {
		t.Fatalf("重试未复用上传结果：%+v，上传 %d 次，部署 %d 次", retried, uploads, product.Deploys)
	}
}

// TestCloudFingerprintMismatchBlocksDeployment 验证详情指纹不同，即使旧 Sha2 匹配也不能部署。
func TestCloudFingerprintMismatchBlocksDeployment(t *testing.T) {
	s, account, local, _, product := cloudFixture(t)
	ctx := context.Background()
	if err := s.Store.SaveCloudUpload(ctx, account.ID, local.Fingerprint, "123"); err != nil {
		t.Fatal(err)
	}
	// 只允许读取已上传证书；不一致时不能重新上传或调用产品更新。
	s.Cloud.Catalog = &aliyun.Catalog{RPC: fingerprintRPC(func(action string, values map[string]string) (map[string]any, error) {
		if action != "GetUserCertificateDetail" || values["CertFilter"] != "true" {
			t.Fatalf("意外调用：%s", action)
		}
		return map[string]any{"Id": "123", "CertSha2": strings.Repeat("F", 64), "Sha2": local.Fingerprint, "Sans": "api.example.com", "EndDate": "2099-01-01"}, nil
	})}
	plan, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "指纹不一致", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", SeriesID: &local.SeriesID, Config: json.RawMessage(`{"domain":"api.example.com"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job := runCloudTestJob(t, s, plan.ID)
	if job.Status != "FAILED" || job.Stage != "CHECK_CERTIFICATE" || !strings.Contains(job.Message, "指纹与本地版本不一致") || !strings.Contains(job.Message, local.Fingerprint) || product.Deploys != 0 {
		t.Fatalf("未阻止不同证书部署或缺少诊断信息：%+v", job)
	}
}
