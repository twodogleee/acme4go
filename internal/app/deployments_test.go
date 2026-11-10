// 本文件验证本机执行器开关和服务关闭并发边界。
//
// @author Andersen
package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestServerWithoutLocalExecutorRejectsLocalTarget 验证纯控制平面不会创建或写入本机部署目录。
func TestServerWithoutLocalExecutorRejectsLocalTarget(t *testing.T) {
	directory := t.TempDir()
	deploymentDirectory := filepath.Join(directory, "deployments")
	services, err := NewServices(config.Config{
		Role:                   config.RoleServer,
		LocalDeploymentEnabled: false,
		Version:                "test",
		PublicURL:              "http://127.0.0.1:8080",
		AgentPublicURL:         "https://127.0.0.1:9443",
		DataDir:                filepath.Join(directory, "data"),
		DeploymentDir:          deploymentDirectory,
		MasterKey:              []byte(strings.Repeat("m", 32)),
		AdminUsername:          "admin",
		AdminPassword:          "correct-horse-battery",
		SessionTTL:             time.Hour,
		RenewBefore:            30 * 24 * time.Hour,
		SchedulerInterval:      time.Hour,
		AgentPollInterval:      30 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer services.Close()

	_, err = services.CreateTarget(context.Background(), 1, DeploymentTargetInput{
		ExecutorType:        store.ExecutorLocal,
		Name:                "本机 Nginx",
		CertificateSeriesID: "missing-series",
		TargetAlias:         "nginx-public",
		Enabled:             true,
	})
	var businessError *Error
	if !errors.As(err, &businessError) || businessError.Status != 400 {
		t.Fatalf("关闭本机执行器后应拒绝 LOCAL 目标，实际错误: %v", err)
	}
	if _, err := os.Stat(deploymentDirectory); !os.IsNotExist(err) {
		t.Fatalf("关闭本机执行器后不应创建部署目录，实际错误: %v", err)
	}
}

// TestCloseConcurrentWithOrderSubmission 验证关机与订单提交并发时不会发生 WaitGroup 竞态或提前关闭数据库。
func TestCloseConcurrentWithOrderSubmission(t *testing.T) {
	directory := t.TempDir()
	services, err := NewServices(config.Config{
		Role: config.RoleServer, Version: "test", LocalDeploymentEnabled: false,
		PublicURL: "http://127.0.0.1:8080", AgentPublicURL: "https://127.0.0.1:9443",
		DataDir: filepath.Join(directory, "data"), DeploymentDir: filepath.Join(directory, "deployments"),
		MasterKey: []byte(strings.Repeat("m", 32)), AdminUsername: "admin", AdminPassword: "correct-horse-battery",
		SessionTTL: time.Hour, RenewBefore: 30 * 24 * time.Hour, SchedulerInterval: time.Hour, AgentPollInterval: 30 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	services.StartBackground(ctx)

	var submitters sync.WaitGroup
	for orderID := int64(1); orderID <= 64; orderID++ {
		orderID := orderID
		submitters.Add(1)
		// 提交协程模拟关机期间并发进入的订单任务。
		go func() {
			defer submitters.Done()
			services.RunOrder(orderID)
		}()
	}
	cancel()
	closeResult := make(chan error, 1)
	// 关闭协程与订单提交并发执行以暴露生命周期竞态。
	go func() { closeResult <- services.Close() }()
	submitters.Wait()
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	if err := services.Close(); err != nil {
		t.Fatalf("Close 应可重复调用: %v", err)
	}
	services.RunOrder(999)
}
