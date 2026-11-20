import React, { useEffect, useRef, useState } from 'react'
import { Download, Search } from 'lucide-react'
import { EmptyState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useI18n } from '../contexts/I18nContext'
import type { TranslationKey } from '../i18n/zh'
import type { AuditLog } from '../types'

const ACTION_KEYS: Record<string, TranslationKey> = {
  'backup.upload': 'audit.action.backup.upload',
  'backup.web_upload': 'audit.action.backup.web_upload',
  'backup.download': 'audit.action.backup.download',
  'task.download': 'audit.action.task.download',
  'task.retry': 'audit.action.task.retry',
  'upstream.create': 'audit.action.upstream.create',
  'upstream.update': 'audit.action.upstream.update',
  'upstream.delete': 'audit.action.upstream.delete',
  'upstream.test': 'audit.action.upstream.test',
  'upstream.test_proxy': 'audit.action.upstream.test_proxy',
  'upstream.reveal_secrets': 'audit.action.upstream.reveal_secrets',
  'upstream.dropbox_oauth': 'audit.action.upstream.dropbox_oauth',
  'client.create': 'audit.action.client.create',
  'client.update': 'audit.action.client.update',
  'client.delete': 'audit.action.client.delete',
  'client.rotate_key': 'audit.action.client.rotate_key',
  'client.reveal_secrets': 'audit.action.client.reveal_secrets',
  'client.set_upstreams': 'audit.action.client.set_upstreams',
  'cron.execute': 'audit.action.cron.execute',
  'cron.update': 'audit.action.cron.update',
  'cron.reveal_secrets': 'audit.action.cron.reveal_secrets',
  'settings.update': 'audit.action.settings.update',
  'settings.smtp_update': 'audit.action.settings.smtp_update',
  'settings.smtp_test': 'audit.action.settings.smtp_test',
  'circuit_breaker.reset': 'audit.action.circuit_breaker.reset',
  'migration.export': 'audit.action.migration.export',
  'migration.import': 'audit.action.migration.import',
  'system.setup_init': 'audit.action.system.setup_init',
  'admin.login': 'audit.action.admin.login',
  'admin.change_password': 'audit.action.admin.change_password',
  'admin.create': 'audit.action.admin.create',
  'admin.update': 'audit.action.admin.update',
  'alert.oversize_rejected': 'audit.action.alert.oversize_rejected',
  'alert.circuit_breaker_tripped': 'audit.action.alert.circuit_breaker_tripped',
}

const RESOURCE_KEYS: Record<string, TranslationKey> = {
  admin: 'audit.resource.admin',
  upstream: 'audit.resource.upstream',
  client: 'audit.resource.client',
  backup_task: 'audit.resource.backup_task',
  cron_job: 'audit.resource.cron_job',
  system_settings: 'audit.resource.system_settings',
  smtp_config: 'audit.resource.smtp_config',
  daily_traffic: 'audit.resource.daily_traffic',
  migration: 'audit.resource.migration',
  alert_email: 'audit.resource.alert_email',
}

const CRON_JOB_KEYS: Record<string, TranslationKey> = {
  cleanup_audit_logs: 'cron.job.cleanup_audit_logs.name',
  cleanup_task_records: 'cron.job.cleanup_task_records.name',
  cleanup_orphan_spool: 'cron.job.cleanup_orphan_spool.name',
  upstream_health_check: 'cron.job.upstream_health_check.name',
  dropbox_token_refresh: 'cron.job.dropbox_token_refresh.name',
  migration_backup: 'cron.job.migration_backup.name',
}

