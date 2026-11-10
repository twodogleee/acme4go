// 本文件用真实 mTLS 慢请求验证节点隔离、关键请求预留以及 HTTP 传输期限释放。
//
// @author Andersen
package control

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/store"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newAgentLimitServer 创建临时数据库和真实 TLS 服务，注册工厂仅生成合成节点。
func newAgentLimitServer(t *testing.T) (*Server, *httptest.Server, func(string) *tls.Config) {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	services, err := app.NewServices(config.Config{DataDir: t.TempDir(), MasterKey: []byte(strings.Repeat("m", 32)), AdminUsername: "admin", AdminPassword: "synthetic-review-password", AgentPublicURL: "https://127.0.0.1:9443", PublicURL: "http://127.0.0.1:8080", SessionTTL: time.Hour}, logger)
	if err != nil {
		t.Fatal(err)
	}
	// 测试资源逆序清理，在服务监听关闭后关闭数据库。
	t.Cleanup(func() { services.Close() })
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(services.PKI.CAPEM())
	// 创建两个独立且没有部署权限的合成节点。
	register := func(id string) *tls.Config {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: id}}, key)
		if err != nil {
			t.Fatal(err)
		}
		cert, serial, expires, err := services.PKI.SignAgentCSR(id, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))
		if err != nil {
			t.Fatal(err)
		}
		if err := services.Store.CreateEnrollmentToken(ctx, id, id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		// 测试注册事务绑定已经签发的独立身份。
		_, err = services.Store.ConsumeEnrollmentToken(ctx, id, time.Now(), func(name string) (store.Agent, error) {
			return store.Agent{ID: id, Name: name, CertificateSerial: serial, CertificateExpiresAt: expires}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.X509KeyPair(cert, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
		if err != nil {
			t.Fatal(err)
		}
		return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "127.0.0.1", Certificates: []tls.Certificate{pair}}
	}

	handler := New(services, logger)
	server := httptest.NewUnstartedServer(handler.AgentHandler())
	server.TLS = services.PKI.ServerTLSConfig()
	pair, err := server.TLS.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS.Certificates = []tls.Certificate{*pair}
	server.Config.ReadTimeout = 2 * time.Minute
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return handler, server, register
}

// agentLimitClient 创建可选择 HTTP/2 的真实客户端，结束时关闭空闲连接。
func agentLimitClient(t *testing.T, identity *tls.Config, h2 bool) *http.Client {
	t.Helper()
	transport := &http.Transport{TLSClientConfig: identity, ForceAttemptHTTP2: h2}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

// agentLimitRequest 发送完整 JSON 或无请求体查询，并返回状态供隔离断言使用。
func agentLimitRequest(t *testing.T, client *http.Client, method, url, body string) int {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response.StatusCode
}

// slowAgentRequest 只发送请求头及 JSON 开头，调用方可观察拒绝响应或显式关闭连接。
func slowAgentRequest(t *testing.T, server *httptest.Server, identity *tls.Config, path string) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", server.Listener.Addr().String(), identity)
	if err != nil {
		t.Fatal(err)
	}
	// 提前断言失败时也不能遗留慢连接。
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{", path); err != nil {
		t.Fatal(err)
	}
	return conn
}

// waitAgentBudget 等待真实 Handler 取得或释放额度，并在归零时检查节点记录已清理。
func waitAgentBudget(t *testing.T, budget *agentConcurrency, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		budget.mu.Lock()
		used, entries := budget.used, len(budget.active)
		budget.mu.Unlock()
		if used == want {
			if want == 0 && entries != 0 {
				t.Fatal("空闲预算遗留节点记录")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("预算未收敛：got=%d want=%d", used, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAgentSlowRequestsAreIsolated 验证原有八慢连接场景仅占一个关键额度，其余被拒绝且不影响其他节点。
func TestAgentSlowRequestsAreIsolated(t *testing.T) {
	handler, server, register := newAgentLimitServer(t)
	noisy, healthy := register("noisy"), register("healthy")
	client := agentLimitClient(t, healthy, false)
	if status := agentLimitRequest(t, client, "POST", server.URL+"/agent/v1/heartbeat", `{}`); status != 200 {
		t.Fatal(status)
	}
	first := slowAgentRequest(t, server, noisy, "/agent/v1/heartbeat")
	waitAgentBudget(t, handler.agentLiveBudget, 1, time.Second)
	for index := 0; index < 7; index++ {
		conn := slowAgentRequest(t, server, noisy, "/agent/v1/heartbeat")
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		conn.Close()
		if response.StatusCode != 429 {
			t.Fatalf("超出单节点额度应拒绝：%d", response.StatusCode)
		}
	}
	dataFirst := slowAgentRequest(t, server, noisy, "/agent/v1/deployments/result")
	dataSecond := slowAgentRequest(t, server, noisy, "/agent/v1/deployments/result")
	waitAgentBudget(t, handler.agentDataBudget, 2, time.Second)
	noisyClient := agentLimitClient(t, noisy, false)
	if status := agentLimitRequest(t, noisyClient, "GET", server.URL+"/agent/v1/desired-state", ""); status != 429 {
		t.Fatalf("普通请求单节点额度未生效：%d", status)
	}
	if status := agentLimitRequest(t, client, "POST", server.URL+"/agent/v1/heartbeat", `{}`); status != 200 {
		t.Fatalf("其他节点心跳被阻断：%d", status)
	}
	if status := agentLimitRequest(t, client, "GET", server.URL+"/agent/v1/desired-state", ""); status != 200 {
		t.Fatalf("其他节点期望状态被阻断：%d", status)
	}
	first.Close()
	dataFirst.Close()
	dataSecond.Close()
	waitAgentBudget(t, handler.agentLiveBudget, 0, time.Second)
	waitAgentBudget(t, handler.agentDataBudget, 0, time.Second)
	if status := agentLimitRequest(t, noisyClient, "POST", server.URL+"/agent/v1/heartbeat", `{}`); status != 200 {
		t.Fatalf("原节点关闭连接后未恢复：%d", status)
	}
}

// TestAgentControlBudgetSurvivesDataSaturation 验证多个节点占满普通额度后，心跳与真实身份续签仍可完成。
func TestAgentControlBudgetSurvivesDataSaturation(t *testing.T) {
	handler, server, register := newAgentLimitServer(t)
	var connections []*tls.Conn
	for index := 0; index < 3; index++ {
		identity := register(fmt.Sprintf("busy-%d", index))
		for count := 0; count < 2; count++ {
			connections = append(connections, slowAgentRequest(t, server, identity, "/agent/v1/deployments/result"))
		}
	}
	waitAgentBudget(t, handler.agentDataBudget, 6, time.Second)
	client := agentLimitClient(t, register("healthy"), false)
	if status := agentLimitRequest(t, client, "GET", server.URL+"/agent/v1/desired-state", ""); status != 503 {
		t.Fatalf("普通全局额度未生效：%d", status)
	}
	if status := agentLimitRequest(t, client, "POST", server.URL+"/agent/v1/heartbeat", `{}`); status != 200 {
		t.Fatalf("关键额度未隔离：%d", status)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(app.RenewAgentIdentityInput{CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}))})
	if err != nil {
		t.Fatal(err)
	}
	if status := agentLimitRequest(t, client, "POST", server.URL+"/agent/v1/identity/renew", string(body)); status != 200 {
		t.Fatalf("续签被普通请求阻断：%d", status)
	}
	for _, conn := range connections {
		conn.Close()
	}
	waitAgentBudget(t, handler.agentDataBudget, 0, time.Second)
	if status := agentLimitRequest(t, client, "GET", server.URL+"/agent/v1/desired-state", ""); status != 200 {
		t.Fatalf("释放后未恢复：%d", status)
	}
}

// TestAgentSlowBodyTimeoutReleasesBudget 验证真实 HTTP/1 和 HTTP/2 在五秒期限后释放额度，HTTP/2 后续请求仍可工作。
func TestAgentSlowBodyTimeoutReleasesBudget(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		// 两种传输分别验证生产期限，不缩短测试用期限或替换解码函数。
		t.Run(fmt.Sprintf("http2=%v", h2), func(t *testing.T) {
			handler, server, register := newAgentLimitServer(t)
			identity := register("slow")
			client := agentLimitClient(t, identity, h2)
			baseline, err := client.Get(server.URL + "/agent/v1/desired-state")
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, baseline.Body)
			baseline.Body.Close()
			if baseline.StatusCode != 200 || (baseline.ProtoMajor == 2) != h2 {
				t.Fatalf("基线请求协议或状态异常：%s %d", baseline.Proto, baseline.StatusCode)
			}
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			request, err := http.NewRequest("POST", server.URL+"/agent/v1/heartbeat", reader)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			done := make(chan error, 1)
			// 请求协程保持请求体未完成，直到传输期限使请求失败或返回错误响应。
			go func() {
				response, err := client.Do(request)
				if err == nil {
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if response.StatusCode == 200 {
						err = fmt.Errorf("未完成请求体被接受")
					}
					if h2 && response.ProtoMajor != 2 {
						err = fmt.Errorf("未使用 HTTP/2")
					}
				} else {
					// 读取期限可能让传输关闭连接或重置流；额度及时释放由主协程另行断言。
					if request.Context().Err() == nil {
						err = nil
					}
				}
				done <- err
			}()
			if _, err := writer.Write([]byte("{")); err != nil {
				t.Fatal(err)
			}
			waitAgentBudget(t, handler.agentLiveBudget, 1, time.Second)
			started := time.Now()
			waitAgentBudget(t, handler.agentLiveBudget, 0, agentBodyReadTimeout+2*time.Second)
			if time.Since(started) < agentBodyReadTimeout-time.Second {
				t.Fatal("未等待实际读取期限")
			}
			writer.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("超时请求未退出")
			}
			if status := agentLimitRequest(t, client, "POST", server.URL+"/agent/v1/heartbeat", `{}`); status != 200 {
				t.Fatalf("超时后未恢复：%d", status)
			}
		})
	}
}

