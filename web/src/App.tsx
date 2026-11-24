import React, { useCallback, useEffect, useRef, useState } from 'react'
import { api, isReauthenticating, setUnauthorizedHandler } from './api'
import { Layout, type NavTab } from './components/Layout'
import { LoadingState } from './components/Feedback'
import { ToastProvider } from './components/Toast'
import { allowNavigation } from './components/ConfirmDialog'
import { useI18n } from './contexts/I18nContext'
import { AuditPage } from './pages/AuditPage'
import { AuthPage, ForcePasswordChangePage, SessionExpiredModal } from './pages/AuthPages'
import { ClientsPage } from './pages/ClientsPage'
import { CronPage } from './pages/CronPage'
import { DashboardPage } from './pages/DashboardPage'
import { RemoteBackupsPage } from './pages/RemoteBackupsPage'
import { MigrationPage } from './pages/MigrationPage'
import { SettingsPage } from './pages/SettingsPage'
import { TasksPage } from './pages/TasksPage'
import { UpstreamsPage } from './pages/UpstreamsPage'
import type { Admin } from './types'

function initialTab(): NavTab {
  const p = window.location.pathname
  if (p.startsWith('/remote-backups')) return 'remote-backups'
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
  const { t } = useI18n()
  const [booting, setBooting] = useState(true)
  const [bootFailed, setBootFailed] = useState(false)
  const [initialized, setInitialized] = useState(true)
  const [admin, setAdmin] = useState<Admin | null>(null)
  const [version, setVersion] = useState('dev')
  const [resetAdminEnvActive, setResetAdminEnvActive] = useState(false)
  const [tab, setTab] = useState<NavTab>(initialTab())
  const [sessionExpired, setSessionExpired] = useState(false)
  const reauthResolversRef = useRef<((ok: boolean) => void)[]>([])

  const settleReauth = useCallback((ok: boolean) => {
    setSessionExpired(false)
    while (reauthResolversRef.current.length > 0) {
      reauthResolversRef.current.shift()?.(ok)
    }
  }, [])

  useEffect(() => {
    const onPopState = () => setTab(initialTab())
    window.addEventListener('popstate', onPopState)
    ;(async () => {
      try {
        const st = await api.setupStatus()
        setInitialized(st.initialized)
        if (st.version) setVersion(st.version)
        if (typeof st.reset_admin_env_active === 'boolean') {
          setResetAdminEnvActive(st.reset_admin_env_active)
        }
        if (st.initialized) {
          try {
            const me = await api.me({ skipAuthPrompt: true })
            setAdmin(me.admin)
            if (me.version) setVersion(me.version)
            if (typeof me.reset_admin_env_active === 'boolean') {
              setResetAdminEnvActive(me.reset_admin_env_active)
            }
          } catch {
            setAdmin(null)
          }
        }
      } catch {
        setBootFailed(true)
      } finally {
        setBooting(false)
      }
    })()
    return () => window.removeEventListener('popstate', onPopState)
  }, [])

  const loggedIn = Boolean(admin)

  useEffect(() => {
    if (!loggedIn) {
      setUnauthorizedHandler(null)
      settleReauth(false)
      return
    }

    setUnauthorizedHandler(() => new Promise<boolean>((resolve) => {
      reauthResolversRef.current.push(resolve)
      setSessionExpired(true)
    }))

    let lastCheckedAt = Date.now()
    const onVisibilityChange = () => {
      if (document.visibilityState !== 'visible' || isReauthenticating()) return
      const now = Date.now()
      if (now - lastCheckedAt < 60_000) return
      lastCheckedAt = now
      void api.me().then((res) => {
        if (res.admin) setAdmin(res.admin)
        if (res.version) setVersion(res.version)
        if (typeof res.reset_admin_env_active === 'boolean') {
          setResetAdminEnvActive(res.reset_admin_env_active)
        }
      }).catch(() => {})
    }
    document.addEventListener('visibilitychange', onVisibilityChange)

    return () => {
      document.removeEventListener('visibilitychange', onVisibilityChange)
      setUnauthorizedHandler(null)
      settleReauth(false)
    }
  }, [loggedIn, settleReauth])

  const navigate = async (next: NavTab) => {
    if (next === tab) return
    if (!await allowNavigation()) return
    setTab(next)
    const path = next === 'dashboard' ? '/' : `/${next}`
    window.history.pushState({}, '', path)
    window.scrollTo({ top: 0 })
  }

  if (booting) {
    return (
      <div className="min-h-screen grid place-items-center bg-[#f5f7fb] dark:bg-[#0b1120]">
        <LoadingState label={t('app.loading')} />
      </div>
    )
  }

  if (bootFailed) {
    return (
      <div className="min-h-screen flex flex-col items-center justify-center gap-4 p-6 text-center bg-[#f5f7fb] dark:bg-[#0b1120]">
        <p role="alert" className="text-sm text-slate-600 dark:text-slate-300">
          {t('app.connectError')}
        </p>
        <button className="primary-button" onClick={() => window.location.reload()}>
          {t('app.reconnect')}
        </button>
      </div>
    )
  }

  if (!initialized) {
    return (
      <AuthPage
        mode="setup"
        version={version}
        resetAdminEnvActive={resetAdminEnvActive}
        onSuccess={(a, envActive) => {
          setInitialized(true)
          setAdmin(a)
          if (typeof envActive === 'boolean') setResetAdminEnvActive(envActive)
        }}
      />
    )
  }

  if (!admin) {
    return (
      <AuthPage
        mode="login"
        version={version}
        resetAdminEnvActive={resetAdminEnvActive}
        onSuccess={(a, envActive) => {
          setAdmin(a)
          if (typeof envActive === 'boolean') setResetAdminEnvActive(envActive)
        }}
      />
    )
  }

  if (admin.must_change_password) {
    return (
      <ForcePasswordChangePage
        admin={admin}
        version={version}
        resetAdminEnvActive={resetAdminEnvActive}
        onSuccess={(nextAdmin, envActive) => {
          setAdmin(nextAdmin)
          if (typeof envActive === 'boolean') setResetAdminEnvActive(envActive)
        }}
        onLogout={async () => {
          try {
            await api.logout()
          } finally {
            settleReauth(false)
            setAdmin(null)
          }
        }}
      />
    )
  }

  return (
    <ToastProvider>
      <Layout
        active={tab}
        onNavigate={navigate}
        admin={admin}
        version={version}
        resetAdminEnvActive={resetAdminEnvActive}
        onLogout={async () => {
          if (!await allowNavigation()) return
          try {
            await api.logout()
          } finally {
            settleReauth(false)
            setAdmin(null)
          }
        }}
      >
        {tab === 'dashboard' && <DashboardPage onNavigate={navigate} />}
        {tab === 'tasks' && <TasksPage onNavigate={navigate} />}
        {tab === 'remote-backups' && <RemoteBackupsPage />}
        {tab === 'upstreams' && <UpstreamsPage />}
        {tab === 'clients' && <ClientsPage />}
        {tab === 'cron' && <CronPage />}
        {tab === 'audit' && <AuditPage />}
        {tab === 'migration' && <MigrationPage />}
        {tab === 'settings' && <SettingsPage />}
      </Layout>
      {sessionExpired && (
        <SessionExpiredModal
          initialUsername={admin.username}
          onSuccess={(nextAdmin, envActive) => {
            if (typeof envActive === 'boolean') setResetAdminEnvActive(envActive)
            setAdmin(nextAdmin)
            settleReauth(!nextAdmin.must_change_password)
          }}
          onLogout={() => {
            settleReauth(false)
            void api.logout().catch(() => {})
            setAdmin(null)
          }}
        />
      )}
    </ToastProvider>
  )
}

export default App
