// Package agent 实现无入站端口的证书期望状态拉取和原子物化运行时。
//
// @author Andersen
package agent

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/twodogleee/acme4go/internal/app"
	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/store"
)

// identity 是 Agent 持久化的 mTLS 身份和控制平面信任材料。
type identity struct {
	AgentID     string `json:"agentId"`     // AgentID 是控制平面分配的节点身份标识。
	ServerURL   string `json:"serverUrl"`   // ServerURL 是控制平面 Agent API 基准地址。
	Certificate string `json:"certificate"` // Certificate 是客户端证书 PEM。
	PrivateKey  string `json:"privateKey"`  // PrivateKey 是客户端私钥 PEM。
	CAPEM       string `json:"caPem"`       // CAPEM 是控制平面内部根证书 PEM。
}

// localState 保存各目标最近一次成功应用的期望状态键。
type localState struct {
	Targets        map[string]string        `json:"targets"`                  // Targets 按目标主键记录修订、指纹和目录别名组合键。
	Revision       int64                    `json:"revision"`                 // Revision 是最近一次完整应用的服务端期望状态修订。
	PendingReports map[string]pendingReport `json:"pendingReports,omitempty"` // PendingReports 保存未被控制面确认的最新目标回执。
	TargetRetries  map[string]targetRetry   `json:"targetRetries,omitempty"`  // TargetRetries 单独限制失败目标重试，避免影响心跳。
}

// targetRetry 为单个目标维护退避时间，证书或配置变更时立即重新尝试。
type targetRetry struct {
	StateKey      string    `json:"stateKey"`      // StateKey 是本次失败对应的配置和版本。
	Attempts      int       `json:"attempts"`      // Attempts 是该版本连续失败次数。
	NextAttemptAt time.Time `json:"nextAttemptAt"` // NextAttemptAt 是下一次材料处理时间。
}

// deploymentFailures 表示控制面可以通信但部分目标尚未收敛，不延迟正常心跳。
type deploymentFailures struct {
	Cause error // Cause 聚合目标处理和回执传输错误。
}

// Error 返回可写入运行日志的安全失败摘要。
func (e *deploymentFailures) Error() string { return e.Cause.Error() }

// pendingReport 是可在进程重启后补报的非敏感部署结果。
type pendingReport struct {
	Target  store.DesiredTarget `json:"target"`  // Target 是回执对应的配置快照，用于拒绝过期回执。
	Status  string              `json:"status"`  // Status 是 SUCCESS 或 FAILED。
	Message string              `json:"message"` // Message 是固定安全说明，不包含证书材料。
}

// desiredState 是控制平面返回的 Agent 完整期望状态。
type desiredState struct {
	Revision int64                 `json:"revision"` // Revision 是本次状态快照的服务端修订标识。
	Targets  []store.DesiredTarget `json:"targets"`  // Targets 是当前节点应维护的全部部署目标。
}

