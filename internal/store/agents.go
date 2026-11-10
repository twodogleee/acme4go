// 本文件持久化 Agent 身份、部署目标和部署结果。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
)

// ErrDeploymentChanged 表示回执对应的配置或授权已失效，不允许覆盖当前部署状态。
var ErrDeploymentChanged = errors.New("部署目标配置或授权已变化，请重新拉取期望状态")

// CreateEnrollmentToken 保存一次性 Agent 注册令牌哈希。
func (s *Store) CreateEnrollmentToken(ctx context.Context, tokenHash, name string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO enrollment_tokens(token_hash,name,expires_at,created_at) VALUES(?,?,?,?)`, tokenHash, name, timeText(expiresAt), nowText())
	return err
}

// DeleteExpiredEnrollmentTokens 清理已使用或过期的注册令牌。
func (s *Store) DeleteExpiredEnrollmentTokens(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM enrollment_tokens WHERE expires_at<=? OR used_at IS NOT NULL`, timeText(before))
	return err
}

// ConsumeEnrollmentToken 在同一事务中校验令牌、创建 Agent 并标记令牌已使用。
func (s *Store) ConsumeEnrollmentToken(ctx context.Context, tokenHash string, now time.Time, create func(name string) (Agent, error)) (Agent, error) {
	var result Agent
	// 注册事务回调锁定一次性令牌并原子创建 Agent 记录。
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var name string
		err := tx.QueryRowContext(ctx, `SELECT name FROM enrollment_tokens WHERE token_hash=? AND used_at IS NULL AND expires_at>?`, tokenHash, timeText(now)).Scan(&name)
		if err != nil {
			return normalizeNotFound(err)
		}
		result, err = create(name)
		if err != nil {
			return err
		}
		created := nowText()
		if _, err = tx.ExecContext(ctx, `INSERT INTO agents(id,name,status,enabled,certificate_serial,certificate_expires_at,created_at,updated_at) VALUES(?,?,'OFFLINE',1,?,?,?,?)`, result.ID, result.Name, result.CertificateSerial, timeText(result.CertificateExpiresAt), created, created); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE enrollment_tokens SET used_at=? WHERE token_hash=?`, created, tokenHash)
		return err
	})
	return result, err
}

// ListAgents 返回全部节点及按心跳计算的在线状态。
func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id,a.name,a.status,a.enabled,a.certificate_serial,a.certificate_expires_at,a.last_seen_at,COALESCE(a.version,''),COALESCE(a.platform,''),a.created_at,a.updated_at,COUNT(t.id)
		FROM agents a LEFT JOIN deployment_targets t ON t.agent_id=a.id
		GROUP BY a.id ORDER BY a.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Agent, 0)
	for rows.Next() {
		value, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// FindAgentByID 按稳定身份标识读取 Agent。
func (s *Store) FindAgentByID(ctx context.Context, id string) (Agent, error) {
	value, err := scanAgent(s.db.QueryRowContext(ctx, `
		SELECT a.id,a.name,a.status,a.enabled,a.certificate_serial,a.certificate_expires_at,a.last_seen_at,COALESCE(a.version,''),COALESCE(a.platform,''),a.created_at,a.updated_at,COUNT(t.id)
		FROM agents a LEFT JOIN deployment_targets t ON t.agent_id=a.id WHERE a.id=? GROUP BY a.id`, id))
	if err != nil {
		return Agent{}, normalizeNotFound(err)
	}
	return value, nil
}

// FindAgentBySerial 按当前或恢复窗口内的旧证书序列号查找 Agent。
func (s *Store) FindAgentBySerial(ctx context.Context, serial string) (Agent, error) {
	value, err := scanAgent(s.db.QueryRowContext(ctx, `
		SELECT a.id,a.name,a.status,a.enabled,a.certificate_serial,a.certificate_expires_at,a.last_seen_at,COALESCE(a.version,''),COALESCE(a.platform,''),a.created_at,a.updated_at,COUNT(t.id)
		FROM agents a LEFT JOIN deployment_targets t ON t.agent_id=a.id WHERE a.certificate_serial=? OR a.previous_certificate_serial=? GROUP BY a.id`, serial, serial))
	if err != nil {
		return Agent{}, normalizeNotFound(err)
	}
	return value, nil
}

// scanAgent 将当前查询行转换为 Agent 模型。
func scanAgent(row rowScanner) (Agent, error) {
	var value Agent
	var enabled int
	var expires, created, updated string
	var lastSeen sql.NullString
	err := row.Scan(&value.ID, &value.Name, &value.Status, &enabled, &value.CertificateSerial, &expires, &lastSeen, &value.Version, &value.Platform, &created, &updated, &value.TargetCount)
	if err != nil {
		return Agent{}, err
	}
	value.Enabled = enabled == 1
	value.CertificateExpiresAt = parseTime(expires)
	value.LastSeenAt = nullableTime(lastSeen)
	value.CreatedAt, value.UpdatedAt = parseTime(created), parseTime(updated)
	if !value.Enabled {
		value.Status = "REVOKED"
	} else if value.LastSeenAt == nil || value.LastSeenAt.Before(time.Now().Add(-config.AgentOfflineAfter)) {
		value.Status = "OFFLINE"
	} else {
		value.Status = "ONLINE"
	}
	return value, nil
}

// HeartbeatAgent 更新节点最后在线时间与版本平台信息。
func (s *Store) HeartbeatAgent(ctx context.Context, id, version, platform string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET status='ONLINE',last_seen_at=?,version=?,platform=?,updated_at=? WHERE id=? AND enabled=1`, nowText(), version, platform, nowText(), id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateAgentCertificate 切换 Agent 当前证书；使用恢复证书续签时继续保留该证书，避免响应丢失导致节点失联。
func (s *Store) UpdateAgentCertificate(ctx context.Context, id, presentedSerial, newSerial string, expiresAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agents
		SET previous_certificate_serial=CASE WHEN certificate_serial=? THEN certificate_serial ELSE previous_certificate_serial END,
			certificate_serial=?,certificate_expires_at=?,updated_at=?
		WHERE id=? AND enabled=1 AND (certificate_serial=? OR previous_certificate_serial=?)`,
		presentedSerial, newSerial, timeText(expiresAt), nowText(), id, presentedSerial, presentedSerial)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAgent 停用节点并把其部署目标标记为不可同步。
func (s *Store) RevokeAgent(ctx context.Context, id string) error {
	now := nowText()
	// 撤销事务回调同时更新 Agent 身份和全部关联目标状态。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE agents SET enabled=0,status='REVOKED',updated_at=? WHERE id=?`, now, id)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `UPDATE deployment_targets SET manual_sync=0,status='FAILED',last_error='Agent 已撤销，目标停止同步',revision=revision+1,updated_at=? WHERE executor_type='AGENT' AND agent_id=?`, now, id)
		return err
	})
}

