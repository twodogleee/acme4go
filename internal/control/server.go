// Package control 提供管理面与 Agent 面两个独立 HTTP 边界。
//
// @author Andersen
package control

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/store"
	"github.com/twodogleee/acme4go/internal/web"
)

// sessionCookie 是管理端 HttpOnly 会话 Cookie 名称。
const sessionCookie = "acme4go_session"

// contextKey 使用私有类型隔离 HTTP 上下文中的身份数据键。
type contextKey string

const (
	// userKey 保存已认证管理员。
	userKey contextKey = "user"
	// csrfHashKey 保存当前会话 CSRF 哈希。
	csrfHashKey contextKey = "csrf_hash"
	// agentKey 保存已认证 Agent。
	agentKey contextKey = "agent"
)

// Server 将领域服务映射为严格解码、统一响应和安全中间件保护的 HTTP API。
type Server struct {
	services        *app.Services           // services 是 HTTP 层调用的领域服务集合。
	logger          *slog.Logger            // logger 记录请求和异常日志。
	loginMu         sync.Mutex              // loginMu 保护登录失败预算。
	loginAttempts   map[string]loginAttempt // loginAttempts 按来源和用户名保存失败状态。
	loginSources    map[string]rateWindow   // loginSources 独立限制单来源的全部登录请求，变换用户名不能绕过。
	loginPruneAt    time.Time               // loginPruneAt 限制缓存全量清理最多每分钟一次。
	loginSlots      chan struct{}           // loginSlots 限制高成本密码校验并发数。
	trustedProxy    []*net.IPNet            // trustedProxy 是允许提供转发来源头的代理网段。
	agentReportMu   sync.Mutex              // agentReportMu 保护部署上报限流窗口。
	agentReports    map[string]rateWindow   // agentReports 按节点保存部署上报窗口。
	agentRequestMu  sync.Mutex              // agentRequestMu 保护 Agent 总请求限流窗口。
	agentRequests   map[string]rateWindow   // agentRequests 按节点保存总请求窗口。
	agentAuthSlots  chan struct{}           // agentAuthSlots 仅限制身份查询并发，在读取业务请求体前释放。
	agentDataBudget *agentConcurrency       // agentDataBudget 隔离普通请求的全局与单节点并发。
	agentLiveBudget *agentConcurrency       // agentLiveBudget 为心跳和身份续签保留独立并发额度。
	enrollmentMu    sync.Mutex              // enrollmentMu 保护公开注册来源限流窗口。
	enrollments     map[string]rateWindow   // enrollments 按来源 IP 保存注册请求窗口。
	enrollmentSlots chan struct{}           // enrollmentSlots 隔离公开注册的并发预算。
}

// loginAttempt 记录一个来源与用户名组合在固定窗口内的登录预算。
type loginAttempt struct {
	failures     int       // failures 是当前窗口已预留的失败次数。
	windowStart  time.Time // windowStart 是失败计数窗口起点。
	blockedUntil time.Time // blockedUntil 是禁止继续登录的截止时间。
}

// rateWindow 记录 Agent 接口在单个固定时间窗口内的请求数量。
type rateWindow struct {
	count   int       // count 是窗口内已接受的请求数。
	started time.Time // started 是固定窗口开始时间。
}

// listPage 是管理端大列表共用的页码分页响应。
type listPage struct {
	Items    any                      `json:"items"`             // Items 是当前页记录。
	Total    int64                    `json:"total"`             // Total 是全部记录数量。
	Page     int                      `json:"page"`              // Page 是从一开始的当前页码。
	PageSize int                      `json:"pageSize"`          // PageSize 是单页最大记录数。
	Summary  *store.CertificateCounts `json:"summary,omitempty"` // Summary 是证书筛选范围的完整统计。
}

// New 创建带登录保护、可信代理和 Agent 资源限额的 HTTP 服务。
func New(services *app.Services, logger *slog.Logger) *Server {
	return &Server{
		services: services, logger: logger,
		loginAttempts: make(map[string]loginAttempt), loginSources: make(map[string]rateWindow), loginSlots: make(chan struct{}, 4),
		trustedProxy: parseTrustedProxyNetworks(services.Config.TrustedProxyCIDRs),
		agentReports: make(map[string]rateWindow), agentRequests: make(map[string]rateWindow),
		agentAuthSlots: make(chan struct{}, 8), agentDataBudget: newAgentConcurrency(6, 2), agentLiveBudget: newAgentConcurrency(2, 1),
		enrollments: make(map[string]rateWindow), enrollmentSlots: make(chan struct{}, 2),
	}
}

