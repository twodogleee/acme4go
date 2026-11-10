// 本文件隔离 Agent 节点间并发资源，并为心跳与身份续签保留独立处理额度。
//
// @author Andersen
package control

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/store"
)

// agentBodyReadTimeout 限制注册及已认证 JSON 请求读取，防止慢请求长期占用额度。
const agentBodyReadTimeout = 5 * time.Second

// agentConcurrency 原子维护一个请求类别的全局额度和各节点在途数量。
type agentConcurrency struct {
	mu       sync.Mutex     // mu 保护额度预留、释放和节点条目清理。
	active   map[string]int // active 只保留在途节点，大小受全局额度约束。
	used     int            // used 是该类别当前占用总数。
	limit    int            // limit 是该类别的全局并发上限。
	perAgent int            // perAgent 是单个已认证节点的并发上限。
}

// newAgentConcurrency 创建独立类别预算，普通请求与维持身份的请求不能互相借用额度。
func newAgentConcurrency(limit, perAgent int) *agentConcurrency {
	return &agentConcurrency{active: make(map[string]int), limit: limit, perAgent: perAgent}
}

// acquire 同时预留节点和全局额度；拒绝请求不创建条目或占用额度。
func (b *agentConcurrency) acquire(agentID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active[agentID] >= b.perAgent {
		return &app.Error{Status: http.StatusTooManyRequests, Code: "AGENT_CONCURRENCY_LIMITED", Message: "当前 Agent 在途请求过多，请稍后重试"}
	}
	if b.used >= b.limit {
		return &app.Error{Status: http.StatusServiceUnavailable, Code: "AGENT_BUSY", Message: "Agent 通道繁忙，请稍后重试"}
	}
	b.active[agentID]++
	b.used++
	return nil
}

// release 释放一个已预留额度，最后一个请求退出时清理节点条目。
func (b *agentConcurrency) release(agentID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used--
	b.active[agentID]--
	if b.active[agentID] == 0 {
		delete(b.active, agentID)
	}
}

// authenticateAgentBounded 在独立有界额度内查询身份，业务处理和请求体读取不占用认证槽位。
func (s *Server) authenticateAgentBounded(ctx context.Context, serial, name string) (store.Agent, error) {
	select {
	case s.agentAuthSlots <- struct{}{}:
		// 身份查询完成即释放，不能跨越后续业务 Handler。
		defer func() { <-s.agentAuthSlots }()
	default:
		return store.Agent{}, &app.Error{Status: http.StatusServiceUnavailable, Code: "AGENT_AUTH_BUSY", Message: "Agent 认证通道繁忙，请稍后重试"}
	}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return s.services.AuthenticateAgent(queryCtx, serial, name)
}

// limitAgentConcurrency 按已认证节点及接口类别限制在途请求，所有退出路径均释放额度。
func (s *Server) limitAgentConcurrency(next http.Handler) http.Handler {
	// 心跳和续签只接受固定 POST 路由，其他请求使用普通预算。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		budget := s.agentDataBudget
		if r.Method == http.MethodPost && (r.URL.Path == "/agent/v1/heartbeat" || r.URL.Path == "/agent/v1/identity/renew") {
			budget = s.agentLiveBudget
		}
		agentID := currentAgent(r).ID
		if err := budget.acquire(agentID); err != nil {
			// HTTP/1 拒绝时关闭连接，避免响应前为复用连接而继续等待恶意请求体。
			if r.ProtoMajor == 1 {
				w.Header().Set("Connection", "close")
			}
			w.Header().Set("Retry-After", "1")
			writeError(w, err)
			return
		}
		defer budget.release(agentID)
		next.ServeHTTP(w, r)
	})
}

// limitAgentBodyRead 对小型 JSON 请求设置实际传输读取期限，同时覆盖未经认证的注册入口。
func limitAgentBodyRead(next http.Handler) http.Handler {
	// 直接使用 HTTP 传输期限中断阻塞读取，不启动无法可靠取消的读协程。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(agentBodyReadTimeout)); err != nil {
				writeError(w, &app.Error{Status: http.StatusServiceUnavailable, Code: "AGENT_READ_DEADLINE_UNAVAILABLE", Message: "Agent 请求读取保护不可用"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
