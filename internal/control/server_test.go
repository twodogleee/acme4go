// 本文件验证管理端登录、CSRF 与可信代理安全边界。
//
// @author Andersen
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestAdminLoginAndCSRFBoundary 验证登录方法限制及状态变更请求的 CSRF 防护。
func TestAdminLoginAndCSRFBoundary(t *testing.T) {
	directory := t.TempDir()
	cfg := config.Config{
		Role: config.RoleServer, Version: "test", LocalDeploymentEnabled: true, PublicURL: "http://127.0.0.1:8080",
		AgentPublicURL: "https://127.0.0.1:9443", DataDir: filepath.Join(directory, "data"),
		DeploymentDir: filepath.Join(directory, "deployments"), MasterKey: []byte(strings.Repeat("m", 32)),
		AdminUsername: "admin", AdminPassword: "correct-horse-battery", SessionTTL: time.Hour,
		RenewBefore: 30 * 24 * time.Hour, SchedulerInterval: time.Hour, AgentPollInterval: 30 * time.Second,
	}
	services, err := app.NewServices(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer services.Close()
	server := New(services, slog.Default())
	handler := server.AdminHandler()
	methodRecorder := httptest.NewRecorder()
	handler.ServeHTTP(methodRecorder, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	if methodRecorder.Code != http.StatusMethodNotAllowed || methodRecorder.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET 登录接口应返回 405 并声明 POST，实际 status=%d allow=%q", methodRecorder.Code, methodRecorder.Header().Get("Allow"))
	}

	loginRecorder := httptest.NewRecorder()
	handler.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"username":"admin","password":"correct-horse-battery"}`)))
	loginResponse := loginRecorder.Result()
	defer loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusOK || len(loginResponse.Cookies()) != 1 {
		t.Fatalf("登录失败: status=%d cookies=%d", loginResponse.StatusCode, len(loginResponse.Cookies()))
	}
	var login struct {
		// Data 是统一响应信封中的登录结果。
		Data struct {
			CSRFToken string `json:"csrfToken"` // CSRFToken 是后续状态变更请求使用的令牌。
		} `json:"data"`
	}
	if err := json.NewDecoder(loginResponse.Body).Decode(&login); err != nil || login.Data.CSRFToken == "" {
		t.Fatalf("登录响应缺少 CSRF Token: value=%+v err=%v", login, err)
	}

	// callEnrollment 构造携带可选 CSRF 令牌的 Agent 注册信息请求。
	callEnrollment := func(csrf string) *http.Response {
		request := httptest.NewRequest(http.MethodPost, "/api/agents/enrollments", bytes.NewBufferString(`{"name":"edge-1"}`))
		request.Host = "localhost:5173"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://localhost:5173")
		if csrf != "" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		request.AddCookie(loginResponse.Cookies()[0])
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result()
	}

	denied := callEnrollment("")
	denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("缺少 CSRF Token 应返回 403，实际 %d", denied.StatusCode)
	}
	allowed := callEnrollment(login.Data.CSRFToken)
	defer allowed.Body.Close()
	if allowed.StatusCode != http.StatusOK {
		content, _ := io.ReadAll(allowed.Body)
		t.Fatalf("有效 CSRF 请求失败: status=%d body=%s", allowed.StatusCode, content)
	}
}

// TestOperationalAPIsKeepAuthAndForwardFilters 验证新增接口鉴权、CSRF、全量查询参数和订单时间线契约。
func TestOperationalAPIsKeepAuthAndForwardFilters(t *testing.T) {
	directory := t.TempDir()
	services, err := app.NewServices(config.Config{Role: config.RoleServer, DataDir: filepath.Join(directory, "data"), DeploymentDir: filepath.Join(directory, "deployments"), MasterKey: []byte(strings.Repeat("m", 32)), AdminUsername: "admin", AdminPassword: "correct-horse-battery", PublicURL: "http://127.0.0.1:8080", AgentPublicURL: "https://127.0.0.1:9443", SessionTTL: time.Hour, RenewBefore: 30 * 24 * time.Hour, AlertWebhookURL: "https://notify.invalid", AlertWebhookSecret: strings.Repeat("s", 32)}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer services.Close()
	handler := New(services, slog.New(slog.NewTextHandler(io.Discard, nil))).AdminHandler()
	login, err := services.Login(context.Background(), "admin", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	// requestAPI 模拟已登录浏览器，并允许省略 CSRF 以验证安全边界。
	requestAPI := func(method, path string, authenticated, csrf bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("Origin", "http://127.0.0.1:8080")
		if authenticated {
			request.AddCookie(&http.Cookie{Name: "acme4go_session", Value: login.Token})
		}
		if csrf {
			request.Header.Set("X-CSRF-Token", login.CSRFToken)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := requestAPI("GET", "/api/alerts", false, false); response.Code != 401 {
		t.Fatalf("通知记录应要求登录：%d", response.Code)
	}
	if response := requestAPI("POST", "/api/alerts/test", true, false); response.Code != 403 {
		t.Fatalf("测试通知应要求 CSRF：%d", response.Code)
	}
	if response := requestAPI("POST", "/api/alerts/test", true, true); response.Code != 200 {
		t.Fatalf("测试通知无法排队：%s", response.Body.String())
	}
	if response := requestAPI("GET", "/api/alerts?status=PENDING", true, false); response.Code != 200 || !strings.Contains(response.Body.String(), "system.notification.test") || strings.Contains(response.Body.String(), services.Config.AlertWebhookSecret) {
		t.Fatalf("投递查询契约异常：%d", response.Code)
	}
	credential, err := services.Store.CreateDNSCredential(context.Background(), store.DNSCredential{Name: "synthetic", Provider: "ALIYUN", Zone: "example.com", AccessKeyID: "encrypted", AccessKeySecret: "encrypted", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	series, err := services.Store.ResolveCertificateSeries(context.Background(), store.CertificateSeries{ID: "api-series", Code: "API", Name: "接口测试", Domains: []string{"example.com"}, DomainsHash: store.DomainSetHash([]string{"example.com"}), Email: "ops@example.com", CAEnvironment: "LETS_ENCRYPT_STAGING", CADirectory: store.StagingDirectory, CredentialID: credential.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"old.example.com", "new.example.com"} {
		order, err := services.Store.CreateOrder(context.Background(), store.Order{SeriesID: series.ID, Domains: []string{domain}, Email: series.Email, CAEnvironment: series.CAEnvironment, CADirectory: series.CADirectory, CredentialID: credential.ID})
		if err != nil {
			t.Fatal(err)
		}
		if err := services.Store.FailOrder(context.Background(), order.ID, "可安全展示的错误"); err != nil {
			t.Fatal(err)
		}
	}
	response := requestAPI("GET", "/api/orders?search=old.example.com&status=FAILED&pageSize=1", true, false)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"total":1`) || !strings.Contains(response.Body.String(), "old.example.com") {
		t.Fatalf("查询未正确传递筛选条件：%s", response.Body.String())
	}
	response = requestAPI("GET", "/api/orders/1", true, false)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"events"`) || !strings.Contains(response.Body.String(), "可安全展示的错误") {
		t.Fatalf("订单详情缺少时间线：%s", response.Body.String())
	}
	if response := requestAPI("GET", "/api/orders?status=UNKNOWN", true, false); response.Code != 400 {
		t.Fatal("未知状态必须明确拒绝")
	}
}

// TestLoginClientTrustsOnlyConfiguredProxyChain 验证客户端不能通过伪造转发头绕过登录限流。
func TestLoginClientTrustsOnlyConfiguredProxyChain(t *testing.T) {
	server := &Server{trustedProxy: parseTrustedProxyNetworks([]string{"127.0.0.0/8", "10.0.0.0/8"})}
	trustedRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	trustedRequest.RemoteAddr = "127.0.0.1:12345"
	trustedRequest.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.2")
	if client := server.loginClient(trustedRequest); client != "198.51.100.7" {
		t.Fatalf("可信代理链客户端解析错误: %s", client)
	}

	untrustedRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	untrustedRequest.RemoteAddr = "192.0.2.10:12345"
	untrustedRequest.Header.Set("X-Forwarded-For", "198.51.100.8")
	if client := server.loginClient(untrustedRequest); client != "192.0.2.10" {
		t.Fatalf("非可信来源的转发头不应生效: %s", client)
	}
}

// TestBeginLoginReservesAttemptBeforePasswordHash 验证第五次失败后续请求会在密码计算前被拒绝。
func TestBeginLoginReservesAttemptBeforePasswordHash(t *testing.T) {
	server := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Now()
	for attempt := 1; attempt <= 5; attempt++ {
		if !server.beginLogin("client", now) {
			t.Fatalf("第 %d 次登录尝试不应提前拒绝", attempt)
		}
		server.finishLoginFailure("client", now)
	}
	if server.beginLogin("client", now) {
		t.Fatal("达到失败上限后应在 Argon2 计算前拒绝")
	}
}

// TestAgentReportRateLimit 验证单个 Agent 在一分钟内不能无限写入部署结果。
func TestAgentReportRateLimit(t *testing.T) {
	server := &Server{agentReports: make(map[string]rateWindow)}
	now := time.Now()
	for report := 1; report <= 120; report++ {
		if !server.allowAgentDeploymentReport("agent", now) {
			t.Fatalf("第 %d 条合法窗口内上报不应被拒绝", report)
		}
	}
	if server.allowAgentDeploymentReport("agent", now) {
		t.Fatal("超过每分钟上报上限后应返回限流")
	}
	if !server.allowAgentDeploymentReport("agent", now.Add(time.Minute)) {
		t.Fatal("新时间窗口应恢复上报")
	}
}

// TestAgentRequestRateLimit 验证异常 Agent 无法无限调用心跳或期望状态接口占用数据库。
func TestAgentRequestRateLimit(t *testing.T) {
	server := &Server{agentRequests: make(map[string]rateWindow)}
	now := time.Now()
	for request := 1; request <= 600; request++ {
		if !server.allowAgentRequest("agent", now) {
			t.Fatalf("第 %d 个请求不应被限流", request)
		}
	}
	if server.allowAgentRequest("agent", now) {
		t.Fatal("超过 Agent 总请求上限后应被限流")
	}
}

// TestEnrollmentRateLimit 验证公开注册接口按来源限制请求预算并在新窗口恢复。
func TestEnrollmentRateLimit(t *testing.T) {
	server := &Server{enrollments: make(map[string]rateWindow)}
	now := time.Now()
	for request := 1; request <= 10; request++ {
		if !server.allowEnrollment("192.0.2.1", now) {
			t.Fatalf("第 %d 个注册请求不应被限流", request)
		}
	}
	if server.allowEnrollment("192.0.2.1", now) {
		t.Fatal("超过公开注册请求上限后应被限流")
	}
	if !server.allowEnrollment("192.0.2.1", now.Add(15*time.Minute)) {
		t.Fatal("新注册时间窗口应恢复预算")
	}
}
