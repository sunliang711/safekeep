import React, { useEffect, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import type { DashboardSummary } from '../types'

export const DashboardPage: React.FC = () => {
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

  if (loading && !data) return <div className="text-sm text-slate-500">加载中...</div>
  if (error) return <div className="p-4 bg-rose-50 text-rose-700 rounded-lg text-sm">{error}</div>
  if (!data) return null

  const cards = [
    { label: '累计备份任务', value: data.total_tasks, sub: `成功 ${data.succeeded_tasks} / 秒传去重 ${data.deduplicated_tasks}` },
    { label: '待处理 / 异常任务', value: `${data.pending_tasks} / ${data.failed_tasks}`, sub: '队列中 / 失败或部分成功' },
    { label: '已备份文件总大小', value: formatBytes(data.total_bytes_backed), sub: `本地缓冲占用 ${formatBytes(data.spool_bytes_used)}` },
    { label: '已启用上游 / 客户', value: `${data.healthy_upstreams}/${data.enabled_upstreams} · ${data.enabled_clients}`, sub: '健康上游数 · 启用客户数' },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900">概览仪表盘</h1>
          <p className="text-sm text-slate-500">实时查看备份任务吞吐、上游连通状态与本地缓冲占用</p>
        </div>
        <button
          onClick={load}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white border border-slate-200 rounded-lg text-sm hover:bg-slate-50"
        >
          <RefreshCw className="w-4 h-4" />
          刷新
        </button>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
        {cards.map((c) => (
          <div key={c.label} className="bg-white rounded-xl border border-slate-200 p-4">
            <div className="text-xs text-slate-500">{c.label}</div>
            <div className="text-2xl font-bold text-slate-900 mt-1">{c.value}</div>
            <div className="text-xs text-slate-400 mt-1">{c.sub}</div>
          </div>
        ))}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div className="bg-white rounded-xl border border-slate-200 p-5">
          <h2 className="font-semibold text-slate-900 mb-3">上游存储健康状态</h2>
          {data.upstreams && data.upstreams.length > 0 ? (
            <div className="space-y-3">
              {data.upstreams.map((u) => (
                <div key={u.id} className="flex items-center justify-between p-2.5 rounded-lg bg-slate-50 border border-slate-100">
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-slate-900 truncate">{u.name}</div>
                    <div className="text-xs text-slate-500">
                      {u.type.toUpperCase()} {u.proxy_enabled ? `· ${u.proxy_type.toUpperCase()} 代理` : '· 直连'}
                    </div>
                  </div>
                  <StatusBadge value={u.enabled ? u.last_test_status : 'disabled'} label={u.enabled ? u.last_test_status : '已禁用'} />
                </div>
              ))}
            </div>
          ) : (
            <div className="text-sm text-slate-400">尚未配置上游存储，请前往「上游存储与代理」添加。</div>
          )}
        </div>

        <div className="lg:col-span-2 bg-white rounded-xl border border-slate-200 p-5">
          <h2 className="font-semibold text-slate-900 mb-3">最近备份任务</h2>
          {data.recent_tasks && data.recent_tasks.length > 0 ? (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-xs text-slate-500 border-b border-slate-100">
                    <th className="pb-2">文件</th>
                    <th className="pb-2">客户</th>
                    <th className="pb-2">模式</th>
                    <th className="pb-2">大小</th>
                    <th className="pb-2">状态</th>
                    <th className="pb-2">时间</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {data.recent_tasks.map((t) => (
                    <tr key={t.id}>
                      <td className="py-2.5 font-medium text-slate-800 max-w-[180px] truncate">
                        {t.original_filename}
                        {t.deduplicated && (
                          <span className="ml-1.5 text-[10px] px-1.5 py-0.5 bg-emerald-100 text-emerald-700 rounded">秒传</span>
                        )}
                      </td>
                      <td className="py-2.5 text-slate-600">{t.client_name || t.client_id}</td>
                      <td className="py-2.5"><StatusBadge value={t.mode} /></td>
                      <td className="py-2.5 text-slate-600">{formatBytes(t.file_size)}</td>
                      <td className="py-2.5"><StatusBadge value={t.status} /></td>
                      <td className="py-2.5 text-xs text-slate-500">{formatTime(t.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="text-sm text-slate-400">暂无备份记录。</div>
          )}
        </div>
      </div>
    </div>
  )
}
