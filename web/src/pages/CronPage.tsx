import React, { useEffect, useState } from 'react'
import { History, Play } from 'lucide-react'
import { LoadingState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { api, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import type { CronJob, CronJobRun } from '../types'

export const CronPage: React.FC = () => {
  const toast = useToast()
  const [loading, setLoading] = useState(true)
  const [runningId, setRunningId] = useState<string | null>(null)
  const [runsLoading, setRunsLoading] = useState(false)
  const [runsError, setRunsError] = useState('')
  const [jobs, setJobs] = useState<CronJob[]>([])
  const [editing, setEditing] = useState<CronJob | null>(null)
  const [cronExpr, setCronExpr] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [configText, setConfigText] = useState('{}')
  const [saving, setSaving] = useState(false)
  const [runsJob, setRunsJob] = useState<CronJob | null>(null)
  const [runs, setRuns] = useState<CronJobRun[]>([])

  const load = async () => {
    try {
      const res = await api.listCronJobs()
      setJobs(res.items || [])
    } catch (err: any) {
      toast.error(`加载定时任务失败: ${err.message}`)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const openEdit = (j: CronJob) => {
    setEditing(j)
    setCronExpr(j.cron_expr)
    setEnabled(j.enabled)
    setConfigText(j.config_json || '{}')
  }

  const cronDirty = editing
    ? cronExpr !== editing.cron_expr || enabled !== editing.enabled || configText !== (editing.config_json || '{}')
    : false

  const closeEdit = () => {
    if (cronDirty && !confirm('定时任务尚未保存，确定放弃修改吗？')) return
    setEditing(null)
  }

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!editing || !cronDirty) return
    setSaving(true)
    try {
      const cfg = JSON.parse(configText || '{}')
      await api.updateCronJob(editing.id, { cron_expr: cronExpr, enabled, config: cfg })
      setEditing(null)
      toast.success('定时任务已更新并热重载')
      await load()
    } catch (err: any) {
      toast.error(err.message || '保存定时任务失败')
    } finally {
      setSaving(false)
    }
  }

  const handleRunNow = async (j: CronJob) => {
    if (runningId) return
    setRunningId(j.id)
    toast.info(`正在执行 ${j.name}...`)
    try {
      const run = await api.runCronJob(j.id)
      const message = `"${j.name}" ${run.status === 'succeeded' ? '执行完成' : '执行失败'} (${run.output_log})`
      if (run.status === 'succeeded') toast.success(message)
      else toast.error(message)
      await load()
    } catch (err: any) {
      toast.error(`执行失败: ${err.message}`)
    } finally {
      setRunningId(null)
    }
  }

  const openRuns = async (j: CronJob) => {
    setRunsJob(j)
    setRuns([])
    setRunsError('')
    setRunsLoading(true)
    try {
      const res = await api.listCronRuns(j.id)
      setRuns(res.items || [])
    } catch (err: any) {
      setRunsError(err.message || '加载执行历史失败')
    } finally {
      setRunsLoading(false)
    }
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-slate-900">定时任务管理</h1>
        <p className="text-sm text-slate-500">管理日志清理、缓冲盘垃圾回收、上游连通巡检等周期任务</p>
      </div>

      <div className="bg-white rounded-xl border border-slate-200 overflow-x-auto">
        <table className="cron-table w-full text-sm">
          <thead className="bg-slate-50 text-xs text-slate-500 border-b">
            <tr>
              <th className="text-left px-4 py-3">任务名称</th>
              <th className="text-left px-4 py-3">Cron 表达式 / 参数</th>
              <th className="text-left px-4 py-3">状态</th>
              <th className="text-left px-4 py-3">最近执行</th>
              <th className="text-left px-4 py-3">下次执行</th>
              <th className="text-right px-4 py-3">操作</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {loading && <tr><td colSpan={6}><LoadingState label="正在加载调度任务…" /></td></tr>}
            {!loading && jobs.length === 0 && <tr><td colSpan={6} className="p-8 text-center text-slate-500">没有可用的定时任务</td></tr>}
            {jobs.map((j) => (
              <tr key={j.id}>
                <td className="px-4 py-3">
                  <div className="font-medium text-slate-900">{j.name}</div>
                  <div className="text-xs text-slate-400">{j.description}</div>
                </td>
                <td className="px-4 py-3">
                  <div className="font-mono text-xs bg-slate-100 inline-block px-2 py-0.5 rounded">{j.cron_expr}</div>
                  <div className="font-mono text-[11px] text-slate-500 mt-1">{j.config_json}</div>
                </td>
                <td className="px-4 py-3">
                  <StatusBadge value={j.enabled ? 'enabled' : 'disabled'} label={j.enabled ? '启用' : '暂停'} />
                </td>
                <td className="px-4 py-3 text-xs">
                  <div className="flex items-center gap-1.5">
                    <StatusBadge value={j.last_status} />
                    <span className="text-slate-500">{formatTime(j.last_run_at)}</span>
                  </div>
                  {j.last_summary && <div className="text-slate-500 mt-1 max-w-xs truncate">{j.last_summary}</div>}
                </td>
                <td className="px-4 py-3 text-xs text-slate-500">{formatTime(j.next_run_at)}</td>
                <td className="px-4 py-3 text-right space-x-2 whitespace-nowrap">
                  <button onClick={() => openEdit(j)} className="text-xs text-indigo-600 hover:underline">配置</button>
                  <button disabled={Boolean(runningId)} onClick={() => handleRunNow(j)} className="text-xs text-emerald-700 hover:underline inline-flex items-center gap-0.5">
                    <Play className="w-3 h-3" /> {runningId === j.id ? '执行中…' : '立即执行'}
                  </button>
                  <button onClick={() => openRuns(j)} className="text-xs text-slate-600 hover:underline inline-flex items-center gap-0.5">
                    <History className="w-3 h-3" /> 历史
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {editing && (
        <Modal title="编辑定时任务" onClose={closeEdit} busy={saving} size="md">
          <div className="bg-white rounded-2xl max-w-md w-full p-6 space-y-4">
            <h2 className="text-lg font-bold">编辑定时任务 · {editing.name}</h2>
            <form onSubmit={handleSave} className="space-y-3 text-sm">
              <div>
                <label htmlFor="cron-field-1" className="block text-xs text-slate-600 mb-1">Cron 表达式（5 位分/时/日/月/周）</label>
                <input id="cron-field-1" required value={cronExpr} onChange={(e) => setCronExpr(e.target.value)} className="w-full px-3 py-2 border rounded-lg font-mono" />
              </div>
              <label className="inline-flex items-center gap-2">
                <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
                <span>启用调度</span>
              </label>
              <div>
                <label htmlFor="cron-field-2" className="block text-xs text-slate-600 mb-1">任务参数 JSON（如 retention_days）</label>
                <textarea id="cron-field-2" rows={3} value={configText} onChange={(e) => setConfigText(e.target.value)} className="w-full px-3 py-2 border rounded-lg font-mono text-xs" />
              </div>
              <div className="flex justify-end gap-2 pt-2 border-t">
                <button type="button" disabled={saving} onClick={closeEdit} className="px-4 py-2 border rounded-lg disabled:opacity-50">取消</button>
                <button
                  type="submit"
                  disabled={saving || !cronDirty}
                  className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg"
                >
                  {saving ? '保存中...' : cronDirty ? '保存并重载' : '无改动'}
                </button>
              </div>
            </form>
          </div>
        </Modal>
      )}

      {runsJob && (
        <Modal title="执行历史" onClose={() => setRunsJob(null)}>
          <div className="bg-white rounded-2xl max-w-2xl w-full max-h-[85vh] overflow-y-auto p-6 space-y-3">
            <h2 className="text-lg font-bold">执行历史 · {runsJob.name}</h2>
            <div className="space-y-2">
              {runsLoading && <LoadingState label="正在加载执行历史…" />}
              {runsError && <div role="alert" className="text-sm text-rose-600">{runsError}<button className="ml-3 underline" onClick={() => openRuns(runsJob)}>重试</button></div>}
              {runs.map((r) => (
                <div key={r.id} className="border rounded-lg p-3 text-xs space-y-1">
                  <div className="flex items-center justify-between">
                    <span className="font-medium">{r.trigger_type} · {r.triggered_by}</span>
                    <StatusBadge value={r.status} />
                  </div>
                  <div className="text-slate-500">开始: {formatTime(r.started_at)} · 影响行数: {r.affected_rows}</div>
                  <div className="text-slate-700 bg-slate-50 p-2 rounded font-mono break-all">{r.output_log}</div>
                </div>
              ))}
              {!runsLoading && !runsError && runs.length === 0 && <div className="text-sm text-slate-400">暂无执行历史。</div>}
            </div>
            <div className="flex justify-end pt-2">
              <button onClick={() => setRunsJob(null)} className="px-4 py-2 border rounded-lg text-sm">关闭</button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
