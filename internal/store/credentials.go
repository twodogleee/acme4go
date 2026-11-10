// 本文件管理共享凭据及用途；保留历史 DNS 主键，云部署直接引用凭据，密钥只存储一份。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// migrateUnifiedCredentials 为现有 DNS 凭据增加用途和云身份约束，不改写已有秘密和引用。
func (s *Store) migrateUnifiedCredentials(ctx context.Context) error {
	// 字段与索引共同提交，也供结构修复迁移补齐曾经登记过的旧版凭据结构。
	return s.withTx(ctx, func(tx *sql.Tx) error { return ensureCredentialScopes(ctx, tx) })
}

// ensureCredentialScopes 在调用方事务内补齐用途和身份约束，保留已有 DNS 密文及引用。
func ensureCredentialScopes(ctx context.Context, tx *sql.Tx) error {
	for _, field := range []struct {
		Name       string // Name 是新增列。
		Definition string // Definition 是兼容已有 DNS 记录的列定义。
	}{{"dns_enabled", "INTEGER NOT NULL DEFAULT 1"}, {"cloud_enabled", "INTEGER NOT NULL DEFAULT 0"}, {"cloud_identity_hash", "TEXT"}} {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('dns_credentials') WHERE name=?`, field.Name).Scan(&found); err != nil {
			return err
		}
		if found == 0 {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE dns_credentials ADD COLUMN %s %s", field.Name, field.Definition)); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_cloud_identity ON dns_credentials(cloud_identity_hash)`)
	return err
}

// credentialColumns 共享查询同时提供用途及云身份锁定状态，不返回身份摘要。
const credentialColumns = `SELECT id,name,provider,zone,access_key_id_enc,access_key_secret_enc,enabled,last_test_status,last_test_message,last_test_at,created_at,updated_at,dns_enabled,cloud_enabled,cloud_identity_hash IS NOT NULL,zones_json,last_test_results_json FROM dns_credentials`

// FindCredential 返回共享凭据的实际启用状态，不按使用场景过滤。
func (s *Store) FindCredential(ctx context.Context, id int64) (DNSCredential, error) {
	v, err := scanDNS(s.db.QueryRowContext(ctx, credentialColumns+` WHERE id=?`, id))
	return v, normalizeNotFound(err)
}

// ListCredentials 返回所有用途的凭据，包括未启用项。
func (s *Store) ListCredentials(ctx context.Context) ([]DNSCredential, error) {
	rows, err := s.db.QueryContext(ctx, credentialColumns+` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []DNSCredential{}
	for rows.Next() {
		v, err := scanDNS(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}

// SaveCredential 原子保存共享密钥和用途，活动云任务期间禁止修改。
func (s *Store) SaveCredential(ctx context.Context, v DNSCredential, identityHash string) (int64, error) {
	id := v.ID
	zonesJSON := credentialZonesJSON(&v)
	// 引用与秘密同步提交，避免云用途读取到不同版本的密钥。
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var busy int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloud_jobs j JOIN cloud_plans p ON p.id=j.plan_id WHERE p.credential_id=? AND j.status IN ('QUEUED','RUNNING')`, id).Scan(&busy); err != nil {
			return err
		}
		if busy > 0 {
			return ErrCloudBusy
		}
		// 应用层校验后可能有并发请求启用云用途，事务内再次保护已固定的云身份。
		var boundIdentity sql.NullString
		identityErr := tx.QueryRowContext(ctx, `SELECT cloud_identity_hash FROM dns_credentials WHERE id=?`, id).Scan(&boundIdentity)
		if identityErr != nil && !errors.Is(identityErr, sql.ErrNoRows) {
			return identityErr
		}
		if boundIdentity.Valid && boundIdentity.String != identityHash {
			return ErrCloudBusy
		}
		// 仅云用途需要唯一身份；停用云用途后仍保留身份，防止旧资源引用被换到其他账号。
		var cloudIdentity any
		if v.CloudEnabled {
			cloudIdentity = identityHash
		}
		if id == 0 {
			result, err := tx.ExecContext(ctx, `INSERT INTO dns_credentials(name,provider,zone,access_key_id_enc,access_key_secret_enc,enabled,created_at,updated_at,dns_enabled,cloud_enabled,cloud_identity_hash,zones_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.Name, v.Provider, v.Zone, v.AccessKeyID, v.AccessKeySecret, v.Enabled, nowText(), nowText(), v.DNSEnabled, v.CloudEnabled, cloudIdentity, zonesJSON)
			if err != nil {
				return err
			}
			id, err = result.LastInsertId()
			if err != nil {
				return err
			}
		} else {
			if err := protectCredentialZoneReferences(ctx, tx, id, v.Zones); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `UPDATE dns_credentials SET name=?,zone=?,zones_json=?,access_key_id_enc=?,access_key_secret_enc=?,enabled=?,dns_enabled=?,cloud_enabled=?,cloud_identity_hash=COALESCE(cloud_identity_hash,?),last_test_results_json='[]',last_test_status=NULL,last_test_message=NULL,last_test_at=NULL,updated_at=? WHERE id=? AND provider=?`, v.Name, v.Zone, zonesJSON, v.AccessKeyID, v.AccessKeySecret, v.Enabled, v.DNSEnabled, v.CloudEnabled, cloudIdentity, nowText(), id, v.Provider)
			if err != nil {
				return err
			}
			count, _ := result.RowsAffected()
			if count == 0 {
				return ErrNotFound
			}
		}
		return nil
	})
	return id, err
}

// DeleteCredential 由外键约束阻止删除仍被 DNS、云配置或上传记录引用的凭据。
func (s *Store) DeleteCredential(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM dns_credentials WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
