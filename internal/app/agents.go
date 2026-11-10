// 本文件实现 Agent 一次性注册、mTLS 鉴权、期望状态和撤销流程。
//
// @author Andersen
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/store"
)

// JoinPayload 是一次性加入链接中携带的控制平面连接信息。
type JoinPayload struct {
	ServerURL string `json:"serverUrl"` // ServerURL 是 Agent mTLS 接口基准地址。
	Token     string `json:"token"`     // Token 是一次性注册令牌。
	CAPEM     string `json:"caPem"`     // CAPEM 是用于校验控制平面的内部根证书。
}

// Enrollment 是管理员创建的一次性 Agent 注册信息。
type Enrollment struct {
	JoinURL   string    `json:"joinUrl"`   // JoinURL 是可直接交给 Agent 使用的加入链接。
	ExpiresAt time.Time `json:"expiresAt"` // ExpiresAt 是注册令牌过期时间。
}

// EnrollInput 是 Agent 首次注册时提交的身份申请。
type EnrollInput struct {
	Token    string `json:"token"`    // Token 是一次性注册令牌。
	CSR      string `json:"csr"`      // CSR 是 Agent 生成的客户端证书签名请求。
	Version  string `json:"version"`  // Version 是 Agent 程序版本。
	Platform string `json:"platform"` // Platform 是 Agent 操作系统与架构。
}

// EnrollResult 是注册成功后返回的 Agent 身份证书材料。
type EnrollResult struct {
	AgentID       string `json:"agentId"`       // AgentID 是新节点的稳定身份标识。
	Certificate   string `json:"certificate"`   // Certificate 是签发的客户端证书 PEM。
	CACertificate string `json:"caCertificate"` // CACertificate 是内部根证书 PEM。
}

// RenewAgentIdentityInput 是 Agent 轮换客户端证书时提交的新 CSR。
type RenewAgentIdentityInput struct {
	CSR string `json:"csr"` // CSR 是使用新私钥生成的证书签名请求。
}

// RenewAgentIdentityResult 是 Agent 身份轮换后的新证书和有效期。
type RenewAgentIdentityResult struct {
	Certificate string    `json:"certificate"` // Certificate 是新客户端证书 PEM。
	ExpiresAt   time.Time `json:"expiresAt"`   // ExpiresAt 是新证书过期时间。
}

// DeploymentResultInput 是 Agent 上报的单次部署执行结果。
type DeploymentResultInput struct {
	TargetID      int64  `json:"targetId"`      // TargetID 是实际执行的部署目标主键。
	Revision      int64  `json:"revision"`      // Revision 必须匹配执行时的目标配置修订，旧 Agent 必须升级后上报。
	CertificateID int64  `json:"certificateId"` // CertificateID 是实际部署的证书主键。
	Fingerprint   string `json:"fingerprint"`   // Fingerprint 是实际部署的证书指纹。
	Status        string `json:"status"`        // Status 是 SUCCESS 或 FAILED。
	Message       string `json:"message"`       // Message 是执行结果说明。
}

// CreateEnrollment 为指定名称生成十分钟有效的一次性加入链接。
func (s *Services) CreateEnrollment(ctx context.Context, userID int64, name string) (Enrollment, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Enrollment{}, BadRequest("Agent 名称不能为空且不能超过 100 个字符")
	}
	token, err := cryptoutil.RandomToken(32)
	if err != nil {
		return Enrollment{}, s.internalError("生成 Agent 注册令牌失败", err)
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	if err := s.Store.CreateEnrollmentToken(ctx, cryptoutil.TokenHash(token), name, expiresAt); err != nil {
		return Enrollment{}, s.internalError("保存 Agent 注册令牌失败", err)
	}
	payload, _ := json.Marshal(JoinPayload{ServerURL: s.Config.AgentPublicURL, Token: token, CAPEM: string(s.PKI.CAPEM())})
	joinURL := "acme4go://join/" + base64.RawURLEncoding.EncodeToString(payload)
	s.RecordAudit(ctx, &userID, "CREATE", "AGENT_ENROLLMENT", "", "为 Agent "+name+" 创建一次性注册信息")
	return Enrollment{JoinURL: joinURL, ExpiresAt: expiresAt}, nil
}

