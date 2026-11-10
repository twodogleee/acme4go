// 本文件提供全量筛选、证书统计及当前使用版本的监控查询。
//
// @author Andersen
package store

import (
	"context"
	"strings"
	"time"
)

// ListFilter 是订单和证书分页共用的筛选契约。
type ListFilter struct {
	Keyword  string // Keyword 匹配域名、编号或系列名称，不作为 SQL 模式解释。
	Status   string // Status 为空或 ALL 时不限制状态。
	SeriesID string // SeriesID 为空时查询全部系列。
}

// CertificateCounts 汇总筛选范围内全部证书版本的生命周期数量。
type CertificateCounts struct {
	Total    int64 `json:"total"`    // Total 是符合筛选条件的版本数。
	Valid    int64 `json:"valid"`    // Valid 是续期窗口之外的有效版本数。
	Expiring int64 `json:"expiring"` // Expiring 是仍有效且处于续期窗口的版本数。
	Expired  int64 `json:"expired"`  // Expired 是已经过期的版本数。
}

// orderFilterQuery 生成参数化订单筛选，域名数组在数据库内完整搜索。
func orderFilterQuery(filters []ListFilter) (string, []any) {
	where, args := " WHERE 1=1", []any{}
	if len(filters) == 0 {
		return where, args
	}
	filter := filters[0]
	keyword := strings.ToLower(strings.TrimSpace(filter.Keyword))
	if keyword != "" {
		where += ` AND (CAST(o.id AS TEXT)=? OR instr(lower(COALESCE(s.name,'')||' '||COALESCE(s.code,'')),?)>0 OR EXISTS(SELECT 1 FROM json_each(o.domains_json) d WHERE instr(lower(d.value),?)>0))`
		args = append(args, keyword, keyword, keyword)
	}
	if filter.Status != "" && filter.Status != "ALL" {
		where += ` AND o.status=?`
		args = append(args, filter.Status)
	}
	if filter.SeriesID != "" {
		where += ` AND o.series_id=?`
		args = append(args, filter.SeriesID)
	}
	return where, args
}

// certificateFilterQuery 在一个时间快照内生成证书筛选，避免页面自行估算有效期。
func certificateFilterQuery(filters []ListFilter, now time.Time, renewBefore time.Duration) (string, []any) {
	where, args := " WHERE 1=1", []any{}
	if len(filters) == 0 {
		return where, args
	}
	filter := filters[0]
	keyword := strings.ToLower(strings.TrimSpace(filter.Keyword))
	if keyword != "" {
		where += ` AND (instr(lower(c.common_name),?)>0 OR instr(lower(COALESCE(s.name,'')||' '||COALESCE(s.code,'')),?)>0 OR EXISTS(SELECT 1 FROM json_each(c.sans_json) d WHERE instr(lower(d.value),?)>0))`
		args = append(args, keyword, keyword, keyword)
	}
	switch filter.Status {
	case "VALID":
		where += ` AND c.not_after>?`
		args = append(args, timeText(now.Add(renewBefore)))
	case "EXPIRING":
		where += ` AND c.not_after>? AND c.not_after<=?`
		args = append(args, timeText(now), timeText(now.Add(renewBefore)))
	case "EXPIRED":
		where += ` AND c.not_after<=?`
		args = append(args, timeText(now))
	}
	if filter.SeriesID != "" {
		where += ` AND c.series_id=?`
		args = append(args, filter.SeriesID)
	}
	return where, args
}

// CertificateSummary 返回完整筛选范围的统计，数量不受当前页大小限制。
func (s *Store) CertificateSummary(ctx context.Context, renewBefore time.Duration, filters ...ListFilter) (CertificateCounts, error) {
	now := time.Now().UTC()
	where, args := certificateFilterQuery(filters, now, renewBefore)
	return s.certificateCounts(ctx, where, args, now, renewBefore)
}

// certificateCounts 执行指定范围的生命周期聚合。
func (s *Store) certificateCounts(ctx context.Context, where string, args []any, now time.Time, renewBefore time.Duration) (CertificateCounts, error) {
	var value CertificateCounts
	parameters := append([]any{timeText(now.Add(renewBefore)), timeText(now), timeText(now.Add(renewBefore)), timeText(now)}, args...)
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(c.not_after>?),0),COALESCE(SUM(c.not_after>? AND c.not_after<=?),0),COALESCE(SUM(c.not_after<=?),0) FROM certificates c LEFT JOIN certificate_series s ON s.id=c.series_id`+where, parameters...).Scan(&value.Total, &value.Valid, &value.Expiring, &value.Expired)
	return value, err
}

// monitoredCertificateIDs 选择启用系列当前版本和启用部署目标最近成功使用的版本，排除无关历史。
const monitoredCertificateIDs = `SELECT current_certificate_id FROM certificate_series WHERE enabled=1 AND current_certificate_id IS NOT NULL
UNION SELECT COALESCE((SELECT r.certificate_id FROM deployment_records r WHERE r.target_id=t.id AND r.status='SUCCESS' ORDER BY r.id DESC LIMIT 1),CASE WHEN t.status='SUCCESS' THEN t.last_certificate_id END) FROM deployment_targets t WHERE t.enabled=1`

// MonitoredCertificates 从全库按到期时间列出实际需要关注的证书；limit 为零时返回全部。
func (s *Store) MonitoredCertificates(ctx context.Context, renewBefore time.Duration, limit int) ([]Certificate, error) {
	query := certificateSelect + ` WHERE c.id IN (` + monitoredCertificateIDs + `) ORDER BY c.not_after,c.id`
	args := []any{}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Certificate, 0)
	for rows.Next() {
		value, err := scanCertificate(rows, renewBefore)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
