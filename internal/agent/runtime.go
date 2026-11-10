// 本文件将节点心跳与可能长时间执行的部署对账隔离，并统一取消与退出顺序。
//
// @author Andersen
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
)

// runAgentLoops 独立运行固定心跳与可退避的部署轮询；退出时等待心跳请求取消完成。
func runAgentLoops(ctx context.Context, reconcile func() bool, sendHeartbeat func(context.Context) error, pollInterval, heartbeatInterval time.Duration, logger *slog.Logger) error {
	if pollInterval <= 0 {
		pollInterval = config.AgentHeartbeatInterval
	}
	if heartbeatInterval <= 0 {
		heartbeatInterval = config.AgentHeartbeatInterval
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	// 心跳独占自己的周期，即使部署或身份续签阻塞也继续上报。
	go func() {
		defer close(heartbeatDone)
		runHeartbeats(heartbeatCtx, sendHeartbeat, heartbeatInterval, logger)
	}()
	// 取消正在发送的心跳并等待退出，避免 Run 返回后仍持有网络请求。
	defer func() { stopHeartbeat(); <-heartbeatDone }()
	timer := time.NewTimer(0)
	defer timer.Stop()
	nextInterval := pollInterval
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			if ctx.Err() != nil {
				return nil
			}
			if reconcile() {
				nextInterval = pollInterval
			} else {
				nextInterval = doubledInterval(nextInterval)
			}
			timer.Reset(jitteredInterval(nextInterval))
		}
	}
}

// runHeartbeats 每轮最多等待十秒，不使用部署的退避或共享超时上下文。
func runHeartbeats(ctx context.Context, send func(context.Context) error, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := send(requestCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			logger.Warn("Agent 心跳失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
