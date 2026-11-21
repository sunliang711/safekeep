package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// RemoteHost 保存 SSH 主机配置，私钥仅在独立的加密列中持久化。
type RemoteHost struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Address             string     `json:"address"`
	Port                int        `json:"port"`
	Username            string     `json:"username"`
	PublicKey           string     `json:"public_key"`
	PrivateKeyEncrypted string     `json:"-"`
	HostKey             string     `json:"host_key"`
	Fingerprint         string     `json:"fingerprint"`
	Platform            string     `json:"platform"`
	ArchiveTool         string     `json:"archive_tool"`
	HealthStatus        string     `json:"health_status"`
	HealthError         string     `json:"health_error"`
	CheckedAt           *time.Time `json:"checked_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type RemoteDirectory struct {
	Path     string   `json:"path"`
	Excludes []string `json:"excludes"`
}

type RemoteJob struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	HostID      string            `json:"host_id"`
	Directories []RemoteDirectory `json:"directories"`
	UpstreamIDs []string          `json:"upstream_ids"`
	ParentPath  string            `json:"parent_path"`
	CronExpr    string            `json:"cron_expr"`
	Timezone    string            `json:"timezone"`
	Enabled     bool              `json:"enabled"`
	TimeoutSec  int               `json:"timeout_sec"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type RemoteUpload struct {
	UpstreamID   string `json:"upstream_id"`
	UpstreamName string `json:"upstream_name"`
	RemotePath   string `json:"remote_path"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	Error        string `json:"error,omitempty"`
}

type RemoteArchive struct {
	SourcePath string         `json:"source_path"`
	Filename   string         `json:"filename"`
	Status     string         `json:"status"`
	Error      string         `json:"error,omitempty"`
	Size       int64          `json:"size"`
	SHA256     string         `json:"sha256,omitempty"`
	Uploads    []RemoteUpload `json:"uploads"`
}

// RemoteRunProgress 保存当前步骤，目录和上游序号从 1 开始，0 表示尚未开始。
type RemoteRunProgress struct {
	Stage          string     `json:"stage"`
	DirectoryIndex int        `json:"directory_index,omitempty"`
	UpstreamIndex  int        `json:"upstream_index,omitempty"`
	RetryAt        *time.Time `json:"retry_at,omitempty"`
}

type RemoteRun struct {
	ID         string             `json:"id"`
	JobID      string             `json:"job_id"`
	JobName    string             `json:"job_name"`
	HostID     string             `json:"host_id"`
	Trigger    string             `json:"trigger"`
	Status     string             `json:"status"`
	Progress   *RemoteRunProgress `json:"progress,omitempty"`
	Error      string             `json:"error,omitempty"`
	Archives   []RemoteArchive    `json:"archives"`
	StartedAt  time.Time          `json:"started_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
	Spools     map[string]string  `json:"-"`
}