const ACTION_GROUPS: { labelKey: TranslationKey; actions: string[] }[] = [
  {
    labelKey: 'audit.actionGroup.backup',
    actions: ['backup.upload', 'backup.web_upload', 'backup.download', 'task.download', 'task.retry'],
  },
  {
    labelKey: 'audit.actionGroup.upstream',
    actions: [
      'upstream.create',
      'upstream.update',
      'upstream.delete',
      'upstream.test',
      'upstream.test_proxy',
      'upstream.reveal_secrets',
      'upstream.dropbox_oauth',
    ],
  },
  {
    labelKey: 'audit.actionGroup.client',
    actions: ['client.create', 'client.update', 'client.delete', 'client.rotate_key', 'client.reveal_secrets', 'client.set_upstreams'],
  },
  {
    labelKey: 'audit.actionGroup.cron',
    actions: ['cron.execute', 'cron.update', 'cron.reveal_secrets'],
  },
  {
    labelKey: 'audit.actionGroup.settings',
    actions: ['settings.update', 'settings.smtp_update', 'settings.smtp_test', 'circuit_breaker.reset'],
  },
  {
    labelKey: 'audit.actionGroup.migration',
    actions: ['migration.export', 'migration.import'],
  },
  {
    labelKey: 'audit.actionGroup.account',
    actions: ['system.setup_init', 'admin.login', 'admin.change_password', 'admin.create', 'admin.update'],
  },
  {
    labelKey: 'audit.actionGroup.alert',
    actions: ['alert.oversize_rejected', 'alert.circuit_breaker_tripped'],
  },
]

function parseDetailJSON(raw?: string): Record<string, any> | null {
  if (!raw) return null
  try {
    const parsed = JSON.parse(raw)
    return parsed && typeof parsed === 'object' ? parsed : null
  } catch {
    return null
  }
}

function shortId(id?: string): string {
  if (!id) return ''
  if (id.length <= 14) return id
  return `${id.slice(0, 8)}…${id.slice(-4)}`
}

