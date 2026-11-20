import React, { useEffect, useRef, useState } from 'react'
import { CheckCircle2, Download, RotateCcw, Search, Upload, XCircle } from 'lucide-react'
import { EmptyState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import type { NavTab } from '../components/Layout'
import type { BackupTask, Client, Upstream } from '../types'

interface TasksPageProps {
  onNavigate?: (tab: NavTab) => void
}

export const TasksPage: React.FC<TasksPageProps> = ({ onNavigate }) => {
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
      if (currentRequest === requestId.current) setPageErr(err.message || '加载任务失败')
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
      setUploadErr(err.message || '加载客户或上游列表失败')
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
        description: 'Web 管理端默认上传客户',
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
      setUploadErr(err.message || '自动创建默认客户失败')
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
      setPageErr(err.message || '重试任务失败')
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
      setUploadErr(err.message || '上传失败')
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

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-slate-900">备份任务与文件恢复</h1>
          <p className="text-sm text-slate-500">共 {total} 条备份记录；支持 Web 端直接上传备份、查看多上游明细、重试及下载恢复</p>
        </div>
        <button
          onClick={openUploadModal}
          className="inline-flex items-center gap-1.5 px-4 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm font-medium"
        >
          <Upload className="w-4 h-4" /> 上传文件备份
        </button>
      </div>

      <div className="filter-bar flex flex-wrap gap-3 bg-white p-4 rounded-xl border border-slate-200">
        <div className="flex items-center gap-2 flex-1 min-w-[200px] border rounded-lg px-3 py-1.5">
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
            aria-label="搜索备份任务"
            placeholder="搜索文件名 / Task ID / SHA-256..."
            className="w-full text-sm focus:outline-none"
          />
        </div>
        <select aria-label="任务状态" value={status} onChange={(e) => setStatus(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white">
          <option value="">全部状态</option>
          <option value="succeeded">已完成</option>
          <option value="pending">待处理</option>
          <option value="running">进行中</option>
          <option value="partial_success">部分成功</option>
          <option value="failed">失败</option>
        </select>
        <select aria-label="传输模式" value={mode} onChange={(e) => setMode(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white">
          <option value="">全部模式</option>
          <option value="sync">同步</option>
          <option value="async">异步</option>
        </select>
        <button
          onClick={() => {
            setOffset(0)
            load(0)
          }}
          disabled={loading}
          className="px-4 py-1.5 bg-slate-900 text-white rounded-lg text-sm"
        >
          {loading ? '查询中…' : '查询'}
        </button>
        {(q || status || mode) && <button onClick={resetFilters} className="text-sm text-slate-500 px-2 hover:text-indigo-600">重置筛选</button>}
      </div>

      {pageErr && <div className="p-3 bg-rose-50 border border-rose-200 text-rose-700 rounded-lg text-sm">{pageErr}</div>}

      <div className="bg-white rounded-xl border border-slate-200 overflow-x-auto">
        <table aria-busy={loading} className="tasks-table w-full text-sm">
          <thead className="bg-slate-50 text-xs text-slate-500 border-b">
            <tr>
              <th className="text-left px-4 py-3">文件名 / Hash</th>
              <th className="text-left px-4 py-3">客户</th>
              <th className="text-left px-4 py-3">模式</th>
              <th className="text-left px-4 py-3">大小</th>
              <th className="text-left px-4 py-3">上游进度</th>
              <th className="text-left px-4 py-3">状态</th>
              <th className="text-left px-4 py-3">创建时间</th>
              <th className="text-right px-4 py-3">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {!loading && tasks.map((t) => {
              const okCount = (t.items || []).filter((i) => i.status === 'succeeded').length
              const totalCount = (t.items || []).length
              return (
                <tr key={t.id} className="hover:bg-slate-50">
                  <td className="px-4 py-3">
                    <div className="font-medium text-slate-900 flex items-center gap-1.5">
                      <span title={t.original_filename} className="block max-w-[260px] truncate">{t.original_filename}</span>
                      {t.deduplicated && (
                        <span className="text-[10px] px-1.5 py-0.5 bg-emerald-100 text-emerald-700 rounded">秒传</span>
                      )}
                    </div>
                    <div className="text-[11px] font-mono text-slate-400">{t.sha256.slice(0, 16)}...</div>
                  </td>
                  <td className="px-4 py-3 text-slate-700">{t.client_name || t.client_id}</td>
                  <td className="px-4 py-3"><StatusBadge value={t.mode} /></td>
                  <td className="px-4 py-3 text-slate-600">{formatBytes(t.file_size)}</td>
                  <td className="px-4 py-3 text-xs text-slate-600">{okCount} / {totalCount} 成功</td>
                  <td className="px-4 py-3"><StatusBadge value={t.status} /></td>
                  <td className="px-4 py-3 text-xs text-slate-500">{formatTime(t.created_at)}</td>
                  <td className="px-4 py-3 text-right space-x-2 whitespace-nowrap">
                    <button onClick={() => setSelected(t)} className="text-xs text-indigo-600 hover:underline">
                      详情
                    </button>
                    {okCount > 0 && (
                      <a
                        href={`/admin/api/v1/tasks/${t.id}/download`}
                        className="text-xs text-emerald-700 hover:underline inline-flex items-center gap-0.5"
                      >
                        <Download className="w-3.5 h-3.5" /> 下载
                      </a>
                    )}
                    {(t.status === 'failed' || t.status === 'partial_success') && (
                      <button disabled={Boolean(retryingId)} onClick={() => handleRetry(t.id)} className="text-xs text-amber-600 hover:underline inline-flex items-center gap-0.5">
                        <RotateCcw className="w-3.5 h-3.5" /> 重试
                      </button>
                    )}
                  </td>
                </tr>
              )
            })}
            {(loading || tasks.length === 0) && (
              <tr>
                <td colSpan={8}><EmptyState title={loading ? '正在加载备份任务…' : '暂无备份任务'} description={loading ? undefined : q || status || mode ? '试试其他关键词，或重置筛选条件。' : '上传第一份文件，备份进度和恢复入口会显示在这里。'} action={!loading && <button className="text-xs font-medium text-indigo-600" onClick={q || status || mode ? resetFilters : openUploadModal}>{q || status || mode ? '重置筛选' : '上传文件备份'}</button>} /></td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between text-xs text-slate-500">
        <span>第 {Math.floor(offset / limit) + 1} 页 · 共 {total} 条</span>
        <div className="flex gap-2">
          <button
            disabled={loading || offset === 0}
            onClick={() => {
              const next = Math.max(0, offset - limit)
              setOffset(next)
              load(next)
            }}
            className="px-3 py-1.5 bg-white border rounded-lg disabled:opacity-40"
          >
            上一页
          </button>
          <button
            disabled={loading || offset + limit >= total}
            onClick={() => {
              const next = offset + limit
              setOffset(next)
              load(next)
            }}
            className="px-3 py-1.5 bg-white border rounded-lg disabled:opacity-40"
          >
            下一页
          </button>
        </div>
      </div>

      {selected && (
        <Modal title="任务详情" onClose={() => setSelected(null)} size="xl">
          <div className="bg-white rounded-2xl max-w-3xl w-full max-h-[90vh] overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-bold text-slate-900">任务详情 · {selected.original_filename}</h2>
              <StatusBadge value={selected.status} />
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs bg-slate-50 p-3 rounded-lg">
              <div>Task ID: <span className="font-mono">{selected.id}</span></div>
              <div>文件大小: {formatBytes(selected.file_size)}</div>
              <div className="break-all">原文件名: <span className="font-mono">{selected.original_filename}</span></div>
              <div className="break-all">
                存储文件名:{' '}
                <span className="font-mono">{selected.remote_filename || selected.original_filename}</span>
                {selected.remote_filename && selected.remote_filename !== selected.original_filename && (
                  <span className="ml-1.5 text-[10px] px-1.5 py-0.5 bg-indigo-100 text-indigo-700 rounded">已重命名</span>
                )}
              </div>
              <div className="col-span-2 break-all">SHA-256: <span className="font-mono">{selected.sha256}</span></div>
              <div>Webhook 状态: {{ none: '未配置', pending: '待发送', sent: '已发送', failed: '发送失败' }[selected.webhook_status] || selected.webhook_status}</div>
              <div>临时缓冲已清理: {selected.spool_cleaned ? '是' : '否'}</div>
            </div>

            <div className="space-y-2">
              <div className="text-sm font-semibold text-slate-800">各上游同步明细</div>
              {(selected.items || []).map((it) => (
                <div key={it.id} className="border rounded-lg p-3 text-xs space-y-1">
                  <div className="flex items-center justify-between">
                    <span className="font-semibold">{it.upstream_name} ({it.upstream_type.toUpperCase()})</span>
                    <StatusBadge value={it.status} />
                  </div>
                  <div className="text-slate-600 break-all">远程路径: <span className="font-mono">{it.remote_path}</span></div>
                  <div className="text-slate-500">
                    耗时: {it.duration_ms} ms · 重试: {it.attempt_count}/{it.max_attempts}
                    {it.remote_etag && ` · ETag/Hash: ${it.remote_etag}`}
                  </div>
                  {it.error_message && <div className="text-rose-600 break-all">错误: {it.error_message}</div>}
                  {it.status === 'succeeded' && (
                    <div>
                      <a
                        href={`/admin/api/v1/tasks/${selected.id}/download?upstream_id=${it.upstream_id}`}
                        className="text-indigo-600 hover:underline inline-flex items-center gap-1"
                      >
                        <Download className="w-3 h-3" /> 从此上游下载恢复
                      </a>
                    </div>
                  )}
                </div>
              ))}
            </div>

            <div className="flex justify-end gap-2 pt-2 border-t">
              {(selected.status === 'failed' || selected.status === 'partial_success') && (
                <button disabled={Boolean(retryingId)} onClick={() => handleRetry(selected.id)} className="px-4 py-2 bg-amber-600 text-white rounded-lg text-sm">
                  {retryingId ? '重试中…' : '重试失败上游'}
                </button>
              )}
              <button onClick={() => setSelected(null)} className="px-4 py-2 border rounded-lg text-sm">关闭</button>
            </div>
          </div>
        </Modal>
      )}

      {uploadOpen && (
        <Modal title="上传文件备份" onClose={() => setUploadOpen(false)} busy={uploading || creatingDefaultClient || uploadMetaLoading}>
          <div className="bg-white rounded-2xl max-w-2xl w-full max-h-[90vh] overflow-y-auto p-6 space-y-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <h2 className="text-lg font-bold text-slate-900">上传文件备份</h2>
                <p className="text-xs text-slate-500 mt-0.5">
                  与客户调用备份 API 完全一致：经过单文件大小校验、每日流量熔断、SHA-256 秒传去重、同步/异步分发及审计日志。
                </p>
              </div>
            </div>

            {uploadMetaLoading ? <div role="status" className="p-8 text-center text-sm text-slate-500">正在加载客户与存储策略…</div> : clients.length === 0 ? (
              <div className="space-y-4">
                <div className="p-4 rounded-xl bg-amber-50 border border-amber-200 text-amber-900 text-sm space-y-3">
                  <div>
                    当前没有启用的客户配置。Web 上传需关联一个客户策略（用于路径前缀隔离、去重索引与审计归属）。
                  </div>
                  {enabledUpstreams.length > 0 ? (
                    <div className="space-y-2">
                      <div className="text-xs text-amber-800">
                        检测到当前有 {enabledUpstreams.length} 个已启用上游（
                        {enabledUpstreams.map((u) => u.name).join('、')}
                        ），您可以直接一键创建默认客户并继续上传：
                      </div>
                      <div className="flex flex-wrap gap-2">
                        <button
                          type="button"
                          disabled={creatingDefaultClient}
                          onClick={handleCreateDefaultClient}
                          className="px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-50 text-white rounded-lg text-xs font-medium"
                        >
                          {creatingDefaultClient ? '创建中...' : '一键创建默认客户并继续上传'}
                        </button>
                        {onNavigate && (
                          <button
                            type="button"
                            onClick={() => {
                              setUploadOpen(false)
                              onNavigate('clients')
                            }}
                            className="px-3.5 py-2 bg-white border border-amber-300 hover:bg-amber-100 text-amber-900 rounded-lg text-xs font-medium"
                          >
                            前往「客户与 API Key」手动配置
                          </button>
                        )}
                      </div>
                    </div>
                  ) : (
                    <div className="space-y-2">
                      <div className="text-xs text-amber-800">
                        当前尚未配置或启用任何上游存储（如 S3 / Dropbox），请先添加并启用上游存储。
                      </div>
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
                              前往「上游存储配置」
                            </button>
                            <button
                              type="button"
                              onClick={() => {
                                setUploadOpen(false)
                                onNavigate('clients')
                              }}
                              className="px-3.5 py-2 bg-white border border-amber-300 hover:bg-amber-100 text-amber-900 rounded-lg text-xs font-medium"
                            >
                              前往「客户与 API Key」
                            </button>
                          </>
                        )}
                      </div>
                    </div>
                  )}
                </div>

                {uploadErr && (
                  <div className="p-3 rounded-lg bg-rose-50 border border-rose-200 text-rose-700 text-xs flex items-start gap-2">
                    <XCircle className="w-4 h-4 shrink-0 mt-0.5" />
                    <span className="break-all">{uploadErr}</span>
                  </div>
                )}

                <div className="flex justify-end pt-2 border-t">
                  <button type="button" onClick={() => setUploadOpen(false)} className="px-4 py-2 border rounded-lg text-sm">
                    关闭
                  </button>
                </div>
              </div>
            ) : (
              <form onSubmit={handleUpload} className="space-y-4 text-sm">
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  <div>
                    <label htmlFor="tasks-field-1" className="block text-xs text-slate-600 mb-1">归属客户 / 策略 *</label>
                    <select id="tasks-field-1"
                      required
                      value={upClientId}
                      onChange={(e) => onSelectClient(e.target.value)}
                      className="w-full px-3 py-1.5 border rounded-lg bg-white"
                    >
                      {clients.map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name} {c.path_prefix ? `(/${c.path_prefix})` : ''}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label htmlFor="tasks-field-2" className="block text-xs text-slate-600 mb-1">传输模式</label>
                    <select id="tasks-field-2" value={upMode} onChange={(e) => setUpMode(e.target.value as any)} className="w-full px-3 py-1.5 border rounded-lg bg-white">
                      <option value="sync">sync（同步等待，超时转后台）</option>
                      <option value="async">async（异步队列立即返回）</option>
                    </select>
                  </div>
                  <div>
                    <label htmlFor="tasks-field-3" className="block text-xs text-slate-600 mb-1">相对子目录（可选）</label>
                    <input id="tasks-field-3" value={upSubpath} onChange={(e) => setUpSubpath(e.target.value)} placeholder="2026/11" className="w-full px-3 py-1.5 border rounded-lg" />
                  </div>
                  <div>
                    <label htmlFor="tasks-field-rename" className="block text-xs text-slate-600 mb-1">重命名文件名（可选）</label>
                    <input
                      id="tasks-field-rename"
                      value={upRename}
                      onChange={(e) => setUpRename(e.target.value)}
                      placeholder="留空则使用原文件名"
                      className="w-full px-3 py-1.5 border rounded-lg"
                    />
                  </div>
                  <div className="md:col-span-2 flex items-center pt-0.5">
                    <label className="inline-flex items-center gap-2 text-xs text-slate-700">
                      <input type="checkbox" checked={upDedup} onChange={(e) => setUpDedup(e.target.checked)} />
                      <span>启用 SHA-256 秒传去重</span>
                    </label>
                  </div>
                </div>

                {activeClient && activeClient.upstreams.length === 0 && (
                  <div className="p-3 rounded-lg bg-amber-50 border border-amber-200 text-amber-800 text-xs flex items-center justify-between gap-2">
                    <span>当前客户「{activeClient.name}」尚未绑定任何上游存储，无法执行上传。</span>
                    {onNavigate && (
                      <button
                        type="button"
                        onClick={() => {
                          setUploadOpen(false)
                          onNavigate('clients')
                        }}
                        className="px-2.5 py-1 bg-white border border-amber-300 hover:bg-amber-100 rounded text-amber-900 font-medium shrink-0"
                      >
                        前往绑定上游
                      </button>
                    )}
                  </div>
                )}

                {activeClient && activeClient.upstreams.length > 0 && (
                  <div>
                    <label className="block text-xs text-slate-600 mb-1.5">目标上游（已默认选中客户默认组，留空则按客户默认组）</label>
                    <div className="flex flex-wrap gap-2">
                      {activeClient.upstreams.map((u) => {
                        const checked = upTargets.includes(u.upstream_id)
                        return (
                          <label
                            key={u.upstream_id}
                            className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-lg border text-xs cursor-pointer ${
                              checked ? 'bg-indigo-50 border-indigo-300 text-indigo-800' : 'bg-slate-50 border-slate-200 text-slate-600'
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
                  <label htmlFor="tasks-field-4" className="block text-xs text-slate-600 mb-1">选择文件 *</label>
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
                    className="w-full text-xs border rounded-lg p-2 bg-slate-50"
                  />
                  {upFile && (
                    <div className="text-[11px] text-slate-500 mt-1">
                      已选文件：<span className="font-medium text-slate-700">{upFile.name}</span>（{formatBytes(upFile.size)}）
                    </div>
                  )}
                </div>

                <div>
                  <label htmlFor="tasks-field-5" className="block text-xs text-slate-600 mb-1">自定义 Metadata JSON（可选）</label>
                  <input id="tasks-field-5"
                    value={upMetadata}
                    onChange={(e) => setUpMetadata(e.target.value)}
                    placeholder='{"source":"web","env":"prod"}'
                    className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                  />
                </div>

                {uploadErr && (
                  <div className="p-3 rounded-lg bg-rose-50 border border-rose-200 text-rose-700 text-xs flex items-start gap-2">
                    <XCircle className="w-4 h-4 shrink-0 mt-0.5" />
                    <span className="break-all">{uploadErr}</span>
                  </div>
                )}

                {uploadResult && (
                  <div className="p-3 rounded-xl bg-slate-50 border border-slate-200 space-y-2 text-xs">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-1.5 font-semibold text-slate-800">
                        <CheckCircle2 className="w-4 h-4 text-emerald-600" />
                        <span>任务 {uploadResult.task.id}</span>
                        {uploadResult.task.deduplicated && (
                          <span className="px-1.5 py-0.5 bg-emerald-100 text-emerald-700 rounded text-[10px]">SHA-256 秒传命中</span>
                        )}
                        {uploadResult.timedOut && (
                          <span className="px-1.5 py-0.5 bg-amber-100 text-amber-800 rounded text-[10px]">同步超时已转后台</span>
                        )}
                      </div>
                      <StatusBadge value={uploadResult.task.status} />
                    </div>
                    {uploadResult.task.remote_filename && uploadResult.task.remote_filename !== uploadResult.task.original_filename && (
                      <div className="text-[11px] text-slate-600">
                        原文件名: <span className="font-mono">{uploadResult.task.original_filename}</span>
                        {' → '}
                        新文件名: <span className="font-mono font-semibold text-slate-800">{uploadResult.task.remote_filename}</span>
                      </div>
                    )}
                    <div className="space-y-1">
                      {(uploadResult.task.items || []).map((it) => (
                        <div key={it.id} className="flex items-center justify-between bg-white px-2.5 py-1.5 rounded border">
                          <div className="min-w-0 truncate mr-2">
                            <span className="font-medium">{it.upstream_name}</span>
                            <span className="text-slate-400 font-mono ml-2">{it.remote_path}</span>
                          </div>
                          <StatusBadge value={it.status} />
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                <div className="flex justify-end gap-2 pt-2 border-t">
                  <button
                    type="button"
                    disabled={uploading}
                    onClick={() => setUploadOpen(false)}
                    className="px-4 py-2 border rounded-lg disabled:opacity-50"
                  >
                    关闭
                  </button>
                  <button
                    type="submit"
                    disabled={uploading || !upFile || !upClientId || !activeClient || activeClient.upstreams.length === 0}
                    className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg font-medium"
                  >
                    {uploading ? '上传中...' : uploadResult && !upFile ? '上传已完成' : '开始上传备份'}
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
