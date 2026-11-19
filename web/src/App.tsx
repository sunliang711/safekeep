import React, { useEffect, useState } from 'react'
import { api } from './api'
import { Layout, type NavTab } from './components/Layout'
import { LoadingState } from './components/Feedback'
import { ToastProvider } from './components/Toast'
import { AuditPage } from './pages/AuditPage'
import { AuthPage } from './pages/AuthPages'
import { ClientsPage } from './pages/ClientsPage'
import { CronPage } from './pages/CronPage'
import { DashboardPage } from './pages/DashboardPage'
import { MigrationPage } from './pages/MigrationPage'
import { SettingsPage } from './pages/SettingsPage'
import { TasksPage } from './pages/TasksPage'
import { UpstreamsPage } from './pages/UpstreamsPage'
import type { Admin } from './types'

function initialTab(): NavTab {
  const p = window.location.pathname
  if (p.startsWith('/upstreams')) return 'upstreams'
  if (p.startsWith('/clients')) return 'clients'
  if (p.startsWith('/tasks')) return 'tasks'
  if (p.startsWith('/cron')) return 'cron'
  if (p.startsWith('/audit')) return 'audit'
  if (p.startsWith('/migration')) return 'migration'
  if (p.startsWith('/settings')) return 'settings'
  return 'dashboard'
}

export function App() {
  const [booting, setBooting] = useState(true)
  const [bootError, setBootError] = useState('')
  const [initialized, setInitialized] = useState(true)
  const [admin, setAdmin] = useState<Admin | null>(null)
  const [tab, setTab] = useState<NavTab>(initialTab())

  useEffect(() => {
    const onPopState = () => setTab(initialTab())
    window.addEventListener('popstate', onPopState)
    ;(async () => {
      try {
        const st = await api.setupStatus()
        setInitialized(st.initialized)
        if (st.initialized) {
          try {
            const me = await api.me()
            setAdmin(me.admin)
          } catch {
            setAdmin(null)
          }
        }
      } catch {
        setBootError('无法连接到服务，请检查服务运行状态后重试。')
      } finally {
        setBooting(false)
      }
    })()
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  const navigate = (next: NavTab) => {
    if (next === tab) return
    if (!window.dispatchEvent(new Event('safekeep:before-navigate', { cancelable: true }))) return
    setTab(next)
    const path = next === 'dashboard' ? '/' : `/${next}`
    window.history.pushState({}, '', path)
    window.scrollTo({ top: 0 })
  }

  if (booting) {
    return <div className="min-h-screen grid place-items-center"><LoadingState label="正在加载 SafeKeep…" /></div>
  }

  if (bootError) {
    return <div className="min-h-screen flex flex-col items-center justify-center gap-4 p-6 text-center"><p role="alert" className="text-sm text-slate-600">{bootError}</p><button className="primary-button" onClick={() => window.location.reload()}>重新连接</button></div>
  }

  if (!initialized) {
    return (
      <AuthPage
        mode="setup"
        onSuccess={(a) => {
          setInitialized(true)
          setAdmin(a)
        }}
      />
    )
  }

  if (!admin) {
    return <AuthPage mode="login" onSuccess={(a) => setAdmin(a)} />
  }

  return (
    <ToastProvider>
      <Layout
        active={tab}
        onNavigate={navigate}
        admin={admin}
        onLogout={async () => {
          if (!window.dispatchEvent(new Event('safekeep:before-navigate', { cancelable: true }))) return
          await api.logout()
          setAdmin(null)
        }}
      >
        {tab === 'dashboard' && <DashboardPage onNavigate={navigate} />}
        {tab === 'tasks' && <TasksPage onNavigate={navigate} />}
        {tab === 'upstreams' && <UpstreamsPage />}
        {tab === 'clients' && <ClientsPage />}
        {tab === 'cron' && <CronPage />}
        {tab === 'audit' && <AuditPage />}
        {tab === 'migration' && <MigrationPage />}
        {tab === 'settings' && <SettingsPage />}
      </Layout>
    </ToastProvider>
  )
}

export default App
