import React, { useEffect, useMemo, useRef, useState } from 'react'
import { Loader2, Plus, RefreshCw, X } from 'lucide-react'
import { LoadingState } from '../components/Feedback'
import { Drawer } from '../components/Drawer'
import { useConfirm } from '../components/ConfirmDialog'
import { SecretField } from '../components/SecretField'
import { api, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import { useI18n } from '../contexts/I18nContext'
import type { TranslationKey } from '../i18n/zh'
import { CRON_PRESETS, describeCron, formatCronPresetLabel, matchCronPreset } from '../utils/cron'
import type { Client, CronJob, CronJobRun } from '../types'

const JOB_I18N: Record<string, { name: TranslationKey; desc: TranslationKey }> = {
  cleanup_audit_logs: { name: 'cron.job.cleanup_audit_logs.name', desc: 'cron.job.cleanup_audit_logs.desc' },
  cleanup_task_records: { name: 'cron.job.cleanup_task_records.name', desc: 'cron.job.cleanup_task_records.desc' },
  cleanup_orphan_spool: { name: 'cron.job.cleanup_orphan_spool.name', desc: 'cron.job.cleanup_orphan_spool.desc' },
  upstream_health_check: { name: 'cron.job.upstream_health_check.name', desc: 'cron.job.upstream_health_check.desc' },
  dropbox_token_refresh: { name: 'cron.job.dropbox_token_refresh.name', desc: 'cron.job.dropbox_token_refresh.desc' },
  migration_backup: { name: 'cron.job.migration_backup.name', desc: 'cron.job.migration_backup.desc' },
}

interface MigrationBackupForm {
  api_base_url: string
  client_id: string
  api_key: string
  has_api_key: boolean
  clear_api_key: boolean
  targets: string[]
  path: string
  mode: 'sync' | 'async'
  rename: string
  passphrase: string
  has_passphrase: boolean
  clear_passphrase: boolean
  include_admins: boolean
  include_tasks: boolean
  include_audit_logs: boolean
}

interface RetentionForm {
  retention_days: number
}

interface SpoolCleanupForm {
  max_age_hours: number
  failed_spool_retention_hours: number
}

interface HealthCheckForm {
  probe_timeout_sec: number
}

type StructuredParams =
  | { kind: 'cleanup_audit_logs'; data: RetentionForm }
  | { kind: 'cleanup_task_records'; data: RetentionForm }
  | { kind: 'cleanup_orphan_spool'; data: SpoolCleanupForm }
  | { kind: 'upstream_health_check'; data: HealthCheckForm }
  | { kind: 'dropbox_token_refresh'; data: Record<string, never> }
  | { kind: 'migration_backup'; data: MigrationBackupForm }
  | { kind: 'raw'; data: string }

function safeParseJSON(raw?: string): Record<string, any> {
  try {
    return JSON.parse(raw || '{}') || {}
  } catch {
    return {}
  }
}

function parseMigrationBackupForm(raw?: string): MigrationBackupForm {
  const obj = safeParseJSON(raw)
  const rawTargets = typeof obj.targets === 'string' ? obj.targets : ''
  const targets = rawTargets
    .split(',')
    .map((s: string) => s.trim())
    .filter(Boolean)
  return {
    api_base_url: obj.api_base_url || '',
    client_id: obj.client_id || '',
    api_key: '',
    has_api_key: Boolean(obj.has_api_key),
    clear_api_key: false,
    targets,
    path: obj.path ?? 'migration-backups',
    mode: obj.mode === 'async' ? 'async' : 'sync',
    rename: obj.rename || '',
    passphrase: '',
    has_passphrase: Boolean(obj.has_passphrase),
    clear_passphrase: false,
    include_admins: obj.include_admins !== undefined ? Boolean(obj.include_admins) : true,
    include_tasks: Boolean(obj.include_tasks),
    include_audit_logs: Boolean(obj.include_audit_logs),
  }
}

function parseStructuredParams(job: CronJob): StructuredParams {
  const obj = safeParseJSON(job.config_json)
  switch (job.job_key) {
    case 'cleanup_audit_logs':
      return {
        kind: 'cleanup_audit_logs',
        data: { retention_days: Number(obj.retention_days) > 0 ? Number(obj.retention_days) : 90 },
      }
    case 'cleanup_task_records':
      return {
        kind: 'cleanup_task_records',
        data: { retention_days: Number(obj.retention_days) > 0 ? Number(obj.retention_days) : 180 },
      }
    case 'cleanup_orphan_spool':
      return {
        kind: 'cleanup_orphan_spool',
        data: {
          max_age_hours: Number(obj.max_age_hours) > 0 ? Number(obj.max_age_hours) : 24,
          failed_spool_retention_hours:
            Number(obj.failed_spool_retention_hours) > 0 ? Number(obj.failed_spool_retention_hours) : 72,
        },
      }
    case 'upstream_health_check':
      return {
        kind: 'upstream_health_check',
        data: { probe_timeout_sec: Number(obj.probe_timeout_sec) > 0 ? Number(obj.probe_timeout_sec) : 20 },
      }
    case 'dropbox_token_refresh':
      return { kind: 'dropbox_token_refresh', data: {} }
    case 'migration_backup':
      return { kind: 'migration_backup', data: parseMigrationBackupForm(job.config_json) }
    default:
      return { kind: 'raw', data: job.config_json || '{}' }
  }
}

function formatDuration(startedAt: string, finishedAt?: string): string {
  if (!finishedAt) return ''
  const ms = new Date(finishedAt).getTime() - new Date(startedAt).getTime()
  if (!Number.isFinite(ms) || ms < 0) return ''
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  const m = Math.floor(ms / 60_000)
  const s = Math.round((ms % 60_000) / 1000)
  return `${m}m${s}s`
}

const Switch: React.FC<{
  checked: boolean
  disabled?: boolean
  label?: string
  onChange: (next: boolean) => void
}> = ({ checked, disabled, label, onChange }) => (
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={label}
    disabled={disabled}
    onClick={() => onChange(!checked)}
    className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors focus-visible:outline-indigo-500 disabled:opacity-50 ${
      checked ? 'bg-indigo-600' : 'bg-slate-200 hover:bg-slate-300 dark:bg-slate-700 dark:hover:bg-slate-600'
    }`}
  >
    <span
      className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow-sm transition-transform ${
        checked ? 'translate-x-[18px]' : 'translate-x-[3px]'
      }`}
    />
  </button>
)

