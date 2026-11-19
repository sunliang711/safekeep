import React, { useEffect, useState } from 'react'
import { Download, Search } from 'lucide-react'
import { api, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import type { AuditLog } from '../types'

export const AuditPage: React.FC = () => {
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [total, setTotal] = useState(0)
  const [actorType, setActorType] = useState('')
  const [status, setStatus] = useState('')
  const [action, setAction] = useState('')
  const [q, setQ] = useState('')
  const [detail, setDetail] = useState<AuditLog | null>(null)

  const load = async () => {
    const params: Record<string, string> = { limit: '80' }
    if (actorType) params.actor_type = actorType
    if (status) params.status = status
    if (action) params.action = action
    if (q) params.q = q
    const res = await api.listAuditLogs(params)
    setLogs(res.items || [])
    setTotal(res.total || 0)
  }

  useEffect(() => {
    load()
  }, [actorType, status])

  const exportQuery = new URLSearchParams({ actor_type: actorType, status, action, q }).toString()

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900">审计日志</h1>
          <p className="text-sm text-slate-500">共 {total} 条记录，覆盖管理员配置变更、API 文件备份/下载、定时任务及环境迁移</p>
        </div>
        <div className="flex gap-2">
          <a
            href={`/admin/api/v1/audit-logs/export?format=csv&${exportQuery}`}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white border rounded-lg text-sm hover:bg-slate-50"
          >
            <Download className="w-4 h-4" /> 导出 CSV
          </a>
          <a
            href={`/admin/api/v1/audit-logs/export?format=json&${exportQuery}`}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white border rounded-lg text-sm hover:bg-slate-50"
          >
            <Download className="w-4 h-4" /> 导出 JSON
          </a>
        </div>
      </div>

      <div className="flex flex-wrap gap-3 bg-white p-3 rounded-xl border border-slate-200">
        <div className="flex items-center gap-2 flex-1 min-w-[200px] border rounded-lg px-3 py-1.5">
          <Search className="w-4 h-4 text-slate-400" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && load()}
            placeholder="搜索操作者 / 资源 ID / 详情..."
            className="w-full text-sm focus:outline-none"
          />
        </div>
        <input
          value={action}
          onChange={(e) => setAction(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && load()}
          placeholder="动作前缀 (如 backup.upload)"
          className="px-3 py-1.5 border rounded-lg text-sm"
        />
        <select value={actorType} onChange={(e) => setActorType(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white">
          <option value="">全部主体</option>
          <option value="admin">admin</option>
          <option value="client">client</option>
          <option value="system">system</option>
        </select>
        <select value={status} onChange={(e) => setStatus(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white">
          <option value="">全部结果</option>
          <option value="success">success</option>
          <option value="failure">failure</option>
        </select>
        <button onClick={load} className="px-4 py-1.5 bg-slate-900 text-white rounded-lg text-sm">
          查询
        </button>
      </div>

      <div className="bg-white rounded-xl border border-slate-200 overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="bg-slate-50 text-xs text-slate-500 border-b">
            <tr>
              <th className="text-left px-4 py-3">时间</th>
              <th className="text-left px-4 py-3">主体</th>
              <th className="text-left px-4 py-3">动作</th>
              <th className="text-left px-4 py-3">资源</th>
              <th className="text-left px-4 py-3">IP</th>
              <th className="text-left px-4 py-3">结果</th>
              <th className="text-right px-4 py-3">详情</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {logs.map((l) => (
              <tr key={l.id}>
                <td className="px-4 py-2.5 text-xs text-slate-500 whitespace-nowrap">{formatTime(l.created_at)}</td>
                <td className="px-4 py-2.5 text-xs">
                  <span className="px-1.5 py-0.5 bg-slate-100 rounded mr-1">{l.actor_type}</span>
                  <span className="font-medium text-slate-800">{l.actor_name}</span>
                </td>
                <td className="px-4 py-2.5 font-mono text-xs text-indigo-700">{l.action}</td>
                <td className="px-4 py-2.5 text-xs text-slate-600">{l.resource_type} {l.resource_id ? `· ${l.resource_id}` : ''}</td>
                <td className="px-4 py-2.5 font-mono text-xs text-slate-500">{l.ip_address || '-'}</td>
                <td className="px-4 py-2.5"><StatusBadge value={l.status} /></td>
                <td className="px-4 py-2.5 text-right">
                  <button onClick={() => setDetail(l)} className="text-xs text-indigo-600 hover:underline">查看</button>
                </td>
              </tr>
            ))}
            {logs.length === 0 && (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-slate-400">暂无审计日志。</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {detail && (
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center p-4 z-50">
          <div className="bg-white rounded-2xl max-w-xl w-full p-6 space-y-3">
            <h2 className="text-lg font-bold">审计详情 #{detail.id}</h2>
            <pre className="bg-slate-900 text-slate-100 p-4 rounded-xl text-xs overflow-auto max-h-96">
              {JSON.stringify(
                { ...detail, detail: (() => { try { return JSON.parse(detail.detail_json) } catch { return detail.detail_json } })() },
                null,
                2,
              )}
            </pre>
            <div className="flex justify-end">
              <button onClick={() => setDetail(null)} className="px-4 py-2 border rounded-lg text-sm">关闭</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
