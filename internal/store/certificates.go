// 本文件持久化 ACME 订单、账户、证书版本与仪表盘指标。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
)

// CreateOrder 持久化一笔待执行的证书签发订单。
func (s *Store) CreateOrder(ctx context.Context, order Order) (Order, error) {
	// 自动续期和内部调用也必须经过与人工申请相同的系列互斥检查。
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		order, err = insertOrderTx(ctx, tx, order)
		return err
	})
	return order, err
}

// FindOrder 按主键读取证书订单。
func (s *Store) FindOrder(ctx context.Context, id int64) (Order, error) {
	order, err := scanOrder(s.db.QueryRowContext(ctx, orderSelect+` WHERE o.id=?`, id))
	if err != nil {
		return Order{}, normalizeNotFound(err)
	}
	return order, nil
}

// ListOrders 按创建时间倒序返回证书订单。
func (s *Store) ListOrders(ctx context.Context) ([]Order, error) {
	values, _, err := s.ListOrdersPage(ctx, 1, 200)
	return values, err
}

// ListOrdersPage 按页返回订单并同时提供完整记录总数。
func (s *Store) ListOrdersPage(ctx context.Context, page, pageSize int, filters ...ListFilter) ([]Order, int64, error) {
	var total int64
	where, args := orderFilterQuery(filters)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_orders o LEFT JOIN certificate_series s ON s.id=o.series_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, orderSelect+where+` ORDER BY o.id DESC LIMIT ? OFFSET ?`, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	values := make([]Order, 0)
	for rows.Next() {
		value, err := scanOrder(rows)
		if err != nil {
			return nil, 0, err
		}
		values = append(values, value)
	}
	return values, total, rows.Err()
}

// orderSelect 是订单模型各查询共用的稳定列顺序。
const orderSelect = `SELECT o.id,COALESCE(o.series_id,''),COALESCE(s.code,''),COALESCE(s.name,''),o.domains_json,o.email,o.ca_environment,o.ca_directory,o.credential_id,o.status,o.stage,o.error_message,o.certificate_id,o.requested_by,o.started_at,o.completed_at,o.created_at,o.updated_at FROM certificate_orders o LEFT JOIN certificate_series s ON s.id=o.series_id`

// scanOrder 将当前查询行转换为证书订单模型。
func scanOrder(row rowScanner) (Order, error) {
	var value Order
	var domains, created, updated string
	var errorMessage, started, completed sql.NullString
	var certificateID, requestedBy sql.NullInt64
	err := row.Scan(&value.ID, &value.SeriesID, &value.SeriesCode, &value.SeriesName, &domains, &value.Email, &value.CAEnvironment, &value.CADirectory, &value.CredentialID, &value.Status, &value.Stage, &errorMessage, &certificateID, &requestedBy, &started, &completed, &created, &updated)
	if err != nil {
		return Order{}, err
	}
	if err := json.Unmarshal([]byte(domains), &value.Domains); err != nil {
		return Order{}, err
	}
	value.ErrorMessage = nullableString(errorMessage)
	value.CertificateID = nullableInt64(certificateID)
	value.RequestedBy = nullableInt64(requestedBy)
	value.StartedAt, value.CompletedAt = nullableTime(started), nullableTime(completed)
	value.CreatedAt, value.UpdatedAt = parseTime(created), parseTime(updated)
	return value, nil
}

// StartOrder 将待处理订单原子推进到运行状态。
func (s *Store) StartOrder(ctx context.Context, id int64) error {
	now := nowText()
	// 领取和时间线更新原子完成，已领取或终态订单不会被再次执行。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE certificate_orders SET status='RUNNING',stage='PREPARING_ACCOUNT',error_message=NULL,started_at=?,completed_at=NULL,updated_at=? WHERE id=? AND status='QUEUED'`, now, now, id)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return ErrActiveOrder
		}
		return appendOrderEvent(ctx, tx, id, "PREPARING_ACCOUNT", "开始准备 ACME 账户", now)
	})
}

// UpdateOrderStage 保存运行中订单的细分执行阶段。
func (s *Store) UpdateOrderStage(ctx context.Context, id int64, stage string) error {
	// 阶段变化与事件一起落库，相同阶段回调不会重复生成时间线。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE certificate_orders SET stage=?,updated_at=? WHERE id=? AND status='RUNNING' AND stage<>?`, stage, nowText(), id, stage)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return nil
		}
		return appendOrderEvent(ctx, tx, id, stage, "任务进入下一执行阶段", nowText())
	})
}

