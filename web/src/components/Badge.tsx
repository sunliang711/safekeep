import React from 'react'

const styles: Record<string, string> = {
  succeeded: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  ok: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  success: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  enabled: 'bg-emerald-50 text-emerald-700 border-emerald-200',
  running: 'bg-blue-50 text-blue-700 border-blue-200',
  pending: 'bg-amber-50 text-amber-700 border-amber-200',
  partial_success: 'bg-amber-50 text-amber-700 border-amber-200',
  timeout: 'bg-amber-50 text-amber-700 border-amber-200',
  skipped: 'bg-slate-100 text-slate-600 border-slate-200',
  untested: 'bg-slate-100 text-slate-600 border-slate-200',
  never: 'bg-slate-100 text-slate-600 border-slate-200',
  disabled: 'bg-slate-100 text-slate-500 border-slate-200',
  failed: 'bg-rose-50 text-rose-700 border-rose-200',
  failure: 'bg-rose-50 text-rose-700 border-rose-200',
  sync: 'bg-indigo-50 text-indigo-700 border-indigo-200',
  async: 'bg-cyan-50 text-cyan-700 border-cyan-200',
}

const labels: Record<string, string> = {
  succeeded: '成功', ok: '正常', success: '成功', enabled: '已启用', running: '执行中',
  pending: '待处理', partial_success: '部分成功', timeout: '超时', skipped: '跳过', untested: '未测试', never: '未执行',
  disabled: '已禁用', failed: '失败', failure: '失败', sync: '同步', async: '异步',
}

export const StatusBadge: React.FC<{ value: string; label?: string }> = ({ value, label }) => {
  const cls = styles[value] || 'bg-slate-100 text-slate-700 border-slate-200'
  return (
    <span className={`inline-flex items-center gap-1.5 whitespace-nowrap px-2 py-0.5 rounded-full text-[11px] font-medium border ${cls}`}>
      <span className={`w-1.5 h-1.5 rounded-full bg-current ${value === 'running' ? 'animate-pulse' : ''}`} aria-hidden="true" />
      {label || labels[value] || value || '未知'}
    </span>
  )
}
