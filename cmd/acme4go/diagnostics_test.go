// 本文件验证诊断覆盖全部关键密文，并拒绝把损坏材料报告为正常。
//
// @author Andersen
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/pki"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestDoctorChecksDNSAndACMEEnvelopes 验证 CA 正常时仍检查停用 DNS 凭据和 ACME 私钥，不泄露明文。
func TestDoctorChecksDNSAndACMEEnvelopes(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{DataDir: t.TempDir(), MasterKey: []byte(strings.Repeat("r", 32))}
	database, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	vault, err := cryptoutil.NewVault(cfg.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pki.LoadOrCreate(cfg.DataDir, "https://127.0.0.1:9443", vault); err != nil {
		t.Fatal(err)
	}
	idEnvelope, err := vault.Encrypt("dns-access-key-id", []byte("synthetic-id"))
	if err != nil {
		t.Fatal(err)
	}
	secretEnvelope, err := vault.Encrypt("dns-access-key-secret", []byte("synthetic-secret"))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := database.CreateDNSCredential(ctx, store.DNSCredential{Name: "diagnostic", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: idEnvelope, AccessKeySecret: secretEnvelope, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := vault.Encrypt("acme-account:diagnostic", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	account := store.ACMEAccount{Key: "diagnostic", Email: "ops@example.com", CADirectory: store.StagingDirectory, PrivateKey: envelope}
	if err := database.PutACMEAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := runDoctor(cfg); err != nil {
		t.Fatalf("正常密文未通过：%v", err)
	}
	credential.AccessKeySecret = "v1.AA.AA"
	if err := database.UpdateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	if err := runDoctor(cfg); err == nil || !strings.Contains(err.Error(), "DNS 凭据") {
		t.Fatalf("损坏 DNS 密文未被识别：%v", err)
	}
	credential.AccessKeySecret = secretEnvelope
	if err := database.UpdateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	account.PrivateKey = "v1.AA.AA"
	if err := database.PutACMEAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := runDoctor(cfg); err == nil || !strings.Contains(err.Error(), "ACME 账户") {
		t.Fatalf("损坏 ACME 密文未被识别：%v", err)
	}
	account.PrivateKey, err = vault.Encrypt("acme-account:diagnostic", []byte("synthetic-secret-not-a-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.PutACMEAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := runDoctor(cfg); err == nil || strings.Contains(err.Error(), "synthetic-secret-not-a-key") {
		t.Fatalf("无效私钥未被拒绝或泄露明文：%v", err)
	}
}