// AdminHandler 返回管理 API、健康检查和 Vue SPA Handler。
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	// 管理面存活检查回调仅确认 HTTP 进程可以响应。
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		s.ok(w, map[string]string{"status": "UP"}, "运行正常")
	})
	// 管理面就绪检查回调同时验证数据库和 Agent TLS 证书。
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.services.Store.Ready(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "data": nil, "message": "数据库尚未就绪", "code": "NOT_READY", "timestamp": time.Now().UTC()})
			return
		}
		expiresAt, err := s.services.PKI.ServerCertificateExpiry()
		if err != nil || !expiresAt.After(time.Now()) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "data": nil, "message": "Agent TLS 服务尚未就绪", "code": "NOT_READY", "timestamp": time.Now().UTC()})
			return
		}
		s.ok(w, map[string]string{"status": "READY"}, "服务已就绪")
	})
	mux.HandleFunc("POST /api/auth/login", s.login)
	// 登录方法兜底回调对非 POST 请求返回明确的 405 响应。
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, &app.Error{Status: http.StatusMethodNotAllowed, Code: "METHOD_NOT_ALLOWED", Message: "登录接口仅支持 POST 请求，请通过管理端登录页面访问"})
	})

	authenticated := http.NewServeMux()
	authenticated.HandleFunc("GET /api/auth/me", s.me)
	authenticated.HandleFunc("POST /api/auth/logout", s.logout)
	authenticated.HandleFunc("POST /api/auth/password", s.changePassword)
	authenticated.HandleFunc("GET /api/dashboard", s.dashboard)
	authenticated.HandleFunc("GET /api/orders", s.listOrders)
	authenticated.HandleFunc("GET /api/orders/{id}", s.orderDetail)
	authenticated.HandleFunc("POST /api/orders", s.createOrder)
	authenticated.HandleFunc("POST /api/orders/{id}/retry", s.retryOrder)
	authenticated.HandleFunc("GET /api/certificates", s.listCertificates)
	authenticated.HandleFunc("GET /api/certificate-series", s.listCertificateSeries)
	authenticated.HandleFunc("PUT /api/certificate-series/{id}", s.updateCertificateSeries)
	authenticated.HandleFunc("GET /api/certificates/{id}/download", s.downloadCertificate)
	authenticated.HandleFunc("GET /api/audit-logs", s.listAuditLogs)
	authenticated.HandleFunc("GET /api/credentials", s.listCredentials)
	authenticated.HandleFunc("POST /api/credentials", s.saveCredential)
	authenticated.HandleFunc("PUT /api/credentials/{id}", s.saveCredential)
	authenticated.HandleFunc("DELETE /api/credentials/{id}", s.deleteCredential)
	authenticated.HandleFunc("GET /api/dns-credentials", s.listDNSCredentials)
	authenticated.HandleFunc("POST /api/dns-credentials", s.createDNSCredential)
	authenticated.HandleFunc("PUT /api/dns-credentials/{id}", s.updateDNSCredential)
	authenticated.HandleFunc("DELETE /api/dns-credentials/{id}", s.deleteDNSCredential)
	authenticated.HandleFunc("POST /api/dns-credentials/{id}/test", s.testDNSCredential)
	authenticated.HandleFunc("GET /api/agents", s.listAgents)
	authenticated.HandleFunc("POST /api/agents/enrollments", s.createEnrollment)
	authenticated.HandleFunc("DELETE /api/agents/{id}", s.revokeAgent)
	authenticated.HandleFunc("GET /api/deployments/targets", s.listTargets)
	authenticated.HandleFunc("POST /api/deployments/targets", s.createTarget)
	authenticated.HandleFunc("PUT /api/deployments/targets/{id}", s.updateTarget)
	authenticated.HandleFunc("DELETE /api/deployments/targets/{id}", s.deleteTarget)
	authenticated.HandleFunc("POST /api/deployments/targets/{id}/sync", s.deployTarget)
	authenticated.HandleFunc("GET /api/deployments/records", s.listDeploymentRecords)
	authenticated.HandleFunc("GET /api/system/info", s.systemInfo)
	authenticated.HandleFunc("GET /api/cloud/products", s.cloudProducts)
	authenticated.HandleFunc("GET /api/credentials/{id}/cloud-certificates", s.listCloudCertificates)
	authenticated.HandleFunc("GET /api/cloud/plans", s.listCloudPlans)
	authenticated.HandleFunc("POST /api/cloud/plans", s.saveCloudPlan)
	authenticated.HandleFunc("PUT /api/cloud/plans/{id}", s.saveCloudPlan)
	authenticated.HandleFunc("DELETE /api/cloud/plans/{id}", s.deleteCloudPlan)
	authenticated.HandleFunc("POST /api/cloud/plans/{id}/run", s.runCloudPlan)
	authenticated.HandleFunc("GET /api/cloud/jobs", s.listCloudJobs)
	authenticated.HandleFunc("GET /api/cloud/jobs/{id}", s.cloudJobDetail)
	authenticated.HandleFunc("GET /api/alerts", s.listAlerts)
	authenticated.HandleFunc("POST /api/alerts/test", s.testAlert)
	authenticated.HandleFunc("POST /api/alerts/{id}/retry", s.retryAlert)
	mux.Handle("/api/", s.requireAuth(authenticated))
	mux.Handle("/", s.spaHandler())
	return s.commonMiddleware(mux)
}

