import type {
  Admin,
  AuditLog,
  BackupTask,
  Client,
  CronJob,
  CronJobRun,
  DailyTraffic,
  DashboardSummary,
  InspectSummary,
  SMTPConfig,
  SystemSettings,
  Upstream,
} from './types'

async function req<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(path, {
    credentials: 'include',
    ...init,
    headers: {
      ...(init.body && !(init.body instanceof FormData) ? { 'Content-Type': 'application/json' } : {}),
      ...(init.headers || {}),
    },
  })
  const text = await res.text()
  let data: any = {}
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = { raw: text }
    }
  }
  if (!res.ok) {
    const msg = data?.error?.message || data?.message || `HTTP ${res.status}`
    throw new Error(msg)
  }
  return data as T
}

export const api = {
  setupStatus: () => req<{ initialized: boolean; version?: string }>('/admin/api/v1/setup/status'),
  setupInit: (username: string, password: string) =>
    req<{ admin: Admin; token: string }>('/admin/api/v1/setup/init', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  login: (username: string, password: string) =>
    req<{ admin: Admin; token: string }>('/admin/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  logout: () => req('/admin/api/v1/auth/logout', { method: 'POST' }),
  me: () => req<{ admin: Admin; version?: string }>('/admin/api/v1/auth/me'),
  changePassword: (old_password: string, new_password: string) =>
    req('/admin/api/v1/auth/password', {
      method: 'PUT',
      body: JSON.stringify({ old_password, new_password }),
    }),

  dashboard: () => req<DashboardSummary>('/admin/api/v1/dashboard/summary'),

  listUpstreams: () => req<{ items: Upstream[] }>('/admin/api/v1/upstreams'),
  createUpstream: (payload: any) =>
    req<Upstream>('/admin/api/v1/upstreams', { method: 'POST', body: JSON.stringify(payload) }),
  updateUpstream: (id: string, payload: any) =>
    req<Upstream>(`/admin/api/v1/upstreams/${id}`, { method: 'PUT', body: JSON.stringify(payload) }),
  deleteUpstream: (id: string) => req(`/admin/api/v1/upstreams/${id}`, { method: 'DELETE' }),
  testUpstream: (id: string) =>
    req<{ status: string; error?: string }>(`/admin/api/v1/upstreams/${id}/test`, { method: 'POST' }),
  testUpstreamProxy: (payload: {
    upstream_id?: string
    upstream_type?: string
    proxy_type: string
    proxy_host: string
    proxy_port: number
    proxy_username?: string
    proxy_password?: string
    clear_proxy_password?: boolean
    target_url?: string
  }) =>
    req<{ status: string; target_url?: string; http_status?: number; latency_ms?: number; error?: string }>(
      '/admin/api/v1/upstreams/test-proxy',
      { method: 'POST', body: JSON.stringify(payload) }
    ),
  startDropboxOAuth: (id: string) =>
    req<{ authorize_url: string; redirect_uri: string }>(`/admin/api/v1/upstreams/${id}/oauth/dropbox/start`),
  revealUpstreamSecrets: (id: string) =>
    req<{ secret_access_key?: string; app_secret?: string; refresh_token?: string; proxy_password?: string }>(
      `/admin/api/v1/upstreams/${id}/secrets`
    ),

  listClients: () => req<{ items: Client[] }>('/admin/api/v1/clients'),
  createClient: (payload: any) =>
    req<{ client: Client; api_key: string }>('/admin/api/v1/clients', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  updateClient: (id: string, payload: any) =>
    req<Client>(`/admin/api/v1/clients/${id}`, { method: 'PUT', body: JSON.stringify(payload) }),
  deleteClient: (id: string) => req(`/admin/api/v1/clients/${id}`, { method: 'DELETE' }),
  rotateClientKey: (id: string) =>
    req<{ api_key: string; key_prefix: string }>(`/admin/api/v1/clients/${id}/rotate-key`, { method: 'POST' }),
  revealClientSecrets: (id: string) =>
    req<{ api_key?: string; webhook_secret?: string }>(`/admin/api/v1/clients/${id}/secrets`),

  listTasks: (params: Record<string, string>) => {
    const qs = new URLSearchParams(params).toString()
    return req<{ items: BackupTask[]; total: number }>(`/admin/api/v1/tasks?${qs}`)
  },
  getTask: (id: string) => req<BackupTask>(`/admin/api/v1/tasks/${id}`),
  retryTask: (id: string) => req<BackupTask>(`/admin/api/v1/tasks/${id}/retry`, { method: 'POST' }),
  adminUploadBackup: async (clientId: string, formData: FormData, query: Record<string, string>) => {
    const qs = new URLSearchParams({ client_id: clientId, ...query }).toString()
    return req<{ task: BackupTask; timed_out_to_async?: boolean }>(`/admin/api/v1/backups/upload?${qs}`, {
      method: 'POST',
      body: formData,
    })
  },

  listCronJobs: () => req<{ items: CronJob[] }>('/admin/api/v1/cron-jobs'),
  updateCronJob: (id: string, payload: { cron_expr: string; enabled: boolean; config: any }) =>
    req<CronJob>(`/admin/api/v1/cron-jobs/${id}`, { method: 'PUT', body: JSON.stringify(payload) }),
  runCronJob: (id: string) => req<CronJobRun>(`/admin/api/v1/cron-jobs/${id}/run`, { method: 'POST' }),
  listCronRuns: (id: string) => req<{ items: CronJobRun[] }>(`/admin/api/v1/cron-jobs/${id}/runs`),

  listAuditLogs: (params: Record<string, string>) => {
    const qs = new URLSearchParams(params).toString()
    return req<{ items: AuditLog[]; total: number }>(`/admin/api/v1/audit-logs?${qs}`)
  },

  getSettings: () => req<SystemSettings>('/admin/api/v1/settings'),
  updateSettings: (payload: SystemSettings) =>
    req<SystemSettings>('/admin/api/v1/settings', { method: 'PUT', body: JSON.stringify(payload) }),
  getSMTP: () => req<SMTPConfig>('/admin/api/v1/settings/smtp'),
  updateSMTP: (payload: any) =>
    req<SMTPConfig>('/admin/api/v1/settings/smtp', { method: 'PUT', body: JSON.stringify(payload) }),
  testSMTP: (payload?: any) =>
    req<{ status: string; host?: string; port?: number; to?: string[] }>('/admin/api/v1/settings/smtp/test', {
      method: 'POST',
      ...(payload ? { body: JSON.stringify(payload) } : {}),
    }),
  resetCircuitBreaker: () => req<DailyTraffic>('/admin/api/v1/circuit-breaker/reset', { method: 'POST' }),
  listAdmins: () => req<{ items: Admin[] }>('/admin/api/v1/admins'),
  createAdmin: (username: string, password: string) =>
    req<Admin>('/admin/api/v1/admins', { method: 'POST', body: JSON.stringify({ username, password }) }),
  updateAdmin: (id: string, payload: { is_active?: boolean; password?: string }) =>
    req<Admin>(`/admin/api/v1/admins/${id}`, { method: 'PUT', body: JSON.stringify(payload) }),

  exportMigration: async (payload: {
    include_tasks: boolean
    include_admins: boolean
    include_audit_logs: boolean
    passphrase: string
  }) => {
    const res = await fetch('/admin/api/v1/migration/export', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
    if (!res.ok) {
      const data = await res.json().catch(() => ({}))
      throw new Error(data?.error?.message || `HTTP ${res.status}`)
    }
    const blob = await res.blob()
    const disp = res.headers.get('Content-Disposition') || ''
    const match = disp.match(/filename="([^"]+)"/)
    const filename = match ? match[1] : 'safekeep-migration.skbundle'
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.click()
    URL.revokeObjectURL(url)
  },
  inspectMigration: (formData: FormData) =>
    req<InspectSummary>('/admin/api/v1/migration/inspect', { method: 'POST', body: formData }),
  importMigration: (formData: FormData) =>
    req<{ counts: InspectSummary['counts'] }>('/admin/api/v1/migration/import', {
      method: 'POST',
      body: formData,
    }),
}

export function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  return `${(bytes / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export function formatTime(iso?: string): string {
  if (!iso) return '-'
  return new Date(iso).toLocaleString('zh-CN', { hour12: false })
}
