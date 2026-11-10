// 本文件验证公开登录字段、来源预算和缓存满载后的资源边界。
//
// @author Andersen
package control

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLoginFieldsRejectedBeforeAccountCache 验证超长输入在账号预算入队和密码计算前返回错误。
func TestLoginFieldsRejectedBeforeAccountCache(t *testing.T) {
	server := &Server{loginAttempts: make(map[string]loginAttempt)}
	for _, input := range []map[string]string{
		{"username": strings.Repeat("u", 257), "password": "valid-password"},
		{"username": "admin", "password": strings.Repeat("p", 1025)},
	} {
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		server.login(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body)))
		if recorder.Code != http.StatusBadRequest || len(server.loginAttempts) != 0 {
			t.Fatalf("超长输入未提前拒绝：status=%d entries=%d", recorder.Code, len(server.loginAttempts))
		}
	}
}

// TestLoginSourceBudgetCannotBeBypassedByUsernames 验证同来源更换用户名仍共享请求预算，畸形请求也计数。
func TestLoginSourceBudgetCannotBeBypassedByUsernames(t *testing.T) {
	server := &Server{loginAttempts: make(map[string]loginAttempt)}
	for index := 0; index <= maxLoginSourceRequests; index++ {
		body := fmt.Sprintf(`{"username":"user-%d","password":""}`, index)
		recorder := httptest.NewRecorder()
		server.login(recorder, httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body)))
		want := http.StatusBadRequest
		if index == maxLoginSourceRequests {
			want = http.StatusTooManyRequests
		}
		if recorder.Code != want {
			t.Fatalf("第 %d 次请求：status=%d want=%d", index+1, recorder.Code, want)
		}
	}
}

// TestLoginCachesHaveHardCapacity 验证两类缓存均不驱逐有效预算，满载拒绝新键，过期后恢复容量。
func TestLoginCachesHaveHardCapacity(t *testing.T) {
	server := &Server{loginAttempts: make(map[string]loginAttempt)}
	now := time.Now()
	for index := 0; index < maxLoginEntries+4; index++ {
		key := fmt.Sprintf("key-%d", index)
		want := index < maxLoginEntries
		if got := server.beginLogin(key, now); got != want {
			t.Fatalf("账号缓存容量错误：index=%d allowed=%v", index, got)
		}
		if got := server.allowLoginSource(key, now); got != want {
			t.Fatalf("来源缓存容量错误：index=%d allowed=%v", index, got)
		}
	}
	if len(server.loginAttempts) != maxLoginEntries || len(server.loginSources) != maxLoginEntries {
		t.Fatal("缓存超过硬容量")
	}
	if !server.beginLogin("key-0", now) || !server.allowLoginSource("key-0", now) {
		t.Fatal("满载不应拒绝仍有预算的已有键")
	}
	if !server.beginLogin("recovered", now.Add(31*time.Minute)) || !server.allowLoginSource("recovered", now.Add(31*time.Minute)) {
		t.Fatal("过期清理后没有恢复容量")
	}
	if len(server.loginAttempts) != 1 || len(server.loginSources) != 1 {
		t.Fatal("过期预算没有清理")
	}
}
