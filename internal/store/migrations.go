// 本文件实现带持久版本记录的 Go 数据库迁移执行器。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	// certificateSeriesMigrationVersion 标识证书系列模型与历史数据归并迁移。
	certificateSeriesMigrationVersion = 1
	// certificateVersionConstraintMigrationVersion 标识系列内证书版本唯一约束迁移。
	certificateVersionConstraintMigrationVersion = 2
	// operationalIndexesMigrationVersion 标识审计查询等运行索引迁移。
	operationalIndexesMigrationVersion = 3
	// reliabilityMigrationVersion 标识订单互斥、执行时间线和持久告警迁移。
	reliabilityMigrationVersion = 4
	// dnsProvidersMigrationVersion 标识旧凭据表服务商约束的兼容升级。
	dnsProvidersMigrationVersion = 5
	// portableMaterialPathsMigrationVersion 标记相对材料路径契约，阻止旧程序误读新写入的路径。
	portableMaterialPathsMigrationVersion = 6
	// latestSchemaMigrationVersion 标识当前二进制能够安全读取的最高数据库迁移版本。
	latestSchemaMigrationVersion = 11
)

// schemaMigration 描述一个按版本顺序执行且必须保持幂等的 Go 数据库迁移。
type schemaMigration struct {
	Version int                         // Version 是不可复用且单调递增的迁移版本号。
	Name    string                      // Name 是用于审计和错误定位的稳定迁移名称。
	Run     func(context.Context) error // Run 执行迁移；nil 表示只登记数据契约版本，不改写既有记录。
}

// runMigrations 按声明顺序执行尚未成功记录的全部 Go 数据库迁移。
func (s *Store) runMigrations(ctx context.Context) error {
	if err := s.ensureSupportedSchemaVersion(ctx); err != nil {
		return err
	}
	migrations := []schemaMigration{
		{Version: certificateSeriesMigrationVersion, Name: "certificate_series", Run: s.migrateCertificateSeries},
		{Version: certificateVersionConstraintMigrationVersion, Name: "certificate_series_version_constraint", Run: s.ensureCertificateVersionConstraint},
		{Version: operationalIndexesMigrationVersion, Name: "operational_indexes", Run: s.ensureOperationalIndexes},
		{Version: reliabilityMigrationVersion, Name: "order_and_alert_reliability", Run: s.migrateReliability},
		{Version: dnsProvidersMigrationVersion, Name: "dns_providers", Run: s.migrateDNSProviders},
		{Version: portableMaterialPathsMigrationVersion, Name: "portable_material_paths"},
		{Version: 7, Name: "cloud_certificate_deployment", Run: s.migrateCloud},
		{Version: 8, Name: "agent_manual_sync", Run: s.migrateManualSync},
		{Version: 9, Name: "unified_credentials", Run: s.migrateUnifiedCredentials},
		{Version: 10, Name: "cloud_credential_schema", Run: s.migrateCloudCredentialSchema},
		{Version: 11, Name: "credential_multiple_zones", Run: s.migrateCredentialZones},
	}
	for _, migration := range migrations {
		if err := s.runSchemaMigration(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

// migrateReliability 保留旧订单记录并终结重复活动任务，再建立系列互斥约束。
func (s *Store) migrateReliability(ctx context.Context) error {
	// 同一事务完成旧数据修复、约束安装和现有订单的时间线快照。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE certificate_orders SET status='FAILED',stage='FAILED',error_message='重复活动订单已合并，请查看同系列较早的订单',completed_at=?,updated_at=? WHERE series_id IS NOT NULL AND status IN ('QUEUED','RUNNING') AND id NOT IN (SELECT MIN(id) FROM certificate_orders WHERE series_id IS NOT NULL AND status IN ('QUEUED','RUNNING') GROUP BY series_id)`, nowText(), nowText()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_active_series ON certificate_orders(series_id) WHERE series_id IS NOT NULL AND status IN ('QUEUED','RUNNING')`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,stage,message,created_at) SELECT id,stage,'升级时记录的当前阶段，升级前的阶段历史不可用',updated_at FROM certificate_orders WHERE NOT EXISTS(SELECT 1 FROM order_events WHERE order_id=certificate_orders.id)`)
		return err
	})
}

// ensureOperationalIndexes 幂等创建新增管理查询所需索引。
func (s *Store) ensureOperationalIndexes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_audit_logs_filters ON audit_logs(action,resource_type,id DESC)`)
	return err
}

// ensureSupportedSchemaVersion 阻止旧二进制打开已经由更高版本升级的数据库。
func (s *Store) ensureSupportedSchemaVersion(ctx context.Context) error {
	var databaseVersion int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&databaseVersion); err != nil {
		return fmt.Errorf("读取数据库迁移版本: %w", err)
	}
	if databaseVersion > latestSchemaMigrationVersion {
		return fmt.Errorf("数据库迁移版本 %d 高于当前程序支持的最高版本 %d，禁止使用旧版本程序启动", databaseVersion, latestSchemaMigrationVersion)
	}
	return nil
}

// runSchemaMigration 跳过已记录版本，并在迁移完整成功后持久化完成记录。
func (s *Store) runSchemaMigration(ctx context.Context, migration schemaMigration) error {
	var appliedName string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM schema_migrations WHERE version=?`, migration.Version).Scan(&appliedName)
	if err == nil {
		if appliedName != migration.Name {
			return fmt.Errorf("数据库迁移版本 %d 已记录为 %s，不能复用为 %s", migration.Version, appliedName, migration.Name)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("检查数据库迁移 %d(%s): %w", migration.Version, migration.Name, err)
	}
	if migration.Run != nil {
		if err := migration.Run(ctx); err != nil {
			return fmt.Errorf("执行数据库迁移 %d(%s): %w", migration.Version, migration.Name, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,applied_at) VALUES(?,?,?)`, migration.Version, migration.Name, nowText()); err != nil {
		return fmt.Errorf("记录数据库迁移 %d(%s): %w", migration.Version, migration.Name, err)
	}
	return nil
}
