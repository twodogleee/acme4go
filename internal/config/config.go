// Package config 负责加载并校验 Acme4go 的运行参数。
//
// @author Andersen
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
)

const (
	// RoleServer 表示控制平面服务端角色。
	RoleServer = "server"
	// RoleAgent 表示远程文件部署节点角色。
	RoleAgent = "agent"
	// AgentHeartbeatInterval 是独立于部署轮询的固定心跳周期，属于节点协议约定。
	AgentHeartbeatInterval = 30 * time.Second
	// AgentOfflineAfter 容忍四个心跳周期未成功上报，Server 与 Agent 共享时序边界。
	AgentOfflineAfter = 4 * AgentHeartbeatInterval
	// defaultVersion 是未通过构建参数注入版本时的开发版本标识。
	defaultVersion = "0.1.0-dev"
	// defaultLocalDeploymentEnabled 表示 Server 默认启用本机证书执行器。
	defaultLocalDeploymentEnabled = true
	// defaultListenAddress 是管理面默认监听地址。
	defaultListenAddress = ":8080"
	// defaultAgentListenAddress 是 Agent 双向 TLS 接口默认监听地址。
	defaultAgentListenAddress = ":9443"
	// defaultPublicURL 是管理面默认本机访问地址。
	defaultPublicURL = "http://127.0.0.1:8080"
	// defaultAgentPublicURL 是 Agent 接口默认本机访问地址。
	defaultAgentPublicURL = "https://127.0.0.1:9443"
	// defaultDataDir 是数据库、密钥和节点身份的默认相对目录。
	defaultDataDir = "./data"
	// defaultDeploymentDir 是证书运行文件的默认相对目录。
	defaultDeploymentDir = "./deployments"
	// defaultAdminUsername 是首次初始化使用的默认管理员登录名。
	defaultAdminUsername = "admin"
	// defaultSessionTTL 是管理会话的默认有效期。
	defaultSessionTTL = 24 * time.Hour
	// defaultRenewBefore 是证书到期前触发续期的默认窗口。
	defaultRenewBefore = 30 * 24 * time.Hour
	// defaultSchedulerInterval 是 Server 后台维护任务的默认周期。
	defaultSchedulerInterval = time.Hour
	// defaultAgentPollInterval 是 Agent 拉取期望状态的默认周期。
	defaultAgentPollInterval = 30 * time.Second
	// defaultHistoryRetention 是订单、审计和部署历史版本的默认保留时间。
	defaultHistoryRetention = 730 * 24 * time.Hour
	// defaultDNSPropagationTimeout 是 DNS-01 传播检查的默认等待上限。
	defaultDNSPropagationTimeout = 3 * time.Minute
	// defaultDNSPollingInterval 是 DNS-01 传播检查的默认轮询间隔。
	defaultDNSPollingInterval = 5 * time.Second
	// defaultAgentName 是无法读取主机名时使用的节点名称。
	defaultAgentName = "acme-agent"
	// defaultLogLevel 是结构化运行日志的默认最低级别。
	defaultLogLevel = slog.LevelInfo
)

// Config 描述同一发布物在 Server 或 Agent 角色下的完整配置。
type Config struct {
	Role                   string        // Role 是当前进程的 server 或 agent 角色。
	Version                string        // Version 是应用发布版本。
	LogLevel               slog.Level    // LogLevel 是结构化运行日志允许输出的最低级别。
	LocalDeploymentEnabled bool          // LocalDeploymentEnabled 表示控制平面是否可直接写本机部署目录。
	ListenAddress          string        // ListenAddress 是管理端 HTTP 监听地址。
	AgentListenAddress     string        // AgentListenAddress 是 Agent 双向 TLS 接口监听地址。
	PublicURL              string        // PublicURL 是浏览器访问控制平面的公开基准地址。
	AgentPublicURL         string        // AgentPublicURL 是 Agent 访问双向 TLS 接口的公开基准地址。
	DataDir                string        // DataDir 是数据库、密钥和节点身份的持久化目录。
	DeploymentDir          string        // DeploymentDir 是证书文件部署根目录。
	MasterKey              []byte        // MasterKey 是敏感数据静态加密主密钥。
	AdminUsername          string        // AdminUsername 是首次初始化的管理员用户名。
	AdminPassword          string        // AdminPassword 是首次初始化的管理员密码。
	SessionTTL             time.Duration // SessionTTL 是管理会话有效期。
	RenewBefore            time.Duration // RenewBefore 是证书提前续期窗口。
	SchedulerInterval      time.Duration // SchedulerInterval 是控制平面维护调度间隔。
	AgentPollInterval      time.Duration // AgentPollInterval 是 Agent 拉取期望状态的间隔。
	HistoryRetention       time.Duration // HistoryRetention 是终态记录和部署历史版本的保留时间。
	AgentJoinURL           string        // AgentJoinURL 是首次注册使用的一次性加入链接。
	AgentName              string        // AgentName 是部署节点的可读名称。
	DNSPropagationTimeout  time.Duration // DNSPropagationTimeout 是 DNS-01 传播等待上限。
	DNSPollingInterval     time.Duration // DNSPollingInterval 是 DNS-01 查询间隔。
	TrustedProxyCIDRs      []string      // TrustedProxyCIDRs 是允许传递客户端来源信息的代理网段。
	AlertWebhookURL        string        // AlertWebhookURL 是失败事件通知的 HTTPS 接收地址。
	AlertWebhookSecret     string        // AlertWebhookSecret 是通知正文 HMAC-SHA256 签名密钥。
}

