// 本文件验证单次远程同步的持久授权、并发受理和过期回执隔离。
//
// @author Andersen
package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// manualSyncFixture 构造暂停自动部署且拥有有效证书的远程目标。
func manualSyncFixture(t *testing.T) (*Store, Agent, DeploymentTarget, Certificate) {
	t.Helper()
	s, series, credentialID := reliabilityStore(t)
	ctx := context.Background()
	if err := s.CreateEnrollmentToken(ctx, "manual-token", "manual", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 合成节点身份仅用于数据库授权验证。
	agent, err := s.ConsumeEnrollmentToken(ctx, "manual-token", time.Now(), func(name string) (Agent, error) {
		return Agent{ID: "manual-agent", Name: name, CertificateSerial: "manual", CertificateExpiresAt: time.Now().Add(time.Hour)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cert, err := s.InsertCertificate(ctx, certificateFixture(credentialID, 91))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE certificate_series SET current_certificate_id=? WHERE id=?`, cert.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	target, err := s.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorAgent, AgentID: &agent.ID, CertificateSeriesID: &series.ID, Name: "manual", TargetAlias: "manual", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	return s, agent, target, cert
}

// TestManualSyncLifecycle 验证暂停目标的单次授权、开始确认、终态撤权和重复回执。
func TestManualSyncLifecycle(t *testing.T) {
	s, agent, target, cert := manualSyncFixture(t)
	ctx := context.Background()
	wg := sync.WaitGroup{}
	for range 8 {
		wg.Add(1)
		// 并发点击必须复用同一修订号。
		go func() {
			defer wg.Done()
			if err := s.RequestAgentSync(ctx, target.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	queued, err := s.FindTarget(ctx, target.ID)
	if err != nil || !queued.ManualSync || queued.Enabled || queued.Revision != target.Revision+1 {
		t.Fatalf("入队异常 %+v %v", queued, err)
	}
	desired, err := s.DesiredTargets(ctx, agent.ID, "https://test.invalid", 0)
	if err != nil || len(desired) != 1 || !desired[0].ManualSync {
		t.Fatalf("暂停目标未下发 %v %v", desired, err)
	}
	if err := s.RecordDeployment(ctx, agent.ID, target.ID, target.Revision, cert.ID, cert.Fingerprint, "SUCCESS", "旧结果", nil); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatalf("接受旧回执 %v", err)
	}
	if err := s.RecordDeployment(ctx, "other-agent", target.ID, queued.Revision, cert.ID, cert.Fingerprint, "RUNNING", "开始", nil); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatalf("接受其他 Agent %v", err)
	}
	if err := s.RecordDeployment(ctx, agent.ID, target.ID, queued.Revision, cert.ID, cert.Fingerprint, "RUNNING", "开始", nil); err != nil {
		t.Fatal(err)
	}
	running, _ := s.FindTarget(ctx, target.ID)
	if running.Status != "RUNNING" || running.LastDeployedAt != nil {
		t.Fatal("开始回执错误地更新了完成时间")
	}
	records, _ := s.ListDeploymentRecords(ctx)
	if len(records) != 0 {
		t.Fatal("开始回执写成了部署结果")
	}
	for range 2 {
		if err := s.RecordDeployment(ctx, agent.ID, target.ID, queued.Revision, cert.ID, cert.Fingerprint, "SUCCESS", "完成", nil); err != nil {
			t.Fatal(err)
		}
	}
	desired, err = s.DesiredTargets(ctx, agent.ID, "https://test.invalid", 0)
	if err != nil || len(desired) != 0 {
		t.Fatal("单次任务完成后仍暴露证书下载授权")
	}
	records, _ = s.ListDeploymentRecords(ctx)
	if len(records) != 1 {
		t.Fatalf("重复结果未去重 %d", len(records))
	}
	if err := s.RequestAgentSync(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	next, _ := s.FindTarget(ctx, target.ID)
	if next.Revision != queued.Revision+1 {
		t.Fatal("下一次手动同步没有独立修订")
	}
	if err := s.RecordDeployment(ctx, agent.ID, target.ID, next.Revision, cert.ID, cert.Fingerprint, "FAILED", "失败", nil); err != nil {
		t.Fatal(err)
	}
	failed, _ := s.FindTarget(ctx, target.ID)
	if failed.ManualSync || failed.Status != "FAILED" || failed.Enabled {
		t.Fatal("失败没有终结单次授权")
	}
}

// TestManualSyncCancelledByConfiguration 验证修改配置或停用系列会取消旧授权和拒绝迟到回执。
func TestManualSyncCancelledByConfiguration(t *testing.T) {
	s, agent, target, cert := manualSyncFixture(t)
	ctx := context.Background()
	if err := s.RequestAgentSync(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	queued, _ := s.FindTarget(ctx, target.ID)
	if err := s.UpdateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeployment(ctx, agent.ID, target.ID, queued.Revision, cert.ID, cert.Fingerprint, "SUCCESS", "迟到", nil); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatal("旧配置结果仍有效")
	}
	if err := s.RequestAgentSync(ctx, target.ID); err != nil {
		t.Fatal(err)
	}
	series, err := s.FindCertificateSeries(ctx, *target.CertificateSeriesID, 0)
	if err != nil {
		t.Fatal(err)
	}
	series.Enabled = false
	if err := s.UpdateCertificateSeries(ctx, series); err != nil {
		t.Fatal(err)
	}
	stopped, _ := s.FindTarget(ctx, target.ID)
	if stopped.ManualSync || stopped.Status != "FAILED" {
		t.Fatal("停用系列未终止单次同步")
	}
	if err := s.RequestAgentSync(ctx, target.ID); !errors.Is(err, ErrDeploymentChanged) {
		t.Fatal("停用系列仍可入队")
	}
}
