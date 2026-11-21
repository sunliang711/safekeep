package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"safekeep/internal/proxy"
)

type ProxyRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Service) exportProxies(ctx context.Context) ([]ProxyRecord, error) {
	list, err := s.store.ListProxies(ctx)
	if err != nil {
		return nil, err
	}
	var records []ProxyRecord
	for _, p := range list {
		user, err := s.cipher.DecryptString(p.UsernameEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt proxy username: %w", err)
		}
		pass, err := s.cipher.DecryptString(p.PasswordEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt proxy password: %w", err)
		}
		records = append(records, ProxyRecord{ID: p.ID, Name: p.Name, Type: p.Type, Host: p.Host,
			Port: p.Port, Username: user, Password: pass, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt})
	}
	return records, nil
}

func proxyConflictID(ctx context.Context, tx *sql.Tx, column, value string) (string, error) {
	var id string
	// column 仅由本包中的固定字段传入，查询值始终通过参数绑定。
	err := tx.QueryRowContext(ctx, `SELECT id FROM proxies WHERE `+column+`=?`, value).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Service) importProxies(ctx context.Context, tx *sql.Tx, records []ProxyRecord, mode, conflict string) (map[string]string, int, error) {
	ids := make(map[string]string, len(records))
	count := 0
	for _, p := range records {
		if p.ID == "" || p.Name == "" {
			return nil, 0, fmt.Errorf("proxy id and name are required")
		}
		if err := (proxy.Config{Enabled: true, Type: p.Type, Host: p.Host, Port: p.Port}).Validate(); err != nil {
			return nil, 0, err
		}
		byID, err := proxyConflictID(ctx, tx, "id", p.ID)
		if err != nil {
			return nil, 0, err
		}
		byName, err := proxyConflictID(ctx, tx, "name", p.Name)
		if err != nil {
			return nil, 0, err
		}
		targetID := p.ID
		if byID != "" {
			targetID = byID
		} else if byName != "" {
			targetID = byName
		}
		ids[p.ID] = targetID
		if mode == "merge" && conflict == "skip" && (byID != "" || byName != "") {
			continue
		}
		user, err := s.cipher.EncryptString(p.Username)
		if err != nil {
			return nil, 0, err
		}
		pass, err := s.cipher.EncryptString(p.Password)
		if err != nil {
			return nil, 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO proxies(id, name, type, host, port,
			username_encrypted, password_encrypted, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name=excluded.name, type=excluded.type, host=excluded.host,
			port=excluded.port, username_encrypted=excluded.username_encrypted,
			password_encrypted=excluded.password_encrypted, updated_at=excluded.updated_at`,
			targetID, p.Name, p.Type, p.Host, p.Port, user, pass, p.CreatedAt, time.Now().UTC()); err != nil {
			return nil, 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE upstreams SET updated_at=?, last_test_status='untested',
			last_test_error='', last_tested_at=NULL WHERE proxy_id=?`, time.Now().UTC(), targetID); err != nil {
			return nil, 0, err
		}
		count++
	}
	return ids, count, nil
}

func (s *Service) proxyConflicts(ctx context.Context, records []ProxyRecord) ([]string, error) {
	list, err := s.store.ListProxies(ctx)
	if err != nil {
		return nil, err
	}
	ids, names := make(map[string]bool), make(map[string]bool)
	for _, p := range list {
		ids[p.ID], names[p.Name] = true, true
	}
	var conflicts []string
	for _, p := range records {
		if ids[p.ID] || names[p.Name] {
			conflicts = append(conflicts, p.Name)
		}
	}
	return conflicts, nil
}
