import React from 'react'
import { Inbox, Loader2 } from 'lucide-react'

export function EmptyState({ title, description, action, icon }: {
  title: string
  description?: string
  action?: React.ReactNode
  icon?: React.ReactNode
}) {
  return <div className="empty-state">
    <div className="empty-icon">{icon || <Inbox className="w-6 h-6" />}</div>
    <div className="text-sm font-semibold text-slate-800">{title}</div>
    {description && <p className="text-xs text-slate-500 max-w-sm leading-6">{description}</p>}
    {action && <div className="mt-2">{action}</div>}
  </div>
}

export function LoadingState({ label = '正在加载…' }: { label?: string }) {
  return <div role="status" className="flex items-center justify-center gap-2 p-10 text-sm text-slate-500">
    <Loader2 className="w-4 h-4 animate-spin text-indigo-500" />{label}
  </div>
}
