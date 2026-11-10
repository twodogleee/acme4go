// 本文件调度持久告警投递、证书到期预警和 Agent 离线通知。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/store"
)

// runNotifications 使用独立于续期扫描的周期发送告警并检查运维状态。
func (s *Services) runNotifications(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var lastScan time.Time
	for {
		if s.Config.AlertWebhookURL != "" {
			now := time.Now().UTC()
			if now.Sub(lastScan) >= time.Minute {
				if err := s.scanNotifications(ctx, now); err != nil {
					s.logger.Error("扫描运维告警失败", "error", err)
				} else {
					lastScan = now
				}
			}
			s.dispatchAlerts(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// dispatchAlerts 每轮至多投递五个事件，每次请求十秒超时，避免大量失败阻塞状态扫描。
func (s *Services) dispatchAlerts(ctx context.Context) {
	for index := 0; index < 5 && ctx.Err() == nil; index++ {
		value, err := s.Store.ClaimAlert(ctx, time.Now().UTC())
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			s.logger.Error("认领告警失败", "error", err)
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = s.sendAlert(sendCtx, value.AlertEvent)
		cancel()
		failure := ""
		if err != nil {
			failure = err.Error()
		}
		// 即使服务退出导致发送取消，也用短上下文保存重试状态。
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
		finishErr := s.Store.FinishAlert(finishCtx, value, failure, time.Now().UTC())
		finishCancel()
		if finishErr != nil {
			s.logger.Error("保存告警投递结果失败", "alert_id", value.ID, "error", finishErr)
		}
	}
}

// scanNotifications 每日最多创建一次同资源同级别提醒，恶化为临期或过期时立即生成新事件。
func (s *Services) scanNotifications(ctx context.Context, now time.Time) error {
	certificates, err := s.Store.MonitoredCertificates(ctx, s.Config.RenewBefore, 0)
	if err != nil {
		return err
	}
	for _, certificate := range certificates {
		remaining := certificate.NotAfter.Sub(now)
		if remaining > s.Config.RenewBefore && remaining > 7*24*time.Hour {
			continue
		}
		event, severity, description := "certificate.expiring", "WARNING", "证书进入续期窗口"
		if remaining <= 0 {
			event, severity, description = "certificate.expired", "ERROR", "在用证书已过期"
		} else if remaining <= 7*24*time.Hour {
			event, severity, description = "certificate.expiry_critical", "ERROR", "在用证书将在七天内过期"
		}
		message := fmt.Sprintf("%s：%s · v%d，到期时间 %s", description, certificate.CommonName, certificate.Version, certificate.NotAfter.Format(time.RFC3339))
		key := fmt.Sprintf("%s:%d:%s", event, certificate.ID, now.Format("2006-01-02"))
		if err := s.Store.EnqueueAlert(ctx, Alert{Event: event, Severity: severity, ResourceType: "CERTIFICATE", ResourceID: formatID(certificate.ID), Message: message, Timestamp: now}, key); err != nil {
			return err
		}
	}
	agents, err := s.Store.ListAgents(ctx)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if !agent.Enabled {
			continue
		}
		lastSeen := agent.CreatedAt
		if agent.LastSeenAt != nil {
			lastSeen = *agent.LastSeenAt
		}
		if now.Sub(lastSeen) <= config.AgentOfflineAfter {
			continue
		}
		key := "agent.offline:" + agent.ID + ":" + lastSeen.Format(time.RFC3339Nano) + ":" + now.Format("2006-01-02")
		if err := s.Store.EnqueueAlert(ctx, Alert{Event: "agent.offline", Severity: "WARNING", ResourceType: "AGENT", ResourceID: agent.ID, Message: "Agent " + agent.Name + " 超过两分钟未与控制面通信", Timestamp: now}, key); err != nil {
			return err
		}
	}
	return nil
}

// TestAlert 创建可追踪的测试事件，不把入队成功误报为通知已送达。
func (s *Services) TestAlert(ctx context.Context, userID int64) error {
	if s.Config.AlertWebhookURL == "" {
		return Conflict("尚未配置 Webhook 地址和签名密钥")
	}
	if err := s.Store.EnqueueAlert(ctx, Alert{Event: "system.notification.test", Severity: "INFO", ResourceType: "SYSTEM", ResourceID: "server", Message: "Acme4go 通知连接测试，请在投递记录中确认结果"}, ""); err != nil {
		return s.internalError("创建测试通知失败", err)
	}
	s.RecordAudit(ctx, &userID, "TEST", "ALERT_DELIVERY", "", "创建通知连接测试")
	return nil
}

// RetryAlert 重新排队失败告警并保留管理员操作审计。
func (s *Services) RetryAlert(ctx context.Context, userID, id int64) error {
	if s.Config.AlertWebhookURL == "" {
		return Conflict("尚未配置 Webhook 地址和签名密钥")
	}
	if err := s.Store.RetryAlert(ctx, id); errors.Is(err, store.ErrNotFound) {
		return Conflict("仅失败或等待重试的通知可以重新发送")
	} else if err != nil {
		return s.internalError("重新发送通知失败", err)
	}
	s.RecordAudit(ctx, &userID, "RETRY", "ALERT_DELIVERY", formatID(id), "重新排队告警通知")
	return nil
}
