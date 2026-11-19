import React, { useEffect, useState } from 'react'
import { api } from './api'
import { Layout, type NavTab } from './components/Layout'
import { AuditPage } from './pages/AuditPage'
import { AuthPage } from './pages/AuthPages'
import { ClientsPage } from './pages/ClientsPage'
import { CronPage } from './pages/CronPage'
import { DashboardPage } from './pages/DashboardPage'
import { SettingsMigrationPage } from './pages/SettingsMigrationPage'
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
  if (p.startsWith('/settings')) return 'settings'
  return 'dashboard'
}

export function App() {
  const [booting, setBooting] = useState(true)
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
      } finally {
        setBooting(false)
      }
    })()
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  const navigate = (next: NavTab) => {
    setTab(next)
    const path = next === 'dashboard' ? '/' : `/${next}`
    window.history.pushState({}, '', path)
  }

  if (booting) {
    return <div className="min-h-screen flex items-center justify-center text-sm text-slate-500">正在加载 SafeKeep...</div>
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
    <Layout
      active={tab}
      onNavigate={navigate}
      admin={admin}
      onLogout={async () => {
        await api.logout()
        setAdmin(null)
      }}
    >
      {tab === 'dashboard' && <DashboardPage />}
      {tab === 'tasks' && <TasksPage />}
      {tab === 'upstreams' && <UpstreamsPage />}
      {tab === 'clients' && <ClientsPage />}
      {tab === 'cron' && <CronPage />}
      {tab === 'audit' && <AuditPage />}
      {tab === 'settings' && <SettingsMigrationPage />}
    </Layout>
  )
}

export default App
