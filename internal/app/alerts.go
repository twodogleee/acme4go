// 本文件通过显式配置且带签名的 Webhook 发布证书签发与部署失败告警。
//
// @author Andersen
package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/twodogleee/acme4go/internal/store"
)

// Alert 描述不会包含凭据、令牌或私钥的生产告警事件。
type Alert = store.AlertEvent

// configuredAlerts 按通知配置生成事务事件，停止阶段仍允许已开始的业务完成持久化。
func (s *Services) configuredAlerts(value Alert) []Alert {
	if s.Config.AlertWebhookURL == "" {
		return nil
	}
	return []Alert{value}
}

// PublishAlert 为受生命周期跟踪的后台流程同步入队；订单和部署失败使用各自的事务接口。
func (s *Services) PublishAlert(value Alert) {
	if s.Config.AlertWebhookURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.EnqueueAlert(ctx, value, ""); err != nil {
		s.logger.Error("保存告警事件失败", "event", value.Event, "resource_id", value.ResourceID, "error", err)
	}
}

// sendAlert 序列化告警、生成 HMAC-SHA256 签名并要求接收端返回成功状态。
func (s *Services) sendAlert(ctx context.Context, value Alert) error {
	if value.Timestamp.IsZero() {
		value.Timestamp = time.Now().UTC()
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(s.Config.AlertWebhookSecret))
	_, _ = mac.Write(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Config.AlertWebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Acme4go-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	client := &http.Client{
		// Webhook 必须直接返回结果，禁止重定向把签名请求转发到未校验地址。
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("通知接收端连接失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Webhook 返回 HTTP %d", response.StatusCode)
	}
	return nil
}
