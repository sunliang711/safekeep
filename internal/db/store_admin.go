package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"safekeep/internal/config"
	"safekeep/internal/crypto"
	"safekeep/internal/notify"
)

var ErrNotFound = errors.New("record not found")

// DB returns the underlying *sql.DB for transactional operations such as migration.
func (s *Store) DB() *sql.DB {
	return s.db
}

// DataDir returns the configured root data directory.
func (s *Store) DataDir() string {
	return s.dataDir
}

// --- System Settings ---

func (s *Store) GetSystemSettings(ctx context.Context) (config.SystemSettings, error) {
	settings := config.DefaultSystemSettings(s.dataDir)
	rows, err := s.db.QueryContext(ctx, `SELECT key, value_json FROM system_settings`)
	if err != nil {
		return settings, err
	}
	defer rows.Close()

	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return settings, err
		}
		switch k {
		case "spool_dir":
			_ = json.Unmarshal([]byte(v), &settings.SpoolDir)
		case "max_file_size_bytes":
			_ = json.Unmarshal([]byte(v), &settings.MaxFileSizeBytes)
		case "daily_traffic_limit_bytes":
			_ = json.Unmarshal([]byte(v), &settings.DailyTrafficLimitBytes)
		case "max_spool_bytes":
			_ = json.Unmarshal([]byte(v), &settings.MaxSpoolBytes)
		case "worker_concurrency":
			_ = json.Unmarshal([]byte(v), &settings.WorkerConcurrency)
		case "sync_timeout_sec":
			_ = json.Unmarshal([]byte(v), &settings.SyncTimeoutSec)
		case "max_retry_attempts":
			_ = json.Unmarshal([]byte(v), &settings.MaxRetryAttempts)
		case "public_base_url":
			_ = json.Unmarshal([]byte(v), &settings.PublicBaseURL)
		}
	}
	return settings, rows.Err()
}

