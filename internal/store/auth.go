// 本文件持久化管理员、会话摘要与审计事件。
//
// @author Andersen
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// UserCount 返回控制平面用户总数。
func (s *Store) UserCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

// CreateUser 保存密码哈希并返回新管理员账户。
func (s *Store) CreateUser(ctx context.Context, username, passwordHash, displayName string) (User, error) {
	now := nowText()
	result, err := s.db.ExecContext(ctx, `INSERT INTO users(username,password_hash,display_name,created_at) VALUES(?,?,?,?)`, username, passwordHash, displayName, now)
	if err != nil {
		return User{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, DisplayName: displayName, Password: passwordHash, CreatedAt: parseTime(now)}, nil
}

// FindUserByUsername 按登录名查找用户。
func (s *Store) FindUserByUsername(ctx context.Context, username string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,username,password_hash,display_name,created_at,last_login_at FROM users WHERE username=?`, username))
}

// FindUserByID 按数据库主键查找用户。
func (s *Store) FindUserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id,username,password_hash,display_name,created_at,last_login_at FROM users WHERE id=?`, id))
}

// scanUser 将单行查询结果转换为用户模型。
func scanUser(row *sql.Row) (User, error) {
	var user User
	var created string
	var last sql.NullString
	err := row.Scan(&user.ID, &user.Username, &user.Password, &user.DisplayName, &created, &last)
	if err != nil {
		return User{}, err
	}
	user.CreatedAt = parseTime(created)
	user.LastLoginAt = nullableTime(last)
	return user, nil
}

// TouchLogin 更新用户最近一次登录时间。
func (s *Store) TouchLogin(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, nowText(), userID)
	return err
}

// UpdatePassword 仅接受仍有效的旧密码校验结果，更新哈希并撤销该用户全部会话。
func (s *Store) UpdatePassword(ctx context.Context, userID int64, verifiedPasswordHash, passwordHash string) error {
	// 密码更新事务回调同时撤销该用户全部现有会话。
	return s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=? WHERE id=? AND password_hash=?`, passwordHash, userID, verifiedPasswordHash)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrCredentialsChanged
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id=?`, userID)
		return err
	})
}

// ErrCredentialsChanged 表示密码验证后凭据已变化，不能创建基于旧凭据的会话。
var ErrCredentialsChanged = errors.New("认证凭据已变化，请重新登录")

// CreateSession 仅在已验证的密码摘要仍为当前值时原子保存会话，避免改密后复活旧认证。
func (s *Store) CreateSession(ctx context.Context, tokenHash, csrfHash string, userID int64, verifiedPasswordHash string, expiresAt time.Time) error {
	result, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions(token_hash,csrf_hash,user_id,expires_at,created_at)
		SELECT ?,?,id,?,? FROM users WHERE id=? AND password_hash=?`, tokenHash, csrfHash, timeText(expiresAt), nowText(), userID, verifiedPasswordHash)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrCredentialsChanged
	}
	return err
}

// SessionUser 通过未过期会话查找用户及 CSRF 哈希。
func (s *Store) SessionUser(ctx context.Context, tokenHash string, now time.Time) (User, string, error) {
	var user User
	var csrfHash, created string
	var last sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id,u.username,u.password_hash,u.display_name,u.created_at,u.last_login_at,s.csrf_hash
		FROM auth_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires_at>?`, tokenHash, timeText(now)).Scan(
		&user.ID, &user.Username, &user.Password, &user.DisplayName, &created, &last, &csrfHash,
	)
	if err != nil {
		return User{}, "", err
	}
	user.CreatedAt = parseTime(created)
	user.LastLoginAt = nullableTime(last)
	return user, csrfHash, nil
}

// DeleteSession 删除指定令牌哈希对应的会话。
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE token_hash=?`, tokenHash)
	return err
}

// DeleteExpiredSessions 清理给定时间前已失效的会话。
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE expires_at<=?`, timeText(now))
	return err
}

// Audit 追加一条不可变的管理员操作审计记录。
func (s *Store) Audit(ctx context.Context, userID *int64, action, resourceType, resourceID, detail string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_logs(user_id,action,resource_type,resource_id,detail,created_at) VALUES(?,?,?,?,?,?)`, nullableID(userID), action, resourceType, resourceID, detail, nowText())
	return err
}

// ListAuditLogs 按递减主键游标和可选动作、资源类型筛选审计记录。
func (s *Store) ListAuditLogs(ctx context.Context, cursor int64, limit int, action, resourceType string) (AuditLogPage, error) {
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT l.id,l.user_id,COALESCE(u.username,''),l.action,l.resource_type,l.resource_id,l.detail,l.created_at
		FROM audit_logs l LEFT JOIN users u ON u.id=l.user_id
		WHERE (?=0 OR l.id<?) AND (?='' OR l.action=?) AND (?='' OR l.resource_type=?)
		ORDER BY l.id DESC LIMIT ?`, cursor, cursor, action, action, resourceType, resourceType, limit+1)
	if err != nil {
		return AuditLogPage{}, err
	}
	defer rows.Close()
	items := make([]AuditLog, 0, limit)
	for rows.Next() {
		var value AuditLog
		var userID sql.NullInt64
		var resourceID, detail sql.NullString
		var created string
		if err := rows.Scan(&value.ID, &userID, &value.Username, &value.Action, &value.ResourceType, &resourceID, &detail, &created); err != nil {
			return AuditLogPage{}, err
		}
		value.UserID = nullableInt64(userID)
		value.ResourceID, value.Detail = resourceID.String, detail.String
		value.CreatedAt = parseTime(created)
		items = append(items, value)
	}
	if err := rows.Err(); err != nil {
		return AuditLogPage{}, err
	}
	page := AuditLogPage{Items: items}
	if len(page.Items) > limit {
		cursorValue := page.Items[limit-1].ID
		page.NextCursor = &cursorValue
		page.Items = page.Items[:limit]
	}
	return page, nil
}

// PruneAuditLogs 删除超过保留期的审计记录，并限制数据库中的最大记录数量。
func (s *Store) PruneAuditLogs(ctx context.Context, before time.Time, maxRecords int) error {
	if maxRecords <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM audit_logs WHERE created_at<? OR id NOT IN (SELECT id FROM audit_logs ORDER BY id DESC LIMIT ?)`, timeText(before), maxRecords)
	return err
}

// ErrNotFound 是仓库对外统一暴露的资源不存在错误。
var ErrNotFound = errors.New("记录不存在")

// normalizeNotFound 将 SQL 无记录错误转换为仓库统一错误。
func normalizeNotFound(err error) error {
	if isNotFound(err) {
		return ErrNotFound
	}
	return err
}
