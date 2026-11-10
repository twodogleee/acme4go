// Command acme4go 启动 server 或 agent 两种运行角色。
//
// @author Andersen
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/twodogleee/acme4go/internal/agent"
	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/control"
)

// version 是构建时通过链接参数覆盖的应用版本。
var version = "1.0.0"

// main 解析运行角色、加载配置并启动对应进程入口。
func main() {
	logger := newLogger(slog.LevelInfo)
	role := ""
	if len(os.Args) > 1 {
		role = strings.ToLower(strings.TrimSpace(os.Args[1]))
		if role == "version" || role == "--version" {
			fmt.Println(version)
			return
		}
		if role == "healthcheck" {
			if err := runHealthcheck(); err != nil {
				logger.Error("健康检查失败", "error", err)
				os.Exit(1)
			}
			return
		}
		if role == "backup" || role == "doctor" {
			if os.Getenv("ACME_VERSION") == "" {
				_ = os.Setenv("ACME_VERSION", version)
			}
			cfg, err := config.Load(config.RoleServer)
			if err != nil {
				logger.Error("运维命令配置校验失败", "error", err)
				os.Exit(1)
			}
			if role == "backup" {
				if len(os.Args) != 3 {
					logger.Error("备份命令需要目标文件", "usage", "acme4go backup /secure/path/acme4go.tar.gz")
					os.Exit(2)
				}
				err = runBackup(cfg, os.Args[2])
			} else {
				err = runDoctor(cfg)
			}
			if err != nil {
				logger.Error("运维命令失败", "command", role, "error", err)
				os.Exit(1)
			}
			return
		}
	}
	if os.Getenv("ACME_VERSION") == "" {
		_ = os.Setenv("ACME_VERSION", version)
	}
	cfg, err := config.Load(role)
	if err != nil {
		logger.Error("配置校验失败", "error", err)
		os.Exit(1)
	}
	logger = newLogger(cfg.LogLevel)
	logConfiguration(logger, cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if cfg.Role == config.RoleAgent {
		if err := agent.Run(ctx, cfg, logger); err != nil {
			logger.Error("Agent 退出", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := runServer(ctx, cfg, logger); err != nil {
		logger.Error("Server 退出", "error", err)
		os.Exit(1)
	}
}

// newLogger 创建统一写入标准输出的 JSON 结构化日志记录器。
func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// logConfiguration 按运行角色输出脱敏后的最终生效配置摘要。
func logConfiguration(logger *slog.Logger, cfg config.Config) {
	commonAttributes := []any{
		"version", cfg.Version,
		"role", cfg.Role,
		"log_level", cfg.LogLevel.String(),
		"data_dir", cfg.DataDir,
		"deployment_dir", cfg.DeploymentDir,
		"agent_poll_interval", cfg.AgentPollInterval.String(),
	}
	if cfg.Role == config.RoleAgent {
		logger.Info("运行配置已加载", append(commonAttributes,
			"agent_name", cfg.AgentName,
			"join_configured", cfg.AgentJoinURL != "",
		)...)
		return
	}
	logger.Info("运行配置已加载", append(commonAttributes,
		"local_deployment_enabled", cfg.LocalDeploymentEnabled,
		"listen_address", cfg.ListenAddress,
		"agent_listen_address", cfg.AgentListenAddress,
		"public_url", summarizeURL(cfg.PublicURL),
		"agent_public_url", summarizeURL(cfg.AgentPublicURL),
		"session_ttl", cfg.SessionTTL.String(),
		"renew_before", cfg.RenewBefore.String(),
		"scheduler_interval", cfg.SchedulerInterval.String(),
		"history_retention", cfg.HistoryRetention.String(),
		"alert_webhook_enabled", cfg.AlertWebhookURL != "",
		"dns_propagation_timeout", cfg.DNSPropagationTimeout.String(),
		"dns_polling_interval", cfg.DNSPollingInterval.String(),
		"trusted_proxy_count", len(cfg.TrustedProxyCIDRs),
	)...)
}

// summarizeURL 仅保留公开地址的协议和主机，避免启动日志意外记录用户信息或查询参数。
func summarizeURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "invalid"
	}
	return parsed.Scheme + "://" + parsed.Host
}

// runHealthcheck 从容器内部验证管理面 readiness，供无 Shell 的 distroless 镜像调用。
func runHealthcheck() error {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://127.0.0.1:8080/readyz")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness 返回 HTTP %d", response.StatusCode)
	}
	return nil
}

// runServer 原子占用双监听端口并协调管理面、Agent 面和后台服务的生命周期。
func runServer(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	adminListener, agentListener, err := listenServerEndpoints(cfg)
	if err != nil {
		return err
	}
	defer adminListener.Close()
	defer agentListener.Close()

	services, err := app.NewServices(cfg, logger)
	if err != nil {
		return err
	}
	defer services.Close()
	services.StartBackground(ctx)
	handlers := control.New(services, logger)
	adminServer := &http.Server{
		Addr: cfg.ListenAddress, Handler: handlers.AdminHandler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute,
	}
	agentServer := &http.Server{
		Addr: cfg.AgentListenAddress, Handler: handlers.AgentHandler(), TLSConfig: handlers.AgentTLSConfig(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute,
	}
	errorsChannel := make(chan error, 2)
	// 管理面服务协程将非正常退出错误送回主生命周期。
	go func() {
		logger.Info("管理面已启动", "address", adminListener.Addr().String(), "role", cfg.Role, "local_executor", cfg.LocalDeploymentEnabled)
		if err := adminServer.Serve(adminListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsChannel <- fmt.Errorf("管理面: %w", err)
		}
	}()
	// Agent 面服务协程使用内存 TLS 配置启动双向认证接口。
	go func() {
		logger.Info("Agent 面已启动", "address", agentListener.Addr().String(), "url", cfg.AgentPublicURL)
		if err := agentServer.ServeTLS(agentListener, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsChannel <- fmt.Errorf("Agent 面: %w", err)
		}
	}()
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errorsChannel:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = agentServer.Shutdown(shutdownCtx)
	shutdownErr := adminServer.Shutdown(shutdownCtx)
	if runErr != nil {
		return runErr
	}
	return shutdownErr
}

// listenServerEndpoints 在启动后台任务前同时占用管理面和 Agent 面端口，确保启动过程具备原子性。
func listenServerEndpoints(cfg config.Config) (net.Listener, net.Listener, error) {
	adminListener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("管理面监听 %s: %w", cfg.ListenAddress, err)
	}
	agentListener, err := net.Listen("tcp", cfg.AgentListenAddress)
	if err != nil {
		_ = adminListener.Close()
		return nil, nil, fmt.Errorf("Agent 面监听 %s: %w", cfg.AgentListenAddress, err)
	}
	return adminListener, agentListener, nil
}
