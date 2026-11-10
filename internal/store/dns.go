// 本文件持久化加密 DNS 凭据及其权限测试结果。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// CreateDNSCredential 保存加密后的 DNS 凭据。
func (s *Store) CreateDNSCredential(ctx context.Context, value DNSCredential) (DNSCredential, error) {
	now := nowText()
	zonesJSON := credentialZonesJSON(&value)
	result, err := s.db.ExecContext(ctx, `INSERT INTO dns_credentials(name,provider,zone,access_key_id_enc,access_key_secret_enc,enabled,created_at,updated_at,zones_json) VALUES(?,?,?,?,?,?,?,?,?)`, value.Name, value.Provider, value.Zone, value.AccessKeyID, value.AccessKeySecret, value.Enabled, now, now, zonesJSON)
	if err != nil {
		return DNSCredential{}, err
	}
	value.ID, err = result.LastInsertId()
	value.DNSEnabled = true
	value.CreatedAt, value.UpdatedAt = parseTime(now), parseTime(now)
	return value, err
}

// UpdateDNSCredential 更新 DNS 凭据密文和启用状态。
func (s *Store) UpdateDNSCredential(ctx context.Context, value DNSCredential) error {
	existing, err := s.FindCredential(ctx, value.ID)
	if err != nil {
		return err
	}
	if existing.AccessKeyIDLocked {
		return ErrCloudBusy
	}
	value.DNSEnabled, value.CloudEnabled = existing.DNSEnabled, existing.CloudEnabled
	_, err = s.SaveCredential(ctx, value, "")
	return err
}

// DeleteDNSCredential 删除指定 DNS 凭据。
func (s *Store) DeleteDNSCredential(ctx context.Context, id int64) error {
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

// DNSCredentialUsageCount 统计订单和证书对凭据的引用数量。
func (s *Store) DNSCredentialUsageCount(ctx context.Context, id int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM certificates WHERE credential_id=?)+(SELECT COUNT(*) FROM certificate_orders WHERE credential_id=?)+(SELECT COUNT(*) FROM certificate_series WHERE credential_id=?)`, id, id, id).Scan(&count)
	return count, err
}

// FindDNSCredential 按主键读取一个 DNS 凭据。
func (s *Store) FindDNSCredential(ctx context.Context, id int64) (DNSCredential, error) {
	value, err := s.FindCredential(ctx, id)
	value.Enabled = value.Enabled && value.DNSEnabled
	return value, err
}

// ListDNSCredentials 按创建时间倒序返回全部 DNS 凭据。
func (s *Store) ListDNSCredentials(ctx context.Context) ([]DNSCredential, error) {
	rows, err := s.db.QueryContext(ctx, credentialColumns+` WHERE dns_enabled=1 ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]DNSCredential, 0)
	for rows.Next() {
		value, err := scanDNSRows(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// rowScanner 抽象 sql.Row 和 sql.Rows 共享的字段扫描能力。
type rowScanner interface {
	// Scan 将当前行各列写入给定目标。
	Scan(...any) error
}

// scanDNS 扫描单个 DNS 凭据并规范化未找到错误。
func scanDNS(row rowScanner) (DNSCredential, error) {
	return scanDNSRows(row)
}

// scanDNSRows 将当前查询行转换为 DNS 凭据模型。
func scanDNSRows(row rowScanner) (DNSCredential, error) {
	var value DNSCredential
	var enabled int
	var status, message, tested sql.NullString
	var created, updated, zonesJSON, resultsJSON string
	err := row.Scan(&value.ID, &value.Name, &value.Provider, &value.Zone, &value.AccessKeyID, &value.AccessKeySecret, &enabled, &status, &message, &tested, &created, &updated, &value.DNSEnabled, &value.CloudEnabled, &value.AccessKeyIDLocked, &zonesJSON, &resultsJSON)
	if err != nil {
		return DNSCredential{}, err
	}
	value.Zones, err = decodeCredentialZones(zonesJSON, value.Zone)
	if err != nil {
		return DNSCredential{}, err
	}
	if err := json.Unmarshal([]byte(resultsJSON), &value.LastTestResults); err != nil {
		return DNSCredential{}, err
	}
	value.Enabled = enabled == 1
	value.LastTestStatus = nullableString(status)
	value.LastTestMessage = nullableString(message)
	value.LastTestAt = nullableTime(tested)
	value.CreatedAt, value.UpdatedAt = parseTime(created), parseTime(updated)
	return value, nil
}

// RecordDNSTest 保存凭据最近一次连通性测试结果。
func (s *Store) RecordDNSTest(ctx context.Context, id int64, success bool, message string) error {
	status := "FAILED"
	if success {
		status = "SUCCESS"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE dns_credentials SET last_test_status=?,last_test_message=?,last_test_at=?,updated_at=? WHERE id=?`, status, message, nowText(), nowText(), id)
	return err
}

// MaskAccessKey 保留访问密钥首尾少量字符用于安全辨识。
func MaskAccessKey(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return "****"
	}
	return value[:4] + "****" + value[len(value)-4:]
}

// timePtr 返回指定时间值的指针。
func timePtr(value time.Time) *time.Time { return &value }
