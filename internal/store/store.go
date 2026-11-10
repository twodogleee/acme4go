// 本文件初始化 SQLite、应用权威 Schema、执行带版本记录的 Go 迁移并提供事务入口。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store 封装控制平面唯一 SQLite 数据库及其事务入口。
type Store struct {
	db             *sql.DB       // db 是启用外键和 WAL 的 SQLite 连接池。
	deploymentGate chan struct{} // deploymentGate 串行协调本机物化与部署配置、系列及当前证书变更，不占用数据库事务。
}

// Open 创建数据库目录，为新库应用完整 Schema，并执行尚未记录的 Go 迁移。
func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录: %w", err)
	}
	dsn := "file:" + filepath.Join(dataDir, "acme4go.db") + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接 SQLite: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化数据库: %w", err)
	}
	database := &Store{db: db, deploymentGate: make(chan struct{}, 1)}
	if err := database.runMigrations(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("迁移数据库: %w", err)
	}
	if err := database.repairUnmigratedCertificateSeriesRows(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("修复未归属证书系列的数据: %w", err)
	}
	if err := database.ensureCertificateVersionConstraint(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("验证证书版本约束: %w", err)
	}
	return database, nil
}

// repairUnmigratedCertificateSeriesRows 仅在发现旧证书或订单时重新执行幂等归并。
func (s *Store) repairUnmigratedCertificateSeriesRows(ctx context.Context) error {
	var pending int
	if err := s.db.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM certificates WHERE series_id IS NULL LIMIT 1)
		OR EXISTS(SELECT 1 FROM certificate_orders WHERE series_id IS NULL LIMIT 1)`).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	return s.migrateCertificateSeries(ctx)
}

// ensureCertificateVersionConstraint 在唯一索引缺失时修复旧版本号并重建约束。
func (s *Store) ensureCertificateVersionConstraint(ctx context.Context) error {
	ready, err := s.hasCertificateVersionConstraint(ctx)
	if err != nil {
		return err
	}
	if ready {
		return nil
	}
	// 修复回调先重排旧重复版本，再建立系列唯一索引。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_certificates_common_version`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_certificates_series_version`); err != nil {
			return err
		}
		var duplicateGroups int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
			SELECT series_id,version FROM certificates WHERE series_id IS NOT NULL GROUP BY series_id,version HAVING COUNT(*)>1
		)`).Scan(&duplicateGroups); err != nil {
			return err
		}
		if duplicateGroups > 0 {
			if _, err := tx.ExecContext(ctx, `WITH ranked AS (
				SELECT id,ROW_NUMBER() OVER (PARTITION BY series_id ORDER BY version,id) AS new_version
				FROM certificates
			)
			UPDATE certificates SET version=(SELECT new_version FROM ranked WHERE ranked.id=certificates.id)`); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_certificates_series_version ON certificates(series_id,version) WHERE series_id IS NOT NULL`)
		return err
	})
}

// hasCertificateVersionConstraint 快速判断系列版本唯一索引是否已经存在。
func (s *Store) hasCertificateVersionConstraint(ctx context.Context) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA index_list(certificates)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			return false, err
		}
		if name == "idx_certificates_series_version" && unique != 0 {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Close 关闭底层 SQLite 连接池。
func (s *Store) Close() error { return s.db.Close() }

// Ready 验证调用方能在给定上下文期限内取得并使用 SQLite 连接。
func (s *Store) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }

// nowText 返回数据库统一使用的 UTC 纳秒时间文本。
func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// timeText 将时间转换为数据库统一使用的 UTC 纳秒格式。
func timeText(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

// nullableTime 将可空数据库时间转换为可选时间指针。
func nullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil
	}
	return &parsed
}

// parseTime 解析数据库时间文本，无效值返回零值时间。
func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

// nullableString 将数据库可空字符串转换为可选字符串指针。
func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

// nullableInt64 将数据库可空整数转换为可选整数指针。
func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

// nullable 将可选字符串转换为适合 SQL 参数绑定的值。
func nullable(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// nullableID 将可选主键转换为适合 SQL 参数绑定的值。
func nullableID(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// isNotFound 判断错误是否表示查询没有返回记录。
func isNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// withTx 在事务中执行回调，并统一提交或回滚。
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
