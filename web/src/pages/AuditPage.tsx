import React, { useEffect, useRef, useState } from 'react'
import { Download, Search } from 'lucide-react'
import { EmptyState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { api, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useI18n } from '../contexts/I18nContext'
import type { AuditLog } from '../types'

export const AuditPage: React.FC = () => {
  const { t, lang } = useI18n()
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
      if (filters.fromDate && filters.toDate && new Date(filters.fromDate) > new Date(filters.toDate)) {
        throw new Error(t('audit.errDateRange'))
      }
      const res = await api.listAuditLogs(buildParams(true, targetOffset, filters))
      if (currentRequest !== requestId.current) return
      setExportQuery(new URLSearchParams(buildParams(false, targetOffset, filters)).toString())
      setLogs(res.items || [])
      setTotal(res.total || 0)
    } catch (err: any) {
      if (currentRequest === requestId.current) setPageErr(err.message || t('audit.loadError'))
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

  const actorLabels: Record<string, string> = {
    admin: t('audit.actorAdmin'),
    client: t('audit.actorClient'),
    system: t('audit.actorSystem'),
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">{t('audit.title')}</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">{t('audit.subtitle', { total })}</p>
        </div>
        <div className="flex gap-2">
          <a
            href={`/admin/api/v1/audit-logs/export?format=csv&${exportQuery}`}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg text-sm hover:bg-slate-50 dark:hover:bg-slate-700"
          >
            <Download className="w-4 h-4" /> {t('audit.exportCsv')}
          </a>
          <a
            href={`/admin/api/v1/audit-logs/export?format=json&${exportQuery}`}
            className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg text-sm hover:bg-slate-50 dark:hover:bg-slate-700"
          >
            <Download className="w-4 h-4" /> {t('audit.exportJson')}
          </a>
        </div>
      </div>

      <div className="filter-bar flex flex-wrap gap-3 bg-white dark:bg-slate-900 p-4 rounded-xl border border-slate-200 dark:border-slate-800">
        <div className="flex items-center gap-2 flex-1 min-w-[200px] border rounded-lg px-3 py-1.5">
          <Search className="w-4 h-4 text-slate-400" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') { setOffset(0); load(0) } }}
            aria-label={t('audit.searchAria')}
            placeholder={t('audit.searchPlaceholder')}
            className="w-full text-sm focus:outline-none bg-transparent"
          />
        </div>
        <input
          value={action}
          onChange={(e) => setAction(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') { setOffset(0); load(0) } }}
          aria-label={t('audit.actionAria')}
          placeholder={t('audit.actionPlaceholder')}
          className="px-3 py-1.5 border rounded-lg text-sm"
        />
        <select aria-label={t('audit.actorAria')} value={actorType} onChange={(e) => setActorType(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white dark:bg-slate-900">
          <option value="">{t('audit.actorAll')}</option>
          <option value="admin">{t('audit.actorAdmin')}</option>
          <option value="client">{t('audit.actorClient')}</option>
          <option value="system">{t('audit.actorSystem')}</option>
        </select>
        <select aria-label={t('audit.statusAria')} value={status} onChange={(e) => setStatus(e.target.value)} className="px-3 py-1.5 border rounded-lg text-sm bg-white dark:bg-slate-900">
          <option value="">{t('audit.statusAll')}</option>
          <option value="success">{t('status.success')}</option>
          <option value="failure">{t('status.failure')}</option>
        </select>
        <input type="datetime-local" value={fromDate} onChange={(e) => setFromDate(e.target.value)} aria-label={t('audit.fromDateAria')} title={t('audit.fromDateAria')} className="px-2.5 py-1.5 border rounded-lg text-xs" />
        <input type="datetime-local" value={toDate} onChange={(e) => setToDate(e.target.value)} aria-label={t('audit.toDateAria')} title={t('audit.toDateAria')} className="px-2.5 py-1.5 border rounded-lg text-xs" />
        <button disabled={loading} onClick={() => { setOffset(0); load(0) }} className="px-4 py-1.5 bg-slate-900 dark:bg-indigo-600 hover:bg-slate-800 dark:hover:bg-indigo-500 text-white rounded-lg text-sm">
          {loading ? t('common.searching') : t('common.search')}
        </button>
        {(q || action || actorType || status || fromDate || toDate) && (
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
        <table aria-busy={loading} className="audit-table w-full text-sm">
          <thead className="bg-slate-50 dark:bg-slate-950/60 text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-800">
            <tr>
              <th className="text-left px-4 py-3">{t('audit.colTime')}</th>
              <th className="text-left px-4 py-3">{t('audit.colActor')}</th>
              <th className="text-left px-4 py-3">{t('audit.colAction')}</th>
              <th className="text-left px-4 py-3">{t('audit.colResource')}</th>
              <th className="text-left px-4 py-3">{t('audit.colIp')}</th>
              <th className="text-left px-4 py-3">{t('audit.colResult')}</th>
              <th className="text-right px-4 py-3">{t('audit.colDetail')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
            {!loading && logs.map((l) => (
              <tr key={l.id}>
                <td className="px-4 py-2.5 text-xs text-slate-500 dark:text-slate-400 whitespace-nowrap">{formatTime(l.created_at, lang)}</td>
                <td className="px-4 py-2.5 text-xs">
                  <span className="px-1.5 py-0.5 bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300 rounded mr-1">
                    {actorLabels[l.actor_type] || l.actor_type}
                  </span>
                  <span className="font-medium text-slate-800 dark:text-slate-100">{l.actor_name}</span>
                </td>
                <td className="px-4 py-2.5 font-mono text-xs text-indigo-700 dark:text-indigo-400">{l.action}</td>
                <td className="px-4 py-2.5 text-xs text-slate-600 dark:text-slate-300">{l.resource_type} {l.resource_id ? `· ${l.resource_id}` : ''}</td>
                <td className="px-4 py-2.5 font-mono text-xs text-slate-500 dark:text-slate-400">{l.ip_address || '-'}</td>
                <td className="px-4 py-2.5"><StatusBadge value={l.status} /></td>
                <td className="px-4 py-2.5 text-right">
                  <button onClick={() => setDetail(l)} className="text-xs text-indigo-600 dark:text-indigo-400 hover:underline">
                    {t('common.view')}
                  </button>
                </td>
              </tr>
            ))}
            {(loading || logs.length === 0) && (
              <tr>
                <td colSpan={7}>
                  <EmptyState
                    title={loading ? t('audit.loadingTitle') : t('audit.emptyTitle')}
                    description={loading ? undefined : t('audit.emptyDesc')}
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
            onClick={() => { const next = Math.max(0, offset - limit); setOffset(next); load(next) }}
            className="px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg disabled:opacity-40"
          >
            {t('common.prevPage')}
          </button>
          <button
            disabled={loading || offset + limit >= total}
            onClick={() => { const next = offset + limit; setOffset(next); load(next) }}
            className="px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg disabled:opacity-40"
          >
            {t('common.nextPage')}
          </button>
        </div>
      </div>

      {detail && (
        <Modal title={t('audit.detailTitle', { id: detail.id })} onClose={() => setDetail(null)}>
          <div className="bg-white dark:bg-slate-900 rounded-2xl max-w-xl w-full p-6 space-y-3">
            <h2 className="text-lg font-bold text-slate-900 dark:text-slate-100">{t('audit.detailTitle', { id: detail.id })}</h2>
            <pre className="bg-slate-900 dark:bg-slate-950 border border-slate-800 text-slate-100 p-4 rounded-xl text-xs overflow-auto max-h-96">
              {JSON.stringify(
                { ...detail, detail: (() => { try { return JSON.parse(detail.detail_json) } catch { return detail.detail_json } })() },
                null,
                2,
              )}
            </pre>
            <div className="flex justify-end">
              <button onClick={() => setDetail(null)} className="px-4 py-2 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-800 rounded-lg text-sm">
                {t('common.close')}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
