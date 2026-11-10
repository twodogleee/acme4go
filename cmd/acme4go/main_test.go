// 本文件验证 Server 双监听端口的启动前检查。
//
// @author Andersen
package main

import (
	"bytes"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/config"
)

// TestRunBackupCreatesPrivateArchive 验证一致备份创建新归档并限制文件权限。
func TestRunBackupCreatesPrivateArchive(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(t.TempDir(), "acme4go.tar.gz")
	if err := runBackup(config.Config{DataDir: directory}, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("备份归档应非空且仅所有者可访问: size=%d mode=%o", info.Size(), info.Mode().Perm())
	}
	if err := runBackup(config.Config{DataDir: directory}, destination); err == nil {
		t.Fatal("备份命令不应覆盖既有归档")
	}
}

// TestLogConfigurationExcludesSecrets 验证启动配置摘要只输出排障所需的非敏感字段。
func TestLogConfigurationExcludesSecrets(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := config.Config{
		Role: config.RoleAgent, Version: "test", LogLevel: slog.LevelDebug,
		DataDir: "/data", DeploymentDir: "/deployments", AgentPollInterval: 30,
		AgentName: "edge-01", AgentJoinURL: "acme4go://join/sensitive-token",
		MasterKey: []byte("sensitive-master-key"), AdminPassword: "sensitive-password",
	}
	logConfiguration(logger, cfg)
	logged := output.String()
	for _, secret := range []string{"sensitive-token", "sensitive-master-key", "sensitive-password"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("启动配置摘要泄露敏感内容 %q: %s", secret, logged)
		}
	}
	for _, expected := range []string{`"role":"agent"`, `"log_level":"DEBUG"`, `"join_configured":true`} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("启动配置摘要缺少字段 %s: %s", expected, logged)
		}
	}
	output.Reset()
	cfg.Role = config.RoleServer
	cfg.PublicURL = "https://admin:secret@example.com/console?token=hidden"
	cfg.AgentPublicURL = "https://agent:secret@agent.example.com:9443/path"
	logConfiguration(logger, cfg)
	logged = output.String()
	for _, secret := range []string{"admin:secret", "agent:secret", "token=hidden"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("Server 配置摘要泄露 URL 敏感内容 %q: %s", secret, logged)
		}
	}
	for _, expected := range []string{`"public_url":"https://example.com"`, `"agent_public_url":"https://agent.example.com:9443"`} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("Server 配置摘要缺少脱敏地址 %s: %s", expected, logged)
		}
	}
}

// TestListenServerEndpoints 验证双监听器可以同时占用不同的动态端口。
func TestListenServerEndpoints(t *testing.T) {
	adminListener, agentListener, err := listenServerEndpoints(config.Config{
		ListenAddress:      "127.0.0.1:0",
		AgentListenAddress: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("监听可用端口: %v", err)
	}
	defer adminListener.Close()
	defer agentListener.Close()
	if adminListener.Addr().String() == agentListener.Addr().String() {
		t.Fatal("管理面和 Agent 面不应占用同一监听地址")
	}
}

// TestListenServerEndpointsReportsAdminConflict 验证管理端口冲突会返回明确错误。
func TestListenServerEndpointsReportsAdminConflict(t *testing.T) {
	occupied := listenForTest(t)
	defer occupied.Close()

	_, _, err := listenServerEndpoints(config.Config{
		ListenAddress:      occupied.Addr().String(),
		AgentListenAddress: "127.0.0.1:0",
	})
	if err == nil || !strings.Contains(err.Error(), "管理面监听") {
		t.Fatalf("期望管理面端口冲突错误，实际为 %v", err)
	}
}

// TestListenServerEndpointsReleasesAdminWhenAgentConflicts 验证 Agent 端口冲突时已占用的管理端口会释放。
func TestListenServerEndpointsReleasesAdminWhenAgentConflicts(t *testing.T) {
	adminAddress := availableAddress(t)
	agentOccupied := listenForTest(t)
	defer agentOccupied.Close()

	_, _, err := listenServerEndpoints(config.Config{
		ListenAddress:      adminAddress,
		AgentListenAddress: agentOccupied.Addr().String(),
	})
	if err == nil || !strings.Contains(err.Error(), "Agent 面监听") {
		t.Fatalf("期望 Agent 面端口冲突错误，实际为 %v", err)
	}

	reopened, err := net.Listen("tcp", adminAddress)
	if err != nil {
		t.Fatalf("Agent 面监听失败后管理面端口未释放: %v", err)
	}
	_ = reopened.Close()
}

// listenForTest 创建测试使用的本地动态端口监听器。
func listenForTest(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("创建测试监听器: %v", err)
	}
	return listener
}

// availableAddress 返回一个刚释放的本地动态监听地址。
func availableAddress(t *testing.T) string {
	t.Helper()
	listener := listenForTest(t)
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("释放测试监听器: %v", err)
	}
	return address
}
