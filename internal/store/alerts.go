// 本文件管理可恢复的 Webhook 投递队列、重试租约和管理员查询。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// maxAlertAttempts 限制每轮投递预算，包含发送结果未确认而恢复的租约。
const maxAlertAttempts = 8

// alertExecutor 允许普通入队和业务事务复用同一事件写入逻辑。
type alertExecutor interface {
	// ExecContext 在当前连接或事务中写入事件。
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// AlertEvent 是稳定且不包含秘密的告警载荷，ID 供接收端幂等去重。
type AlertEvent struct {
	ID           int64     `json:"id,omitempty"` // ID 是持久化事件标识。
	Event        string    `json:"event"`        // Event 是稳定事件类型。
	Severity     string    `json:"severity"`     // Severity 是 ERROR、WARNING 或 INFO。
	ResourceType string    `json:"resourceType"` // ResourceType 是业务资源类型。
	ResourceID   string    `json:"resourceId"`   // ResourceID 是资源标识。
	Message      string    `json:"message"`      // Message 是安全事件说明。
	Timestamp    time.Time `json:"timestamp"`    // Timestamp 是原始事件时间，重试不修改。
}

// AlertDelivery 是投递管理页面使用的事件及传输状态。
type AlertDelivery struct {
	AlertEvent               // AlertEvent 是已经持久化的签名载荷。
	Status        string     `json:"status"`        // Status 是投递状态。
	Attempts      int        `json:"attempts"`      // Attempts 是本轮已经尝试的次数。
	NextAttemptAt time.Time  `json:"nextAttemptAt"` // NextAttemptAt 是下次发送或租约到期时间。
	LastError     *string    `json:"lastError"`     // LastError 是安全失败说明。
	DeliveredAt   *time.Time `json:"deliveredAt"`   // DeliveredAt 是成功投递时间。
}

// EnqueueAlert 先持久化事件，可选去重键保证定时扫描不会重复创建同一提醒。
func (s *Store) EnqueueAlert(ctx context.Context, event AlertEvent, dedupeKey string) error {
	return enqueueAlert(ctx, s.db, event, dedupeKey)
}

// enqueueAlert 将事件写入调用方事务，业务状态与通知可以一起提交或回滚。
func enqueueAlert(ctx context.Context, executor alertExecutor, event AlertEvent, dedupeKey string) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	var key any
	if dedupeKey != "" {
		key = dedupeKey
	}
	_, err := executor.ExecContext(ctx, `INSERT INTO alert_deliveries(dedupe_key,event,severity,resource_type,resource_id,message,next_attempt_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(dedupe_key) DO NOTHING`, key, event.Event, event.Severity, event.ResourceType, event.ResourceID, event.Message, nowText(), timeText(event.Timestamp), nowText())
	return err
}

// alertSelect 统一投递记录查询列的顺序。
const alertSelect = `SELECT id,event,severity,resource_type,resource_id,message,created_at,status,attempts,next_attempt_at,last_error,delivered_at FROM alert_deliveries`

// scanAlert 转换队列行，不从第三方原始响应恢复任何消息。
func scanAlert(row rowScanner) (AlertDelivery, error) {
	var value AlertDelivery
	var created, next string
	var lastError, delivered sql.NullString
	err := row.Scan(&value.ID, &value.Event, &value.Severity, &value.ResourceType, &value.ResourceID, &value.Message, &created, &value.Status, &value.Attempts, &next, &lastError, &delivered)
	value.Timestamp, value.NextAttemptAt = parseTime(created), parseTime(next)
	value.LastError, value.DeliveredAt = nullableString(lastError), nullableTime(delivered)
	return value, normalizeNotFound(err)
}

// ClaimAlert 原子认领一个到期事件；过期 SENDING 租约可在进程重启后恢复。
func (s *Store) ClaimAlert(ctx context.Context, now time.Time) (AlertDelivery, error) {
	var value AlertDelivery
	// 读取和租约递增在同一事务内完成，防止多个投递循环发送同一事件。
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// 租约耗尽也必须终结；没有可认领事件时仍提交这一清理结果。
		if _, err := tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='FAILED',last_error='投递次数已耗尽，最后一次结果未确认，可人工重新发送',updated_at=? WHERE status IN ('PENDING','RETRY','SENDING') AND attempts>=? AND next_attempt_at<=?`, timeText(now), maxAlertAttempts, timeText(now)); err != nil {
			return err
		}
		var err error
		value, err = scanAlert(tx.QueryRowContext(ctx, alertSelect+` WHERE status IN ('PENDING','RETRY','SENDING') AND attempts<? AND next_attempt_at<=? ORDER BY next_attempt_at,id LIMIT 1`, maxAlertAttempts, timeText(now)))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='SENDING',attempts=attempts+1,next_attempt_at=?,updated_at=? WHERE id=?`, timeText(now.Add(time.Minute)), timeText(now), value.ID)
		value.Status, value.Attempts = "SENDING", value.Attempts+1
		value.NextAttemptAt = now.Add(time.Minute)
		return err
	})
	if err == nil && value.ID == 0 {
		return value, ErrNotFound
	}
	return value, err
}

// FinishAlert 同时核对次数和租约截止时间，人工重发重置次数后旧工作者也不能覆盖新租约。
func (s *Store) FinishAlert(ctx context.Context, value AlertDelivery, failure string, now time.Time) error {
	status, next := "SENT", now
	var message, delivered any
	if failure == "" {
		delivered = timeText(now)
	} else {
		message = failure
		status = "RETRY"
		if value.Attempts >= maxAlertAttempts {
			status = "FAILED"
		}
		delay := 15 * time.Second
		for attempt := 1; attempt < value.Attempts && delay < 15*time.Minute; attempt++ {
			delay *= 2
		}
		if delay > 15*time.Minute {
			delay = 15 * time.Minute
		}
		next = now.Add(delay)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE alert_deliveries SET status=?,next_attempt_at=?,last_error=?,delivered_at=?,updated_at=? WHERE id=? AND status='SENDING' AND attempts=? AND next_attempt_at=?`, status, timeText(next), message, delivered, timeText(now), value.ID, value.Attempts, timeText(value.NextAttemptAt))
	return err
}

// RetryAlert 将失败或等待重试的事件重新入队，正在发送和已成功事件不允许重发。
func (s *Store) RetryAlert(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE alert_deliveries SET status='PENDING',attempts=0,next_attempt_at=?,last_error=NULL,updated_at=? WHERE id=? AND status IN ('FAILED','RETRY')`, nowText(), nowText(), id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAlertDeliveries 按状态分页返回完整投递历史。
func (s *Store) ListAlertDeliveries(ctx context.Context, page, pageSize int, status string) ([]AlertDelivery, int64, error) {
	where, args := " WHERE 1=1", []any{}
	if status != "" && status != "ALL" {
		where += ` AND status=?`
		args = append(args, status)
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alert_deliveries`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, alertSelect+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	values := make([]AlertDelivery, 0)
	for rows.Next() {
		value, err := scanAlert(rows)
		if err != nil {
			return nil, 0, err
		}
		values = append(values, value)
	}
	return values, total, rows.Err()
}

// PruneAlerts 按保留期清理终态投递，始终保留未完成队列。
func (s *Store) PruneAlerts(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_deliveries WHERE status IN ('SENT','FAILED') AND updated_at<?`, timeText(before))
	return err
}