// Load 依次从 .env、进程环境变量和命令行角色加载配置，后者优先级更高。
func Load(role string) (Config, error) {
	if err := loadEnvFile(); err != nil {
		return Config{}, err
	}
	if role == "" {
		role = env("ACME_ROLE", RoleServer)
	}
	localDeploymentEnabled, err := boolean("ACME_LOCAL_DEPLOYMENT_ENABLED", defaultLocalDeploymentEnabled)
	if err != nil {
		return Config{}, err
	}
	configuredLogLevel, err := logLevel("ACME_LOG_LEVEL", defaultLogLevel)
	if err != nil {
		return Config{}, err
	}
	adminPassword, err := secret("ACME_ADMIN_PASSWORD")
	if err != nil {
		return Config{}, err
	}
	masterKey, err := secret("ACME_MASTER_KEY")
	if err != nil {
		return Config{}, err
	}
	alertWebhookSecret, err := secret("ACME_ALERT_WEBHOOK_SECRET")
	if err != nil {
		return Config{}, err
	}
	sessionTTL, err := duration("ACME_SESSION_TTL", defaultSessionTTL)
	if err != nil {
		return Config{}, err
	}
	renewBefore, err := duration("ACME_RENEW_BEFORE", defaultRenewBefore)
	if err != nil {
		return Config{}, err
	}
	schedulerInterval, err := duration("ACME_SCHEDULER_INTERVAL", defaultSchedulerInterval)
	if err != nil {
		return Config{}, err
	}
	agentPollInterval, err := duration("ACME_AGENT_POLL_INTERVAL", defaultAgentPollInterval)
	if err != nil {
		return Config{}, err
	}
	historyRetention, err := duration("ACME_HISTORY_RETENTION", defaultHistoryRetention)
	if err != nil {
		return Config{}, err
	}
	dnsPropagationTimeout, err := duration("ACME_DNS_PROPAGATION_TIMEOUT", defaultDNSPropagationTimeout)
	if err != nil {
		return Config{}, err
	}
	dnsPollingInterval, err := duration("ACME_DNS_POLLING_INTERVAL", defaultDNSPollingInterval)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Role:                   strings.ToLower(strings.TrimSpace(role)),
		Version:                env("ACME_VERSION", defaultVersion),
		LogLevel:               configuredLogLevel,
		LocalDeploymentEnabled: localDeploymentEnabled,
		ListenAddress:          env("ACME_LISTEN_ADDRESS", defaultListenAddress),
		AgentListenAddress:     env("ACME_AGENT_LISTEN_ADDRESS", defaultAgentListenAddress),
		PublicURL:              strings.TrimRight(env("ACME_PUBLIC_URL", defaultPublicURL), "/"),
		AgentPublicURL:         strings.TrimRight(env("ACME_AGENT_PUBLIC_URL", defaultAgentPublicURL), "/"),
		DataDir:                env("ACME_DATA_DIR", defaultDataDir),
		DeploymentDir:          env("ACME_DEPLOYMENT_DIR", defaultDeploymentDir),
		AdminUsername:          env("ACME_ADMIN_USERNAME", defaultAdminUsername),
		AdminPassword:          adminPassword,
		SessionTTL:             sessionTTL,
		RenewBefore:            renewBefore,
		SchedulerInterval:      schedulerInterval,
		AgentPollInterval:      agentPollInterval,
		HistoryRetention:       historyRetention,
		AgentJoinURL:           strings.TrimSpace(env("ACME_AGENT_JOIN_URL", "")),
		AgentName:              env("ACME_AGENT_NAME", hostname()),
		DNSPropagationTimeout:  dnsPropagationTimeout,
		DNSPollingInterval:     dnsPollingInterval,
		TrustedProxyCIDRs:      commaSeparated("ACME_TRUSTED_PROXY_CIDRS"),
		AlertWebhookURL:        strings.TrimSpace(env("ACME_ALERT_WEBHOOK_URL", "")),
		AlertWebhookSecret:     alertWebhookSecret,
	}
	if cfg.Role == RoleAgent {
		cfg.LocalDeploymentEnabled = false
	}
	cfg.MasterKey = []byte(masterKey)

	absDataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("解析数据目录: %w", err)
	}
	absDeploymentDir, err := filepath.Abs(cfg.DeploymentDir)
	if err != nil {
		return Config{}, fmt.Errorf("解析部署目录: %w", err)
	}
	cfg.DataDir = absDataDir
	cfg.DeploymentDir = absDeploymentDir
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 在启动任何监听器或后台任务前校验角色和关键安全参数。
func (c Config) Validate() error {
	switch c.Role {
	case RoleServer, RoleAgent:
	default:
		return fmt.Errorf("不支持的运行角色 %q，仅支持 server 或 agent", c.Role)
	}
	if c.Role == RoleAgent {
		if fileExists(filepath.Join(c.DataDir, "agent", "identity.json")) {
			return nil
		}
		if c.AgentJoinURL == "" {
			return errors.New("Agent 首次启动必须配置 ACME_AGENT_JOIN_URL")
		}
		if !strings.HasPrefix(c.AgentJoinURL, "acme4go://join/") {
			return errors.New("ACME_AGENT_JOIN_URL 必须使用 acme4go://join/ 格式")
		}
		return nil
	}
	if len(c.MasterKey) < 32 {
		return errors.New("ACME_MASTER_KEY 至少需要 32 字节，推荐通过 ACME_MASTER_KEY_FILE 提供")
	}
	if parsed, err := url.Parse(c.PublicURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("ACME_PUBLIC_URL 必须是完整 URL")
	}
	if parsed, err := url.Parse(c.AgentPublicURL); err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("ACME_AGENT_PUBLIC_URL 必须是 HTTPS URL")
	}
	if c.AdminPassword != "" && len(c.AdminPassword) < 12 {
		return errors.New("初始管理员密码至少需要 12 位")
	}
	if len(c.AdminUsername) > cryptoutil.MaxUsernameBytes || len(c.AdminPassword) > cryptoutil.MaxPasswordBytes {
		return errors.New("初始管理员用户名不能超过 256 字节，密码不能超过 1024 字节")
	}
	for _, cidr := range c.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("ACME_TRUSTED_PROXY_CIDRS 包含无效网段 %q", cidr)
		}
	}
	if c.AlertWebhookURL != "" {
		parsed, err := url.Parse(c.AlertWebhookURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) {
			return errors.New("ACME_ALERT_WEBHOOK_URL 必须是 HTTPS URL；仅回环地址允许 HTTP")
		}
		if len(c.AlertWebhookSecret) < 32 {
			return errors.New("启用通知时 ACME_ALERT_WEBHOOK_SECRET 至少需要 32 字节")
		}
	}
	return nil
}