// CompleteOrder 关联签发证书并将订单标记为成功。
func (s *Store) CompleteOrder(ctx context.Context, id, certificateID int64) error {
	if err := s.lockDeployment(ctx); err != nil {
		return err
	}
	defer s.unlockDeployment()
	now := nowText()
	// 完成事务同时推进订单终态与系列当前版本，避免部署读到半完成状态。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var status string
		var completedCertificate sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT status,certificate_id FROM certificate_orders WHERE id=?`, id).Scan(&status, &completedCertificate); err != nil {
			return normalizeNotFound(err)
		}
		if status == "SUCCESS" && completedCertificate.Valid && completedCertificate.Int64 == certificateID {
			return nil
		}
		if status != "QUEUED" && status != "RUNNING" {
			return fmt.Errorf("已结束的订单不能改变签发结果")
		}
		result, err := tx.ExecContext(ctx, `UPDATE certificate_orders SET status='SUCCESS',stage='COMPLETED',certificate_id=?,error_message=NULL,completed_at=?,updated_at=? WHERE id=? AND series_id=(SELECT series_id FROM certificates WHERE id=?)`, certificateID, now, now, id, certificateID)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return fmt.Errorf("订单与证书系列不一致")
		}
		if err := appendOrderEvent(ctx, tx, id, "COMPLETED", "证书已安全保存，部署目标将自动同步", now); err != nil {
			return err
		}
		seriesResult, err := tx.ExecContext(ctx, `UPDATE certificate_series SET current_certificate_id=?,updated_at=?
			WHERE id=(SELECT series_id FROM certificates WHERE id=?)
			AND (current_certificate_id IS NULL OR COALESCE((SELECT version FROM certificates WHERE id=current_certificate_id),0)<=(SELECT version FROM certificates WHERE id=?))`, certificateID, now, certificateID, certificateID)
		if err != nil {
			return err
		}
		seriesUpdated, _ := seriesResult.RowsAffected()
		if seriesUpdated == 0 {
			return nil
		}
		// 新版本入库后立即让全部目标退出旧的成功态；Agent 修订递增也会强制重新对账。
		_, err = tx.ExecContext(ctx, `UPDATE deployment_targets SET status='PENDING',last_error=NULL,revision=revision+1,updated_at=? WHERE enabled=1 AND certificate_series_id=(SELECT series_id FROM certificates WHERE id=?)`, now, certificateID)
		return err
	})
}

// FailOrder 原子保存失败终态、时间线和可选的首个通知，重复终态回调不重复入队。
func (s *Store) FailOrder(ctx context.Context, id int64, message string, notifications ...AlertEvent) error {
	now := nowText()
	// 失败摘要和终态原子保存，重复失败回调不重复追加事件。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE certificate_orders SET status='FAILED',stage='FAILED',error_message=?,completed_at=?,updated_at=? WHERE id=? AND status IN ('QUEUED','RUNNING')`, message, now, now, id)
		if err != nil {
			return err
		}
		count, _ := result.RowsAffected()
		if count == 0 {
			return nil
		}
		if err := appendOrderEvent(ctx, tx, id, "FAILED", message, now); err != nil {
			return err
		}
		if len(notifications) > 0 {
			return enqueueAlert(ctx, tx, notifications[0], fmt.Sprintf("order.failure:%d", id))
		}
		return nil
	})
}

// FailInterruptedOrders 原子终结上次退出遗留订单；首个可选参数为 true 时同时保存恢复通知。
func (s *Store) FailInterruptedOrders(ctx context.Context, notify ...bool) error {
	now := nowText()
	// 重启恢复同时保存时间线，避免只改变订单快照而丢失中断原因。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if len(notify) > 0 && notify[0] {
			rows, err := tx.QueryContext(ctx, `SELECT id FROM certificate_orders WHERE status IN ('QUEUED','RUNNING')`)
			if err != nil {
				return err
			}
			var ids []int64
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, id := range ids {
				event := AlertEvent{Event: "certificate.order.failed", Severity: "ERROR", ResourceType: "CERTIFICATE_ORDER", ResourceID: fmt.Sprint(id), Message: "服务重启导致任务中断，请重试"}
				if err := enqueueAlert(ctx, tx, event, fmt.Sprintf("order.failure:%d", id)); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,stage,message,created_at) SELECT id,'FAILED','服务重启导致任务中断，请重试',? FROM certificate_orders WHERE status IN ('QUEUED','RUNNING')`, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE certificate_orders SET status='FAILED',stage='FAILED',error_message='服务重启导致任务中断，请重试',completed_at=?,updated_at=? WHERE status IN ('QUEUED','RUNNING')`, now, now)
		return err
	})
}