func (s *Store) UpdateSystemSettings(ctx context.Context, settings config.SystemSettings, updatedBy string) error {
	pairs := map[string]any{
		"spool_dir":                 settings.SpoolDir,
		"max_file_size_bytes":       settings.MaxFileSizeBytes,
		"daily_traffic_limit_bytes": settings.DailyTrafficLimitBytes,
		"max_spool_bytes":           settings.MaxSpoolBytes,
		"worker_concurrency":        settings.WorkerConcurrency,
		"sync_timeout_sec":          settings.SyncTimeoutSec,
		"max_retry_attempts":        settings.MaxRetryAttempts,
		"public_base_url":           settings.PublicBaseURL,
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for k, v := range pairs {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO system_settings(key, value_json, updated_at, updated_by)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json, updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
			k, string(raw), now, updatedBy,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetSMTPConfig loads SMTP alerting settings and decrypts the stored SMTP password.
func (s *Store) GetSMTPConfig(ctx context.Context, cipher *crypto.Cipher) (notify.SMTPConfig, error) {
	cfg := notify.SMTPConfig{
		Port:     587,
		TLSMode:  "starttls",
		FromName: "SafeKeep",
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value_json FROM system_settings WHERE key = 'smtp_config'`).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, err
	}
	if cfg.PasswordEncrypted != "" && cipher != nil {
		plain, err := cipher.DecryptString(cfg.PasswordEncrypted)
		if err != nil {
			return cfg, fmt.Errorf("decrypt smtp password: %w", err)
		}
		cfg.Password = plain
	}
	return cfg, nil
}

// SaveSMTPConfig encrypts the SMTP password and persists SMTP settings into system_settings.
func (s *Store) SaveSMTPConfig(ctx context.Context, cipher *crypto.Cipher, cfg notify.SMTPConfig, updatedBy string) error {
	toStore := cfg
	if cfg.Password != "" && cipher != nil {
		enc, err := cipher.EncryptString(cfg.Password)
		if err != nil {
			return err
		}
		toStore.PasswordEncrypted = enc
	} else if cfg.Password == "" && cfg.PasswordEncrypted == "" {
		toStore.PasswordEncrypted = ""
	}
	toStore.Password = ""

	raw, err := json.Marshal(toStore)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO system_settings(key, value_json, updated_at, updated_by)
		 VALUES ('smtp_config', ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json, updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
		string(raw), time.Now().UTC(), updatedBy,
	)
	return err
}

// --- Admins ---

func (s *Store) CountAdmins(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n)
	return n, err
}

func (s *Store) CreateAdmin(ctx context.Context, a *Admin) error {
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admins(id, username, password_hash, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.Username, a.PasswordHash, boolToInt(a.IsActive), a.CreatedAt, a.UpdatedAt,
	)
	return err
}

func (s *Store) GetAdminByUsername(ctx context.Context, username string) (*Admin, error) {
	return s.scanAdmin(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, is_active, last_login_at, created_at, updated_at
		 FROM admins WHERE username = ?`, username))
}

func (s *Store) GetAdminByID(ctx context.Context, id string) (*Admin, error) {
	return s.scanAdmin(s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, is_active, last_login_at, created_at, updated_at
		 FROM admins WHERE id = ?`, id))
}

func (s *Store) ListAdmins(ctx context.Context) ([]Admin, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, is_active, last_login_at, created_at, updated_at
		 FROM admins ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Admin
	for rows.Next() {
		a, err := s.scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *a)
	}
	return list, rows.Err()
}

var ErrAlreadyInitialized = errors.New("system is already initialized")

func (s *Store) CreateInitialAdminIfEmpty(ctx context.Context, a *Admin) error {
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var n int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrAlreadyInitialized
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO admins(id, username, password_hash, is_active, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.Username, a.PasswordHash, boolToInt(a.IsActive), a.CreatedAt, a.UpdatedAt,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateAdminLogin(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE admins SET last_login_at = ?, updated_at = ? WHERE id = ?`, now, now, id)
	return err
}

func (s *Store) UpdateAdminPassword(ctx context.Context, id, passwordHash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE admins SET password_hash = ?, updated_at = ? WHERE id = ?`, passwordHash, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateAdminActive(ctx context.Context, id string, active bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE admins SET is_active = ?, updated_at = ? WHERE id = ?`, boolToInt(active), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanAdmin(row rowScanner) (*Admin, error) {
	var a Admin
	var active int
	var lastLogin sql.NullTime
	if err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &active, &lastLogin, &a.CreatedAt, &a.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.IsActive = active == 1
	if lastLogin.Valid {
		t := lastLogin.Time
		a.LastLoginAt = &t
	}
	return &a, nil
}

// --- Upstreams ---

func (s *Store) CreateUpstream(ctx context.Context, u *Upstream) error {
	now := time.Now().UTC()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	u.UpdatedAt = now
	if u.LastTestStatus == "" {
		u.LastTestStatus = "untested"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO upstreams(
			id, name, type, enabled, config_encrypted,
			proxy_enabled, proxy_type, proxy_host, proxy_port,
			proxy_username_encrypted, proxy_password_encrypted,
			last_test_status, last_test_error, last_tested_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Name, u.Type, boolToInt(u.Enabled), u.ConfigEncrypted,
		boolToInt(u.ProxyEnabled), u.ProxyType, u.ProxyHost, u.ProxyPort,
		u.ProxyUsernameEncrypted, u.ProxyPasswordEncrypted,
		u.LastTestStatus, u.LastTestError, u.LastTestedAt, u.CreatedAt, u.UpdatedAt,
	)
	return err
}

func (s *Store) UpdateUpstream(ctx context.Context, u *Upstream) error {
	u.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE upstreams SET
			name = ?, type = ?, enabled = ?, config_encrypted = ?,
			proxy_enabled = ?, proxy_type = ?, proxy_host = ?, proxy_port = ?,
			proxy_username_encrypted = ?, proxy_password_encrypted = ?,
			updated_at = ?
		 WHERE id = ?`,
		u.Name, u.Type, boolToInt(u.Enabled), u.ConfigEncrypted,
		boolToInt(u.ProxyEnabled), u.ProxyType, u.ProxyHost, u.ProxyPort,
		u.ProxyUsernameEncrypted, u.ProxyPasswordEncrypted,
		u.UpdatedAt, u.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUpstreamConfigEncrypted persists refreshed OAuth tokens without bumping updated_at
// so cached provider HTTP transports remain active and do not churn on token refresh.
func (s *Store) UpdateUpstreamConfigEncrypted(ctx context.Context, id, configEncrypted string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE upstreams SET config_encrypted = ? WHERE id = ?`,
		configEncrypted, id,
	)
	return err
}

func (s *Store) UpdateUpstreamTestStatus(ctx context.Context, id, status, errMsg string) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`UPDATE upstreams SET last_test_status = ?, last_test_error = ?, last_tested_at = ?, updated_at = ? WHERE id = ?`,
		status, errMsg, now, now, id,
	)
	return err
}

func (s *Store) DeleteUpstream(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM upstreams WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetUpstreamByID(ctx context.Context, id string) (*Upstream, error) {
	return s.scanUpstream(s.db.QueryRowContext(ctx,
		`SELECT id, name, type, enabled, config_encrypted,
			proxy_enabled, proxy_type, proxy_host, proxy_port,
			proxy_username_encrypted, proxy_password_encrypted,
			last_test_status, last_test_error, last_tested_at, created_at, updated_at
		 FROM upstreams WHERE id = ?`, id))
}

func (s *Store) ListUpstreams(ctx context.Context) ([]Upstream, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, type, enabled, config_encrypted,
			proxy_enabled, proxy_type, proxy_host, proxy_port,
			proxy_username_encrypted, proxy_password_encrypted,
			last_test_status, last_test_error, last_tested_at, created_at, updated_at
		 FROM upstreams ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Upstream
	for rows.Next() {
		u, err := s.scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *u)
	}
	return list, rows.Err()
}

func (s *Store) scanUpstream(row rowScanner) (*Upstream, error) {
	var u Upstream
	var enabled, proxyEnabled int
	var testedAt sql.NullTime
	if err := row.Scan(
		&u.ID, &u.Name, &u.Type, &enabled, &u.ConfigEncrypted,
		&proxyEnabled, &u.ProxyType, &u.ProxyHost, &u.ProxyPort,
		&u.ProxyUsernameEncrypted, &u.ProxyPasswordEncrypted,
		&u.LastTestStatus, &u.LastTestError, &testedAt, &u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Enabled = enabled == 1
	u.ProxyEnabled = proxyEnabled == 1
	if testedAt.Valid {
		t := testedAt.Time
		u.LastTestedAt = &t
	}
	return &u, nil
}

// --- Clients & ClientUpstreams ---

func (s *Store) CreateClient(ctx context.Context, c *Client, upstreams []ClientUpstream) error {
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.AllowedMimeTypesJSON == "" {
		c.AllowedMimeTypesJSON = "[]"
	}
	if c.DefaultMode == "" {
		c.DefaultMode = "async"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO clients(
			id, name, description, key_prefix, key_hash, enabled,
			default_mode, path_prefix, max_file_size_bytes, allowed_mime_types_json,
			rate_limit_rpm, webhook_url, webhook_secret_encrypted, expires_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Description, c.KeyPrefix, c.KeyHash, boolToInt(c.Enabled),
		c.DefaultMode, c.PathPrefix, c.MaxFileSizeBytes, c.AllowedMimeTypesJSON,
		c.RateLimitRPM, c.WebhookURL, c.WebhookSecretEncrypted, c.ExpiresAt, c.CreatedAt, c.UpdatedAt,
	); err != nil {
		return err
	}

	for _, cu := range upstreams {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO client_upstreams(client_id, upstream_id, is_default, priority) VALUES (?, ?, ?, ?)`,
			c.ID, cu.UpstreamID, boolToInt(cu.IsDefault), cu.Priority,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpdateClient(ctx context.Context, c *Client) error {
	c.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE clients SET
			name = ?, description = ?, enabled = ?, default_mode = ?,
			path_prefix = ?, max_file_size_bytes = ?, allowed_mime_types_json = ?,
			rate_limit_rpm = ?, webhook_url = ?, webhook_secret_encrypted = ?,
			expires_at = ?, updated_at = ?
		 WHERE id = ?`,
		c.Name, c.Description, boolToInt(c.Enabled), c.DefaultMode,
		c.PathPrefix, c.MaxFileSizeBytes, c.AllowedMimeTypesJSON,
		c.RateLimitRPM, c.WebhookURL, c.WebhookSecretEncrypted,
		c.ExpiresAt, c.UpdatedAt, c.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RotateClientKey(ctx context.Context, id, prefix, hash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE clients SET key_prefix = ?, key_hash = ?, updated_at = ? WHERE id = ?`,
		prefix, hash, time.Now().UTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) TouchClientLastUsed(ctx context.Context, id string) {
	_, _ = s.db.ExecContext(ctx, `UPDATE clients SET last_used_at = ? WHERE id = ?`, time.Now().UTC(), id)
}

func (s *Store) SetClientUpstreams(ctx context.Context, clientID string, upstreams []ClientUpstream) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients WHERE id = ?`, clientID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM client_upstreams WHERE client_id = ?`, clientID); err != nil {
		return err
	}
	for _, cu := range upstreams {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO client_upstreams(client_id, upstream_id, is_default, priority) VALUES (?, ?, ?, ?)`,
			clientID, cu.UpstreamID, boolToInt(cu.IsDefault), cu.Priority,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteClient(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetClientByID(ctx context.Context, id string) (*Client, error) {
	return s.scanClient(s.db.QueryRowContext(ctx,
		`SELECT id, name, description, key_prefix, key_hash, enabled, default_mode,
			path_prefix, max_file_size_bytes, allowed_mime_types_json, rate_limit_rpm,
			webhook_url, webhook_secret_encrypted, expires_at, last_used_at, created_at, updated_at
		 FROM clients WHERE id = ?`, id))
}

func (s *Store) GetClientByKeyHash(ctx context.Context, keyHash string) (*Client, error) {
	return s.scanClient(s.db.QueryRowContext(ctx,
		`SELECT id, name, description, key_prefix, key_hash, enabled, default_mode,
			path_prefix, max_file_size_bytes, allowed_mime_types_json, rate_limit_rpm,
			webhook_url, webhook_secret_encrypted, expires_at, last_used_at, created_at, updated_at
		 FROM clients WHERE key_hash = ?`, keyHash))
}

func (s *Store) ListClients(ctx context.Context) ([]Client, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, key_prefix, key_hash, enabled, default_mode,
			path_prefix, max_file_size_bytes, allowed_mime_types_json, rate_limit_rpm,
			webhook_url, webhook_secret_encrypted, expires_at, last_used_at, created_at, updated_at
		 FROM clients ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Client
	for rows.Next() {
		c, err := s.scanClient(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *c)
	}
	return list, rows.Err()
}

func (s *Store) GetClientUpstreams(ctx context.Context, clientID string) ([]ClientUpstream, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT cu.client_id, cu.upstream_id, u.name, u.type, u.enabled, cu.is_default, cu.priority
		 FROM client_upstreams cu
		 JOIN upstreams u ON u.id = cu.upstream_id
		 WHERE cu.client_id = ?
		 ORDER BY cu.priority ASC, u.name ASC`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []ClientUpstream
	for rows.Next() {
		var cu ClientUpstream
		var uEnabled, isDefault int
		if err := rows.Scan(&cu.ClientID, &cu.UpstreamID, &cu.UpstreamName, &cu.UpstreamType, &uEnabled, &isDefault, &cu.Priority); err != nil {
			return nil, err
		}
		cu.Enabled = uEnabled == 1
		cu.IsDefault = isDefault == 1
		list = append(list, cu)
	}
	return list, rows.Err()
}

func (s *Store) scanClient(row rowScanner) (*Client, error) {
	var c Client
	var enabled int
	var expiresAt, lastUsedAt sql.NullTime
	if err := row.Scan(
		&c.ID, &c.Name, &c.Description, &c.KeyPrefix, &c.KeyHash, &enabled, &c.DefaultMode,
		&c.PathPrefix, &c.MaxFileSizeBytes, &c.AllowedMimeTypesJSON, &c.RateLimitRPM,
		&c.WebhookURL, &c.WebhookSecretEncrypted, &expiresAt, &lastUsedAt, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Enabled = enabled == 1
	if expiresAt.Valid {
		t := expiresAt.Time
		c.ExpiresAt = &t
	}
	if lastUsedAt.Valid {
		t := lastUsedAt.Time
		c.LastUsedAt = &t
	}
	return &c, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListAllClientUpstreams returns every client_upstream mapping (for migration export).
func (s *Store) ListAllClientUpstreams(ctx context.Context) ([]ClientUpstream, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT client_id, upstream_id, is_default, priority FROM client_upstreams ORDER BY client_id, priority`)
	if err != nil {
		return nil, fmt.Errorf("list client upstreams: %w", err)
	}
	defer rows.Close()
	var list []ClientUpstream
	for rows.Next() {
		var cu ClientUpstream
		var isDefault int
		if err := rows.Scan(&cu.ClientID, &cu.UpstreamID, &isDefault, &cu.Priority); err != nil {
			return nil, err
		}
		cu.IsDefault = isDefault == 1
		list = append(list, cu)
	}
	return list, rows.Err()
}
