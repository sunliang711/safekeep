import React, { useEffect, useState } from 'react'
import { Download, ShieldAlert, Upload } from 'lucide-react'
import { api, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import type { Admin, InspectSummary, SystemSettings } from '../types'

export const SettingsMigrationPage: React.FC = () => {
  const [settings, setSettings] = useState<SystemSettings | null>(null)
  const [admins, setAdmins] = useState<Admin[]>([])
  const [newAdminUser, setNewAdminUser] = useState('')
  const [newAdminPass, setNewAdminPass] = useState('')
  const [msg, setMsg] = useState('')
  const [err, setErr] = useState('')

  // Migration Export
  const [expPass, setExpPass] = useState('')
  const [expTasks, setExpTasks] = useState(true)
  const [expAdmins, setExpAdmins] = useState(false)
  const [expAudit, setExpAudit] = useState(false)

  // Migration Import
  const [impFile, setImpFile] = useState<File | null>(null)
  const [impPass, setImpPass] = useState('')
  const [impMode, setImpMode] = useState<'merge' | 'replace'>('merge')
  const [impConflict, setImpConflict] = useState<'skip' | 'overwrite'>('skip')
  const [impTasks, setImpTasks] = useState(true)
  const [impAdmins, setImpAdmins] = useState(false)
  const [impAudit, setImpAudit] = useState(false)
  const [inspectRes, setInspectRes] = useState<InspectSummary | null>(null)

  const load = async () => {
    const [s, a] = await Promise.all([api.getSettings(), api.listAdmins()])
    setSettings(s)
    setAdmins(a.items || [])
  }

  useEffect(() => {
    load()
  }, [])

  const saveSettings = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!settings) return
    setMsg('')
    setErr('')
    try {
      const updated = await api.updateSettings(settings)
      setSettings(updated)
      setMsg('系统设置已保存')
    } catch (e: any) {
      setErr(e.message)
    }
  }

  const handleCreateAdmin = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg('')
    setErr('')
    try {
      await api.createAdmin(newAdminUser, newAdminPass)
      setNewAdminUser('')
      setNewAdminPass('')
      setMsg('新管理员已创建')
      await load()
    } catch (e: any) {
      setErr(e.message)
    }
  }

  const handleExport = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg('')
    setErr('')
    try {
      await api.exportMigration({
        include_tasks: expTasks,
        include_admins: expAdmins,
        include_audit_logs: expAudit,
        passphrase: expPass,
      })
      setMsg('加密迁移包已生成并开始下载')
    } catch (e: any) {
      setErr(e.message)
    }
  }

  const handleInspect = async () => {
    if (!impFile || !impPass) return
    setMsg('')
    setErr('')
    const fd = new FormData()
    fd.append('bundle', impFile)
    fd.append('passphrase', impPass)
    try {
      const summary = await api.inspectMigration(fd)
      setInspectRes(summary)
    } catch (e: any) {
      setInspectRes(null)
      setErr(e.message)
    }
  }

  const handleImport = async () => {
    if (!impFile || !impPass) return
    if (impMode === 'replace' && !confirm('确定使用「覆盖恢复」模式？当前环境的上游与客户配置将被清空替换为迁移包快照！')) {
      return
    }
    setMsg('')
    setErr('')
    const fd = new FormData()
    fd.append('bundle', impFile)
    fd.append('passphrase', impPass)
    fd.append('mode', impMode)
    fd.append('conflict_strategy', impConflict)
    fd.append('include_tasks', String(impTasks))
    fd.append('include_admins', String(impAdmins))
    fd.append('include_audit_logs', String(impAudit))
    try {
      const res = await api.importMigration(fd)
      setMsg(`环境恢复成功！已导入上游 ${res.counts.upstreams} 个、客户 ${res.counts.clients} 个、定时任务 ${res.counts.cron_jobs} 个、历史任务 ${res.counts.backup_tasks} 条。`)
      setInspectRes(null)
      await load()
    } catch (e: any) {
      setErr(e.message)
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-slate-900">系统设置与环境迁移</h1>
        <p className="text-sm text-slate-500">配置运行参数、管理员账号，以及加密迁移导出与一键恢复</p>
      </div>

      {msg && <div className="p-3 bg-emerald-50 border border-emerald-200 text-emerald-800 rounded-lg text-sm">{msg}</div>}
      {err && <div className="p-3 bg-rose-50 border border-rose-200 text-rose-800 rounded-lg text-sm">{err}</div>}

      {/* Migration Export & Import */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
          <div className="flex items-center gap-2 font-semibold text-slate-900">
            <Download className="w-4 h-4 text-indigo-600" />
            <span>导出环境迁移包（.skbundle）</span>
          </div>
          <p className="text-xs text-slate-500">
            所有上游密钥、Dropbox Token 与代理密码将使用临时迁移口令重加密打包；客户 API Key 哈希保留，迁移后客户端无需更换密钥。
          </p>
          <form onSubmit={handleExport} className="space-y-3 text-sm">
            <div className="space-y-1.5 text-xs">
              <div className="font-medium text-slate-700">必选包含：系统设置、上游存储与代理、客户与 API Key、定时任务配置</div>
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={expTasks} onChange={(e) => setExpTasks(e.target.checked)} />
                <span>包含历史备份文件索引（导入后可继续检索与恢复下载旧文件）</span>
              </label>
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={expAdmins} onChange={(e) => setExpAdmins(e.target.checked)} />
                <span>包含管理员账号</span>
              </label>
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={expAudit} onChange={(e) => setExpAudit(e.target.checked)} />
                <span>包含历史审计日志</span>
              </label>
            </div>
            <div>
              <label className="block text-xs text-slate-600 mb-1">设置迁移加密口令（至少 6 位，导入时需验证）*</label>
              <input
                type="password"
                required
                minLength={6}
                value={expPass}
                onChange={(e) => setExpPass(e.target.value)}
                placeholder="请输入加密口令"
                className="w-full px-3 py-2 border rounded-lg"
              />
            </div>
            <button type="submit" className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg font-medium">
              生成并下载迁移包
            </button>
          </form>
        </div>

        <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
          <div className="flex items-center gap-2 font-semibold text-slate-900">
            <Upload className="w-4 h-4 text-emerald-600" />
            <span>导入恢复环境</span>
          </div>
          <p className="text-xs text-slate-500">
            上传 `.skbundle` 并输入迁移口令。先点击「预检包内容」查看清单与冲突，确认后执行事务导入并自动使用本机主密钥重新加密。
          </p>
          <div className="space-y-3 text-sm">
            <div>
              <label className="block text-xs text-slate-600 mb-1">选择迁移包文件（.skbundle）</label>
              <input type="file" onChange={(e) => { setImpFile(e.target.files?.[0] || null); setInspectRes(null) }} className="w-full text-xs" />
            </div>
            <div>
              <label className="block text-xs text-slate-600 mb-1">迁移包解密口令</label>
              <input type="password" value={impPass} onChange={(e) => setImpPass(e.target.value)} className="w-full px-3 py-2 border rounded-lg" />
            </div>
            <div className="grid grid-cols-2 gap-3 text-xs">
              <div>
                <label className="block text-slate-600 mb-1">恢复模式</label>
                <select value={impMode} onChange={(e) => setImpMode(e.target.value as any)} className="w-full px-2.5 py-1.5 border rounded-lg bg-white">
                  <option value="merge">增量合并（merge）</option>
                  <option value="replace">清空覆盖恢复（replace）</option>
                </select>
              </div>
              {impMode === 'merge' && (
                <div>
                  <label className="block text-slate-600 mb-1">冲突处理策略</label>
                  <select value={impConflict} onChange={(e) => setImpConflict(e.target.value as any)} className="w-full px-2.5 py-1.5 border rounded-lg bg-white">
                    <option value="skip">跳过已存在项</option>
                    <option value="overwrite">覆盖同名/同 ID 项</option>
                  </select>
                </div>
              )}
            </div>
            <div className="flex flex-wrap gap-4 text-xs">
              <label className="flex items-center gap-1.5"><input type="checkbox" checked={impTasks} onChange={(e) => setImpTasks(e.target.checked)} />导入历史备份索引</label>
              <label className="flex items-center gap-1.5"><input type="checkbox" checked={impAdmins} onChange={(e) => setImpAdmins(e.target.checked)} />导入管理员</label>
              <label className="flex items-center gap-1.5"><input type="checkbox" checked={impAudit} onChange={(e) => setImpAudit(e.target.checked)} />导入审计日志</label>
            </div>
            <div className="flex gap-2">
              <button type="button" onClick={handleInspect} disabled={!impFile || !impPass} className="px-4 py-2 border rounded-lg hover:bg-slate-50 disabled:opacity-50">
                预检包内容
              </button>
              <button type="button" onClick={handleImport} disabled={!impFile || !impPass} className="px-4 py-2 bg-emerald-600 hover:bg-emerald-700 text-white rounded-lg font-medium disabled:opacity-50">
                执行环境恢复
              </button>
            </div>
          </div>

          {inspectRes && (
            <div className="p-3 rounded-lg bg-slate-50 border text-xs space-y-1.5">
              <div className="font-semibold text-slate-800">预检通过（导出于 {formatTime(inspectRes.exported_at)}）</div>
              <div>
                上游 {inspectRes.counts.upstreams} · 客户 {inspectRes.counts.clients} · 定时任务 {inspectRes.counts.cron_jobs} · 历史任务 {inspectRes.counts.backup_tasks} · 审计日志 {inspectRes.counts.audit_logs}
              </div>
              {(inspectRes.upstream_conflicts?.length || inspectRes.client_conflicts?.length) ? (
                <div className="text-amber-700 flex items-start gap-1">
                  <ShieldAlert className="w-4 h-4 shrink-0 mt-0.5" />
                  <span>
                    检测到冲突：{inspectRes.upstream_conflicts?.join(', ')} {inspectRes.client_conflicts?.join(', ')}
                  </span>
                </div>
              ) : (
                <div className="text-emerald-700">无资源名称/ID 冲突</div>
              )}
            </div>
          )}
        </div>
      </div>

      {/* System Settings */}
      {settings && (
        <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
          <h2 className="font-semibold text-slate-900">全局运行参数</h2>
          <form onSubmit={saveSettings} className="grid grid-cols-1 md:grid-cols-3 gap-4 text-sm">
            <div>
              <label className="block text-xs text-slate-600 mb-1">临时缓冲目录 (spool_dir)</label>
              <input value={settings.spool_dir} onChange={(e) => setSettings({ ...settings, spool_dir: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
            </div>
            <div>
              <label className="block text-xs text-slate-600 mb-1">公网回调 Base URL（Dropbox OAuth2）</label>
              <input value={settings.public_base_url} onChange={(e) => setSettings({ ...settings, public_base_url: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
            </div>
            <div>
              <label className="block text-xs text-slate-600 mb-1">默认单文件上限（字节）</label>
              <input type="number" value={settings.max_file_size_bytes} onChange={(e) => setSettings({ ...settings, max_file_size_bytes: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
            </div>
            <div>
              <label className="block text-xs text-slate-600 mb-1">同步模式超时（秒，超时转后台）</label>
              <input type="number" min={1} value={settings.sync_timeout_sec} onChange={(e) => setSettings({ ...settings, sync_timeout_sec: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
            </div>
            <div>
              <label className="block text-xs text-slate-600 mb-1">异步并发 Worker 数（重启生效）</label>
              <input type="number" min={1} value={settings.worker_concurrency} onChange={(e) => setSettings({ ...settings, worker_concurrency: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
            </div>
            <div>
              <label className="block text-xs text-slate-600 mb-1">失败最大重试次数</label>
              <input type="number" min={1} value={settings.max_retry_attempts} onChange={(e) => setSettings({ ...settings, max_retry_attempts: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
            </div>
            <div className="md:col-span-3 flex justify-end">
              <button type="submit" className="px-4 py-2 bg-slate-900 text-white rounded-lg font-medium">
                保存全局设置
              </button>
            </div>
          </form>
        </div>
      )}

      {/* Admins */}
      <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
        <h2 className="font-semibold text-slate-900">管理员账号</h2>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div className="space-y-2">
            {admins.map((a) => (
              <div key={a.id} className="flex items-center justify-between p-2.5 bg-slate-50 rounded-lg text-sm">
                <div>
                  <div className="font-medium">{a.username}</div>
                  <div className="text-xs text-slate-400">最近登录: {formatTime(a.last_login_at)}</div>
                </div>
                <div className="flex items-center gap-2">
                  <StatusBadge value={a.is_active ? 'enabled' : 'disabled'} label={a.is_active ? '启用' : '禁用'} />
                  <button
                    onClick={async () => {
                      await api.updateAdmin(a.id, { is_active: !a.is_active })
                      await load()
                    }}
                    className="text-xs text-indigo-600 hover:underline"
                  >
                    {a.is_active ? '禁用' : '启用'}
                  </button>
                </div>
              </div>
            ))}
          </div>
          <form onSubmit={handleCreateAdmin} className="space-y-2.5 text-sm border rounded-xl p-4">
            <div className="font-medium text-slate-800">新增管理员</div>
            <input required minLength={3} placeholder="用户名" value={newAdminUser} onChange={(e) => setNewAdminUser(e.target.value)} className="w-full px-3 py-1.5 border rounded-lg" />
            <input type="password" required minLength={8} placeholder="密码（>=8位）" value={newAdminPass} onChange={(e) => setNewAdminPass(e.target.value)} className="w-full px-3 py-1.5 border rounded-lg" />
            <button type="submit" className="px-4 py-1.5 bg-indigo-600 text-white rounded-lg text-xs font-medium">添加管理员</button>
          </form>
        </div>
      </div>
    </div>
  )
}
