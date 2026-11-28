import { useReadOnly } from '../contexts/PermissionContext'
import React, { useEffect, useRef, useState } from 'react'
import { CheckCircle2, Download, RotateCcw, Search, Upload, XCircle } from 'lucide-react'
import { EmptyState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { AuditSelect } from '../components/AuditFilters'
import { api, formatBytes, formatTime, isReauthenticating } from '../api'
import { StatusBadge } from '../components/Badge'
import { useI18n } from '../contexts/I18nContext'
import type { NavTab } from '../components/Layout'
import type { BackupTask, Client, Upstream } from '../types'

interface TasksPageProps {
  onNavigate?: (tab: NavTab) => void
}

export const TasksPage: React.FC<TasksPageProps> = ({ onNavigate }) => {
  const readOnly = useReadOnly()
  const { t, lang } = useI18n()
  const [tasks, setTasks] = useState<BackupTask[]>([])
  const [loading, setLoading] = useState(true)
  const requestId = useRef(0)
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const limit = 20
  const [status, setStatus] = useState('')
  const [mode, setMode] = useState('')
  const [q, setQ] = useState('')
  const [pageErr, setPageErr] = useState('')
  const [uploadMetaLoading, setUploadMetaLoading] = useState(false)
  const [retryingId, setRetryingId] = useState<string | null>(null)
  const [selected, setSelected] = useState<BackupTask | null>(null)

  // Web Upload modal state (mirrors client API behavior)
  const [uploadOpen, setUploadOpen] = useState(false)
  const [clients, setClients] = useState<Client[]>([])
  const [allClientNames, setAllClientNames] = useState<string[]>([])
  const [enabledUpstreams, setEnabledUpstreams] = useState<Upstream[]>([])
  const [creatingDefaultClient, setCreatingDefaultClient] = useState(false)
  const [upClientId, setUpClientId] = useState('')
  const [upMode, setUpMode] = useState<'sync' | 'async'>('sync')
  const [upSubpath, setUpSubpath] = useState('')
  const [upRename, setUpRename] = useState('')
  const [upTargets, setUpTargets] = useState<string[]>([])
  const [upDedup, setUpDedup] = useState(true)
  const [upMetadata, setUpMetadata] = useState('')
  const [upFile, setUpFile] = useState<File | null>(null)
  const [fileInputKey, setFileInputKey] = useState(0)
  const [uploading, setUploading] = useState(false)
  const [uploadErr, setUploadErr] = useState('')
  const [uploadResult, setUploadResult] = useState<{ task: BackupTask; timedOut?: boolean } | null>(null)

  const load = async (targetOffset = offset, filters = { status, mode, q }) => {
    const currentRequest = ++requestId.current
    setLoading(true)
    setPageErr('')
    try {
      const params: Record<string, string> = { limit: String(limit), offset: String(targetOffset) }
      if (filters.status) params.status = filters.status
      if (filters.mode) params.mode = filters.mode
      if (filters.q) params.q = filters.q
      const res = await api.listTasks(params)
      if (currentRequest !== requestId.current) return
      setTasks(res.items || [])
      setTotal(res.total || 0)
    } catch (err: any) {
      if (currentRequest === requestId.current) setPageErr(err.message || t('tasks.loadError'))
    } finally {
      if (currentRequest === requestId.current) setLoading(false)
    }
  }

  useEffect(() => {
    setOffset(0)
    load(0)
  }, [status, mode])

  const loadUploadMeta = async () => {
    const [cRes, uRes] = await Promise.all([api.listClients(), api.listUpstreams()])
    const allClients = cRes.items || []
    setAllClientNames(allClients.map((c) => c.name))
    const enabledClients = allClients.filter((c) => c.enabled)
    setClients(enabledClients)
    setEnabledUpstreams((uRes.items || []).filter((u) => u.enabled))
    if (enabledClients.length > 0) {
      const first = enabledClients[0]
      setUpClientId(first.id)
      setUpMode(first.default_mode || 'sync')
      setUpTargets((first.upstreams || []).filter((u) => u.is_default).map((u) => u.upstream_id))
    } else {
      setUpClientId('')
      setUpTargets([])
    }
  }

  const openUploadModal = async () => {
    setUploadErr('')
    setUploadResult(null)
    setUpFile(null)
    setUpRename('')
    setUploadMetaLoading(true)
    setUploadOpen(true)
    try {
      await loadUploadMeta()
    } catch (err: any) {
      setUploadErr(err.message || t('tasks.loadMetaError'))
    } finally {
      setUploadMetaLoading(false)
    }
  }

  const handleCreateDefaultClient = async () => {
    if (enabledUpstreams.length === 0) return
    setUploadErr('')
    setCreatingDefaultClient(true)
    try {
      const existing = new Set(allClientNames)
      let name = 'web-admin'
      let idx = 2
      while (existing.has(name)) {
        name = `web-admin-${idx++}`
      }
      await api.createClient({
        name,
        description: t('tasks.createDefaultClientDesc'),
        enabled: true,
        default_mode: 'sync',
        path_prefix: '',
        max_file_size_bytes: 0,
        allowed_mime_types: [],
        rate_limit_rpm: 0,
        webhook_url: '',
        webhook_secret: '',
        expires_at: null,
        upstreams: enabledUpstreams.map((u, i) => ({
          upstream_id: u.id,
          is_default: true,
          priority: (i + 1) * 10,
        })),
      })
      await loadUploadMeta()
    } catch (err: any) {
      setUploadErr(err.message || t('tasks.createDefaultClientError'))
    } finally {
      setCreatingDefaultClient(false)
    }
  }

  const onSelectClient = (cid: string) => {
    setUpClientId(cid)
    const found = clients.find((c) => c.id === cid)
    if (found) {
      setUpMode(found.default_mode || 'sync')
      setUpTargets((found.upstreams || []).filter((u) => u.is_default).map((u) => u.upstream_id))
    }
  }

  // Poll async / timed-out task until terminal state
  useEffect(() => {
    if (!uploadOpen || !uploadResult) return
    const st = uploadResult.task.status
    if (st !== 'pending' && st !== 'running') return
    const timer = setInterval(async () => {
      if (isReauthenticating()) return
      try {
        const latest = await api.getTask(uploadResult.task.id)
        setUploadResult((prev) => (prev ? { ...prev, task: latest } : null))
        if (latest.status !== 'pending' && latest.status !== 'running') {
          load()
        }
      } catch {
        // ignore transient poll errors
      }
    }, 1500)
    return () => clearInterval(timer)
  }, [uploadOpen, uploadResult?.task.id, uploadResult?.task.status])

  const handleRetry = async (id: string) => {
    if (retryingId) return
    setRetryingId(id)
    try {
      const updated = await api.retryTask(id)
      setSelected(updated)
      await load()
    } catch (err: any) {
      setPageErr(err.message || t('tasks.retryError'))
    } finally {
      setRetryingId(null)
    }
  }

  const handleUpload = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!upFile || !upClientId) return
    setUploadErr('')
    setUploadResult(null)
    setUploading(true)
    const fd = new FormData()
    fd.append('file', upFile)
    if (upMetadata.trim()) {
      fd.append('metadata', upMetadata.trim())
    }
    const query: Record<string, string> = {
      mode: upMode,
      dedup: upDedup ? 'true' : 'false',
    }
    if (upSubpath.trim()) query.path = upSubpath.trim()
    if (upRename.trim()) query.rename = upRename.trim()
    if (upTargets.length > 0) query.targets = upTargets.join(',')

    try {
      const res = await api.adminUploadBackup(upClientId, fd, query)
      setUploadResult({ task: res.task, timedOut: res.timed_out_to_async })
      setUpFile(null)
      setUpRename('')
      setFileInputKey((k) => k + 1)
      await load(0)
      setOffset(0)
    } catch (err: any) {
      setUploadErr(err.message || t('tasks.uploadFailed'))
    } finally {
      setUploading(false)
    }
  }

  const resetFilters = () => {
    setQ('')
    setStatus('')
    setMode('')
    setOffset(0)
    load(0, { status: '', mode: '', q: '' })
  }

  const activeClient = clients.find((c) => c.id === upClientId)

  const webhookLabels: Record<string, string> = {
    none: t('tasks.detail.webhookNone'),
    pending: t('tasks.detail.webhookPending'),
    sent: t('tasks.detail.webhookSent'),
    failed: t('tasks.detail.webhookFailed'),
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">{t('tasks.title')}</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">{t('tasks.subtitle', { total })}</p>
        </div>
        <button disabled={readOnly}
          onClick={openUploadModal}
          className="inline-flex items-center gap-1.5 px-4 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm font-medium"
        >
          <Upload className="w-4 h-4" /> {t('tasks.uploadBtn')}
        </button>
      </div>

      <div className="filter-bar audit-filter-bar flex flex-wrap gap-3 bg-white dark:bg-slate-900 p-4 rounded-xl border border-slate-200 dark:border-slate-800">
        <div className="audit-filter-search flex items-center gap-2 flex-1 min-w-[200px] border rounded-lg px-3 py-1.5">
          <Search className="w-4 h-4 text-slate-400" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                setOffset(0)
                load(0)
              }
            }}
            aria-label={t('tasks.searchAria')}
            placeholder={t('tasks.searchPlaceholder')}
            className="w-full text-sm focus:outline-none bg-transparent"
          />
        </div>
        <AuditSelect label={t('tasks.filterStatusAria')} value={status} onChange={setStatus} options={[
          { value: '', label: t('tasks.filterStatusAll') },
          { value: 'succeeded', label: t('tasks.filterStatusSucceeded') },
          { value: 'pending', label: t('tasks.filterStatusPending') },
          { value: 'running', label: t('tasks.filterStatusRunning') },
          { value: 'partial_success', label: t('tasks.filterStatusPartial') },
          { value: 'failed', label: t('tasks.filterStatusFailed') },
        ]} />
        <AuditSelect label={t('tasks.filterModeAria')} value={mode} onChange={setMode} options={[
          { value: '', label: t('tasks.filterModeAll') },
          { value: 'sync', label: t('status.sync') },
          { value: 'async', label: t('status.async') },
        ]} />
        <button
          onClick={() => {
            setOffset(0)
            load(0)
          }}
          disabled={loading}
          className="audit-filter-submit px-4 py-1.5 bg-slate-900 dark:bg-indigo-600 hover:bg-slate-800 dark:hover:bg-indigo-500 text-white rounded-lg text-sm"
        >
          {loading ? t('common.searching') : t('common.search')}
        </button>
        {(q || status || mode) && (
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
        <table aria-busy={loading} className="tasks-table w-full text-sm">
          <thead className="bg-slate-50 dark:bg-slate-950/60 text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className="text-left px-4 py-3">{t('tasks.colFile')}</th>
              <th className="text-left px-4 py-3">{t('tasks.colClient')}</th>
              <th className="text-left px-4 py-3">{t('tasks.colMode')}</th>
              <th className="text-left px-4 py-3">{t('tasks.colSize')}</th>
              <th className="text-left px-4 py-3">{t('tasks.colProgress')}</th>
              <th className="text-left px-4 py-3">{t('tasks.colStatus')}</th>
              <th className="text-left px-4 py-3">{t('tasks.colCreatedAt')}</th>
              <th className="text-right px-4 py-3">{t('tasks.colActions')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {!loading && tasks.map((task) => {
              const okCount = (task.items || []).filter((i) => i.status === 'succeeded').length
              const totalCount = (task.items || []).length
              return (
                <tr key={task.id} className="hover:bg-slate-50 dark:hover:bg-slate-800/50">
                  <td className="px-4 py-3">
                    <div className="font-medium text-slate-900 dark:text-slate-100 flex items-center gap-1.5">
                      <span title={task.original_filename} className="block max-w-[260px] truncate">{task.original_filename}</span>
                      {task.deduplicated && (
                        <span className="text-[10px] px-1.5 py-0.5 bg-emerald-100 dark:bg-emerald-500/20 text-emerald-700 dark:text-emerald-300 rounded">
                          {t('common.dedupTag')}
                        </span>
                      )}
                    </div>
                    <div className="text-[11px] font-mono text-slate-400">{task.sha256.slice(0, 16)}...</div>
                  </td>
                  <td className="px-4 py-3 text-slate-700 dark:text-slate-300">{task.client_name || task.client_id}</td>
                  <td className="px-4 py-3"><StatusBadge value={task.mode} /></td>
                  <td className="px-4 py-3 text-slate-600 dark:text-slate-300">{formatBytes(task.file_size)}</td>
                  <td className="px-4 py-3 text-xs text-slate-600 dark:text-slate-300">{t('tasks.progressOk', { ok: okCount, total: totalCount })}</td>
                  <td className="px-4 py-3"><StatusBadge value={task.status} /></td>
                  <td className="px-4 py-3 text-xs text-slate-500 dark:text-slate-400">{formatTime(task.created_at, lang)}</td>
                  <td className="px-4 py-3 text-right space-x-2 whitespace-nowrap">
                    <button onClick={() => setSelected(task)} className="text-xs text-indigo-600 dark:text-indigo-400 hover:underline">
                      {t('common.details')}
                    </button>
                    {okCount > 0 && (
                      <a
                        href={`/admin/api/v1/tasks/${task.id}/download`}
                        className="text-xs text-emerald-700 dark:text-emerald-400 hover:underline inline-flex items-center gap-0.5"
                      >
                        <Download className="w-3.5 h-3.5" /> {t('common.download')}
                      </a>
                    )}
                    {(task.status === 'failed' || task.status === 'partial_success') && (
                      <button disabled={readOnly || (Boolean(retryingId))} onClick={() => handleRetry(task.id)} className="text-xs text-amber-600 dark:text-amber-400 hover:underline inline-flex items-center gap-0.5">
                        <RotateCcw className="w-3.5 h-3.5" /> {t('common.retry')}
                      </button>
                    )}
                  </td>
                </tr>
              )
            })}
            {(loading || tasks.length === 0) && (
              <tr>
                <td colSpan={8}>
                  <EmptyState
                    title={loading ? t('tasks.loadingTitle') : t('tasks.emptyTitle')}
                    description={loading ? undefined : q || status || mode ? t('tasks.emptyFilteredDesc') : t('tasks.emptyDesc')}
                    action={
                      !loading && (
                        <button disabled={readOnly} className="text-xs font-medium text-indigo-600 dark:text-indigo-400" onClick={q || status || mode ? resetFilters : openUploadModal}>
                          {q || status || mode ? t('common.resetFilters') : t('tasks.uploadBtn')}
                        </button>
                      )
                    }
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
            onClick={() => {
              const next = Math.max(0, offset - limit)
              setOffset(next)
              load(next)
            }}
            className="px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg disabled:opacity-40"
          >
            {t('common.prevPage')}
          </button>
          <button
            disabled={loading || offset + limit >= total}
            onClick={() => {
              const next = offset + limit
              setOffset(next)
              load(next)
            }}
            className="px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg disabled:opacity-40"
          >
            {t('common.nextPage')}
          </button>
        </div>
      </div>

      {selected && (
        <Modal title={t('tasks.detail.title')} onClose={() => setSelected(null)} size="xl">
          <div className="bg-white dark:bg-slate-900 rounded-2xl max-w-3xl w-full max-h-[90vh] overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between pr-8">
              <h2 className="text-lg font-bold text-slate-900 dark:text-slate-100">{t('tasks.detail.header', { filename: selected.original_filename })}</h2>
              <StatusBadge value={selected.status} />
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs bg-slate-50 dark:bg-slate-800/70 text-slate-700 dark:text-slate-200 p-3 rounded-lg">
              <div>Task ID: <span className="font-mono">{selected.id}</span></div>
              <div>{t('tasks.detail.fileSize')}: {formatBytes(selected.file_size)}</div>
              <div className="break-all">{t('tasks.detail.originalFilename')}: <span className="font-mono">{selected.original_filename}</span></div>
              <div className="break-all">
                {t('tasks.detail.remoteFilename')}:{' '}
                <span className="font-mono">{selected.remote_filename || selected.original_filename}</span>
                {selected.remote_filename && selected.remote_filename !== selected.original_filename && (
                  <span className="ml-1.5 text-[10px] px-1.5 py-0.5 bg-indigo-100 dark:bg-indigo-500/20 text-indigo-700 dark:text-indigo-300 rounded">
                    {t('tasks.detail.renamedTag')}
                  </span>
                )}
              </div>
              <div className="col-span-2 break-all">SHA-256: <span className="font-mono">{selected.sha256}</span></div>
              <div>{t('tasks.detail.webhookStatus')}: {webhookLabels[selected.webhook_status] || selected.webhook_status}</div>
              <div>{t('tasks.detail.spoolCleaned')}: {selected.spool_cleaned ? t('common.yes') : t('common.no')}</div>
            </div>

            <div className="space-y-2">
              <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">{t('tasks.detail.upstreamItems')}</div>
              {(selected.items || []).map((it) => (
                <div key={it.id} className="border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800/40 rounded-lg p-3 text-xs space-y-1">
                  <div className="flex items-center justify-between">
                    <span className="font-semibold text-slate-900 dark:text-slate-100">{it.upstream_name} ({it.upstream_type.toUpperCase()})</span>
                    <StatusBadge value={it.status} />
                  </div>
                  <div className="text-slate-600 dark:text-slate-300 break-all">{t('tasks.detail.remotePath')}: <span className="font-mono">{it.remote_path}</span></div>
                  <div className="text-slate-500 dark:text-slate-400">
                    {t('tasks.detail.duration')}: {it.duration_ms} ms · {t('tasks.detail.attempts')}: {it.attempt_count}/{it.max_attempts}
                    {it.remote_etag && ` · ETag/Hash: ${it.remote_etag}`}
                  </div>
                  {it.error_message && <div className="text-rose-600 dark:text-rose-400 break-all">{t('tasks.detail.error')}: {it.error_message}</div>}
                  {it.status === 'succeeded' && (
                    <div>
                      <a
                        href={`/admin/api/v1/tasks/${selected.id}/download?upstream_id=${it.upstream_id}`}
                        className="text-indigo-600 dark:text-indigo-400 hover:underline inline-flex items-center gap-1"
                      >
                        <Download className="w-3 h-3" /> {t('tasks.detail.downloadFromUpstream')}
                      </a>
                    </div>
                  )}
                </div>
              ))}
            </div>

            <div className="flex justify-end gap-2 pt-2 border-t border-slate-200 dark:border-slate-800">
              {(selected.status === 'failed' || selected.status === 'partial_success') && (
                <button disabled={readOnly || (Boolean(retryingId))} onClick={() => handleRetry(selected.id)} className="px-4 py-2 bg-amber-600 hover:bg-amber-700 text-white rounded-lg text-sm">
                  {retryingId ? t('common.retrying') : t('tasks.detail.retryFailedUpstreams')}
                </button>
              )}
              <button onClick={() => setSelected(null)} className="px-4 py-2 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-800 rounded-lg text-sm">
                {t('common.close')}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {uploadOpen && (
        <Modal title={t('tasks.upload.title')} onClose={() => setUploadOpen(false)} busy={uploading || creatingDefaultClient || uploadMetaLoading}>
          <div className="bg-white dark:bg-slate-900 rounded-2xl max-w-2xl w-full max-h-[90vh] overflow-y-auto p-6 space-y-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <h2 className="text-lg font-bold text-slate-900 dark:text-slate-100">{t('tasks.upload.title')}</h2>
                <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">{t('tasks.upload.desc')}</p>
              </div>
            </div>

            {uploadMetaLoading ? (
              <div role="status" className="p-8 text-center text-sm text-slate-500 dark:text-slate-400">{t('tasks.upload.loadingMeta')}</div>
            ) : clients.length === 0 ? (
              <div className="space-y-4">
                <div className="p-4 rounded-xl bg-amber-50 dark:bg-amber-500/10 border border-amber-200 dark:border-amber-500/30 text-amber-900 dark:text-amber-200 text-sm space-y-3">
                  <div>{t('tasks.upload.noClientNotice')}</div>
                  {enabledUpstreams.length > 0 ? (
                    <div className="space-y-2">
                      <div className="text-xs text-amber-800 dark:text-amber-300">
                        {t('tasks.upload.hasUpstreamsNotice', {
                          count: enabledUpstreams.length,
                          names: enabledUpstreams.map((u) => u.name).join(lang === 'en' ? ', ' : '、'),
                        })}
                      </div>
                      <div className="flex flex-wrap gap-2">
                        <button
                          type="button"
                          disabled={readOnly || (creatingDefaultClient)}
                          onClick={handleCreateDefaultClient}
                          className="px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-50 text-white rounded-lg text-xs font-medium"
                        >
                          {creatingDefaultClient ? t('tasks.upload.creatingDefaultClient') : t('tasks.upload.createDefaultClientBtn')}
                        </button>
                        {onNavigate && (
                          <button
                            type="button"
                            onClick={() => {
                              setUploadOpen(false)
                              onNavigate('clients')
                            }}
                            className="px-3.5 py-2 bg-white dark:bg-slate-800 border border-amber-300 dark:border-amber-500/40 hover:bg-amber-100 dark:hover:bg-slate-700 text-amber-900 dark:text-amber-200 rounded-lg text-xs font-medium"
                          >
                            {t('tasks.upload.goClientsManual')}
                          </button>
                        )}
                      </div>
                    </div>
                  ) : (
                    <div className="space-y-2">
                      <div className="text-xs text-amber-800 dark:text-amber-300">{t('tasks.upload.noUpstreamsNotice')}</div>
                      <div className="flex flex-wrap gap-2">
                        {onNavigate && (
                          <>
                            <button
                              type="button"
                              onClick={() => {
                                setUploadOpen(false)
                                onNavigate('upstreams')
                              }}
                              className="px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-xs font-medium"
                            >
                              {t('tasks.upload.goUpstreams')}
                            </button>
                            <button
                              type="button"
                              onClick={() => {
                                setUploadOpen(false)
                                onNavigate('clients')
                              }}
                              className="px-3.5 py-2 bg-white dark:bg-slate-800 border border-amber-300 dark:border-amber-500/40 hover:bg-amber-100 dark:hover:bg-slate-700 text-amber-900 dark:text-amber-200 rounded-lg text-xs font-medium"
                            >
                              {t('tasks.upload.goClients')}
                            </button>
                          </>
                        )}
                      </div>
                    </div>
                  )}
                </div>

                {uploadErr && (
                  <div className="p-3 rounded-lg bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 text-xs flex items-start gap-2">
                    <XCircle className="w-4 h-4 shrink-0 mt-0.5" />
                    <span className="break-all">{uploadErr}</span>
                  </div>
                )}

                <div className="flex justify-end pt-2 border-t border-slate-200 dark:border-slate-800">
                  <button type="button" onClick={() => setUploadOpen(false)} className="px-4 py-2 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg text-sm">
                    {t('common.close')}
                  </button>
                </div>
              </div>
            ) : (
              <form onSubmit={handleUpload} className="space-y-4 text-sm">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  <div>
                    <label htmlFor="tasks-field-1" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                      {t('tasks.upload.clientLabel')}
                    </label>
                    <select
                      id="tasks-field-1"
                      required
                      value={upClientId}
                      onChange={(e) => onSelectClient(e.target.value)}
                      className="w-full px-3 py-1.5 border rounded-lg bg-white dark:bg-slate-900"
                    >
                      {clients.map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name} {c.path_prefix ? `(/${c.path_prefix})` : ''}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label htmlFor="tasks-field-2" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                      {t('tasks.upload.modeLabel')}
                    </label>
                    <select
                      id="tasks-field-2"
                      value={upMode}
                      onChange={(e) => setUpMode(e.target.value as any)}
                      className="w-full px-3 py-1.5 border rounded-lg bg-white dark:bg-slate-900"
                    >
                      <option value="sync">{t('tasks.upload.modeSync')}</option>
                      <option value="async">{t('tasks.upload.modeAsync')}</option>
                    </select>
                  </div>
                  <div>
                    <label htmlFor="tasks-field-3" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                      {t('tasks.upload.subpathLabel')}
                    </label>
                    <input id="tasks-field-3" value={upSubpath} onChange={(e) => setUpSubpath(e.target.value)} placeholder="2026/11" className="w-full px-3 py-1.5 border rounded-lg" />
                  </div>
                  <div>
                    <label htmlFor="tasks-field-rename" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                      {t('tasks.upload.renameLabel')}
                    </label>
                    <input
                      id="tasks-field-rename"
                      value={upRename}
                      onChange={(e) => setUpRename(e.target.value)}
                      placeholder={t('tasks.upload.renamePlaceholder')}
                      className="w-full px-3 py-1.5 border rounded-lg"
                    />
                  </div>
                  <div className="md:col-span-2 flex items-center pt-0.5">
                    <label className="inline-flex items-center gap-2 text-xs text-slate-700 dark:text-slate-300">
                      <input type="checkbox" checked={upDedup} onChange={(e) => setUpDedup(e.target.checked)} />
                      <span>{t('tasks.upload.dedupLabel')}</span>
                    </label>
                  </div>
                </div>

                {activeClient && activeClient.upstreams.length === 0 && (
                  <div className="p-3 rounded-lg bg-amber-50 dark:bg-amber-500/10 border border-amber-200 dark:border-amber-500/30 text-amber-800 dark:text-amber-200 text-xs flex items-center justify-between gap-2">
                    <span>{t('tasks.upload.clientNoUpstreams', { name: activeClient.name })}</span>
                    {onNavigate && (
                      <button
                        type="button"
                        onClick={() => {
                          setUploadOpen(false)
                          onNavigate('clients')
                        }}
                        className="px-2.5 py-1 bg-white dark:bg-slate-800 border border-amber-300 dark:border-amber-500/40 hover:bg-amber-100 dark:hover:bg-slate-700 rounded text-amber-900 dark:text-amber-200 font-medium shrink-0"
                      >
                        {t('tasks.upload.goBindUpstreams')}
                      </button>
                    )}
                  </div>
                )}

                {activeClient && activeClient.upstreams.length > 0 && (
                  <div>
                    <label className="block text-xs text-slate-600 dark:text-slate-300 mb-1.5">{t('tasks.upload.targetsLabel')}</label>
                    <div className="flex flex-wrap gap-2">
                      {activeClient.upstreams.map((u) => {
                        const checked = upTargets.includes(u.upstream_id)
                        return (
                          <label
                            key={u.upstream_id}
                            className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg border text-xs cursor-pointer ${
                              checked
                                ? 'bg-indigo-50 dark:bg-indigo-500/15 border-indigo-300 dark:border-indigo-500/40 text-indigo-800 dark:text-indigo-200'
                                : 'bg-slate-50 dark:bg-slate-800 border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-300'
                            }`}
                          >
                            <input
                              type="checkbox"
                              checked={checked}
                              onChange={(e) => {
                                if (e.target.checked) {
                                  setUpTargets([...upTargets, u.upstream_id])
                                } else {
                                  setUpTargets(upTargets.filter((id) => id !== u.upstream_id))
                                }
                              }}
                            />
                            <span>{u.upstream_name}</span>
                            <span className="text-[10px] opacity-70">({u.upstream_type?.toUpperCase()})</span>
                          </label>
                        )
                      })}
                    </div>
                  </div>
                )}

                <div>
                  <label htmlFor="tasks-field-4" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                    {t('tasks.upload.fileLabel')}
                  </label>
                  <input
                    id="tasks-field-4"
                    key={fileInputKey}
                    type="file"
                    required
                    onChange={(e) => {
                      setUpFile(e.target.files?.[0] || null)
                      setUploadResult(null)
                      setUploadErr('')
                    }}
                    className="w-full text-xs border rounded-lg p-2 bg-slate-50 dark:bg-slate-800"
                  />
                  {upFile && (
                    <div className="text-[11px] text-slate-500 dark:text-slate-400 mt-1">
                      {t('tasks.upload.selectedFile')}
                      <span className="font-medium text-slate-700 dark:text-slate-200">{upFile.name}</span> ({formatBytes(upFile.size)})
                    </div>
                  )}
                </div>

                <div>
                  <label htmlFor="tasks-field-5" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                    {t('tasks.upload.metadataLabel')}
                  </label>
                  <input
                    id="tasks-field-5"
                    value={upMetadata}
                    onChange={(e) => setUpMetadata(e.target.value)}
                    placeholder='{"source":"web","env":"prod"}'
                    className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                  />
                </div>

                {uploadErr && (
                  <div className="p-3 rounded-lg bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 text-xs flex items-start gap-2">
                    <XCircle className="w-4 h-4 shrink-0 mt-0.5" />
                    <span className="break-all">{uploadErr}</span>
                  </div>
                )}

                {uploadResult && (
                  <div className="p-3 rounded-xl bg-slate-50 dark:bg-slate-800/70 border border-slate-200 dark:border-slate-700 space-y-2 text-xs">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-1.5 font-semibold text-slate-800 dark:text-slate-100 flex-wrap">
                        <CheckCircle2 className="w-4 h-4 text-emerald-600 dark:text-emerald-400" />
                        <span>{t('tasks.upload.resultTask', { id: uploadResult.task.id })}</span>
                        {uploadResult.task.deduplicated && (
                          <span className="px-1.5 py-0.5 bg-emerald-100 dark:bg-emerald-500/20 text-emerald-700 dark:text-emerald-300 rounded text-[10px]">
                            {t('tasks.upload.resultDedup')}
                          </span>
                        )}
                        {uploadResult.timedOut && (
                          <span className="px-1.5 py-0.5 bg-amber-100 dark:bg-amber-500/20 text-amber-800 dark:text-amber-300 rounded text-[10px]">
                            {t('tasks.upload.resultTimedOut')}
                          </span>
                        )}
                      </div>
                      <StatusBadge value={uploadResult.task.status} />
                    </div>
                    {uploadResult.task.remote_filename && uploadResult.task.remote_filename !== uploadResult.task.original_filename && (
                      <div className="text-[11px] text-slate-600 dark:text-slate-300">
                        {t('tasks.detail.originalFilename')}: <span className="font-mono">{uploadResult.task.original_filename}</span>
                        {' → '}
                        {t('tasks.upload.newFilename')}: <span className="font-mono font-semibold text-slate-800 dark:text-slate-100">{uploadResult.task.remote_filename}</span>
                      </div>
                    )}
                    <div className="space-y-1">
                      {(uploadResult.task.items || []).map((it) => (
                        <div key={it.id} className="flex items-center justify-between bg-white dark:bg-slate-900 px-2.5 py-1.5 rounded border border-slate-200 dark:border-slate-700">
                          <div className="min-w-0 truncate mr-2">
                            <span className="font-medium text-slate-800 dark:text-slate-100">{it.upstream_name}</span>
                            <span className="text-slate-400 font-mono ml-2">{it.remote_path}</span>
                          </div>
                          <StatusBadge value={it.status} />
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                <div className="flex justify-end gap-2 pt-2 border-t border-slate-200 dark:border-slate-800">
                  <button
                    type="button"
                    disabled={uploading}
                    onClick={() => setUploadOpen(false)}
                    className="px-4 py-2 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-800 rounded-lg disabled:opacity-50"
                  >
                    {t('common.close')}
                  </button>
                  <button
                    type="submit"
                    disabled={uploading || !upFile || !upClientId || !activeClient || activeClient.upstreams.length === 0}
                    className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg font-medium"
                  >
                    {uploading ? t('tasks.upload.uploading') : uploadResult && !upFile ? t('tasks.upload.done') : t('tasks.upload.submit')}
                  </button>
                </div>
              </form>
            )}
          </div>
        </Modal>
      )}
    </div>
  )
}
