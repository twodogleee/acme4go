// 本文件验证停用系列、证书链损坏及登录凭据变化的业务边界。
//
// @author Andersen
package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/store"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// followupDeploymentFixture 创建带真实签名和临时物化目录的本机目标，不访问外部服务。
func followupDeploymentFixture(t *testing.T) (*Services, store.DeploymentTarget, deploy.Bundle) {
	t.Helper()
	s, cred := testReliabilityServices(t)
	s.Config.LocalDeploymentEnabled = true
	s.Config.DeploymentDir = t.TempDir()
	s.Storage = deploy.NewStorage(t.TempDir(), s.Vault)
	ctx := context.Background()
	order, err := s.CreateOrder(ctx, nil, CreateOrderInput{Domains: []string{"api.example.com"}, Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CredentialID: cred, AgreeTerms: true})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "api.example.com"}, DNSNames: []string{"api.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(der)
	b := deploy.Bundle{Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), Fingerprint: hex.EncodeToString(digest[:]), CommonName: "api.example.com", SANs: []string{"api.example.com"}, NotAfter: template.NotAfter}
	b.FullChain = b.Certificate
	cp, chp, kp, err := s.Storage.Save(b)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := s.Store.InsertCertificate(ctx, store.Certificate{SeriesID: order.SeriesID, CommonName: b.CommonName, SANs: b.SANs, Fingerprint: b.Fingerprint, CredentialID: cred, CADirectory: store.StagingDirectory, NotBefore: template.NotBefore, NotAfter: template.NotAfter, CertPath: cp, ChainPath: chp, KeyPath: kp})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.CompleteOrder(ctx, order.ID, cert.ID); err != nil {
		t.Fatal(err)
	}
	target, err := s.Store.CreateTarget(ctx, store.DeploymentTarget{ExecutorType: store.ExecutorLocal, Name: "fixture", CertificateSeriesID: &order.SeriesID, CommonName: b.CommonName, TargetAlias: "old-alias", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, target, b
}

// TestDisabledSeriesBlocksLocalDeployment 验证停用系列同时阻止自动对账和显式同步，恢复启用后可以正常部署。
func TestDisabledSeriesBlocksLocalDeployment(t *testing.T) {
	s, target, bundle := followupDeploymentFixture(t)
	ctx := context.Background()
	series, err := s.Store.FindCertificateSeries(ctx, *target.CertificateSeriesID, 0)
	if err != nil {
		t.Fatal(err)
	}
	series.Enabled = false
	if err = s.Store.UpdateCertificateSeries(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileLocalTargets(ctx); err != nil {
		t.Fatal(err)
	}
	var failure *Error
	if err = s.DeployLocalTarget(ctx, target.ID); !errors.As(err, &failure) || failure.Status != 409 {
		t.Fatalf("停用系列仍允许手动同步：%v", err)
	}
	if _, err = os.Stat(filepath.Join(s.Config.DeploymentDir, target.TargetAlias, "current")); !os.IsNotExist(err) {
		t.Fatalf("停用系列产生物化文件：%v", err)
	}
	series.Enabled = true
	if err = s.Store.UpdateCertificateSeries(ctx, series); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileLocalTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if err = deploy.ValidateMaterialized(s.Config.DeploymentDir, target.TargetAlias, bundle.SANs, bundle.Fingerprint); err != nil {
		t.Fatal(err)
	}
}

// TestPausedTargetAllowsOnlyExplicitLocalSync 验证暂停目标不被自动部署，同时保留手动同步契约。
func TestPausedTargetAllowsOnlyExplicitLocalSync(t *testing.T) {
	s, target, _ := followupDeploymentFixture(t)
	ctx := context.Background()
	target.Enabled = false
	if err := s.Store.UpdateTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileLocalTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Config.DeploymentDir, target.TargetAlias, "current")); !os.IsNotExist(err) {
		t.Fatalf("暂停目标仍被自动部署：%v", err)
	}
	if err := s.DeployLocalTarget(ctx, target.ID); err != nil {
		t.Fatalf("显式同步被错误阻止：%v", err)
	}
}

// TestLocalDeploymentRejectsCorruptFullChain 验证损坏链不会切换文件，失败状态与通知正常保存。
func TestLocalDeploymentRejectsCorruptFullChain(t *testing.T) {
	s, target, bundle := followupDeploymentFixture(t)
	ctx := context.Background()
	s.Config.AlertWebhookURL = "https://receiver.example.test"
	bundle.FullChain = []byte("not a certificate chain")
	if _, _, _, err := s.Storage.Save(bundle); err != nil {
		t.Fatal(err)
	}
	if err := s.DeployLocalTarget(ctx, target.ID); err == nil {
		t.Fatal("损坏链被成功部署")
	}
	current, err := s.Store.FindTarget(ctx, target.ID)
	if err != nil || current.Status != "FAILED" {
		t.Fatalf("未保存部署失败：%s %v", current.Status, err)
	}
	if _, err = os.Stat(filepath.Join(s.Config.DeploymentDir, target.TargetAlias, "current")); !os.IsNotExist(err) {
		t.Fatalf("损坏链仍切换了文件：%v", err)
	}
	_, total, err := s.Store.ListAlertDeliveries(ctx, 1, 50, "PENDING")
	if err != nil || total != 1 {
		t.Fatalf("部署失败通知未入队：%d %v", total, err)
	}
}
