import React, { useEffect, useRef, useState } from 'react'
import { Download, Search } from 'lucide-react'
import { EmptyState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { api, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import type { AuditLog } from '../types'

export const AuditPage: React.FC = () => {
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const requestId = useRef(0)
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const limit = 50
  const [actorType, setActorType] = useState('')
  const [status, setStatus] = useState('')
  const [action, setAction] = useState('')
  const [q, setQ] = useState('')
  const [fromDate, setFromDate] = useState('')
  const [toDate, setToDate] = useState('')
  const [pageErr, setPageErr] = useState('')
  const [exportQuery, setExportQuery] = useState('')
  const [detail, setDetail] = useState<AuditLog | null>(null)

  const buildParams = (includePage: boolean, targetOffset = offset, filters = { actorType, status, action, q, fromDate, toDate }) => {
    const params: Record<string, string> = {}
    if (includePage) {
      params.limit = String(limit)
      params.offset = String(targetOffset)
    }
    if (filters.actorType) params.actor_type = filters.actorType
    if (filters.status) params.status = filters.status
    if (filters.action) params.action = filters.action
    if (filters.q) params.q = filters.q
    if (filters.fromDate) params.from = new Date(filters.fromDate).toISOString()
    if (filters.toDate) params.to = new Date(filters.toDate).toISOString()
    return params
  }

  const load = async (targetOffset = offset, filters = { actorType, status, action, q, fromDate, toDate }) => {
    const currentRequest = ++requestId.current
    setLoading(true)
    setPageErr('')
    try {
      if (filters.fromDate && filters.toDate && new Date(filters.fromDate) > new Date(filters.toDate)) throw new Error('开始时间不能晚于结束时间')
      const res = await api.listAuditLogs(buildParams(true, targetOffset, filters))
      if (currentRequest !== requestId.current) return
      setExportQuery(new URLSearchParams(buildParams(false, targetOffset, filters)).toString())
      setLogs(res.items || [])
      setTotal(res.total || 0)
    } catch (err: any) {
      if (currentRequest === requestId.current) setPageErr(err.message || '加载审计日志失败')
    } finally {
      if (currentRequest === requestId.current) setLoading(false)
    }
  }

  useEffect(() => {
    setOffset(0)
    load(0)
  }, [actorType, status])

  const resetFilters = () => {
    setActorType('')
    setStatus('')
    setAction('')
    setQ('')
    setFromDate('')
    setToDate('')
    setOffset(0)
    load(0, { actorType: '', status: '', action: '', q: '', fromDate: '', toDate: '' })
  }

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

      <div className="filter-bar flex flex-wrap gap-3 bg-white p-4 rounded-xl border border-slate-200">
        <div className="flex items-center gap-2 flex-1 min-w-[200px] border rounded-lg px-3 py-1.5">
          <Search className="w-4 h-4 text-slate-400" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') { setOffset(0); load(0) } }}
            aria-label="搜索审计日志"
            placeholder="搜索操作者 / 资源 ID / 详情..."
            className="w-full text-sm focus:outline-none"
          />
        </div>
        <input
          value={action}
          onChange={(e) => setAction(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') { setOffset(0); load(0) } }}
          aria-label="动作前缀"
          placeholder="动作前缀 (如 backup.upload)"
          className="px-3 py-1.5 border rounded-lg text-sm"
        />
        <select aria-label="操作主体" value={actorType} onChange={(e) => setActorType(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white">
          <option value="">全部主体</option>
          <option value="admin">管理员</option>
          <option value="client">客户</option>
          <option value="system">系统</option>
        </select>
        <select aria-label="操作结果" value={status} onChange={(e) => setStatus(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white">
          <option value="">全部结果</option>
          <option value="success">成功</option>
          <option value="failure">失败</option>
        </select>
        <input type="datetime-local" value={fromDate} onChange={(e) => setFromDate(e.target.value)} aria-label="开始时间" title="开始时间" className="px-2.5 py-1.5 border rounded-lg text-xs" />
        <input type="datetime-local" value={toDate} onChange={(e) => setToDate(e.target.value)} aria-label="结束时间" title="结束时间" className="px-2.5 py-1.5 border rounded-lg text-xs" />
        <button disabled={loading} onClick={() => { setOffset(0); load(0) }} className="px-4 py-1.5 bg-slate-900 text-white rounded-lg text-sm">
          {loading ? '查询中…' : '查询'}
        </button>
        {(q || action || actorType || status || fromDate || toDate) && <button onClick={resetFilters} className="text-sm text-slate-500 px-2 hover:text-indigo-600">重置筛选</button>}
      </div>

      {pageErr && <div className="p-3 bg-rose-50 border border-rose-200 text-rose-700 rounded-lg text-sm">{pageErr}</div>}

      <div className="bg-white rounded-xl border border-slate-200 overflow-x-auto">
        <table aria-busy={loading} className="audit-table w-full text-sm">
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
            {!loading && logs.map((l) => (
              <tr key={l.id}>
                <td className="px-4 py-2.5 text-xs text-slate-500 whitespace-nowrap">{formatTime(l.created_at)}</td>
                <td className="px-4 py-2.5 text-xs">
                  <span className="px-1.5 py-0.5 bg-slate-100 rounded mr-1">{{ admin: '管理员', client: '客户', system: '系统' }[l.actor_type] || l.actor_type}</span>
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
            {(loading || logs.length === 0) && (
              <tr>
                <td colSpan={7}><EmptyState title={loading ? '正在加载审计日志…' : '暂无匹配的审计记录'} description={loading ? undefined : '调整筛选条件，或等待新的操作记录。'} /></td>
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
            onClick={() => { const next = Math.max(0, offset - limit); setOffset(next); load(next) }}
            className="px-3 py-1.5 bg-white border rounded-lg disabled:opacity-40"
          >
            上一页
          </button>
          <button
            disabled={loading || offset + limit >= total}
            onClick={() => { const next = offset + limit; setOffset(next); load(next) }}
            className="px-3 py-1.5 bg-white border rounded-lg disabled:opacity-40"
          >
            下一页
          </button>
        </div>
      </div>

      {detail && (
        <Modal title={`审计详情 #${detail.id}`} onClose={() => setDetail(null)}>
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
        </Modal>
      )}
    </div>
  )
}
