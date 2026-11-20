import React, { useEffect, useState } from 'react'
import { History, Play } from 'lucide-react'
import { LoadingState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { SecretField } from '../components/SecretField'
import { api, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import type { Client, CronJob, CronJobRun } from '../types'

interface MigrationBackupForm {
  api_base_url: string
  client_id: string
  api_key: string
  has_api_key: boolean
  clear_api_key: boolean
  targets: string
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

function parseMigrationBackupForm(raw?: string): MigrationBackupForm {
  let obj: any = {}
  try {
    obj = JSON.parse(raw || '{}') || {}
  } catch {
    obj = {}
  }
  return {
    api_base_url: obj.api_base_url || '',
    client_id: obj.client_id || '',
    api_key: '',
    has_api_key: Boolean(obj.has_api_key),
    clear_api_key: false,
    targets: obj.targets || '',
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

export const CronPage: React.FC = () => {
  const toast = useToast()
  const [loading, setLoading] = useState(true)
  const [runningId, setRunningId] = useState<string | null>(null)
  const [runsLoading, setRunsLoading] = useState(false)
  const [runsError, setRunsError] = useState('')
  const [jobs, setJobs] = useState<CronJob[]>([])
  const [clients, setClients] = useState<Client[]>([])
  const [editing, setEditing] = useState<CronJob | null>(null)
  const [cronExpr, setCronExpr] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [configText, setConfigText] = useState('{}')
  const [mbForm, setMbForm] = useState<MigrationBackupForm | null>(null)
  const [mbInitialSnap, setMbInitialSnap] = useState('')
  const [saving, setSaving] = useState(false)
  const [runsJob, setRunsJob] = useState<CronJob | null>(null)
  const [runs, setRuns] = useState<CronJobRun[]>([])

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

  const openEdit = (j: CronJob) => {
    setEditing(j)
    setCronExpr(j.cron_expr)
    setEnabled(j.enabled)
    if (j.job_key === 'migration_backup') {
      const parsed = parseMigrationBackupForm(j.config_json)
      setMbForm(parsed)
      setMbInitialSnap(JSON.stringify(parsed))
      setConfigText(j.config_json || '{}')
    } else {
      setMbForm(null)
      setMbInitialSnap('')
      setConfigText(j.config_json || '{}')
    }
  }

  const isMigrationJob = editing?.job_key === 'migration_backup' && mbForm !== null
  const cronDirty = editing
    ? cronExpr !== editing.cron_expr ||
      enabled !== editing.enabled ||
      (isMigrationJob ? JSON.stringify(mbForm) !== mbInitialSnap : configText !== (editing.config_json || '{}'))
    : false

  const closeEdit = () => {
    if (cronDirty && !confirm('定时任务尚未保存，确定放弃修改吗？')) return
    setEditing(null)
    setMbForm(null)
  }

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!editing || !cronDirty) return

    let cfg: any
    if (isMigrationJob && mbForm) {
      if (mbForm.passphrase && mbForm.passphrase.length < 6) {
        toast.error('导出加密口令若设置，需至少 6 位（或留空不设口令）')
        return
      }
      if (enabled && !mbForm.has_api_key && !mbForm.api_key.trim() && !mbForm.client_id) {
        toast.error('启用自动备份前，请先选择本机客户或填写目标 API Key')
        return
      }
      cfg = {
        api_base_url: mbForm.api_base_url.trim(),
        client_id: mbForm.client_id,
        targets: mbForm.targets.trim(),
        path: mbForm.path.trim(),
        mode: mbForm.mode,
        rename: mbForm.rename.trim(),
        include_admins: mbForm.include_admins,
        include_tasks: mbForm.include_tasks,
        include_audit_logs: mbForm.include_audit_logs,
      }
      if (mbForm.clear_api_key) {
        cfg.api_key = ''
      } else if (mbForm.api_key.trim()) {
        cfg.api_key = mbForm.api_key.trim()
      }
      if (mbForm.clear_passphrase) {
        cfg.passphrase = ''
      } else if (mbForm.passphrase) {
        cfg.passphrase = mbForm.passphrase
      }
    } else {
      try {
        cfg = JSON.parse(configText || '{}')
      } catch (err: any) {
        toast.error(`参数 JSON 格式有误: ${err.message}`)
        return
      }
    }

    setSaving(true)
    try {
      await api.updateCronJob(editing.id, { cron_expr: cronExpr, enabled, config: cfg })
      setEditing(null)
      setMbForm(null)
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

  const selectedLocalClient = mbForm?.client_id ? clients.find((c) => c.id === mbForm.client_id) : undefined
  const selectedTargetsList = (mbForm?.targets || '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)

  const renderConfigSummary = (j: CronJob) => {
    if (j.job_key !== 'migration_backup') {
      return <div className="font-mono text-[11px] text-slate-500 mt-1">{j.config_json}</div>
    }
    const parsed = parseMigrationBackupForm(j.config_json)
    const clName = clients.find((c) => c.id === parsed.client_id)?.name
    const targetDesc = parsed.api_base_url || '本机 SafeKeep'
    const authDesc = parsed.has_api_key ? '自定义 API Key' : clName ? `客户 ${clName}` : '未配置认证'
    return (
      <div className="text-[11px] text-slate-500 mt-1 space-y-0.5 max-w-xs">
        <div className="truncate">
          目标: <span className="font-mono text-slate-700">{targetDesc}</span> · {authDesc}
        </div>
        <div className="truncate">
          路径: <span className="font-mono">/{parsed.path || ''}</span>
          {parsed.targets ? ` · 上游: ${parsed.targets}` : ''}
          {parsed.rename ? ` · 命名: ${parsed.rename}` : ''}
          {parsed.has_passphrase ? ' · 已设口令' : ' · 无口令'}
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-bold text-slate-900">定时任务管理</h1>
        <p className="text-sm text-slate-500">管理日志清理、缓冲盘垃圾回收、上游连通巡检与环境迁移包自动备份等周期任务</p>
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
                  {renderConfigSummary(j)}
                </td>
                <td className="px-4 py-3">
                  <StatusBadge value={j.enabled ? 'enabled' : 'disabled'} label={j.enabled ? '启用' : '暂停'} />
                </td>
                <td className="px-4 py-3 text-xs">
                  <div className="flex items-center gap-1.5">
                    <StatusBadge value={j.last_status} />
                    <span className="text-slate-500">{formatTime(j.last_run_at)}</span>
                  </div>
                  {j.last_summary && <div className="text-slate-500 mt-1 max-w-xs truncate" title={j.last_summary}>{j.last_summary}</div>}
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
        <Modal title="编辑定时任务" onClose={closeEdit} busy={saving} size={isMigrationJob ? 'lg' : 'md'}>
          <div className={`bg-white rounded-2xl w-full p-6 space-y-4 ${isMigrationJob ? 'max-w-2xl' : 'max-w-md'}`}>
            <h2 className="text-lg font-bold">编辑定时任务 · {editing.name}</h2>
            <form onSubmit={handleSave} className="space-y-3 text-sm">
              <div className={isMigrationJob ? 'grid grid-cols-1 sm:grid-cols-2 gap-3 items-end' : 'space-y-3'}>
                <div>
                  <label htmlFor="cron-field-1" className="block text-xs text-slate-600 mb-1">Cron 表达式（5 位分/时/日/月/周）</label>
                  <input id="cron-field-1" required value={cronExpr} onChange={(e) => setCronExpr(e.target.value)} className="w-full px-3 py-2 border rounded-lg font-mono" />
                </div>
                <div className="pb-2">
                  <label className="inline-flex items-center gap-2">
                    <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
                    <span>启用调度</span>
                  </label>
                </div>
              </div>

              {isMigrationJob && mbForm ? (
                <div className="space-y-3 pt-2 border-t text-xs">
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    <div className="sm:col-span-2">
                      <label htmlFor="mb-base-url" className="block text-slate-600 mb-1">
                        目标 SafeKeep 服务地址（可选）
                      </label>
                      <input
                        id="mb-base-url"
                        value={mbForm.api_base_url}
                        onChange={(e) => setMbForm({ ...mbForm, api_base_url: e.target.value })}
                        placeholder="留空则通过 HTTP 请求本机 SafeKeep；外部实例可填 https://backup.example.com"
                        className="w-full px-3 py-1.5 border rounded-lg font-mono"
                      />
                    </div>

                    <div>
                      <label htmlFor="mb-client" className="block text-slate-600 mb-1">
                        快捷选择本机客户（自动使用其 API Key）
                      </label>
                      <select
                        id="mb-client"
                        value={mbForm.client_id}
                        onChange={(e) => setMbForm({ ...mbForm, client_id: e.target.value })}
                        className="w-full px-3 py-1.5 border rounded-lg bg-white"
                      >
                        <option value="">-- 不选（使用下方手动填写的 API Key）--</option>
                        {clients.filter((c) => c.enabled).map((c) => (
                          <option key={c.id} value={c.id}>
                            {c.name}{c.has_api_key === false ? '（旧 Key，需轮换或手动填）' : ''}
                          </option>
                        ))}
                      </select>
                    </div>

                    <div>
                      <div className="flex items-center justify-between mb-1">
                        <label htmlFor="mb-api-key" className="block text-slate-600">
                          手动指定 API Key（外部实例必填，可选覆盖本机客户）
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
                        placeholder={mbForm.has_api_key ? '留空保持不变，点击查看或修改' : 'sk_...（留空则使用所选本机客户 Key）'}
                      />
                    </div>

                    <div className="sm:col-span-2">
                      <label htmlFor="mb-targets" className="block text-slate-600 mb-1">
                        目标上游名称或 ID（逗号分隔，可选；留空则按客户端默认上游组备份）
                      </label>
                      <input
                        id="mb-targets"
                        value={mbForm.targets}
                        onChange={(e) => setMbForm({ ...mbForm, targets: e.target.value })}
                        placeholder="如 prod-s3,dropbox（留空走默认上游）"
                        className="w-full px-3 py-1.5 border rounded-lg font-mono"
                      />
                      {selectedLocalClient && selectedLocalClient.upstreams.length > 0 && (
                        <div className="flex flex-wrap gap-1.5 mt-1.5">
                          {selectedLocalClient.upstreams.map((u) => {
                            const active = selectedTargetsList.includes(u.upstream_name || '') || selectedTargetsList.includes(u.upstream_id)
                            return (
                              <button
                                key={u.upstream_id}
                                type="button"
                                onClick={() => {
                                  const name = u.upstream_name || u.upstream_id
                                  const next = active
                                    ? selectedTargetsList.filter((t) => t !== name && t !== u.upstream_id)
                                    : [...selectedTargetsList, name]
                                  setMbForm({ ...mbForm, targets: next.join(',') })
                                }}
                                className={`px-2 py-0.5 rounded border text-[11px] ${
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
                    </div>

                    <div>
                      <label htmlFor="mb-path" className="block text-slate-600 mb-1">远端相对子目录（可选）</label>
                      <input
                        id="mb-path"
                        value={mbForm.path}
                        onChange={(e) => setMbForm({ ...mbForm, path: e.target.value })}
                        placeholder="migration-backups"
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>

                    <div>
                      <label htmlFor="mb-rename" className="block text-slate-600 mb-1">固定重命名文件名（可选）</label>
                      <input
                        id="mb-rename"
                        value={mbForm.rename}
                        onChange={(e) => setMbForm({ ...mbForm, rename: e.target.value })}
                        placeholder="留空按 safekeep-migration-时间戳.skbundle"
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>

                    <div>
                      <label htmlFor="mb-mode" className="block text-slate-600 mb-1">上传传输模式</label>
                      <select
                        id="mb-mode"
                        value={mbForm.mode}
                        onChange={(e) => setMbForm({ ...mbForm, mode: e.target.value as 'sync' | 'async' })}
                        className="w-full px-3 py-1.5 border rounded-lg bg-white"
                      >
                        <option value="sync">sync（同步等待上传完成）</option>
                        <option value="async">async（异步队列立即返回）</option>
                      </select>
                    </div>

                    <div>
                      <div className="flex items-center justify-between mb-1">
                        <label htmlFor="mb-passphrase" className="block text-slate-600">
                          导出加密口令（可选，≥6 位或留空）
                        </label>
                        {mbForm.has_passphrase && (
                          <button
                            type="button"
                            onClick={() => setMbForm({ ...mbForm, passphrase: '', has_passphrase: false, clear_passphrase: true })}
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
                        placeholder={mbForm.has_passphrase ? '留空保持不变，点击查看或修改' : '留空则不设口令；如设置需至少 6 位'}
                      />
                    </div>
                  </div>

                  <div className="pt-1 space-y-1.5">
                    <div className="text-slate-600 font-medium">导出范围（默认已包含系统设置、上游与代理、客户与 Key、定时任务）</div>
                    <div className="flex flex-wrap gap-4">
                      <label className="inline-flex items-center gap-1.5">
                        <input
                          type="checkbox"
                          checked={mbForm.include_admins}
                          onChange={(e) => setMbForm({ ...mbForm, include_admins: e.target.checked })}
                        />
                        <span>包含管理员账号</span>
                      </label>
                      <label className="inline-flex items-center gap-1.5">
                        <input
                          type="checkbox"
                          checked={mbForm.include_tasks}
                          onChange={(e) => setMbForm({ ...mbForm, include_tasks: e.target.checked })}
                        />
                        <span>包含历史备份索引</span>
                      </label>
                      <label className="inline-flex items-center gap-1.5">
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
              ) : (
                <div>
                  <label htmlFor="cron-field-2" className="block text-xs text-slate-600 mb-1">任务参数 JSON（如 retention_days）</label>
                  <textarea id="cron-field-2" rows={3} value={configText} onChange={(e) => setConfigText(e.target.value)} className="w-full px-3 py-2 border rounded-lg font-mono text-xs" />
                </div>
              )}

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