// AgentHandler 返回只在内部 TLS 监听器上运行的注册、拉取和回报 API。
func (s *Server) AgentHandler() http.Handler {
	mux := http.NewServeMux()
	// Agent 通道存活检查回调不要求客户端证书。
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		s.ok(w, map[string]string{"status": "UP"}, "Agent 通道正常")
	})
	mux.Handle("POST /agent/v1/enroll", s.limitEnrollment(http.HandlerFunc(s.enrollAgent)))
	protected := http.NewServeMux()
	protected.HandleFunc("GET /agent/v1/desired-state", s.desiredState)
	protected.HandleFunc("POST /agent/v1/identity/renew", s.renewAgentIdentity)
	protected.HandleFunc("POST /agent/v1/heartbeat", s.agentHeartbeat)
	protected.HandleFunc("GET /agent/v1/certificates/{id}", s.agentCertificate)
	protected.HandleFunc("POST /agent/v1/deployments/result", s.agentDeploymentResult)
	mux.Handle("/agent/", s.requireAgent(s.limitAgentRate(s.limitAgentConcurrency(protected))))
	return s.commonMiddleware(limitAgentBodyRead(mux))
}

// limitEnrollment 按可信客户端 IP 限速并隔离公开注册接口的并发预算。
func (s *Server) limitEnrollment(next http.Handler) http.Handler {
	// 注册限制回调在解析昂贵 CSR 前检查来源预算和独立并发槽位。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.allowEnrollment(s.loginClient(r), time.Now()) {
			writeError(w, &app.Error{Status: http.StatusTooManyRequests, Code: "ENROLLMENT_RATE_LIMITED", Message: "Agent 注册请求过于频繁"})
			return
		}
		select {
		case s.enrollmentSlots <- struct{}{}:
			// 注册请求结束回调释放独立并发槽位。
			defer func() { <-s.enrollmentSlots }()
			next.ServeHTTP(w, r)
		default:
			writeError(w, &app.Error{Status: http.StatusServiceUnavailable, Code: "ENROLLMENT_BUSY", Message: "Agent 注册通道繁忙，请稍后重试"})
		}
	})
}

// allowEnrollment 将单个来源的公开注册请求限制为每十五分钟十次。
func (s *Server) allowEnrollment(client string, now time.Time) bool {
	s.enrollmentMu.Lock()
	defer s.enrollmentMu.Unlock()
	if len(s.enrollments) >= 4096 {
		for key, window := range s.enrollments {
			if now.Sub(window.started) >= 15*time.Minute {
				delete(s.enrollments, key)
			}
		}
	}
	if _, exists := s.enrollments[client]; !exists && len(s.enrollments) >= 4096 {
		return false
	}
	window := s.enrollments[client]
	if window.started.IsZero() || now.Sub(window.started) >= 15*time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= 10 {
		return false
	}
	window.count++
	s.enrollments[client] = window
	return true
}

// limitAgentRate 对已认证 Agent 的全部 API 请求设置宽松上限，限制失控节点的持续影响。
func (s *Server) limitAgentRate(next http.Handler) http.Handler {
	// 速率限制回调按认证后的 Agent 身份执行总量检查。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent := currentAgent(r)
		if !s.allowAgentRequest(agent.ID, time.Now()) {
			writeError(w, &app.Error{Status: http.StatusTooManyRequests, Code: "AGENT_RATE_LIMITED", Message: "Agent 请求过于频繁"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// commonMiddleware 统一设置安全响应头、恢复 Panic 并记录请求耗时。
func (s *Server) commonMiddleware(next http.Handler) http.Handler {
	// 通用中间件回调为每个 HTTP 请求设置安全边界。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		// 请求收尾回调恢复 Panic 并记录完整处理耗时。
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("HTTP 请求发生 Panic", "method", r.Method, "path", r.URL.Path)
				writeError(w, &app.Error{Status: 500, Code: "INTERNAL_ERROR", Message: "服务器无法完成请求"})
			}
			s.logger.Info("HTTP 请求", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(w, r)
	})
}

// requireAuth 验证管理会话，并为非安全方法强制同源与 CSRF 校验。
func (s *Server) requireAuth(next http.Handler) http.Handler {
	// 管理认证回调加载会话并将管理员身份注入请求上下文。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, app.Unauthorized("尚未登录"))
			return
		}
		user, csrfHash, err := s.services.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, err)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !app.VerifyCSRF(csrfHash, r.Header.Get("X-CSRF-Token")) || !s.sameOrigin(r) {
				writeError(w, app.Forbidden("请求来源或 CSRF 令牌无效"))
				return
			}
		}
		ctx := context.WithValue(r.Context(), userKey, user)
		ctx = context.WithValue(ctx, csrfHashKey, csrfHash)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireAgent 将客户端证书身份绑定到仍启用的 Agent 记录。
