package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"safekeep/internal/crypto"
	"safekeep/internal/db"
)

const FormatVersion = 1

// ExportOptions controls which categories of data are included in the migration bundle.
type ExportOptions struct {
	IncludeTasks     bool   `json:"include_tasks"`
	IncludeAdmins    bool   `json:"include_admins"`
	IncludeAuditLogs bool   `json:"include_audit_logs"`
	Passphrase       string `json:"passphrase"`
}

// ImportOptions controls how the bundle is applied to the target environment.
type ImportOptions struct {
	Passphrase       string `json:"passphrase"`
	Mode             string `json:"mode"`              // replace | merge
	ConflictStrategy string `json:"conflict_strategy"` // skip | overwrite (for merge mode)
	IncludeTasks     bool   `json:"include_tasks"`
	IncludeAdmins    bool   `json:"include_admins"`
	IncludeAuditLogs bool   `json:"include_audit_logs"`
}

type Bundle struct {
	FormatVersion int           `json:"format_version"`
	ExportedAt    time.Time     `json:"exported_at"`
	Includes      BundleInclude `json:"includes"`
	Checksum      string        `json:"checksum_sha256"`
	Data          BundleData    `json:"data"`
}

type BundleInclude struct {
	Tasks     bool `json:"tasks"`
	Admins    bool `json:"admins"`
	AuditLogs bool `json:"audit_logs"`
}

type BundleData struct {
	SystemSettings  []SettingRecord     `json:"system_settings"`
	Upstreams       []UpstreamRecord    `json:"upstreams"`
	Clients         []ClientRecord      `json:"clients"`
	ClientUpstreams []db.ClientUpstream `json:"client_upstreams"`
	CronJobs        []db.CronJob        `json:"cron_jobs"`
	Admins          []AdminRecord       `json:"admins,omitempty"`
	BackupTasks     []db.BackupTask     `json:"backup_tasks,omitempty"`
	BackupItems     []db.BackupTaskItem `json:"backup_task_items,omitempty"`
	AuditLogs       []db.AuditLog       `json:"audit_logs,omitempty"`
}

