// 本文件验证环境变量、.env 优先级与运行角色配置边界。
//
// @author Andersen
package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadUsesDocumentedDefaults 验证未覆盖的非敏感配置与文档默认值保持一致。
func TestLoadUsesDocumentedDefaults(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", "")
	clearOptionalConfigForTest(t)
	t.Setenv("ACME_MASTER_KEY", strings.Repeat("m", 32))

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Role != RoleServer || !cfg.LocalDeploymentEnabled {
		t.Fatalf("默认拓扑应为 Server + 本机执行器，实际 role=%s local=%t", cfg.Role, cfg.LocalDeploymentEnabled)
	}
	if cfg.Version != "0.1.0-dev" || cfg.LogLevel != slog.LevelInfo {
		t.Errorf("默认版本或日志级别错误: version=%s logLevel=%s", cfg.Version, cfg.LogLevel)
	}
	if cfg.ListenAddress != ":8080" || cfg.AgentListenAddress != ":9443" {
		t.Errorf("默认监听地址错误: admin=%s agent=%s", cfg.ListenAddress, cfg.AgentListenAddress)
	}
	if cfg.PublicURL != "http://127.0.0.1:8080" || cfg.AgentPublicURL != "https://127.0.0.1:9443" {
		t.Errorf("默认公开地址错误: admin=%s agent=%s", cfg.PublicURL, cfg.AgentPublicURL)
	}
	expectedDataDir, _ := filepath.Abs("./data")
	expectedDeploymentDir, _ := filepath.Abs("./deployments")
	if cfg.DataDir != expectedDataDir || cfg.DeploymentDir != expectedDeploymentDir {
		t.Errorf("默认运行目录错误: data=%s deployments=%s", cfg.DataDir, cfg.DeploymentDir)
	}
	if cfg.AdminUsername != "admin" || cfg.AdminPassword != "" {
		t.Errorf("默认管理员配置错误: username=%s passwordConfigured=%t", cfg.AdminUsername, cfg.AdminPassword != "")
	}
	if cfg.SessionTTL != 24*time.Hour || cfg.RenewBefore != 30*24*time.Hour || cfg.SchedulerInterval != time.Hour {
		t.Errorf("默认 Server 周期错误: session=%s renew=%s scheduler=%s", cfg.SessionTTL, cfg.RenewBefore, cfg.SchedulerInterval)
	}
	if cfg.AgentPollInterval != 30*time.Second || cfg.DNSPropagationTimeout != 3*time.Minute || cfg.DNSPollingInterval != 5*time.Second {
		t.Errorf("默认轮询周期错误: agent=%s propagation=%s dnsPolling=%s", cfg.AgentPollInterval, cfg.DNSPropagationTimeout, cfg.DNSPollingInterval)
	}
	if cfg.HistoryRetention != 730*24*time.Hour {
		t.Errorf("默认历史保留期错误: %s", cfg.HistoryRetention)
	}
	if len(cfg.TrustedProxyCIDRs) != 0 {
		t.Errorf("默认不应信任反向代理网段: %v", cfg.TrustedProxyCIDRs)
	}
}

// TestLoadAcceptsSupportedLogLevels 验证四种公开日志级别均映射到对应 slog 级别。
func TestLoadAcceptsSupportedLogLevels(t *testing.T) {
	levels := map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError}
	for input, expected := range levels {
		// 日志级别子测试回调隔离每种大小写不敏感的输入映射。
		t.Run(input, func(t *testing.T) {
			t.Setenv("ACME_ENV_FILE", "")
			clearOptionalConfigForTest(t)
			t.Setenv("ACME_MASTER_KEY", strings.Repeat("m", 32))
			t.Setenv("ACME_LOG_LEVEL", strings.ToUpper(input))
			cfg, err := Load(RoleServer)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LogLevel != expected {
				t.Fatalf("日志级别映射错误: input=%s expected=%s actual=%s", input, expected, cfg.LogLevel)
			}
		})
	}
}