// PruneOrders 删除超过保留期的终态订单，并限制终态订单总量。
func (s *Store) PruneOrders(ctx context.Context, before time.Time, maxRecords int) error {
	if maxRecords <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM certificate_orders WHERE status IN ('SUCCESS','FAILED') AND (completed_at<? OR id NOT IN (SELECT id FROM certificate_orders WHERE status IN ('SUCCESS','FAILED') ORDER BY id DESC LIMIT ?))`, timeText(before), maxRecords)
	return err
}

// HasActiveSeriesOrder 判断系列是否已有待执行或近期自动续期订单。
func (s *Store) HasActiveSeriesOrder(ctx context.Context, seriesID string, automatedSince time.Time) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_orders WHERE series_id=? AND (status IN ('QUEUED','RUNNING') OR (requested_by IS NULL AND created_at>=?))`, seriesID, timeText(automatedSince)).Scan(&count)
	return count > 0, err
}

// InsertCertificate 在单一事务中分配系列内版本并写入证书元数据。
func (s *Store) InsertCertificate(ctx context.Context, value Certificate) (Certificate, error) {
	sans, _ := json.Marshal(value.SANs)
	now := nowText()
	var version int
	var certificateID int64
	// 证书写入事务回调串行分配同一系列的递增版本号。
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		if value.SeriesID == "" {
			return fmt.Errorf("证书缺少系列标识")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM certificates WHERE series_id=?`, value.SeriesID).Scan(&version); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO certificates(series_id,common_name,sans_json,issuer,serial_number,not_before,not_after,fingerprint,version,cert_path,chain_path,key_path,email,ca_directory,credential_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.SeriesID, value.CommonName, string(sans), value.Issuer, value.SerialNumber, timeText(value.NotBefore), timeText(value.NotAfter), value.Fingerprint, version, value.CertPath, value.ChainPath, value.KeyPath, value.Email, value.CADirectory, value.CredentialID, now, now)
		if err != nil {
			return err
		}
		certificateID, err = result.LastInsertId()
		return err
	}); err != nil {
		return Certificate{}, err
	}
	value.ID = certificateID
	value.Version = version
	value.CreatedAt, value.UpdatedAt = parseTime(now), parseTime(now)
	value.Status = certificateStatus(value.NotAfter, time.Now(), 30*24*time.Hour)
	return value, nil
}

// ListCertificates 返回全部证书并计算当前生命周期状态。
func (s *Store) ListCertificates(ctx context.Context, renewBefore time.Duration) ([]Certificate, error) {
	values, _, err := s.ListCertificatesPage(ctx, renewBefore, 1, 200)
	return values, err
}

// ListCertificatesPage 按页返回证书元数据并同时提供完整记录总数。
func (s *Store) ListCertificatesPage(ctx context.Context, renewBefore time.Duration, page, pageSize int, filters ...ListFilter) ([]Certificate, int64, error) {
	var total int64
	where, args := certificateFilterQuery(filters, time.Now().UTC(), renewBefore)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificates c LEFT JOIN certificate_series s ON s.id=c.series_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, certificateSelect+where+` ORDER BY c.id DESC LIMIT ? OFFSET ?`, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	values := make([]Certificate, 0)
	for rows.Next() {
		value, err := scanCertificate(rows, renewBefore)
		if err != nil {
			return nil, 0, err
		}
		values = append(values, value)
	}
	return values, total, rows.Err()
}

// FindCertificate 按主键读取证书并计算生命周期状态。
func (s *Store) FindCertificate(ctx context.Context, id int64, renewBefore time.Duration) (Certificate, error) {
	value, err := scanCertificate(s.db.QueryRowContext(ctx, certificateSelect+` WHERE c.id=?`, id), renewBefore)
	if err != nil {
		return Certificate{}, normalizeNotFound(err)
	}
	return value, nil
}

// FindCurrentCertificate 返回系列指针指向且仍在有效期内的当前证书。
func (s *Store) FindCurrentCertificate(ctx context.Context, seriesID string, renewBefore time.Duration) (Certificate, error) {
	value, err := scanCertificate(s.db.QueryRowContext(ctx, certificateSelect+` JOIN certificate_series current_series ON current_series.current_certificate_id=c.id WHERE current_series.id=? AND current_series.enabled=1 AND c.not_after>?`, seriesID, timeText(time.Now())), renewBefore)
	if err != nil {
		return Certificate{}, normalizeNotFound(err)
	}
	return value, nil
}

// certificateSelect 是证书模型各查询共用的稳定列顺序。
const certificateSelect = `SELECT c.id,COALESCE(c.series_id,''),COALESCE(s.code,''),c.common_name,c.sans_json,c.issuer,c.serial_number,c.not_before,c.not_after,c.fingerprint,c.version,c.cert_path,c.chain_path,c.key_path,c.email,c.ca_directory,c.credential_id,c.created_at,c.updated_at FROM certificates c LEFT JOIN certificate_series s ON s.id=c.series_id`

// scanCertificate 将当前查询行转换为证书模型并计算状态。
func scanCertificate(row rowScanner, renewBefore time.Duration) (Certificate, error) {
	var value Certificate
	var sans, notBefore, notAfter, created, updated string
	err := row.Scan(&value.ID, &value.SeriesID, &value.SeriesCode, &value.CommonName, &sans, &value.Issuer, &value.SerialNumber, &notBefore, &notAfter, &value.Fingerprint, &value.Version, &value.CertPath, &value.ChainPath, &value.KeyPath, &value.Email, &value.CADirectory, &value.CredentialID, &created, &updated)
	if err != nil {
		return Certificate{}, err
	}
	if err := json.Unmarshal([]byte(sans), &value.SANs); err != nil {
		return Certificate{}, err
	}
	value.NotBefore, value.NotAfter = parseTime(notBefore), parseTime(notAfter)
	value.CreatedAt, value.UpdatedAt = parseTime(created), parseTime(updated)
	value.Status = certificateStatus(value.NotAfter, time.Now(), renewBefore)
	return value, nil
}

// certificateStatus 按过期时间和续期窗口计算证书生命周期状态。
func certificateStatus(notAfter, now time.Time, renewBefore time.Duration) string {
	if !notAfter.After(now) {
		return "EXPIRED"
	}
	if !notAfter.After(now.Add(renewBefore)) {
		return "EXPIRING"
	}
	return "VALID"
}

// GetACMEAccount 读取加密持久化的 ACME 账户。
func (s *Store) GetACMEAccount(ctx context.Context, key string) (ACMEAccount, error) {
	var value ACMEAccount
	var registration sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT account_key,email,ca_directory,private_key_enc,registration_json FROM acme_accounts WHERE account_key=?`, key).Scan(&value.Key, &value.Email, &value.CADirectory, &value.PrivateKey, &registration)
	if err != nil {
		return ACMEAccount{}, normalizeNotFound(err)
	}
	value.RegistrationJSON = nullableString(registration)
	return value, nil
}

