// 本文件验证健康检查读取监听地址时的环境优先级和配置边界。
//
// @author Andersen
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHealthcheckListenAddressUsesEnvFile 验证健康检查只读取配置文件中的首个监听地址。
func TestHealthcheckListenAddressUsesEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.env")
	content := "ACME_MASTER_KEY='test-only-secret'\nexport ACME_LISTEN_ADDRESS=\":18080\" # 管理端\nACME_LISTEN_ADDRESS=:19090\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACME_ENV_FILE", path)
	unsetEnvForTest(t, "ACME_LISTEN_ADDRESS")
	unsetEnvForTest(t, "ACME_MASTER_KEY")
	address, err := HealthcheckListenAddress()
	if err != nil || address != ":18080" {
		t.Fatalf("未正确读取环境文件中的监听地址: address=%q err=%v", address, err)
	}
	if _, exists := os.LookupEnv("ACME_MASTER_KEY"); exists {
		t.Fatal("健康检查不应将无关密钥加载到进程环境")
	}
}

// TestHealthcheckListenAddressUsesProcessEnvironment 验证进程环境及空值回退的优先级。
func TestHealthcheckListenAddressUsesProcessEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(path, []byte("ACME_LISTEN_ADDRESS=:18080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACME_ENV_FILE", path)
	t.Setenv("ACME_LISTEN_ADDRESS", " 127.0.0.1:19090 ")
	address, err := HealthcheckListenAddress()
	if err != nil || address != "127.0.0.1:19090" {
		t.Fatalf("进程环境应覆盖文件中的监听地址: address=%q err=%v", address, err)
	}
	t.Setenv("ACME_LISTEN_ADDRESS", "  ")
	address, err = HealthcheckListenAddress()
	if err != nil || address != defaultListenAddress {
		t.Fatalf("进程环境为空时应使用默认地址: address=%q err=%v", address, err)
	}
}

// TestHealthcheckListenAddressDefaults 验证默认环境文件缺失时回退，显式文件缺失时返回错误。
func TestHealthcheckListenAddressDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("ACME_ENV_FILE", "")
	unsetEnvForTest(t, "ACME_LISTEN_ADDRESS")
	address, err := HealthcheckListenAddress()
	if err != nil || address != defaultListenAddress {
		t.Fatalf("缺少默认环境文件时应使用默认地址: address=%q err=%v", address, err)
	}
	if err := os.WriteFile(defaultEnvFile, []byte("ACME_LISTEN_ADDRESS=:18080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	address, err = HealthcheckListenAddress()
	if err != nil || address != ":18080" {
		t.Fatalf("应读取默认环境文件中的监听地址: address=%q err=%v", address, err)
	}
	t.Setenv("ACME_ENV_FILE", "missing.env")
	if _, err := HealthcheckListenAddress(); err == nil {
		t.Fatal("显式环境文件不存在时应返回错误")
	}
}
