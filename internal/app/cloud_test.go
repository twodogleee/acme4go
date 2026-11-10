// 本文件验证云上传与部署分离、续期跟随、密钥隔离和失败恢复。
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
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/cloud"
	"github.com/twodogleee/acme4go/internal/cloud/aliyun"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/store"
)

// fakeCloudCatalog 记录上传次数，返回确定的云端元数据。
type fakeCloudCatalog struct {
	Cert    cloud.Certificate  // Cert 是云端已有记录。
	Uploads int                // Uploads 是实际上传调用次数。
	Cancel  context.CancelFunc // Cancel 模拟云请求期间服务关闭。
}

// List 返回一个已有有效证书。
func (f *fakeCloudCatalog) List(context.Context, cloud.Credentials, int, string) (cloud.CertificatePage, error) {
	return cloud.CertificatePage{Items: []cloud.Certificate{f.Cert}, Total: 1}, nil
}

// Get 只接受模拟账号中的确定证书。
func (f *fakeCloudCatalog) Get(_ context.Context, _ cloud.Credentials, id string) (cloud.Certificate, error) {
	if f.Cancel != nil {
		f.Cancel()
		return cloud.Certificate{}, context.Canceled
	}
	if id != f.Cert.ID {
		return cloud.Certificate{}, errors.New("云证书不存在")
	}
	return f.Cert, nil
}

// TestCloudConcurrentQueueLocksPlanAndAccount 验证并发入队互斥及活动任务的配置保护。
func TestCloudConcurrentQueueLocksPlanAndAccount(t *testing.T) {
	s, account, cert, _, _ := cloudFixture(t)
	ctx := context.Background()
	input := CloudPlanInput{Name: "并发上传", CredentialID: account.ID, Kind: "UPLOAD", SeriesID: &cert.SeriesID, Enabled: true}
	plan, err := s.SaveCloudPlan(ctx, 1, 0, input)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 10)
	for range 10 {
		// 同时提交同一目标时数据库只能接受一个活动任务。
		go func() { _, err := s.Store.EnqueueCloudJob(ctx, plan, &cert.ID); results <- err }()
	}
	success := 0
	for range 10 {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("并发入队成功数 %d", success)
	}
	if _, err := s.SaveCloudPlan(ctx, 1, plan.ID, input); err == nil {
		t.Fatal("活动目标仍可修改")
	}
	if err := s.DeleteCloudPlan(ctx, 1, plan.ID); err == nil {
		t.Fatal("活动目标仍可删除")
	}
	if _, err := s.SaveCredential(ctx, 1, account.ID, CredentialInput{DNSCredentialInput: DNSCredentialInput{Name: "停用", Provider: "ALIYUN", Enabled: false}, CloudEnabled: true}); err == nil {
		t.Fatal("活动账号仍可修改")
	}
}

// TestCloudShutdownKeepsJobRecoverable 验证外部请求中断时不会把服务关闭误记为业务失败。
func TestCloudShutdownKeepsJobRecoverable(t *testing.T) {
	s, account, _, catalog, product := cloudFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plan, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "中断恢复", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", RemoteCertificateID: "123", Config: json.RawMessage(`{"domain":"api.example.com"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.QueueCloudPlan(ctx, 1, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Cancel = cancel
	if s.executeCloudJob(ctx, job) {
		t.Fatal("取消后工作者应停止本轮执行")
	}
	job, err = s.Store.FindCloudJob(context.Background(), job.ID)
	if err != nil || job.Status != "RUNNING" || product.Deploys != 0 {
		t.Fatalf("中断任务不可恢复 %+v %v", job, err)
	}
	if err := s.Store.RecoverCloudJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err = s.Store.NextCloudJob(context.Background())
	if err != nil || job.Status != "QUEUED" {
		t.Fatalf("恢复入队失败 %+v %v", job, err)
	}
}

