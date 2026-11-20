package db

import "time"

type Admin struct {
	ID           string     `json:"id"`
	Username     string     `json:"username"`
	PasswordHash string     `json:"-"`
	IsActive     bool       `json:"is_active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type Upstream struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Type                   string     `json:"type"` // s3 | dropbox
	Enabled                bool       `json:"enabled"`
	ConfigEncrypted        string     `json:"-"`
	ProxyEnabled           bool       `json:"proxy_enabled"`
	ProxyType              string     `json:"proxy_type"` // http | https | socks5
	ProxyHost              string     `json:"proxy_host"`
	ProxyPort              int        `json:"proxy_port"`
	ProxyUsernameEncrypted string     `json:"-"`
	ProxyPasswordEncrypted string     `json:"-"`
	LastTestStatus         string     `json:"last_test_status"` // untested | ok | failed
	LastTestError          string     `json:"last_test_error,omitempty"`
	LastTestedAt           *time.Time `json:"last_tested_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

type Client struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Description            string     `json:"description"`
	KeyPrefix              string     `json:"key_prefix"`
	KeyHash                string     `json:"-"`
	KeyEncrypted           string     `json:"-"`
	Enabled                bool       `json:"enabled"`
	DefaultMode            string     `json:"default_mode"` // sync | async
	PathPrefix             string     `json:"path_prefix"`
	MaxFileSizeBytes       int64      `json:"max_file_size_bytes"`
	AllowedMimeTypesJSON   string     `json:"allowed_mime_types_json"`
	RateLimitRPM           int        `json:"rate_limit_rpm"`
	WebhookURL             string     `json:"webhook_url"`
	WebhookSecretEncrypted string     `json:"-"`
	ExpiresAt              *time.Time `json:"expires_at,omitempty"`
	LastUsedAt             *time.Time `json:"last_used_at,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

type ClientUpstream struct {
	ClientID     string `json:"client_id"`
	UpstreamID   string `json:"upstream_id"`
	UpstreamName string `json:"upstream_name,omitempty"`
	UpstreamType string `json:"upstream_type,omitempty"`
	Enabled      bool   `json:"enabled,omitempty"`
	IsDefault    bool   `json:"is_default"`
	Priority     int    `json:"priority"`
}

type BackupTask struct {
	ID               string           `json:"id"`
	ClientID         string           `json:"client_id"`
	ClientName       string           `json:"client_name,omitempty"`
	IdempotencyKey   string           `json:"idempotency_key,omitempty"`
	Mode             string           `json:"mode"`   // sync | async
	Status           string           `json:"status"` // pending | running | partial_success | succeeded | failed
	OriginalFilename string           `json:"original_filename"`
	RemoteFilename   string           `json:"remote_filename,omitempty"`
	ContentType      string           `json:"content_type"`
	FileSize         int64            `json:"file_size"`
	SHA256           string           `json:"sha256"`
	SpoolPath        string           `json:"spool_path,omitempty"`
	SpoolCleaned     bool             `json:"spool_cleaned"`
	CustomSubpath    string           `json:"custom_subpath,omitempty"`
	MetadataJSON     string           `json:"metadata_json,omitempty"`
	Deduplicated     bool             `json:"deduplicated"`
	WebhookStatus    string           `json:"webhook_status"` // none | pending | sent | failed
	ErrorSummary     string           `json:"error_summary,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	StartedAt        *time.Time       `json:"started_at,omitempty"`
	FinishedAt       *time.Time       `json:"finished_at,omitempty"`
	Items            []BackupTaskItem `json:"items,omitempty"`
}

type BackupTaskItem struct {
	ID              string     `json:"id"`
	TaskID          string     `json:"task_id"`
	UpstreamID      string     `json:"upstream_id"`
	UpstreamName    string     `json:"upstream_name"`
	UpstreamType    string     `json:"upstream_type"`
	RemotePath      string     `json:"remote_path"`
	RemoteETag      string     `json:"remote_etag,omitempty"`
	RemoteVersionID string     `json:"remote_version_id,omitempty"`
	Status          string     `json:"status"` // pending | running | succeeded | failed
	AttemptCount    int        `json:"attempt_count"`
	MaxAttempts     int        `json:"max_attempts"`
	NextRetryAt     *time.Time `json:"next_retry_at,omitempty"`
	DurationMs      int64      `json:"duration_ms"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
}

type AuditLog struct {
	ID           int64     `json:"id"`
	ActorType    string    `json:"actor_type"` // admin | client | system
	ActorID      string    `json:"actor_id"`
	ActorName    string    `json:"actor_name"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Status       string    `json:"status"` // success | failure
	IPAddress    string    `json:"ip_address"`
	UserAgent    string    `json:"user_agent"`
	RequestID    string    `json:"request_id"`
	DetailJSON   string    `json:"detail_json"`
	CreatedAt    time.Time `json:"created_at"`
}

type CronJob struct {
	ID             string     `json:"id"`
	JobKey         string     `json:"job_key"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	CronExpr       string     `json:"cron_expr"`
	Enabled        bool       `json:"enabled"`
	ConfigJSON     string     `json:"config_json"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastStatus     string     `json:"last_status"`
	LastDurationMs int64      `json:"last_duration_ms"`
	LastSummary    string     `json:"last_summary,omitempty"`
	NextRunAt      *time.Time `json:"next_run_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type CronJobRun struct {
	ID           string     `json:"id"`
	JobID        string     `json:"job_id"`
	TriggerType  string     `json:"trigger_type"` // schedule | manual
	TriggeredBy  string     `json:"triggered_by"`
	Status       string     `json:"status"` // running | succeeded | failed
	AffectedRows int64      `json:"affected_rows"`
	OutputLog    string     `json:"output_log"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

type DailyTraffic struct {
	DateKey    string     `json:"date_key"`
	BytesUsed  int64      `json:"bytes_used"`
	LimitBytes int64      `json:"limit_bytes"`
	Tripped    bool       `json:"tripped"`
	TrippedAt  *time.Time `json:"tripped_at,omitempty"`
	NotifiedAt *time.Time `json:"notified_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

type DashboardSummary struct {
	TotalTasks        int64          `json:"total_tasks"`
	SucceededTasks    int64          `json:"succeeded_tasks"`
	FailedTasks       int64          `json:"failed_tasks"`
	PendingTasks      int64          `json:"pending_tasks"`
	TotalBytesBacked  int64          `json:"total_bytes_backed"`
	DeduplicatedTasks int64          `json:"deduplicated_tasks"`
	EnabledUpstreams  int64          `json:"enabled_upstreams"`
	HealthyUpstreams  int64          `json:"healthy_upstreams"`
	EnabledClients    int64          `json:"enabled_clients"`
	SpoolBytesUsed    int64          `json:"spool_bytes_used"`
	DailyTraffic      *DailyTraffic  `json:"daily_traffic,omitempty"`
	RecentTasks       []BackupTask   `json:"recent_tasks"`
	Upstreams         []Upstream     `json:"upstreams"`
}