// CreateTarget 保存新的部署目标及初始修订号。
func (s *Store) CreateTarget(ctx context.Context, value DeploymentTarget) (DeploymentTarget, error) {
	now := nowText()
	result, err := s.db.ExecContext(ctx, `INSERT INTO deployment_targets(executor_type,agent_id,name,certificate_series_id,common_name,target_alias,enabled,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'PENDING',?,?)`, value.ExecutorType, nullable(value.AgentID), value.Name, nullable(value.CertificateSeriesID), value.CommonName, value.TargetAlias, value.Enabled, now, now)
	if err != nil {
		return DeploymentTarget{}, err
	}
	value.ID, err = result.LastInsertId()
	value.Revision, value.Status = 1, "PENDING"
	value.CreatedAt, value.UpdatedAt = parseTime(now), parseTime(now)
	return value, err
}

// TargetAliasExists 判断同一执行节点是否已有相同目录别名。
func (s *Store) TargetAliasExists(ctx context.Context, alias, executorType string, agentID *string, excludeID int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_targets WHERE target_alias=? AND executor_type=? AND id<>? AND (executor_type='LOCAL' OR agent_id=?)`, alias, executorType, excludeID, nullable(agentID)).Scan(&count)
	return count > 0, err
}

// UpdateTarget 保存部署目标并递增期望修订号。
func (s *Store) UpdateTarget(ctx context.Context, value DeploymentTarget) error {
	if err := s.lockDeployment(ctx); err != nil {
		return err
	}
	defer s.unlockDeployment()
	result, err := s.db.ExecContext(ctx, `UPDATE deployment_targets SET executor_type=?,agent_id=?,name=?,certificate_series_id=?,common_name=?,target_alias=?,enabled=?,manual_sync=0,revision=revision+1,status='PENDING',last_error=NULL,updated_at=? WHERE id=?`, value.ExecutorType, nullable(value.AgentID), value.Name, nullable(value.CertificateSeriesID), value.CommonName, value.TargetAlias, value.Enabled, nowText(), value.ID)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTarget 删除部署目标配置。
func (s *Store) DeleteTarget(ctx context.Context, id int64) error {
	if err := s.lockDeployment(ctx); err != nil {
		return err
	}
	defer s.unlockDeployment()
	result, err := s.db.ExecContext(ctx, `DELETE FROM deployment_targets WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// FindTarget 按主键读取部署目标。
func (s *Store) FindTarget(ctx context.Context, id int64) (DeploymentTarget, error) {
	value, err := scanTarget(s.db.QueryRowContext(ctx, targetSelect+` WHERE t.id=?`, id))
	if err != nil {
		return DeploymentTarget{}, normalizeNotFound(err)
	}
	return value, nil
}

// ListTargets 返回全部本机和远程部署目标。
func (s *Store) ListTargets(ctx context.Context) ([]DeploymentTarget, error) {
	rows, err := s.db.QueryContext(ctx, targetSelect+` ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]DeploymentTarget, 0)
	for rows.Next() {
		value, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// targetSelect 是部署目标各查询共用的稳定列和执行器名称关联。
const targetSelect = `SELECT t.id,t.executor_type,t.agent_id,CASE WHEN t.executor_type='LOCAL' THEN 'Server 本机执行器' ELSE COALESCE(a.name,'未知 Agent') END,t.name,t.certificate_series_id,COALESCE(s.code,''),COALESCE(s.name,''),COALESCE(s.domains_json,'[]'),t.common_name,t.target_alias,t.enabled,t.revision,t.status,t.last_certificate_id,t.last_fingerprint,t.last_deployed_at,t.last_error,t.created_at,t.updated_at,t.manual_sync FROM deployment_targets t LEFT JOIN agents a ON a.id=t.agent_id LEFT JOIN certificate_series s ON s.id=t.certificate_series_id`

// scanTarget 将当前查询行转换为部署目标模型。
func scanTarget(row rowScanner) (DeploymentTarget, error) {
	var value DeploymentTarget
	var agentID, certificateSeriesID, lastFingerprint, lastDeployed, lastError sql.NullString
	var seriesDomainsJSON string
	var lastCertificate sql.NullInt64
	var enabled int
	var created, updated string
	err := row.Scan(&value.ID, &value.ExecutorType, &agentID, &value.ExecutorName, &value.Name, &certificateSeriesID, &value.SeriesCode, &value.SeriesName, &seriesDomainsJSON, &value.CommonName, &value.TargetAlias, &enabled, &value.Revision, &value.Status, &lastCertificate, &lastFingerprint, &lastDeployed, &lastError, &created, &updated, &value.ManualSync)
	if err != nil {
		return DeploymentTarget{}, err
	}
	value.AgentID = nullableString(agentID)
	value.CertificateSeriesID = nullableString(certificateSeriesID)
	if err := json.Unmarshal([]byte(seriesDomainsJSON), &value.SeriesDomains); err != nil {
		return DeploymentTarget{}, err
	}
	value.Enabled = enabled == 1
	value.LastCertificateID = nullableInt64(lastCertificate)
	value.LastFingerprint = nullableString(lastFingerprint)
	value.LastDeployedAt = nullableTime(lastDeployed)
	value.LastError = nullableString(lastError)
	value.CreatedAt, value.UpdatedAt = parseTime(created), parseTime(updated)
	return value, nil
}

// DesiredTargets 使用一次关联查询返回 Agent 当前应物化的部署目标。
func (s *Store) DesiredTargets(ctx context.Context, agentID, baseURL string, renewBefore time.Duration) ([]DesiredTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.certificate_series_id,t.name,t.target_alias,t.common_name,s.domains_json,t.revision,c.id,c.version,c.fingerprint,c.not_after,t.manual_sync
		FROM deployment_targets t
		JOIN certificate_series s ON s.id=t.certificate_series_id
		LEFT JOIN certificates c ON c.id=s.current_certificate_id AND c.not_after>?
		WHERE t.executor_type='AGENT' AND t.agent_id=? AND (t.enabled=1 OR t.manual_sync=1) AND s.enabled=1 ORDER BY t.id`, timeText(time.Now()), agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]DesiredTarget, 0)
	for rows.Next() {
		var value DesiredTarget
		var domainsJSON string
		var certificateID, version sql.NullInt64
		var fingerprint, notAfter sql.NullString
		if err := rows.Scan(&value.ID, &value.SeriesID, &value.Name, &value.TargetAlias, &value.CommonName, &domainsJSON, &value.Revision, &certificateID, &version, &fingerprint, &notAfter, &value.ManualSync); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(domainsJSON), &value.Domains); err != nil {
			return nil, err
		}
		if certificateID.Valid && version.Valid && fingerprint.Valid && notAfter.Valid {
			value.Certificate = &CertificateDescriptor{ID: certificateID.Int64, SeriesID: value.SeriesID, Version: int(version.Int64), Fingerprint: fingerprint.String, NotAfter: parseTime(notAfter.String), DownloadURL: fmt.Sprintf("%s/agent/v1/certificates/%d", baseURL, certificateID.Int64)}
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = renewBefore // 生命周期状态不参与期望证书描述，保留参数以维持仓库接口兼容。
	return values, nil
}

// RecordDeployment 在同一事务内核对当前授权与修订、登记回执并保存可选失败通知。
func (s *Store) RecordDeployment(ctx context.Context, agentID string, targetID, revision, certificateID int64, fingerprint, status, message string, notification *AlertEvent) error {
	// 远程部署事务回调校验授权、去重回执并更新目标终态。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		now := nowText()
		var previousStatus string
		var enabled, manual bool
		var previousError sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT t.status,t.last_error,t.enabled,t.manual_sync FROM deployment_targets t
			JOIN agents a ON a.id=t.agent_id AND a.enabled=1
			JOIN certificate_series s ON s.id=t.certificate_series_id AND s.enabled=1
			JOIN certificates c ON c.id=s.current_certificate_id AND c.series_id=s.id
			WHERE t.id=? AND t.executor_type='AGENT' AND t.agent_id=? AND (t.enabled=1 OR t.manual_sync=1 OR (t.status=? AND t.last_certificate_id=? AND t.last_fingerprint=?))
			AND t.revision=? AND c.id=? AND c.fingerprint=? AND c.not_after>?`, targetID, agentID, status, certificateID, fingerprint, revision, certificateID, fingerprint, now).Scan(&previousStatus, &previousError, &enabled, &manual)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDeploymentChanged
		}
		if err != nil {
			return err
		}
		// 暂停目标的终态重传仅确认收妥，不能重新授权部署或改写时间。
		if !enabled && !manual {
			return nil
		}
		if status == "RUNNING" {
			if !manual {
				return ErrDeploymentChanged
			}
			_, err := tx.ExecContext(ctx, `UPDATE deployment_targets SET status='RUNNING',updated_at=? WHERE id=?`, now, targetID)
			return err
		}
		var lastDeployed any
		var lastError any = message
		if status == "SUCCESS" {
			lastDeployed, lastError = now, nil
		}
		result, err := tx.ExecContext(ctx, `UPDATE deployment_targets SET manual_sync=0,status=?,last_certificate_id=?,last_fingerprint=?,last_deployed_at=COALESCE(?,last_deployed_at),last_error=?,updated_at=? WHERE id=? AND executor_type='AGENT' AND agent_id=?`, status, certificateID, fingerprint, lastDeployed, lastError, now, targetID, agentID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrNotFound
		}
		if status == "FAILED" && notification != nil && (previousStatus != "FAILED" || !previousError.Valid || previousError.String != message) {
			if err := enqueueAlert(ctx, tx, *notification, ""); err != nil {
				return err
			}
		}
		duplicate, err := duplicateDeploymentRecord(ctx, tx, targetID, agentID, certificateID, fingerprint, status, message, time.Now().Add(-10*time.Minute))
		if err != nil || (duplicate && !manual) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO deployment_records(target_id,executor_type,agent_id,certificate_id,fingerprint,status,message,created_at) VALUES(?,'AGENT',?,?,?,?,?,?)`, targetID, agentID, certificateID, fingerprint, status, message, now)
		return err
	})
}

