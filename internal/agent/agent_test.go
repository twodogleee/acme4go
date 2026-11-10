// 本文件以隔离传输验证 Agent 失败传播、回执落盘和重启补报，不访问真实控制面。
//
// @author Andersen
package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
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

// testTransport 将 HTTP 请求交给内存回调。
type testTransport func(*http.Request) (*http.Response, error)

// RoundTrip 实现无需监听端口的测试 HTTP 传输。
func (f testTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// agentTestResponse 生成符合控制面响应信封的内存结果。
func agentTestResponse(status int, data any) *http.Response {
	body, _ := json.Marshal(map[string]any{"success": status == 200, "data": data, "message": "测试响应"})
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

// TestReconcilePropagatesDownloadAndReportFailure 验证失败上报也失败时不推进成功修订且保留回执。
func TestReconcilePropagatesDownloadAndReportFailure(t *testing.T) {
	target := store.DesiredTarget{ID: 1, Revision: 7, TargetAlias: "test", Domains: []string{"example.com"}, Certificate: &store.CertificateDescriptor{ID: 1, Fingerprint: "synthetic", DownloadURL: "https://test.invalid/bundle"}}
	// 期望状态成功，但材料下载和回执均模拟服务不可用。
	client := &http.Client{Transport: testTransport(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/agent/v1/desired-state" {
			return agentTestResponse(200, desiredState{Revision: 7, Targets: []store.DesiredTarget{target}}), nil
		}
		return agentTestResponse(503, nil), nil
	})}
	state := localState{Targets: make(map[string]string)}
	err := reconcileState(context.Background(), client, "https://test.invalid", config.Config{}, nil, &state)
	if err == nil || state.Revision != 0 || len(state.PendingReports) != 1 {
		t.Fatalf("失败被吞掉或回执丢失：%+v %v", state, err)
	}
}

// TestSuccessfulDeploymentResendsReceiptAfterRestart 验证文件已成功落地但回执失败时重启仅补报而不重复下载。
func TestSuccessfulDeploymentResendsReceiptAfterRestart(t *testing.T) {
	bundle := testAgentBundle(t)
	target := store.DesiredTarget{ID: 1, Revision: 7, TargetAlias: "test", Domains: bundle.SANs, Certificate: &store.CertificateDescriptor{ID: 1, Fingerprint: bundle.Fingerprint, DownloadURL: "https://test.invalid/bundle"}}
	failReport, downloads := true, 0
	// 下载返回合法合成证书，第一轮回执失败，恢复后只应补报。
	client := &http.Client{Transport: testTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/agent/v1/desired-state":
			return agentTestResponse(200, desiredState{Revision: 7, Targets: []store.DesiredTarget{target}}), nil
		case "/bundle":
			downloads++
			return agentTestResponse(200, bundle), nil
		default:
			var receipt app.DeploymentResultInput
			if err := json.NewDecoder(request.Body).Decode(&receipt); err != nil || receipt.Revision != target.Revision {
				t.Errorf("回执没有携带执行修订：%+v %v", receipt, err)
				return agentTestResponse(400, nil), nil
			}
			if failReport {
				return agentTestResponse(503, nil), nil
			}
			return agentTestResponse(200, nil), nil
		}
	})}
	vault, err := cryptoutil.NewVault([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	storage := deploy.NewStorage(t.TempDir(), vault)
	cfg := config.Config{DeploymentDir: t.TempDir()}
	state := localState{Targets: make(map[string]string)}
	if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &state); err == nil {
		t.Fatal("回执失败必须传递")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := atomicJSON(path, state, 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var restored localState
	if err := json.Unmarshal(content, &restored); err != nil {
		t.Fatal(err)
	}
	failReport = false
	if err := reconcileState(context.Background(), client, "https://test.invalid", cfg, storage, &restored); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 || len(restored.PendingReports) != 0 || restored.Revision != 7 {
		t.Fatalf("补报发生重复下载或状态未收敛：downloads=%d state=%+v", downloads, restored)
	}
}

// testAgentBundle 生成只用于临时目录的有效证书和密钥对。
func testAgentBundle(t *testing.T) deploy.Bundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.com"}, DNSNames: []string{"example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	fingerprint := sha256.Sum256(der)
	return deploy.Bundle{Certificate: certificate, FullChain: certificate, PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), Fingerprint: strings.ToUpper(hex.EncodeToString(fingerprint[:])), CommonName: "example.com", SANs: []string{"example.com"}, NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter}
}
