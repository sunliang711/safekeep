import React, { useEffect, useState } from 'react'
import { AlertTriangle, ArrowRight, CheckCircle2, Cloud, FileArchive, HardDrive, RefreshCw, ShieldCheck, Upload } from 'lucide-react'
import { api, formatBytes, formatTime } from '../api'
import { EmptyState, LoadingState } from '../components/Feedback'
import { useToast } from '../components/Toast'
import type { NavTab } from '../components/Layout'
import { StatusBadge } from '../components/Badge'
import type { DashboardSummary } from '../types'

export const DashboardPage: React.FC<{ onNavigate: (tab: NavTab) => void }> = ({ onNavigate }) => {
  const toast = useToast()
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

  if (loading && !data) return <LoadingState label="正在读取备份概览…" />
  if (error && !data) return <div role="alert" className="p-4 bg-rose-50 text-rose-700 rounded-lg text-sm">{error}<button onClick={load} className="ml-4 underline">重新加载</button></div>
  if (!data) return null

  const cards = [
    { icon: <FileArchive className="w-5 h-5" />, label: '累计备份任务', value: data.total_tasks, sub: `成功 ${data.succeeded_tasks} / 秒传去重 ${data.deduplicated_tasks}` },
    { icon: <CheckCircle2 className="w-5 h-5" />, label: '待处理 / 异常任务', value: `${data.pending_tasks} / ${data.failed_tasks}`, sub: '队列中 / 失败或部分成功' },
    { icon: <HardDrive className="w-5 h-5" />, label: '已备份文件总大小', value: formatBytes(data.total_bytes_backed), sub: `本地缓冲占用 ${formatBytes(data.spool_bytes_used)}` },
    { icon: <Cloud className="w-5 h-5" />, label: '健康上游 · 启用客户', value: `${data.healthy_upstreams}/${data.enabled_upstreams} · ${data.enabled_clients}`, sub: '健康上游数 · 启用客户数' },
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
          disabled={loading}
          className="inline-flex items-center gap-1.5 px-3 py-1.5 bg-white border border-slate-200 rounded-lg text-sm hover:bg-slate-50"
        >
          <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />
          刷新
        </button>
      </div>

      {error && <div role="alert" className="p-3 rounded-xl bg-rose-50 text-rose-700 text-sm">{error}</div>}
      <div className="rounded-2xl border border-indigo-100 bg-gradient-to-r from-indigo-50 via-white to-white p-6 sm:p-7 flex flex-wrap items-center justify-between gap-5">
        <div className="flex gap-4 items-center">
          <div className="hidden sm:grid w-12 h-12 place-items-center rounded-2xl bg-white border border-indigo-100 text-indigo-500"><ShieldCheck className="w-7 h-7" /></div>
          <div><div className="text-xs font-medium text-indigo-500 mb-1.5">你的备份工作空间</div><h2 className="text-lg font-semibold text-slate-900">{data.enabled_upstreams > 0 ? '备份状态，一目了然' : '从连接第一个云端存储开始'}</h2><p className="text-xs text-slate-500 mt-2">{data.enabled_upstreams > 0 ? '集中查看任务进度与存储状态，随时找回所需文件。' : '连接 S3、Cloudflare R2、Google Cloud Storage 或 Dropbox，创建客户策略，即可开始安全备份。'}</p></div>
        </div>
        <button className="primary-button" onClick={() => onNavigate(data.enabled_upstreams > 0 ? 'tasks' : 'upstreams')}>{data.enabled_upstreams > 0 ? <Upload className="w-4 h-4" /> : <Cloud className="w-4 h-4" />}{data.enabled_upstreams > 0 ? '管理备份文件' : '连接上游存储'}<ArrowRight className="w-3.5 h-3.5" /></button>
      </div>

      <div className="grid grid-cols-1 min-[380px]:grid-cols-2 lg:grid-cols-4 gap-4">
        {cards.map((c) => (
          <div key={c.label} className="bg-white rounded-xl border border-slate-200 p-5">
            <div className="flex items-center justify-between gap-2 text-xs text-slate-500">{c.label}<span className="text-indigo-400 bg-indigo-50 p-2 rounded-lg">{c.icon}</span></div>
            <div className="text-2xl font-semibold tracking-tight text-slate-900 mt-2 tabular-nums">{c.value}</div>
            <div className="text-xs text-slate-400 mt-1">{c.sub}</div>
          </div>
        ))}
      </div>

      {data.daily_traffic && (
        <div
          className={`rounded-xl border p-4 flex flex-wrap items-center justify-between gap-4 ${
            data.daily_traffic.tripped
              ? 'bg-rose-50 border-rose-200 text-rose-900'
              : 'bg-white border-slate-200 text-slate-800'
          }`}
        >
          <div className="flex items-center gap-3">
            {data.daily_traffic.tripped ? (
              <AlertTriangle className="w-5 h-5 text-rose-600 shrink-0" />
            ) : (
              <ShieldCheck className="w-5 h-5 text-emerald-600 shrink-0" />
            )}
            <div>
              <div className="text-sm font-semibold flex items-center gap-2">
                <span>每日发送流量与熔断器 ({data.daily_traffic.date_key})</span>
                <StatusBadge
                  value={data.daily_traffic.tripped ? 'failed' : 'ok'}
                  label={data.daily_traffic.tripped ? '已熔断 (503)' : '正常'}
                />
              </div>
              <div className="text-xs opacity-80 mt-0.5">
                今日已用：{formatBytes(data.daily_traffic.bytes_used)} /{' '}
                {data.daily_traffic.limit_bytes > 0 ? formatBytes(data.daily_traffic.limit_bytes) : '不限'}
                {data.daily_traffic.tripped_at && ` · 熔断时间: ${formatTime(data.daily_traffic.tripped_at)}`}
              </div>
            </div>
          </div>
          <button
            onClick={async () => {
              if (!confirm('确定重置今日流量计数并解除服务熔断吗？')) return
              setResetting(true)
              try {
                await api.resetCircuitBreaker()
                toast.success('今日流量计数已重置')
                await load()
              } catch (e: any) {
                toast.error(e.message || '重置失败')
              } finally {
                setResetting(false)
              }
            }}
            disabled={resetting}
            className={`px-3.5 py-1.5 rounded-lg text-xs font-medium ${
              data.daily_traffic.tripped
                ? 'bg-rose-600 hover:bg-rose-700 text-white'
                : 'bg-slate-100 hover:bg-slate-200 text-slate-700'
            }`}
          >
            {resetting ? '重置中…' : data.daily_traffic.tripped ? '解除熔断并重置今日流量' : '重置今日流量计数'}
          </button>
        </div>
      )}

      {data.daily_traffic && data.daily_traffic.limit_bytes > 0 && <div className="-mt-3 px-1" role="progressbar" aria-label="今日流量使用比例" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.min(100, Math.round(data.daily_traffic.bytes_used / data.daily_traffic.limit_bytes * 100))}><div className="h-1.5 rounded-full bg-slate-200 overflow-hidden"><div className={`h-full rounded-full ${data.daily_traffic.tripped ? 'bg-rose-500' : 'bg-indigo-500'}`} style={{ width: `${Math.min(100, data.daily_traffic.bytes_used / data.daily_traffic.limit_bytes * 100)}%` }} /></div></div>}

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
                  <StatusBadge value={u.enabled ? u.last_test_status : 'disabled'} label={u.enabled ? undefined : '已禁用'} />
                </div>
              ))}
            </div>
          ) : (
            <EmptyState icon={<Cloud className="w-6 h-6" />} title="还没有连接存储" description="连接你的云端存储，查看连通性与健康状态。" action={<button onClick={() => onNavigate('upstreams')} className="text-xs text-indigo-600 font-medium inline-flex items-center gap-1">添加上游存储<ArrowRight className="w-3 h-3" /></button>} />
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
            <EmptyState title="第一份备份，从这里开始" description="上传文件后，可以在这里查看最近的任务和备份结果。" action={<button onClick={() => onNavigate('tasks')} className="text-xs text-indigo-600 font-medium inline-flex items-center gap-1">前往备份任务<ArrowRight className="w-3 h-3" /></button>} />
          )}
        </div>
      </div>
    </div>
  )
}
