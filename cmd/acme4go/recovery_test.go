// 本文件验证异目录恢复与错误主密钥的离线诊断边界。
//
// @author Andersen
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/pki"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestBackupRestoresUnderNewDataDir 验证新相对路径与历史绝对路径均可在异目录恢复。
func TestBackupRestoresUnderNewDataDir(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		// 每种格式使用独立数据库和归档，避免同路径恢复掩盖问题。
		t.Run(map[bool]string{false: "relative", true: "legacy-absolute"}[legacy], func(t *testing.T) { testBackupAtNewRoot(t, legacy) })
	}
}

// testBackupAtNewRoot 实际备份并解压测试归档，确认从未回退读取原材料目录。
func testBackupAtNewRoot(t *testing.T, legacy bool) {
	ctx := context.Background()
	original := filepath.Join(t.TempDir(), "old-data")
	database, err := store.Open(original)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := database.CreateDNSCredential(ctx, store.DNSCredential{Name: "fixture", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "encrypted", AccessKeySecret: "encrypted", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series, err := database.ResolveCertificateSeries(ctx, store.CertificateSeries{ID: "review-series", Code: "R", Name: "review", Domains: []string{"example.com"}, DomainsHash: store.DomainSetHash([]string{"example.com"}), Email: "ops@example.com", CADirectory: store.StagingDirectory, CAEnvironment: "LETS_ENCRYPT_STAGING", CredentialID: cred.ID})
	if err != nil {
		t.Fatal(err)
	}
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	storage := deploy.NewStorage(original, vault)
	fingerprint := strings.Repeat("a", 64)
	cp, chp, kp, err := storage.Save(deploy.Bundle{Certificate: []byte("synthetic-cert"), FullChain: []byte("synthetic-chain"), PrivateKey: []byte("synthetic-key"), Fingerprint: fingerprint, CommonName: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if legacy {
		cp = filepath.Join(original, "certificates", cp)
		chp = filepath.Join(original, "certificates", chp)
		kp = filepath.Join(original, "certificates", kp)
	}
	cert, err := database.InsertCertificate(ctx, store.Certificate{SeriesID: series.ID, CommonName: "example.com", SANs: series.Domains, Fingerprint: fingerprint, CredentialID: cred.ID, CADirectory: store.StagingDirectory, CertPath: cp, ChainPath: chp, KeyPath: kp, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := runBackup(config.Config{DataDir: original}, archive); err != nil {
		t.Fatal(err)
	}
	restoredRoot := t.TempDir()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsLocal(h.Name) {
			t.Fatal("不安全归档路径")
		}
		p := filepath.Join(restoredRoot, h.Name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, content, 0600); err != nil {
			t.Fatal(err)
		}
	}

	restored := filepath.Join(restoredRoot, "data")
	database, err = store.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	saved, err := database.FindCertificate(ctx, cert.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	restoredStorage := deploy.NewStorage(restored, vault)
	// 原目录仍存在但内容已经损坏，恢复实例必须只使用新目录材料。
	originalCertificate := filepath.Join(original, "certificates", "example.com", fingerprint, "certificate.pem")
	if err := os.WriteFile(originalCertificate, []byte("wrong-original"), 0600); err != nil {
		t.Fatal(err)
	}
	bundle, err := restoredStorage.Load(saved.CertPath, saved.ChainPath, saved.KeyPath, saved.Fingerprint, saved.CommonName)
	if err != nil || string(bundle.Certificate) != "synthetic-cert" || string(bundle.PrivateKey) != "synthetic-key" {
		t.Fatalf("恢复读取了旧目录或无法解密：%v", err)
	}
	if err := os.Rename(original, original+"-offline"); err != nil {
		t.Fatal(err)
	}
	if _, err := restoredStorage.Load(saved.CertPath, saved.ChainPath, saved.KeyPath, saved.Fingerprint, saved.CommonName); err != nil {
		t.Fatalf("原目录移走后恢复材料不可读：%v", err)
	}
	// 当前目录缺失材料时必须失败，不能使用仍然存在的旧目录填补缺口。
	if err := os.Rename(original+"-offline", original); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(restored, "certificates", "example.com", fingerprint, "certificate.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := restoredStorage.Load(saved.CertPath, saved.ChainPath, saved.KeyPath, saved.Fingerprint, saved.CommonName); err == nil {
		t.Fatal("当前材料缺失时回退读取了旧目录")
	}

}

// TestDoctorRejectsWrongMasterKey 验证无业务证书时，错误主密钥也不能通过诊断。
func TestDoctorRejectsWrongMasterKey(t *testing.T) {
	dataDir := t.TempDir()
	database, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pki.LoadOrCreate(dataDir, "https://127.0.0.1:9443", vault); err != nil {
		t.Fatal(err)
	}
	wrong := []byte(strings.Repeat("s", 32))
	wrongVault, err := cryptoutil.NewVault(wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pki.LoadOrCreate(dataDir, "https://127.0.0.1:9443", wrongVault); err == nil {
		t.Fatal("未构造出错误主密钥")
	}
	before, err := os.ReadFile(filepath.Join(dataDir, "pki", "ca-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runDoctor(config.Config{DataDir: dataDir, MasterKey: wrong}); err == nil {
		t.Fatal("错误主密钥仍通过诊断")
	}
	if err := runDoctor(config.Config{DataDir: dataDir, MasterKey: []byte(strings.Repeat("r", 32))}); err != nil {
		t.Fatalf("正确主密钥被拒绝：%v", err)
	}
	after, err := os.ReadFile(filepath.Join(dataDir, "pki", "ca-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("诊断修改了内部 CA 材料")
	}

}
