import React from 'react'
import {
  Activity,
  Clock,
  FileArchive,
  HardDrive,
  KeyRound,
  LogOut,
  ScrollText,
  Settings,
  ShieldCheck,
} from 'lucide-react'
import type { Admin } from '../types'

export type NavTab = 'dashboard' | 'tasks' | 'upstreams' | 'clients' | 'cron' | 'audit' | 'settings'

interface Props {
  active: NavTab
  onNavigate: (tab: NavTab) => void
  admin: Admin
  onLogout: () => void
  children: React.ReactNode
}

const navItems: { id: NavTab; label: string; icon: React.ReactNode }[] = [
  { id: 'dashboard', label: '概览仪表盘', icon: <Activity className="w-4 h-4" /> },
  { id: 'tasks', label: '备份任务与文件', icon: <FileArchive className="w-4 h-4" /> },
  { id: 'upstreams', label: '上游存储与代理', icon: <HardDrive className="w-4 h-4" /> },
  { id: 'clients', label: '客户与 API Key', icon: <KeyRound className="w-4 h-4" /> },
  { id: 'cron', label: '定时任务', icon: <Clock className="w-4 h-4" /> },
  { id: 'audit', label: '审计日志', icon: <ScrollText className="w-4 h-4" /> },
  { id: 'settings', label: '系统设置与迁移', icon: <Settings className="w-4 h-4" /> },
]

export const Layout: React.FC<Props> = ({ active, onNavigate, admin, onLogout, children }) => {
  return (
    <div className="min-h-screen flex bg-slate-50">
      <aside className="w-60 bg-slate-900 text-slate-200 flex flex-col shrink-0">
        <div className="px-5 py-4 border-b border-slate-800 flex items-center gap-2.5">
          <div className="w-8 h-8 rounded-lg bg-indigo-500 flex items-center justify-center text-white">
            <ShieldCheck className="w-5 h-5" />
          </div>
          <div>
            <div className="font-semibold text-white tracking-tight leading-none">SafeKeep</div>
            <div className="text-[11px] text-slate-400 mt-1">多云备份网关</div>
          </div>
        </div>

        <nav className="flex-1 p-3 space-y-1">
          {navItems.map((item) => {
            const isActive = active === item.id
            return (
              <button
                key={item.id}
                onClick={() => onNavigate(item.id)}
                className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition ${
                  isActive
                    ? 'bg-indigo-600 text-white font-medium'
                    : 'text-slate-300 hover:bg-slate-800 hover:text-white'
                }`}
              >
                {item.icon}
                <span>{item.label}</span>
              </button>
            )
          })}
        </nav>

        <div className="p-3 border-t border-slate-800 flex items-center justify-between text-xs">
          <div className="truncate">
            <div className="text-slate-400">当前管理员</div>
            <div className="text-white font-medium truncate">{admin.username}</div>
          </div>
          <button
            onClick={onLogout}
            title="退出登录"
            className="p-2 rounded-lg text-slate-400 hover:text-white hover:bg-slate-800"
          >
            <LogOut className="w-4 h-4" />
          </button>
        </div>
      </aside>

      <main className="flex-1 min-w-0 overflow-y-auto">
        <div className="max-w-7xl mx-auto p-6">{children}</div>
      </main>
    </div>
  )
}