// ParseJoinURL 解码并校验 acme4go 协议的一次性加入链接。
func ParseJoinURL(value string) (JoinPayload, error) {
	// prefix 是 Agent 一次性加入链接的固定协议头。
	const prefix = "acme4go://join/"
	if !strings.HasPrefix(value, prefix) {
		return JoinPayload{}, BadRequest("Agent Join URL 格式无效")
	}
	content, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return JoinPayload{}, BadRequest("Agent Join URL 编码无效")
	}
	var payload JoinPayload
	if err := json.Unmarshal(content, &payload); err != nil || payload.ServerURL == "" || payload.Token == "" || payload.CAPEM == "" {
		return JoinPayload{}, BadRequest("Agent Join URL 内容无效")
	}
	return payload, nil
}

// EnrollAgent 原子消费一次性令牌并为新 Agent 签发客户端证书。
func (s *Services) EnrollAgent(ctx context.Context, input EnrollInput) (EnrollResult, error) {
	var certificatePEM []byte
	// 注册创建回调在令牌事务内生成身份并签发客户端证书。
	agent, err := s.Store.ConsumeEnrollmentToken(ctx, cryptoutil.TokenHash(input.Token), time.Now(), func(name string) (store.Agent, error) {
		agentID, err := randomUUID()
		if err != nil {
			return store.Agent{}, err
		}
		var serial string
		var expires time.Time
		certificatePEM, serial, expires, err = s.PKI.SignAgentCSR(agentID, []byte(input.CSR))
		if err != nil {
			return store.Agent{}, err
		}
		return store.Agent{ID: agentID, Name: name, CertificateSerial: serial, CertificateExpiresAt: expires, Version: input.Version, Platform: input.Platform}, nil
	})
	if err != nil {
		return EnrollResult{}, Forbidden("注册令牌无效、已使用或已过期")
	}
	return EnrollResult{AgentID: agent.ID, Certificate: string(certificatePEM), CACertificate: string(s.PKI.CAPEM())}, nil
}

// AuthenticateAgent 将 mTLS 证书序列号与 Agent 身份、启用状态进行绑定校验。
func (s *Services) AuthenticateAgent(ctx context.Context, serial, commonName string) (store.Agent, error) {
	agent, err := s.Store.FindAgentBySerial(ctx, strings.ToLower(serial))
	if err != nil || agent.ID != commonName || !agent.Enabled {
		return store.Agent{}, Forbidden("Agent 身份无效或已撤销")
	}
	return agent, nil
}

// RenewAgentIdentity 使用仍然有效的旧 mTLS 身份签发并切换新的客户端证书。
func (s *Services) RenewAgentIdentity(ctx context.Context, agent store.Agent, input RenewAgentIdentityInput) (RenewAgentIdentityResult, error) {
	certificate, serial, expiresAt, err := s.PKI.SignAgentCSR(agent.ID, []byte(input.CSR))
	if err != nil {
		return RenewAgentIdentityResult{}, BadRequest("Agent CSR 无效")
	}
	presentedSerial := agent.AuthenticatedSerial
	if presentedSerial == "" {
		presentedSerial = agent.CertificateSerial
	}
	if err := s.Store.UpdateAgentCertificate(ctx, agent.ID, presentedSerial, serial, expiresAt); err != nil {
		return RenewAgentIdentityResult{}, s.internalError("切换 Agent 身份证书失败", err)
	}
	return RenewAgentIdentityResult{Certificate: string(certificate), ExpiresAt: expiresAt}, nil
}

