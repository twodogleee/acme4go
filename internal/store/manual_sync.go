// 本文件持久化远程单次同步授权；自动部署开关保持不变，修订号隔离旧回执。
//
// @author Andersen
package store

import "context"

// migrateManualSync 为已有数据库幂等增加单次同步状态。
func (s *Store) migrateManualSync(ctx context.Context) error {
	return s.ensureColumn(ctx, "deployment_targets", "manual_sync", "INTEGER NOT NULL DEFAULT 0")
}

// RequestAgentSync 原子校验节点、系列和有效证书后入队；重复点击复用未完成请求。
func (s *Store) RequestAgentSync(ctx context.Context, id int64) error {
	if err := s.lockDeployment(ctx); err != nil {
		return err
	}
	defer s.unlockDeployment()
	result, err := s.db.ExecContext(ctx, `UPDATE deployment_targets SET
		revision=revision+CASE WHEN manual_sync=0 THEN 1 ELSE 0 END,
		status=CASE WHEN manual_sync=0 THEN 'PENDING' ELSE status END,
		last_error=CASE WHEN manual_sync=0 THEN NULL ELSE last_error END,
		manual_sync=1,updated_at=?
		WHERE id=? AND executor_type='AGENT'
		AND EXISTS(SELECT 1 FROM agents a WHERE a.id=agent_id AND a.enabled=1)
		AND EXISTS(SELECT 1 FROM certificate_series s JOIN certificates c ON c.id=s.current_certificate_id
		 WHERE s.id=certificate_series_id AND s.enabled=1 AND c.not_after>?)`, nowText(), id, nowText())
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrDeploymentChanged
	}
	return nil
}
