// 本文件验证内部 CA 原子持久化和运行中 TLS 证书轮换。
//
// @author Andersen
package pki

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
)

// TestLoadOrCreatePersistsAtomicCABundle 验证首次启动只需一个原子 CA 封装即可恢复相同 CA。
func TestLoadOrCreatePersistsAtomicCABundle(t *testing.T) {
	directory := t.TempDir()
	vault := testVault(t)
	first, err := LoadOrCreate(directory, "https://127.0.0.1:9443", vault)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "pki", "ca-bundle.json")); err != nil {
		t.Fatalf("缺少原子 CA 封装: %v", err)
	}
	second, err := LoadOrCreate(directory, "https://127.0.0.1:9443", vault)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.CAPEM()) != string(second.CAPEM()) {
		t.Fatal("重启后应恢复同一个内部 CA")
	}
}

// TestLoadOrCreateRejectsPartialLegacyCA 验证旧 CA 双文件只剩一份时不会静默生成新 CA。
func TestLoadOrCreateRejectsPartialLegacyCA(t *testing.T) {
	directory := t.TempDir()
	pkiDirectory := filepath.Join(directory, "pki")
	if err := os.MkdirAll(pkiDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkiDirectory, "ca.pem"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(directory, "https://127.0.0.1:9443", testVault(t)); err == nil {
		t.Fatal("CA 文件不完整时应要求从备份恢复")
	}
}

// TestLoadOrCreateRejectsMismatchedCAKey 验证来自不同备份的 CA 证书和私钥不会被组合使用。
func TestLoadOrCreateRejectsMismatchedCAKey(t *testing.T) {
	vault := testVault(t)
	firstDirectory := t.TempDir()
	secondDirectory := t.TempDir()
	if _, err := LoadOrCreate(firstDirectory, "https://127.0.0.1:9443", vault); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(secondDirectory, "https://127.0.0.1:9443", vault); err != nil {
		t.Fatal(err)
	}
	firstBundle := readCABundle(t, firstDirectory)
	secondBundle := readCABundle(t, secondDirectory)
	firstBundle.EncryptedKeyEnvelope = secondBundle.EncryptedKeyEnvelope
	content, err := json.Marshal(firstBundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstDirectory, "pki", "ca-bundle.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(firstDirectory, "https://127.0.0.1:9443", vault); err == nil {
		t.Fatal("CA 证书与私钥不匹配时应拒绝启动")
	}
}

// TestRotateServerCertificateUpdatesTLSConfig 验证既有 TLS 配置会读取运行中原子切换的新证书。
func TestRotateServerCertificateUpdatesTLSConfig(t *testing.T) {
	manager, err := LoadOrCreate(t.TempDir(), "https://127.0.0.1:9443", testVault(t))
	if err != nil {
		t.Fatal(err)
	}
	configuration := manager.ServerTLSConfig()
	before, err := configuration.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt, err := manager.ServerCertificateExpiry()
	if err != nil {
		t.Fatal(err)
	}
	if _, rotated, err := manager.RotateServerCertificateIfNeeded(expiresAt.Add(-29*24*time.Hour), 30*24*time.Hour); err != nil || !rotated {
		t.Fatalf("进入续期窗口后应完成轮换: rotated=%t err=%v", rotated, err)
	}
	after, err := configuration.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(before.Certificate[0]) == string(after.Certificate[0]) {
		t.Fatal("TLS 配置仍返回旧服务端证书")
	}
}

// testVault 创建测试内部 CA 使用的固定主密钥加密器。
func testVault(t *testing.T) *cryptoutil.Vault {
	t.Helper()
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("m", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return vault
}

// readCABundle 读取测试目录中的内部 CA 原子封装。
func readCABundle(t *testing.T, directory string) caBundle {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directory, "pki", "ca-bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var bundle caBundle
	if err := json.Unmarshal(content, &bundle); err != nil {
		t.Fatal(err)
	}
	return bundle
}