export const CronPage: React.FC = () => {
  const askConfirm = useConfirm()
  const toast = useToast()
  const { t, lang } = useI18n()
  const [loading, setLoading] = useState(true)
  const [jobs, setJobs] = useState<CronJob[]>([])
  const [clients, setClients] = useState<Client[]>([])
  const [runningId, setRunningId] = useState<string | null>(null)
  const [togglingId, setTogglingId] = useState<string | null>(null)

  // Edit drawer state
  const [editing, setEditing] = useState<CronJob | null>(null)
  const [preset, setPreset] = useState('custom')
  const [cronExpr, setCronExpr] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [timeoutSec, setTimeoutSec] = useState(300)
  const [params, setParams] = useState<StructuredParams | null>(null)
  const [initialSnap, setInitialSnap] = useState('')
  const [previewRaw, setPreviewRaw] = useState<string[]>([])
  const [previewLoading, setPreviewLoading] = useState(false)
  const [previewError, setPreviewError] = useState('')
  const [formError, setFormError] = useState('')
  const [saving, setSaving] = useState(false)
  const [targetInput, setTargetInput] = useState('')
  const previewSeq = useRef(0)

  // History drawer state
  const [runsJob, setRunsJob] = useState<CronJob | null>(null)
  const [runs, setRuns] = useState<CronJobRun[]>([])
  const [runsLoading, setRunsLoading] = useState(false)
  const [runsError, setRunsError] = useState('')

  const jobName = (job: CronJob) => {
    const mapped = JOB_I18N[job.job_key]
    return mapped ? t(mapped.name) : job.name
  }

  const jobDesc = (job: CronJob) => {
    const mapped = JOB_I18N[job.job_key]
    return mapped ? t(mapped.desc) : job.description
  }

  const load = async () => {
    try {
      const [jobsRes, clientsRes] = await Promise.all([api.listCronJobs(), api.listClients()])
      setJobs(jobsRes.items || [])
      setClients(clientsRes.items || [])
    } catch (err: any) {
      toast.error(t('cron.loadError', { error: err.message }))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const openEdit = (job: CronJob, overrideEnabled?: boolean) => {
    const matched = matchCronPreset(job.cron_expr)
    const parsedParams = parseStructuredParams(job)
    const nextEnabled = overrideEnabled ?? job.enabled
    const nextTimeout = job.timeout_sec > 0 ? job.timeout_sec : 300

    setEditing(job)
    setPreset(matched)
    setCronExpr(job.cron_expr)
    setEnabled(nextEnabled)
    setTimeoutSec(nextTimeout)
    setParams(parsedParams)
    setTargetInput('')
    setFormError('')
    setPreviewError('')
    setPreviewRaw(job.next_runs || [])
    setInitialSnap(
      JSON.stringify({
        cronExpr: job.cron_expr,
        enabled: job.enabled,
        timeoutSec: nextTimeout,
        params: parsedParams,
      })
    )
  }

  // Live preview of next 5 execution times whenever cronExpr changes
  useEffect(() => {
    if (!editing) return
    const expr = cronExpr.trim()
    if (!expr) {
      setPreviewRaw([])
      setPreviewError(t('cron.errEmptyExpr'))
      return
    }
    const seq = ++previewSeq.current
    const timer = window.setTimeout(async () => {
      setPreviewLoading(true)
      try {
        const res = await api.previewCronSchedule(expr)
        if (previewSeq.current !== seq) return
        setPreviewRaw(res.next_runs || [])
        setPreviewError('')
      } catch (err: any) {
        if (previewSeq.current !== seq) return
        setPreviewRaw([])
        setPreviewError(err.message || t('cron.errInvalidExpr'))
      } finally {
        if (previewSeq.current === seq) setPreviewLoading(false)
      }
    }, 180)
    return () => window.clearTimeout(timer)
  }, [editing?.id, cronExpr])

  const currentSnap = useMemo(
    () => JSON.stringify({ cronExpr, enabled, timeoutSec, params }),
    [cronExpr, enabled, timeoutSec, params]
  )
  const isDirty = Boolean(editing && currentSnap !== initialSnap)

  const closeEdit = async () => {
    if (saving) return
    if (isDirty && !await askConfirm(t('cron.discardConfirm'), { title: t('common.unsavedTitle'), confirmText: t('common.discardChanges') })) return
    setEditing(null)
    setParams(null)
    setFormError('')
  }

  const handlePresetChange = (value: string) => {
    setPreset(value)
    if (value !== 'custom') {
      setCronExpr(value)
    }
  }

  const handleToggle = async (job: CronJob, nextEnabled: boolean) => {
    if (togglingId) return
    if (nextEnabled && job.job_key === 'migration_backup') {
      const parsed = parseMigrationBackupForm(job.config_json)
      if (!parsed.has_api_key && !parsed.client_id) {
        toast.info(t('cron.needAuthBeforeEnable'))
        openEdit(job, true)
        return
      }
    }
    setTogglingId(job.id)
    try {
      await api.updateCronJob(job.id, {
        cron_expr: job.cron_expr,
        enabled: nextEnabled,
        timeout_sec: job.timeout_sec,
      })
      await load()
    } catch (err: any) {
      toast.error(err.message || t('cron.toggleError'))
    } finally {
      setTogglingId(null)
    }
  }

  const handleSave = async (e?: React.FormEvent) => {
    e?.preventDefault()
    if (!editing || !params || previewError) return
    setFormError('')

    const cleanTimeout = Number(timeoutSec)
    if (!Number.isInteger(cleanTimeout) || cleanTimeout < 1 || cleanTimeout > 86400) {
      setFormError(t('cron.errTimeout'))
      return
    }

    let cfg: any
    switch (params.kind) {
      case 'cleanup_audit_logs':
      case 'cleanup_task_records': {
        const days = Number(params.data.retention_days)
        if (!Number.isInteger(days) || days < 1) {
          setFormError(t('cron.errRetentionDays'))
          return
        }
        cfg = { retention_days: days }
        break
      }
      case 'cleanup_orphan_spool': {
        const maxAge = Number(params.data.max_age_hours)
        const failedHours = Number(params.data.failed_spool_retention_hours)
        if (!Number.isInteger(maxAge) || maxAge < 1 || !Number.isInteger(failedHours) || failedHours < 1) {
          setFormError(t('cron.errSpoolHours'))
          return
        }
        cfg = { max_age_hours: maxAge, failed_spool_retention_hours: failedHours }
        break
      }
      case 'upstream_health_check': {
        const probeTimeout = Number(params.data.probe_timeout_sec)
        if (!Number.isInteger(probeTimeout) || probeTimeout < 1 || probeTimeout > 120) {
          setFormError(t('cron.errProbeTimeout'))
          return
        }
        cfg = { probe_timeout_sec: probeTimeout }
        break
      }
      case 'dropbox_token_refresh':
        cfg = {}
        break
      case 'migration_backup': {
        const mb = params.data
        if (mb.passphrase && mb.passphrase.length < 6) {
          setFormError(t('cron.errPassphraseMin'))
          return
        }
        if (enabled && !mb.has_api_key && !mb.api_key.trim() && !mb.client_id) {
          setFormError(t('cron.errNeedClientOrKey'))
          return
        }
        const pendingTag = targetInput.trim()
        const finalTargets = pendingTag && !mb.targets.includes(pendingTag) ? [...mb.targets, pendingTag] : mb.targets
        cfg = {
          api_base_url: mb.api_base_url.trim(),
          client_id: mb.client_id,
          targets: finalTargets.join(','),
          path: mb.path.trim(),
          mode: mb.mode,
          rename: mb.rename.trim(),
          include_admins: mb.include_admins,
          include_tasks: mb.include_tasks,
          include_audit_logs: mb.include_audit_logs,
        }
        if (mb.clear_api_key) {
          cfg.api_key = ''
        } else if (mb.api_key.trim()) {
          cfg.api_key = mb.api_key.trim()
        }
        if (mb.clear_passphrase) {
          cfg.passphrase = ''
        } else if (mb.passphrase) {
          cfg.passphrase = mb.passphrase
        }
        break
      }
      case 'raw':
        try {
          cfg = JSON.parse(params.data || '{}')
        } catch (err: any) {
          setFormError(t('cron.errInvalidJson', { error: err.message }))
          return
        }
        break
    }

    setSaving(true)
    try {
      await api.updateCronJob(editing.id, {
        cron_expr: cronExpr.trim(),
        enabled,
        timeout_sec: cleanTimeout,
        config: cfg,
      })
      setEditing(null)
      setParams(null)
      toast.success(t('common.saved'))
      await load()
    } catch (err: any) {
      setFormError(err.message || t('cron.saveError'))
    } finally {
      setSaving(false)
    }
  }

  const handleRunNow = async (job: CronJob) => {
    if (runningId) return
    const displayName = jobName(job)
    setRunningId(job.id)
    toast.info(t('cron.runningToast', { name: displayName }))
    try {
      const run = await api.runCronJob(job.id)
      const detail = run.output_log ? (lang === 'en' ? ` (${run.output_log})` : `（${run.output_log}）`) : ''
      if (run.status === 'succeeded') {
        toast.success(t('cron.runSucceededToast', { name: displayName, detail }))
      } else if (run.status === 'skipped') {
        toast.info(t('cron.runSkippedToast', { name: displayName, detail }))
      } else {
        toast.error(t('cron.runFailedToast', { name: displayName, detail }))
      }
      await load()
      if (runsJob?.id === job.id) {
        await loadRuns(job)
      }
    } catch (err: any) {
      toast.error(t('cron.runErrorToast', { error: err.message }))
    } finally {
      setRunningId(null)
    }
  }

  const loadRuns = async (job: CronJob) => {
    setRunsError('')
    setRunsLoading(true)
    try {
      const res = await api.listCronRuns(job.id)
      setRuns(res.items || [])
    } catch (err: any) {
      setRunsError(err.message || t('cron.loadRunsError'))
    } finally {
      setRunsLoading(false)
    }
  }

  const openRuns = (job: CronJob) => {
    setRunsJob(job)
    setRuns([])
    void loadRuns(job)
  }

  const mbForm = params?.kind === 'migration_backup' ? params.data : null
  const setMbForm = (next: MigrationBackupForm) => setParams({ kind: 'migration_backup', data: next })
  const selectedLocalClient = mbForm?.client_id ? clients.find((c) => c.id === mbForm.client_id) : undefined

  const addTargetTag = (raw: string) => {
    if (!mbForm) return
    const items = raw
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
    if (items.length === 0) return
    const next = [...mbForm.targets]
    for (const item of items) {
      if (!next.includes(item)) next.push(item)
    }
    setMbForm({ ...mbForm, targets: next })
    setTargetInput('')
  }

  const removeTargetTag = (name: string) => {
    if (!mbForm) return
    setMbForm({ ...mbForm, targets: mbForm.targets.filter((tItem) => tItem !== name) })
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">{t('cron.title')}</h1>
        <p className="text-sm text-slate-500 dark:text-slate-400">{t('cron.subtitle')}</p>
      </div>

      <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 overflow-x-auto">
        <table className="cron-table w-full text-sm">
          <thead className="bg-slate-50 dark:bg-slate-950/60 text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200/80 dark:border-slate-800">
            <tr>
              <th className="text-left px-4 py-3">{t('cron.colJob')}</th>
              <th className="text-left px-4 py-3">{t('cron.colSchedule')}</th>
              <th className="text-left px-4 py-3">{t('cron.colNextRun')}</th>
              <th className="text-left px-4 py-3">{t('cron.colLastRun')}</th>
              <th className="text-left px-4 py-3">{t('cron.colEnabled')}</th>
              <th className="text-right px-4 py-3">{t('cron.colActions')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {loading && (
              <tr>
                <td colSpan={6}>
                  <LoadingState label={t('cron.loading')} />
                </td>
              </tr>
            )}
            {!loading && jobs.length === 0 && (
              <tr>
                <td colSpan={6} className="p-8 text-center text-slate-500 dark:text-slate-400">
                  {t('cron.empty')}
                </td>
              </tr>
            )}
            {jobs.map((j) => {
              const scheduleText = describeCron(j.cron_expr, lang)
              const nextTime = j.enabled ? j.next_runs?.[0] || j.next_run_at : undefined
              const isRunning = runningId === j.id
              const displayName = jobName(j)
              return (
                <tr key={j.id}>
                  <td className="px-4 py-3.5 align-top">
                    <div className="font-medium text-slate-900 dark:text-slate-100">{displayName}</div>
                    <div className="text-xs text-slate-400 mt-0.5 leading-relaxed">{jobDesc(j)}</div>
                  </td>
                  <td className="px-4 py-3.5 align-top">
                    {scheduleText && <div className="text-xs text-slate-700 dark:text-slate-300 mb-1">{scheduleText}</div>}
                    <code className="font-mono text-[11px] bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300 px-1.5 py-0.5 rounded inline-block">
                      {j.cron_expr}
                    </code>
                  </td>
                  <td className="px-4 py-3.5 align-top text-xs text-slate-600 dark:text-slate-300 whitespace-nowrap">
                    {nextTime ? formatTime(nextTime, lang) : <span className="text-slate-400">—</span>}
                  </td>
                  <td className="px-4 py-3.5 align-top text-xs">
                    {!j.last_run_at ? (
                      <span className="text-slate-400">—</span>
                    ) : (
                      <div className="space-y-1">
                        <div className="flex items-center gap-2 whitespace-nowrap">
                          <StatusBadge value={j.last_status} />
                          <span className="text-slate-600 dark:text-slate-300">{formatTime(j.last_run_at, lang)}</span>
                        </div>
                        {j.last_summary && (
                          <div className="text-[11px] text-slate-400 max-w-[240px] truncate" title={j.last_summary}>
                            {j.last_summary}
                          </div>
                        )}
                      </div>
                    )}
                  </td>
                  <td className="px-4 py-3.5 align-top">
                    <div className="pt-0.5">
                      <Switch
                        checked={j.enabled}
                        disabled={togglingId === j.id}
                        label={t('cron.toggleAria', { name: displayName })}
                        onChange={(next) => handleToggle(j, next)}
                      />
                    </div>
                  </td>
                  <td className="px-4 py-3.5 align-top text-right whitespace-nowrap">
                    <div className="inline-flex items-center gap-1.5">
                      <button
                        type="button"
                        disabled={Boolean(runningId)}
                        onClick={() => handleRunNow(j)}
                        className="px-2.5 py-1 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-700 hover:border-slate-300 disabled:opacity-45 inline-flex items-center gap-1"
                      >
                        {isRunning && <Loader2 className="w-3 h-3 animate-spin" />}
                        {isRunning ? t('cron.running') : t('cron.runNow')}
                      </button>
                      <button
                        type="button"
                        onClick={() => openEdit(j)}
                        className="px-2.5 py-1 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-700 hover:border-slate-300"
                      >
                        {t('common.edit')}
                      </button>
                      <button
                        type="button"
                        onClick={() => openRuns(j)}
                        className="px-2.5 py-1 text-xs rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-700 hover:border-slate-300"
                      >
                        {t('cron.runsBtn')}
                      </button>
                    </div>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {editing && params && (
        <Drawer
          title={jobName(editing)}
          subtitle={jobDesc(editing)}
          onClose={closeEdit}
          busy={saving}
          width={editing.job_key === 'migration_backup' ? 'lg' : 'md'}
          footer={
            <>
              <button
                type="button"
                disabled={saving}
                onClick={closeEdit}
                className="px-3.5 py-1.5 text-xs font-medium rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-700 disabled:opacity-50"
              >
                {t('common.cancel')}
              </button>
              <button
                type="button"
                disabled={saving || Boolean(previewError) || !isDirty}
                onClick={() => handleSave()}
                className="px-4 py-1.5 text-xs font-medium rounded-lg bg-indigo-600 hover:bg-indigo-700 text-white disabled:opacity-45"
              >
                {saving ? t('common.saving') : t('common.save')}
              </button>
            </>
          }
        >
          <form onSubmit={handleSave} className="space-y-4 text-sm">
            {formError && (
              <div role="alert" className="rounded-lg border border-rose-200 dark:border-rose-500/30 bg-rose-50 dark:bg-rose-500/15 px-3 py-2 text-xs text-rose-700 dark:text-rose-300">
                {formError}
              </div>
            )}

            <div>
              <label htmlFor="cron-preset" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                {t('cron.fieldSchedule')}
              </label>
              <select
                id="cron-preset"
                value={preset}
                onChange={(e) => handlePresetChange(e.target.value)}
                className="w-full px-3 py-2 text-xs border rounded-lg bg-white dark:bg-slate-900"
              >
                {CRON_PRESETS.map((opt) => (
                  <option key={opt.value} value={opt.value}>
                    {formatCronPresetLabel(opt, t(opt.labelKey), lang)}
                  </option>
                ))}
              </select>
            </div>

            {preset === 'custom' && (
              <div>
                <label htmlFor="cron-expr" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                  {t('cron.fieldExpr')}
                </label>
                <input
                  id="cron-expr"
                  required
                  value={cronExpr}
                  onChange={(e) => setCronExpr(e.target.value)}
                  className={`w-full px-3 py-2 text-xs border rounded-lg font-mono ${
                    previewError ? 'border-rose-400 focus:border-rose-500' : ''
                  }`}
                />
                <p className="text-[11px] text-slate-400 mt-1">
                  {t('cron.fieldExprHint')}
                  <span className="font-mono">{editing.default_schedule || editing.cron_expr}</span>
                </p>
              </div>
            )}

            <div>
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-xs font-medium text-slate-700 dark:text-slate-300">{t('cron.nextRuns')}</span>
                {previewLoading && <Loader2 className="w-3 h-3 animate-spin text-slate-400" />}
              </div>
              {previewError ? (
                <div className="text-xs text-rose-600 dark:text-rose-400">{previewError}</div>
              ) : (
                <div className="rounded-lg bg-slate-50 dark:bg-slate-800/60 border border-slate-200/70 dark:border-slate-700 px-3 py-2 space-y-1">
                  {previewRaw.length > 0 ? (
                    previewRaw.map((rawTime) => (
                      <div key={rawTime} className="text-xs font-mono text-slate-500 dark:text-slate-400">
                        {formatTime(rawTime, lang)}
                      </div>
                    ))
                  ) : (
                    <div className="text-xs text-slate-400">—</div>
                  )}
                </div>
              )}
            </div>

            <div className="grid grid-cols-2 gap-3 items-start">
              <div>
                <label htmlFor="cron-timeout" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                  {t('cron.fieldTimeout')}
                </label>
                <input
                  id="cron-timeout"
                  type="number"
                  min={1}
                  max={86400}
                  value={timeoutSec}
                  onChange={(e) => setTimeoutSec(Number(e.target.value))}
                  className="w-full px-3 py-2 text-xs border rounded-lg"
                />
              </div>
              <div>
                <span className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">{t('cron.fieldEnabled')}</span>
                <div className="h-[34px] flex items-center gap-2.5">
                  <Switch checked={enabled} label={t('cron.toggleScheduleAria')} onChange={setEnabled} />
                  <span className="text-xs text-slate-600 dark:text-slate-300">{enabled ? t('common.enabled') : t('cron.statusPaused')}</span>
                </div>
              </div>
            </div>

            {params.kind === 'cleanup_audit_logs' && (
              <div className="pt-3 border-t border-slate-100 dark:border-slate-800">
                <label htmlFor="param-retention-days" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                  {t('cron.fieldRetentionDays')}
                </label>
                <input
                  id="param-retention-days"
                  type="number"
                  min={1}
                  max={36500}
                  value={params.data.retention_days}
                  onChange={(e) =>
                    setParams({ kind: 'cleanup_audit_logs', data: { retention_days: Number(e.target.value) } })
                  }
                  className="w-full px-3 py-2 text-xs border rounded-lg"
                />
                <p className="text-[11px] text-slate-400 mt-1">{t('cron.auditRetentionHint')}</p>
              </div>
            )}

            {params.kind === 'cleanup_task_records' && (
              <div className="pt-3 border-t border-slate-100 dark:border-slate-800">
                <label htmlFor="param-task-retention-days" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                  {t('cron.fieldRetentionDays')}
                </label>
                <input
                  id="param-task-retention-days"
                  type="number"
                  min={1}
                  max={36500}
                  value={params.data.retention_days}
                  onChange={(e) =>
                    setParams({ kind: 'cleanup_task_records', data: { retention_days: Number(e.target.value) } })
                  }
                  className="w-full px-3 py-2 text-xs border rounded-lg"
                />
                <p className="text-[11px] text-slate-400 mt-1">{t('cron.taskRetentionHint')}</p>
              </div>
            )}

            {params.kind === 'cleanup_orphan_spool' && (
              <div className="pt-3 border-t border-slate-100 dark:border-slate-800 space-y-3">
                <div>
                  <label htmlFor="param-max-age-hours" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                    {t('cron.fieldMaxAgeHours')}
                  </label>
                  <input
                    id="param-max-age-hours"
                    type="number"
                    min={1}
                    max={8760}
                    value={params.data.max_age_hours}
                    onChange={(e) =>
                      setParams({
                        kind: 'cleanup_orphan_spool',
                        data: { ...params.data, max_age_hours: Number(e.target.value) },
                      })
                    }
                    className="w-full px-3 py-2 text-xs border rounded-lg"
                  />
                  <p className="text-[11px] text-slate-400 mt-1">{t('cron.maxAgeHoursHint')}</p>
                </div>
                <div>
                  <label htmlFor="param-failed-hours" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                    {t('cron.fieldFailedHours')}
                  </label>
                  <input
                    id="param-failed-hours"
                    type="number"
                    min={1}
                    max={8760}
                    value={params.data.failed_spool_retention_hours}
                    onChange={(e) =>
                      setParams({
                        kind: 'cleanup_orphan_spool',
                        data: { ...params.data, failed_spool_retention_hours: Number(e.target.value) },
                      })
                    }
                    className="w-full px-3 py-2 text-xs border rounded-lg"
                  />
                  <p className="text-[11px] text-slate-400 mt-1">{t('cron.failedHoursHint')}</p>
                </div>
              </div>
            )}

            {params.kind === 'upstream_health_check' && (
              <div className="pt-3 border-t border-slate-100 dark:border-slate-800">
                <label htmlFor="param-probe-timeout" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                  {t('cron.fieldProbeTimeout')}
                </label>
                <input
                  id="param-probe-timeout"
                  type="number"
                  min={1}
                  max={120}
                  value={params.data.probe_timeout_sec}
                  onChange={(e) =>
                    setParams({ kind: 'upstream_health_check', data: { probe_timeout_sec: Number(e.target.value) } })
                  }
                  className="w-full px-3 py-2 text-xs border rounded-lg"
                />
                <p className="text-[11px] text-slate-400 mt-1">{t('cron.probeTimeoutHint')}</p>
              </div>
            )}

            {mbForm && (
              <div className="pt-3 border-t border-slate-100 dark:border-slate-800 space-y-3.5 text-xs">
                <div>
                  <label htmlFor="mb-base-url" className="block font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                    {t('cron.mb.baseUrl')}
                  </label>
                  <input
                    id="mb-base-url"
                    value={mbForm.api_base_url}
                    onChange={(e) => setMbForm({ ...mbForm, api_base_url: e.target.value })}
                    placeholder="https://backup.example.com"
                    className="w-full px-3 py-2 border rounded-lg font-mono"
                  />
                  <p className="text-[11px] text-slate-400 mt-1">{t('cron.mb.baseUrlHint')}</p>
                </div>

                <div>
                  <label htmlFor="mb-client" className="block font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                    {t('cron.mb.localClient')}
                  </label>
                  <select
                    id="mb-client"
                    value={mbForm.client_id}
                    onChange={(e) => setMbForm({ ...mbForm, client_id: e.target.value })}
                    className="w-full px-3 py-2 border rounded-lg bg-white dark:bg-slate-900"
                  >
                    <option value="">{t('cron.mb.noLocalClient')}</option>
                    {clients
                      .filter((c) => c.enabled)
                      .map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name}
                          {c.has_api_key === false ? t('cron.mb.legacyKeySuffix') : ''}
                        </option>
                      ))}
                  </select>
                  <p className="text-[11px] text-slate-400 mt-1">{t('cron.mb.localClientHint')}</p>
                </div>

                <div>
                  <div className="flex items-center justify-between mb-1.5">
                    <label htmlFor="mb-api-key" className="block font-medium text-slate-700 dark:text-slate-300">
                      API Key
                    </label>
                    {mbForm.has_api_key && (
                      <button
                        type="button"
                        onClick={() => setMbForm({ ...mbForm, api_key: '', has_api_key: false, clear_api_key: true })}
                        className="text-[11px] text-rose-600 dark:text-rose-400 hover:underline"
                      >
                        {t('common.clear')}
                      </button>
                    )}
                  </div>
                  <SecretField
                    id="mb-api-key"
                    hasSaved={mbForm.has_api_key}
                    value={mbForm.api_key}
                    onChange={(v) => setMbForm({ ...mbForm, api_key: v, clear_api_key: false })}
                    onReveal={async () => (await api.revealCronSecrets(editing.id)).api_key || ''}
                    placeholder={mbForm.has_api_key ? t('cron.mb.apiKeySavedPlaceholder') : t('cron.mb.apiKeyPlaceholder')}
                  />
                </div>

                <div>
                  <label htmlFor="mb-path" className="block font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                    {t('cron.mb.path')}
                  </label>
                  <input
                    id="mb-path"
                    value={mbForm.path}
                    onChange={(e) => setMbForm({ ...mbForm, path: e.target.value })}
                    placeholder="migration-backups"
                    className="w-full px-3 py-2 border rounded-lg"
                  />
                  <p className="text-[11px] text-slate-400 mt-1">{t('cron.mb.pathHint')}</p>
                </div>

                <div>
                  <label htmlFor="mb-targets-input" className="block font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                    {t('cron.mb.targets')}
                  </label>
                  <div className="flex flex-wrap items-center gap-1.5 mb-1.5">
                    {mbForm.targets.map((tag) => (
                      <span
                        key={tag}
                        className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md bg-indigo-50 dark:bg-indigo-500/15 border border-indigo-200 dark:border-indigo-500/30 text-indigo-700 dark:text-indigo-300 font-mono text-[11px]"
                      >
                        {tag}
                        <button
                          type="button"
                          onClick={() => removeTargetTag(tag)}
                          aria-label={t('cron.mb.removeTargetAria', { name: tag })}
                          className="text-indigo-400 hover:text-indigo-700 dark:hover:text-indigo-200"
                        >
                          <X className="w-3 h-3" />
                        </button>
                      </span>
                    ))}
                  </div>
                  <div className="flex gap-1.5">
                    <input
                      id="mb-targets-input"
                      value={targetInput}
                      onChange={(e) => setTargetInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                          e.preventDefault()
                          addTargetTag(targetInput)
                        }
                      }}
                      placeholder={t('cron.mb.targetPlaceholder')}
                      className="flex-1 px-3 py-1.5 border rounded-lg font-mono"
                    />
                    <button
                      type="button"
                      disabled={!targetInput.trim()}
                      onClick={() => addTargetTag(targetInput)}
                      className="px-2.5 py-1.5 border border-slate-200 dark:border-slate-700 rounded-lg text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-40 inline-flex items-center gap-1"
                    >
                      <Plus className="w-3.5 h-3.5" /> {t('common.add')}
                    </button>
                  </div>
                  {selectedLocalClient && selectedLocalClient.upstreams.length > 0 && (
                    <div className="flex flex-wrap gap-1.5 mt-2">
                      {selectedLocalClient.upstreams.map((u) => {
                        const name = u.upstream_name || u.upstream_id
                        const active = mbForm.targets.includes(name) || mbForm.targets.includes(u.upstream_id)
                        return (
                          <button
                            key={u.upstream_id}
                            type="button"
                            onClick={() => {
                              if (active) {
                                setMbForm({
                                  ...mbForm,
                                  targets: mbForm.targets.filter((tItem) => tItem !== name && tItem !== u.upstream_id),
                                })
                              } else {
                                setMbForm({ ...mbForm, targets: [...mbForm.targets, name] })
                              }
                            }}
                            className={`px-2 py-0.5 rounded-md border text-[11px] ${
                              active
                                ? 'bg-indigo-50 dark:bg-indigo-500/15 border-indigo-300 dark:border-indigo-500/40 text-indigo-700 dark:text-indigo-300'
                                : 'bg-slate-50 dark:bg-slate-800 border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700'
                            }`}
                          >
                            {u.upstream_name} ({u.upstream_type?.toUpperCase()})
                          </button>
                        )
                      })}
                    </div>
                  )}
                  <p className="text-[11px] text-slate-400 mt-1">{t('cron.mb.targetsHint')}</p>
                </div>

                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <div>
                    <label htmlFor="mb-rename" className="block font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                      {t('cron.mb.rename')}
                    </label>
                    <input
                      id="mb-rename"
                      value={mbForm.rename}
                      onChange={(e) => setMbForm({ ...mbForm, rename: e.target.value })}
                      placeholder={t('cron.mb.renamePlaceholder')}
                      className="w-full px-3 py-2 border rounded-lg"
                    />
                  </div>
                  <div>
                    <label htmlFor="mb-mode" className="block font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                      {t('cron.mb.mode')}
                    </label>
                    <select
                      id="mb-mode"
                      value={mbForm.mode}
                      onChange={(e) => setMbForm({ ...mbForm, mode: e.target.value as 'sync' | 'async' })}
                      className="w-full px-3 py-2 border rounded-lg bg-white dark:bg-slate-900"
                    >
                      <option value="sync">{t('cron.mb.modeSync')}</option>
                      <option value="async">{t('cron.mb.modeAsync')}</option>
                    </select>
                  </div>
                </div>

                <div>
                  <div className="flex items-center justify-between mb-1.5">
                    <label htmlFor="mb-passphrase" className="block font-medium text-slate-700 dark:text-slate-300">
                      {t('cron.mb.passphrase')}
                    </label>
                    {mbForm.has_passphrase && (
                      <button
                        type="button"
                        onClick={() =>
                          setMbForm({ ...mbForm, passphrase: '', has_passphrase: false, clear_passphrase: true })
                        }
                        className="text-[11px] text-rose-600 dark:text-rose-400 hover:underline"
                      >
                        {t('common.clear')}
                      </button>
                    )}
                  </div>
                  <SecretField
                    id="mb-passphrase"
                    hasSaved={mbForm.has_passphrase}
                    value={mbForm.passphrase}
                    onChange={(v) => setMbForm({ ...mbForm, passphrase: v, clear_passphrase: false })}
                    onReveal={async () => (await api.revealCronSecrets(editing.id)).passphrase || ''}
                    placeholder={mbForm.has_passphrase ? t('cron.mb.apiKeySavedPlaceholder') : t('cron.mb.passphrasePlaceholder')}
                  />
                  <p className="text-[11px] text-slate-400 mt-1">{t('cron.mb.passphraseHint')}</p>
                </div>

                <div className="pt-1 space-y-2">
                  <div className="font-medium text-slate-700 dark:text-slate-300">{t('cron.mb.exportScope')}</div>
                  <p className="text-[11px] text-slate-400">{t('cron.mb.exportScopeHint')}</p>
                  <div className="flex flex-wrap gap-x-4 gap-y-2 pt-0.5">
                    <label className="inline-flex items-center gap-1.5 text-slate-700 dark:text-slate-300">
                      <input
                        type="checkbox"
                        checked={mbForm.include_admins}
                        onChange={(e) => setMbForm({ ...mbForm, include_admins: e.target.checked })}
                      />
                      <span>{t('cron.mb.includeAdmins')}</span>
                    </label>
                    <label className="inline-flex items-center gap-1.5 text-slate-700 dark:text-slate-300">
                      <input
                        type="checkbox"
                        checked={mbForm.include_tasks}
                        onChange={(e) => setMbForm({ ...mbForm, include_tasks: e.target.checked })}
                      />
                      <span>{t('cron.mb.includeTasks')}</span>
                    </label>
                    <label className="inline-flex items-center gap-1.5 text-slate-700 dark:text-slate-300">
                      <input
                        type="checkbox"
                        checked={mbForm.include_audit_logs}
                        onChange={(e) => setMbForm({ ...mbForm, include_audit_logs: e.target.checked })}
                      />
                      <span>{t('cron.mb.includeAuditLogs')}</span>
                    </label>
                  </div>
                </div>
              </div>
            )}

            {params.kind === 'raw' && (
              <div className="pt-3 border-t border-slate-100 dark:border-slate-800">
                <label htmlFor="cron-raw-config" className="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1.5">
                  {t('cron.rawConfigLabel')}
                </label>
                <textarea
                  id="cron-raw-config"
                  rows={4}
                  value={params.data}
                  onChange={(e) => setParams({ kind: 'raw', data: e.target.value })}
                  className="w-full px-3 py-2 border rounded-lg font-mono text-xs"
                />
              </div>
            )}
          </form>
        </Drawer>
      )}

      {runsJob && (
        <Drawer
          title={t('cron.runsDrawerTitle', { name: jobName(runsJob) })}
          onClose={() => setRunsJob(null)}
          width="xl"
          headerExtra={
            <button
              type="button"
              disabled={runsLoading}
              onClick={() => loadRuns(runsJob)}
              className="p-1.5 rounded-lg text-slate-400 hover:text-slate-700 dark:hover:text-slate-200 hover:bg-slate-100 dark:hover:bg-slate-800 disabled:opacity-40"
              title={t('cron.refreshRuns')}
              aria-label={t('cron.refreshRuns')}
            >
              <RefreshCw className={`w-4 h-4 ${runsLoading ? 'animate-spin' : ''}`} />
            </button>
          }
        >
          {runsLoading && <LoadingState label={t('cron.loadingRuns')} />}
          {runsError && (
            <div role="alert" className="rounded-lg border border-rose-200 dark:border-rose-500/30 bg-rose-50 dark:bg-rose-500/15 px-3 py-2 text-xs text-rose-700 dark:text-rose-300">
              {runsError}
              <button type="button" className="ml-3 underline" onClick={() => loadRuns(runsJob)}>
                {t('common.retry')}
              </button>
            </div>
          )}
          {!runsLoading && !runsError && runs.length === 0 && (
            <div className="text-sm text-slate-400 py-8 text-center">{t('cron.noRuns')}</div>
          )}
          {!runsLoading && runs.length > 0 && (
            <div className="divide-y divide-slate-100 dark:divide-slate-800">
              {runs.map((r) => {
                const dur = formatDuration(r.started_at, r.finished_at)
                return (
                  <div key={r.id} className="py-3 first:pt-0">
                    <div className="flex flex-wrap items-center gap-2 text-xs">
                      <StatusBadge value={r.status} />
                      <span className="text-slate-800 dark:text-slate-100">{formatTime(r.started_at, lang)}</span>
                      <span className="text-slate-400">
                        {r.trigger_type === 'manual' ? t('cron.triggerManual', { by: r.triggered_by }) : t('cron.triggerScheduled')}
                      </span>
                      {dur && <span className="text-slate-400">{t('cron.runDuration', { dur })}</span>}
                      {r.affected_rows > 0 && <span className="text-slate-400">{t('cron.runAffected', { count: r.affected_rows })}</span>}
                    </div>
                    {r.output_log && (
                      <pre className="mt-2 rounded-lg bg-slate-50 dark:bg-slate-800/70 border border-slate-100 dark:border-slate-700 px-3 py-2 text-xs font-mono text-slate-700 dark:text-slate-200 whitespace-pre-wrap break-all">
                        {r.output_log}
                      </pre>
                    )}
                  </div>
                )
              })}
            </div>
          )}
        </Drawer>
      )}
    </div>
  )
}
