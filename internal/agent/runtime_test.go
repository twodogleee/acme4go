// 本文件验证独立心跳在慢部署、轮询退避和退出时的运行边界。
//
// @author Andersen
package agent

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// TestHeartbeatContinuesDuringBlockedDeployment 验证长时间部署与很长的轮询间隔均不阻塞心跳。
func TestHeartbeatContinuesDuringBlockedDeployment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := make(chan struct{})
	var heartbeats atomic.Int32
	// 部署持续阻塞直到心跳独立运行三次后取消整个节点。
	reconcile := func() bool { close(started); <-ctx.Done(); return false }
	// 只统计部署已经开始后的心跳，避免把启动竞争当作隔离成功。
	send := func(requestCtx context.Context) error {
		select {
		case <-started:
		case <-requestCtx.Done():
			return requestCtx.Err()
		}
		if heartbeats.Add(1) == 3 {
			cancel()
		}
		return nil
	}
	if err := runAgentLoops(ctx, reconcile, send, 10*time.Minute, 5*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if heartbeats.Load() < 3 || ctx.Err() != context.Canceled {
		t.Fatalf("慢部署阻塞了独立心跳：count=%d err=%v", heartbeats.Load(), ctx.Err())
	}
}

// TestHeartbeatRequestStopsBeforeLoopReturns 验证退出会取消独立请求并等待协程结束。
func TestHeartbeatRequestStopsBeforeLoopReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	finished := make(chan struct{})
	// 心跳请求模拟一直等待网络直到节点退出。
	send := func(requestCtx context.Context) error {
		cancel()
		<-requestCtx.Done()
		close(finished)
		return requestCtx.Err()
	}
	// 对账不参与取消，验证心跳自身使用父生命周期。
	reconcile := func() bool { return true }
	if err := runAgentLoops(ctx, reconcile, send, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("退出后仍有心跳请求")
	}
}