// Upload 记录材料被发送，返回云端证书标识。
func (f *fakeCloudCatalog) Upload(_ context.Context, _ cloud.Credentials, m cloud.Material) (cloud.Certificate, error) {
	f.Uploads++
	f.Cert.Fingerprint = m.Fingerprint
	return f.Cert, nil
}

// fakeCloudDeployer 模拟产品成功、失败和已生效绑定。
type fakeCloudDeployer struct {
	Deploys int    // Deploys 是外部更新次数。
	Bound   string // Bound 是当前绑定证书 ID。
	Fail    bool   // Fail 控制外部更新失败。
}

// Product 复用生产产品配置描述。
func (*fakeCloudDeployer) Product() cloud.Product { return (&aliyun.CDN{}).Product() }

// Validate 复用真实配置校验。
func (*fakeCloudDeployer) Validate(raw json.RawMessage) (string, json.RawMessage, error) {
	return (&aliyun.CDN{}).Validate(raw)
}

// Deploy 模拟云端操作，保持同证书幂等。
func (f *fakeCloudDeployer) Deploy(_ context.Context, _ cloud.Credentials, _ json.RawMessage, c cloud.Certificate) error {
	f.Deploys++
	if f.Fail {
		return errors.New("模拟部署失败")
	}
	f.Bound = c.ID
	return nil
}

// Verify 模拟查询绑定是否已生效。
func (f *fakeCloudDeployer) Verify(_ context.Context, _ cloud.Credentials, _ json.RawMessage, c cloud.Certificate) (bool, error) {
	return f.Bound == c.ID, nil
}

