// 本文件验证新旧材料路径解析及当前证书库的目录隔离。
//
// @author Andersen
package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
)

// TestMaterialPathsRejectTraversalAndIdentityMismatch 验证路径只对应当前身份，不能串读其他证书或文件类型。
func TestMaterialPathsRejectTraversalAndIdentityMismatch(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	want := filepath.Join("example.com", fingerprint, "certificate.pem")
	for _, path := range []string{want, filepath.Join(t.TempDir(), "certificates", want)} {
		got, err := materialPath(path, "example.com", fingerprint, "certificate.pem")
		if err != nil || got != want {
			t.Fatalf("正常路径未归一到当前证书库：%q %v", got, err)
		}
	}
	for _, path := range []string{"../" + want, want + "/../certificate.pem", filepath.Join("other.example.com", fingerprint, "certificate.pem"), filepath.Join("example.com", strings.Repeat("b", 64), "certificate.pem"), filepath.Join("example.com", fingerprint, "private-key.pem.enc"), "/etc/passwd"} {
		if _, err := materialPath(path, "example.com", fingerprint, "certificate.pem"); err == nil {
			t.Fatalf("接受越界或不匹配路径：%q", path)
		}
	}
}

// TestStorageLoadRejectsSymlinkEscape 验证即使路径格式正确，符号链接也不能把读取引向证书库外。
func TestStorageLoadRejectsSymlinkEscape(t *testing.T) {
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	storage := NewStorage(t.TempDir(), vault)
	bundle := Bundle{CommonName: "example.com", Fingerprint: strings.Repeat("a", 64), Certificate: []byte("cert"), FullChain: []byte("chain"), PrivateKey: []byte("key")}
	cp, chp, kp, err := storage.Save(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(cp) || filepath.IsAbs(chp) || filepath.IsAbs(kp) {
		t.Fatal("新材料仍持久化绝对路径")
	}
	outside := filepath.Join(t.TempDir(), "outside.pem")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(storage.root, cp)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(storage.root, cp)); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Load(cp, chp, kp, bundle.Fingerprint, bundle.CommonName); err == nil {
		t.Fatal("通过符号链接读取了证书库外材料")
	}
}
