// 本文件验证容器健康检查使用管理面实际监听地址并正确处理就绪结果。
//
// @author Andersen
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHealthcheckURL 验证通配监听地址转换为同地址族回环地址，显式地址保持不变。
func TestHealthcheckURL(t *testing.T) {
	tests := []struct {
		name    string // name 标识待验证的监听地址类型。
		address string // address 是管理面配置的监听地址。
		want    string // want 是健康检查应访问的完整 URL。
	}{
		{name: "默认通配", address: ":8080", want: "http://127.0.0.1:8080/readyz"},
		{name: "IPv4 通配", address: "0.0.0.0:18080", want: "http://127.0.0.1:18080/readyz"},
		{name: "IPv6 通配", address: "[::]:18080", want: "http://[::1]:18080/readyz"},
		{name: "IPv4 明确绑定", address: "127.0.0.2:18080", want: "http://127.0.0.2:18080/readyz"},
		{name: "IPv6 明确绑定", address: "[::1]:18080", want: "http://[::1]:18080/readyz"},
		{name: "主机名绑定", address: "localhost:18080", want: "http://localhost:18080/readyz"},
	}
	for _, test := range tests {
		// 子测试分别验证每一种监听地址格式的转换结果。
		t.Run(test.name, func(t *testing.T) {
			got, err := healthcheckURL(test.address)
			if err != nil || got != test.want {
				t.Fatalf("健康检查地址不匹配: got=%q want=%q err=%v", got, test.want, err)
			}
		})
	}
	if _, err := healthcheckURL("invalid-address"); err == nil {
		t.Fatal("无效的监听地址应返回错误")
	}
}

// TestRunHealthcheckUsesConfiguredAddress 验证自定义监听端口被实际探测，且非就绪状态会失败。
func TestRunHealthcheckUsesConfiguredAddress(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	paths := make(chan string, 2)
	// 本地测试服务记录探测路径，避免依赖固定端口或真实业务数据。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	t.Setenv("ACME_LISTEN_ADDRESS", strings.TrimPrefix(server.URL, "http://"))
	if err := runHealthcheck(); err != nil {
		t.Fatalf("自定义监听地址的就绪探测失败: %v", err)
	}
	if path := <-paths; path != "/readyz" {
		t.Fatalf("健康检查使用了错误路径: %q", path)
	}
	status.Store(http.StatusServiceUnavailable)
	if err := runHealthcheck(); err == nil {
		t.Fatal("就绪接口返回非 200 时健康检查应失败")
	}
	server.Close()
	if err := runHealthcheck(); err == nil {
		t.Fatal("就绪接口不可连接时健康检查应失败")
	}
}