type SettingRecord struct {
	Key       string    `json:"key"`
	ValueJSON string    `json:"value_json"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

type UpstreamRecord struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Type            string     `json:"type"`
	Enabled         bool       `json:"enabled"`
	ConfigPlainJSON string     `json:"config_plain_json"`
	ProxyEnabled    bool       `json:"proxy_enabled"`
	ProxyType       string     `json:"proxy_type"`
	ProxyHost       string     `json:"proxy_host"`
	ProxyPort       int        `json:"proxy_port"`
	ProxyUsername   string     `json:"proxy_username"`
	ProxyPassword   string     `json:"proxy_password"`
	LastTestStatus  string     `json:"last_test_status"`
	LastTestError   string     `json:"last_test_error"`
	LastTestedAt    *time.Time `json:"last_tested_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type ClientRecord struct {
	ID                   string     `json:"id"`
	Name                 string     `json:"name"`
	Description          string     `json:"description"`
	KeyPrefix            string     `json:"key_prefix"`
	KeyHash              string     `json:"key_hash"`
	Enabled              bool       `json:"enabled"`
	DefaultMode          string     `json:"default_mode"`
	PathPrefix           string     `json:"path_prefix"`
	MaxFileSizeBytes     int64      `json:"max_file_size_bytes"`
	AllowedMimeTypesJSON string     `json:"allowed_mime_types_json"`
	RateLimitRPM         int        `json:"rate_limit_rpm"`
	WebhookURL           string     `json:"webhook_url"`
	WebhookSecretPlain   string     `json:"webhook_secret_plain"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	LastUsedAt           *time.Time `json:"last_used_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type AdminRecord struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"password_hash"`
	IsActive     bool       `json:"is_active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// InspectSummary describes the contents and potential conflicts of an uploaded bundle.
type InspectSummary struct {
	FormatVersion      int           `json:"format_version"`
	ExportedAt         time.Time     `json:"exported_at"`
	Includes           BundleInclude `json:"includes"`
	Counts             BundleCounts  `json:"counts"`
	UpstreamConflicts  []string      `json:"upstream_conflicts"`
	ClientConflicts    []string      `json:"client_conflicts"`
	AdminConflicts     []string      `json:"admin_conflicts"`
}

type BundleCounts struct {
	SystemSettings  int `json:"system_settings"`
	Upstreams       int `json:"upstreams"`
	Clients         int `json:"clients"`
	ClientUpstreams int `json:"client_upstreams"`
	CronJobs        int `json:"cron_jobs"`
	Admins          int `json:"admins"`
	BackupTasks     int `json:"backup_tasks"`
	AuditLogs       int `json:"audit_logs"`
}

type Service struct {
	store  *db.Store
	cipher *crypto.Cipher
}

func NewService(store *db.Store, cipher *crypto.Cipher) *Service {
	return &Service{store: store, cipher: cipher}
}

// Export decrypts local secrets, builds a portable Bundle, and encrypts it with opts.Passphrase.
func (s *Service) Export(ctx context.Context, opts ExportOptions) ([]byte, error) {
	if len(opts.Passphrase) < 6 {
		return nil, fmt.Errorf("migration passphrase must be at least 6 characters")
	}

	var data BundleData

	// 1. System settings
	rows, err := s.store.DB().QueryContext(ctx, `SELECT key, value_json, updated_at, updated_by FROM system_settings`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r SettingRecord
		if err := rows.Scan(&r.Key, &r.ValueJSON, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			rows.Close()
			return nil, err
		}
		data.SystemSettings = append(data.SystemSettings, r)
	}
	rows.Close()

	// 2. Upstreams
	ups, err := s.store.ListUpstreams(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range ups {
		cfgPlain, err := s.cipher.DecryptString(u.ConfigEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt upstream %s config: %w", u.Name, err)
		}
		pUser, err := s.cipher.DecryptString(u.ProxyUsernameEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt upstream %s proxy username: %w", u.Name, err)
		}
		pPass, err := s.cipher.DecryptString(u.ProxyPasswordEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt upstream %s proxy password: %w", u.Name, err)
		}
		data.Upstreams = append(data.Upstreams, UpstreamRecord{
			ID:              u.ID,
			Name:            u.Name,
			Type:            u.Type,
			Enabled:         u.Enabled,
			ConfigPlainJSON: cfgPlain,
			ProxyEnabled:    u.ProxyEnabled,
			ProxyType:       u.ProxyType,
			ProxyHost:       u.ProxyHost,
			ProxyPort:       u.ProxyPort,
			ProxyUsername:   pUser,
			ProxyPassword:   pPass,
			LastTestStatus:  u.LastTestStatus,
			LastTestError:   u.LastTestError,
			LastTestedAt:    u.LastTestedAt,
			CreatedAt:       u.CreatedAt,
			UpdatedAt:       u.UpdatedAt,
		})
	}

	// 3. Clients & ClientUpstreams
	clients, err := s.store.ListClients(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range clients {
		whSecret, err := s.cipher.DecryptString(c.WebhookSecretEncrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt client %s webhook secret: %w", c.Name, err)
		}
		data.Clients = append(data.Clients, ClientRecord{
			ID:                   c.ID,
			Name:                 c.Name,
			Description:          c.Description,
			KeyPrefix:            c.KeyPrefix,
			KeyHash:              c.KeyHash,
			Enabled:              c.Enabled,
			DefaultMode:          c.DefaultMode,
			PathPrefix:           c.PathPrefix,
			MaxFileSizeBytes:     c.MaxFileSizeBytes,
			AllowedMimeTypesJSON: c.AllowedMimeTypesJSON,
			RateLimitRPM:         c.RateLimitRPM,
			WebhookURL:           c.WebhookURL,
			WebhookSecretPlain:   whSecret,
			ExpiresAt:            c.ExpiresAt,
			LastUsedAt:           c.LastUsedAt,
			CreatedAt:            c.CreatedAt,
			UpdatedAt:            c.UpdatedAt,
		})
	}
	data.ClientUpstreams, err = s.store.ListAllClientUpstreams(ctx)
	if err != nil {
		return nil, err
	}

	// 4. Cron Jobs
	data.CronJobs, err = s.store.ListCronJobs(ctx)
	if err != nil {
		return nil, err
	}

	// 5. Optional Admins
	if opts.IncludeAdmins {
		admins, err := s.store.ListAdmins(ctx)
		if err != nil {
			return nil, err
		}
		for _, a := range admins {
			data.Admins = append(data.Admins, AdminRecord{
				ID:           a.ID,
				Username:     a.Username,
				PasswordHash: a.PasswordHash,
				IsActive:     a.IsActive,
				LastLoginAt:  a.LastLoginAt,
				CreatedAt:    a.CreatedAt,
				UpdatedAt:    a.UpdatedAt,
			})
		}
	}

	// 6. Optional Backup Tasks & Items
	if opts.IncludeTasks {
		tasks, _, err := s.store.ListBackupTasks(ctx, db.TaskFilter{Limit: 100000})
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			items := t.Items
			t.Items = nil
			data.BackupTasks = append(data.BackupTasks, t)
			data.BackupItems = append(data.BackupItems, items...)
		}
	}

	// 7. Optional Audit Logs
	if opts.IncludeAuditLogs {
		logs, _, err := s.store.ListAuditLogs(ctx, db.AuditFilter{Limit: 100000})
		if err != nil {
			return nil, err
		}
		data.AuditLogs = logs
	}

	dataBytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(dataBytes)

	bundle := Bundle{
		FormatVersion: FormatVersion,
		ExportedAt:    time.Now().UTC(),
		Includes: BundleInclude{
			Tasks:     opts.IncludeTasks,
			Admins:    opts.IncludeAdmins,
			AuditLogs: opts.IncludeAuditLogs,
		},
		Checksum: hex.EncodeToString(sum[:]),
		Data:     data,
	}

	rawBundle, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	return crypto.EncryptWithPassphrase(rawBundle, opts.Passphrase)
}

func decodeBundle(encrypted []byte, passphrase string) (*Bundle, error) {
	raw, err := crypto.DecryptWithPassphrase(encrypted, passphrase)
	if err != nil {
		return nil, err
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("parse bundle json: %w", err)
	}
	if b.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("unsupported migration format_version %d", b.FormatVersion)
	}
	dataBytes, err := json.Marshal(b.Data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(dataBytes)
	if b.Checksum != "" && hex.EncodeToString(sum[:]) != b.Checksum {
		return nil, fmt.Errorf("bundle checksum verification failed")
	}
	return &b, nil
}

// Inspect decrypts a bundle without modifying the database and reports counts & conflicts.
func (s *Service) Inspect(ctx context.Context, encrypted []byte, passphrase string) (*InspectSummary, error) {
	b, err := decodeBundle(encrypted, passphrase)
	if err != nil {
		return nil, err
	}

	summary := &InspectSummary{
		FormatVersion: b.FormatVersion,
		ExportedAt:    b.ExportedAt,
		Includes:      b.Includes,
		Counts: BundleCounts{
			SystemSettings:  len(b.Data.SystemSettings),
			Upstreams:       len(b.Data.Upstreams),
			Clients:         len(b.Data.Clients),
			ClientUpstreams: len(b.Data.ClientUpstreams),
			CronJobs:        len(b.Data.CronJobs),
			Admins:          len(b.Data.Admins),
			BackupTasks:     len(b.Data.BackupTasks),
			AuditLogs:       len(b.Data.AuditLogs),
		},
	}

	existingUps, _ := s.store.ListUpstreams(ctx)
	upNames := make(map[string]bool)
	for _, u := range existingUps {
		upNames[u.ID] = true
		upNames[u.Name] = true
	}
	for _, u := range b.Data.Upstreams {
		if upNames[u.ID] || upNames[u.Name] {
			summary.UpstreamConflicts = append(summary.UpstreamConflicts, u.Name)
		}
	}

	existingClients, _ := s.store.ListClients(ctx)
	clNames := make(map[string]bool)
	for _, c := range existingClients {
		clNames[c.ID] = true
		clNames[c.Name] = true
	}
	for _, c := range b.Data.Clients {
		if clNames[c.ID] || clNames[c.Name] {
			summary.ClientConflicts = append(summary.ClientConflicts, c.Name)
		}
	}

	existingAdmins, _ := s.store.ListAdmins(ctx)
	adNames := make(map[string]bool)
	for _, a := range existingAdmins {
		adNames[a.ID] = true
		adNames[a.Username] = true
	}
	for _, a := range b.Data.Admins {
		if adNames[a.ID] || adNames[a.Username] {
			summary.AdminConflicts = append(summary.AdminConflicts, a.Username)
		}
	}

	return summary, nil
}

// Import applies a migration bundle atomically inside a SQLite transaction,
// re-encrypting all secrets using the target environment's master key.
func (s *Service) Import(ctx context.Context, encrypted []byte, opts ImportOptions) (*BundleCounts, error) {
	b, err := decodeBundle(encrypted, opts.Passphrase)
	if err != nil {
		return nil, err
	}

	mode := opts.Mode
	if mode == "" {
		mode = "merge"
	}
	if mode != "replace" && mode != "merge" {
		return nil, fmt.Errorf("invalid import mode %q (must be replace or merge)", mode)
	}
	conflict := opts.ConflictStrategy
	if conflict == "" {
		conflict = "skip"
	}
	if conflict != "skip" && conflict != "overwrite" {
		return nil, fmt.Errorf("invalid conflict_strategy %q (must be skip or overwrite)", conflict)
	}

	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if mode == "replace" {
		if opts.IncludeTasks && b.Includes.Tasks {
			if _, err := tx.ExecContext(ctx, `DELETE FROM backup_task_items`); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM backup_tasks`); err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM client_upstreams`); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM clients`); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM upstreams`); err != nil {
			return nil, err
		}
		if opts.IncludeAuditLogs && b.Includes.AuditLogs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM audit_logs`); err != nil {
				return nil, err
			}
		}
	}

	counts := &BundleCounts{}

	// 1. System Settings
	for _, st := range b.Data.SystemSettings {
		if mode == "merge" && conflict == "skip" {
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO system_settings(key, value_json, updated_at, updated_by) VALUES (?, ?, ?, ?)`,
				st.Key, st.ValueJSON, st.UpdatedAt, st.UpdatedBy,
			); err != nil {
				return nil, err
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO system_settings(key, value_json, updated_at, updated_by) VALUES (?, ?, ?, ?)
				 ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json, updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
				st.Key, st.ValueJSON, st.UpdatedAt, st.UpdatedBy,
			); err != nil {
				return nil, err
			}
		}
		counts.SystemSettings++
	}

	// 2. Upstreams (re-encrypt with target master key)
	for _, u := range b.Data.Upstreams {
		cfgEnc, err := s.cipher.EncryptString(u.ConfigPlainJSON)
		if err != nil {
			return nil, err
		}
		pUserEnc, err := s.cipher.EncryptString(u.ProxyUsername)
		if err != nil {
			return nil, err
		}
		pPassEnc, err := s.cipher.EncryptString(u.ProxyPassword)
		if err != nil {
			return nil, err
		}

		if mode == "merge" && conflict == "skip" {
			var exists int
			_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM upstreams WHERE id = ? OR name = ?`, u.ID, u.Name).Scan(&exists)
			if exists > 0 {
				continue
			}
		}
		if mode == "merge" && conflict == "overwrite" {
			_, _ = tx.ExecContext(ctx, `DELETE FROM upstreams WHERE name = ? AND id != ?`, u.Name, u.ID)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO upstreams(
				id, name, type, enabled, config_encrypted,
				proxy_enabled, proxy_type, proxy_host, proxy_port,
				proxy_username_encrypted, proxy_password_encrypted,
				last_test_status, last_test_error, last_tested_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name=excluded.name, type=excluded.type, enabled=excluded.enabled,
				config_encrypted=excluded.config_encrypted,
				proxy_enabled=excluded.proxy_enabled, proxy_type=excluded.proxy_type,
				proxy_host=excluded.proxy_host, proxy_port=excluded.proxy_port,
				proxy_username_encrypted=excluded.proxy_username_encrypted,
				proxy_password_encrypted=excluded.proxy_password_encrypted,
				updated_at=excluded.updated_at`,
			u.ID, u.Name, u.Type, boolInt(u.Enabled), cfgEnc,
			boolInt(u.ProxyEnabled), u.ProxyType, u.ProxyHost, u.ProxyPort,
			pUserEnc, pPassEnc,
			u.LastTestStatus, u.LastTestError, u.LastTestedAt, u.CreatedAt, u.UpdatedAt,
		); err != nil {
			return nil, err
		}
		counts.Upstreams++
	}

	// 3. Clients (re-encrypt webhook secret, keep key_hash intact)
	for _, c := range b.Data.Clients {
		whEnc, err := s.cipher.EncryptString(c.WebhookSecretPlain)
		if err != nil {
			return nil, err
		}
		if mode == "merge" && conflict == "skip" {
			var exists int
			_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients WHERE id = ? OR name = ? OR key_hash = ?`, c.ID, c.Name, c.KeyHash).Scan(&exists)
			if exists > 0 {
				continue
			}
		}
		if mode == "merge" && conflict == "overwrite" {
			_, _ = tx.ExecContext(ctx, `DELETE FROM clients WHERE (name = ? OR key_hash = ?) AND id != ?`, c.Name, c.KeyHash, c.ID)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO clients(
				id, name, description, key_prefix, key_hash, enabled, default_mode,
				path_prefix, max_file_size_bytes, allowed_mime_types_json, rate_limit_rpm,
				webhook_url, webhook_secret_encrypted, expires_at, last_used_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name=excluded.name, description=excluded.description, key_prefix=excluded.key_prefix,
				key_hash=excluded.key_hash, enabled=excluded.enabled, default_mode=excluded.default_mode,
				path_prefix=excluded.path_prefix, max_file_size_bytes=excluded.max_file_size_bytes,
				allowed_mime_types_json=excluded.allowed_mime_types_json, rate_limit_rpm=excluded.rate_limit_rpm,
				webhook_url=excluded.webhook_url, webhook_secret_encrypted=excluded.webhook_secret_encrypted,
				expires_at=excluded.expires_at, updated_at=excluded.updated_at`,
			c.ID, c.Name, c.Description, c.KeyPrefix, c.KeyHash, boolInt(c.Enabled), c.DefaultMode,
			c.PathPrefix, c.MaxFileSizeBytes, c.AllowedMimeTypesJSON, c.RateLimitRPM,
			c.WebhookURL, whEnc, c.ExpiresAt, c.LastUsedAt, c.CreatedAt, c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		counts.Clients++
	}

	// 4. ClientUpstreams
	for _, cu := range b.Data.ClientUpstreams {
		var cExists, uExists int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients WHERE id = ?`, cu.ClientID).Scan(&cExists)
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM upstreams WHERE id = ?`, cu.UpstreamID).Scan(&uExists)
		if cExists == 0 || uExists == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO client_upstreams(client_id, upstream_id, is_default, priority)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(client_id, upstream_id) DO UPDATE SET is_default=excluded.is_default, priority=excluded.priority`,
			cu.ClientID, cu.UpstreamID, boolInt(cu.IsDefault), cu.Priority,
		); err != nil {
			return nil, err
		}
		counts.ClientUpstreams++
	}

	// 5. CronJobs (update config by job_key)
	for _, j := range b.Data.CronJobs {
		if mode == "merge" && conflict == "skip" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE cron_jobs SET cron_expr = ?, enabled = ?, config_json = ?, updated_at = ? WHERE job_key = ?`,
			j.CronExpr, boolInt(j.Enabled), j.ConfigJSON, time.Now().UTC(), j.JobKey,
		); err != nil {
			return nil, err
		}
		counts.CronJobs++
	}

	// 6. Optional Admins
	if opts.IncludeAdmins && b.Includes.Admins {
		for _, a := range b.Data.Admins {
			if mode == "merge" && conflict == "skip" {
				var exists int
				_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins WHERE id = ? OR username = ?`, a.ID, a.Username).Scan(&exists)
				if exists > 0 {
					continue
				}
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO admins(id, username, password_hash, is_active, last_login_at, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET username=excluded.username, password_hash=excluded.password_hash, is_active=excluded.is_active, updated_at=excluded.updated_at`,
				a.ID, a.Username, a.PasswordHash, boolInt(a.IsActive), a.LastLoginAt, a.CreatedAt, a.UpdatedAt,
			); err != nil {
				return nil, err
			}
			counts.Admins++
		}
	}

	// 7. Optional Backup Tasks & Items
	if opts.IncludeTasks && b.Includes.Tasks {
		for _, t := range b.Data.BackupTasks {
			var cExists int
			_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM clients WHERE id = ?`, t.ClientID).Scan(&cExists)
			if cExists == 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO backup_tasks(
					id, client_id, idempotency_key, mode, status,
					original_filename, content_type, file_size, sha256,
					spool_path, spool_cleaned, custom_subpath, metadata_json,
					deduplicated, webhook_status, error_summary,
					created_at, started_at, finished_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
				t.ID, t.ClientID, t.IdempotencyKey, t.Mode, t.Status,
				t.OriginalFilename, t.ContentType, t.FileSize, t.SHA256,
				t.CustomSubpath, t.MetadataJSON,
				boolInt(t.Deduplicated), t.WebhookStatus, t.ErrorSummary,
				t.CreatedAt, t.StartedAt, t.FinishedAt,
			); err != nil {
				return nil, err
			}
			counts.BackupTasks++
		}
		for _, it := range b.Data.BackupItems {
			var tExists int
			_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_tasks WHERE id = ?`, it.TaskID).Scan(&tExists)
			if tExists == 0 {
				continue
			}
			_, _ = tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO backup_task_items(
					id, task_id, upstream_id, upstream_name, upstream_type,
					remote_path, remote_etag, remote_version_id, status,
					attempt_count, max_attempts, next_retry_at, duration_ms,
					error_message, started_at, finished_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				it.ID, it.TaskID, it.UpstreamID, it.UpstreamName, it.UpstreamType,
				it.RemotePath, it.RemoteETag, it.RemoteVersionID, it.Status,
				it.AttemptCount, it.MaxAttempts, it.NextRetryAt, it.DurationMs,
				it.ErrorMessage, it.StartedAt, it.FinishedAt,
			)
		}
	}

	// 8. Optional Audit Logs
	if opts.IncludeAuditLogs && b.Includes.AuditLogs {
		for _, l := range b.Data.AuditLogs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO audit_logs(
					actor_type, actor_id, actor_name, action, resource_type, resource_id,
					status, ip_address, user_agent, request_id, detail_json, created_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				l.ActorType, l.ActorID, l.ActorName, l.Action, l.ResourceType, l.ResourceID,
				l.Status, l.IPAddress, l.UserAgent, l.RequestID, l.DetailJSON, l.CreatedAt,
			); err != nil {
				return nil, err
			}
			counts.AuditLogs++
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
