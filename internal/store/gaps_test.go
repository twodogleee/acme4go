// 本文件验证告警租约预算、业务与通知原子提交及异常恢复的审查回归边界。
//
// @author Andersen
package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAlertLeaseRecoveryHonorsAttemptLimit 验证八次未确认发送后终结且人工重发仍能正常工作。
func TestAlertLeaseRecoveryHonorsAttemptLimit(t *testing.T) {
	database, _, _ := reliabilityStore(t)
	ctx := context.Background()
	if err := database.EnqueueAlert(ctx, AlertEvent{Event: "lease.test"}, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	var last AlertDelivery
	for attempt := 1; attempt <= maxAlertAttempts; attempt++ {
		var err error
		last, err = database.ClaimAlert(ctx, now)
		if err != nil || last.Attempts != attempt {
			t.Fatalf("租约恢复失败：%+v %v", last, err)
		}
		now = now.Add(2 * time.Minute)
	}
	if _, err := database.ClaimAlert(ctx, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("第九次仍被认领：%v", err)
	}
	values, total, err := database.ListAlertDeliveries(ctx, 1, 50, "FAILED")
	if err != nil || total != 1 || values[0].Attempts != 8 {
		t.Fatalf("耗尽预算的终态未提交：%+v %d %v", values, total, err)
	}
	if err := database.FinishAlert(ctx, last, "", now); err != nil {
		t.Fatal(err)
	}
	if err := database.RetryAlert(ctx, last.ID); err != nil {
		t.Fatal(err)
	}
	current, err := database.ClaimAlert(ctx, now.Add(time.Minute))
	if err != nil || current.Attempts != 1 {
		t.Fatalf("人工重发未恢复预算：%+v %v", current, err)
	}
	if err := database.FinishAlert(ctx, current, "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, total, err = database.ListAlertDeliveries(ctx, 1, 50, "SENT")
	if err != nil || total != 1 {
		t.Fatalf("人工重发未送达：%d %v", total, err)
	}
}

// TestManualRetryDoesNotReuseOldLease 验证人工重发重置次数后，旧工作者仍不能完成新租约。
func TestManualRetryDoesNotReuseOldLease(t *testing.T) {
	database, _, _ := reliabilityStore(t)
	ctx := context.Background()
	if err := database.EnqueueAlert(ctx, AlertEvent{Event: "lease.reset"}, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	first, err := database.ClaimAlert(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := database.ClaimAlert(ctx, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.FinishAlert(ctx, second, "失败", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := database.RetryAlert(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	current, err := database.ClaimAlert(ctx, now.Add(3*time.Minute))
	if err != nil || current.Attempts != first.Attempts {
		t.Fatalf("未进入相同次数的新租约：%+v %v", current, err)
	}
	if err := database.FinishAlert(ctx, first, "", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, total, err := database.ListAlertDeliveries(ctx, 1, 50, "SENDING")
	if err != nil || total != 1 {
		t.Fatalf("旧工作者覆盖了新租约：%d %v", total, err)
	}
}

// rejectAlertWrites 通过临时数据库触发器模拟通知入队失败，用于验证业务写入一起回滚。
func rejectAlertWrites(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER reject_alert BEFORE INSERT ON alert_deliveries BEGIN SELECT RAISE(ABORT,'synthetic outbox failure'); END`); err != nil {
		t.Fatal(err)
	}
}

// TestOrderFailureAndRecoveryRollBackWithAlert 验证普通失败和启动恢复都不留下缺少通知的终态。
func TestOrderFailureAndRecoveryRollBackWithAlert(t *testing.T) {
	database, series, credentialID := reliabilityStore(t)
	ctx := context.Background()
	order, err := database.CreateOrder(ctx, Order{SeriesID: series.ID, Domains: series.Domains, Email: series.Email, CADirectory: series.CADirectory, CredentialID: credentialID})
	if err != nil {
		t.Fatal(err)
	}
	rejectAlertWrites(t, database)
	if err := database.FailOrder(ctx, order.ID, "失败", AlertEvent{Event: "certificate.order.failed"}); err == nil {
		t.Fatal("通知失败应中止事务")
	}
	if err := database.FailInterruptedOrders(ctx, true); err == nil {
		t.Fatal("恢复通知失败应中止事务")
	}
	updated, err := database.FindOrder(ctx, order.ID)
	if err != nil || updated.Status != "QUEUED" {
		t.Fatalf("失败事务遗留终态：%+v %v", updated, err)
	}
	events, err := database.ListOrderEvents(ctx, order.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("时间线未回滚：%+v %v", events, err)
	}
	if _, err := database.db.Exec(`DROP TRIGGER reject_alert`); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := database.FailInterruptedOrders(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	updated, err = database.FindOrder(ctx, order.ID)
	if err != nil || updated.Status != "FAILED" {
		t.Fatalf("恢复没有终结订单：%+v %v", updated, err)
	}
	_, total, err := database.ListAlertDeliveries(ctx, 1, 50, "PENDING")
	if err != nil || total != 1 {
		t.Fatalf("重启告警缺失或重复：%d %v", total, err)
	}
}

// TestDeploymentFailuresCommitWithAlerts 验证远程和本机失败、历史与通知共同回滚，连续重复失败只入队一次。
func TestDeploymentFailuresCommitWithAlerts(t *testing.T) {
	database, series, credentialID := reliabilityStore(t)
	ctx := context.Background()
	if err := database.CreateEnrollmentToken(ctx, "atomic-agent", "atomic", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 在测试数据库内登记合成节点。
	agent, err := database.ConsumeEnrollmentToken(ctx, "atomic-agent", time.Now(), func(name string) (Agent, error) {
		return Agent{ID: "atomic-agent", Name: name, CertificateSerial: "atomic", CertificateExpiresAt: time.Now().Add(time.Hour)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := database.InsertCertificate(ctx, certificateFixture(credentialID, 90))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE certificate_series SET current_certificate_id=? WHERE id=?`, certificate.ID, series.ID); err != nil {
		t.Fatal(err)
	}
	remote, err := database.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorAgent, AgentID: &agent.ID, CertificateSeriesID: &series.ID, Name: "remote", TargetAlias: "remote", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	local, err := database.CreateTarget(ctx, DeploymentTarget{ExecutorType: ExecutorLocal, CertificateSeriesID: &series.ID, Name: "local", TargetAlias: "local", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	alert := AlertEvent{Event: "certificate.deployment.failed"}
	rejectAlertWrites(t, database)
	if err := database.RecordDeployment(ctx, agent.ID, remote.ID, remote.Revision, certificate.ID, certificate.Fingerprint, "FAILED", "失败", &alert); err == nil {
		t.Fatal("远程失败没有回滚")
	}
	if err := database.RecordLocalDeployment(ctx, local.ID, local.Revision, certificate.ID, certificate.Fingerprint, "FAILED", "失败", alert); err == nil {
		t.Fatal("本机失败没有回滚")
	}
	for _, id := range []int64{remote.ID, local.ID} {
		target, err := database.FindTarget(ctx, id)
		if err != nil || target.Status != "PENDING" {
			t.Fatalf("失败事务遗留状态：%+v %v", target, err)
		}
	}
	records, err := database.ListDeploymentRecords(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("失败事务遗留历史：%+v %v", records, err)
	}
	if _, err := database.db.Exec(`DROP TRIGGER reject_alert`); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := database.RecordDeployment(ctx, agent.ID, remote.ID, remote.Revision, certificate.ID, certificate.Fingerprint, "FAILED", "失败", &alert); err != nil {
			t.Fatal(err)
		}
		if err := database.RecordLocalDeployment(ctx, local.ID, local.Revision, certificate.ID, certificate.Fingerprint, "FAILED", "失败", alert); err != nil {
			t.Fatal(err)
		}
	}
	_, total, err := database.ListAlertDeliveries(ctx, 1, 50, "PENDING")
	if err != nil || total != 2 {
		t.Fatalf("失败通知去重错误：%d %v", total, err)
	}
}
