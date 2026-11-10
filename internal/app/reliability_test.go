// 本文件验证申请校验副作用、Cloudflare 凭据隔离与运维通知持久化。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/store"
)

// testReliabilityServices 初始化纯本地领域服务，阻止测试访问真实 ACME 或 DNS。
func testReliabilityServices(t *testing.T) (*Services, int64) {
	t.Helper()
	directory := t.TempDir()
	database, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	// 仅关闭临时数据库，测试不启动真实后台任务。
	t.Cleanup(func() { database.Close() })
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	services := &Services{Store: database, Vault: vault, closing: true, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Config: config.Config{DataDir: directory, RenewBefore: 30 * 24 * time.Hour}}
	value, err := services.prepareDNSInput(DNSCredentialInput{Name: "测试凭据", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "synthetic-id", AccessKeySecret: "synthetic-secret", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := database.CreateDNSCredential(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	return services, credential.ID
}

// TestRejectedOrderDoesNotPersistSeries 验证域名或停用凭据校验失败不会产生空系列。
func TestRejectedOrderDoesNotPersistSeries(t *testing.T) {
	services, credentialID := testReliabilityServices(t)
	ctx := context.Background()
	input := CreateOrderInput{Domains: []string{"api.example.net"}, Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CredentialID: credentialID, AgreeTerms: true}
	if _, err := services.CreateOrder(ctx, nil, input); err == nil {
		t.Fatal("域名不匹配必须失败")
	}
	series, err := services.Store.ListCertificateSeries(ctx, 0)
	if err != nil || len(series) != 0 {
		t.Fatalf("校验失败遗留系列：%d %v", len(series), err)
	}
	input.Domains = []string{"api.example.com"}
	if _, err := services.CreateOrder(ctx, nil, input); err != nil {
		t.Fatal(err)
	}
	_, err = services.CreateOrder(ctx, nil, input)
	var conflict *Error
	if !errors.As(err, &conflict) || conflict.Status != 409 {
		t.Fatalf("重复申请应返回明确的 409：%v", err)
	}
}

// TestCloudflareCredentialRetainsEncryptedToken 验证 Token 单独加密、编辑留空保留且服务商不能隐式切换。
func TestCloudflareCredentialRetainsEncryptedToken(t *testing.T) {
	services, _ := testReliabilityServices(t)
	input := DNSCredentialInput{Name: "Cloudflare", Provider: "CLOUDFLARE", Zone: "example.net", AccessKeySecret: "synthetic-token", Enabled: true}
	value, err := services.prepareDNSInput(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(value.AccessKeySecret, "synthetic-token") {
		t.Fatal("Token 必须加密")
	}
	if err := services.decryptDNS(&value); err != nil {
		t.Fatal(err)
	}
	input.AccessKeySecret = ""
	updated, err := services.prepareDNSInput(input, &value)
	if err != nil {
		t.Fatal(err)
	}
	if err := services.decryptDNS(&updated); err != nil || updated.AccessKeySecret != "synthetic-token" {
		t.Fatal("编辑留空丢失了原 Token")
	}
	input.Provider = "ALIYUN"
	if _, err := services.prepareDNSInput(input, &value); err == nil {
		t.Fatal("已有凭据不应更换服务商")
	}
	if maskedDNSIdentifier("CLOUDFLARE", "synthetic-token") != "API Token" {
		t.Fatal("Token 不应以片段形式展示")
	}
}

// TestOfflineNotificationIsDurableAndDeduplicated 验证离线通知同一故障去重且事件落库。
func TestOfflineNotificationIsDurableAndDeduplicated(t *testing.T) {
	services, _ := testReliabilityServices(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := services.Store.CreateEnrollmentToken(ctx, "test-hash", "offline-node", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 使用合成身份创建节点，不生成或传播真实私钥。
	_, err := services.Store.ConsumeEnrollmentToken(ctx, "test-hash", now, func(name string) (store.Agent, error) {
		return store.Agent{ID: "offline-agent", Name: name, CertificateSerial: "synthetic", CertificateExpiresAt: now.Add(time.Hour), Enabled: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		if err := services.scanNotifications(ctx, now.Add(3*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	values, total, err := services.Store.ListAlertDeliveries(ctx, 1, 50, "PENDING")
	if err != nil || total != 1 || values[0].Event != "agent.offline" {
		t.Fatalf("离线通知持久化或去重异常：%+v %d %v", values, total, err)
	}
}

// TestExpiryNotificationsEscalateOnlyForMonitoredVersions 验证在用证书风险升级、去重和历史版本排除。
func TestExpiryNotificationsEscalateOnlyForMonitoredVersions(t *testing.T) {
	services, credentialID := testReliabilityServices(t)
	ctx := context.Background()
	now := time.Now().UTC()
	order, err := services.CreateOrder(ctx, nil, CreateOrderInput{Domains: []string{"api.example.com"}, Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CredentialID: credentialID, AgreeTerms: true})
	if err != nil {
		t.Fatal(err)
	}
	certificate := store.Certificate{SeriesID: order.SeriesID, CommonName: "api.example.com", SANs: []string{"api.example.com"}, Fingerprint: "old-history-version", CredentialID: credentialID, CADirectory: store.StagingDirectory, NotBefore: now.Add(-60 * 24 * time.Hour), NotAfter: now.Add(-24 * time.Hour)}
	if _, err := services.Store.InsertCertificate(ctx, certificate); err != nil {
		t.Fatal(err)
	}
	certificate.Fingerprint = "current-monitored-version"
	certificate.NotAfter = now.Add(20 * 24 * time.Hour)
	certificate, err = services.Store.InsertCertificate(ctx, certificate)
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Store.CompleteOrder(ctx, order.ID, certificate.ID); err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{0, 14 * 24 * time.Hour, 21 * 24 * time.Hour} {
		for attempt := 0; attempt < 2; attempt++ {
			if err := services.scanNotifications(ctx, now.Add(elapsed)); err != nil {
				t.Fatal(err)
			}
		}
	}
	values, total, err := services.Store.ListAlertDeliveries(ctx, 1, 50, "PENDING")
	if err != nil || total != 3 {
		t.Fatalf("每种风险只应创建一次提醒：%+v %d %v", values, total, err)
	}
	expected := map[string]string{"certificate.expiring": "WARNING", "certificate.expiry_critical": "ERROR", "certificate.expired": "ERROR"}
	for _, value := range values {
		if value.ResourceID != formatID(certificate.ID) || expected[value.Event] != value.Severity {
			t.Fatalf("通知包含无关历史或风险级别错误：%+v", value)
		}
		delete(expected, value.Event)
	}
	if len(expected) != 0 {
		t.Fatalf("缺少风险升级事件：%v", expected)
	}
}
