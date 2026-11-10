// 本文件验证 CDN 已生效时的幂等恢复，以及确认超时的持久诊断和取消行为。
//
// @author Andersen
package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/cloud"
	"github.com/twodogleee/acme4go/internal/cloud/aliyun"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestCDNCloudJobRecognizesCertificateContent 复现不同 CDN ID 且缺少 Status 时的幂等恢复与提交后核验。
func TestCDNCloudJobRecognizesCertificateContent(t *testing.T) {
	for _, alreadyBound := range []bool{true, false} {
		// 分别验证已绑定和新提交两种证书恢复路径。
		t.Run(map[bool]string{true: "已生效直接恢复", false: "提交后立即核验"}[alreadyBound], func(t *testing.T) {
			s, account, local, catalog, _ := cloudFixture(t)
			ctx := context.Background()
			_, bundle, err := s.CertificateBundle(ctx, local.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Store.SaveCloudUpload(ctx, account.ID, local.Fingerprint, "123"); err != nil {
				t.Fatal(err)
			}
			bound, deploys, queries := alreadyBound, 0, 0
			// 模拟部署与查询，按绑定状态返回对应的 CDN 证书。
			s.Cloud.Deployers["CDN"] = &aliyun.CDN{RPC: fingerprintRPC(func(action string, values map[string]string) (map[string]any, error) {
				switch action {
				case "SetCdnDomainSSLCertificate":
					if values["CertId"] != "123" || values["CertType"] != "cas" {
						t.Fatal("部署必须引用原云证书")
					}
					deploys++
					bound = true
					return map[string]any{}, nil
				case "DescribeDomainCertificateInfo":
					queries++
					item := map[string]any{"DomainName": "api.example.com", "CertId": "cdn-copy-999", "ServerCertificateStatus": "on", "CertType": "upload"}
					if bound {
						item["ServerCertificate"] = string(bundle.Certificate)
					}
					return map[string]any{"CertInfos": map[string]any{"CertInfo": []any{item}}}, nil
				default:
					t.Fatalf("不应调用额外接口：%s", action)
					return nil, nil
				}
			})}
			plan, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "CDN 恢复", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", SeriesID: &local.SeriesID, Config: json.RawMessage(`{"domain":"api.example.com"}`), Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			job := runCloudTestJob(t, s, plan.ID)
			wantDeploys := 1
			if alreadyBound {
				wantDeploys = 0
			}
			if job.Status != "SUCCESS" || job.Stage != "COMPLETE" || job.RemoteCertificateID != "123" || deploys != wantDeploys || queries != wantDeploys+1 || catalog.Uploads != 0 {
				t.Fatalf("证书身份恢复错误：%+v；部署 %d 次，查询 %d 次，上传 %d 次", job, deploys, queries, catalog.Uploads)
			}
		})
	}
}

// cloudVerificationFixture 创建真实 SQLite 任务，供轮询进度和历史记录测试使用。
func cloudVerificationFixture(t *testing.T) (*Services, store.CloudJob) {
	t.Helper()
	s, account, _, _, _ := cloudFixture(t)
	ctx := context.Background()
	plan, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "核验进度", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", RemoteCertificateID: "123", Config: json.RawMessage(`{"domain":"api.example.com"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.QueueCloudPlan(ctx, 1, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	job.Status, job.Stage = "RUNNING", "VERIFY"
	if err := s.Store.SetCloudJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	return s, job
}

// TestCloudVerificationTimeoutKeepsLatestReason 验证请求占满剩余时限时仍保留最后有效状态。
func TestCloudVerificationTimeoutKeepsLatestReason(t *testing.T) {
	s, job := cloudVerificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	latest := "证书 SHA-256 指纹一致；HTTPS：已开启（on）；证书状态：检测中（checking）"
	// 先返回两次状态，再等待超时以检查最近一次状态的保留。
	err := s.waitCloudVerification(ctx, &job, func(checkCtx context.Context) (cloud.Verification, error) {
		calls++
		switch calls {
		case 1:
			return cloud.Verification{Message: "CDN 当前证书 SHA-256 指纹与目标证书不一致"}, nil
		case 2:
			return cloud.Verification{Message: latest}, nil
		default:
			<-checkCtx.Done()
			return cloud.Verification{}, errors.New("模拟 SDK 将超时包装为查询失败")
		}
	}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "本地确认超时，不代表云端部署失败") || !strings.Contains(err.Error(), latest) || strings.Contains(err.Error(), "模拟 SDK") {
		t.Fatalf("超时未保留最后状态：%v", err)
	}
	persisted, findErr := s.Store.FindCloudJob(context.Background(), job.ID)
	if findErr != nil || persisted.Stage != "VERIFY" || !strings.Contains(persisted.Message, latest) {
		t.Fatalf("运行进度未保存：%+v %v", persisted, findErr)
	}
}

// TestCloudVerificationPollingOutcomes 验证立即成功、后续成功、API 错误和主动取消均可正确结束。
func TestCloudVerificationPollingOutcomes(t *testing.T) {
	for _, scenario := range []string{"立即成功", "轮询成功", "查询错误", "主动取消", "等待超时"} {
		// 在独立任务中验证每种轮询终态。
		t.Run(scenario, func(t *testing.T) {
			s, job := cloudVerificationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			calls := 0
			apiErr := errors.New("模拟权限不足")
			interval := time.Millisecond
			if scenario == "等待超时" {
				interval = time.Hour
			}
			// 按场景模拟成功、失败、取消或持续等待。
			err := s.waitCloudVerification(ctx, &job, func(context.Context) (cloud.Verification, error) {
				calls++
				if scenario == "立即成功" || (scenario == "轮询成功" && calls == 2) {
					return cloud.Verification{Verified: true}, nil
				}
				if scenario == "查询错误" {
					return cloud.Verification{}, apiErr
				}
				if scenario == "主动取消" {
					cancel()
					return cloud.Verification{}, ctx.Err()
				}
				return cloud.Verification{Message: "HTTPS 尚未开启"}, nil
			}, interval)
			switch scenario {
			case "立即成功":
				if err != nil || calls != 1 {
					t.Fatalf("没有立即成功：%d %v", calls, err)
				}
			case "轮询成功":
				if err != nil || calls != 2 {
					t.Fatalf("轮询成功结果错误：%d %v", calls, err)
				}
			case "查询错误":
				if !errors.Is(err, apiErr) || !strings.Contains(err.Error(), "查询生效结果失败") {
					t.Fatalf("丢失 API 失败原因：%v", err)
				}
			case "主动取消":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("主动取消不应标记为超时：%v", err)
				}
			case "等待超时":
				if err == nil || !strings.Contains(err.Error(), "本地确认超时") || !strings.Contains(err.Error(), "HTTPS 尚未开启") || calls != 1 {
					t.Fatalf("等待超时结果错误：%d %v", calls, err)
				}
			}
		})
	}
}
