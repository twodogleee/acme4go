// 本文件在事务内受理证书申请并持久化脱敏执行时间线。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ErrActiveOrder 表示同一系列已有排队或运行中的订单。
var ErrActiveOrder = errors.New("同一证书系列已有排队或运行中的申请，请等待当前任务完成")

// ErrSeriesChanged 表示参数校验后系列配置发生变化，调用方应重新读取再提交。
var ErrSeriesChanged = errors.New("证书系列配置已变化，请刷新后重试")

// OrderEvent 是可以安全展示给管理员的订单阶段记录。
type OrderEvent struct {
	ID        int64  `json:"id"`        // ID 是事件的稳定顺序标识。
	OrderID   int64  `json:"orderId"`   // OrderID 是所属订单。
	Stage     string `json:"stage"`     // Stage 是业务执行阶段。
	Message   string `json:"message"`   // Message 是安全说明。
	CreatedAt string `json:"createdAt"` // CreatedAt 是事件 UTC 时间。
}

// CreateOrderWithSeries 在校验通过后原子创建系列和订单，任何失败均不遗留新系列。
func (s *Store) CreateOrderWithSeries(ctx context.Context, series CertificateSeries, requestedBy *int64, validatedZones ...string) (Order, error) {
	var result Order
	// 受理事务校验配置快照，并让系列自然键和活动订单约束一起生效。
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		domains, err := json.Marshal(series.Domains)
		if err != nil {
			return err
		}
		if series.Revision == 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO certificate_series(id,code,name,domains_json,domains_hash,email,ca_environment,ca_directory,credential_id,auto_renew,enabled,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,1,1,1,?,?) ON CONFLICT(ca_directory,domains_hash) DO NOTHING`, series.ID, series.Code, series.Name, string(domains), series.DomainsHash, series.Email, series.CAEnvironment, series.CADirectory, series.CredentialID, nowText(), nowText()); err != nil {
				return err
			}
		}
		stored, err := scanCertificateSeries(tx.QueryRowContext(ctx, certificateSeriesSelect+` WHERE s.ca_directory=? AND s.domains_hash=?`, series.CADirectory, series.DomainsHash), 0)
		if err != nil {
			return err
		}
		if !stored.Enabled || stored.CredentialID != series.CredentialID || stored.Email != series.Email || (series.Revision != 0 && stored.Revision != series.Revision) {
			return ErrSeriesChanged
		}
		if err := checkCredentialZonesSnapshot(ctx, tx, stored.CredentialID, validatedZones, true); err != nil {
			return err
		}
		result, err = insertOrderTx(ctx, tx, Order{SeriesID: stored.ID, SeriesCode: stored.Code, SeriesName: stored.Name, Domains: stored.Domains, Email: stored.Email, CAEnvironment: stored.CAEnvironment, CADirectory: stored.CADirectory, CredentialID: stored.CredentialID, RequestedBy: requestedBy})
		return err
	})
	return result, err
}

// insertOrderTx 在同一写事务内检查系列互斥并保存排队事件。
func insertOrderTx(ctx context.Context, tx *sql.Tx, order Order) (Order, error) {
	if order.SeriesID != "" {
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM certificate_orders WHERE series_id=? AND status IN ('QUEUED','RUNNING'))`, order.SeriesID).Scan(&active); err != nil {
			return Order{}, err
		}
		if active {
			return Order{}, ErrActiveOrder
		}
	}
	domains, err := json.Marshal(order.Domains)
	if err != nil {
		return Order{}, err
	}
	now := nowText()
	result, err := tx.ExecContext(ctx, `INSERT INTO certificate_orders(series_id,domains_json,email,ca_environment,ca_directory,credential_id,status,stage,requested_by,created_at,updated_at) VALUES(?,?,?,?,?,?,'QUEUED','QUEUED',?,?,?)`, order.SeriesID, string(domains), order.Email, order.CAEnvironment, order.CADirectory, order.CredentialID, nullableID(order.RequestedBy), now, now)
	if err != nil {
		return Order{}, err
	}
	order.ID, err = result.LastInsertId()
	if err != nil {
		return Order{}, err
	}
	order.Status, order.Stage = "QUEUED", "QUEUED"
	order.CreatedAt, order.UpdatedAt = parseTime(now), parseTime(now)
	if err := appendOrderEvent(ctx, tx, order.ID, "QUEUED", "申请已受理，等待签发槽位", now); err != nil {
		return Order{}, err
	}
	return order, nil
}

// appendOrderEvent 写入调用方已经脱敏的阶段说明。
func appendOrderEvent(ctx context.Context, tx *sql.Tx, orderID int64, stage, message, timestamp string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO order_events(order_id,stage,message,created_at) VALUES(?,?,?,?)`, orderID, stage, message, timestamp)
	return err
}

// ListOrderEvents 按发生顺序返回订单时间线，历史订单至少保留升级时的状态快照。
func (s *Store) ListOrderEvents(ctx context.Context, orderID int64) ([]OrderEvent, error) {
	if _, err := s.FindOrder(ctx, orderID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,order_id,stage,message,created_at FROM order_events WHERE order_id=? ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]OrderEvent, 0)
	for rows.Next() {
		var value OrderEvent
		if err := rows.Scan(&value.ID, &value.OrderID, &value.Stage, &value.Message, &value.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