// Run 完成首次注册后持续拉取并应用期望证书状态。
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	directory := filepath.Join(cfg.DataDir, "agent")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	identityPath := filepath.Join(directory, "identity.json")
	identityValue, err := loadIdentity(identityPath)
	if errors.Is(err, os.ErrNotExist) {
		identityValue, err = enroll(ctx, cfg, logger)
		if err != nil {
			return err
		}
		if err := atomicJSON(identityPath, identityValue, 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	client, err := authenticatedClient(identityValue)
	if err != nil {
		return err
	}
	masterKeyPath := filepath.Join(directory, "master.key")
	masterKey, err := loadOrCreateMasterKey(masterKeyPath)
	if err != nil {
		return err
	}
	vault, err := cryptoutil.NewVault(masterKey)
	if err != nil {
		return err
	}
	storage := deploy.NewStorage(directory, vault)
	statePath := filepath.Join(directory, "state.json")
	state := localState{Targets: map[string]string{}}
	if content, readErr := os.ReadFile(statePath); readErr == nil {
		_ = json.Unmarshal(content, &state)
	}
	if state.Targets == nil {
		state.Targets = map[string]string{}
	}
	var heartbeatClient atomic.Pointer[http.Client]
	heartbeatClient.Store(client)
	serverURL := identityValue.ServerURL

	// reconcile 续签身份并应用期望状态，独立心跳通过原子快照取得新的 mTLS 客户端。
	reconcile := func() bool {
		cycleCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		succeeded := true
		var renewErr error
		client, renewErr = renewIdentityIfDue(cycleCtx, client, &identityValue, identityPath)
		heartbeatClient.Store(client)
		if renewErr != nil {
			logger.Warn("Agent 身份证书续签失败，将继续使用当前证书", "error", renewErr)
			succeeded = false
		}
		if err := reconcileState(cycleCtx, client, identityValue.ServerURL, cfg, storage, &state); err != nil {
			logger.Error("Agent 对账失败", "error", err)
			var targetErrors *deploymentFailures
			if !errors.As(err, &targetErrors) {
				succeeded = false
			}
		}
		if err := atomicJSON(statePath, state, 0o600); err != nil {
			logger.Error("保存 Agent 状态失败", "error", err)
			return false
		}
		historyRetention := cfg.HistoryRetention
		if historyRetention <= 0 {
			historyRetention = 730 * 24 * time.Hour
		}
		if removed, err := deploy.PruneMaterializedVersions(cfg.DeploymentDir, time.Now().Add(-historyRetention), 3); err != nil {
			logger.Warn("清理 Agent 部署历史版本失败", "error", err)
		} else if removed > 0 {
			logger.Info("已清理 Agent 部署历史版本", "count", removed)
		}
		return succeeded
	}
	logger.Info("Agent 已启动", "agent_id", identityValue.AgentID, "server", identityValue.ServerURL)
	// 心跳只读取不可变连接快照，不等待证书下载、磁盘操作或身份续签。
	sendHeartbeat := func(heartbeatCtx context.Context) error {
		return heartbeat(heartbeatCtx, heartbeatClient.Load(), serverURL, cfg.Version)
	}
	return runAgentLoops(ctx, reconcile, sendHeartbeat, cfg.AgentPollInterval, config.AgentHeartbeatInterval, logger)
}

// enroll 使用一次性加入链接生成密钥、提交 CSR 并取得初始 mTLS 身份。
func enroll(ctx context.Context, cfg config.Config, logger *slog.Logger) (identity, error) {
	join, err := app.ParseJoinURL(cfg.AgentJoinURL)
	if err != nil {
		return identity{}, err
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return identity{}, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cfg.AgentName}}, privateKey)
	if err != nil {
		return identity{}, err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(join.CAPEM)) {
		return identity{}, errors.New("Join 信息中的内部 CA 无效")
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}}}
	input := app.EnrollInput{Token: join.Token, CSR: string(csrPEM), Version: cfg.Version, Platform: runtime.GOOS + "/" + runtime.GOARCH}
	var result app.EnrollResult
	if err := requestJSON(ctx, client, http.MethodPost, join.ServerURL+"/agent/v1/enroll", input, &result); err != nil {
		return identity{}, fmt.Errorf("Agent 注册失败: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return identity{}, err
	}
	logger.Info("Agent 注册成功", "agent_id", result.AgentID)
	return identity{
		AgentID: result.AgentID, ServerURL: join.ServerURL, Certificate: result.Certificate,
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), CAPEM: result.CACertificate,
	}, nil
}

// authenticatedClient 使用持久化身份构造强制 TLS 1.3 的 mTLS 客户端。
func authenticatedClient(value identity) (*http.Client, error) {
	certificate, err := tls.X509KeyPair([]byte(value.Certificate), []byte(value.PrivateKey))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(value.CAPEM)) {
		return nil, errors.New("Agent CA 证书无效")
	}
	return &http.Client{
		Timeout:   90 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{certificate}}},
	}, nil
}

