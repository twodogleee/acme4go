// 本文件实现证书系列身份、兼容迁移和系列查询。
//
// @author Andersen
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// legacySeriesSeed 是旧证书或订单迁移为系列时使用的中间记录。
type legacySeriesSeed struct {
	ID             string   // ID 是可重复计算的迁移系列标识。
	Code           string   // Code 是可重复计算的迁移系列编码。
	Name           string   // Name 是迁移系列显示名称。
	Domains        []string // Domains 是迁移系列的域名组合。
	DomainsHash    string   // DomainsHash 是域名组合的顺序无关摘要。
	Email          string   // Email 是续期联系邮箱。
	CAEnvironment  string   // CAEnvironment 是 CA 环境标识。
	CADirectory    string   // CADirectory 是 ACME Directory。
	CredentialID   int64    // CredentialID 是 DNS 凭据主键。
	CertificateID  *int64   // CertificateID 是迁移时选择的当前证书主键。
	CertificateIDs []int64  // CertificateIDs 是归入系列的历史证书主键。
	OrderIDs       []int64  // OrderIDs 是归入系列的历史订单主键。
}

// legacyCertificateRow 保存迁移所需的旧证书字段。
type legacyCertificateRow struct {
	ID           int64    // ID 是旧证书主键。
	Domains      []string // Domains 是旧证书 SAN 列表。
	Email        string   // Email 是旧证书联系邮箱。
	CADirectory  string   // CADirectory 是旧证书 CA Directory。
	CredentialID int64    // CredentialID 是旧证书 DNS 凭据主键。
}

// legacyOrderRow 保存迁移所需的旧订单字段。
type legacyOrderRow struct {
	ID            int64    // ID 是旧订单主键。
	Domains       []string // Domains 是订单域名列表。
	Email         string   // Email 是订单联系邮箱。
	CAEnvironment string   // CAEnvironment 是订单 CA 环境。
	CADirectory   string   // CADirectory 是订单 CA Directory。
	CredentialID  int64    // CredentialID 是订单 DNS 凭据主键。
}

// DomainSetHash 返回域名集合与输入顺序无关的 SHA-256 摘要。
func DomainSetHash(domains []string) string {
	unique := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		canonical := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		unique[canonical] = struct{}{}
	}
	canonical := make([]string, 0, len(unique))
	for domain := range unique {
		canonical = append(canonical, domain)
	}
	sort.Strings(canonical)
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\x00")))
	return hex.EncodeToString(digest[:])
}