export const AuditPage: React.FC = () => {
  const { t, lang } = useI18n()
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const requestId = useRef(0)
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const limit = 50
  const [actorType, setActorType] = useState('')
  const [status, setStatus] = useState('')
  const [action, setAction] = useState('')
  const [q, setQ] = useState('')
  const [fromDate, setFromDate] = useState('')
  const [toDate, setToDate] = useState('')
  const [pageErr, setPageErr] = useState('')
  const [exportQuery, setExportQuery] = useState('')
  const [detail, setDetail] = useState<AuditLog | null>(null)

  const buildParams = (includePage: boolean, targetOffset = offset, filters = { actorType, status, action, q, fromDate, toDate }) => {
    const params: Record<string, string> = {}
    if (includePage) {
      params.limit = String(limit)
      params.offset = String(targetOffset)
    }
    if (filters.actorType) params.actor_type = filters.actorType
    if (filters.status) params.status = filters.status
    if (filters.action) params.action = filters.action
    if (filters.q) params.q = filters.q
    if (filters.fromDate) params.from = new Date(filters.fromDate).toISOString()
    if (filters.toDate) params.to = new Date(filters.toDate).toISOString()
    return params
  }

  const load = async (targetOffset = offset, filters = { actorType, status, action, q, fromDate, toDate }) => {
    const currentRequest = ++requestId.current
    setLoading(true)
    setPageErr('')
    try {
      if (filters.fromDate && filters.toDate && new Date(filters.fromDate) > new Date(filters.toDate)) {
        throw new Error(t('audit.errDateRange'))
      }
      const res = await api.listAuditLogs(buildParams(true, targetOffset, filters))
      if (currentRequest !== requestId.current) return
      setExportQuery(new URLSearchParams(buildParams(false, targetOffset, filters)).toString())
      setLogs(res.items || [])
      setTotal(res.total || 0)
    } catch (err: any) {
      if (currentRequest === requestId.current) setPageErr(err.message || t('audit.loadError'))
    } finally {
      if (currentRequest === requestId.current) setLoading(false)
    }
  }

  useEffect(() => {
    setOffset(0)
    load(0)
  }, [actorType, status, action])

  const resetFilters = () => {
    setActorType('')
    setStatus('')
    setAction('')
    setQ('')
    setFromDate('')
    setToDate('')
    setOffset(0)
    load(0, { actorType: '', status: '', action: '', q: '', fromDate: '', toDate: '' })
  }

  const actorLabels: Record<string, string> = {
    admin: t('audit.actorAdmin'),
    client: t('audit.actorClient'),
    system: t('audit.actorSystem'),
  }

  const formatActionLabel = (rawAction: string): string => {
    const key = ACTION_KEYS[rawAction]
    return key ? t(key) : rawAction
  }

  const formatResourceTypeLabel = (rawType: string): string => {
    const key = RESOURCE_KEYS[rawType]
    return key ? t(key) : rawType
  }

  const formatResourceSummary = (log: AuditLog): string => {
    const d = parseDetailJSON(log.detail_json)
    const errText = d?.error ? t('audit.summary.error', { msg: d.error }) : ''

    switch (log.resource_type) {
      case 'backup_task': {
        const name = d?.remote_filename || d?.filename
        if (name) {
          const parts = [String(name)]
          if (typeof d?.file_size === 'number' && d.file_size > 0) {
            parts.push(`(${formatBytes(d.file_size)})`)
          }
          if (d?.deduplicated) {
            parts.push(`[${t('audit.summary.dedup')}]`)
          }
          if (errText) parts.push(`· ${errText}`)
          return parts.join(' ')
        }
        if (typeof d?.requeued_items === 'number') {
          return `${t('audit.summary.requeued', { count: d.requeued_items })}${log.resource_id ? ` · ${shortId(log.resource_id)}` : ''}`
        }
        return shortId(log.resource_id)
      }
      case 'upstream': {
        if (d?.name) {
          const typeLabel = d?.type ? ` (${String(d.type).toUpperCase()})` : ''
          return `${d.name}${typeLabel}${errText ? ` · ${errText}` : ''}`
        }
        if (d?.proxy_host) {
          const proto = d.proxy_type ? `${String(d.proxy_type).toUpperCase()}://` : ''
          return `${proto}${d.proxy_host}:${d.proxy_port ?? ''}${errText ? ` · ${errText}` : ''}`
        }
        if (d?.account_email) {
          return String(d.account_email)
        }
        return shortId(log.resource_id)
      }
      case 'client': {
        if (d?.name) {
          const prefix = d?.key_prefix ? ` (${t('audit.summary.keyPrefix', { prefix: d.key_prefix })})` : ''
          return `${d.name}${prefix}`
        }
        if (d?.new_prefix) {
          return t('audit.summary.keyPrefix', { prefix: d.new_prefix })
        }
        return shortId(log.resource_id)
      }
      case 'cron_job': {
        const jobKey = d?.job_key ? String(d.job_key) : ''
        const jobName = jobKey && CRON_JOB_KEYS[jobKey] ? t(CRON_JOB_KEYS[jobKey]) : jobKey || shortId(log.resource_id)
        if (log.action === 'cron.execute') {
          const trigger = d?.trigger_type === 'manual' ? t('audit.summary.cronManual') : t('audit.summary.cronScheduled')
          const summary = d?.summary ? ` · ${d.summary}` : ''
          return `${jobName} (${trigger})${summary}`
        }
        if (d?.cron_expr) {
          return `${jobName} · ${d.cron_expr}`
        }
        return jobName
      }
      case 'admin': {
        if (d?.username) return String(d.username)
        if (log.actor_name) return log.actor_name
        return shortId(log.resource_id)
      }
      case 'system_settings':
        return t('audit.summary.globalSettings')
      case 'smtp_config': {
        if (d?.host) {
          const addr = `${d.host}${d.port ? `:${d.port}` : ''}`
          const toList = Array.isArray(d.to) && d.to.length > 0 ? ` · ${t('audit.summary.smtpRecipients', { to: d.to.join(', ') })}` : ''
          return `${addr}${toList}${errText ? ` · ${errText}` : ''}`
        }
        return t('audit.summary.smtpConfig')
      }
      case 'daily_traffic':
        return log.resource_id || ''
      case 'migration': {
        if (log.action === 'migration.export' && typeof d?.size_bytes === 'number') {
          return t('audit.summary.migrationExport', { size: formatBytes(d.size_bytes) })
        }
        if (log.action === 'migration.import') {
          if (errText) return errText
          if (d?.counts) {
            const modeLabel =
              d.mode === 'replace' ? t('audit.summary.migrationImportReplace') : t('audit.summary.migrationImportMerge')
            return t('audit.summary.migrationCounts', {
              mode: modeLabel,
              upstreams: d.counts.upstreams ?? 0,
              clients: d.counts.clients ?? 0,
              cron: d.counts.cron_jobs ?? 0,
            })
          }
        }
        return shortId(log.resource_id)
      }
      case 'alert_email': {
        const recipients = Array.isArray(d?.to) && d.to.length > 0 ? d.to.join(', ') : ''
        if (recipients) return `${t('audit.summary.smtpRecipients', { to: recipients })}${errText ? ` · ${errText}` : ''}`
        if (errText) return errText
        return log.resource_id || ''
      }
      default:
        return shortId(log.resource_id)
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">{t('audit.title')}</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">{t('audit.subtitle', { total })}</p>
        </div>
        <div className="flex gap-2">
          <a
            href={`/admin/api/v1/audit-logs/export?format=csv&${exportQuery}`}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg text-sm hover:bg-slate-50 dark:hover:bg-slate-700"
          >
            <Download className="w-4 h-4" /> {t('audit.exportCsv')}
          </a>
          <a
            href={`/admin/api/v1/audit-logs/export?format=json&${exportQuery}`}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg text-sm hover:bg-slate-50 dark:hover:bg-slate-700"
          >
            <Download className="w-4 h-4" /> {t('audit.exportJson')}
          </a>
        </div>
      </div>

      <div className="filter-bar flex flex-wrap gap-3 bg-white dark:bg-slate-900 p-4 rounded-xl border border-slate-200 dark:border-slate-800">
        <div className="flex items-center gap-2 flex-1 min-w-[200px] border rounded-lg px-3 py-1.5">
          <Search className="w-4 h-4 text-slate-400" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') { setOffset(0); load(0) } }}
            aria-label={t('audit.searchAria')}
            placeholder={t('audit.searchPlaceholder')}
            className="w-full text-sm focus:outline-none bg-transparent"
          />
        </div>
        <select
          aria-label={t('audit.actionAria')}
          value={action}
          onChange={(e) => setAction(e.target.value)}
          className="px-3 py-1.5 border rounded-lg text-sm bg-white dark:bg-slate-900"
        >
          <option value="">{t('audit.actionAll')}</option>
          {ACTION_GROUPS.map((group) => (
            <optgroup key={group.labelKey} label={t(group.labelKey)}>
              {group.actions.map((act) => (
                <option key={act} value={act}>
                  {formatActionLabel(act)}
                </option>
              ))}
            </optgroup>
          ))}
        </select>
        <select aria-label={t('audit.actorAria')} value={actorType} onChange={(e) => setActorType(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white dark:bg-slate-900">
          <option value="">{t('audit.actorAll')}</option>
          <option value="admin">{t('audit.actorAdmin')}</option>
          <option value="client">{t('audit.actorClient')}</option>
          <option value="system">{t('audit.actorSystem')}</option>
        </select>
        <select aria-label={t('audit.statusAria')} value={status} onChange={(e) => setStatus(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white dark:bg-slate-900">
          <option value="">{t('audit.statusAll')}</option>
          <option value="success">{t('status.success')}</option>
          <option value="failure">{t('status.failure')}</option>
        </select>
        <input type="datetime-local" value={fromDate} onChange={(e) => setFromDate(e.target.value)} aria-label={t('audit.fromDateAria')} title={t('audit.fromDateAria')} className="px-2.5 py-1.5 border rounded-lg text-xs" />
        <input type="datetime-local" value={toDate} onChange={(e) => setToDate(e.target.value)} aria-label={t('audit.toDateAria')} title={t('audit.toDateAria')} className="px-2.5 py-1.5 border rounded-lg text-xs" />
        <button disabled={loading} onClick={() => { setOffset(0); load(0) }} className="px-4 py-1.5 bg-slate-900 dark:bg-indigo-600 hover:bg-slate-800 dark:hover:bg-indigo-500 text-white rounded-lg text-sm">
          {loading ? t('common.searching') : t('common.search')}
        </button>
        {(q || action || actorType || status || fromDate || toDate) && (
          <button onClick={resetFilters} className="text-sm text-slate-500 dark:text-slate-400 px-2 hover:text-indigo-600 dark:hover:text-indigo-400">
            {t('common.resetFilters')}
          </button>
        )}
      </div>

      {pageErr && (
        <div className="p-3 bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 rounded-lg text-sm">
          {pageErr}
        </div>
      )}

      <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 overflow-x-auto">
        <table aria-busy={loading} className="audit-table w-full text-sm">
          <thead className="bg-slate-50 dark:bg-slate-950/60 text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className="text-left px-4 py-3">{t('audit.colTime')}</th>
              <th className="text-left px-4 py-3">{t('audit.colActor')}</th>
              <th className="text-left px-4 py-3">{t('audit.colAction')}</th>
              <th className="text-left px-4 py-3">{t('audit.colResource')}</th>
              <th className="text-left px-4 py-3">{t('audit.colIp')}</th>
              <th className="text-left px-4 py-3">{t('audit.colResult')}</th>
              <th className="text-right px-4 py-3">{t('audit.colDetail')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {!loading && logs.map((l) => {
              const actionTitle = formatActionLabel(l.action)
              const resourceLabel = formatResourceTypeLabel(l.resource_type)
              const resourceSummary = formatResourceSummary(l)
              return (
                <tr key={l.id}>
                  <td className="px-4 py-2.5 text-xs text-slate-500 dark:text-slate-400 whitespace-nowrap">{formatTime(l.created_at, lang)}</td>
                  <td className="px-4 py-2.5 text-xs">
                    <span className="px-1.5 py-0.5 bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300 rounded mr-1">
                      {actorLabels[l.actor_type] || l.actor_type}
                    </span>
                    <span className="font-medium text-slate-800 dark:text-slate-100">{l.actor_name || '-'}</span>
                  </td>
                  <td className="px-4 py-2.5 text-xs font-medium text-slate-800 dark:text-slate-100" title={l.action}>
                    {actionTitle}
                  </td>
                  <td className="px-4 py-2.5 text-xs text-slate-600 dark:text-slate-300">
                    <div className="flex items-center gap-1.5 min-w-0">
                      <span className="px-1.5 py-0.5 rounded bg-indigo-50 dark:bg-indigo-500/15 text-indigo-700 dark:text-indigo-300 border border-indigo-200/70 dark:border-indigo-500/30 text-[11px] font-medium shrink-0">
                        {resourceLabel}
                      </span>
                      {resourceSummary && (
                        <span className="truncate max-w-[280px] text-slate-700 dark:text-slate-200" title={l.resource_id ? `${resourceSummary} (${l.resource_id})` : resourceSummary}>
                          {resourceSummary}
                        </span>
                      )}
                    </div>
                  </td>
                  <td className="px-4 py-2.5 font-mono text-xs text-slate-500 dark:text-slate-400">{l.ip_address || '-'}</td>
                  <td className="px-4 py-2.5"><StatusBadge value={l.status} /></td>
                  <td className="px-4 py-2.5 text-right">
                    <button onClick={() => setDetail(l)} className="text-xs text-indigo-600 dark:text-indigo-400 hover:underline">
                      {t('common.view')}
                    </button>
                  </td>
                </tr>
              )
            })}
            {(loading || logs.length === 0) && (
              <tr>
                <td colSpan={7}>
                  <EmptyState
                    title={loading ? t('audit.loadingTitle') : t('audit.emptyTitle')}
                    description={loading ? undefined : t('audit.emptyDesc')}
                  />
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between text-xs text-slate-500 dark:text-slate-400">
        <span>{t('common.pageSummary', { page: Math.floor(offset / limit) + 1, total })}</span>
        <div className="flex gap-2">
          <button
            disabled={loading || offset === 0}
            onClick={() => { const next = Math.max(0, offset - limit); setOffset(next); load(next) }}
            className="px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg disabled:opacity-40"
          >
            {t('common.prevPage')}
          </button>
          <button
            disabled={loading || offset + limit >= total}
            onClick={() => { const next = offset + limit; setOffset(next); load(next) }}
            className="px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg disabled:opacity-40"
          >
            {t('common.nextPage')}
          </button>
        </div>
      </div>

      {detail && (
        <Modal title={t('audit.detailTitle', { id: detail.id })} onClose={() => setDetail(null)} size="lg">
          <div className="bg-white dark:bg-slate-900 rounded-2xl max-w-2xl w-full p-6 space-y-4">
            <div className="flex items-center justify-between gap-3 pr-8">
              <div>
                <h2 className="text-lg font-bold text-slate-900 dark:text-slate-100">{formatActionLabel(detail.action)}</h2>
                <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">{t('audit.detailTitle', { id: detail.id })} · {formatTime(detail.created_at, lang)}</p>
              </div>
              <StatusBadge value={detail.status} />
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5 text-xs bg-slate-50 dark:bg-slate-800/60 border border-slate-200/70 dark:border-slate-700 p-3.5 rounded-xl text-slate-700 dark:text-slate-200">
              <div>
                <span className="text-slate-400">{t('audit.colActor')}: </span>
                <span className="font-medium">{actorLabels[detail.actor_type] || detail.actor_type} · {detail.actor_name || '-'}</span>
              </div>
              <div>
                <span className="text-slate-400">{t('audit.colIp')}: </span>
                <span className="font-mono">{detail.ip_address || '-'}</span>
              </div>
              <div className="sm:col-span-2">
                <span className="text-slate-400">{t('audit.colResource')}: </span>
                <span className="font-medium">{formatResourceTypeLabel(detail.resource_type)}</span>
                {formatResourceSummary(detail) ? ` · ${formatResourceSummary(detail)}` : ''}
              </div>
              <div>
                <span className="text-slate-400">{t('audit.detail.actionCode')}: </span>
                <code className="font-mono">{detail.action}</code>
              </div>
              {detail.resource_id && (
                <div className="break-all">
                  <span className="text-slate-400">{t('audit.detail.resourceId')}: </span>
                  <code className="font-mono">{detail.resource_id}</code>
                </div>
              )}
              {detail.request_id && (
                <div className="sm:col-span-2 break-all">
                  <span className="text-slate-400">{t('audit.detail.requestId')}: </span>
                  <code className="font-mono">{detail.request_id}</code>
                </div>
              )}
              {detail.user_agent && (
                <div className="sm:col-span-2 break-all">
                  <span className="text-slate-400">{t('audit.detail.userAgent')}: </span>
                  <span className="font-mono">{detail.user_agent}</span>
                </div>
              )}
            </div>

            <div className="space-y-1.5">
              <div className="text-xs font-medium text-slate-500 dark:text-slate-400">{t('audit.detail.rawJson')}</div>
              <pre className="bg-slate-900 dark:bg-slate-950 border border-slate-800 text-slate-100 p-4 rounded-xl text-xs overflow-auto max-h-72">
                {JSON.stringify(
                  { ...detail, detail: (() => { try { return JSON.parse(detail.detail_json) } catch { return detail.detail_json } })() },
                  null,
                  2,
                )}
              </pre>
            </div>
            <div className="flex justify-end">
              <button onClick={() => setDetail(null)} className="px-4 py-2 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-800 rounded-lg text-sm">
                {t('common.close')}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
