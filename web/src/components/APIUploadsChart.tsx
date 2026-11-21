import React, { useState } from 'react'
import { PieChart } from 'lucide-react'
import { useI18n } from '../contexts/I18nContext'
import type { UploadStats } from '../types'
import { EmptyState } from './Feedback'

type Period = 'today' | 'week' | 'month'

export const APIUploadsChart: React.FC<{ stats: UploadStats }> = ({ stats }) => {
  const { t, lang } = useI18n()
  const [period, setPeriod] = useState<Period>('today')
  const counts = stats[period]
  const total = counts.reduce((sum, item) => sum + item.upload_count, 0)
  // 三个时间范围共用颜色表，用黄金角分散相邻客户的色相。
  const clientIDs = Array.from(new Set([...stats.today, ...stats.week, ...stats.month].map((item) => item.client_id))).sort()
  const clientColors = new Map(clientIDs.map((id, index) => [id, `hsl(${(240 + index * 137.508) % 360}, 68%, 55%)`]))
  const number = (value: number) => value.toLocaleString(lang === 'en' ? 'en-US' : 'zh-CN')
  const periods = [
    { value: 'today' as const, label: t('dashboard.uploads.today') },
    { value: 'week' as const, label: t('dashboard.uploads.week') },
    { value: 'month' as const, label: t('dashboard.uploads.month') },
  ]
  let angle = -Math.PI / 2

  return (
    <section className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5" aria-labelledby="api-uploads-title">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h2 id="api-uploads-title" className="font-semibold text-slate-900 dark:text-slate-100">{t('dashboard.uploads.title')}</h2>
          <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">{t('dashboard.uploads.subtitle')}</p>
        </div>
        <div role="group" aria-label={t('dashboard.uploads.period')} className="inline-flex rounded-lg bg-slate-100 dark:bg-slate-800 p-1 gap-1">
          {periods.map((item) => (
            <button
              key={item.value}
              type="button"
              aria-pressed={period === item.value}
              onClick={() => setPeriod(item.value)}
              className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${period === item.value
                ? 'bg-white dark:bg-slate-700 text-indigo-600 dark:text-indigo-300 shadow-sm'
                : 'text-slate-500 dark:text-slate-400 hover:text-slate-800 dark:hover:text-slate-200'}`}
            >
              {item.label}
            </button>
          ))}
        </div>
      </div>

      {total > 0 ? (
        <div className="mt-5 grid grid-cols-1 md:grid-cols-2 gap-6 items-center">
          <div className="flex flex-col items-center gap-2">
            <svg viewBox="0 0 240 240" className="w-56 h-56 sm:w-64 sm:h-64" role="img" aria-label={t('dashboard.uploads.chartAria', { total: number(total) })}>
              {counts.map((item) => {
                const start = angle
                const sweep = (item.upload_count / total) * Math.PI * 2
                angle += sweep
                const label = t('dashboard.uploads.slice', { name: item.client_name, count: number(item.upload_count), percent: ((item.upload_count / total) * 100).toFixed(1) })
                return counts.length === 1 ? (
                  <circle key={item.client_id} cx="120" cy="120" r="100" fill={clientColors.get(item.client_id)}>
                    <title>{label}</title>
                  </circle>
                ) : (
                  <path
                    key={item.client_id}
                    d={`M 120 120 L ${120 + 100 * Math.cos(start)} ${120 + 100 * Math.sin(start)} A 100 100 0 ${sweep > Math.PI ? 1 : 0} 1 ${120 + 100 * Math.cos(angle)} ${120 + 100 * Math.sin(angle)} Z`}
                    fill={clientColors.get(item.client_id)}
                    className="stroke-white dark:stroke-slate-900"
                    strokeWidth="2"
                  >
                    <title>{label}</title>
                  </path>
                )
              })}
            </svg>
            <p aria-live="polite" className="text-sm text-slate-500 dark:text-slate-400">
              {t('dashboard.uploads.total', { count: number(total) })}
            </p>
          </div>
          <ul className="space-y-1 max-h-72 overflow-y-auto" aria-label={t('dashboard.uploads.legend')}>
            {counts.map((item) => (
              <li key={item.client_id} className="flex items-center gap-3 p-2.5 rounded-lg bg-slate-50 dark:bg-slate-800/60 text-sm">
                <span className="w-3 h-3 rounded-full shrink-0" style={{ backgroundColor: clientColors.get(item.client_id) }} aria-hidden="true" />
                <span className="min-w-0 flex-1 truncate text-slate-700 dark:text-slate-200" title={`${item.client_name} (${item.client_id})`}>{item.client_name}</span>
                <span className="font-semibold tabular-nums text-slate-900 dark:text-slate-100">{number(item.upload_count)}</span>
                <span className="w-16 text-right tabular-nums text-xs text-slate-500 dark:text-slate-400">{((item.upload_count / total) * 100).toFixed(1)}%</span>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <EmptyState icon={<PieChart className="w-6 h-6" />} title={t('dashboard.uploads.emptyTitle')} description={t('dashboard.uploads.emptyDesc')} />
      )}

      <p className="mt-4 text-xs text-slate-400 dark:text-slate-500">{t('dashboard.uploads.timezone', { timezone: stats.timezone })}</p>
    </section>
  )
}