// migrateCertificateSeries 为旧表补列、归并历史记录并建立系列索引。
func (s *Store) migrateCertificateSeries(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_certificates_series_version`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "certificates", "series_id", "TEXT REFERENCES certificate_series(id)"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "certificate_orders", "series_id", "TEXT REFERENCES certificate_series(id)"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "deployment_targets", "certificate_series_id", "TEXT REFERENCES certificate_series(id)"); err != nil {
		return err
	}
	certificates, err := s.readLegacyCertificates(ctx)
	if err != nil {
		return err
	}
	orders, err := s.readLegacyOrders(ctx)
	if err != nil {
		return err
	}
	seeds := make(map[string]*legacySeriesSeed)
	for _, certificate := range certificates {
		seed := mergeLegacySeed(seeds, certificate.Domains, certificate.Email, caEnvironment(certificate.CADirectory), certificate.CADirectory, certificate.CredentialID)
		seed.CertificateIDs = append(seed.CertificateIDs, certificate.ID)
		current := certificate.ID
		seed.CertificateID = &current
	}
	for _, order := range orders {
		seed := mergeLegacySeed(seeds, order.Domains, order.Email, order.CAEnvironment, order.CADirectory, order.CredentialID)
		seed.OrderIDs = append(seed.OrderIDs, order.ID)
	}
	// 迁移事务回调原子创建系列、关联历史数据并处理旧部署目标。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		now := nowText()
		for _, seed := range seeds {
			domainsJSON, _ := json.Marshal(seed.Domains)
			if _, err := tx.ExecContext(ctx, `INSERT INTO certificate_series(id,code,name,domains_json,domains_hash,email,ca_environment,ca_directory,credential_id,current_certificate_id,auto_renew,enabled,revision,created_at,updated_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,1,1,1,?,?) ON CONFLICT(ca_directory,domains_hash) DO NOTHING`, seed.ID, seed.Code, seed.Name, string(domainsJSON), seed.DomainsHash, seed.Email, seed.CAEnvironment, seed.CADirectory, seed.CredentialID, nullableID(seed.CertificateID), now, now); err != nil {
				return err
			}
			var seriesID string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM certificate_series WHERE ca_directory=? AND domains_hash=?`, seed.CADirectory, seed.DomainsHash).Scan(&seriesID); err != nil {
				return err
			}
			for _, certificateID := range seed.CertificateIDs {
				if _, err := tx.ExecContext(ctx, `UPDATE certificates SET series_id=? WHERE id=? AND series_id IS NULL`, seriesID, certificateID); err != nil {
					return err
				}
			}
			for _, orderID := range seed.OrderIDs {
				if _, err := tx.ExecContext(ctx, `UPDATE certificate_orders SET series_id=? WHERE id=? AND series_id IS NULL`, seriesID, orderID); err != nil {
					return err
				}
			}
			if seed.CertificateID != nil {
				if _, err := tx.ExecContext(ctx, `UPDATE certificate_series SET current_certificate_id=?,updated_at=? WHERE id=?`, *seed.CertificateID, now, seriesID); err != nil {
					return err
				}
			}
		}
		if err := migrateLegacyTargets(ctx, tx, now); err != nil {
			return err
		}
		statements := []string{
			`CREATE INDEX IF NOT EXISTS idx_orders_series ON certificate_orders(series_id,status)`,
			`CREATE INDEX IF NOT EXISTS idx_certificates_series ON certificates(series_id,version DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_targets_series ON deployment_targets(certificate_series_id,enabled)`,
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
}

// ensureColumn 在兼容旧数据库时按需增加列。
func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

