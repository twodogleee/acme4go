// 本文件验证诊断不会初始化或迁移内部 CA，也能识别错误密钥和缺失材料。
//
// @author Andersen
package pki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
)

// TestCheckExistingCALegacyWithoutMigration 验证旧 CA 双文件只读检查不会生成原子封装。
func TestCheckExistingCALegacyWithoutMigration(t *testing.T) {
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	certificate, envelope, err := createCA(vault)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	pkiDir := filepath.Join(directory, "pki")
	if err := os.Mkdir(pkiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkiDir, "ca.pem"), certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(pkiDir, "ca-key.pem.enc")
	if err := os.WriteFile(keyPath, envelope, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckExistingCA(directory, vault); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pkiDir, "ca-bundle.json")); !os.IsNotExist(err) {
		t.Fatalf("诊断迁移了旧身份材料：%v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if err := CheckExistingCA(directory, vault); err == nil {
		t.Fatal("缺少私钥的旧 CA 仍通过诊断")
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("诊断重新生成了缺失私钥")
	}
}

// TestCheckExistingCADoesNotCreateIdentity 验证空目录诊断明确失败且不创建 CA。
func TestCheckExistingCADoesNotCreateIdentity(t *testing.T) {
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := CheckExistingCA(directory, vault); err == nil {
		t.Fatal("无 CA 的目录被报告为正常")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("诊断创建了身份材料：%v", err)
	}
}