func (s *Server) requireAgent(next http.Handler) http.Handler {
	// Agent 认证回调验证客户端证书并将节点身份注入请求上下文。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			writeError(w, app.Forbidden("必须提供 Agent 客户端证书"))
			return
		}
		certificate := r.TLS.PeerCertificates[0]
		now := time.Now()
		if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
			writeError(w, app.Forbidden("Agent 客户端证书尚未生效或已过期，请更新身份"))
			return
		}
		presentedSerial := strings.ToLower(certificate.SerialNumber.Text(16))
		agent, err := s.authenticateAgentBounded(r.Context(), presentedSerial, certificate.Subject.CommonName)
		if err != nil {
			writeError(w, err)
			return
		}
		agent.AuthenticatedSerial = presentedSerial
		ctx := context.WithValue(r.Context(), agentKey, agent)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sameOrigin 接受配置公开地址或当前请求 Host 的同源 Origin，并仅信任显式代理的协议头。
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	got, err := url.Parse(origin)
	if err != nil || got.Scheme == "" || got.Host == "" {
		return false
	}
	configured, configuredErr := url.Parse(s.services.Config.PublicURL)
	if configuredErr == nil && sameURLOrigin(configured, got) {
		return true
	}
	requestScheme := "http"
	if r.TLS != nil {
		requestScheme = "https"
	} else if s.isTrustedProxy(remoteIP(r.RemoteAddr)) {
		forwardedScheme := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
		if forwardedScheme == "http" || forwardedScheme == "https" {
			requestScheme = forwardedScheme
		}
	}
	return strings.EqualFold(requestScheme, got.Scheme) && strings.EqualFold(r.Host, got.Host)
}

// sameURLOrigin 比较两个 URL 的协议和主机部分，不比较路径与查询参数。
func sameURLOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

// login 执行并发限制、失败预算校验并创建安全会话 Cookie。
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	source := s.loginClient(r)
	if !s.allowLoginSource(source, time.Now()) {
		writeError(w, &app.Error{Status: http.StatusTooManyRequests, Code: "LOGIN_RATE_LIMITED", Message: "登录请求过多，请稍后重试"})
		return
	}
	var input struct {
		Username string // Username 是管理员登录名。
		Password string // Password 是管理员明文登录密码。
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := app.ValidateLoginInput(input.Username, input.Password); err != nil {
		writeError(w, err)
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		// 登录请求结束回调释放高成本密码校验槽位。
		defer func() { <-s.loginSlots }()
	default:
		writeError(w, &app.Error{Status: http.StatusTooManyRequests, Code: "LOGIN_BUSY", Message: "登录请求过多，请稍后重试"})
		return
	}
	client := cryptoutil.TokenHash(source + "\x00" + strings.ToLower(strings.TrimSpace(input.Username)))
	if !s.beginLogin(client, time.Now()) {
		writeError(w, &app.Error{Status: http.StatusTooManyRequests, Code: "LOGIN_RATE_LIMITED", Message: "登录失败次数过多，请稍后重试"})
		return
	}
	result, err := s.services.Login(r.Context(), input.Username, input.Password)
	if err != nil {
		s.finishLoginFailure(client, time.Now())
		writeError(w, err)
		return
	}
	s.clearLoginFailures(client)
	secure := strings.HasPrefix(strings.ToLower(s.services.Config.PublicURL), "https://")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: result.Token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, Expires: result.ExpiresAt, MaxAge: int(time.Until(result.ExpiresAt).Seconds())})
	s.ok(w, result, "登录成功")
}

// loginClient 返回用于登录限流的客户端地址，非可信来源提供的转发头会被忽略。
func (s *Server) loginClient(r *http.Request) string {
	remote := remoteIP(r.RemoteAddr)
	if remote != nil {
		if s.isTrustedProxy(remote) {
			if forwarded := s.forwardedClientIP(r.Header.Get("X-Forwarded-For")); forwarded != "" {
				return forwarded
			}
			if realIP := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); realIP != nil {
				return realIP.String()
			}
		}
		return remote.String()
	}
	return r.RemoteAddr
}

// remoteIP 从带端口或纯 IP 的远端地址中提取规范化 IP。
func remoteIP(remoteAddress string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(strings.TrimSpace(remoteAddress))
}

// beginLogin 在密码计算前原子预留一次失败预算，阻止并发请求同时越过上限。
func (s *Server) beginLogin(client string, now time.Time) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.pruneLoginAttempts(now)
	attempt, exists := s.loginAttempts[client]
	if !exists && len(s.loginAttempts) >= maxLoginEntries {
		return false
	}
	if attempt.blockedUntil.After(now) {
		return false
	}
	if attempt.windowStart.IsZero() || now.Sub(attempt.windowStart) > 15*time.Minute {
		attempt = loginAttempt{windowStart: now}
	}
	if attempt.failures >= 5 {
		attempt.blockedUntil = now.Add(15 * time.Minute)
		s.loginAttempts[client] = attempt
		return false
	}
	attempt.failures++
	s.loginAttempts[client] = attempt
	return true
}

// finishLoginFailure 在一次密码验证失败后按阈值设置封禁时间。
func (s *Server) finishLoginFailure(client string, now time.Time) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	attempt, exists := s.loginAttempts[client]
	if !exists {
		return
	}
	if attempt.failures >= 5 {
		attempt.blockedUntil = now.Add(15 * time.Minute)
	}
	s.loginAttempts[client] = attempt
}

// pruneLoginAttempts 每分钟最多清理一次过期预算；容量限制由插入入口执行，不能驱逐有效封禁。
func (s *Server) pruneLoginAttempts(now time.Time) {
	if !s.loginPruneAt.IsZero() && now.Sub(s.loginPruneAt) < time.Minute {
		return
	}
	s.loginPruneAt = now
	for client, attempt := range s.loginAttempts {
		if !attempt.blockedUntil.After(now) && now.Sub(attempt.windowStart) > 15*time.Minute {
			delete(s.loginAttempts, client)
		}
	}
	for source, window := range s.loginSources {
		if now.Sub(window.started) >= time.Minute {
			delete(s.loginSources, source)
		}
	}
}