// reconcileState 拉取期望状态并验证、缓存、物化和上报每个变更目标。
func reconcileState(ctx context.Context, client *http.Client, serverURL string, cfg config.Config, storage *deploy.Storage, state *localState) error {
	var desired desiredState
	if err := requestJSON(ctx, client, http.MethodGet, serverURL+"/agent/v1/desired-state", nil, &desired); err != nil {
		return err
	}
	desiredTargetIDs := make(map[string]struct{}, len(desired.Targets))
	if state.PendingReports == nil {
		state.PendingReports = make(map[string]pendingReport)
	}
	if state.TargetRetries == nil {
		state.TargetRetries = make(map[string]targetRetry)
	}
	var failures []error
	for _, target := range desired.Targets {
		targetID := fmt.Sprint(target.ID)
		desiredTargetIDs[targetID] = struct{}{}
		if pending, exists := state.PendingReports[targetID]; exists {
			if target.Certificate == nil || pending.Target.Certificate == nil || pending.Target.Revision != target.Revision || pending.Target.Certificate.Fingerprint != target.Certificate.Fingerprint {
				delete(state.PendingReports, targetID)
			} else if err := reportDeployment(ctx, client, serverURL, target, pending.Status, pending.Message); err != nil {
				failures = append(failures, fmt.Errorf("目标 %d 的待发送回执补报失败", target.ID))
				if target.ManualSync {
					continue
				}
			} else {
				delete(state.PendingReports, targetID)
				if pending.Status == "SUCCESS" {
					delete(state.TargetRetries, targetID)
				}
				// 单次任务终态已确认，本轮不能再使用拉取时的旧授权执行。
				if target.ManualSync {
					continue
				}
			}
		}
		if target.Certificate == nil {
			delete(state.TargetRetries, targetID)
			continue
		}
		stateKey := fmt.Sprintf("%d:%s:%s", target.Revision, target.Certificate.Fingerprint, target.TargetAlias)
		if retry, exists := state.TargetRetries[targetID]; exists && retry.StateKey == stateKey && time.Now().Before(retry.NextAttemptAt) {
			failures = append(failures, fmt.Errorf("目标 %d 等待下次重试", target.ID))
			continue
		}
		if state.Targets[fmt.Sprint(target.ID)] == stateKey && deploy.ValidateMaterialized(cfg.DeploymentDir, target.TargetAlias, target.Domains, target.Certificate.Fingerprint) == nil {
			// 文件可能由外部修复；曾经失败的目标必须让控制面确认恢复，不能仅跳过物化。
			if _, recovering := state.TargetRetries[targetID]; recovering {
				if err := queueDeploymentReport(ctx, client, serverURL, target, "SUCCESS", "证书文件已恢复并通过校验", state); err != nil {
					failures = append(failures, err)
				} else {
					delete(state.TargetRetries, targetID)
				}
			}
			continue
		}
		if target.ManualSync {
			// 开始前让控制面确认本次修订仍有效；失败时等待下次拉取。
			if err := reportDeployment(ctx, client, serverURL, target, "RUNNING", "Agent 已接收手动同步请求"); err != nil {
				failures = append(failures, fmt.Errorf("目标 %d 无法确认手动同步请求", target.ID))
				continue
			}
		}
		var bundle deploy.Bundle
		if err := requestJSON(ctx, client, http.MethodGet, target.Certificate.DownloadURL, nil, &bundle); err != nil {
			failures = append(failures, queueDeploymentReport(ctx, client, serverURL, target, "FAILED", "证书下载失败", state))
			continue
		}
		if err := deploy.ValidateBundleDomains(bundle, target.Domains, target.Certificate.Fingerprint, true); err != nil {
			failures = append(failures, queueDeploymentReport(ctx, client, serverURL, target, "FAILED", "证书材料验证失败", state))
			continue
		}
		certPath, chainPath, keyPath, err := storage.Save(bundle)
		if err != nil {
			failures = append(failures, queueDeploymentReport(ctx, client, serverURL, target, "FAILED", "证书加密缓存失败", state))
			continue
		}
		cached, err := storage.Load(certPath, chainPath, keyPath, bundle.Fingerprint, bundle.CommonName)
		if err != nil {
			failures = append(failures, queueDeploymentReport(ctx, client, serverURL, target, "FAILED", "证书加密缓存校验失败", state))
			continue
		}
		cached.SANs, cached.NotAfter = bundle.SANs, bundle.NotAfter
		if _, err := deploy.Materialize(cfg.DeploymentDir, target.TargetAlias, target.Certificate.ID, cached); err != nil {
			failures = append(failures, queueDeploymentReport(ctx, client, serverURL, target, "FAILED", "本地 PEM 原子物化失败", state))
			continue
		}
		state.Targets[targetID] = stateKey
		delete(state.TargetRetries, targetID)
		if err := queueDeploymentReport(ctx, client, serverURL, target, "SUCCESS", "证书已原子部署", state); err != nil {
			failures = append(failures, err)
		}
	}
	for targetID := range state.Targets {
		if _, exists := desiredTargetIDs[targetID]; !exists {
			delete(state.Targets, targetID)
		}
	}
	for targetID := range state.PendingReports {
		if _, exists := desiredTargetIDs[targetID]; !exists {
			delete(state.PendingReports, targetID)
		}
	}
	for targetID := range state.TargetRetries {
		if _, exists := desiredTargetIDs[targetID]; !exists {
			delete(state.TargetRetries, targetID)
		}
	}
	if len(failures) > 0 {
		return &deploymentFailures{Cause: errors.Join(failures...)}
	}
	state.Revision = desired.Revision
	return nil
}

