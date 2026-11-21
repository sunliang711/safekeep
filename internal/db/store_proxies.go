package db

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"safekeep/internal/crypto"
)

var ErrProxyInUse = errors.New("proxy is used by upstreams; unbind them before deleting")

const proxySelect = `SELECT p.id, p.name, p.type, p.host, p.port,
	p.username_encrypted, p.password_encrypted, p.created_at, p.updated_at,
	(SELECT COUNT(*) FROM upstreams u WHERE u.proxy_id = p.id) FROM proxies p`

func (s *Store) ListProxies(ctx context.Context) ([]Proxy, error) {
	rows, err := s.db.QueryContext(ctx, proxySelect+` ORDER BY p.created_at, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]Proxy, 0)
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *p)
	}
	return list, rows.Err()
}

func (s *Store) GetProxyByID(ctx context.Context, id string) (*Proxy, error) {
	return scanProxy(s.db.QueryRowContext(ctx, proxySelect+` WHERE p.id = ?`, id))
}

func scanProxy(row rowScanner) (*Proxy, error) {
	var p Proxy
	if err := row.Scan(&p.ID, &p.Name, &p.Type, &p.Host, &p.Port,
		&p.UsernameEncrypted, &p.PasswordEncrypted, &p.CreatedAt, &p.UpdatedAt, &p.UpstreamCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func insertProxy(ctx context.Context, tx *sql.Tx, p *Proxy) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO proxies(id, name, type, host, port,
		username_encrypted, password_encrypted, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Type, p.Host, p.Port, p.UsernameEncrypted, p.PasswordEncrypted, p.CreatedAt, p.UpdatedAt)
	return err
}

func (s *Store) CreateProxy(ctx context.Context, p *Proxy) error {
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertProxy(ctx, tx, p); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateProxy(ctx context.Context, p *Proxy) error {
	p.UpdatedAt = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE proxies SET name=?, type=?, host=?, port=?,
		username_encrypted=?, password_encrypted=?, updated_at=? WHERE id=?`,
		p.Name, p.Type, p.Host, p.Port, p.UsernameEncrypted, p.PasswordEncrypted, p.UpdatedAt, p.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	// 同一事务更新引用方的版本，保证缓存中的客户端在下次使用时重建。
	if _, err := tx.ExecContext(ctx, `UPDATE upstreams SET updated_at=?, last_test_status='untested',
		last_test_error='', last_tested_at=NULL WHERE proxy_id=?`, p.UpdatedAt, p.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteProxy(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id=?
		AND NOT EXISTS (SELECT 1 FROM upstreams WHERE proxy_id=?)`, id, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n > 0 {
		return err
	}
	if _, err := s.GetProxyByID(ctx, id); err != nil {
		return err
	}
	return ErrProxyInUse
}

// 将旧版内嵌代理转换为独立记录，密文直接搬迁，避免改变原有认证信息。
func ensureLegacyProxy(ctx context.Context, tx *sql.Tx, u *Upstream) error {
	if u.ProxyID != "" || !u.ProxyEnabled {
		return nil
	}
	p := &Proxy{
		ID: crypto.NewULID(), Type: u.ProxyType, Host: u.ProxyHost, Port: u.ProxyPort,
		UsernameEncrypted: u.ProxyUsernameEncrypted, PasswordEncrypted: u.ProxyPasswordEncrypted,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
	p.Name = u.Name + " 代理 " + p.ID
	if err := insertProxy(ctx, tx, p); err != nil {
		return err
	}
	u.ProxyID, u.ProxyName = p.ID, p.Name
	return nil
}

// MigrateLegacyProxies 同时供启动升级和旧迁移包导入使用，重复执行不会生成额外记录。
func MigrateLegacyProxies(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, proxy_type, proxy_host, proxy_port,
		proxy_username_encrypted, proxy_password_encrypted, created_at, updated_at
		FROM upstreams WHERE proxy_id IS NULL AND proxy_enabled=1`)
	if err != nil {
		return err
	}
	var list []Upstream
	for rows.Next() {
		u := Upstream{ProxyEnabled: true}
		if err := rows.Scan(&u.ID, &u.Name, &u.ProxyType, &u.ProxyHost, &u.ProxyPort,
			&u.ProxyUsernameEncrypted, &u.ProxyPasswordEncrypted, &u.CreatedAt, &u.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		list = append(list, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range list {
		if err := ensureLegacyProxy(ctx, tx, &u); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE upstreams SET proxy_id=? WHERE id=?`, u.ProxyID, u.ID); err != nil {
			return err
		}
	}
	return nil
}
