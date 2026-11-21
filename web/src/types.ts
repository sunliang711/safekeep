export interface Admin {
  id: string;
  username: string;
  is_active: boolean;
  last_login_at?: string;
  created_at: string;
  updated_at: string;
}

export interface NetworkProxy {
  id: string;
  name: string;
  type: 'http' | 'https' | 'socks5';
  host: string;
  port: number;
  username: string;
  has_password: boolean;
  upstream_count: number;
  created_at: string;
  updated_at: string;
}

export interface Upstream {
  id: string;
  name: string;
  type: 's3' | 'r2' | 'gcs' | 'dropbox';
  enabled: boolean;
  config: Record<string, any>;
  proxy_id: string;
  proxy_name: string;
  proxy_enabled: boolean;
  proxy_type: 'http' | 'https' | 'socks5' | '';
  proxy_host: string;
  proxy_port: number;
  proxy_username?: string;
  has_proxy_password: boolean;
  last_test_status: 'untested' | 'ok' | 'failed';
  last_test_error?: string;
  last_tested_at?: string;
  created_at: string;
  updated_at: string;
}

export interface ClientUpstream {
  client_id?: string;
  upstream_id: string;
  upstream_name?: string;
  upstream_type?: string;
  enabled?: boolean;
  is_default: boolean;
  priority: number;
}

export interface Client {
  id: string;
  name: string;
  description: string;
  key_prefix: string;
  has_api_key?: boolean;
  enabled: boolean;
  default_mode: 'sync' | 'async';
  path_prefix: string;
  max_file_size_bytes: number;
  allowed_mime_types: string[];
  rate_limit_rpm: number;
  webhook_url: string;
  has_webhook_secret: boolean;
  expires_at?: string;
  last_used_at?: string;
  created_at: string;
  updated_at: string;
  upstreams: ClientUpstream[];
}

export interface BackupTaskItem {
  id: string;
  task_id: string;
  upstream_id: string;
  upstream_name: string;
  upstream_type: string;
  remote_path: string;
  remote_etag?: string;
  remote_version_id?: string;
  status: 'pending' | 'running' | 'succeeded' | 'failed';
  attempt_count: number;
  max_attempts: number;
  next_retry_at?: string;
  duration_ms: number;
  error_message?: string;
  started_at?: string;
  finished_at?: string;
}

export interface BackupTask {
  id: string;
  client_id: string;
  client_name?: string;
  idempotency_key?: string;
  mode: 'sync' | 'async';
  status: 'pending' | 'running' | 'partial_success' | 'succeeded' | 'failed';
  original_filename: string;
  remote_filename?: string;
  content_type: string;
  file_size: number;
  sha256: string;
  spool_cleaned: boolean;
  custom_subpath?: string;
  metadata_json?: string;
  deduplicated: boolean;
  webhook_status: string;
  error_summary?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  items?: BackupTaskItem[];
}

export interface CronJob {
  id: string;
  job_key: string;
  name: string;
  description: string;
  cron_expr: string;
  default_schedule?: string;
  enabled: boolean;
  timeout_sec: number;
  config_json: string;
  last_run_at?: string;
  last_status: string;
  last_duration_ms: number;
  last_summary?: string;
  next_run_at?: string;
  next_runs?: string[];
  updated_at: string;
}

export interface CronJobRun {
  id: string;
  job_id: string;
  trigger_type: 'schedule' | 'manual';
  triggered_by: string;
  status: 'running' | 'succeeded' | 'failed' | 'skipped' | 'timeout';
  affected_rows: number;
  output_log: string;
  started_at: string;
  finished_at?: string;
}

export interface AuditLog {
  id: number;
  actor_type: 'admin' | 'client' | 'system';
  actor_id: string;
  actor_name: string;
  action: string;
  resource_type: string;
  resource_id: string;
  status: 'success' | 'failure';
  ip_address: string;
  user_agent: string;
  request_id: string;
  detail_json: string;
  created_at: string;
}

export interface SystemSettings {
  spool_dir: string;
  max_file_size_bytes: number;
  daily_traffic_limit_bytes: number;
  max_spool_bytes: number;
  worker_concurrency: number;
  sync_timeout_sec: number;
  max_retry_attempts: number;
  public_base_url: string;
}

export interface SMTPConfig {
  enabled: boolean;
  host: string;
  port: number;
  tls_mode: 'starttls' | 'tls' | 'none';
  username: string;
  has_password: boolean;
  password?: string;
  from_email: string;
  from_name: string;
  to_emails: string[];
}

export interface DailyTraffic {
  date_key: string;
  bytes_used: number;
  limit_bytes: number;
  tripped: boolean;
  tripped_at?: string;
  notified_at?: string;
  updated_at: string;
}

export interface UploadCount {
  client_id: string;
  client_name: string;
  upload_count: number;
}

export interface UploadStats {
  today: UploadCount[];
  week: UploadCount[];
  month: UploadCount[];
  timezone: string;
}

export interface DashboardSummary {
  total_tasks: number;
  succeeded_tasks: number;
  failed_tasks: number;
  pending_tasks: number;
  total_bytes_backed: number;
  deduplicated_tasks: number;
  enabled_upstreams: number;
  healthy_upstreams: number;
  enabled_clients: number;
  spool_bytes_used: number;
  daily_traffic?: DailyTraffic;
  recent_tasks: BackupTask[];
  upstreams: Upstream[];
  api_uploads: UploadStats;
}

export interface InspectSummary {
  format_version: number;
  exported_at: string;
  includes: {
    tasks: boolean;
    admins: boolean;
    audit_logs: boolean;
  };
  counts: {
    remote_hosts: number;
    remote_jobs: number;
    remote_runs: number;
    proxies: number;
    system_settings: number;
    upstreams: number;
    clients: number;
    client_upstreams: number;
    cron_jobs: number;
    admins: number;
    backup_tasks: number;
    audit_logs: number;
  };
  proxy_conflicts: string[];
  upstream_conflicts: string[];
  client_conflicts: string[];
  admin_conflicts: string[];
}

export interface RemoteHost {
  id: string;
  name: string;
  address: string;
  port: number;
  username: string;
  public_key: string;
  fingerprint: string;
  platform: string;
  archive_tool: string;
  health_status: string;
  health_error: string;
  checked_at?: string;
}

export interface RemoteJob {
  id: string;
  name: string;
  host_id: string;
  directories: { path: string; excludes: string[] }[];
  upstream_ids: string[];
  parent_path: string;
  cron_expr: string;
  timezone: string;
  enabled: boolean;
  timeout_sec: number;
}

export interface RemoteRun {
  id: string;
  job_id: string;
  job_name: string;
  host_id: string;
  trigger: string;
  status: string;
  error?: string;
  started_at: string;
  finished_at?: string;
  archives: {
    source_path: string;
    filename: string;
    status: string;
    error?: string;
    size: number;
    sha256?: string;
    uploads: { upstream_id: string; upstream_name: string; remote_path: string; status: string; attempts: number; error?: string }[];
  }[];
}
