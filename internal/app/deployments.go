// 本文件实现本机与远程 Agent 部署目标的业务规则。
//
// @author Andersen
package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/store"
)

// DeploymentTargetInput 使用显式执行器类型描述本机或远程部署目标。
type DeploymentTargetInput struct {
	ExecutorType        string  `json:"executorType"`        // ExecutorType 是 LOCAL 或 AGENT。
	AgentID             *string `json:"agentId"`             // AgentID 是远程执行时选择的节点标识。
	Name                string  `json:"name"`                // Name 是目标的可读名称。
	CertificateSeriesID string  `json:"certificateSeriesId"` // CertificateSeriesID 是人工选择的证书系列标识。
	TargetAlias         string  `json:"targetAlias"`         // TargetAlias 是部署根目录下的安全别名。
	Enabled             bool    `json:"enabled"`             // Enabled 表示是否自动同步新证书。
}

// CreateTarget 校验并创建部署目标，启用的本机目标会立即尝试对账。
func (s *Services) CreateTarget(ctx context.Context, userID int64, input DeploymentTargetInput) (store.DeploymentTarget, error) {
	value, err := s.prepareTarget(ctx, input, 0)
	if err != nil {
		return store.DeploymentTarget{}, err
	}
	value, err = s.Store.CreateTarget(ctx, value)
	if err != nil {
		return store.DeploymentTarget{}, s.internalError("创建设备部署目标失败", err)
	}
	value.RuntimePath = s.targetRuntimePath(value)
	s.RecordAudit(ctx, &userID, "CREATE", "DEPLOYMENT_TARGET", formatID(value.ID), value.Name)
	if value.ExecutorType == store.ExecutorLocal && value.Enabled {
		_ = s.runLocalDeployment(ctx, value.ID, true)
	}
	return value, nil
}

// UpdateTarget 更新部署目标并递增期望修订号。
func (s *Services) UpdateTarget(ctx context.Context, userID, id int64, input DeploymentTargetInput) (store.DeploymentTarget, error) {
	value, err := s.prepareTarget(ctx, input, id)
	if err != nil {
		return store.DeploymentTarget{}, err
	}
	value.ID = id
	if err := s.Store.UpdateTarget(ctx, value); err != nil {
		return store.DeploymentTarget{}, normalizeStoreError(err, "部署目标不存在")
	}
	s.RecordAudit(ctx, &userID, "UPDATE", "DEPLOYMENT_TARGET", formatID(id), value.Name)
	updated, err := s.Store.FindTarget(ctx, id)
	if err != nil {
		return store.DeploymentTarget{}, err
	}
	updated.RuntimePath = s.targetRuntimePath(updated)
	if updated.ExecutorType == store.ExecutorLocal && updated.Enabled {
		_ = s.runLocalDeployment(ctx, id, true)
	}
	return updated, nil
}

// DeleteTarget 删除部署配置但保留已经物化的证书文件。
func (s *Services) DeleteTarget(ctx context.Context, userID, id int64) error {
	if err := s.Store.DeleteTarget(ctx, id); err != nil {
		return normalizeStoreError(err, "部署目标不存在")
	}
	s.RecordAudit(ctx, &userID, "DELETE", "DEPLOYMENT_TARGET", formatID(id), "删除部署配置，保留已物化文件")
	return nil
}

// SyncTarget 本机执行同步，远程登记单次授权并等待 Agent 拉取；不会启用自动部署。
func (s *Services) SyncTarget(ctx context.Context, id int64) error {
	target, err := s.Store.FindTarget(ctx, id)
	if err != nil {
		return normalizeStoreError(err, "部署目标不存在")
	}
	if target.ExecutorType == store.ExecutorLocal {
		return s.DeployLocalTarget(ctx, id)
	}
	if err := s.Store.RequestAgentSync(ctx, id); err != nil {
		return Conflict("无法请求同步，请确认 Agent 和证书系列已启用，且系列存在有效证书")
	}
	return nil
}

