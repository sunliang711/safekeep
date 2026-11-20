import React, { useState } from 'react'
import { Download, ShieldAlert, Upload } from 'lucide-react'
import { api, formatTime } from '../api'
import { useToast } from '../components/Toast'
import type { InspectSummary } from '../types'

export const MigrationPage: React.FC = () => {
  const toast = useToast()

  // Migration Export
  const [expPass, setExpPass] = useState('')
  const [expTasks, setExpTasks] = useState(true)
  const [expAdmins, setExpAdmins] = useState(false)
  const [expAudit, setExpAudit] = useState(false)
  const [exporting, setExporting] = useState(false)

  // Migration Import
  const [impFile, setImpFile] = useState<File | null>(null)
  const [impPass, setImpPass] = useState('')
  const [impMode, setImpMode] = useState<'merge' | 'replace'>('merge')
  const [impConflict, setImpConflict] = useState<'skip' | 'overwrite'>('skip')
  const [impTasks, setImpTasks] = useState(true)
  const [impAdmins, setImpAdmins] = useState(false)
  const [impAudit, setImpAudit] = useState(false)
  const [inspectRes, setInspectRes] = useState<InspectSummary | null>(null)
  const [inspecting, setInspecting] = useState(false)
  const [importing, setImporting] = useState(false)

  const handleExport = async (e: React.FormEvent) => {
    e.preventDefault()
    if (expPass !== '' && expPass.length < 6) {
      toast.error('迁移口令若设置，需至少 6 位（或留空不设口令）')
      return
    }
    setExporting(true)
    try {
      await api.exportMigration({
        include_tasks: expTasks,
        include_admins: expAdmins,
        include_audit_logs: expAudit,
        passphrase: expPass,
      })
      toast.success('加密迁移包已生成并开始下载')
    } catch (e: any) {
      toast.error(e.message || '导出迁移包失败')
    } finally {
      setExporting(false)
    }
  }

  const handleInspect = async () => {
    if (!impFile) return
    setInspecting(true)
    const fd = new FormData()
    fd.append('bundle', impFile)
    fd.append('passphrase', impPass)
    try {
      const summary = await api.inspectMigration(fd)
      setInspectRes(summary)
      toast.info('迁移包预检完成')
    } catch (e: any) {
      setInspectRes(null)
      toast.error(e.message || '预检迁移包失败')
    } finally {
      setInspecting(false)
    }
  }

  const handleImport = async () => {
    if (!impFile || !inspectRes || importing) return
    if (impMode === 'replace' && !confirm('确定使用「覆盖恢复」模式？当前环境的上游与客户配置将被清空替换为迁移包快照！')) {
      return
    }
    setImporting(true)
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
      toast.success(
        `环境恢复成功！已导入上游 ${res.counts.upstreams} 个、客户 ${res.counts.clients} 个、定时任务 ${res.counts.cron_jobs} 个、历史任务 ${res.counts.backup_tasks} 条。`
      )
      setInspectRes(null)
    } catch (e: any) {
      toast.error(e.message || '环境恢复失败')
    } finally {
      setImporting(false)
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-slate-900">环境迁移与恢复</h1>
        <p className="text-sm text-slate-500">
          导出加密迁移包（.skbundle）快速迁移到新服务器或灾备节点，支持预检冲突与事务化恢复
        </p>
      </div>

      <div className="flex flex-wrap items-center gap-3 rounded-xl border border-indigo-100 bg-indigo-50/60 p-4 text-xs text-indigo-800"><span className="font-semibold">迁移流程</span><span>01 生成加密迁移包</span><span aria-hidden="true">→</span><span>02 上传并预检</span><span aria-hidden="true">→</span><span>03 确认后恢复</span></div>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
          <div className="flex items-center gap-2 font-semibold text-slate-900">
            <Download className="w-4 h-4 text-indigo-600" />
            <span>导出环境迁移包（.skbundle）</span>
          </div>
          <p className="text-xs text-slate-500">
            所有上游密钥（S3 / R2 Secret、GCS 服务账号密钥、Dropbox Token）、SMTP 密码与代理密码将使用临时迁移口令重加密打包；客户 API Key 哈希保留，迁移后客户端无需更换密钥。
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
              <label htmlFor="migration-field-1" className="block text-xs text-slate-600 mb-1">设置迁移加密口令（可选，留空则不设口令；如设置需至少 6 位）</label>
              <input id="migration-field-1"
                type="password"
                value={expPass}
                onChange={(e) => setExpPass(e.target.value)}
                placeholder="可选，留空则不设口令"
                className="w-full px-3 py-2 border rounded-lg"
              />
            </div>
            <button
              type="submit"
              disabled={exporting}
              className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-50 text-white rounded-lg font-medium"
            >
              {exporting ? '打包导出中...' : '生成并下载迁移包'}
            </button>
          </form>
        </div>

        <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
          <div className="flex items-center gap-2 font-semibold text-slate-900">
            <Upload className="w-4 h-4 text-emerald-600" />
            <span>导入恢复环境</span>
          </div>
          <p className="text-xs text-slate-500">
            上传 .skbundle 并输入迁移口令。先点击「预检包内容」查看清单与冲突，确认后执行事务导入并自动使用本机主密钥重新加密。
          </p>
          <div className="space-y-3 text-sm">
            <div>
              <label htmlFor="migration-field-2" className="block text-xs text-slate-600 mb-1">选择迁移包文件（.skbundle）</label>
              <input id="migration-field-2"
                type="file"
                accept=".skbundle"
                aria-label="选择迁移包文件"
                disabled={inspecting || importing}
                onChange={(e) => {
                  setImpFile(e.target.files?.[0] || null)
                  setInspectRes(null)
                }}
                className="w-full text-xs"
              />
            </div>
            <div>
              <label htmlFor="migration-field-3" className="block text-xs text-slate-600 mb-1">迁移包解密口令（如导出时未设口令请留空）</label>
              <input id="migration-field-3" aria-label="迁移包解密口令" autoComplete="off" disabled={inspecting || importing} type="password" value={impPass} onChange={(e) => { setImpPass(e.target.value); setInspectRes(null) }} placeholder="如导出时未设口令可留空" className="w-full px-3 py-2 border rounded-lg" />
            </div>
            <div className="grid grid-cols-2 gap-3 text-xs">
              <div>
                <label htmlFor="migration-field-4" className="block text-slate-600 mb-1">恢复模式</label>
                <select id="migration-field-4" value={impMode} onChange={(e) => setImpMode(e.target.value as any)} className="w-full px-2.5 py-1.5 border rounded-lg bg-white">
                  <option value="merge">增量合并</option>
                  <option value="replace">清空覆盖恢复</option>
                </select>
              </div>
              {impMode === 'merge' && (
                <div>
                  <label htmlFor="migration-field-5" className="block text-slate-600 mb-1">冲突处理策略</label>
                  <select id="migration-field-5" value={impConflict} onChange={(e) => setImpConflict(e.target.value as any)} className="w-full px-2.5 py-1.5 border rounded-lg bg-white">
                    <option value="skip">跳过已存在项</option>
                    <option value="overwrite">覆盖同名/同 ID 项</option>
                  </select>
                </div>
              )}
            </div>
            <div className="flex flex-wrap gap-4 text-xs">
              <label className="flex items-center gap-1.5">
                <input type="checkbox" checked={impTasks} onChange={(e) => setImpTasks(e.target.checked)} />
                导入历史备份索引
              </label>
              <label className="flex items-center gap-1.5">
                <input type="checkbox" checked={impAdmins} onChange={(e) => setImpAdmins(e.target.checked)} />
                导入管理员
              </label>
              <label className="flex items-center gap-1.5">
                <input type="checkbox" checked={impAudit} onChange={(e) => setImpAudit(e.target.checked)} />
                导入审计日志
              </label>
            </div>
            <p className="text-xs text-slate-500">{inspectRes ? '预检完成，请核对下方清单与冲突后执行恢复。' : '先预检包内容，确认成功后即可执行环境恢复。'}</p>
            <div className="flex flex-wrap gap-2">
              <button
                type="button"
                onClick={handleInspect}
                disabled={!impFile || inspecting || importing}
                className="px-4 py-2 border rounded-lg hover:bg-slate-50 disabled:opacity-50"
              >
                {inspecting ? '预检中...' : '预检包内容'}
              </button>
              <button
                type="button"
                onClick={handleImport}
                disabled={!impFile || !inspectRes || inspecting || importing}
                className="px-4 py-2 bg-emerald-600 hover:bg-emerald-700 text-white rounded-lg font-medium disabled:opacity-50"
              >
                {importing ? '导入中...' : '执行环境恢复'}
              </button>
            </div>
          </div>

          {inspectRes && (
            <div className="p-3 rounded-lg bg-slate-50 border text-xs space-y-1.5">
              <div className="font-semibold text-slate-800">预检通过（导出于 {formatTime(inspectRes.exported_at)}）</div>
              <div>
                上游 {inspectRes.counts.upstreams} · 客户 {inspectRes.counts.clients} · 定时任务 {inspectRes.counts.cron_jobs} · 历史任务{' '}
                {inspectRes.counts.backup_tasks} · 审计日志 {inspectRes.counts.audit_logs}
              </div>
              {inspectRes.upstream_conflicts?.length || inspectRes.client_conflicts?.length || inspectRes.admin_conflicts?.length ? (
                <div className="text-amber-700 flex items-start gap-1">
                  <ShieldAlert className="w-4 h-4 shrink-0 mt-0.5" />
                  <span>
                    检测到冲突：
                    {[
                      inspectRes.upstream_conflicts?.length ? `上游(${inspectRes.upstream_conflicts.join(', ')})` : '',
                      inspectRes.client_conflicts?.length ? `客户(${inspectRes.client_conflicts.join(', ')})` : '',
                      inspectRes.admin_conflicts?.length ? `管理员(${inspectRes.admin_conflicts.join(', ')})` : '',
                    ]
                      .filter(Boolean)
                      .join(' · ')}
                  </span>
                </div>
              ) : (
                <div className="text-emerald-700">无资源名称/ID 冲突</div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