// RecordLocalDeployment 核对本机执行修订、启用系列及当前证书后原子登记结果、历史和首个可选通知。
// 暂停自动部署的目标仍允许手动同步；连续相同失败不会重复入队。
func (s *Store) RecordLocalDeployment(ctx context.Context, targetID, revision, certificateID int64, fingerprint, status, message string, notifications ...AlertEvent) error {
	// 本机部署事务回调去重回执并更新目标终态。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		now := nowText()
		var previousStatus string
		var previousError sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT t.status,t.last_error FROM deployment_targets t
			JOIN certificate_series s ON s.id=t.certificate_series_id AND s.enabled=1
			JOIN certificates c ON c.id=s.current_certificate_id AND c.series_id=s.id
			WHERE t.id=? AND t.executor_type='LOCAL' AND t.revision=?
			AND c.id=? AND c.fingerprint=? AND c.not_after>?`, targetID, revision, certificateID, fingerprint, now).Scan(&previousStatus, &previousError)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDeploymentChanged
		}
		if err != nil {
			return err
		}
		var deployed any
		var lastError any = message
		if status == "SUCCESS" {
			deployed, lastError = now, nil
		}
		result, err := tx.ExecContext(ctx, `UPDATE deployment_targets SET status=?,last_certificate_id=?,last_fingerprint=?,last_deployed_at=COALESCE(?,last_deployed_at),last_error=?,updated_at=? WHERE id=? AND executor_type='LOCAL'`, status, certificateID, fingerprint, deployed, lastError, now, targetID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrNotFound
		}
		if status == "FAILED" && len(notifications) > 0 && (previousStatus != "FAILED" || !previousError.Valid || previousError.String != message) {
			if err := enqueueAlert(ctx, tx, notifications[0], ""); err != nil {
				return err
			}
		}
		duplicate, err := duplicateDeploymentRecord(ctx, tx, targetID, "", certificateID, fingerprint, status, message, time.Now().Add(-10*time.Minute))
		if err != nil || (duplicate && status != "SUCCESS") {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO deployment_records(target_id,executor_type,certificate_id,fingerprint,status,message,created_at) VALUES(?,'LOCAL',?,?,?,?,?)`, targetID, certificateID, fingerprint, status, message, now)
		return err
	})
}