// isTrustedProxy 判断 IP 是否属于配置的可信代理网段。
func (s *Server) isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, network := range s.trustedProxy {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// parseTrustedProxyNetworks 将已校验的 CIDR 文本转换为网络对象。
func parseTrustedProxyNetworks(values []string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		if _, network, err := net.ParseCIDR(value); err == nil {
			networks = append(networks, network)
		}
	}
	return networks
}

// forwardedClientIP 从右向左跳过可信代理，返回最靠近代理链的非可信客户端地址。
func (s *Server) forwardedClientIP(value string) string {
	parts := strings.Split(value, ",")
	for index := len(parts) - 1; index >= 0; index-- {
		if ip := net.ParseIP(strings.TrimSpace(parts[index])); ip != nil {
			if s.isTrustedProxy(ip) {
				continue
			}
			return ip.String()
		}
	}
	return ""
}

// clearLoginFailures 在成功登录后移除对应失败预算。
func (s *Server) clearLoginFailures(client string) {
	s.loginMu.Lock()
	delete(s.loginAttempts, client)
	s.loginMu.Unlock()
}

// me 返回当前登录管理员信息。
func (s *Server) me(w http.ResponseWriter, r *http.Request) { s.ok(w, currentUser(r), "获取成功") }

// logout 删除服务端会话并清理浏览器 Cookie。
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(sessionCookie)
	if cookie != nil {
		_ = s.services.Logout(r.Context(), cookie.Value)
	}
	user := currentUser(r)
	s.services.RecordAudit(r.Context(), &user.ID, "LOGOUT", "USER", strconv.FormatInt(user.ID, 10), "管理员退出登录")
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, Expires: time.Unix(0, 0)})
	s.ok(w, nil, "已退出登录")
}

// changePassword 修改当前管理员密码并强制重新登录。
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CurrentPassword string // CurrentPassword 是用于二次校验的现有密码。
		NewPassword     string // NewPassword 是需要保存的新密码。
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	if err := s.services.ChangePassword(r.Context(), currentUser(r), input.CurrentPassword, input.NewPassword); err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	s.ok(w, nil, "密码已修改，请重新登录")
}

// dashboard 返回控制平面运行指标。
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	value, err := s.services.Store.Dashboard(r.Context(), s.services.Config.Role, s.services.Config.LocalDeploymentEnabled, s.services.Config.RenewBefore)
	s.respond(w, value, "获取成功", err)
}

// listOrders 返回证书订单列表。
func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) {
	page, pageSize, err := listPageQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	filter, err := resourceListFilter(r, "QUEUED", "RUNNING", "SUCCESS", "FAILED")
	if err != nil {
		writeError(w, err)
		return
	}
	values, total, err := s.services.Store.ListOrdersPage(r.Context(), page, pageSize, filter)
	s.respond(w, listPage{Items: values, Total: total, Page: page, PageSize: pageSize}, "获取成功", err)
}

// createOrder 接收并提交新的证书签发订单。
func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	var input app.CreateOrderInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	user := currentUser(r)
	value, err := s.services.CreateOrder(r.Context(), &user.ID, input)
	s.respond(w, value, "订单已提交", err)
}

// retryOrder 根据失败订单创建新的签发任务。
func (s *Server) retryOrder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.RetryOrder(r.Context(), currentUser(r).ID, id)
	s.respond(w, value, "重试订单已提交", err)
}

// listCertificates 返回证书库元数据列表。
func (s *Server) listCertificates(w http.ResponseWriter, r *http.Request) {
	page, pageSize, err := listPageQuery(r)
	if err != nil {
		writeError(w, err)
		return
	}
	filter, err := resourceListFilter(r, "VALID", "EXPIRING", "EXPIRED")
	if err != nil {
		writeError(w, err)
		return
	}
	values, total, err := s.services.Store.ListCertificatesPage(r.Context(), s.services.Config.RenewBefore, page, pageSize, filter)
	if err != nil {
		s.respond(w, nil, "获取失败", err)
		return
	}
	summary, err := s.services.Store.CertificateSummary(r.Context(), s.services.Config.RenewBefore, filter)
	s.respond(w, listPage{Items: values, Total: total, Page: page, PageSize: pageSize, Summary: &summary}, "获取成功", err)
}

// listCertificateSeries 返回可供订单和部署目标引用的稳定证书系列。
func (s *Server) listCertificateSeries(w http.ResponseWriter, r *http.Request) {
	values, err := s.services.Store.ListCertificateSeries(r.Context(), s.services.Config.RenewBefore)
	s.respond(w, values, "获取成功", err)
}

// updateCertificateSeries 显式修改系列续期设置。
func (s *Server) updateCertificateSeries(w http.ResponseWriter, r *http.Request) {
	var input app.CertificateSeriesInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.UpdateCertificateSeries(r.Context(), currentUser(r).ID, r.PathValue("id"), input)
	s.respond(w, value, "更新成功", err)
}