// PutACMEAccount 创建或更新 ACME 账户与注册资源。
func (s *Store) PutACMEAccount(ctx context.Context, value ACMEAccount) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO acme_accounts(account_key,email,ca_directory,private_key_enc,registration_json,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(account_key) DO UPDATE SET private_key_enc=excluded.private_key_enc,registration_json=excluded.registration_json,updated_at=excluded.updated_at`, value.Key, value.Email, value.CADirectory, value.PrivateKey, nullable(value.RegistrationJSON), nowText())
	return err
}

// Dashboard 聚合证书、订单和 Agent 运行指标。
func (s *Store) Dashboard(ctx context.Context, role string, localDeploymentEnabled bool, renewBefore time.Duration) (Dashboard, error) {
	var value Dashboard
	value.Role = role
	value.LocalDeploymentEnabled = localDeploymentEnabled
	now := time.Now().UTC()
	counts, err := s.certificateCounts(ctx, ` WHERE c.id IN (`+monitoredCertificateIDs+`)`, nil, now, renewBefore)
	if err != nil {
		return Dashboard{}, err
	}
	value.CertificateTotal, value.CertificateValid, value.CertificateExpiring, value.CertificateExpired = counts.Total, counts.Valid, counts.Expiring, counts.Expired
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificates`).Scan(&value.HistoryVersionTotal); err != nil {
		return Dashboard{}, err
	}
	value.UpcomingCertificates, err = s.MonitoredCertificates(ctx, renewBefore, 5)
	if err != nil {
		return Dashboard{}, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN status IN ('QUEUED','RUNNING') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='FAILED' THEN 1 ELSE 0 END),0) FROM certificate_orders`).Scan(&value.OrderRunning, &value.OrderFailed); err != nil {
		return Dashboard{}, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN enabled=1 AND last_seen_at>? THEN 1 ELSE 0 END),0),COUNT(*) FROM agents`, timeText(now.Add(-config.AgentOfflineAfter))).Scan(&value.AgentOnline, &value.AgentTotal); err != nil {
		return Dashboard{}, err
	}
	var nearest sql.NullString
	if err = s.db.QueryRowContext(ctx, `SELECT MIN(not_after) FROM certificates WHERE id IN (`+monitoredCertificateIDs+`) AND not_after>?`, timeText(now)).Scan(&nearest); err != nil {
		return Dashboard{}, err
	}
	value.NearestExpiry = nullableTime(nearest)
	return value, nil
}

// CAEnvironmentDirectory 将界面环境标识映射为受支持的 ACME Directory。
func CAEnvironmentDirectory(environment string) (string, error) {
	switch environment {
	case "LETS_ENCRYPT_PRODUCTION":
		return ProductionDirectory, nil
	case "LETS_ENCRYPT_STAGING":
		return StagingDirectory, nil
	default:
		return "", fmt.Errorf("不支持的 CA 环境 %q", environment)
	}
}
