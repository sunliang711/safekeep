package db

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

var ErrLastAdmin = errors.New("at least one active administrator is required")

// AdminSession 保存登录设备信息，令牌只以摘要形式存储。
type AdminSession struct {
	ID         string
	AdminID    string
	TokenHash  string
	ClientIP   string
	UserAgent  string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// CreateAdminSession 仅为凭据和角色仍有效的账号创建会话，避免并发改密后复活旧登录态。
func (s *Store) CreateAdminSession(ctx context.Context, session *AdminSession, admin *Admin) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at <= ?`, time.Now().UTC()); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions(id, admin_id, token_hash, client_ip, user_agent, created_at, last_seen_at, expires_at)
		SELECT ?, id, ?, ?, ?, ?, ?, ? FROM admins WHERE id = ? AND password_hash = ? AND role = ? AND is_active = 1`,
		session.ID, session.TokenHash, session.ClientIP, session.UserAgent, session.CreatedAt, session.LastSeenAt, session.ExpiresAt,
		admin.ID, admin.PasswordHash, admin.Role)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func scanAdminSession(row rowScanner) (*AdminSession, error) {
	var session AdminSession
	if err := row.Scan(&session.ID, &session.AdminID, &session.TokenHash, &session.ClientIP, &session.UserAgent,
		&session.CreatedAt, &session.LastSeenAt, &session.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &session, nil
}

func (s *Store) GetAdminSession(ctx context.Context, tokenHash string) (*AdminSession, error) {
	return scanAdminSession(s.db.QueryRowContext(ctx, `SELECT id, admin_id, token_hash, client_ip, user_agent, created_at, last_seen_at, expires_at
		FROM admin_sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, time.Now().UTC()))
}

func (s *Store) ListAdminSessions(ctx context.Context, adminID string) ([]AdminSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, admin_id, token_hash, client_ip, user_agent, created_at, last_seen_at, expires_at
		FROM admin_sessions WHERE admin_id = ? AND expires_at > ? ORDER BY last_seen_at DESC, id DESC`, adminID, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AdminSession, 0)
	for rows.Next() {
		session, err := scanAdminSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *session)
	}
	return out, rows.Err()
}

func (s *Store) TouchAdminSession(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_sessions SET last_seen_at = ? WHERE id = ? AND last_seen_at <= ?`, now, id, now.Add(-time.Minute))
	return err
}

func (s *Store) DeleteAdminSession(ctx context.Context, adminID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE admin_id = ? AND id = ?`, adminID, id)
	return err
}

// UpdateAdminAccount 将账号变更与会话撤销放在同一事务，并保护最后一个可用管理员。
func (s *Store) UpdateAdminAccount(ctx context.Context, id string, role *string, active *bool, passwordHash *string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldRole string
	var oldActive bool
	if err := tx.QueryRowContext(ctx, `SELECT role, is_active FROM admins WHERE id = ?`, id).Scan(&oldRole, &oldActive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	nextRole, nextActive := oldRole, oldActive
	if role != nil {
		if *role != RoleAdmin && *role != RoleViewer {
			return errors.New("invalid account role")
		}
		nextRole = *role
	}
	if active != nil {
		nextActive = *active
	}
	if oldRole == RoleAdmin && oldActive && (nextRole != RoleAdmin || !nextActive) {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins WHERE role = 'admin' AND is_active = 1`).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE admins SET role = ?, is_active = ?, updated_at = ? WHERE id = ?`, nextRole, nextActive, time.Now().UTC(), id); err != nil {
		return err
	}
	if passwordHash != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE admins SET password_hash = ?, must_change_password = 0 WHERE id = ?`, *passwordHash, id); err != nil {
			return err
		}
	}
	if passwordHash != nil || nextRole != oldRole || !nextActive {
		if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions WHERE admin_id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