// listAuditLogs 分页返回管理员操作审计记录。
func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	cursor, err := optionalPositiveQueryInt64(r, "cursor")
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := optionalPositiveQueryInt(r, "limit", 50)
	if err != nil {
		writeError(w, err)
		return
	}
	if limit > 200 {
		writeError(w, app.BadRequest("limit 不能超过 200"))
		return
	}
	value, err := s.services.Store.ListAuditLogs(r.Context(), cursor, limit, strings.TrimSpace(r.URL.Query().Get("action")), strings.TrimSpace(r.URL.Query().Get("resourceType")))
	s.respond(w, value, "获取成功", err)
}

// downloadCertificate 按类型下载证书材料，私钥额外要求 CSRF 与同源验证。
func (s *Server) downloadCertificate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	certificate, bundle, err := s.services.CertificateBundle(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	var content []byte
	suffix := "certificate.pem"
	switch r.URL.Query().Get("type") {
	case "CERTIFICATE":
		content = bundle.Certificate
	case "FULL_CHAIN":
		content, suffix = bundle.FullChain, "fullchain.pem"
	case "PRIVATE_KEY":
		csrfHash, _ := r.Context().Value(csrfHashKey).(string)
		if !app.VerifyCSRF(csrfHash, r.Header.Get("X-CSRF-Token")) || !s.sameOrigin(r) {
			writeError(w, app.Forbidden("下载私钥需要有效的 CSRF 令牌"))
			return
		}
		content, suffix = bundle.PrivateKey, "private-key.pem"
	default:
		writeError(w, app.BadRequest("下载类型无效"))
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s"`, strings.ReplaceAll(certificate.CommonName, "*", "wildcard"), suffix))
	w.Header().Set("Cache-Control", "no-store")
	downloadType := r.URL.Query().Get("type")
	user := currentUser(r)
	s.services.RecordAudit(r.Context(), &user.ID, "DOWNLOAD", "CERTIFICATE_MATERIAL", strconv.FormatInt(id, 10), certificate.SeriesCode+" · "+downloadType)
	_, _ = w.Write(content)
}

// listDNSCredentials 返回脱敏 DNS 凭据列表。
func (s *Server) listDNSCredentials(w http.ResponseWriter, r *http.Request) {
	values, err := s.services.ListDNSCredentials(r.Context())
	s.respond(w, values, "获取成功", err)
}

// createDNSCredential 创建加密 DNS 凭据。
func (s *Server) createDNSCredential(w http.ResponseWriter, r *http.Request) {
	var input app.DNSCredentialInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.CreateDNSCredential(r.Context(), currentUser(r).ID, input)
	s.respond(w, value, "创建成功", err)
}

// updateDNSCredential 更新指定 DNS 凭据。
func (s *Server) updateDNSCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input app.DNSCredentialInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.UpdateDNSCredential(r.Context(), currentUser(r).ID, id, input)
	s.respond(w, value, "更新成功", err)
}

// deleteDNSCredential 删除未被引用的 DNS 凭据。
func (s *Server) deleteDNSCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	err = s.services.DeleteDNSCredential(r.Context(), currentUser(r).ID, id)
	s.respond(w, nil, "删除成功", err)
}

// testDNSCredential 验证指定 DNS 凭据的区域查询和临时 TXT 记录读写能力。
func (s *Server) testDNSCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.services.TestDNSCredential(r.Context(), id)
	if err == nil {
		user := currentUser(r)
		s.services.RecordAudit(r.Context(), &user.ID, "TEST", "DNS_CREDENTIAL", strconv.FormatInt(id, 10), result.Message)
	}
	s.respond(w, result, "测试完成", err)
}

// listAgents 返回已注册 Agent 列表。
func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	values, err := s.services.Store.ListAgents(r.Context())
	s.respond(w, values, "获取成功", err)
}

// createEnrollment 创建一次性 Agent 加入信息。
func (s *Server) createEnrollment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"` // Name 是待注册 Agent 的可读名称。
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.CreateEnrollment(r.Context(), currentUser(r).ID, input.Name)
	s.respond(w, value, "注册信息已生成", err)
}

// revokeAgent 撤销指定 Agent 身份。
func (s *Server) revokeAgent(w http.ResponseWriter, r *http.Request) {
	err := s.services.RevokeAgent(r.Context(), currentUser(r).ID, r.PathValue("id"))
	s.respond(w, nil, "Agent 已撤销", err)
}

// listTargets 返回部署目标及其稳定运行时路径。
func (s *Server) listTargets(w http.ResponseWriter, r *http.Request) {
	values, err := s.services.Store.ListTargets(r.Context())
	for index := range values {
		if values[index].ExecutorType == store.ExecutorLocal {
			values[index].RuntimePath = path.Join(s.services.Config.DeploymentDir, values[index].TargetAlias, "current")
		} else {
			values[index].RuntimePath = path.Join("<Agent 部署根目录>", values[index].TargetAlias, "current")
		}
	}
	s.respond(w, values, "获取成功", err)
}