// DeployLocalTarget 验证证书材料并原子切换指定本机目标的 current 目录。
func (s *Services) DeployLocalTarget(ctx context.Context, id int64) error {
	// 手动部署保留暂停自动部署目标的显式同步能力，但不能绕过停用系列。
	return s.runLocalDeployment(ctx, id, false)
}

// runLocalDeployment 将读取、物化和登记纳入协调区间，自动任务还需重新检查目标开关。
func (s *Services) runLocalDeployment(ctx context.Context, id int64, automatic bool) error {
	// 本机执行回调不修改配置，避免递归申请部署协调权。
	return s.Store.WithLocalDeployment(ctx, func() error {
		return s.deployLocalTarget(ctx, id, automatic)
	})
}

// deployLocalTarget 在持有部署协调权时使用最新目标配置执行文件切换。
func (s *Services) deployLocalTarget(ctx context.Context, id int64, automatic bool) error {
	if !s.Config.LocalDeploymentEnabled {
		return Conflict("Server 未启用本机执行器，不能写入本机部署目录")
	}
	target, err := s.Store.FindTarget(ctx, id)
	if err != nil {
		return normalizeStoreError(err, "部署目标不存在")
	}
	if target.ExecutorType != store.ExecutorLocal {
		return BadRequest("远程 Agent 目标由 Agent 主动拉取")
	}
	if automatic && !target.Enabled {
		return nil
	}
	if target.CertificateSeriesID == nil {
		return Conflict("部署目标尚未绑定证书系列，请先编辑目标")
	}
	series, err := s.Store.FindCertificateSeries(ctx, *target.CertificateSeriesID, s.Config.RenewBefore)
	if err != nil {
		return normalizeStoreError(err, "证书系列不存在")
	}
	if !series.Enabled {
		if automatic {
			return nil
		}
		return Conflict("证书系列已停用，不能部署")
	}
	certificate, err := s.Store.FindCurrentCertificate(ctx, *target.CertificateSeriesID, s.Config.RenewBefore)
	if err != nil {
		return normalizeStoreError(err, "所选证书系列尚无有效的当前证书")
	}
	_, bundle, err := s.CertificateBundle(ctx, certificate.ID)
	if err != nil {
		return err
	}
	if err := deploy.ValidateBundleDomains(bundle, target.SeriesDomains, certificate.Fingerprint, true); err != nil {
		s.recordLocalFailure(target.ID, target.Revision, certificate.ID, certificate.Fingerprint, "本机证书材料验证失败")
		return s.internalError("本机证书材料验证失败", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 自动检查仍校验磁盘材料；只有配置、版本和文件均正常时才跳过重写及回执。
	if automatic && target.Status == "SUCCESS" && target.LastCertificateID != nil && *target.LastCertificateID == certificate.ID && target.LastFingerprint != nil && *target.LastFingerprint == certificate.Fingerprint && deploy.ValidateMaterialized(s.Config.DeploymentDir, target.TargetAlias, target.SeriesDomains, certificate.Fingerprint) == nil {
		return nil
	}
	path, err := deploy.Materialize(s.Config.DeploymentDir, target.TargetAlias, certificate.ID, bundle)
	if err != nil {
		s.recordLocalFailure(target.ID, target.Revision, certificate.ID, certificate.Fingerprint, "本机证书物化失败")
		return s.internalError("本机证书物化失败", err)
	}
	// 文件已切换后使用短独立上下文保存结果，原请求取消不使实际部署失去登记。
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.RecordLocalDeployment(finishCtx, target.ID, target.Revision, certificate.ID, certificate.Fingerprint, "SUCCESS", "已部署到 "+path); err != nil {
		return s.internalError("记录本机部署结果失败", err)
	}
	return nil
}

// recordLocalFailure 在独立短上下文中将失败状态与通知一起保存，原请求取消不丢失故障。
func (s *Services) recordLocalFailure(targetID, revision, certificateID int64, fingerprint, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	notifications := s.configuredAlerts(Alert{Event: "certificate.deployment.failed", Severity: "ERROR", ResourceType: "DEPLOYMENT_TARGET", ResourceID: formatID(targetID), Message: message})
	if err := s.Store.RecordLocalDeployment(ctx, targetID, revision, certificateID, fingerprint, "FAILED", message, notifications...); err != nil {
		s.logger.Error("保存本机部署失败及通知失败", "target_id", targetID, "error", err)
	}
}

// ReconcileLocalTargets 逐一对账所有已启用的本机部署目标。
func (s *Services) ReconcileLocalTargets(ctx context.Context) error {
	targets, err := s.Store.ListTargets(ctx)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.ExecutorType == store.ExecutorLocal && target.Enabled {
			if err := s.runLocalDeployment(ctx, target.ID, true); err != nil {
				s.logger.Warn("本机部署目标对账失败", "target_id", target.ID, "error", err)
			}
		}
	}
	return nil
}

// prepareTarget 规范化输入并校验执行器、Agent 和目录别名唯一性。
func (s *Services) prepareTarget(ctx context.Context, input DeploymentTargetInput, excludeID int64) (store.DeploymentTarget, error) {
	name := strings.TrimSpace(input.Name)
	seriesID := strings.TrimSpace(input.CertificateSeriesID)
	alias := strings.TrimSpace(input.TargetAlias)
	if name == "" || seriesID == "" {
		return store.DeploymentTarget{}, BadRequest("名称和证书系列不能为空")
	}
	series, err := s.Store.FindCertificateSeries(ctx, seriesID, s.Config.RenewBefore)
	if err != nil || !series.Enabled {
		return store.DeploymentTarget{}, BadRequest("所选证书系列不存在或已停用")
	}
	if !validAlias(alias) {
		return store.DeploymentTarget{}, BadRequest("目录别名只能包含小写字母、数字、下划线和中划线")
	}
	executorType := strings.ToUpper(strings.TrimSpace(input.ExecutorType))
	switch executorType {
	case store.ExecutorLocal:
		if !s.Config.LocalDeploymentEnabled {
			return store.DeploymentTarget{}, BadRequest("Server 未启用本机执行器，请选择已注册 Agent")
		}
		input.AgentID = nil
	case store.ExecutorAgent:
		if input.AgentID == nil {
			return store.DeploymentTarget{}, BadRequest("Agent 执行器必须选择 Agent")
		}
		trimmed := strings.TrimSpace(*input.AgentID)
		if trimmed == "" {
			return store.DeploymentTarget{}, BadRequest("Agent 执行器必须选择 Agent")
		}
		agent, err := s.Store.FindAgentByID(ctx, trimmed)
		if err != nil || !agent.Enabled {
			return store.DeploymentTarget{}, BadRequest("所选 Agent 不存在或已停用")
		}
		input.AgentID = &trimmed
	default:
		return store.DeploymentTarget{}, BadRequest("执行器类型必须是 LOCAL 或 AGENT")
	}
	exists, err := s.Store.TargetAliasExists(ctx, alias, executorType, input.AgentID, excludeID)
	if err != nil {
		return store.DeploymentTarget{}, s.internalError("校验部署目录别名失败", err)
	}
	if exists {
		return store.DeploymentTarget{}, Conflict("所选节点已经使用此目录别名")
	}
	commonName := series.Domains[0]
	return store.DeploymentTarget{ExecutorType: executorType, AgentID: input.AgentID, Name: name, CertificateSeriesID: &series.ID, SeriesCode: series.Code, SeriesName: series.Name, SeriesDomains: series.Domains, CommonName: commonName, TargetAlias: alias, Enabled: input.Enabled, RuntimePath: fmt.Sprintf("%s/current", alias)}, nil
}

// targetRuntimePath 返回适合界面展示的目标稳定读取路径。
func (s *Services) targetRuntimePath(target store.DeploymentTarget) string {
	if target.ExecutorType == store.ExecutorAgent {
		return filepath.Join("<Agent 部署根目录>", target.TargetAlias, "current")
	}
	return filepath.Join(s.Config.DeploymentDir, target.TargetAlias, "current")
}
