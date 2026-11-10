// 本文件验证磁盘自行恢复后的成功回执以及失败补报的持久化恢复。
//
// @author Andersen
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestHealedFilesRestoreSuccess 验证实际文件恢复后，Agent 清理重试并向控制面确认成功。
func TestHealedFilesRestoreSuccess(t *testing.T) {
	testHealedFilesRestoreSuccess(t, false)
}

// TestHealedSuccessReportSurvivesRestart 验证恢复成功回执丢失后仍持久化，重启补报不重复下载或发送旧失败。
func TestHealedSuccessReportSurvivesRestart(t *testing.T) {
	testHealedFilesRestoreSuccess(t, true)
}

// testHealedFilesRestoreSuccess 驱动成功、损坏、下载失败、文件恢复及可选的回执丢失场景。
func testHealedFilesRestoreSuccess(t *testing.T, loseRecoveryReport bool) {
	t.Helper()
	bundle := testAgentBundle(t)
	target := store.DesiredTarget{ID: 1, Revision: 7, TargetAlias: "recovery", Domains: bundle.SANs, Certificate: &store.CertificateDescriptor{ID: 1, Fingerprint: bundle.Fingerprint, DownloadURL: "https://test.invalid/bundle"}}
	failDownload := false
	lastStatus := ""
	reports := 0
	downloads := 0
	rejectSuccess := false
	// 内存传输保留实际回执，下载故障不影响控制面接收失败状态。
	client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/agent/v1/desired-state":
			return agentTestResponse(200, desiredState{Revision: 7, Targets: []store.DesiredTarget{target}}), nil
		case "/bundle":
			downloads++
			if failDownload {
				return agentTestResponse(503, nil), nil
			}
			return agentTestResponse(200, bundle), nil
		default:
			var report app.DeploymentResultInput
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Fatal(err)
			}
			if report.Status == "SUCCESS" && rejectSuccess {
				return agentTestResponse(503, nil), nil
			}
			lastStatus = report.Status
			reports++
			return agentTestResponse(200, nil), nil
		}
	})}
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	storage := deploy.NewStorage(t.TempDir(), vault)
	cfg := config.Config{DeploymentDir: t.TempDir()}
	state := localState{Targets: map[string]string{}}
	if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &state); err != nil {
		t.Fatal(err)
	}
	chainPath := filepath.Join(cfg.DeploymentDir, "recovery", "current", "fullchain.pem")
	if err := os.WriteFile(chainPath, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	failDownload = true
	if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &state); err == nil {
		t.Fatal("未进入失败场景")
	}
	if lastStatus != "FAILED" {
		t.Fatal("失败回执未送达")
	}
	if err := os.WriteFile(chainPath, bundle.FullChain, 0600); err != nil {
		t.Fatal(err)
	}
	failDownload = false
	retry := state.TargetRetries["1"]
	retry.NextAttemptAt = time.Now().Add(-time.Second)
	state.TargetRetries["1"] = retry
	if loseRecoveryReport {
		rejectSuccess = true
		if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &state); err == nil {
			t.Fatal("恢复回执丢失未被报告")
		}
		if state.PendingReports["1"].Status != "SUCCESS" || len(state.TargetRetries) == 0 {
			t.Fatal("未确认的恢复回执没有保留")
		}
		path := filepath.Join(t.TempDir(), "state.json")
		if err := atomicJSON(path, state, 0o600); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		state = localState{}
		if err := json.Unmarshal(content, &state); err != nil {
			t.Fatal(err)
		}
		rejectSuccess = false
	}
	if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &state); err != nil {
		t.Fatal(err)
	}
	if lastStatus != "SUCCESS" || len(state.TargetRetries) != 0 || len(state.PendingReports) != 0 || downloads != 2 || reports != 3 {
		t.Fatalf("文件已恢复且对账返回成功，但最后回执仍为 %s，共 %d 次上报", lastStatus, reports)
	}
}
