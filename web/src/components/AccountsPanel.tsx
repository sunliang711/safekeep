import React, { useCallback, useEffect, useState } from 'react'
import { Loader2, Plus, RefreshCw } from 'lucide-react'
import { api, formatTime } from '../api'
import { useI18n } from '../contexts/I18nContext'
import { useConfirm } from './ConfirmDialog'
import { LoadingState } from './Feedback'
import { Modal } from './Modal'
import { StatusBadge } from './Badge'
import { useToast } from './Toast'
import type { Admin, AdminSession } from '../types'

type AccountAction = { kind: 'create' | 'reset' | 'role' | 'active'; target?: Admin }

export const AccountsPanel: React.FC<{ admin: Admin; onSignedOut: () => void }> = ({ admin, onSignedOut }) => {
  const { t, lang } = useI18n()
  const toast = useToast()
  const askConfirm = useConfirm()
  const isAdmin = admin.role === 'admin'
  const [sessions, setSessions] = useState<AdminSession[]>([])
  const [accounts, setAccounts] = useState<Admin[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [revoking, setRevoking] = useState('')
  const [oldPassword, setOldPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [changingPassword, setChangingPassword] = useState(false)
  const [action, setAction] = useState<AccountAction | null>(null)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<Admin['role']>('viewer')
  const [currentPassword, setCurrentPassword] = useState('')
  const [saving, setSaving] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [s, a] = await Promise.all([api.listSessions(), isAdmin ? api.listAdmins() : Promise.resolve({ items: [] })])
      setSessions(s.items)
      setAccounts(a.items || [])
    } catch (e: any) {
      setError(e.message || t('accounts.loadError'))
    } finally {
      setLoading(false)
    }
  }, [isAdmin, t])

  useEffect(() => { void load() }, [load])

  const openAction = (next: AccountAction) => {
    setUsername('')
    setPassword('')
    setRole('viewer')
    setCurrentPassword('')
    setAction(next)
  }

  const revokeSession = async (session: AdminSession) => {
    if (revoking) return
    if (session.current && !await askConfirm(t('accounts.revokeCurrentConfirm'), { title: t('accounts.revokeCurrent'), confirmText: t('accounts.revoke'), danger: true })) return
    setRevoking(session.id)
    try {
      await api.revokeSession(session.id)
      if (session.current) {
        onSignedOut()
        return
      }
      toast.success(t('accounts.revoked'))
      await load()
    } catch (e: any) {
      toast.error(e.message || t('accounts.revokeError'))
    } finally {
      setRevoking('')
    }
  }

  const changePassword = async (event: React.FormEvent) => {
    event.preventDefault()
    if (changingPassword) return
    setChangingPassword(true)
    try {
      await api.changePassword(oldPassword, newPassword)
      setOldPassword('')
      setNewPassword('')
      toast.success(t('accounts.passwordChanged'))
      await load()
    } catch (e: any) {
      toast.error(e.message || t('settings.changeOwnPwdError'))
    } finally {
      setChangingPassword(false)
    }
  }

  const saveAccount = async (event: React.FormEvent) => {
    event.preventDefault()
    if (!action || saving) return
    setSaving(true)
    try {
      if (action.kind === 'create') {
        await api.createAdmin(username.trim(), password, role, currentPassword)
      } else if (action.target) {
        const target = action.target
        await api.updateAdmin(target.id, {
          current_password: currentPassword,
          ...(action.kind === 'reset' ? { password } : {}),
          ...(action.kind === 'role' ? { role: target.role === 'admin' ? 'viewer' : 'admin' } : {}),
          ...(action.kind === 'active' ? { is_active: !target.is_active } : {}),
        })
        if (action.kind === 'role' && target.id === admin.id) {
          onSignedOut()
          return
        }
      }
      setAction(null)
      setPassword('')
      setCurrentPassword('')
      toast.success(t('accounts.saved'))
      await load()
    } catch (e: any) {
      toast.error(e.message || t('accounts.saveError'))
    } finally {
      setSaving(false)
    }
  }

  const actionTitle = action?.kind === 'create' ? t('accounts.create')
    : action?.kind === 'reset' ? t('settings.resetPassword')
    : action?.kind === 'role' ? t('accounts.changeRole')
    : action?.target?.is_active ? t('common.disable') : t('common.enable')
  const cardClass = 'rounded-xl border border-slate-200 dark:border-slate-800 bg-white dark:bg-slate-900 p-5'
  const inputClass = 'w-full px-3 py-2 border rounded-lg text-sm'
  const secondaryClass = 'inline-flex items-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-40'

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold text-slate-900 dark:text-slate-100">{t('accounts.title')}</h2>
          <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">{t('accounts.description')}</p>
        </div>
        <button type="button" onClick={() => void load()} disabled={loading || !!revoking || saving || changingPassword} className={secondaryClass}>
          <RefreshCw className={`w-4 h-4 ${loading ? 'animate-spin' : ''}`} />{t('accounts.refresh')}
        </button>
      </div>
      {error && <div role="alert" className="text-sm text-rose-600 dark:text-rose-400">{error}</div>}
      <div className="grid grid-cols-1 xl:grid-cols-2 gap-5">
        <section className={cardClass}>
          <h3 className="font-medium text-slate-900 dark:text-slate-100 mb-4">{t('settings.changeOwnPwdTitle')} ({admin.username})</h3>
          <form onSubmit={changePassword} className="space-y-3">
            <label className="block text-xs text-slate-500 dark:text-slate-400">{t('settings.currentPwd')}
              <input type="password" required autoComplete="current-password" value={oldPassword} onChange={e => setOldPassword(e.target.value)} className={`${inputClass} mt-1`} />
            </label>
            <label className="block text-xs text-slate-500 dark:text-slate-400">{t('settings.newPwdGe8')}
              <input type="password" required minLength={8} maxLength={72} autoComplete="new-password" value={newPassword} onChange={e => setNewPassword(e.target.value)} className={`${inputClass} mt-1`} />
            </label>
            <button type="submit" disabled={changingPassword || loading || !!revoking || saving} className="primary-button disabled:opacity-40">
              {changingPassword && <Loader2 className="w-4 h-4 animate-spin" />}{t('settings.updatePwdBtn')}
            </button>
          </form>
        </section>
        <section className={cardClass}>
          <h3 className="font-medium text-slate-900 dark:text-slate-100 mb-4">{t('accounts.sessions')} ({sessions.length})</h3>
          {loading ? <LoadingState label={t('accounts.loading')} /> : sessions.length === 0 ? (
            <p className="text-sm text-slate-500 dark:text-slate-400">{t('accounts.noSessions')}</p>
          ) : (
            <div className="space-y-3 max-h-96 overflow-y-auto">
              {sessions.map(session => (
                <div key={session.id} className="flex items-start justify-between gap-3 p-3 rounded-lg border border-slate-200 dark:border-slate-800 bg-slate-50 dark:bg-slate-800/40">
                  <div className="min-w-0 space-y-1">
                    <div className="flex flex-wrap items-center gap-2 text-sm font-mono text-slate-800 dark:text-slate-100">
                      {session.client_ip || '—'}{session.current && <StatusBadge value="enabled" label={t('accounts.current')} />}
                    </div>
                    <p title={session.user_agent} className="text-xs truncate text-slate-500 dark:text-slate-400">{session.user_agent || '—'}</p>
                    <p className="text-xs text-slate-500 dark:text-slate-400">{t('accounts.lastSeen', { time: formatTime(session.last_seen_at, lang) })}</p>
                    <p className="text-xs text-slate-500 dark:text-slate-400">{t('accounts.expires', { time: formatTime(session.expires_at, lang) })}</p>
                  </div>
                  <button type="button" disabled={!!revoking || saving || changingPassword} onClick={() => void revokeSession(session)} className="shrink-0 text-xs text-rose-600 dark:text-rose-400 hover:underline disabled:opacity-40">
                    {revoking === session.id ? t('common.loading') : t('accounts.revoke')}
                  </button>
                </div>
              ))}
            </div>
          )}
        </section>
      </div>
      {isAdmin && (
        <section className={cardClass}>
          <div className="flex flex-wrap justify-between items-center gap-3 mb-4">
            <h3 className="font-medium text-slate-900 dark:text-slate-100">{t('accounts.management')}</h3>
            <button type="button" onClick={() => openAction({ kind: 'create' })} className="primary-button"><Plus className="w-4 h-4" />{t('accounts.create')}</button>
          </div>
          {loading ? <LoadingState label={t('accounts.loading')} /> : (
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm whitespace-nowrap">
                <thead className="text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-800">
                  <tr><th className="py-3 pr-4">{t('auth.username')}</th><th className="pr-4">{t('accounts.role')}</th><th className="pr-4">{t('accounts.status')}</th><th className="pr-4">{t('accounts.lastLogin')}</th><th className="text-right">{t('accounts.actions')}</th></tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {accounts.map(account => (
                    <tr key={account.id}>
                      <td className="py-3 pr-4 font-medium text-slate-900 dark:text-slate-100">{account.username}{account.id === admin.id && <span className="ml-2 text-xs text-slate-400">{t('accounts.self')}</span>}</td>
                      <td className="pr-4 text-slate-600 dark:text-slate-300">{account.role === 'admin' ? t('accounts.admin') : t('accounts.viewer')}</td>
                      <td className="pr-4"><StatusBadge value={account.is_active ? 'enabled' : 'disabled'} /></td>
                      <td className="pr-4 text-xs text-slate-500 dark:text-slate-400">{formatTime(account.last_login_at, lang)}</td>
                      <td><div className="flex justify-end gap-3 text-xs">
                        <button type="button" onClick={() => openAction({ kind: 'role', target: account })} className="text-indigo-600 dark:text-indigo-400 hover:underline">{account.role === 'admin' ? t('accounts.setViewer') : t('accounts.setAdmin')}</button>
                        <button type="button" onClick={() => openAction({ kind: 'reset', target: account })} className="text-slate-600 dark:text-slate-300 hover:underline">{t('settings.resetPassword')}</button>
                        <button type="button" disabled={account.id === admin.id && account.is_active} title={account.id === admin.id ? t('accounts.noDisableSelf') : undefined} onClick={() => openAction({ kind: 'active', target: account })} className="text-rose-600 dark:text-rose-400 hover:underline disabled:opacity-30 disabled:cursor-not-allowed">{account.is_active ? t('common.disable') : t('common.enable')}</button>
                      </div></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}
      {action && (
        <Modal title={actionTitle} onClose={() => { setAction(null); setCurrentPassword(''); setPassword('') }} busy={saving} size="md">
          <form onSubmit={saveAccount} className="p-6 space-y-4">
            <h3 className="text-lg font-semibold text-slate-900 dark:text-slate-100">{actionTitle}{action.target && ` · ${action.target.username}`}</h3>
            {action.kind === 'create' && <>
              <label className="block text-xs text-slate-500 dark:text-slate-400">{t('auth.username')}<input required minLength={3} autoComplete="off" value={username} onChange={e => setUsername(e.target.value)} className={`${inputClass} mt-1`} /></label>
              <label className="block text-xs text-slate-500 dark:text-slate-400">{t('accounts.role')}<select value={role} onChange={e => setRole(e.target.value as Admin['role'])} className={`${inputClass} mt-1`}><option value="viewer">{t('accounts.viewer')}</option><option value="admin">{t('accounts.admin')}</option></select></label>
            </>}
            {(action.kind === 'create' || action.kind === 'reset') && <label className="block text-xs text-slate-500 dark:text-slate-400">{t('settings.newPwdGe8')}<input type="password" required minLength={8} maxLength={72} autoComplete="new-password" value={password} onChange={e => setPassword(e.target.value)} className={`${inputClass} mt-1`} /></label>}
            {action.kind === 'role' && <p className="text-sm text-slate-500 dark:text-slate-400">{t('accounts.roleConfirm', { role: action.target?.role === 'admin' ? t('accounts.viewer') : t('accounts.admin') })}</p>}
            <p className="text-xs text-amber-700 dark:text-amber-300">{t('accounts.verifyDescription')}</p>
            <label className="block text-xs text-slate-500 dark:text-slate-400">{t('accounts.verifyPassword')}<input type="password" required autoComplete="current-password" value={currentPassword} onChange={e => setCurrentPassword(e.target.value)} className={`${inputClass} mt-1`} /></label>
            <div className="flex justify-end gap-2">
              <button type="button" disabled={saving} onClick={() => { setAction(null); setCurrentPassword(''); setPassword('') }} className={secondaryClass}>{t('common.cancel')}</button>
              <button type="submit" disabled={saving} className="primary-button disabled:opacity-40">{saving ? t('common.saving') : t('accounts.confirm')}</button>
            </div>
          </form>
        </Modal>
      )}
    </div>
  )
}
