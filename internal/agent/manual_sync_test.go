// 本文件验证远程手动请求的开始确认、相同证书强制重部署及重启补报。
//
// @author Andersen
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/store"
)

// TestManualSyncStartsAndResumesReceipt 验证新版手动请求重新部署同一证书，回执丢失后只补报。
func TestManualSyncStartsAndResumesReceipt(t *testing.T) {
	bundle := testAgentBundle(t)
	target := store.DesiredTarget{ID: 1, Revision: 8, ManualSync: true, TargetAlias: "manual", Domains: bundle.SANs, Certificate: &store.CertificateDescriptor{ID: 1, Fingerprint: bundle.Fingerprint, DownloadURL: "https://test.invalid/bundle"}}
	downloads, starts, finishes := 0, 0, 0
	failFinish, rejectStart := true, false
	// 模拟控制面只接受当前手动修订，并丢失首个终态响应。
	client := &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/agent/v1/desired-state":
			return agentTestResponse(200, desiredState{Revision: target.Revision, Targets: []store.DesiredTarget{target}}), nil
		case "/bundle":
			downloads++
			return agentTestResponse(200, bundle), nil
		default:
			var result app.DeploymentResultInput
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result.Status == "RUNNING" {
				starts++
				if rejectStart {
					return agentTestResponse(409, nil), nil
				}
			} else if result.Status == "SUCCESS" {
				finishes++
				if failFinish {
					return agentTestResponse(503, nil), nil
				}
			} else {
				t.Fatalf("意外回执 %s", result.Status)
			}
			return agentTestResponse(200, nil), nil
		}
	})}
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("m", 32)))
	if err != nil {
		t.Fatal(err)
	}
	storage := deploy.NewStorage(t.TempDir(), vault)
	cfg := config.Config{DeploymentDir: t.TempDir()}
	if _, err := deploy.Materialize(cfg.DeploymentDir, target.TargetAlias, 1, bundle); err != nil {
		t.Fatal(err)
	}
	state := localState{Targets: map[string]string{"1": fmt.Sprintf("7:%s:manual", bundle.Fingerprint)}}
	if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &state); err == nil {
		t.Fatal("丢失的回执没有保留")
	}
	if downloads != 1 || starts != 1 || state.PendingReports["1"].Status != "SUCCESS" {
		t.Fatal("手动部署流程或待发送回执错误")
	}
	content, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored localState
	if err := json.Unmarshal(content, &restored); err != nil {
		t.Fatal(err)
	}
	failFinish = false
	if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &restored); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 || starts != 1 || finishes != 2 || len(restored.PendingReports) != 0 {
		t.Fatal("重启补报重复执行了单次请求")
	}
	target.Revision++
	rejectStart = true
	if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &restored); err == nil {
		t.Fatal("已撤销请求仍然开始执行")
	}
	if downloads != 1 {
		t.Fatal("开始确认失败后仍下载证书")
	}
}