// readLegacyCertificates 读取尚未绑定系列的历史证书。
func (s *Store) readLegacyCertificates(ctx context.Context) ([]legacyCertificateRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,sans_json,email,ca_directory,credential_id FROM certificates WHERE series_id IS NULL ORDER BY version,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]legacyCertificateRow, 0)
	for rows.Next() {
		var value legacyCertificateRow
		var domainsJSON string
		if err := rows.Scan(&value.ID, &domainsJSON, &value.Email, &value.CADirectory, &value.CredentialID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(domainsJSON), &value.Domains); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// readLegacyOrders 读取尚未绑定系列的历史订单。
func (s *Store) readLegacyOrders(ctx context.Context) ([]legacyOrderRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,domains_json,email,ca_environment,ca_directory,credential_id FROM certificate_orders WHERE series_id IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]legacyOrderRow, 0)
	for rows.Next() {
		var value legacyOrderRow
		var domainsJSON string
		if err := rows.Scan(&value.ID, &domainsJSON, &value.Email, &value.CAEnvironment, &value.CADirectory, &value.CredentialID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(domainsJSON), &value.Domains); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// mergeLegacySeed 合并相同 CA 与域名集合的历史记录。
func mergeLegacySeed(seeds map[string]*legacySeriesSeed, domains []string, email, environment, directory string, credentialID int64) *legacySeriesSeed {
	domainsHash := DomainSetHash(domains)
	identity := sha256.Sum256([]byte(directory + "\x00" + domainsHash))
	idHex := hex.EncodeToString(identity[:])
	key := directory + "\x00" + domainsHash
	if seed, ok := seeds[key]; ok {
		return seed
	}
	name := "历史证书系列"
	if len(domains) > 0 {
		name = domains[0]
	}
	seed := &legacySeriesSeed{ID: "series_" + idHex, Code: "CERT-" + strings.ToUpper(idHex[:16]), Name: name, Domains: append([]string(nil), domains...), DomainsHash: domainsHash, Email: email, CAEnvironment: environment, CADirectory: directory, CredentialID: credentialID}
	seeds[key] = seed
	return seed
}

// migrateLegacyTargets 仅在主域名可唯一对应一个系列时迁移旧部署目标。
func migrateLegacyTargets(ctx context.Context, tx *sql.Tx, now string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,common_name FROM deployment_targets WHERE certificate_series_id IS NULL`)
	if err != nil {
		return err
	}
	targets := make([]struct {
		ID         int64  // ID 是旧部署目标主键。
		CommonName string // CommonName 是旧目标匹配字段。
	}, 0)
	for rows.Next() {
		var target struct {
			ID         int64  // ID 是旧部署目标主键。
			CommonName string // CommonName 是旧目标匹配字段。
		}
		if err := rows.Scan(&target.ID, &target.CommonName); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, target)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, target := range targets {
		candidateRows, err := tx.QueryContext(ctx, `SELECT s.id FROM certificate_series s WHERE json_extract(s.domains_json,'$[0]')=?`, target.CommonName)
		if err != nil {
			return err
		}
		candidates := make([]string, 0, 2)
		for candidateRows.Next() {
			var candidate string
			if err := candidateRows.Scan(&candidate); err != nil {
				candidateRows.Close()
				return err
			}
			candidates = append(candidates, candidate)
		}
		if err := candidateRows.Close(); err != nil {
			return err
		}
		if len(candidates) == 1 {
			if _, err := tx.ExecContext(ctx, `UPDATE deployment_targets SET certificate_series_id=?,updated_at=? WHERE id=?`, candidates[0], now, target.ID); err != nil {
				return err
			}
			continue
		}
		message := "历史部署目标无法唯一匹配证书系列，请重新选择证书系列"
		if _, err := tx.ExecContext(ctx, `UPDATE deployment_targets SET status='NEEDS_BINDING',last_error=?,updated_at=? WHERE id=?`, message, now, target.ID); err != nil {
			return err
		}
	}
	return nil
}

// ResolveCertificateSeries 创建域名组合系列；已有系列保持独立维护的续期配置不变。
func (s *Store) ResolveCertificateSeries(ctx context.Context, value CertificateSeries) (CertificateSeries, error) {
	domainsJSON, _ := json.Marshal(value.Domains)
	now := nowText()
	// 解析事务回调按系列自然键幂等创建，不让一次签发请求隐式覆盖长期配置。
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO certificate_series(id,code,name,domains_json,domains_hash,email,ca_environment,ca_directory,credential_id,auto_renew,enabled,revision,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,1,1,1,?,?) ON CONFLICT(ca_directory,domains_hash) DO NOTHING`, value.ID, value.Code, value.Name, string(domainsJSON), value.DomainsHash, value.Email, value.CAEnvironment, value.CADirectory, value.CredentialID, now, now)
		return err
	})
	if err != nil {
		return CertificateSeries{}, err
	}
	return s.FindCertificateSeriesByIdentity(ctx, value.CADirectory, value.DomainsHash, 0)
}

