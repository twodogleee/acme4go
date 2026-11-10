// 本文件验证旧配置回执、短续期窗口和关闭阶段告警的审查回归边界。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/store"
)

// gapCertificate 创建合成的当前证书，不访问外部 CA 或 DNS。
func gapCertificate(t *testing.T, services *Services, credentialID int64, validFor time.Duration) (store.Order, store.Certificate) {
	t.Helper()
	ctx := context.Background()
	order, err := services.CreateOrder(ctx, nil, CreateOrderInput{Domains: []string{"api.example.com"}, Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CredentialID: credentialID, AgreeTerms: true})
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := services.Store.InsertCertificate(ctx, store.Certificate{SeriesID: order.SeriesID, CommonName: "api.example.com", SANs: []string{"api.example.com"}, Fingerprint: "gap-current", CredentialID: credentialID, CADirectory: store.StagingDirectory, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(validFor)})
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Store.CompleteOrder(ctx, order.ID, certificate.ID); err != nil {
		t.Fatal(err)
	}
	return order, certificate
}

// TestDeploymentReceiptRequiresCurrentRevision 验证旧修订、停用系列、停用目标与撤销身份不能覆盖状态。
func TestDeploymentReceiptRequiresCurrentRevision(t *testing.T) {
	services, credentialID := testReliabilityServices(t)
	ctx := context.Background()
	order, certificate := gapCertificate(t, services, credentialID, 30*24*time.Hour)
	if err := services.Store.CreateEnrollmentToken(ctx, "gap-token", "gap-agent", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 注册合成节点，不生成真实网络身份。
	agent, err := services.Store.ConsumeEnrollmentToken(ctx, "gap-token", time.Now(), func(name string) (store.Agent, error) {
		return store.Agent{ID: "gap-agent", Name: name, CertificateSerial: "gap-serial", CertificateExpiresAt: time.Now().Add(time.Hour)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := services.Store.CreateTarget(ctx, store.DeploymentTarget{ExecutorType: store.ExecutorAgent, AgentID: &agent.ID, Name: "gap", CertificateSeriesID: &order.SeriesID, CommonName: "api.example.com", TargetAlias: "old-directory", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	input := DeploymentResultInput{TargetID: target.ID, Revision: target.Revision, CertificateID: certificate.ID, Fingerprint: certificate.Fingerprint, Status: "SUCCESS", Message: "已部署"}
	target.TargetAlias = "new-directory"
	if err := services.Store.UpdateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	// 所有过期或停用授权必须明确返回冲突，不能当作发送成功。
	assertConflict := func() {
		t.Helper()
		var failure *Error
		if err := services.RecordAgentDeployment(ctx, agent, input); !errors.As(err, &failure) || failure.Status != 409 {
			t.Fatalf("无效回执应返回 409：%v", err)
		}
	}
	assertConflict()
	target, err = services.Store.FindTarget(ctx, target.ID)
	if err != nil || target.Status != "PENDING" || target.TargetAlias != "new-directory" {
		t.Fatalf("旧回执覆盖了新配置：%+v %v", target, err)
	}
	input.Revision = 0
	var failure *Error
	if err := services.RecordAgentDeployment(ctx, agent, input); !errors.As(err, &failure) || failure.Status != 400 {
		t.Fatalf("缺少修订应要求升级：%v", err)
	}
	input.Revision = target.Revision
	if err := services.RecordAgentDeployment(ctx, agent, input); err != nil {
		t.Fatal(err)
	}
	series, err := services.Store.FindCertificateSeries(ctx, order.SeriesID, 0)
	if err != nil {
		t.Fatal(err)
	}
	series.Enabled = false
	if err := services.Store.UpdateCertificateSeries(ctx, series); err != nil {
		t.Fatal(err)
	}
	assertConflict()
	series.Enabled = true
	if err := services.Store.UpdateCertificateSeries(ctx, series); err != nil {
		t.Fatal(err)
	}
	target.Enabled = false
	if err := services.Store.UpdateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	target, err = services.Store.FindTarget(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.Revision = target.Revision
	assertConflict()
	target.Enabled = true
	if err := services.Store.UpdateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	target, err = services.Store.FindTarget(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.Revision = target.Revision
	if err := services.Store.RevokeAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	assertConflict()
}

// TestShortRenewalWindowStillSendsCriticalAlert 验证风险提醒不被一天的自动续期窗口屏蔽。
func TestShortRenewalWindowStillSendsCriticalAlert(t *testing.T) {
	services, credentialID := testReliabilityServices(t)
	services.Config.RenewBefore = 24 * time.Hour
	_, certificate := gapCertificate(t, services, credentialID, 3*24*time.Hour)
	if err := services.scanNotifications(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	values, total, err := services.Store.ListAlertDeliveries(context.Background(), 1, 50, "ALL")
	if err != nil || total != 1 || values[0].Event != "certificate.expiry_critical" || values[0].ResourceID != formatID(certificate.ID) {
		t.Fatalf("临期告警缺失：%+v %d %v", values, total, err)
	}
}

// TestClosingOrderFailurePersistsAlert 验证关闭标记不阻止已启动业务的失败状态和告警提交。
func TestClosingOrderFailurePersistsAlert(t *testing.T) {
	services, credentialID := testReliabilityServices(t)
	ctx := context.Background()
	services.Config.AlertWebhookURL = "https://gap.invalid"
	order, err := services.CreateOrder(ctx, nil, CreateOrderInput{Domains: []string{"api.example.com"}, Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CredentialID: credentialID, AgreeTerms: true})
	if err != nil {
		t.Fatal(err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	services.processOrder(canceledCtx, order.ID)
	services.failOrder(order.ID, "服务停止导致任务中断，请重试")
	updated, err := services.Store.FindOrder(ctx, order.ID)
	if err != nil || updated.Status != "FAILED" {
		t.Fatalf("终态未保存：%+v %v", updated, err)
	}
	values, total, err := services.Store.ListAlertDeliveries(ctx, 1, 50, "ALL")
	if err != nil || total != 1 || values[0].Event != "certificate.order.failed" {
		t.Fatalf("关闭通知未持久化或重复：%+v %d %v", values, total, err)
	}
}

// TestCloseWaitsForQueuedOrderAlert 验证真实关闭流程会等待排队任务终态和通知落盘，重开数据库可恢复投递。
func TestCloseWaitsForQueuedOrderAlert(t *testing.T) {
	services, credentialID := testReliabilityServices(t)
	ctx := context.Background()
	services.Config.AlertWebhookURL = "https://gap.invalid"
	order, err := services.CreateOrder(ctx, nil, CreateOrderInput{Domains: []string{"api.example.com"}, Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CredentialID: credentialID, AgreeTerms: true})
	if err != nil {
		t.Fatal(err)
	}
	services.serviceCtx, services.serviceStop = context.WithCancel(ctx)
	services.orderSlots = make(chan struct{})
	services.closing = false
	services.RunOrder(order.ID)
	if err := services.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(services.Config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	updated, err := database.FindOrder(ctx, order.ID)
	if err != nil || updated.Status != "FAILED" {
		t.Fatalf("关闭后订单未终结：%+v %v", updated, err)
	}
	_, total, err := database.ListAlertDeliveries(ctx, 1, 50, "PENDING")
	if err != nil || total != 1 {
		t.Fatalf("关闭后重开数据库找不到待投递通知：%d %v", total, err)
	}
}