// queueDeploymentReport 保留最新回执直到服务端确认，部署失败即使已上报也会传递给外层。
func queueDeploymentReport(ctx context.Context, client *http.Client, serverURL string, target store.DesiredTarget, status, message string, state *localState) error {
	targetID := fmt.Sprint(target.ID)
	if status == "FAILED" && target.Certificate != nil {
		stateKey := fmt.Sprintf("%d:%s:%s", target.Revision, target.Certificate.Fingerprint, target.TargetAlias)
		retry := state.TargetRetries[targetID]
		if retry.StateKey != stateKey {
			retry = targetRetry{StateKey: stateKey}
		}
		retry.Attempts++
		delay := 30 * time.Second
		for attempt := 1; attempt < retry.Attempts && delay < 5*time.Minute; attempt++ {
			delay = doubledInterval(delay)
		}
		retry.NextAttemptAt = time.Now().Add(delay)
		state.TargetRetries[targetID] = retry
	}
	state.PendingReports[targetID] = pendingReport{Target: target, Status: status, Message: message}
	if err := reportDeployment(ctx, client, serverURL, target, status, message); err != nil {
		return fmt.Errorf("目标 %d：%s；回执等待补报", target.ID, message)
	}
	delete(state.PendingReports, targetID)
	if status == "FAILED" {
		return fmt.Errorf("目标 %d：%s", target.ID, message)
	}
	return nil
}

// heartbeat 向控制平面上报当前 Agent 版本和运行平台。
func heartbeat(ctx context.Context, client *http.Client, serverURL, version string) error {
	return requestJSON(ctx, client, http.MethodPost, serverURL+"/agent/v1/heartbeat", map[string]string{"version": version, "platform": runtime.GOOS + "/" + runtime.GOARCH}, nil)
}

