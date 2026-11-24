import React, { useId, useState } from 'react'
import { AlertTriangle, ArrowRight, Cloud, Eye, EyeOff, Loader2, LockKeyhole, ShieldCheck } from 'lucide-react'
import { api } from '../api'
import { Modal } from '../components/Modal'
import { PreferenceControls } from '../components/PreferenceControls'
import { useToast } from '../components/Toast'
import { useI18n } from '../contexts/I18nContext'
import type { Admin } from '../types'

export const ResetAdminBanner: React.FC = () => {
  const { t } = useI18n()
  return (
    <div
      role="status"
      className="flex items-start gap-2.5 rounded-xl border border-amber-300 dark:border-amber-500/40 bg-amber-50 dark:bg-amber-500/10 px-4 py-3 text-xs leading-5 text-amber-900 dark:text-amber-200"
    >
      <AlertTriangle aria-hidden="true" className="w-4 h-4 shrink-0 mt-0.5 text-amber-600 dark:text-amber-400" />
      <span>{t('auth.resetAdminBanner')}</span>
    </div>
  )
}

function formatAuthError(err: any, t: ReturnType<typeof useI18n>['t']): string {
  return err?.message === 'invalid username or password'
    ? t('auth.invalidCredentials')
    : err?.message || t('auth.actionFailed')
}

interface Props {
  mode: 'setup' | 'login'
  version?: string
  resetAdminEnvActive?: boolean
  onSuccess: (admin: Admin, resetAdminEnvActive?: boolean) => void
}

