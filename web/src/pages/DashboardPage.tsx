import React, { useEffect, useState } from 'react'
import { AlertTriangle, ArrowRight, CheckCircle2, Cloud, FileArchive, HardDrive, RefreshCw, ShieldCheck, Upload } from 'lucide-react'
import { api, formatBytes, formatTime } from '../api'
import { EmptyState, LoadingState } from '../components/Feedback'
import { useToast } from '../components/Toast'
import { useConfirm } from '../components/ConfirmDialog'
import { useI18n } from '../contexts/I18nContext'
import type { NavTab } from '../components/Layout'
import { StatusBadge } from '../components/Badge'
import { APIUploadsChart } from '../components/APIUploadsChart'
import type { DashboardSummary } from '../types'

export const DashboardPage: React.FC<{ onNavigate: (tab: NavTab) => void }> = ({ onNavigate }) => {
  const askConfirm = useConfirm()
  const toast = useToast()
  const { t, lang } = useI18n()
  const [resetting, setResetting] = useState(false)
  const [data, setData] = useState<DashboardSummary | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = async () => {
    setLoading(true)
    setError('')
    try {
      setData(await api.dashboard())
    } catch (e: any) {
      setError(e.message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  if (loading && !data) return <LoadingState label={t('dashboard.loading')} />
  if (error && !data) {
    return (
      <div role="alert" className="p-4 bg-rose-50 dark:bg-rose-500/15 text-rose-700 dark:text-rose-300 border border-rose-200 dark:border-rose-500/30 rounded-lg text-sm">
        {error}
        <button onClick={load} className="ml-4 underline">{t('common.reload')}</button>
      </div>
    )
  }
  if (!data) return null

  const cards = [
    {
      icon: <FileArchive className="w-5 h-5" />,
      label: t('dashboard.card.totalTasks'),
      value: data.total_tasks,
      sub: t('dashboard.card.totalTasksSub', { succeeded: data.succeeded_tasks, dedup: data.deduplicated_tasks }),
    },
    {
      icon: <CheckCircle2 className="w-5 h-5" />,
      label: t('dashboard.card.pendingFailed'),
      value: `${data.pending_tasks} / ${data.failed_tasks}`,
      sub: t('dashboard.card.pendingFailedSub'),
    },
    {
      icon: <HardDrive className="w-5 h-5" />,
      label: t('dashboard.card.totalBytes'),
      value: formatBytes(data.total_bytes_backed),
      sub: t('dashboard.card.totalBytesSub', { spool: formatBytes(data.spool_bytes_used) }),
    },
    {
      icon: <Cloud className="w-5 h-5" />,
      label: t('dashboard.card.upstreamsClients'),
      value: `${data.healthy_upstreams}/${data.enabled_upstreams} · ${data.enabled_clients}`,
      sub: t('dashboard.card.upstreamsClientsSub'),
    },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">{t('dashboard.title')}</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">{t('dashboard.subtitle')}</p>
        </div>
        <button
          onClick={load}
          disabled={loading}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 rounded-lg text-sm hover:bg-slate-50 dark:hover:bg-slate-700"
        >
          <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />
          {t('common.refresh')}
        </button>
      </div>

      {error && (
        <div role="alert" className="p-3 rounded-xl bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 text-sm">
          {error}
        </div>
      )}

      <div className="grid grid-cols-1 xl:grid-cols-12 gap-5 items-stretch">
        <div className="xl:col-span-6 flex flex-col gap-4">
          <div className="rounded-2xl border border-indigo-100 dark:border-indigo-500/25 bg-gradient-to-r from-indigo-50 via-white to-white dark:from-indigo-950/60 dark:via-slate-900 dark:to-slate-900 p-5 sm:p-6 flex flex-wrap items-center justify-between gap-4">
            <div className="flex gap-4 items-center min-w-0">
              <div className="hidden sm:grid w-11 h-11 place-items-center rounded-2xl bg-white dark:bg-indigo-500/15 border border-indigo-100 dark:border-indigo-500/30 text-indigo-500 dark:text-indigo-300 shrink-0">
                <ShieldCheck className="w-6 h-6" />
              </div>
              <div className="min-w-0">
                <div className="text-xs font-medium text-indigo-500 dark:text-indigo-400 mb-1">{t('dashboard.hero.eyebrow')}</div>
                <h2 className="text-lg font-semibold text-slate-900 dark:text-slate-100">
                  {data.enabled_upstreams > 0 ? t('dashboard.hero.titleReady') : t('dashboard.hero.titleEmpty')}
                </h2>
                <p className="text-xs text-slate-500 dark:text-slate-400 mt-1.5">
                  {data.enabled_upstreams > 0 ? t('dashboard.hero.descReady') : t('dashboard.hero.descEmpty')}
                </p>
              </div>
            </div>
            <button className="primary-button shrink-0" onClick={() => onNavigate(data.enabled_upstreams > 0 ? 'tasks' : 'upstreams')}>
              {data.enabled_upstreams > 0 ? <Upload className="w-4 h-4" /> : <Cloud className="w-4 h-4" />}
              {data.enabled_upstreams > 0 ? t('dashboard.hero.btnReady') : t('dashboard.hero.btnEmpty')}
              <ArrowRight className="w-3.5 h-3.5" />
            </button>
          </div>

          <div className="grid grid-cols-1 min-[380px]:grid-cols-2 lg:grid-cols-4 xl:grid-cols-2 gap-4 flex-1">
            {cards.map((c) => (
              <div key={c.label} className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5 flex flex-col justify-between">
                <div className="flex items-center justify-between gap-2 text-xs text-slate-500 dark:text-slate-400">
                  {c.label}
                  <span className="text-indigo-400 bg-indigo-50 dark:bg-indigo-500/15 p-2 rounded-lg shrink-0">{c.icon}</span>
                </div>
                <div className="mt-2">
                  <div className="text-2xl font-semibold tracking-tight text-slate-900 dark:text-slate-100 tabular-nums">{c.value}</div>
                  <div className="text-xs text-slate-400 dark:text-slate-400 mt-1">{c.sub}</div>
                </div>
              </div>
            ))}
          </div>
        </div>

        <div className="xl:col-span-6">
          <APIUploadsChart stats={data.api_uploads} />
        </div>
      </div>

      {data.daily_traffic && (
        <div
          className={`rounded-xl border p-4 flex flex-wrap items-center justify-between gap-4 ${
            data.daily_traffic.tripped
              ? 'bg-rose-50 dark:bg-rose-500/15 border-rose-200 dark:border-rose-500/30 text-rose-900 dark:text-rose-200'
              : 'bg-white dark:bg-slate-900 border-slate-200 dark:border-slate-800 text-slate-800 dark:text-slate-200'
          }`}
        >
          <div className="flex items-center gap-3">
            {data.daily_traffic.tripped ? (
              <AlertTriangle className="w-5 h-5 text-rose-600 dark:text-rose-400 shrink-0" />
            ) : (
              <ShieldCheck className="w-5 h-5 text-emerald-600 dark:text-emerald-400 shrink-0" />
            )}
            <div>
              <div className="text-sm font-semibold flex items-center gap-2 flex-wrap">
                <span>{t('dashboard.traffic.title', { date: data.daily_traffic.date_key })}</span>
                <StatusBadge
                  value={data.daily_traffic.tripped ? 'failed' : 'ok'}
                  label={data.daily_traffic.tripped ? t('dashboard.traffic.tripped') : t('dashboard.traffic.normal')}
                />
              </div>
              <div className="text-xs opacity-80 mt-0.5">
                {t('dashboard.traffic.used', {
                  used: formatBytes(data.daily_traffic.bytes_used),
                  limit: data.daily_traffic.limit_bytes > 0 ? formatBytes(data.daily_traffic.limit_bytes) : t('common.unlimited'),
                })}
                {data.daily_traffic.tripped_at && t('dashboard.traffic.trippedAt', { time: formatTime(data.daily_traffic.tripped_at, lang) })}
              </div>
            </div>
          </div>
          <button
            onClick={async () => {
              if (!await askConfirm(t('dashboard.traffic.confirmReset'), { danger: true })) return
              setResetting(true)
              try {
                await api.resetCircuitBreaker()
                toast.success(t('dashboard.traffic.resetSuccess'))
                await load()
              } catch (e: any) {
                toast.error(e.message || t('dashboard.traffic.resetFailed'))
              } finally {
                setResetting(false)
              }
            }}
            disabled={resetting}
            className={`px-3.5 py-1.5 rounded-lg text-xs font-medium ${
              data.daily_traffic.tripped
                ? 'bg-rose-600 hover:bg-rose-700 text-white'
                : 'bg-slate-100 dark:bg-slate-800 hover:bg-slate-200 dark:hover:bg-slate-700 text-slate-700 dark:text-slate-200'
            }`}
          >
            {resetting
              ? t('dashboard.traffic.resetting')
              : data.daily_traffic.tripped
              ? t('dashboard.traffic.resetTrippedBtn')
              : t('dashboard.traffic.resetNormalBtn')}
          </button>
        </div>
      )}

      {data.daily_traffic && data.daily_traffic.limit_bytes > 0 && (
        <div
          className="-mt-3 px-1"
          role="progressbar"
          aria-label={t('dashboard.traffic.progressAria')}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.min(100, Math.round((data.daily_traffic.bytes_used / data.daily_traffic.limit_bytes) * 100))}
        >
          <div className="h-1.5 rounded-full bg-slate-200 dark:bg-slate-800 overflow-hidden">
            <div
              className={`h-full rounded-full ${data.daily_traffic.tripped ? 'bg-rose-500' : 'bg-indigo-500'}`}
              style={{ width: `${Math.min(100, (data.daily_traffic.bytes_used / data.daily_traffic.limit_bytes) * 100)}%` }}
            />
          </div>
        </div>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5">
          <h2 className="font-semibold text-slate-900 dark:text-slate-100 mb-3">{t('dashboard.upstreams.title')}</h2>
          {data.upstreams && data.upstreams.length > 0 ? (
            <div className="space-y-3">
              {data.upstreams.map((u) => (
                <div key={u.id} className="flex items-center justify-between p-2.5 rounded-lg bg-slate-50 dark:bg-slate-800/60 border border-slate-100 dark:border-slate-800">
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-slate-900 dark:text-slate-100 truncate">{u.name}</div>
                    <div className="text-xs text-slate-500 dark:text-slate-400">
                      {u.type.toUpperCase()} · {u.proxy_enabled ? t('common.proxy', { type: u.proxy_type.toUpperCase() }) : t('common.direct')}
                    </div>
                  </div>
                  <StatusBadge value={u.enabled ? u.last_test_status : 'disabled'} label={u.enabled ? undefined : t('common.disabled')} />
                </div>
              ))}
            </div>
          ) : (
            <EmptyState
              icon={<Cloud className="w-6 h-6" />}
              title={t('dashboard.upstreams.emptyTitle')}
              description={t('dashboard.upstreams.emptyDesc')}
              action={
                <button onClick={() => onNavigate('upstreams')} className="text-xs text-indigo-600 dark:text-indigo-400 font-medium inline-flex items-center gap-1">
                  {t('dashboard.upstreams.addAction')}<ArrowRight className="w-3 h-3" />
                </button>
              }
            />
          )}
        </div>

        <div className="lg:col-span-2 bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5">
          <h2 className="font-semibold text-slate-900 dark:text-slate-100 mb-3">{t('dashboard.recent.title')}</h2>
          {data.recent_tasks && data.recent_tasks.length > 0 ? (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-xs text-slate-500 dark:text-slate-400 border-b border-slate-100 dark:border-slate-800">
                    <th className="pb-2">{t('dashboard.recent.colFile')}</th>
                    <th className="pb-2">{t('dashboard.recent.colClient')}</th>
                    <th className="pb-2">{t('dashboard.recent.colMode')}</th>
                    <th className="pb-2">{t('dashboard.recent.colSize')}</th>
                    <th className="pb-2">{t('dashboard.recent.colStatus')}</th>
                    <th className="pb-2">{t('dashboard.recent.colTime')}</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {data.recent_tasks.map((tItem) => (
                    <tr key={tItem.id}>
                      <td className="py-2.5 font-medium text-slate-800 dark:text-slate-100 max-w-[180px] truncate">
                        {tItem.original_filename}
                        {tItem.deduplicated && (
                          <span className="ml-1.5 text-[10px] px-1.5 py-0.5 bg-emerald-100 dark:bg-emerald-500/20 text-emerald-700 dark:text-emerald-300 rounded">
                            {t('common.dedupTag')}
                          </span>
                        )}
                      </td>
                      <td className="py-2.5 text-slate-600 dark:text-slate-300">{tItem.client_name || tItem.client_id}</td>
                      <td className="py-2.5"><StatusBadge value={tItem.mode} /></td>
                      <td className="py-2.5 text-slate-600 dark:text-slate-300">{formatBytes(tItem.file_size)}</td>
                      <td className="py-2.5"><StatusBadge value={tItem.status} /></td>
                      <td className="py-2.5 text-xs text-slate-500 dark:text-slate-400">{formatTime(tItem.created_at, lang)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <EmptyState
              title={t('dashboard.recent.emptyTitle')}
              description={t('dashboard.recent.emptyDesc')}
              action={
                <button onClick={() => onNavigate('tasks')} className="text-xs text-indigo-600 dark:text-indigo-400 font-medium inline-flex items-center gap-1">
                  {t('dashboard.recent.goTasks')}<ArrowRight className="w-3 h-3" />
                </button>
              }
            />
          )}
        </div>
      </div>
    </div>
  )
}