// createTarget 创建本机或远程部署目标。
func (s *Server) createTarget(w http.ResponseWriter, r *http.Request) {
	var input app.DeploymentTargetInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.CreateTarget(r.Context(), currentUser(r).ID, input)
	s.respond(w, value, "创建成功", err)
}

// updateTarget 修改指定部署目标。
func (s *Server) updateTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var input app.DeploymentTargetInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.UpdateTarget(r.Context(), currentUser(r).ID, id, input)
	s.respond(w, value, "更新成功", err)
}

// deleteTarget 删除指定部署目标配置。
func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	err = s.services.DeleteTarget(r.Context(), currentUser(r).ID, id)
	s.respond(w, nil, "删除成功", err)
}

// deployTarget 本机立即执行，远程持久登记一次同步请求。
func (s *Server) deployTarget(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	err = s.services.SyncTarget(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	user := currentUser(r)
	s.services.RecordAudit(r.Context(), &user.ID, "SYNC", "DEPLOYMENT_TARGET", strconv.FormatInt(id, 10), "手动请求证书同步")
	value, err := s.services.Store.FindTarget(r.Context(), id)
	s.respond(w, value, "同步请求已处理", err)
}

// listDeploymentRecords 返回最近部署结果记录。
func (s *Server) listDeploymentRecords(w http.ResponseWriter, r *http.Request) {
	values, err := s.services.Store.ListDeploymentRecords(r.Context())
	s.respond(w, values, "获取成功", err)
}

// systemInfo 返回脱敏后的当前节点运行配置摘要。
func (s *Server) systemInfo(w http.ResponseWriter, _ *http.Request) {
	cfg := s.services.Config
	s.ok(w, map[string]any{"version": cfg.Version, "runtime": "Go", "role": cfg.Role, "localDeploymentEnabled": cfg.LocalDeploymentEnabled, "adminUrl": cfg.PublicURL, "agentUrl": cfg.AgentPublicURL, "deploymentDir": cfg.DeploymentDir, "renewBeforeDays": int(cfg.RenewBefore.Hours() / 24), "pollIntervalSeconds": int(cfg.AgentPollInterval.Seconds()), "historyRetentionDays": int(cfg.HistoryRetention.Hours() / 24), "alertWebhookEnabled": cfg.AlertWebhookURL != ""}, "获取成功")
}

// enrollAgent 处理一次性令牌注册请求。
func (s *Server) enrollAgent(w http.ResponseWriter, r *http.Request) {
	var input app.EnrollInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.EnrollAgent(r.Context(), input)
	s.respond(w, value, "Agent 注册成功", err)
}

// desiredState 返回当前认证 Agent 的完整部署期望状态。
func (s *Server) desiredState(w http.ResponseWriter, r *http.Request) {
	values, err := s.services.AgentDesiredState(r.Context(), currentAgent(r))
	revision := int64(17)
	for _, target := range values {
		revision = revision*31 + target.ID*7 + target.Revision
		if target.Certificate != nil {
			revision = revision*31 + target.Certificate.ID
		}
	}
	s.respond(w, map[string]any{"revision": revision, "targets": values}, "获取成功", err)
}

// renewAgentIdentity 使用当前 mTLS 身份轮换 Agent 客户端证书。
func (s *Server) renewAgentIdentity(w http.ResponseWriter, r *http.Request) {
	var input app.RenewAgentIdentityInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	value, err := s.services.RenewAgentIdentity(r.Context(), currentAgent(r), input)
	s.respond(w, value, "Agent 身份证书已续签", err)
}

// agentHeartbeat 接收节点版本与平台心跳。
func (s *Server) agentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version  string // Version 是 Agent 应用版本。
		Platform string // Platform 是 Agent 操作系统和架构。
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	err := s.services.HeartbeatAgent(r.Context(), currentAgent(r), input.Version, input.Platform)
	s.respond(w, nil, "心跳已记录", err)
}

// agentCertificate 向已授权 Agent 返回指定证书包。
func (s *Server) agentCertificate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	bundle, err := s.services.AgentCertificateBundle(r.Context(), currentAgent(r), id)
	s.respond(w, bundle, "获取成功", err)
}

// agentDeploymentResult 限流并记录 Agent 部署结果。
func (s *Server) agentDeploymentResult(w http.ResponseWriter, r *http.Request) {
	agent := currentAgent(r)
	if !s.allowAgentDeploymentReport(agent.ID, time.Now()) {
		writeError(w, &app.Error{Status: http.StatusTooManyRequests, Code: "AGENT_REPORT_RATE_LIMITED", Message: "部署结果上报过于频繁"})
		return
	}
	var input app.DeploymentResultInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, err)
		return
	}
	err := s.services.RecordAgentDeployment(r.Context(), agent, input)
	s.respond(w, nil, "部署结果已记录", err)
}