export const AuthPage: React.FC<Props> = ({ mode, version = 'dev', resetAdminEnvActive = false, onSuccess }) => {
  const { t } = useI18n()
  const versionLabel = version && version !== 'dev' && !version.startsWith('v') ? `v${version}` : version || 'dev'
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    if (mode === 'setup' && password !== confirm) {
      setError(t('auth.passwordMismatch'))
      return
    }
    setLoading(true)
    try {
      if (mode === 'setup') {
        const res = await api.setupInit(username, password)
        onSuccess(res.admin, resetAdminEnvActive)
      } else {
        const res = await api.login(username, password)
        onSuccess(res.admin, res.reset_admin_env_active)
      }
    } catch (err: any) {
      setError(formatAuthError(err, t))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen grid lg:grid-cols-2 bg-[#f7f9fc] dark:bg-[#0b1120] transition-colors">
      <div className="hidden lg:flex flex-col justify-between relative overflow-hidden bg-[#111b34] dark:bg-[#090e1a] border-r border-transparent dark:border-slate-800 text-white p-12 xl:p-16">
        <div className="absolute -right-32 top-1/4 w-96 h-96 bg-indigo-500/10 rounded-full blur-3xl" />
        <div className="flex items-center gap-3 font-semibold text-2xl"><ShieldCheck className="w-8 h-8 text-indigo-400" />SafeKeep<span className="text-indigo-400">.</span></div>
        <div className="relative py-16 max-w-lg">
          <div className="w-16 h-16 bg-indigo-400/10 border border-indigo-400/20 rounded-2xl grid place-items-center mb-8"><Cloud className="w-8 h-8 text-indigo-300" /></div>
          <div className="text-[40px] xl:text-5xl font-semibold leading-tight tracking-tight">
            {t('auth.heroLine1')}<br /><span className="text-indigo-300">{t('auth.heroLine2')}</span>
          </div>
          <p className="text-slate-400 text-sm leading-7 mt-6">{t('auth.heroDesc')}</p>
          <div className="flex gap-5 text-xs text-slate-300 mt-8">
            <span>{t('auth.feature1')}</span>
            <span className="text-slate-600">/</span>
            <span>{t('auth.feature2')}</span>
            <span className="text-slate-600">/</span>
            <span>{t('auth.feature3')}</span>
          </div>
        </div>
        <div className="text-xs text-slate-500">{t('auth.footerBrand')} · <span className="font-mono">{versionLabel}</span></div>
      </div>

      <div className="relative flex flex-col items-center justify-center p-6 sm:p-12">
        <div className="absolute top-5 right-5 sm:top-6 sm:right-6">
          <PreferenceControls />
        </div>

        <div className="w-full max-w-md bg-white dark:bg-slate-900 rounded-2xl border border-slate-200/80 dark:border-slate-800 shadow-xl shadow-slate-200/30 dark:shadow-black/40 p-6 sm:p-9">
          <div className="flex items-center gap-3 mb-6">
            <div className="w-10 h-10 rounded-xl bg-indigo-600 text-white flex items-center justify-center">
              <ShieldCheck className="w-6 h-6" />
            </div>
            <div>
              <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">
                {mode === 'setup' ? t('auth.setupTitle') : t('auth.loginTitle')}
              </h1>
              <p className="text-xs text-slate-500 dark:text-slate-400">
                {mode === 'setup' ? t('auth.setupSubtitle') : t('auth.loginSubtitle')}
              </p>
            </div>
          </div>

          {mode === 'login' && resetAdminEnvActive && (
            <div className="mb-4">
              <ResetAdminBanner />
            </div>
          )}

          {error && (
            <div role="alert" className="mb-4 p-3 rounded-lg bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 text-sm">
              {error}
            </div>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label htmlFor="auth-username" className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-1">
                {t('auth.username')}
              </label>
              <input
                id="auth-username"
                autoComplete="username"
                autoFocus
                type="text"
                required
                minLength={3}
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                placeholder="admin"
                className="w-full px-3 py-2 border border-slate-300 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
              />
            </div>
            <div>
              <label htmlFor="auth-password" className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-1">
                {t('auth.password')}{mode === 'setup' ? t('auth.passwordMinHint') : ''}
              </label>
              <div className="relative">
                <input
                  id="auth-password"
                  autoComplete={mode === 'setup' ? 'new-password' : 'current-password'}
                  type={showPassword ? 'text' : 'password'}
                  required
                  minLength={8}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder={t('auth.passwordPlaceholder')}
                  className="w-full pl-3 pr-11 py-2 border border-slate-300 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                />
                <button
                  type="button"
                  aria-label={showPassword ? t('auth.hidePassword') : t('auth.showPassword')}
                  onClick={() => setShowPassword(!showPassword)}
                  className="absolute right-2 top-1/2 -translate-y-1/2 p-1.5 text-slate-400 hover:text-indigo-600 dark:hover:text-indigo-400"
                >
                  {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
              </div>
            </div>
            {mode === 'setup' && (
              <div>
                <label htmlFor="auth-confirm" className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-1">
                  {t('auth.confirmPassword')}
                </label>
                <input
                  id="auth-confirm"
                  autoComplete="new-password"
                  type={showPassword ? 'text' : 'password'}
                  required
                  minLength={8}
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  placeholder="••••••••"
                  className="w-full px-3 py-2 border border-slate-300 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                />
              </div>
            )}
            <button
              type="submit"
              disabled={loading}
              className="w-full flex justify-center items-center gap-2 py-2.5 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-50 text-white rounded-lg text-sm font-medium transition"
            >
              {loading && <Loader2 className="w-4 h-4 animate-spin" />}
              {loading ? t('auth.verifying') : mode === 'setup' ? t('auth.setupSubmit') : t('auth.loginSubmit')}
              {!loading && <ArrowRight className="w-4 h-4" />}
            </button>
          </form>

          {mode === 'login' && (
            <details className="mt-4 pt-3 border-t border-slate-100 dark:border-slate-800 text-xs text-slate-500 dark:text-slate-400">
              <summary className="cursor-pointer select-none hover:text-indigo-600 dark:hover:text-indigo-400 font-medium">
                {t('auth.forgotPasswordTitle')}
              </summary>
              <p className="mt-2 leading-5 text-slate-500 dark:text-slate-400">
                {t('auth.forgotPasswordDesc')}
              </p>
            </details>
          )}
        </div>
        <div className="flex items-center gap-1.5 text-xs text-slate-400 dark:text-slate-500 mt-6">
          <LockKeyhole className="w-3.5 h-3.5" />
          {t('auth.securityNote')}
        </div>
      </div>
    </div>
  )
}

interface ForcePasswordChangeProps {
  admin: Admin
  version?: string
  resetAdminEnvActive?: boolean
  onSuccess: (admin: Admin, resetAdminEnvActive?: boolean) => void
  onLogout: () => void
}

export const ForcePasswordChangePage: React.FC<ForcePasswordChangeProps> = ({
  admin,
  version = 'dev',
  resetAdminEnvActive = false,
  onSuccess,
  onLogout,
}) => {
  const { t } = useI18n()
  const versionLabel = version && version !== 'dev' && !version.startsWith('v') ? `v${version}` : version || 'dev'
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    if (password !== confirm) {
      setError(t('auth.passwordMismatch'))
      return
    }
    setLoading(true)
    try {
      const res = await api.changePassword('', password)
      const nextAdmin = res.admin ? res.admin : { ...admin, must_change_password: false }
      onSuccess(nextAdmin, res.reset_admin_env_active)
    } catch (err: any) {
      setError(err?.message || t('auth.actionFailed'))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex flex-col items-center justify-center p-6 sm:p-12 bg-[#f7f9fc] dark:bg-[#0b1120] transition-colors relative">
      <div className="absolute top-5 right-5 sm:top-6 sm:right-6">
        <PreferenceControls />
      </div>

      <div className="w-full max-w-md space-y-4">
        {resetAdminEnvActive && <ResetAdminBanner />}

        <div className="bg-white dark:bg-slate-900 rounded-2xl border border-slate-200/80 dark:border-slate-800 shadow-xl shadow-slate-200/30 dark:shadow-black/40 p-6 sm:p-9">
          <div className="flex items-center gap-3 mb-6">
            <div className="w-10 h-10 rounded-xl bg-amber-500 text-white flex items-center justify-center shrink-0">
              <LockKeyhole className="w-5 h-5" />
            </div>
            <div>
              <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">
                {t('auth.forceResetTitle')}
              </h1>
              <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5 leading-5">
                {t('auth.forceResetSubtitle', { username: admin.username })}
              </p>
            </div>
          </div>

          {error && (
            <div role="alert" className="mb-4 p-3 rounded-lg bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 text-sm">
              {error}
            </div>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label htmlFor="force-new-password" className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-1">
                {t('auth.newPassword')}{t('auth.passwordMinHint')}
              </label>
              <div className="relative">
                <input
                  id="force-new-password"
                  autoComplete="new-password"
                  autoFocus
                  type={showPassword ? 'text' : 'password'}
                  required
                  minLength={8}
                  disabled={loading}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder={t('auth.passwordPlaceholder')}
                  className="w-full pl-3 pr-11 py-2 border border-slate-300 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                />
                <button
                  type="button"
                  disabled={loading}
                  aria-label={showPassword ? t('auth.hidePassword') : t('auth.showPassword')}
                  onClick={() => setShowPassword(!showPassword)}
                  className="absolute right-2 top-1/2 -translate-y-1/2 p-1.5 text-slate-400 hover:text-indigo-600 dark:hover:text-indigo-400"
                >
                  {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
              </div>
            </div>

            <div>
              <label htmlFor="force-confirm-password" className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-1">
                {t('auth.confirmPassword')}
              </label>
              <input
                id="force-confirm-password"
                autoComplete="new-password"
                type={showPassword ? 'text' : 'password'}
                required
                minLength={8}
                disabled={loading}
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                placeholder="••••••••"
                className="w-full px-3 py-2 border border-slate-300 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
              />
            </div>

            <div className="flex items-center gap-2.5 pt-1">
              <button
                type="button"
                disabled={loading}
                onClick={onLogout}
                className="px-4 py-2.5 rounded-lg border border-slate-200 dark:border-slate-700 text-sm text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50"
              >
                {t('auth.backToLogin')}
              </button>
              <button
                type="submit"
                disabled={loading}
                className="flex-1 flex justify-center items-center gap-2 py-2.5 px-4 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-50 text-white rounded-lg text-sm font-medium transition"
              >
                {loading && <Loader2 className="w-4 h-4 animate-spin" />}
                {loading ? t('auth.verifying') : t('auth.forceResetSubmit')}
                {!loading && <ArrowRight className="w-4 h-4" />}
              </button>
            </div>
          </form>
        </div>

        <div className="text-center text-xs text-slate-400 dark:text-slate-500">
          {t('auth.footerBrand')} · <span className="font-mono">{versionLabel}</span>
        </div>
      </div>
    </div>
  )
}

interface SessionExpiredModalProps {
  initialUsername: string
  onSuccess: (admin: Admin, resetAdminEnvActive?: boolean) => void
  onLogout: () => void
}

export const SessionExpiredModal: React.FC<SessionExpiredModalProps> = ({
  initialUsername,
  onSuccess,
  onLogout,
}) => {
  const { t } = useI18n()
  const toast = useToast()
  const descriptionId = useId()
  const [username, setUsername] = useState(initialUsername)
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      const res = await api.login(username.trim(), password)
      toast.success(t('auth.reloginSuccess'))
      onSuccess(res.admin, res.reset_admin_env_active)
    } catch (err: any) {
      setError(formatAuthError(err, t))
    } finally {
      setLoading(false)
    }
  }

  return (
    <Modal
      title={t('auth.sessionExpiredTitle')}
      onClose={() => {}}
      busy={loading}
      dismissible={false}
      size="md"
      descriptionId={descriptionId}
      backdropClassName="!z-[70]"
    >
      <div className="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded-2xl p-6 space-y-5">
        <div className="flex items-start gap-3">
          <div className="w-10 h-10 shrink-0 rounded-xl bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-400 grid place-items-center">
            <LockKeyhole aria-hidden="true" className="w-5 h-5" />
          </div>
          <div className="min-w-0">
            <h2 className="text-base font-semibold text-slate-900 dark:text-slate-100">
              {t('auth.sessionExpiredTitle')}
            </h2>
            <p id={descriptionId} className="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
              {t('auth.sessionExpiredSubtitle')}
            </p>
          </div>
        </div>

        {error && (
          <div role="alert" className="p-3 rounded-lg bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 text-sm">
            {error}
          </div>
        )}

        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label htmlFor="reauth-username" className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-1">
              {t('auth.username')}
            </label>
            <input
              id="reauth-username"
              autoComplete="username"
              data-autofocus={!initialUsername ? true : undefined}
              type="text"
              required
              minLength={3}
              disabled={loading}
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="admin"
              className="w-full px-3 py-2 border border-slate-300 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
            />
          </div>

          <div>
            <label htmlFor="reauth-password" className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-1">
              {t('auth.password')}
            </label>
            <div className="relative">
              <input
                id="reauth-password"
                autoComplete="current-password"
                data-autofocus={initialUsername ? true : undefined}
                type={showPassword ? 'text' : 'password'}
                required
                minLength={8}
                disabled={loading}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t('auth.passwordPlaceholder')}
                className="w-full pl-3 pr-11 py-2 border border-slate-300 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
              />
              <button
                type="button"
                disabled={loading}
                aria-label={showPassword ? t('auth.hidePassword') : t('auth.showPassword')}
                onClick={() => setShowPassword(!showPassword)}
                className="absolute right-2 top-1/2 -translate-y-1/2 p-1.5 text-slate-400 hover:text-indigo-600 dark:hover:text-indigo-400"
              >
                {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
          </div>

          <div className="flex items-center justify-end gap-2.5 pt-1">
            <button
              type="button"
              disabled={loading}
              onClick={onLogout}
              className="px-4 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50"
            >
              {t('auth.backToLogin')}
            </button>
            <button
              type="submit"
              disabled={loading}
              className="inline-flex items-center justify-center gap-2 px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-50 text-white rounded-lg text-sm font-medium transition"
            >
              {loading && <Loader2 className="w-4 h-4 animate-spin" />}
              {loading ? t('auth.verifying') : t('auth.reloginSubmit')}
              {!loading && <ArrowRight className="w-4 h-4" />}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  )
}
