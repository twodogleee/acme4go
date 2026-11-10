// 本文件持久化凭据多根域名与逐域名测试结果，并在事务内保护证书系列的 DNS 范围。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/dnsnames"
)

// DNSZoneTestResult 是单个根域名的安全测试结果，不保存服务商原始错误或秘密。
type DNSZoneTestResult struct {
	Zone     string    `json:"zone"`     // Zone 是本次实际验证的根域名。
	Success  bool      `json:"success"`  // Success 表示查询、写入及清理均成功。
	Message  string    `json:"message"`  // Message 是可公开的失败原因或成功说明。
	TestedAt time.Time `json:"testedAt"` // TestedAt 是该域名测试开始的 UTC 时间。
}

// CredentialScopeError 列出缩小范围后无法继续签发的系列或运行中订单。
type CredentialScopeError struct {
	References []string // References 是受影响的业务名称，仅用于展示。
}

// Error 指引管理员先迁移引用，避免静默破坏续期。
func (e *CredentialScopeError) Error() string {
	return "无法移除仍被使用的根域名，受影响的证书系列或申请：" + strings.Join(e.References, "、") + "。请先为相关系列更换 DNS 凭据，并等待运行中的申请完成"
}

// migrateCredentialZones 增量补齐两列，只把旧单域名转成数组，保留主键、密文和所有引用。
func (s *Store) migrateCredentialZones(ctx context.Context) error {
	// 在同一事务中补齐字段并迁移旧的单根域名。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		for _, column := range []string{"zones_json", "last_test_results_json"} {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('dns_credentials') WHERE name=?`, column).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				if _, err := tx.ExecContext(ctx, "ALTER TABLE dns_credentials ADD COLUMN "+column+" TEXT NOT NULL DEFAULT '[]'"); err != nil {
					return err
				}
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE dns_credentials SET zones_json=json_array(zone) WHERE zones_json='[]' AND zone<>''`)
		return err
	})
}

// decodeCredentialZones 兼容旧数据或内部单域名调用，正常多域名数据以数组为准。
func decodeCredentialZones(raw, legacy string) ([]string, error) {
	zones := []string{}
	if err := json.Unmarshal([]byte(raw), &zones); err != nil {
		return nil, err
	}
	if len(zones) == 0 && legacy != "" {
		zones = []string{legacy}
	}
	if zones == nil {
		zones = []string{}
	}
	return zones, nil
}

// credentialZonesJSON 保持旧单域名调用兼容，并同步首域名与完整数组，避免双字段不一致。
func credentialZonesJSON(value *DNSCredential) string {
	if value.Zones == nil {
		value.Zones = []string{}
		if value.Zone != "" {
			value.Zones = append(value.Zones, value.Zone)
		}
	}
	value.Zone = ""
	if len(value.Zones) > 0 {
		value.Zone = value.Zones[0]
	}
	raw, _ := json.Marshal(value.Zones)
	return string(raw)
}

// protectCredentialZoneReferences 在保存事务内检查所有系列及活动订单，不允许删除仍被使用的范围。
func protectCredentialZoneReferences(ctx context.Context, tx *sql.Tx, id int64, zones []string) error {
	var legacy, raw string
	if err := tx.QueryRowContext(ctx, `SELECT zone,zones_json FROM dns_credentials WHERE id=?`, id).Scan(&legacy, &raw); err != nil {
		return err
	}
	old, err := decodeCredentialZones(raw, legacy)
	if err != nil {
		return err
	}
	if slices.Equal(old, zones) {
		return nil
	}
	// 扩大或重排范围不会影响旧引用，只在缩小范围时扫描。
	allCovered := true
	for _, zone := range old {
		allCovered = allCovered && dnsnames.CoversDomain(zones, zone)
	}
	if allCovered {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT name || '（' || code || '）',domains_json FROM certificate_series WHERE credential_id=?
		UNION ALL SELECT '申请 #' || id,domains_json FROM certificate_orders WHERE credential_id=? AND status IN ('QUEUED','RUNNING')`, id, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	affected := []string{}
	for rows.Next() {
		var name, domainsJSON string
		if err := rows.Scan(&name, &domainsJSON); err != nil {
			return err
		}
		var domains []string
		if err := json.Unmarshal([]byte(domainsJSON), &domains); err != nil {
			return err
		}
		for _, domain := range domains {
			if !dnsnames.CoversDomain(zones, domain) {
				affected = append(affected, name)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(affected) > 0 {
		return &CredentialScopeError{References: affected}
	}
	return nil
}

// checkCredentialZonesSnapshot 在写事务内再次确认 DNS 用途和完整根域名范围未发生变化。
func checkCredentialZonesSnapshot(ctx context.Context, tx *sql.Tx, id int64, expected []string, requireEnabled bool) error {
	var enabled, dnsEnabled bool
	var legacy, raw string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,dns_enabled,zone,zones_json FROM dns_credentials WHERE id=?`, id).Scan(&enabled, &dnsEnabled, &legacy, &raw); err != nil {
		return err
	}
	zones, err := decodeCredentialZones(raw, legacy)
	if err != nil {
		return err
	}
	if (requireEnabled && (!enabled || !dnsEnabled)) || (len(expected) > 0 && !slices.Equal(zones, expected)) {
		return ErrSeriesChanged
	}
	return nil
}

// RecordDNSZoneTests 仅在凭据未被修改时保存测试快照，避免旧测试覆盖新配置的状态。
func (s *Store) RecordDNSZoneTests(ctx context.Context, tested DNSCredential, results []DNSZoneTestResult) error {
	raw, err := json.Marshal(results)
	if err != nil {
		return err
	}
	successful := 0
	for _, result := range results {
		if result.Success {
			successful++
		}
	}
	status := "FAILED"
	if len(results) > 0 && successful == len(results) {
		status = "SUCCESS"
	}
	message := fmt.Sprintf("已验证 %d 个根域名：%d 个通过，%d 个失败", len(results), successful, len(results)-successful)
	result, err := s.db.ExecContext(ctx, `UPDATE dns_credentials SET last_test_status=?,last_test_message=?,last_test_at=?,last_test_results_json=?,updated_at=? WHERE id=? AND updated_at=?`, status, message, nowText(), string(raw), nowText(), tested.ID, timeText(tested.UpdatedAt))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrSeriesChanged
	}
	return err
}
