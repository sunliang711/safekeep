package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"safekeep/internal/config"
	"safekeep/internal/crypto"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database connection and provides repository methods.
type Store struct {
	db      *sql.DB
	dataDir string
}

// Open opens (or creates) the SQLite database in WAL mode, runs migrations, and seeds defaults.
func Open(dbPath, dataDir string) (*Store, error) {
	return OpenWithDefaults(dbPath, dataDir, config.DefaultSystemSettings(dataDir))
}

// OpenWithDefaults opens (or creates) the SQLite database and seeds system_settings with the provided defaults.
func OpenWithDefaults(dbPath, dataDir string, defaults config.SystemSettings) (*Store, error) {
	sqldb, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite handles concurrent readers well in WAL mode, while serializing writers
	// through a single connection prevents SQLITE_BUSY write contention errors.
	sqldb.SetMaxOpenConns(1)
	sqldb.SetMaxIdleConns(1)
	sqldb.SetConnMaxLifetime(0)

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA foreign_keys=ON;",
		"PRAGMA busy_timeout=5000;",
	}
	for _, p := range pragmas {
		if _, err := sqldb.Exec(p); err != nil {
			_ = sqldb.Close()
			return nil, fmt.Errorf("apply pragma %q: %w", p, err)
		}
	}

	s := &Store{db: sqldb, dataDir: dataDir}
	if err := s.migrate(context.Background()); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := s.seedDefaults(context.Background(), defaults); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("seed defaults: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS admins (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 1,
			last_login_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS system_settings (
			key TEXT PRIMARY KEY,
			value_json TEXT NOT NULL,
			updated_at DATETIME NOT NULL,
			updated_by TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS proxies (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			type TEXT NOT NULL,
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			username_encrypted TEXT NOT NULL DEFAULT '',
			password_encrypted TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS upstreams (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			type TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			config_encrypted TEXT NOT NULL,
			proxy_enabled INTEGER NOT NULL DEFAULT 0,
			proxy_type TEXT NOT NULL DEFAULT '',
			proxy_host TEXT NOT NULL DEFAULT '',
			proxy_port INTEGER NOT NULL DEFAULT 0,
			proxy_username_encrypted TEXT NOT NULL DEFAULT '',
			proxy_password_encrypted TEXT NOT NULL DEFAULT '',
			last_test_status TEXT NOT NULL DEFAULT 'untested',
			last_test_error TEXT NOT NULL DEFAULT '',
			last_tested_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS clients (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			key_prefix TEXT NOT NULL,
			key_hash TEXT NOT NULL UNIQUE,
			key_encrypted TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			default_mode TEXT NOT NULL DEFAULT 'async',
			path_prefix TEXT NOT NULL DEFAULT '',
			max_file_size_bytes INTEGER NOT NULL DEFAULT 0,
			allowed_mime_types_json TEXT NOT NULL DEFAULT '[]',
			rate_limit_rpm INTEGER NOT NULL DEFAULT 0,
			webhook_url TEXT NOT NULL DEFAULT '',
			webhook_secret_encrypted TEXT NOT NULL DEFAULT '',
			expires_at DATETIME,
			last_used_at DATETIME,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS client_upstreams (
			client_id TEXT NOT NULL,
			upstream_id TEXT NOT NULL,
			is_default INTEGER NOT NULL DEFAULT 1,
			priority INTEGER NOT NULL DEFAULT 100,
			PRIMARY KEY (client_id, upstream_id),
			FOREIGN KEY (client_id) REFERENCES clients(id) ON DELETE CASCADE,
			FOREIGN KEY (upstream_id) REFERENCES upstreams(id) ON DELETE CASCADE
		);`,
		`CREATE TABLE IF NOT EXISTS backup_tasks (
			id TEXT PRIMARY KEY,
			client_id TEXT NOT NULL,
			idempotency_key TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL,
			status TEXT NOT NULL,
			original_filename TEXT NOT NULL,
			remote_filename TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL,
			file_size INTEGER NOT NULL,
			sha256 TEXT NOT NULL,
			spool_path TEXT NOT NULL DEFAULT '',
			spool_cleaned INTEGER NOT NULL DEFAULT 0,
			custom_subpath TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			deduplicated INTEGER NOT NULL DEFAULT 0,
			webhook_status TEXT NOT NULL DEFAULT 'none',
			error_summary TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			started_at DATETIME,
			finished_at DATETIME,
			FOREIGN KEY (client_id) REFERENCES clients(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_backup_tasks_client_created ON backup_tasks(client_id, created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_backup_tasks_client_sha256 ON backup_tasks(client_id, sha256);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_backup_tasks_idempotency ON backup_tasks(client_id, idempotency_key) WHERE idempotency_key != '';`,
		`CREATE INDEX IF NOT EXISTS idx_backup_tasks_status ON backup_tasks(status);`,
		`CREATE TABLE IF NOT EXISTS backup_task_items (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			upstream_id TEXT NOT NULL,
			upstream_name TEXT NOT NULL,
			upstream_type TEXT NOT NULL,
			remote_path TEXT NOT NULL,
			remote_etag TEXT NOT NULL DEFAULT '',
			remote_version_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			max_attempts INTEGER NOT NULL DEFAULT 3,
			next_retry_at DATETIME,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			error_message TEXT NOT NULL DEFAULT '',
			started_at DATETIME,
			finished_at DATETIME,
			FOREIGN KEY (task_id) REFERENCES backup_tasks(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_task_items_task ON backup_task_items(task_id);`,
		`CREATE INDEX IF NOT EXISTS idx_task_items_status_retry ON backup_task_items(status, next_retry_at);`,
		`CREATE INDEX IF NOT EXISTS idx_task_items_upstream_status ON backup_task_items(upstream_id, status);`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			actor_type TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			actor_name TEXT NOT NULL,
			action TEXT NOT NULL,
			resource_type TEXT NOT NULL,
			resource_id TEXT NOT NULL,
			status TEXT NOT NULL,
			ip_address TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT '',
			detail_json TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_created ON audit_logs(created_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs(action);`,
		`CREATE TABLE IF NOT EXISTS cron_jobs (
			id TEXT PRIMARY KEY,
			job_key TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			cron_expr TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			timeout_sec INTEGER NOT NULL DEFAULT 300,
			config_json TEXT NOT NULL DEFAULT '{}',
			last_run_at DATETIME,
			last_status TEXT NOT NULL DEFAULT 'never',
			last_duration_ms INTEGER NOT NULL DEFAULT 0,
			last_summary TEXT NOT NULL DEFAULT '',
			next_run_at DATETIME,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS cron_job_runs (
			id TEXT PRIMARY KEY,
			job_id TEXT NOT NULL,
			trigger_type TEXT NOT NULL,
			triggered_by TEXT NOT NULL,
			status TEXT NOT NULL,
			affected_rows INTEGER NOT NULL DEFAULT 0,
			output_log TEXT NOT NULL DEFAULT '',
			started_at DATETIME NOT NULL,
			finished_at DATETIME,
			FOREIGN KEY (job_id) REFERENCES cron_jobs(id) ON DELETE CASCADE
		);`,
		`CREATE INDEX IF NOT EXISTS idx_cron_runs_job_started ON cron_job_runs(job_id, started_at DESC);`,
		`CREATE TABLE IF NOT EXISTS daily_traffic (
			date_key TEXT PRIMARY KEY,
			bytes_used INTEGER NOT NULL DEFAULT 0,
			tripped_at DATETIME,
			notified_at DATETIME,
			updated_at DATETIME NOT NULL
		);`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	if err := s.ensureColumn(ctx, "clients", "key_encrypted", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure clients.key_encrypted: %w", err)
	}
	if err := s.ensureColumn(ctx, "backup_tasks", "remote_filename", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure backup_tasks.remote_filename: %w", err)
	}
	if err := s.ensureColumn(ctx, "cron_jobs", "timeout_sec", "INTEGER NOT NULL DEFAULT 300"); err != nil {
		return fmt.Errorf("ensure cron_jobs.timeout_sec: %w", err)
	}
	if err := s.ensureColumn(ctx, "upstreams", "proxy_id", "TEXT REFERENCES proxies(id) ON DELETE RESTRICT"); err != nil {
		return fmt.Errorf("ensure upstreams.proxy_id: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_upstreams_proxy ON upstreams(proxy_id)`); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := MigrateLegacyProxies(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	return err
}

func (s *Store) seedDefaults(ctx context.Context, defaults config.SystemSettings) error {
	now := time.Now().UTC()
	settingsMap := map[string]any{
		"spool_dir":                 defaults.SpoolDir,
		"max_file_size_bytes":       defaults.MaxFileSizeBytes,
		"daily_traffic_limit_bytes": defaults.DailyTrafficLimitBytes,
		"max_spool_bytes":           defaults.MaxSpoolBytes,
		"worker_concurrency":        defaults.WorkerConcurrency,
		"sync_timeout_sec":          defaults.SyncTimeoutSec,
		"max_retry_attempts":        defaults.MaxRetryAttempts,
		"public_base_url":           defaults.PublicBaseURL,
	}

	for k, v := range settingsMap {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO system_settings(key, value_json, updated_at, updated_by) VALUES (?, ?, ?, 'system')`,
			k, string(raw), now,
		); err != nil {
			return err
		}
	}

	// Migrate legacy system-seeded port 8080 public_base_url and 512MiB max_file_size_bytes to new defaults.
	if rawNew, err := json.Marshal(defaults.PublicBaseURL); err == nil {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE system_settings SET value_json = ?, updated_at = ?
			 WHERE key = 'public_base_url' AND value_json = '"http://localhost:8080"' AND updated_by = 'system'`,
			string(rawNew), now,
		)
	}
	if rawSize, err := json.Marshal(defaults.MaxFileSizeBytes); err == nil {
		_, _ = s.db.ExecContext(ctx,
			`UPDATE system_settings SET value_json = ?, updated_at = ?
			 WHERE key = 'max_file_size_bytes' AND value_json = '536870912' AND updated_by = 'system'`,
			string(rawSize), now,
		)
	}

	defaultJobs := []struct {
		key, name, desc, expr, cfg string
		enabled, timeoutSec        int
	}{
		{
			key:        "cleanup_audit_logs",
			name:       "清理过期审计日志",
			desc:       "定期删除超过保留天数的历史审计日志，控制数据库体积。",
			expr:       "0 2 * * *",
			cfg:        `{"retention_days":90}`,
			enabled:    1,
			timeoutSec: 300,
		},
		{
			key:        "cleanup_task_records",
			name:       "清理过期任务记录",
			desc:       "定期清理已完成且临时文件已清除的历史备份任务及定时任务执行历史。",
			expr:       "0 3 * * *",
			cfg:        `{"retention_days":180}`,
			enabled:    1,
			timeoutSec: 300,
		},
		{
			key:        "cleanup_orphan_spool",
			name:       "清理缓冲盘孤儿文件",
			desc:       "扫描本地临时缓冲目录，清理超时或未被未完结任务引用的残留文件。",
			expr:       "0 * * * *",
			cfg:        `{"max_age_hours":24,"failed_spool_retention_hours":72}`,
			enabled:    1,
			timeoutSec: 300,
		},
		{
			key:        "upstream_health_check",
			name:       "上游存储与代理健康巡检",
			desc:       "定期测试所有已启用上游（含 HTTP/SOCKS5 代理）的连通性并刷新状态。",
			expr:       "*/30 * * * *",
			cfg:        `{"probe_timeout_sec":20}`,
			enabled:    1,
			timeoutSec: 180,
		},
		{
			key:        "dropbox_token_refresh",
			name:       "Dropbox OAuth2 Token 预刷新",
			desc:       "提前刷新即将过期的 Dropbox Access Token，确认 Refresh Token 持续有效。",
			expr:       "0 */4 * * *",
			cfg:        `{}`,
			enabled:    1,
			timeoutSec: 120,
		},
		{
			key:        "migration_backup",
			name:       "环境迁移包自动备份",
			desc:       "定期导出环境迁移包（.skbundle），并通过 SafeKeep HTTP API 备份到远端上游（支持本机或外部 SafeKeep）。",
			expr:       "0 4 * * *",
			cfg:        `{"api_base_url":"","client_id":"","targets":"","path":"migration-backups","mode":"sync","rename":"","include_admins":true,"include_tasks":false,"include_audit_logs":false}`,
			enabled:    0,
			timeoutSec: 300,
		},
	}

	for _, j := range defaultJobs {
		if _, err := s.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO cron_jobs(id, job_key, name, description, cron_expr, enabled, timeout_sec, config_json, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			crypto.NewULID(), j.key, j.name, j.desc, j.expr, j.enabled, j.timeoutSec, j.cfg, now,
		); err != nil {
			return err
		}
	}

	return nil
}
