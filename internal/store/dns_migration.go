// 本文件在保留凭据主键和所有引用的前提下扩展旧数据库的 DNS 服务商约束。
//
// @author Andersen
package store

import (
	"context"
	"fmt"
	"strings"
)

// migrateDNSProviders 在独占连接中重建旧凭据表，事务失败会完整回滚且恢复外键校验。
func (s *Store) migrateDNSProviders(ctx context.Context) (migrationErr error) {
	var definition string
	if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='dns_credentials'`).Scan(&definition); err != nil {
		return err
	}
	if strings.Contains(definition, "CLOUDFLARE") {
		return nil
	}
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	// 必须在事务之外恢复该连接的外键设置，包含所有失败返回路径。
	defer func() {
		_, err := connection.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		if migrationErr == nil {
			migrationErr = err
		}
	}()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	start := strings.Index(schemaSQL, "CREATE TABLE IF NOT EXISTS dns_credentials (")
	end := strings.Index(schemaSQL[start:], "\n);") + start + 3
	createSQL := strings.Replace(schemaSQL[start:end], "dns_credentials (", "dns_credentials_upgrade (", 1)
	if _, err := tx.ExecContext(ctx, createSQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO dns_credentials_upgrade(id,name,provider,zone,access_key_id_enc,access_key_secret_enc,enabled,last_test_status,last_test_message,last_test_at,created_at,updated_at) SELECT id,name,provider,zone,access_key_id_enc,access_key_secret_enc,enabled,last_test_status,last_test_message,last_test_at,created_at,updated_at FROM dns_credentials`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE dns_credentials`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE dns_credentials_upgrade RENAME TO dns_credentials`); err != nil {
		return err
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return err
	}
	if violations != 0 {
		return fmt.Errorf("DNS 凭据迁移发现 %d 项外键异常，已回滚", violations)
	}
	return tx.Commit()
}