// cloudFixture 创建生产系列与有效测试材料，所有云端调用被替换为模拟实现。
func cloudFixture(t *testing.T) (*Services, store.DNSCredential, store.Certificate, *fakeCloudCatalog, *fakeCloudDeployer) {
	t.Helper()
	s, credentialID := testReliabilityServices(t)
	s.Storage = deploy.NewStorage(t.TempDir(), s.Vault)
	ctx := context.Background()
	if _, err := s.Store.CreateUser(ctx, "test", "hash", "测试"); err != nil {
		t.Fatal(err)
	}
	order, err := s.CreateOrder(ctx, nil, CreateOrderInput{Domains: []string{"api.example.com"}, Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_PRODUCTION", CredentialID: credentialID, AgreeTerms: true})
	if err != nil {
		t.Fatal(err)
	}
	cert := cloudLocalCertificate(t, s, order.SeriesID, credentialID, 1)
	if err := s.Store.CompleteOrder(ctx, order.ID, cert.ID); err != nil {
		t.Fatal(err)
	}
	catalog := &fakeCloudCatalog{Cert: cloud.Certificate{ID: "123", Name: "existing", Domains: []string{"api.example.com"}, Fingerprint: cert.Fingerprint, NotAfter: cert.NotAfter.Format(time.RFC3339)}}
	product := &fakeCloudDeployer{}
	s.Cloud = &cloud.Registry{Catalog: catalog, Deployers: map[string]cloud.Deployer{"CDN": product}}
	account, err := s.SaveCredential(ctx, 1, 0, CredentialInput{DNSCredentialInput: DNSCredentialInput{Name: "test", Provider: "ALIYUN", AccessKeyID: "test-cloud-id", AccessKeySecret: "test-cloud-secret", Enabled: true}, CloudEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, account, cert, catalog, product
}

// cloudLocalCertificate 生成独立的新版本用于续期与材料验证。
func cloudLocalCertificate(t *testing.T, s *Services, seriesID string, credentialID, serial int64) store.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{"api.example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(der)
	bundle := deploy.Bundle{Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), Fingerprint: hex.EncodeToString(digest[:]), CommonName: "api.example.com", SANs: template.DNSNames, NotAfter: template.NotAfter}
	bundle.FullChain = bundle.Certificate
	certPath, chainPath, keyPath, err := s.Storage.Save(bundle)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := s.Store.InsertCertificate(context.Background(), store.Certificate{SeriesID: seriesID, CommonName: bundle.CommonName, SANs: bundle.SANs, Fingerprint: bundle.Fingerprint, CredentialID: credentialID, CADirectory: store.ProductionDirectory, NotBefore: template.NotBefore, NotAfter: template.NotAfter, CertPath: certPath, ChainPath: chainPath, KeyPath: keyPath})
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// runCloudTestJob 模拟工作者执行并重新读取持久结果。
func runCloudTestJob(t *testing.T, s *Services, planID int64) store.CloudJob {
	t.Helper()
	job, err := s.QueueCloudPlan(context.Background(), 1, planID)
	if err != nil {
		t.Fatal(err)
	}
	s.executeCloudJob(context.Background(), job)
	job, err = s.Store.FindCloudJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

// TestCloudUploadOnlyAndIndependentDeployment 验证上传规则独立成功且多个目标复用上传映射。
func TestCloudUploadOnlyAndIndependentDeployment(t *testing.T) {
	s, account, cert, catalog, product := cloudFixture(t)
	ctx := context.Background()
	upload, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "只上传", CredentialID: account.ID, Kind: "UPLOAD", SeriesID: &cert.SeriesID, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job := runCloudTestJob(t, s, upload.ID)
	if job.Status != "SUCCESS" || catalog.Uploads != 1 || product.Deploys != 0 {
		t.Fatalf("上传规则触发了部署或失败 %+v", job)
	}
	target, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "部署", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", SeriesID: &cert.SeriesID, Config: json.RawMessage(`{"domain":"api.example.com"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job = runCloudTestJob(t, s, target.ID)
	if job.Status != "SUCCESS" || catalog.Uploads != 1 || product.Deploys != 1 {
		t.Fatalf("应复用已上传证书 %+v", job)
	}
	job = runCloudTestJob(t, s, target.ID)
	if job.Status != "SUCCESS" || product.Deploys != 1 {
		t.Fatal("已生效绑定不应重复更新")
	}
}

// TestCloudExistingCertificateDoesNotUpload 验证固定云证书不依赖本地材料且支持失败后显式重试。
func TestCloudExistingCertificateDoesNotUpload(t *testing.T) {
	s, account, _, catalog, product := cloudFixture(t)
	ctx := context.Background()
	target, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "已有证书", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", RemoteCertificateID: "123", Config: json.RawMessage(`{"domain":"api.example.com"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	product.Fail = true
	job := runCloudTestJob(t, s, target.ID)
	if job.Status != "FAILED" || job.Stage != "DEPLOY" || catalog.Uploads != 0 {
		t.Fatalf("失败不能记为成功或触发上传 %+v", job)
	}
	product.Fail = false
	job = runCloudTestJob(t, s, target.ID)
	if job.Status != "SUCCESS" || catalog.Uploads != 0 {
		t.Fatalf("已有证书重试失败 %+v", job)
	}
}

// TestCloudRejectsMismatchAndDisabledAccount 验证不匹配证书与停用凭证都不会修改云产品。
func TestCloudRejectsMismatchAndDisabledAccount(t *testing.T) {
	s, account, _, _, product := cloudFixture(t)
	ctx := context.Background()
	target, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "不匹配", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", RemoteCertificateID: "123", Config: json.RawMessage(`{"domain":"other.example.com"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job := runCloudTestJob(t, s, target.ID)
	if job.Status != "FAILED" || product.Deploys != 0 {
		t.Fatal("域名不匹配仍部署")
	}
	if _, err := s.SaveCredential(ctx, 1, account.ID, CredentialInput{DNSCredentialInput: DNSCredentialInput{Name: "停用", Provider: "ALIYUN", Enabled: false}, CloudEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueCloudPlan(ctx, 1, target.ID); err == nil {
		t.Fatal("停用账号仍可入队")
	}
}

// TestCloudAutoRenewalQueuesOnlyNewVersions 验证自动规则不会反复部署同一版本，续期后会执行新版本。
func TestCloudAutoRenewalQueuesOnlyNewVersions(t *testing.T) {
	s, account, cert, catalog, _ := cloudFixture(t)
	ctx := context.Background()
	plan, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "自动上传", CredentialID: account.ID, Kind: "UPLOAD", SeriesID: &cert.SeriesID, Enabled: true, AutoSync: true})
	if err != nil {
		t.Fatal(err)
	}
	s.scheduleCloudPlans(ctx)
	job, err := s.Store.NextCloudJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.executeCloudJob(ctx, job)
	s.scheduleCloudPlans(ctx)
	if _, err := s.Store.NextCloudJob(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("同版本重复入队 %v", err)
	}
	newCert := cloudLocalCertificate(t, s, cert.SeriesID, cert.CredentialID, 2)
	renewal, err := s.Store.CreateOrder(ctx, store.Order{SeriesID: cert.SeriesID, Domains: []string{"api.example.com"}, Email: "ops@example.com", CADirectory: store.ProductionDirectory, CredentialID: cert.CredentialID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store.CompleteOrder(ctx, renewal.ID, newCert.ID); err != nil {
		t.Fatal(err)
	}
	s.scheduleCloudPlans(ctx)
	job, err = s.Store.NextCloudJob(ctx)
	if err != nil || job.PlanID != plan.ID || *job.CertificateID != newCert.ID {
		t.Fatalf("未跟随新版本 %+v %v", job, err)
	}
	s.executeCloudJob(ctx, job)
	if catalog.Uploads != 2 {
		t.Fatalf("续期未上传 %d", catalog.Uploads)
	}
}

// TestCloudCredentialsNeverSerialize 验证凭证加密和响应脱敏，并阻止更换已有账号身份。
func TestCloudCredentialsNeverSerialize(t *testing.T) {
	s, account, _, _, _ := cloudFixture(t)
	ctx := context.Background()
	stored, err := s.Store.FindCredential(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.AccessKeySecret, "test-cloud-secret") {
		t.Fatal("秘密以明文存储")
	}
	values, err := s.ListCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(values)
	if strings.Contains(string(raw), "test-cloud-secret") || strings.Contains(string(raw), stored.AccessKeySecret) {
		t.Fatal("响应泄露秘密或密文")
	}
	if _, err := s.SaveCredential(ctx, 1, account.ID, CredentialInput{DNSCredentialInput: DNSCredentialInput{Name: "替换", Provider: "ALIYUN", AccessKeyID: "other-account", Enabled: true}, CloudEnabled: true}); err == nil {
		t.Fatal("允许跨账号替换")
	}
}

// TestCloudRestartChecksRemoteBeforeRedeploy 验证外部部署成功而本地尚未保存结果时可恢复。
func TestCloudRestartChecksRemoteBeforeRedeploy(t *testing.T) {
	s, account, _, catalog, product := cloudFixture(t)
	ctx := context.Background()
	target, err := s.SaveCloudPlan(ctx, 1, 0, CloudPlanInput{Name: "恢复", CredentialID: account.ID, Kind: "DEPLOY", Product: "CDN", RemoteCertificateID: "123", Config: json.RawMessage(`{"domain":"api.example.com"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.QueueCloudPlan(ctx, 1, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	job.Status = "RUNNING"
	job.Stage = "VERIFY"
	if err := s.Store.SetCloudJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	product.Bound = "123"
	if err := s.Store.RecoverCloudJobs(ctx); err != nil {
		t.Fatal(err)
	}
	job, err = s.Store.NextCloudJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.executeCloudJob(ctx, job)
	job, err = s.Store.FindCloudJob(ctx, job.ID)
	if err != nil || job.Status != "SUCCESS" || product.Deploys != 0 || catalog.Uploads != 0 {
		t.Fatalf("恢复不幂等 %+v %v", job, err)
	}
}
