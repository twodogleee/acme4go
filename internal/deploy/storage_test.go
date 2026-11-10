// 本文件验证证书原子物化、路径隔离和密钥匹配规则。
//
// @author Andersen
package deploy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMaterializeSwitchesCurrent 验证证书版本写入后 current 原子指向新版本。
func TestMaterializeSwitchesCurrent(t *testing.T) {
	root := t.TempDir()
	fingerprint := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	bundle := Bundle{Certificate: []byte("cert"), FullChain: []byte("chain"), PrivateKey: []byte("key"), Fingerprint: fingerprint, CommonName: "example.com", SANs: []string{"example.com"}, NotAfter: time.Now().Add(time.Hour)}
	current, err := Materialize(root, "nginx-public", 1, bundle)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(current)
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join("versions", fingerprint) {
		t.Fatalf("current 指向异常: %s", target)
	}
	privateKey, err := os.ReadFile(filepath.Join(current, "private-key.pem"))
	if err != nil || string(privateKey) != "key" {
		t.Fatalf("私钥物化异常: %q, %v", privateKey, err)
	}
}

// TestPruneMaterializedVersionsKeepsCurrentAndRollbackVersions 验证清理不会删除 current 与最近的回退版本。
func TestPruneMaterializedVersionsKeepsCurrentAndRollbackVersions(t *testing.T) {
	root := t.TempDir()
	versionsRoot := filepath.Join(root, "nginx", "versions")
	if err := os.MkdirAll(versionsRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-800 * 24 * time.Hour)
	for index := 1; index <= 6; index++ {
		path := filepath.Join(versionsRoot, fmt.Sprintf("%064x", index))
		if err := os.Mkdir(path, 0o750); err != nil {
			t.Fatal(err)
		}
		stamp := old.Add(time.Duration(index) * time.Hour)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	currentFingerprint := fmt.Sprintf("%064x", 6)
	if err := os.Symlink(filepath.Join("versions", currentFingerprint), filepath.Join(root, "nginx", "current")); err != nil {
		t.Fatal(err)
	}
	removed, err := PruneMaterializedVersions(root, time.Now().Add(-730*24*time.Hour), 3)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("应删除两个超过保留期的旧版本，实际删除 %d", removed)
	}
	if _, err := os.Stat(filepath.Join(versionsRoot, currentFingerprint)); err != nil {
		t.Fatalf("current 指向版本不应被删除: %v", err)
	}
}

// TestMaterializeRejectsAliasTraversal 验证目录穿越别名被拒绝。
func TestMaterializeRejectsAliasTraversal(t *testing.T) {
	_, err := Materialize(t.TempDir(), "../escape", 1, Bundle{Fingerprint: strings.Repeat("a", 64)})
	if err == nil {
		t.Fatal("目录穿越别名应被拒绝")
	}
}

// TestMaterializeRejectsSymlinkedParent 验证预置的父目录符号链接不能把明文私钥写出部署根目录。
func TestMaterializeRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	targetRoot := filepath.Join(root, "nginx-public")
	if err := os.Mkdir(targetRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(targetRoot, "versions")); err != nil {
		t.Fatal(err)
	}
	bundle := Bundle{
		Certificate: []byte("cert"), FullChain: []byte("chain"), PrivateKey: []byte("private"),
		Fingerprint: strings.Repeat("b", 64), CommonName: "example.com",
	}
	if _, err := Materialize(root, "nginx-public", 1, bundle); err == nil {
		t.Fatal("包含符号链接的父目录应被拒绝")
	}
	if _, err := os.Stat(filepath.Join(outside, bundle.Fingerprint, "private-key.pem")); !os.IsNotExist(err) {
		t.Fatalf("部署根目录外不应出现私钥文件: %v", err)
	}
}

// TestValidateBundleChecksIdentityAndPrivateKey 验证指纹、域名和私钥匹配规则。
func TestValidateBundleChecksIdentityAndPrivateKey(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(certificateDER)
	bundle := Bundle{
		Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}),
		PrivateKey:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}),
	}
	bundle.FullChain = bundle.Certificate
	bundle.Fingerprint = hex.EncodeToString(digest[:])
	bundle.CommonName = "example.com"
	bundle.SANs = []string{"example.com"}
	fingerprint := hex.EncodeToString(digest[:])
	if err := ValidateBundle(bundle, "example.com", fingerprint); err != nil {
		t.Fatalf("有效证书材料应通过校验: %v", err)
	}
	if err := ValidateBundle(bundle, "example.com", "wrong"); err == nil {
		t.Fatal("错误指纹应被拒绝")
	}
	if err := ValidateBundle(bundle, "other.example.com", fingerprint); err == nil {
		t.Fatal("不在 SAN 中的域名应被拒绝")
	}
	root := t.TempDir()
	if _, err := Materialize(root, "self-heal", 1, bundle); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMaterialized(root, "self-heal", bundle.SANs, fingerprint); err != nil {
		t.Fatalf("完整物化目录应通过状态复核: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "self-heal", "versions", fingerprint, "private-key.pem")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMaterialized(root, "self-heal", bundle.SANs, fingerprint); err == nil {
		t.Fatal("缺失私钥时必须触发 Agent 重新物化")
	}
}
