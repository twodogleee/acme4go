// 本文件约束公开登录入口的来源预算和缓存容量，避免用户名变换导致资源持续增长。
//
// @author Andersen
package control

import "time"

const (
	// maxLoginEntries 分别限制账号预算和来源预算的最大条目数；满载时拒绝新键。
	maxLoginEntries = 4096
	// maxLoginSourceRequests 是同一来源每分钟允许的全部登录请求数，成功登录也不重置。
	maxLoginSourceRequests = 30
)

// allowLoginSource 在读取请求和执行 Argon2 前按可信来源 IP 预留请求预算。
func (s *Server) allowLoginSource(source string, now time.Time) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.pruneLoginAttempts(now)
	if s.loginSources == nil {
		s.loginSources = make(map[string]rateWindow)
	}
	window, exists := s.loginSources[source]
	if !exists && len(s.loginSources) >= maxLoginEntries {
		return false
	}
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= maxLoginSourceRequests {
		return false
	}
	window.count++
	s.loginSources[source] = window
	return true
}
