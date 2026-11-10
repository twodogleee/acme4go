// 本文件提供运维命令使用的 SQLite 一致性检查与在线快照能力。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// OpenReadOnly 打开已经存在的数据库供诊断使用，不创建数据库、应用 Schema 或执行迁移。
func OpenReadOnly(ctx context.Context, dataDir string) (*Store, error) {
	path, err := filepath.Abs(filepath.Join(dataDir, "acme4go.db"))
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("诊断数据库不是普通文件")
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?mode=ro&_pragma=query_only(1)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	database := &Store{db: db, deploymentGate: make(chan struct{}, 1)}
	if err := database.ensureSupportedSchemaVersion(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return database, nil
}

// ListACMEAccounts 返回诊断需要检查的账户密文；这些字段不得写入日志或管理 API 响应。
func (s *Store) ListACMEAccounts(ctx context.Context) ([]ACMEAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_key,email,ca_directory,private_key_enc FROM acme_accounts ORDER BY account_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []ACMEAccount
	for rows.Next() {
		var value ACMEAccount
		if err := rows.Scan(&value.Key, &value.Email, &value.CADirectory, &value.PrivateKey); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// IntegrityCheck 执行 SQLite 完整性检查并要求返回 ok。
func (s *Store) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite 完整性检查失败: %s", result)
	}
	return nil
}

// Backup 使用 SQLite VACUUM INTO 创建包含已提交事务的一致数据库快照。
func (s *Store) Backup(ctx context.Context, destination string) error {
	escaped := strings.ReplaceAll(destination, "'", "''")
	_, err := s.db.ExecContext(ctx, `VACUUM INTO '`+escaped+`'`)
	return err
}