// isLoopbackHost 判断主机名是否明确指向当前节点回环地址。
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// commaSeparated 将逗号分隔的环境变量解析为去除空白的非空值列表。
func commaSeparated(name string) []string {
	parts := strings.Split(os.Getenv(name), ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// env 返回去除首尾空白的环境变量值，缺失或为空时使用默认值。
func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return fallback
}

// secret 优先从同名的 _FILE 文件读取秘密；显式配置的文件无法读取时立即失败。
func secret(name string) (string, error) {
	if path := strings.TrimSpace(os.Getenv(name + "_FILE")); path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("读取 %s_FILE 指定的秘密文件: %w", name, err)
		}
		return strings.TrimSpace(string(content)), nil
	}
	return strings.TrimSpace(os.Getenv(name)), nil
}

// duration 解析正数时长环境变量，并拒绝显式提供的无效配置。
func duration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s 必须是正数时长，例如 30s 或 24h", name)
	}
	return parsed, nil
}

// boolean 解析布尔环境变量，并对非布尔文本返回明确错误。
func boolean(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s 必须是 true 或 false", name)
	}
	return parsed, nil
}

// logLevel 解析受支持的结构化日志级别，并拒绝可能掩盖配置错误的未知文本。
func logLevel(name string, fallback slog.Level) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "":
		return fallback, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return fallback, fmt.Errorf("%s 必须是 debug、info、warn 或 error", name)
	}
}

// hostname 返回当前主机名，读取失败时使用稳定的 Agent 默认名称。
func hostname() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return defaultAgentName
	}
	return name
}

// fileExists 判断给定路径是否存在且为普通文件或其他非目录节点。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