// renewIdentityIfDue 在证书进入三十天窗口时原子切换新的客户端证书。
func renewIdentityIfDue(ctx context.Context, client *http.Client, value *identity, identityPath string) (*http.Client, error) {
	certificateBlock, _ := pem.Decode([]byte(value.Certificate))
	if certificateBlock == nil {
		return client, errors.New("Agent 身份证书 PEM 无效")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		return client, err
	}
	if certificate.NotAfter.After(time.Now().UTC().Add(30 * 24 * time.Hour)) {
		return client, nil
	}
	keyBlock, _ := pem.Decode([]byte(value.PrivateKey))
	if keyBlock == nil {
		return client, errors.New("Agent 身份私钥 PEM 无效")
	}
	privateValue, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return client, err
	}
	signer, ok := privateValue.(crypto.Signer)
	if !ok {
		return client, errors.New("Agent 身份私钥不支持签名")
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: value.AgentID}}, signer)
	if err != nil {
		return client, err
	}
	var result app.RenewAgentIdentityResult
	input := app.RenewAgentIdentityInput{CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))}
	if err := requestJSON(ctx, client, http.MethodPost, value.ServerURL+"/agent/v1/identity/renew", input, &result); err != nil {
		return client, err
	}
	updated := *value
	updated.Certificate = result.Certificate
	if err := atomicJSON(identityPath, updated, 0o600); err != nil {
		return client, err
	}
	newClient, err := authenticatedClient(updated)
	if err != nil {
		return client, err
	}
	*value = updated
	return newClient, nil
}

// reportDeployment 向控制平面提交目标、证书和指纹绑定的执行结果。
func reportDeployment(ctx context.Context, client *http.Client, serverURL string, target store.DesiredTarget, status, message string) error {
	if target.Certificate == nil {
		return nil
	}
	return requestJSON(ctx, client, http.MethodPost, serverURL+"/agent/v1/deployments/result", app.DeploymentResultInput{TargetID: target.ID, Revision: target.Revision, CertificateID: target.Certificate.ID, Fingerprint: target.Certificate.Fingerprint, Status: status, Message: message}, nil)
}

// requestJSON 发送带上下文的 JSON 请求并解析控制平面统一响应信封。
func requestJSON(ctx context.Context, client *http.Client, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		content, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(content)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return err
	}
	var envelope json.RawMessage
	var base struct {
		Success bool            `json:"success"` // Success 表示服务端是否接受并完成请求。
		Data    json.RawMessage `json:"data"`    // Data 是延迟解析的业务响应数据。
		Message string          `json:"message"` // Message 是失败时的安全错误说明。
	}
	if err := json.Unmarshal(content, &base); err != nil {
		return fmt.Errorf("服务端返回无效 JSON（HTTP %d）", response.StatusCode)
	}
	envelope = base.Data
	if response.StatusCode < 200 || response.StatusCode >= 300 || !base.Success {
		return errors.New(base.Message)
	}
	if output != nil && len(envelope) > 0 && string(envelope) != "null" {
		return json.Unmarshal(envelope, output)
	}
	return nil
}

// loadIdentity 读取并完整性校验 Agent 身份文件。
func loadIdentity(path string) (identity, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return identity{}, err
	}
	var value identity
	if err := json.Unmarshal(content, &value); err != nil {
		return identity{}, err
	}
	if value.AgentID == "" || value.ServerURL == "" || value.Certificate == "" || value.PrivateKey == "" || value.CAPEM == "" {
		return identity{}, errors.New("Agent 身份文件不完整")
	}
	return value, nil
}

// loadOrCreateMasterKey 以独占方式创建或读取 Agent 本地缓存加密主密钥。
func loadOrCreateMasterKey(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err == nil {
		if len(content) != 32 {
			return nil, errors.New("Agent 本地主密钥长度无效")
		}
		return content, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	content = make([]byte, 32)
	if _, err := rand.Read(content); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	return content, file.Close()
}

// atomicJSON 经临时文件同步后原子替换 JSON 状态文件。
func atomicJSON(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".state-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(append(content, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}

// doubledInterval 对失败轮询执行有上限的指数退避。
func doubledInterval(value time.Duration) time.Duration {
	// maximum 是异常期间允许的最长轮询间隔。
	const maximum = 5 * time.Minute
	if value >= maximum/2 {
		return maximum
	}
	return value * 2
}

// jitteredInterval 为轮询间隔添加正负百分之十抖动，避免大量节点同时请求。
func jitteredInterval(value time.Duration) time.Duration {
	nanoseconds := time.Now().UnixNano()
	percent := float64(nanoseconds%21-10) / 100
	return time.Duration(float64(value) * (1 + percent))
}
