// 本文件修复已建表但未投入使用的旧云部署结构；只重建空云表，保留 DNS 业务数据。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"fmt"
)

// migrateCloudCredentialSchema 用新迁移版本修复旧 account_id 结构，避免已登记的迁移被跳过。
func (s *Store) migrateCloudCredentialSchema(ctx context.Context) error {
	// 空表检查、结构替换和凭据字段补齐共同提交，失败不会留下半修复结构。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		needsRebuild := false
		for _, table := range []string{"cloud_plans", "cloud_uploads"} {
			var found int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name='credential_id'`, table).Scan(&found); err != nil {
				return err
			}
			needsRebuild = needsRebuild || found == 0
		}
		if needsRebuild {
			// 用户未启用旧云账号功能；若实际存在数据则拒绝清空，也不能将账号 ID 当成凭据 ID。
			for _, table := range []string{"cloud_jobs", "cloud_uploads", "cloud_plans", "cloud_accounts"} {
				var exists int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
					return err
				}
				if exists == 0 {
					continue
				}
				var count int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					return fmt.Errorf("检测到旧版云部署数据（%s 有 %d 条记录），未执行自动清空；请先备份并处理旧云配置，再重新启动", table, count)
				}
			}
			// 从子表到父表删除已确认为空的旧结构，不关闭外键检查。
			if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS cloud_jobs; DROP TABLE IF EXISTS cloud_uploads; DROP TABLE IF EXISTS cloud_plans; DROP TABLE IF EXISTS cloud_accounts;`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, cloudSchema); err != nil {
			return err
		}
		// 初版版本 9 已登记但缺少 cloud_identity_hash，也必须在本次新版本中补齐。
		return ensureCredentialScopes(ctx, tx)
	})
}
