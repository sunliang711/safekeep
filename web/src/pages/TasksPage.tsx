import React, { useEffect, useState } from 'react'
import { Download, RotateCcw, Search, Upload } from 'lucide-react'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import type { BackupTask } from '../types'

export const TasksPage: React.FC = () => {
  const [tasks, setTasks] = useState<BackupTask[]>([])
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const limit = 20
  const [status, setStatus] = useState('')
  const [mode, setMode] = useState('')
  const [q, setQ] = useState('')
  const [pageErr, setPageErr] = useState('')
  const [selected, setSelected] = useState<BackupTask | null>(null)
  const [uploadOpen, setUploadOpen] = useState(false)
  const [testApiKey, setTestApiKey] = useState('')
  const [testMode, setTestMode] = useState('sync')
  const [testSubpath, setTestSubpath] = useState('')
  const [testTargets, setTestTargets] = useState('')
  const [testFile, setTestFile] = useState<File | null>(null)
  const [testMsg, setTestMsg] = useState('')

  const load = async (targetOffset = offset) => {
    setPageErr('')
    try {
      const params: Record<string, string> = { limit: String(limit), offset: String(targetOffset) }
      if (status) params.status = status
      if (mode) params.mode = mode
      if (q) params.q = q
      const res = await api.listTasks(params)
      setTasks(res.items || [])
      setTotal(res.total || 0)
    } catch (err: any) {
      setPageErr(err.message || '加载任务失败')
    }
  }

  useEffect(() => {
    setOffset(0)
    load(0)
  }, [status, mode])

  const handleRetry = async (id: string) => {
    const updated = await api.retryTask(id)
    setSelected(updated)
    await load()
  }

  const handleTestUpload = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!testFile || !testApiKey) return
    setTestMsg('上传中...')
    const fd = new FormData()
    fd.append('file', testFile)
    const query: Record<string, string> = { mode: testMode }
    if (testSubpath) query.path = testSubpath
    if (testTargets) query.targets = testTargets
    try {
      const res = await api.testClientUpload(testApiKey, fd, query)
      setTestMsg(
        `完成！Task ID: ${res.task.id}，状态: ${res.task.status}${
          res.task.deduplicated ? '（SHA-256 命中秒传去重）' : ''
        }${res.timed_out_to_async ? '（同步超时已转后台）' : ''}`,
      )
      await load()
    } catch (err: any) {
      setTestMsg(`失败: ${err.message}`)
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-bold text-slate-900">备份任务与文件恢复</h1>
          <p className="text-sm text-slate-500">共 {total} 条备份记录；支持查看多上游明细、重试失败项、代理下载恢复</p>
        </div>
        <button
          onClick={() => {
            setTestMsg('')
            setUploadOpen(true)
          }}
          className="inline-flex items-center gap-1.5 px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm font-medium"
        >
          <Upload className="w-4 h-4" /> Web 测试上传
        </button>
      </div>

      <div className="flex flex-wrap gap-3 bg-white p-3 rounded-xl border border-slate-200">
        <div className="flex items-center gap-2 flex-1 min-w-[200px] border rounded-lg px-3 py-1.5">
          <Search className="w-4 h-4 text-slate-400" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') { setOffset(0); load(0) } }}
            placeholder="搜索文件名 / Task ID / SHA-256..."
            className="w-full text-sm focus:outline-none"
          />
        </div>
        <select value={status} onChange={(e) => setStatus(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white">
          <option value="">全部状态</option>
          <option value="succeeded">succeeded</option>
          <option value="pending">pending</option>
          <option value="running">running</option>
          <option value="partial_success">partial_success</option>
          <option value="failed">failed</option>
        </select>
        <select value={mode} onChange={(e) => setMode(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white">
          <option value="">全部模式</option>
          <option value="sync">sync</option>
          <option value="async">async</option>
        </select>
        <button onClick={() => { setOffset(0); load(0) }} className="px-4 py-1.5 bg-slate-900 text-white rounded-lg text-sm">
          查询
        </button>
      </div>

      {pageErr && <div className="p-3 bg-rose-50 border border-rose-200 text-rose-700 rounded-lg text-sm">{pageErr}</div>}

      <div className="bg-white rounded-xl border border-slate-200 overflow-x-auto">
        <table className="w-full text-sm">
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
            {tasks.map((t) => {
              const okCount = (t.items || []).filter((i) => i.status === 'succeeded').length
              const totalCount = (t.items || []).length
              return (
                <tr key={t.id} className="hover:bg-slate-50">
                  <td className="px-4 py-3">
                    <div className="font-medium text-slate-900 flex items-center gap-1.5">
                      <span>{t.original_filename}</span>
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
                      <button onClick={() => handleRetry(t.id)} className="text-xs text-amber-600 hover:underline inline-flex items-center gap-0.5">
                        <RotateCcw className="w-3.5 h-3.5" /> 重试
                      </button>
                    )}
                  </td>
                </tr>
              )
            })}
            {tasks.length === 0 && (
              <tr>
                <td colSpan={8} className="px-4 py-8 text-center text-slate-400">暂无匹配任务。</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      <div className="flex items-center justify-between text-xs text-slate-500">
        <span>第 {Math.floor(offset / limit) + 1} 页 · 共 {total} 条</span>
        <div className="flex gap-2">
          <button
            disabled={offset === 0}
            onClick={() => { const next = Math.max(0, offset - limit); setOffset(next); load(next) }}
            className="px-3 py-1.5 bg-white border rounded-lg disabled:opacity-40"
          >
            上一页
          </button>
          <button
            disabled={offset + limit >= total}
            onClick={() => { const next = offset + limit; setOffset(next); load(next) }}
            className="px-3 py-1.5 bg-white border rounded-lg disabled:opacity-40"
          >
            下一页
          </button>
        </div>
      </div>

      {selected && (
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center p-4 z-50">
          <div className="bg-white rounded-2xl max-w-3xl w-full max-h-[90vh] overflow-y-auto p-6 space-y-4">
            <div className="flex items-center justify-between">
              <h2 className="text-lg font-bold text-slate-900">任务详情 · {selected.original_filename}</h2>
              <StatusBadge value={selected.status} />
            </div>
            <div className="grid grid-cols-2 gap-2 text-xs bg-slate-50 p-3 rounded-lg">
              <div>Task ID: <span className="font-mono">{selected.id}</span></div>
              <div>文件大小: {formatBytes(selected.file_size)}</div>
              <div className="col-span-2 break-all">SHA-256: <span className="font-mono">{selected.sha256}</span></div>
              <div>Webhook 状态: {selected.webhook_status}</div>
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
                <button onClick={() => handleRetry(selected.id)} className="px-4 py-2 bg-amber-600 text-white rounded-lg text-sm">
                  重试失败上游
                </button>
              )}
              <button onClick={() => setSelected(null)} className="px-4 py-2 border rounded-lg text-sm">关闭</button>
            </div>
          </div>
        </div>
      )}

      {uploadOpen && (
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center p-4 z-50">
          <div className="bg-white rounded-2xl max-w-lg w-full p-6 space-y-4">
            <h2 className="text-lg font-bold text-slate-900">模拟客户 API 上传测试</h2>
            <form onSubmit={handleTestUpload} className="space-y-3 text-sm">
              <div>
                <label className="block text-xs text-slate-600 mb-1">客户 API Key *</label>
                <input required value={testApiKey} onChange={(e) => setTestApiKey(e.target.value)} placeholder="sk_..." className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-slate-600 mb-1">模式</label>
                  <select value={testMode} onChange={(e) => setTestMode(e.target.value)} className="w-full px-3 py-1.5 border rounded-lg bg-white">
                    <option value="sync">sync</option>
                    <option value="async">async</option>
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">子目录（可选）</label>
                  <input value={testSubpath} onChange={(e) => setTestSubpath(e.target.value)} placeholder="2026/11" className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
              </div>
              <div>
                <label className="block text-xs text-slate-600 mb-1">指定目标上游名称（逗号分隔，留空用默认组）</label>
                <input value={testTargets} onChange={(e) => setTestTargets(e.target.value)} placeholder="prod-s3,team-dropbox" className="w-full px-3 py-1.5 border rounded-lg" />
              </div>
              <div>
                <label className="block text-xs text-slate-600 mb-1">选择文件 *</label>
                <input type="file" required onChange={(e) => setTestFile(e.target.files?.[0] || null)} className="w-full text-xs" />
              </div>
              {testMsg && <div className="p-2.5 rounded-lg bg-slate-100 text-xs break-all">{testMsg}</div>}
              <div className="flex justify-end gap-2 pt-2 border-t">
                <button type="button" onClick={() => setUploadOpen(false)} className="px-4 py-2 border rounded-lg">关闭</button>
                <button type="submit" className="px-4 py-2 bg-indigo-600 text-white rounded-lg font-medium">发起上传</button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
