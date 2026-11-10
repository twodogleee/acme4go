// 本文件验证 Webhook 告警签名、重定向边界和部署失败去重规则。
//
// @author Andersen
package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestSendAlertSignsBody 验证接收端能够用配置密钥校验原始请求正文。
func TestSendAlertSignsBody(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	received := make(chan bool, 1)
	// 告警接收回调根据原始正文重算并比较 HMAC 签名。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("读取告警正文失败: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		received <- hmac.Equal([]byte(expected), []byte(request.Header.Get("X-Acme4go-Signature")))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	services := &Services{Config: config.Config{AlertWebhookURL: server.URL, AlertWebhookSecret: secret}}
	err := services.sendAlert(context.Background(), Alert{Event: "test.failed", Severity: "ERROR", ResourceType: "TEST", ResourceID: "1", Message: "test", Timestamp: time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatalf("发送告警失败: %v", err)
	}
	if !<-received {
		t.Fatal("告警正文签名不匹配")
	}
}

// TestSendAlertRejectsRedirect 验证签名告警不会跟随重定向抵达第二个地址。
func TestSendAlertRejectsRedirect(t *testing.T) {
	var destinationCalls atomic.Int32
	// 目标回调记录是否收到被重定向的签名请求。
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		destinationCalls.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	// 起始回调始终返回保留 POST 语义的临时重定向。
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	services := &Services{Config: config.Config{AlertWebhookURL: redirect.URL, AlertWebhookSecret: "0123456789abcdef0123456789abcdef"}}
	if err := services.sendAlert(context.Background(), Alert{Event: "test.failed"}); err == nil {
		t.Fatal("重定向响应应被拒绝")
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("签名告警不应跟随重定向")
	}
}

// TestAlertDispatchRecoversAfterRestart 验证失败落库、重启恢复、人工重发和事件身份稳定。
func TestAlertDispatchRecoversAfterRestart(t *testing.T) {
	services, _ := testReliabilityServices(t)
	directory := filepath.Join(t.TempDir(), "queue")
	database, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	services.Store = database
	// 清理重启后当前使用的测试数据库。
	t.Cleanup(func() {
		if services.Store != nil {
			services.Store.Close()
		}
	})
	ctx := context.Background()
	received := make(chan store.AlertEvent, 2)
	var calls atomic.Int32
	// 接收端先返回临时失败，下一次确认成功，用于验证跨进程状态恢复。
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var value store.AlertEvent
		if err := json.NewDecoder(request.Body).Decode(&value); err != nil {
			t.Errorf("通知载荷无效：%v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- value
		if calls.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	services.Config.AlertWebhookURL = server.URL
	services.Config.AlertWebhookSecret = "synthetic-webhook-secret-32-characters"
	if err := services.Store.EnqueueAlert(ctx, Alert{Event: "test.delivery", Severity: "INFO", Message: "合成通知"}, ""); err != nil {
		t.Fatal(err)
	}
	services.dispatchAlerts(ctx)
	values, total, err := services.Store.ListAlertDeliveries(ctx, 1, 50, "RETRY")
	if err != nil || total != 1 || values[0].Attempts != 1 || values[0].LastError == nil || *values[0].LastError != "Webhook 返回 HTTP 503" {
		t.Fatalf("临时失败必须持久化：%+v %d %v", values, total, err)
	}
	id := values[0].ID
	if err := services.Store.Close(); err != nil {
		t.Fatal(err)
	}
	services.Store, err = store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := services.Store.RetryAlert(ctx, id); err != nil {
		t.Fatal(err)
	}
	services.dispatchAlerts(ctx)
	values, total, err = services.Store.ListAlertDeliveries(ctx, 1, 50, "SENT")
	if err != nil || total != 1 || values[0].ID != id || values[0].DeliveredAt == nil || calls.Load() != 2 {
		t.Fatalf("重启重发后应确认送达：%+v %d %v", values, total, err)
	}
	first, second := <-received, <-received
	if first.ID != id || first.ID != second.ID || !first.Timestamp.Equal(second.Timestamp) {
		t.Fatalf("重试必须保留接收端去重身份：%+v %+v", first, second)
	}
}
