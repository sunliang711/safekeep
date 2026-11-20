import React, { useEffect, useMemo, useRef, useState } from 'react'
import { Loader2, Plus, RefreshCw, X } from 'lucide-react'
import { LoadingState } from '../components/Feedback'
import { Drawer } from '../components/Drawer'
import { SecretField } from '../components/SecretField'
import { api, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import { describeCron } from '../utils/cron'
import type { Client, CronJob, CronJobRun } from '../types'

interface PresetOption {
  label: string
  value: string
}

const BASE_PRESETS: PresetOption[] = [
  { label: '每小时', value: '0 * * * *' },
  { label: '每天', value: '0 4 * * *' },
  { label: '每周', value: '0 5 * * 1' },
  { label: '每月', value: '0 4 1 * *' },
  { label: '自定义', value: 'custom' },
]

function getPlanPresets(jobKey?: string): PresetOption[] {
  let extra: PresetOption[] = []
  switch (jobKey) {
    case 'cleanup_audit_logs':
      extra = [{ label: '每天 02:00', value: '0 2 * * *' }]
      break
    case 'cleanup_task_records':
      extra = [{ label: '每天 03:00', value: '0 3 * * *' }]
      break
    case 'cleanup_orphan_spool':
      extra = [
        { label: '每 30 分钟', value: '*/30 * * * *' },
        { label: '每 6 小时', value: '0 */6 * * *' },
      ]
      break
    case 'upstream_health_check':
      extra = [
        { label: '每 5 分钟', value: '*/5 * * * *' },
        { label: '每 15 分钟', value: '*/15 * * * *' },
        { label: '每 30 分钟', value: '*/30 * * * *' },
      ]
      break
    case 'dropbox_token_refresh':
      extra = [
        { label: '每 2 小时', value: '0 */2 * * *' },
        { label: '每 4 小时', value: '0 */4 * * *' },
        { label: '每 6 小时', value: '0 */6 * * *' },
      ]
      break
    case 'migration_backup':
      extra = [
        { label: '每天 05:00', value: '0 5 * * *' },
        { label: '每月 1 日和 15 日', value: '30 4 1,15 * *' },
      ]
      break
  }
  const seen = new Set(extra.map((item) => item.value))
  return [...extra, ...BASE_PRESETS.filter((item) => !seen.has(item.value))]
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
      checked ? 'bg-indigo-600' : 'bg-slate-200 hover:bg-slate-300'
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
  const toast = useToast()
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
  const [preview, setPreview] = useState<string[]>([])
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

  const load = async () => {
    try {
      const [jobsRes, clientsRes] = await Promise.all([api.listCronJobs(), api.listClients()])
      setJobs(jobsRes.items || [])
      setClients(clientsRes.items || [])
    } catch (err: any) {
      toast.error(`加载定时任务失败: ${err.message}`)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const planPresets = useMemo(() => getPlanPresets(editing?.job_key), [editing?.job_key])

  const openEdit = (job: CronJob, overrideEnabled?: boolean) => {
    const presets = getPlanPresets(job.job_key)
    const matched = presets.find((item) => item.value === job.cron_expr)?.value ?? 'custom'
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
    setPreview((job.next_runs || []).map((t) => formatTime(t)))
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
      setPreview([])
      setPreviewError('请输入 cron 表达式')
      return
    }
    const seq = ++previewSeq.current
    const timer = window.setTimeout(async () => {
      setPreviewLoading(true)
      try {
        const res = await api.previewCronSchedule(expr)
        if (previewSeq.current !== seq) return
        setPreview((res.next_runs || []).map((t) => formatTime(t)))
        setPreviewError('')
      } catch (err: any) {
        if (previewSeq.current !== seq) return
        setPreview([])
        setPreviewError(err.message || 'cron 表达式无效')
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

  const closeEdit = () => {
    if (saving) return
    if (isDirty && !confirm('定时任务尚未保存，确定放弃修改吗？')) return
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
        toast.info('启用自动备份前，请先在「编辑」中选择本机客户或填写目标 API Key')
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
      toast.error(err.message || '更新状态失败')
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
      setFormError('超时时间需为 1–86400 之间的整数（秒）')
      return
    }

    let cfg: any
    switch (params.kind) {
      case 'cleanup_audit_logs':
      case 'cleanup_task_records': {
        const days = Number(params.data.retention_days)
        if (!Number.isInteger(days) || days < 1) {
          setFormError('保留天数需为大于等于 1 的整数')
          return
        }
        cfg = { retention_days: days }
        break
      }
      case 'cleanup_orphan_spool': {
        const maxAge = Number(params.data.max_age_hours)
        const failedHours = Number(params.data.failed_spool_retention_hours)
        if (!Number.isInteger(maxAge) || maxAge < 1 || !Number.isInteger(failedHours) || failedHours < 1) {
          setFormError('存活与保留时间需为大于等于 1 的整数（小时）')
          return
        }
        cfg = { max_age_hours: maxAge, failed_spool_retention_hours: failedHours }
        break
      }
      case 'upstream_health_check': {
        const probeTimeout = Number(params.data.probe_timeout_sec)
        if (!Number.isInteger(probeTimeout) || probeTimeout < 1 || probeTimeout > 120) {
          setFormError('每次巡检超时需为 1–120 之间的整数（秒）')
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
          setFormError('加密口令至少需要 6 个字符（或留空不设口令）')
          return
        }
        if (enabled && !mb.has_api_key && !mb.api_key.trim() && !mb.client_id) {
          setFormError('启用自动备份前，请先选择本机客户或填写目标 API Key')
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
          setFormError(`参数 JSON 格式有误: ${err.message}`)
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
      toast.success('已保存')
      await load()
    } catch (err: any) {
      setFormError(err.message || '保存定时任务失败')
    } finally {
      setSaving(false)
    }
  }

  const handleRunNow = async (job: CronJob) => {
    if (runningId) return
    setRunningId(job.id)
    toast.info(`正在执行「${job.name}」…`)
    try {
      const run = await api.runCronJob(job.id)
      const detail = run.output_log ? `（${run.output_log}）` : ''
      if (run.status === 'succeeded') {
        toast.success(`「${job.name}」执行完成${detail}`)
      } else if (run.status === 'skipped') {
        toast.info(`「${job.name}」已跳过${detail}`)
      } else {
        toast.error(`「${job.name}」执行失败${detail}`)
      }
      await load()
      if (runsJob?.id === job.id) {
        await loadRuns(job)
      }
    } catch (err: any) {
      toast.error(`执行失败: ${err.message}`)
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
      setRunsError(err.message || '加载执行记录失败')
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
    setMbForm({ ...mbForm, targets: mbForm.targets.filter((t) => t !== name) })
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-slate-900">任务计划</h1>
        <p className="text-sm text-slate-500">管理日志清理、缓冲盘垃圾回收、上游连通巡检、Token 预刷新与环境迁移包自动备份等周期任务</p>
      </div>

      <div className="bg-white rounded-xl border border-slate-200 overflow-x-auto">
        <table className="cron-table w-full text-sm">
          <thead className="bg-slate-50 text-xs text-slate-500 border-b border-slate-200/80">
            <tr>
              <th className="text-left px-4 py-3">任务</th>
              <th className="text-left px-4 py-3">计划</th>
              <th className="text-left px-4 py-3">下次执行</th>
              <th className="text-left px-4 py-3">最近一次</th>
              <th className="text-left px-4 py-3">启用</th>
              <th className="text-right px-4 py-3">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {loading && (
              <tr>
                <td colSpan={6}>
                  <LoadingState label="正在加载任务计划…" />
                </td>
              </tr>
            )}
            {!loading && jobs.length === 0 && (
              <tr>
                <td colSpan={6} className="p-8 text-center text-slate-500">
                  没有可用的定时任务
                </td>
              </tr>
            )}
            {jobs.map((j) => {
              const scheduleText = describeCron(j.cron_expr)
              const nextTime = j.enabled ? j.next_runs?.[0] || j.next_run_at : undefined
              const isRunning = runningId === j.id
              return (
                <tr key={j.id}>
                  <td className="px-4 py-3.5 align-top">
                    <div className="font-medium text-slate-900">{j.name}</div>
                    <div className="text-xs text-slate-400 mt-0.5 leading-relaxed">{j.description}</div>
                  </td>
                  <td className="px-4 py-3.5 align-top">
                    {scheduleText && <div className="text-xs text-slate-700 mb-1">{scheduleText}</div>}
                    <code className="font-mono text-[11px] bg-slate-100 text-slate-700 px-1.5 py-0.5 rounded inline-block">
                      {j.cron_expr}
                    </code>
                  </td>
                  <td className="px-4 py-3.5 align-top text-xs text-slate-600 whitespace-nowrap">
                    {nextTime ? formatTime(nextTime) : <span className="text-slate-400">—</span>}
                  </td>
                  <td className="px-4 py-3.5 align-top text-xs">
                    {!j.last_run_at ? (
                      <span className="text-slate-400">—</span>
                    ) : (
                      <div className="space-y-1">
                        <div className="flex items-center gap-2 whitespace-nowrap">
                          <StatusBadge value={j.last_status} />
                          <span className="text-slate-600">{formatTime(j.last_run_at)}</span>
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
                        label={`切换 ${j.name} 启用状态`}
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
                        className="px-2.5 py-1 text-xs rounded-lg border border-slate-200 bg-white text-slate-700 hover:bg-slate-50 hover:border-slate-300 disabled:opacity-45 inline-flex items-center gap-1"
                      >
                        {isRunning && <Loader2 className="w-3 h-3 animate-spin" />}
                        {isRunning ? '执行中' : '立即执行'}
                      </button>
                      <button
                        type="button"
                        onClick={() => openEdit(j)}
                        className="px-2.5 py-1 text-xs rounded-lg border border-slate-200 bg-white text-slate-700 hover:bg-slate-50 hover:border-slate-300"
                      >
                        编辑
                      </button>
                      <button
                        type="button"
                        onClick={() => openRuns(j)}
                        className="px-2.5 py-1 text-xs rounded-lg border border-slate-200 bg-white text-slate-700 hover:bg-slate-50 hover:border-slate-300"
                      >
                        记录
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
          title={editing.name}
          subtitle={editing.description}
          onClose={closeEdit}
          busy={saving}
          width={editing.job_key === 'migration_backup' ? 'lg' : 'md'}
          footer={
            <>
              <button
                type="button"
                disabled={saving}
                onClick={closeEdit}
                className="px-3.5 py-1.5 text-xs font-medium rounded-lg border border-slate-200 bg-white text-slate-700 hover:bg-slate-50 disabled:opacity-50"
              >
                取消
              </button>
              <button
                type="button"
                disabled={saving || Boolean(previewError) || !isDirty}
                onClick={() => handleSave()}
                className="px-4 py-1.5 text-xs font-medium rounded-lg bg-indigo-600 hover:bg-indigo-700 text-white disabled:opacity-45"
              >
                {saving ? '保存中…' : '保存'}
              </button>
            </>
          }
        >
          <form onSubmit={handleSave} className="space-y-4 text-sm">
            {formError && (
              <div role="alert" className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700">
                {formError}
              </div>
            )}

            <div>
              <label htmlFor="cron-preset" className="block text-xs font-medium text-slate-700 mb-1.5">
                计划
              </label>
              <select
                id="cron-preset"
                value={preset}
                onChange={(e) => handlePresetChange(e.target.value)}
                className="w-full px-3 py-2 text-xs border rounded-lg bg-white"
              >
                {planPresets.map((opt) => (
                  <option key={opt.value} value={opt.value}>
                    {opt.value === 'custom' ? opt.label : `${opt.label}（${opt.value}）`}
                  </option>
                ))}
              </select>
            </div>

            {preset === 'custom' && (
              <div>
                <label htmlFor="cron-expr" className="block text-xs font-medium text-slate-700 mb-1.5">
                  cron 表达式
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
                  五段式：分 时 日 月 周；默认 <span className="font-mono">{editing.default_schedule || editing.cron_expr}</span>
                </p>
              </div>
            )}

            <div>
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-xs font-medium text-slate-700">未来执行时间</span>
                {previewLoading && <Loader2 className="w-3 h-3 animate-spin text-slate-400" />}
              </div>
              {previewError ? (
                <div className="text-xs text-rose-600">{previewError}</div>
              ) : (
                <div className="rounded-lg bg-slate-50 border border-slate-200/70 px-3 py-2 space-y-1">
                  {preview.length > 0 ? (
                    preview.map((time) => (
                      <div key={time} className="text-xs font-mono text-slate-500">
                        {time}
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
                <label htmlFor="cron-timeout" className="block text-xs font-medium text-slate-700 mb-1.5">
                  超时（秒）
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
                <span className="block text-xs font-medium text-slate-700 mb-1.5">启用状态</span>
                <div className="h-[34px] flex items-center gap-2.5">
                  <Switch checked={enabled} label="启用调度" onChange={setEnabled} />
                  <span className="text-xs text-slate-600">{enabled ? '已启用' : '已暂停'}</span>
                </div>
              </div>
            </div>

            {params.kind === 'cleanup_audit_logs' && (
              <div className="pt-3 border-t border-slate-100">
                <label htmlFor="param-retention-days" className="block text-xs font-medium text-slate-700 mb-1.5">
                  保留天数
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
                <p className="text-[11px] text-slate-400 mt-1">
                  超过该天数的历史审计日志将被定期删除；默认 90 天
                </p>
              </div>
            )}

            {params.kind === 'cleanup_task_records' && (
              <div className="pt-3 border-t border-slate-100">
                <label htmlFor="param-task-retention-days" className="block text-xs font-medium text-slate-700 mb-1.5">
                  保留天数
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
                <p className="text-[11px] text-slate-400 mt-1">
                  超过该天数且临时文件已清除的历史备份任务与定时任务执行记录将被删除；默认 180 天
                </p>
              </div>
            )}

            {params.kind === 'cleanup_orphan_spool' && (
              <div className="pt-3 border-t border-slate-100 space-y-3">
                <div>
                  <label htmlFor="param-max-age-hours" className="block text-xs font-medium text-slate-700 mb-1.5">
                    孤儿文件最大存活时间（小时）
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
                  <p className="text-[11px] text-slate-400 mt-1">
                    未被活跃任务引用的 .spool 残留文件超过此时长后清理；默认 24 小时
                  </p>
                </div>
                <div>
                  <label htmlFor="param-failed-hours" className="block text-xs font-medium text-slate-700 mb-1.5">
                    失败任务临时文件保留时间（小时）
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
                  <p className="text-[11px] text-slate-400 mt-1">
                    永久失败任务的临时缓冲文件保留时长，超时后自动清理释放磁盘；默认 72 小时
                  </p>
                </div>
              </div>
            )}

            {params.kind === 'upstream_health_check' && (
              <div className="pt-3 border-t border-slate-100">
                <label htmlFor="param-probe-timeout" className="block text-xs font-medium text-slate-700 mb-1.5">
                  每次巡检超时（秒）
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
                <p className="text-[11px] text-slate-400 mt-1">
                  每个已启用上游（含代理）的连通性探测超时；任务总超时需覆盖全部上游巡检时间；默认 20 秒
                </p>
              </div>
            )}

            {mbForm && (
              <div className="pt-3 border-t border-slate-100 space-y-3.5 text-xs">
                <div>
                  <label htmlFor="mb-base-url" className="block font-medium text-slate-700 mb-1.5">
                    SafeKeep 地址
                  </label>
                  <input
                    id="mb-base-url"
                    value={mbForm.api_base_url}
                    onChange={(e) => setMbForm({ ...mbForm, api_base_url: e.target.value })}
                    placeholder="https://backup.example.com"
                    className="w-full px-3 py-2 border rounded-lg font-mono"
                  />
                  <p className="text-[11px] text-slate-400 mt-1">
                    可选，留空则通过本机 HTTP 请求当前 SafeKeep；外部实例填服务根地址
                  </p>
                </div>

                <div>
                  <label htmlFor="mb-client" className="block font-medium text-slate-700 mb-1.5">
                    本机客户
                  </label>
                  <select
                    id="mb-client"
                    value={mbForm.client_id}
                    onChange={(e) => setMbForm({ ...mbForm, client_id: e.target.value })}
                    className="w-full px-3 py-2 border rounded-lg bg-white"
                  >
                    <option value="">不使用本机客户（改用下方手动填写的 API Key）</option>
                    {clients
                      .filter((c) => c.enabled)
                      .map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name}
                          {c.has_api_key === false ? '（旧 Key，需轮换或手动填）' : ''}
                        </option>
                      ))}
                  </select>
                  <p className="text-[11px] text-slate-400 mt-1">
                    快捷使用所选本机客户的 API Key；若填写下方 API Key 则优先使用手动值
                  </p>
                </div>

                <div>
                  <div className="flex items-center justify-between mb-1.5">
                    <label htmlFor="mb-api-key" className="block font-medium text-slate-700">
                      API Key
                    </label>
                    {mbForm.has_api_key && (
                      <button
                        type="button"
                        onClick={() => setMbForm({ ...mbForm, api_key: '', has_api_key: false, clear_api_key: true })}
                        className="text-[11px] text-rose-600 hover:underline"
                      >
                        清除
                      </button>
                    )}
                  </div>
                  <SecretField
                    id="mb-api-key"
                    hasSaved={mbForm.has_api_key}
                    value={mbForm.api_key}
                    onChange={(v) => setMbForm({ ...mbForm, api_key: v, clear_api_key: false })}
                    onReveal={async () => (await api.revealCronSecrets(editing.id)).api_key || ''}
                    placeholder={mbForm.has_api_key ? '留空保持不变，点击查看或修改' : 'sk_…（外部实例必填，可选覆盖本机客户）'}
                  />
                </div>

                <div>
                  <label htmlFor="mb-path" className="block font-medium text-slate-700 mb-1.5">
                    存放子路径
                  </label>
                  <input
                    id="mb-path"
                    value={mbForm.path}
                    onChange={(e) => setMbForm({ ...mbForm, path: e.target.value })}
                    placeholder="migration-backups"
                    className="w-full px-3 py-2 border rounded-lg"
                  />
                  <p className="text-[11px] text-slate-400 mt-1">可选，例如 migration-backups</p>
                </div>

                <div>
                  <label htmlFor="mb-targets-input" className="block font-medium text-slate-700 mb-1.5">
                    上游
                  </label>
                  <div className="flex flex-wrap items-center gap-1.5 mb-1.5">
                    {mbForm.targets.map((tag) => (
                      <span
                        key={tag}
                        className="inline-flex items-center gap-1 px-2 py-0.5 rounded-md bg-indigo-50 border border-indigo-200 text-indigo-700 font-mono text-[11px]"
                      >
                        {tag}
                        <button
                          type="button"
                          onClick={() => removeTargetTag(tag)}
                          aria-label={`移除上游 ${tag}`}
                          className="text-indigo-400 hover:text-indigo-700"
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
                      placeholder="输入上游名称后按回车添加"
                      className="flex-1 px-3 py-1.5 border rounded-lg font-mono"
                    />
                    <button
                      type="button"
                      disabled={!targetInput.trim()}
                      onClick={() => addTargetTag(targetInput)}
                      className="px-2.5 py-1.5 border border-slate-200 rounded-lg text-slate-600 hover:bg-slate-50 disabled:opacity-40 inline-flex items-center gap-1"
                    >
                      <Plus className="w-3.5 h-3.5" /> 添加
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
                                  targets: mbForm.targets.filter((t) => t !== name && t !== u.upstream_id),
                                })
                              } else {
                                setMbForm({ ...mbForm, targets: [...mbForm.targets, name] })
                              }
                            }}
                            className={`px-2 py-0.5 rounded-md border text-[11px] ${
                              active
                                ? 'bg-indigo-50 border-indigo-300 text-indigo-700'
                                : 'bg-slate-50 border-slate-200 text-slate-600 hover:bg-slate-100'
                            }`}
                          >
                            {u.upstream_name} ({u.upstream_type?.toUpperCase()})
                          </button>
                        )
                      })}
                    </div>
                  )}
                  <p className="text-[11px] text-slate-400 mt-1">
                    可选，填写目标 SafeKeep 中的上游名称；留空时使用该 API Key 的默认上游组
                  </p>
                </div>

                <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                  <div>
                    <label htmlFor="mb-rename" className="block font-medium text-slate-700 mb-1.5">
                      固定重命名文件名
                    </label>
                    <input
                      id="mb-rename"
                      value={mbForm.rename}
                      onChange={(e) => setMbForm({ ...mbForm, rename: e.target.value })}
                      placeholder="留空按时间戳命名"
                      className="w-full px-3 py-2 border rounded-lg"
                    />
                  </div>
                  <div>
                    <label htmlFor="mb-mode" className="block font-medium text-slate-700 mb-1.5">
                      上传模式
                    </label>
                    <select
                      id="mb-mode"
                      value={mbForm.mode}
                      onChange={(e) => setMbForm({ ...mbForm, mode: e.target.value as 'sync' | 'async' })}
                      className="w-full px-3 py-2 border rounded-lg bg-white"
                    >
                      <option value="sync">sync（同步等待完成）</option>
                      <option value="async">async（异步队列返回）</option>
                    </select>
                  </div>
                </div>

                <div>
                  <div className="flex items-center justify-between mb-1.5">
                    <label htmlFor="mb-passphrase" className="block font-medium text-slate-700">
                      加密口令
                    </label>
                    {mbForm.has_passphrase && (
                      <button
                        type="button"
                        onClick={() =>
                          setMbForm({ ...mbForm, passphrase: '', has_passphrase: false, clear_passphrase: true })
                        }
                        className="text-[11px] text-rose-600 hover:underline"
                      >
                        清除
                      </button>
                    )}
                  </div>
                  <SecretField
                    id="mb-passphrase"
                    hasSaved={mbForm.has_passphrase}
                    value={mbForm.passphrase}
                    onChange={(v) => setMbForm({ ...mbForm, passphrase: v, clear_passphrase: false })}
                    onReveal={async () => (await api.revealCronSecrets(editing.id)).passphrase || ''}
                    placeholder={mbForm.has_passphrase ? '留空保持不变，点击查看或修改' : '留空不设口令；如设置至少 6 位'}
                  />
                  <p className="text-[11px] text-slate-400 mt-1">
                    上传的迁移包用此口令加密，至少 6 个字符；导入时需要输入，请另行保存。留空时不设口令
                  </p>
                </div>

                <div className="pt-1 space-y-2">
                  <div className="font-medium text-slate-700">导出范围</div>
                  <p className="text-[11px] text-slate-400">默认已包含系统设置、上游与代理、客户与 Key、定时任务</p>
                  <div className="flex flex-wrap gap-x-4 gap-y-2 pt-0.5">
                    <label className="inline-flex items-center gap-1.5 text-slate-700">
                      <input
                        type="checkbox"
                        checked={mbForm.include_admins}
                        onChange={(e) => setMbForm({ ...mbForm, include_admins: e.target.checked })}
                      />
                      <span>包含管理员账号</span>
                    </label>
                    <label className="inline-flex items-center gap-1.5 text-slate-700">
                      <input
                        type="checkbox"
                        checked={mbForm.include_tasks}
                        onChange={(e) => setMbForm({ ...mbForm, include_tasks: e.target.checked })}
                      />
                      <span>包含历史备份索引</span>
                    </label>
                    <label className="inline-flex items-center gap-1.5 text-slate-700">
                      <input
                        type="checkbox"
                        checked={mbForm.include_audit_logs}
                        onChange={(e) => setMbForm({ ...mbForm, include_audit_logs: e.target.checked })}
                      />
                      <span>包含历史审计日志</span>
                    </label>
                  </div>
                </div>
              </div>
            )}

            {params.kind === 'raw' && (
              <div className="pt-3 border-t border-slate-100">
                <label htmlFor="cron-raw-config" className="block text-xs font-medium text-slate-700 mb-1.5">
                  任务参数 JSON
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
          title={`${runsJob.name}：执行记录`}
          onClose={() => setRunsJob(null)}
          width="xl"
          headerExtra={
            <button
              type="button"
              disabled={runsLoading}
              onClick={() => loadRuns(runsJob)}
              className="p-1.5 rounded-lg text-slate-400 hover:text-slate-700 hover:bg-slate-100 disabled:opacity-40"
              title="刷新记录"
              aria-label="刷新记录"
            >
              <RefreshCw className={`w-4 h-4 ${runsLoading ? 'animate-spin' : ''}`} />
            </button>
          }
        >
          {runsLoading && <LoadingState label="正在加载执行记录…" />}
          {runsError && (
            <div role="alert" className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700">
              {runsError}
              <button type="button" className="ml-3 underline" onClick={() => loadRuns(runsJob)}>
                重试
              </button>
            </div>
          )}
          {!runsLoading && !runsError && runs.length === 0 && (
            <div className="text-sm text-slate-400 py-8 text-center">还没有执行记录</div>
          )}
          {!runsLoading && runs.length > 0 && (
            <div className="divide-y divide-slate-100">
              {runs.map((r) => {
                const dur = formatDuration(r.started_at, r.finished_at)
                return (
                  <div key={r.id} className="py-3 first:pt-0">
                    <div className="flex flex-wrap items-center gap-2 text-xs">
                      <StatusBadge value={r.status} />
                      <span className="text-slate-800">{formatTime(r.started_at)}</span>
                      <span className="text-slate-400">
                        {r.trigger_type === 'manual' ? `手动（${r.triggered_by}）` : '计划'}
                      </span>
                      {dur && <span className="text-slate-400">耗时 {dur}</span>}
                      {r.affected_rows > 0 && <span className="text-slate-400">影响 {r.affected_rows} 项</span>}
                    </div>
                    {r.output_log && (
                      <pre className="mt-2 rounded-lg bg-slate-50 border border-slate-100 px-3 py-2 text-xs font-mono text-slate-700 whitespace-pre-wrap break-all">
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