// TestLoadRejectsUnsupportedLogLevel 验证未知日志级别不会被静默降级为默认值。
func TestLoadRejectsUnsupportedLogLevel(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", "")
	t.Setenv("ACME_LOG_LEVEL", "verbose")
	if _, err := Load(RoleServer); err == nil || !strings.Contains(err.Error(), "ACME_LOG_LEVEL") {
		t.Fatalf("未知日志级别应返回明确错误，实际为 %v", err)
	}
}

// TestSecretFilesOverrideEnvironment 验证敏感项优先读取对应的 Secret 文件。
func TestSecretFilesOverrideEnvironment(t *testing.T) {
	directory := t.TempDir()
	masterKeyPath := filepath.Join(directory, "master-key")
	passwordPath := filepath.Join(directory, "admin-password")
	if err := os.WriteFile(masterKeyPath, []byte(strings.Repeat("f", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordPath, []byte("password-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACME_ENV_FILE", "")
	t.Setenv("ACME_MASTER_KEY", strings.Repeat("e", 32))
	t.Setenv("ACME_MASTER_KEY_FILE", masterKeyPath)
	t.Setenv("ACME_ADMIN_PASSWORD", "password-from-environment")
	t.Setenv("ACME_ADMIN_PASSWORD_FILE", passwordPath)
	cfg, err := Load(RoleServer)
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.MasterKey) != strings.Repeat("f", 32) || cfg.AdminPassword != "password-from-file" {
		t.Fatal("Secret 文件内容应覆盖同名环境变量")
	}
}

// TestLoadRejectsUnreadableSecretFile 验证显式 Secret 文件错误不会静默回退到普通环境变量。
func TestLoadRejectsUnreadableSecretFile(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", "")
	t.Setenv("ACME_MASTER_KEY", strings.Repeat("m", 32))
	t.Setenv("ACME_MASTER_KEY_FILE", filepath.Join(t.TempDir(), "missing-secret"))
	if _, err := Load(RoleServer); err == nil || !strings.Contains(err.Error(), "ACME_MASTER_KEY_FILE") {
		t.Fatalf("不可读 Secret 文件应返回明确错误，实际为 %v", err)
	}
}

// TestLoadRejectsInvalidDuration 验证显式无效时长不会静默使用默认值。
func TestLoadRejectsInvalidDuration(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", "")
	t.Setenv("ACME_MASTER_KEY", strings.Repeat("m", 32))
	t.Setenv("ACME_SCHEDULER_INTERVAL", "sometimes")
	if _, err := Load(RoleServer); err == nil || !strings.Contains(err.Error(), "ACME_SCHEDULER_INTERVAL") {
		t.Fatalf("无效显式时长应返回明确错误，实际为 %v", err)
	}
}

// TestLoadRejectsLegacyRole 验证已经移除的旧运行角色会被拒绝。
func TestLoadRejectsLegacyRole(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", "")
	t.Setenv("ACME_MASTER_KEY", strings.Repeat("m", 32))
	if _, err := Load("standalone"); err == nil {
		t.Fatal("standalone 旧角色应被拒绝")
	}
}

// TestAgentAlwaysDisablesLocalExecutor 验证 Agent 角色不能启用控制平面本机执行器。
func TestAgentAlwaysDisablesLocalExecutor(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", "")
	directory := t.TempDir()
	t.Setenv("ACME_LOCAL_DEPLOYMENT_ENABLED", "true")
	t.Setenv("ACME_DATA_DIR", directory)
	t.Setenv("ACME_AGENT_JOIN_URL", "acme4go://join/test")

	cfg, err := Load(RoleAgent)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocalDeploymentEnabled {
		t.Fatal("Agent 角色不应启用 Server 本机执行器")
	}
}

// TestLoadRejectsInvalidLocalExecutorFlag 验证非法布尔开关不会静默回退。
func TestLoadRejectsInvalidLocalExecutorFlag(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", "")
	t.Setenv("ACME_LOCAL_DEPLOYMENT_ENABLED", "enabled")
	if _, err := Load(RoleServer); err == nil {
		t.Fatal("无效的本机执行器开关应被拒绝")
	}
}

// TestLoadRejectsInvalidTrustedProxyCIDR 验证错误的可信代理网段不会导致转发头被误信任。
func TestLoadRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", "")
	t.Setenv("ACME_MASTER_KEY", strings.Repeat("m", 32))
	t.Setenv("ACME_TRUSTED_PROXY_CIDRS", "127.0.0.0/8,not-a-network")
	if _, err := Load(RoleServer); err == nil {
		t.Fatal("无效的可信代理 CIDR 应被拒绝")
	}
}

// TestLoadReadsEnvFileWithoutOverridingProcessEnvironment 验证进程环境变量优先于 .env。
func TestLoadReadsEnvFileWithoutOverridingProcessEnvironment(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "acme4go.env")
	content := strings.Join([]string{
		"ACME_MASTER_KEY='" + strings.Repeat("f", 32) + "'",
		"ACME_LOCAL_DEPLOYMENT_ENABLED=false # 纯控制平面",
		"ACME_ADMIN_USERNAME=from-file",
		"ACME_DATA_DIR=./file-data",
		"ACME_DEPLOYMENT_DIR=\"./file-deployments\"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	unsetEnvForTest(t, "ACME_MASTER_KEY")
	unsetEnvForTest(t, "ACME_LOCAL_DEPLOYMENT_ENABLED")
	unsetEnvForTest(t, "ACME_DATA_DIR")
	unsetEnvForTest(t, "ACME_DEPLOYMENT_DIR")
	t.Setenv("ACME_ENV_FILE", path)
	t.Setenv("ACME_ADMIN_USERNAME", "from-process")

	cfg, err := Load(RoleServer)
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.MasterKey) != strings.Repeat("f", 32) || cfg.LocalDeploymentEnabled {
		t.Fatalf(".env 配置未生效: local=%t keyLength=%d", cfg.LocalDeploymentEnabled, len(cfg.MasterKey))
	}
	if cfg.AdminUsername != "from-process" {
		t.Fatalf("进程环境变量应覆盖 .env，实际用户名: %s", cfg.AdminUsername)
	}
}

// TestLoadRejectsMissingExplicitEnvFile 验证显式指定但不存在的配置文件会报错。
func TestLoadRejectsMissingExplicitEnvFile(t *testing.T) {
	t.Setenv("ACME_ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))
	if _, err := Load(RoleServer); err == nil {
		t.Fatal("显式指定的环境配置文件不存在时应返回错误")
	}
}

// unsetEnvForTest 临时移除环境变量并在测试结束后恢复原值。
func unsetEnvForTest(t *testing.T, name string) {
	t.Helper()
	previous, exists := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	// 清理回调恢复测试开始前的环境变量状态。
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, previous)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

// clearOptionalConfigForTest 清空可能由测试进程继承的可选配置，确保验证内置默认值。
func clearOptionalConfigForTest(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ACME_ROLE", "ACME_VERSION", "ACME_LOG_LEVEL", "ACME_LOCAL_DEPLOYMENT_ENABLED",
		"ACME_LISTEN_ADDRESS", "ACME_AGENT_LISTEN_ADDRESS", "ACME_PUBLIC_URL", "ACME_AGENT_PUBLIC_URL",
		"ACME_TRUSTED_PROXY_CIDRS", "ACME_DATA_DIR", "ACME_DEPLOYMENT_DIR", "ACME_ADMIN_USERNAME",
		"ACME_ADMIN_PASSWORD", "ACME_ADMIN_PASSWORD_FILE", "ACME_SESSION_TTL", "ACME_RENEW_BEFORE",
		"ACME_SCHEDULER_INTERVAL", "ACME_AGENT_POLL_INTERVAL", "ACME_AGENT_JOIN_URL", "ACME_AGENT_NAME",
		"ACME_HISTORY_RETENTION",
		"ACME_DNS_PROPAGATION_TIMEOUT", "ACME_DNS_POLLING_INTERVAL", "ACME_MASTER_KEY_FILE",
		"ACME_ALERT_WEBHOOK_URL", "ACME_ALERT_WEBHOOK_SECRET", "ACME_ALERT_WEBHOOK_SECRET_FILE",
	} {
		t.Setenv(name, "")
	}
}