// duplicateDeploymentRecord 判断短时间内是否已有完全相同的部署回执。
func duplicateDeploymentRecord(ctx context.Context, tx *sql.Tx, targetID int64, agentID string, certificateID int64, fingerprint, status, message string, since time.Time) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_records
		WHERE target_id=? AND COALESCE(agent_id,'')=? AND certificate_id=? AND fingerprint=? AND status=? AND message=? AND created_at>=?`,
		targetID, agentID, certificateID, fingerprint, status, message, timeText(since)).Scan(&count)
	return count > 0, err
}

// PruneDeploymentRecords 清理历史，但保留启用目标最近一次成功回执作为实际使用版本依据。
func (s *Store) PruneDeploymentRecords(ctx context.Context, before time.Time, maxRecords int) error {
	if maxRecords <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_records
		WHERE (created_at<? OR id NOT IN (SELECT id FROM deployment_records ORDER BY id DESC LIMIT ?))
		AND id NOT IN (SELECT MAX(r.id) FROM deployment_records r JOIN deployment_targets t ON t.id=r.target_id WHERE t.enabled=1 AND r.status='SUCCESS' GROUP BY r.target_id)`, timeText(before), maxRecords)
	return err
}

// ListDeploymentRecords 返回最近的部署审计记录。
func (s *Store) ListDeploymentRecords(ctx context.Context) ([]DeploymentRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,target_id,executor_type,agent_id,certificate_id,fingerprint,status,message,created_at FROM deployment_records ORDER BY id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]DeploymentRecord, 0)
	for rows.Next() {
		var value DeploymentRecord
		var targetID, certificateID sql.NullInt64
		var agentID, fingerprint sql.NullString
		var created string
		if err := rows.Scan(&value.ID, &targetID, &value.ExecutorType, &agentID, &certificateID, &fingerprint, &value.Status, &value.Message, &created); err != nil {
			return nil, err
		}
		value.TargetID, value.CertificateID = nullableInt64(targetID), nullableInt64(certificateID)
		value.AgentID, value.Fingerprint = nullableString(agentID), nullableString(fingerprint)
		value.CreatedAt = parseTime(created)
		values = append(values, value)
	}
	return values, rows.Err()
}