// TestAgentConcurrencyReleasesOnCancelAndPanic 验证在途请求取消及 Handler 异常均不会泄漏额度。
func TestAgentConcurrencyReleasesOnCancelAndPanic(t *testing.T) {
	for _, mode := range []string{"cancel", "panic"} {
		// 两种非正常退出均经过真实并发中间件和 Panic 恢复中间件。
		t.Run(mode, func(t *testing.T) {
			server := New(&app.Services{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			entered := make(chan struct{})
			// 业务回调模拟在途取消或已进入 Handler 后发生异常。
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				if mode == "panic" {
					panic("synthetic failure")
				}
				<-r.Context().Done()
			})
			handler := server.commonMiddleware(server.limitAgentConcurrency(next))
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), agentKey, store.Agent{ID: "cancel-agent"}))
			defer cancel()
			request := httptest.NewRequest("POST", "/agent/v1/heartbeat", nil).WithContext(ctx)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			// 独立请求协程允许测试在取得额度之后主动取消。
			go func() { defer close(done); handler.ServeHTTP(response, request) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("请求未进入业务处理")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("请求未退出")
			}
			if mode == "panic" && response.Code != 500 {
				t.Fatal("异常未转换为错误响应")
			}
			waitAgentBudget(t, server.agentLiveBudget, 0, time.Second)
			if err := server.agentLiveBudget.acquire("cancel-agent"); err != nil {
				t.Fatal("异常退出后无法重新使用额度")
			}
			server.agentLiveBudget.release("cancel-agent")
		})
	}
}