// UpdateCertificateSeries 显式更新系列的续期配置并递增修订号。
func (s *Store) UpdateCertificateSeries(ctx context.Context, value CertificateSeries, validatedZones ...string) error {
	if err := s.lockDeployment(ctx); err != nil {
		return err
	}
	defer s.unlockDeployment()
	// 停用系列同时撤销未完成的单次授权，避免重新启用时执行旧请求。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if len(validatedZones) > 0 {
			if err := checkCredentialZonesSnapshot(ctx, tx, value.CredentialID, validatedZones, value.Enabled || value.AutoRenew); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE certificate_series SET name=?,email=?,credential_id=?,auto_renew=?,enabled=?,revision=revision+1,updated_at=? WHERE id=?`, value.Name, value.Email, value.CredentialID, value.AutoRenew, value.Enabled, nowText(), value.ID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return ErrNotFound
		}
		if !value.Enabled {
			_, err := tx.ExecContext(ctx, `UPDATE deployment_targets SET manual_sync=0,revision=revision+1,status='FAILED',last_error='证书系列已停用，手动同步已取消',updated_at=? WHERE certificate_series_id=? AND manual_sync=1`, nowText(), value.ID)
			return err
		}
		return nil
	})
}

// FindCertificateSeries 按稳定 ID 读取证书系列。
func (s *Store) FindCertificateSeries(ctx context.Context, id string, renewBefore time.Duration) (CertificateSeries, error) {
	value, err := scanCertificateSeries(s.db.QueryRowContext(ctx, certificateSeriesSelect+` WHERE s.id=?`, id), renewBefore)
	if err != nil {
		return CertificateSeries{}, normalizeNotFound(err)
	}
	return value, nil
}

// FindCertificateSeriesByIdentity 按 CA 与域名集合摘要读取系列。
func (s *Store) FindCertificateSeriesByIdentity(ctx context.Context, directory, domainsHash string, renewBefore time.Duration) (CertificateSeries, error) {
	value, err := scanCertificateSeries(s.db.QueryRowContext(ctx, certificateSeriesSelect+` WHERE s.ca_directory=? AND s.domains_hash=?`, directory, domainsHash), renewBefore)
	if err != nil {
		return CertificateSeries{}, normalizeNotFound(err)
	}
	return value, nil
}

// ListCertificateSeries 返回全部证书系列及当前版本摘要。
func (s *Store) ListCertificateSeries(ctx context.Context, renewBefore time.Duration) ([]CertificateSeries, error) {
	rows, err := s.db.QueryContext(ctx, certificateSeriesSelect+` ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]CertificateSeries, 0)
	for rows.Next() {
		value, err := scanCertificateSeries(rows, renewBefore)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// CertificateSeriesDueForRenewal 返回续期窗口内且启用自动续期的系列配置。
func (s *Store) CertificateSeriesDueForRenewal(ctx context.Context, before time.Time, renewBefore time.Duration) ([]CertificateSeries, error) {
	rows, err := s.db.QueryContext(ctx, certificateSeriesSelect+` WHERE s.enabled=1 AND s.auto_renew=1 AND c.not_after<=? ORDER BY c.not_after`, timeText(before))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]CertificateSeries, 0)
	for rows.Next() {
		value, err := scanCertificateSeries(rows, renewBefore)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// certificateSeriesSelect 是证书系列与当前证书摘要的共用查询列。
const certificateSeriesSelect = `SELECT s.id,s.code,s.name,s.domains_json,s.domains_hash,s.email,s.ca_environment,s.ca_directory,s.credential_id,s.current_certificate_id,s.auto_renew,s.enabled,s.revision,s.created_at,s.updated_at,COALESCE(c.version,0),c.not_after FROM certificate_series s LEFT JOIN certificates c ON c.id=s.current_certificate_id`

// scanCertificateSeries 将查询行转换为证书系列模型。
func scanCertificateSeries(row rowScanner, renewBefore time.Duration) (CertificateSeries, error) {
	var value CertificateSeries
	var domainsJSON, created, updated string
	var currentCertificateID sql.NullInt64
	var currentNotAfter sql.NullString
	var autoRenew, enabled int
	if err := row.Scan(&value.ID, &value.Code, &value.Name, &domainsJSON, &value.DomainsHash, &value.Email, &value.CAEnvironment, &value.CADirectory, &value.CredentialID, &currentCertificateID, &autoRenew, &enabled, &value.Revision, &created, &updated, &value.CurrentVersion, &currentNotAfter); err != nil {
		return CertificateSeries{}, err
	}
	if err := json.Unmarshal([]byte(domainsJSON), &value.Domains); err != nil {
		return CertificateSeries{}, err
	}
	value.CurrentCertificateID = nullableInt64(currentCertificateID)
	value.AutoRenew, value.Enabled = autoRenew == 1, enabled == 1
	value.CreatedAt, value.UpdatedAt = parseTime(created), parseTime(updated)
	value.CurrentNotAfter = nullableTime(currentNotAfter)
	if value.CurrentNotAfter != nil {
		status := certificateStatus(*value.CurrentNotAfter, time.Now(), renewBefore)
		value.CurrentStatus = &status
	}
	return value, nil
}

// caEnvironment 根据已知 Directory 推导界面环境标识。
func caEnvironment(directory string) string {
	if directory == StagingDirectory {
		return "LETS_ENCRYPT_STAGING"
	}
	return "LETS_ENCRYPT_PRODUCTION"
}
