import React, { useEffect, useRef, useState } from 'react'
import {
  Activity,
  ArrowLeftRight,
  Clock,
  FileArchive,
  HardDrive,
  KeyRound,
  LogOut,
  Menu,
  X,
  ChevronRight,
  ScrollText,
  Settings,
  ShieldCheck,
} from 'lucide-react'
import type { Admin } from '../types'

export type NavTab = 'dashboard' | 'tasks' | 'upstreams' | 'clients' | 'cron' | 'audit' | 'migration' | 'settings'

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
  { id: 'migration', label: '环境迁移', icon: <ArrowLeftRight className="w-4 h-4" /> },
  { id: 'settings', label: '系统设置', icon: <Settings className="w-4 h-4" /> },
]

export const Layout: React.FC<Props> = ({ active, onNavigate, admin, onLogout, children }) => {
  const [menuOpen, setMenuOpen] = useState(false)
  const navigation = useRef<HTMLElement>(null)
  const current = navItems.find((item) => item.id === active)!

  useEffect(() => {
    if (!menuOpen) return
    const previous = document.activeElement as HTMLElement | null
    const overflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const focusable = () => Array.from(navigation.current?.querySelectorAll<HTMLElement>('button, a[href]') || []).filter((el) => el.getClientRects().length > 0)
    focusable()[0]?.focus()
    const onEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenuOpen(false)
      if (event.key === 'Tab') {
        const elements = focusable()
        const first = elements[0]
        const last = elements[elements.length - 1]
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault()
          last?.focus()
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault()
          first?.focus()
        }
      }
    }
    document.addEventListener('keydown', onEscape)
    return () => {
      document.body.style.overflow = overflow
      document.removeEventListener('keydown', onEscape)
      previous?.focus()
    }
  }, [menuOpen])

  return (
    <div className="min-h-screen bg-[#f5f7fb]">
      <a href="#main-content" className="sr-only focus:not-sr-only focus:fixed focus:top-4 focus:left-4 focus:z-[80] bg-white p-3 rounded-lg">跳转到主要内容</a>
      {menuOpen && <button className="fixed inset-0 bg-slate-950/50 backdrop-blur-sm z-30 lg:hidden" aria-label="关闭导航菜单" onClick={() => setMenuOpen(false)} />}
      <aside ref={navigation} id="app-navigation" className={`app-sidebar fixed inset-y-0 left-0 w-64 text-slate-200 flex-col z-40 transition-transform duration-200 ${menuOpen ? 'flex translate-x-0' : 'hidden -translate-x-full'} lg:flex lg:translate-x-0`}>
        <div className="px-6 h-[88px] flex items-center gap-3 shrink-0">
          <div className="w-10 h-10 rounded-xl bg-indigo-500 flex items-center justify-center text-white shadow-lg shadow-indigo-950/30">
            <ShieldCheck className="w-6 h-6" />
          </div>
          <div>
            <div className="font-semibold text-xl text-white tracking-tight leading-none">SafeKeep<span className="text-indigo-400">.</span></div>
            <div className="text-[11px] text-slate-400 mt-2 tracking-wider">多云文件备份管理平台</div>
          </div>
          <button onClick={() => setMenuOpen(false)} className="lg:hidden ml-auto p-1 text-slate-400" aria-label="收起导航"><X className="w-5 h-5" /></button>
        </div>

        <nav aria-label="主导航" className="flex-1 px-4 py-4 overflow-y-auto">
          {navItems.map((item, index) => {
            const isActive = active === item.id
            return (
              <React.Fragment key={item.id}>
                {(index === 0 || index === 4) && <div className="text-[10px] text-slate-500 tracking-widest px-3 mb-3 mt-2">{index === 0 ? '备份工作空间' : '管理与维护'}</div>}
                <a
                  href={item.id === 'dashboard' ? '/' : `/${item.id}`}
                  aria-current={isActive ? 'page' : undefined}
                  onClick={(event) => {
                    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
                    event.preventDefault()
                    setMenuOpen(false)
                    onNavigate(item.id)
                  }}
                  className={`w-full flex items-center gap-3 px-3 py-3 mb-1 rounded-xl text-[13px] transition ${isActive ? 'bg-indigo-500 text-white font-medium shadow-md shadow-indigo-950/20' : 'text-slate-400 hover:bg-white/5 hover:text-white'} ${index === 3 ? 'mb-7' : ''}`}
                >
                  {item.icon}<span>{item.label}</span>
                  {isActive && <ChevronRight className="w-3.5 h-3.5 ml-auto opacity-70" />}
                </a>
              </React.Fragment>
            )
          })}
        </nav>

        <div className="mx-4 mb-4 rounded-xl bg-white/[0.04] p-3 flex items-center gap-3 text-xs">
          <div className="w-9 h-9 rounded-full bg-indigo-400/15 text-indigo-200 flex items-center justify-center font-semibold text-sm shrink-0">{admin.username.slice(0, 1).toUpperCase()}</div>
          <div className="truncate flex-1">
            <div className="text-white font-medium truncate">{admin.username}</div>
            <div className="text-slate-500 mt-1">管理员</div>
          </div>
          <button onClick={onLogout} title="退出登录" aria-label="退出登录" className="p-2 rounded-lg text-slate-400 hover:text-white hover:bg-white/10"><LogOut className="w-4 h-4" /></button>
        </div>
      </aside>

      <div className="lg:pl-64">
        <header className="h-[72px] border-b border-slate-200/70 bg-white/90 flex items-center justify-between gap-3 px-5 sm:px-8">
          <div className="flex items-center gap-3 text-xs text-slate-400">
            <button onClick={() => setMenuOpen(true)} aria-label="打开导航菜单" aria-controls="app-navigation" aria-expanded={menuOpen} className="lg:hidden p-2 -ml-2 text-slate-600 rounded-lg hover:bg-slate-100"><Menu className="w-5 h-5" /></button>
            <span className="hidden sm:inline">工作空间</span><ChevronRight className="hidden sm:block w-3 h-3" /><span className="font-medium text-slate-700">{current.label}</span>
          </div>
          <div className="flex items-center gap-2 text-xs text-slate-500"><ShieldCheck className="w-4 h-4 text-indigo-500" /><span>SafeKeep 控制台</span></div>
        </header>
        <main id="main-content" tabIndex={-1} className="min-w-0">
          <div className="page-content max-w-[1440px] mx-auto px-4 py-6 sm:p-8">{children}</div>
          <footer className="px-8 py-6 text-[11px] text-slate-400 flex flex-wrap gap-2 justify-between"><span>SafeKeep · 让每一份数据都有归处</span><span>多云备份 · 统一管理</span></footer>
        </main>
      </div>
    </div>
  )
}