// AgentDesiredState 返回当前 Agent 有权读取的完整部署期望状态。
func (s *Services) AgentDesiredState(ctx context.Context, agent store.Agent) ([]store.DesiredTarget, error) {
	values, err := s.Store.DesiredTargets(ctx, agent.ID, s.Config.AgentPublicURL, s.Config.RenewBefore)
	if err != nil {
		return nil, s.internalError("查询 Agent 期望状态失败", err)
	}
	return values, nil
}

// AgentCertificateBundle 仅向已分配对应证书的 Agent 返回证书材料。
func (s *Services) AgentCertificateBundle(ctx context.Context, agent store.Agent, certificateID int64) (deploy.Bundle, error) {
	allowed := false
	targets, err := s.Store.DesiredTargets(ctx, agent.ID, s.Config.AgentPublicURL, s.Config.RenewBefore)
	if err != nil {
		return deploy.Bundle{}, s.internalError("校验证书分配失败", err)
	}
	for _, target := range targets {
		if target.Certificate != nil && target.Certificate.ID == certificateID {
			allowed = true
			break
		}
	}
	if !allowed {
		return deploy.Bundle{}, Forbidden("当前证书未分配给此 Agent")
	}
	_, bundle, err := s.CertificateBundle(ctx, certificateID)
	return bundle, err
}

// RecordAgentDeployment 校验目标授权和期望证书后登记 Agent 部署结果。
func (s *Services) RecordAgentDeployment(ctx context.Context, agent store.Agent, input DeploymentResultInput) error {
	if input.Status != "SUCCESS" && input.Status != "FAILED" && input.Status != "RUNNING" {
		return BadRequest("部署状态无效")
	}
	if input.TargetID <= 0 || input.Revision <= 0 || input.CertificateID <= 0 || strings.TrimSpace(input.Fingerprint) == "" {
		return BadRequest("部署结果缺少目标、配置修订、证书或指纹，请确认 Agent 已升级")
	}
	messageRunes := []rune(input.Message)
	if len(messageRunes) > 500 {
		input.Message = string(messageRunes[:500])
	}
	var notification *Alert
	if s.Config.AlertWebhookURL != "" {
		notification = &Alert{Event: "certificate.deployment.failed", Severity: "ERROR", ResourceType: "DEPLOYMENT_TARGET", ResourceID: formatID(input.TargetID), Message: "Agent " + agent.Name + "：" + input.Message}
	}
	err := s.Store.RecordDeployment(ctx, agent.ID, input.TargetID, input.Revision, input.CertificateID, input.Fingerprint, input.Status, input.Message, notification)
	if errors.Is(err, store.ErrDeploymentChanged) {
		return Conflict(err.Error())
	}
	if err != nil {
		return normalizeStoreError(err, "部署目标不存在")
	}
	return nil
}

// HeartbeatAgent 更新节点最近在线时间和运行版本信息。
func (s *Services) HeartbeatAgent(ctx context.Context, agent store.Agent, version, platform string) error {
	if len(version) > 100 || len(platform) > 100 {
		return BadRequest("Agent 版本或平台信息过长")
	}
	return s.Store.HeartbeatAgent(ctx, agent.ID, version, platform)
}

// RevokeAgent 撤销指定 Agent 的访问权并写入审计日志。
func (s *Services) RevokeAgent(ctx context.Context, userID int64, id string) error {
	if err := s.Store.RevokeAgent(ctx, id); err != nil {
		return normalizeStoreError(err, "Agent 不存在")
	}
	s.RecordAudit(ctx, &userID, "REVOKE", "AGENT", id, "撤销 Agent 身份和访问权")
	return nil
}

// randomUUID 使用密码学安全随机源生成 RFC 4122 第四版 UUID。
func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	text := hex.EncodeToString(value)
	return fmt.Sprintf("%s-%s-%s-%s-%s", text[:8], text[8:12], text[12:16], text[16:20], text[20:]), nil
}