// allowAgentDeploymentReport 将单个 Agent 的部署结果上报限制为每分钟 120 次。
func (s *Server) allowAgentDeploymentReport(agentID string, now time.Time) bool {
	s.agentReportMu.Lock()
	defer s.agentReportMu.Unlock()
	if len(s.agentReports) >= 4096 {
		for existingAgentID, existingWindow := range s.agentReports {
			if now.Sub(existingWindow.started) >= time.Minute {
				delete(s.agentReports, existingAgentID)
			}
		}
	}
	if _, exists := s.agentReports[agentID]; !exists && len(s.agentReports) >= 4096 {
		return false
	}
	window := s.agentReports[agentID]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= 120 {
		s.agentReports[agentID] = window
		return false
	}
	window.count++
	s.agentReports[agentID] = window
	return true
}

// allowAgentRequest 将单个已认证 Agent 的总请求量限制为每分钟 600 次。
func (s *Server) allowAgentRequest(agentID string, now time.Time) bool {
	s.agentRequestMu.Lock()
	defer s.agentRequestMu.Unlock()
	if len(s.agentRequests) >= 4096 {
		for existingAgentID, existingWindow := range s.agentRequests {
			if now.Sub(existingWindow.started) >= time.Minute {
				delete(s.agentRequests, existingAgentID)
			}
		}
	}
	if _, exists := s.agentRequests[agentID]; !exists && len(s.agentRequests) >= 4096 {
		return false
	}
	window := s.agentRequests[agentID]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= 600 {
		s.agentRequests[agentID] = window
		return false
	}
	window.count++
	s.agentRequests[agentID] = window
	return true
}

// spaHandler 提供嵌入式静态资源并为前端路由回退到 index.html。
func (s *Server) spaHandler() http.Handler {
	dist, err := fs.Sub(web.Assets, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(dist))
	// SPA 回调优先返回静态文件，不存在时回退到前端入口。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(dist, name); err == nil && !info.IsDir() {
			if strings.Contains(path.Base(name), "-") && name != "index.html" {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			files.ServeHTTP(w, r)
			return
		}
		content, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "管理端尚未构建", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(content)
	})
}

// respond 将领域结果或错误转换为统一 HTTP 响应。
func (s *Server) respond(w http.ResponseWriter, data any, message string, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	s.ok(w, data, message)
}

// ok 写入成功的统一 JSON 信封。
func (s *Server) ok(w http.ResponseWriter, data any, message string) {
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": data, "message": message, "timestamp": time.Now().UTC()})
}

// decodeJSON 限制请求体大小、拒绝未知字段和额外 JSON 值。
func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return app.BadRequest("请求 JSON 格式无效")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return app.BadRequest("请求只能包含一个 JSON 对象")
	}
	return nil
}

// writeError 仅将安全业务错误信息写入统一失败信封。
func writeError(w http.ResponseWriter, err error) {
	business := &app.Error{Status: 500, Code: "INTERNAL_ERROR", Message: "服务器无法完成请求"}
	if errors.As(err, &business) && business.Status > 0 {
		writeJSON(w, business.Status, map[string]any{"success": false, "data": nil, "message": business.Message, "code": business.Code, "timestamp": time.Now().UTC()})
		return
	}
	writeJSON(w, 500, map[string]any{"success": false, "data": nil, "message": business.Message, "code": business.Code, "timestamp": time.Now().UTC()})
}

// writeJSON 设置 JSON 内容类型并写入状态码和响应体。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// pathID 解析并校验路由中的正整数资源主键。
func pathID(r *http.Request) (int64, error) {
	value, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || value <= 0 {
		return 0, app.BadRequest("资源编号无效")
	}
	return value, nil
}

// optionalPositiveQueryInt64 解析可选的正整数查询参数，缺失时返回零。
func optionalPositiveQueryInt64(r *http.Request, name string) (int64, error) {
	text := strings.TrimSpace(r.URL.Query().Get(name))
	if text == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil || value <= 0 {
		return 0, app.BadRequest(name + " 必须是正整数")
	}
	return value, nil
}

// optionalPositiveQueryInt 解析带默认值的可选正整数查询参数。
func optionalPositiveQueryInt(r *http.Request, name string, fallback int) (int, error) {
	value, err := optionalPositiveQueryInt64(r, name)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return fallback, nil
	}
	return int(value), nil
}

// listPageQuery 解析管理列表统一使用的页码和单页数量。
func listPageQuery(r *http.Request) (int, int, error) {
	page, err := optionalPositiveQueryInt(r, "page", 1)
	if err != nil {
		return 0, 0, err
	}
	if page > 1000000 {
		return 0, 0, app.BadRequest("page 不能超过 1000000")
	}
	pageSize, err := optionalPositiveQueryInt(r, "pageSize", 50)
	if err != nil {
		return 0, 0, err
	}
	if pageSize > 200 {
		return 0, 0, app.BadRequest("pageSize 不能超过 200")
	}
	return page, pageSize, nil
}

// currentUser 返回认证中间件写入的管理员身份。
func currentUser(r *http.Request) store.User {
	value, _ := r.Context().Value(userKey).(store.User)
	return value
}

// currentAgent 返回 mTLS 中间件写入的 Agent 身份。
func currentAgent(r *http.Request) store.Agent {
	value, _ := r.Context().Value(agentKey).(store.Agent)
	return value
}

// AgentTLSConfig 暴露只读 TLS 配置给启动器。
func (s *Server) AgentTLSConfig() *tls.Config { return s.services.PKI.ServerTLSConfig() }