func (s *Store) migrateRemoteBackups(ctx context.Context) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS remote_hosts (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, private_key_encrypted TEXT NOT NULL, data_json TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS remote_backup_jobs (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, host_id TEXT NOT NULL REFERENCES remote_hosts(id) ON DELETE RESTRICT, data_json TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS remote_backup_runs (id TEXT PRIMARY KEY, job_id TEXT NOT NULL, status TEXT NOT NULL, started_at DATETIME NOT NULL, data_json TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_remote_runs_job_started ON remote_backup_runs(job_id, started_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_remote_runs_running ON remote_backup_runs(job_id) WHERE status='running'`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListRemoteHosts(ctx context.Context) ([]RemoteHost, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data_json, private_key_encrypted FROM remote_hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]RemoteHost, 0)
	for rows.Next() {
		var raw, secret string
		if err := rows.Scan(&raw, &secret); err != nil {
			return nil, err
		}
		var h RemoteHost
		if err := json.Unmarshal([]byte(raw), &h); err != nil {
			return nil, err
		}
		h.PrivateKeyEncrypted = secret
		list = append(list, h)
	}
	return list, rows.Err()
}

func (s *Store) GetRemoteHost(ctx context.Context, id string) (*RemoteHost, error) {
	var raw, secret string
	if err := s.db.QueryRowContext(ctx, `SELECT data_json, private_key_encrypted FROM remote_hosts WHERE id=?`, id).Scan(&raw, &secret); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var h RemoteHost
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return nil, err
	}
	h.PrivateKeyEncrypted = secret
	return &h, nil
}

func (s *Store) SaveRemoteHost(ctx context.Context, h *RemoteHost) error {
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO remote_hosts(id,name,private_key_encrypted,data_json) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,private_key_encrypted=excluded.private_key_encrypted,data_json=excluded.data_json`, h.ID, h.Name, h.PrivateKeyEncrypted, string(raw))
	return err
}

func (s *Store) DeleteRemoteHost(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM remote_hosts WHERE id=?`, id)
	return err
}

func (s *Store) ListRemoteJobs(ctx context.Context) ([]RemoteJob, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data_json FROM remote_backup_jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]RemoteJob, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var j RemoteJob
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			return nil, err
		}
		list = append(list, j)
	}
	return list, rows.Err()
}

func (s *Store) GetRemoteJob(ctx context.Context, id string) (*RemoteJob, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT data_json FROM remote_backup_jobs WHERE id=?`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var j RemoteJob
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Store) SaveRemoteJob(ctx context.Context, j *RemoteJob) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO remote_backup_jobs(id,name,host_id,data_json) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,host_id=excluded.host_id,data_json=excluded.data_json`, j.ID, j.Name, j.HostID, string(raw))
	return err
}

func (s *Store) DeleteRemoteJob(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM remote_backup_jobs WHERE id=?`, id)
	return err
}

// 执行记录的私有部分与对外响应分离，避免暴露本地缓冲路径。
type remoteRunRecord struct {
	Run    *RemoteRun        `json:"run"`
	Spools map[string]string `json:"spools"`
}

func (s *Store) SaveRemoteRun(ctx context.Context, r *RemoteRun) error {
	raw, err := json.Marshal(remoteRunRecord{r, r.Spools})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO remote_backup_runs(id,job_id,status,started_at,data_json) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,data_json=excluded.data_json`, r.ID, r.JobID, r.Status, r.StartedAt, string(raw))
	return err
}

func decodeRemoteRun(raw string) (*RemoteRun, error) {
	var rec remoteRunRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, err
	}
	if rec.Run == nil {
		return nil, errors.New("invalid remote run")
	}
	rec.Run.Spools = rec.Spools
	return rec.Run, nil
}

func (s *Store) GetRemoteRun(ctx context.Context, id string) (*RemoteRun, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT data_json FROM remote_backup_runs WHERE id=?`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return decodeRemoteRun(raw)
}

func (s *Store) ListRemoteRuns(ctx context.Context, jobID string, limit int) ([]RemoteRun, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data_json FROM remote_backup_runs WHERE (?='' OR job_id=?) ORDER BY started_at DESC LIMIT ?`, jobID, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]RemoteRun, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		r, err := decodeRemoteRun(raw)
		if err != nil {
			return nil, err
		}
		list = append(list, *r)
	}
	return list, rows.Err()
}

func (s *Store) ListInterruptedRemoteRuns(ctx context.Context) ([]RemoteRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data_json FROM remote_backup_runs WHERE status='running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]RemoteRun, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		r, err := decodeRemoteRun(raw)
		if err != nil {
			return nil, err
		}
		list = append(list, *r)
	}
	return list, rows.Err()
}

func (s *Store) ListRemoteBufferedRuns(ctx context.Context, before time.Time) ([]RemoteRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data_json FROM remote_backup_runs WHERE status!='running' AND started_at<? AND json_extract(data_json,'$.spools')!='{}'`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]RemoteRun, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		r, err := decodeRemoteRun(raw)
		if err != nil {
			return nil, err
		}
		list = append(list, *r)
	}
	return list, rows.Err()
}

func (s *Store) RemoteSpoolPaths(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data_json FROM remote_backup_runs WHERE json_extract(data_json,'$.spools')!='{}'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		r, err := decodeRemoteRun(raw)
		if err != nil {
			return nil, err
		}
		for _, p := range r.Spools {
			paths[p] = true
		}
	}
	return paths, rows.Err()
}
