import React, { useEffect, useState } from 'react'
import { Check, CheckCircle2, ChevronDown, ChevronRight, Copy, Eye, EyeOff, Loader2, Mail, Server, SlidersHorizontal, Users, XCircle } from 'lucide-react'
import { Modal } from '../components/Modal'
import { useConfirm, type BeforeNavigateDetail } from '../components/ConfirmDialog'
import { LoadingState } from '../components/Feedback'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import { useI18n } from '../contexts/I18nContext'
import { copyText } from '../utils/clipboard'
import type { Admin, NotifyMode, NotifyRules, SMTPConfig, SystemSettings } from '../types'

type SettingsSubTab = 'general' | 'smtp' | 'admins'
type SizeUnit = 'KB' | 'MB' | 'GB'

const SIZE_UNIT_MULTIPLIER: Record<SizeUnit, number> = {
  KB: 1024,
  MB: 1024 * 1024,
  GB: 1024 * 1024 * 1024,
}

function splitBytesToUnit(bytes: number): { amount: string; unit: SizeUnit } {
  if (!bytes || bytes <= 0) return { amount: '0', unit: 'MB' }
  if (bytes % SIZE_UNIT_MULTIPLIER.GB === 0) return { amount: String(bytes / SIZE_UNIT_MULTIPLIER.GB), unit: 'GB' }
  if (bytes % SIZE_UNIT_MULTIPLIER.MB === 0) return { amount: String(bytes / SIZE_UNIT_MULTIPLIER.MB), unit: 'MB' }
  if (bytes % SIZE_UNIT_MULTIPLIER.KB === 0) return { amount: String(bytes / SIZE_UNIT_MULTIPLIER.KB), unit: 'KB' }
  return { amount: String(Number((bytes / SIZE_UNIT_MULTIPLIER.MB).toFixed(4))), unit: 'MB' }
}

interface SizeFieldState {
  amount: string
  unit: SizeUnit
}

const DEFAULT_NOTIFY_RULES: NotifyRules = {
  circuit_breaker_tripped: true,
  oversize_rejected: true,
  backup_upload: 'failure',
  remote_backup: 'failure',
  migration_backup: 'failure',
  migration_export: false,
  migration_import: false,
  upstream_health_check: true,
  client_delete: false,
  client_key_rotate: false,
  resource_delete: false,
  secrets_reveal: false,
  admin_security: false,
}

function normalizeNotifyRules(rules?: Partial<NotifyRules>): NotifyRules {
  return {
    ...DEFAULT_NOTIFY_RULES,
    ...(rules || {}),
  }
}

function smtpSnapshot(cfg: {
  enabled: boolean
  host: string
  port: number
  tls_mode: string
  username: string
  from_email: string
  from_name: string
  to_emails: string[]
  notifications: NotifyRules
}): string {
  return JSON.stringify({
    enabled: cfg.enabled,
    host: cfg.host.trim(),
    port: Number(cfg.port),
    tls_mode: cfg.tls_mode,
    username: cfg.username.trim(),
    from_email: cfg.from_email.trim(),
    from_name: cfg.from_name.trim(),
    to_emails: cfg.to_emails,
    notifications: normalizeNotifyRules(cfg.notifications),
  })
}

export const SettingsPage: React.FC = () => {
  const askConfirm = useConfirm()
  const toast = useToast()
  const { t, lang } = useI18n()
  const [subTab, setSubTab] = useState<SettingsSubTab>('general')
  const [loading, setLoading] = useState(true)
  const [settings, setSettings] = useState<SystemSettings | null>(null)
  const [savedGeneralSnap, setSavedGeneralSnap] = useState('')
  const [savingGeneral, setSavingGeneral] = useState(false)
  const [maxFileSizeField, setMaxFileSizeField] = useState<SizeFieldState>({ amount: '100', unit: 'MB' })
  const [dailyLimitField, setDailyLimitField] = useState<SizeFieldState>({ amount: '1', unit: 'GB' })
  const [maxSpoolField, setMaxSpoolField] = useState<SizeFieldState>({ amount: '10', unit: 'GB' })
  const [admins, setAdmins] = useState<Admin[]>([])

  // SMTP state
  const [smtpEnabled, setSmtpEnabled] = useState(false)
  const [smtpHost, setSmtpHost] = useState('')
  const [smtpPort, setSmtpPort] = useState(587)
  const [smtpTLSMode, setSmtpTLSMode] = useState<'starttls' | 'tls' | 'none'>('starttls')
  const [smtpUsername, setSmtpUsername] = useState('')
  const [smtpPassword, setSmtpPassword] = useState('')
  const [smtpSavedPassword, setSmtpSavedPassword] = useState('')
  const [smtpHasPassword, setSmtpHasPassword] = useState(false)
  const [smtpEditingPwd, setSmtpEditingPwd] = useState(false)
  const [smtpShowPwd, setSmtpShowPwd] = useState(false)
  const [smtpPwdCopied, setSmtpPwdCopied] = useState(false)
  const [smtpClearPassword, setSmtpClearPassword] = useState(false)
  const [smtpFromEmail, setSmtpFromEmail] = useState('')
  const [smtpFromName, setSmtpFromName] = useState('SafeKeep')
  const [smtpToEmails, setSmtpToEmails] = useState('')
  const [notifyRules, setNotifyRules] = useState<NotifyRules>(DEFAULT_NOTIFY_RULES)
  const [smtpServerOpen, setSmtpServerOpen] = useState(false)
  const [savedSmtpSnap, setSavedSmtpSnap] = useState('')
  const [savingSmtp, setSavingSmtp] = useState(false)
  const [smtpTesting, setSmtpTesting] = useState(false)
  const [smtpTestResult, setSmtpTestResult] = useState<{ ok: boolean; message: string; time: string } | null>(null)

  // Admins state
  const [newAdminUser, setNewAdminUser] = useState('')
  const [newAdminPass, setNewAdminPass] = useState('')
  const [creatingAdmin, setCreatingAdmin] = useState(false)
  const [oldPwd, setOldPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [changingOwnPwd, setChangingOwnPwd] = useState(false)
  const [resetAdmin, setResetAdmin] = useState<Admin | null>(null)
  const [resetPassword, setResetPassword] = useState('')
  const [resettingPassword, setResettingPassword] = useState(false)
  const [loadError, setLoadError] = useState('')

  const applySavedSmtp = (smtp: SMTPConfig) => {
    setSmtpEnabled(smtp.enabled)
    setSmtpHost(smtp.host || '')
    setSmtpPort(smtp.port || 587)
    setSmtpTLSMode(smtp.tls_mode || 'starttls')
    setSmtpUsername(smtp.username || '')
    setSmtpHasPassword(smtp.has_password)
    setSmtpSavedPassword(smtp.password || '')
    setSmtpPassword('')
    setSmtpEditingPwd(false)
    setSmtpShowPwd(false)
    setSmtpClearPassword(false)
    setSmtpFromEmail(smtp.from_email || '')
    setSmtpFromName(smtp.from_name || 'SafeKeep')
    setSmtpToEmails((smtp.to_emails || []).join(', '))
    const normalizedRules = normalizeNotifyRules(smtp.notifications)
    setNotifyRules(normalizedRules)
    setSavedSmtpSnap(
      smtpSnapshot({
        enabled: smtp.enabled,
        host: smtp.host || '',
        port: smtp.port || 587,
        tls_mode: smtp.tls_mode || 'starttls',
        username: smtp.username || '',
        from_email: smtp.from_email || '',
        from_name: smtp.from_name || 'SafeKeep',
        to_emails: smtp.to_emails || [],
        notifications: normalizedRules,
      })
    )
  }

  const syncSizeFieldsFromSettings = (s: SystemSettings) => {
    setMaxFileSizeField(splitBytesToUnit(s.max_file_size_bytes))
    setDailyLimitField(splitBytesToUnit(s.daily_traffic_limit_bytes))
    setMaxSpoolField(splitBytesToUnit(s.max_spool_bytes))
  }

  const updateByteField = (
    key: 'max_file_size_bytes' | 'daily_traffic_limit_bytes' | 'max_spool_bytes',
    setter: React.Dispatch<React.SetStateAction<SizeFieldState>>,
    next: SizeFieldState
  ) => {
    setter(next)
    if (!settings) return
    const num = Number(next.amount)
    const computed = Number.isFinite(num) && num >= 0 ? Math.round(num * SIZE_UNIT_MULTIPLIER[next.unit]) : 0
    setSettings({ ...settings, [key]: computed })
  }

  const load = async () => {
    setLoading(true)
    setLoadError('')
    try {
      const [s, a, smtp] = await Promise.all([api.getSettings(), api.listAdmins(), api.getSMTP()])
      setSettings(s)
      syncSizeFieldsFromSettings(s)
      setSavedGeneralSnap(JSON.stringify(s))
      setAdmins(a.items || [])
      applySavedSmtp(smtp)
    } catch (e: any) {
      setLoadError(e.message || t('settings.loadError'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const switchTab = (next: SettingsSubTab) => {
    setSubTab(next)
  }

  const saveSettings = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!settings) return
    if (settings.max_file_size_bytes <= 0) {
      toast.error(t('settings.maxFileSizeZeroErr'))
      return
    }
    setSavingGeneral(true)
    try {
      const updated = await api.updateSettings(settings)
      setSettings(updated)
      syncSizeFieldsFromSettings(updated)
      setSavedGeneralSnap(JSON.stringify(updated))
      toast.success(t('settings.saveGeneralSuccess'))
    } catch (e: any) {
      toast.error(e.message || t('settings.saveGeneralError'))
    } finally {
      setSavingGeneral(false)
    }
  }

  const parseToEmails = (raw: string) =>
    raw
      .split(/[,;\n，；]+/)
      .map((s) => s.trim())
      .filter(Boolean)

  const smtpPayload = () => ({
    enabled: smtpEnabled,
    host: smtpHost.trim(),
    port: Number(smtpPort),
    tls_mode: smtpTLSMode,
    username: smtpUsername.trim(),
    ...(smtpClearPassword ? { clear_password: true } : smtpEditingPwd && smtpPassword !== '' ? { password: smtpPassword } : {}),
    from_email: smtpFromEmail.trim(),
    from_name: smtpFromName.trim(),
    to_emails: parseToEmails(smtpToEmails),
    notifications: notifyRules,
  })

  const currentSmtpSnap = smtpSnapshot({
    enabled: smtpEnabled,
    host: smtpHost,
    port: smtpPort,
    tls_mode: smtpTLSMode,
    username: smtpUsername,
    from_email: smtpFromEmail,
    from_name: smtpFromName,
    to_emails: parseToEmails(smtpToEmails),
    notifications: notifyRules,
  })
  const generalDirty = settings ? JSON.stringify(settings) !== savedGeneralSnap : false
  const smtpDirty =
    savedSmtpSnap !== '' &&
    (currentSmtpSnap !== savedSmtpSnap || smtpClearPassword || (smtpEditingPwd && smtpPassword !== ''))

  useEffect(() => {
    if (!generalDirty && !smtpDirty) return
    // Preserve drafts when leaving the settings workspace.
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    const beforeNavigate = (event: Event) => {
      const navigation = event as CustomEvent<BeforeNavigateDetail>
      navigation.detail.waitUntil(askConfirm(t('settings.leaveConfirm'), { title: t('common.unsavedTitle'), confirmText: t('common.discardChanges') }))
    }
    window.addEventListener('beforeunload', beforeUnload)
    window.addEventListener('safekeep:before-navigate', beforeNavigate)
    return () => {
      window.removeEventListener('beforeunload', beforeUnload)
      window.removeEventListener('safekeep:before-navigate', beforeNavigate)
    }
  }, [generalDirty, smtpDirty, t, askConfirm])

  const saveSMTP = async (e: React.FormEvent) => {
    e.preventDefault()
    setSavingSmtp(true)
    try {
      const updated = await api.updateSMTP(smtpPayload())
      applySavedSmtp(updated)
      toast.success(t('settings.smtpSaveSuccess'))
    } catch (e: any) {
      toast.error(e.message || t('settings.smtpSaveError'))
    } finally {
      setSavingSmtp(false)
    }
  }

  const copySmtpPassword = async () => {
    const valueToCopy = smtpEditingPwd && smtpPassword !== '' ? smtpPassword : smtpSavedPassword
    if (!valueToCopy) return
    try {
      await copyText(valueToCopy)
      setSmtpPwdCopied(true)
      toast.success(t('settings.smtpCopiedPwd'))
      setTimeout(() => setSmtpPwdCopied(false), 1500)
    } catch (e: any) {
      toast.error(e.message || t('settings.smtpCopyPwdFail'))
    }
  }

  const changeSmtpTLSMode = (nextMode: 'starttls' | 'tls' | 'none') => {
    setSmtpTLSMode(nextMode)
    if (smtpPort === 25 || smtpPort === 587 || smtpPort === 465 || !smtpPort) {
      setSmtpPort(nextMode === 'tls' ? 465 : nextMode === 'starttls' ? 587 : 25)
    }
  }

  const testSMTP = async () => {
    setSmtpTestResult(null)
    setSmtpTesting(true)
    const nowText = new Date().toLocaleTimeString(lang === 'en' ? 'en-US' : 'zh-CN', { hour12: false })
    try {
      const res = await api.testSMTP(smtpPayload())
      const targetText = res.to && res.to.length > 0 ? res.to.join(', ') : smtpToEmails || t('settings.smtpDefaultRecipients')
      const serverText = res.host ? `${res.host}:${res.port || smtpPort}` : `${smtpHost}:${smtpPort}`
      const successMsg = t('settings.smtpTestSuccess', { target: targetText, server: serverText })
      setSmtpTestResult({ ok: true, message: successMsg, time: nowText })
      toast.success(successMsg)
    } catch (e: any) {
      const failMsg = t('settings.smtpTestFail', { error: e.message || t('common.unknownError') })
      setSmtpTestResult({ ok: false, message: failMsg, time: nowText })
      toast.error(failMsg)
    } finally {
      setSmtpTesting(false)
    }
  }

  const handleCreateAdmin = async (e: React.FormEvent) => {
    e.preventDefault()
    setCreatingAdmin(true)
    try {
      await api.createAdmin(newAdminUser, newAdminPass)
      setNewAdminUser('')
      setNewAdminPass('')
      toast.success(t('settings.createdAdminToast'))
      const res = await api.listAdmins()
      setAdmins(res.items || [])
    } catch (e: any) {
      toast.error(e.message || t('settings.createAdminError'))
    } finally {
      setCreatingAdmin(false)
    }
  }

  const handleChangeOwnPassword = async (e: React.FormEvent) => {
    e.preventDefault()
    setChangingOwnPwd(true)
    try {
      await api.changePassword(oldPwd, newPwd)
      setOldPwd('')
      setNewPwd('')
      toast.success(t('settings.changedOwnPwdToast'))
    } catch (e: any) {
      toast.error(e.message || t('settings.changeOwnPwdError'))
    } finally {
      setChangingOwnPwd(false)
    }
  }

  const subTabs: { id: SettingsSubTab; label: string; shortLabel: string; desc: string; icon: React.ReactNode; badge?: string }[] = [
    {
      id: 'general',
      label: t('settings.tabGeneral'),
      shortLabel: t('settings.tabGeneralShort'),
      desc: t('settings.tabGeneralDesc'),
      icon: <SlidersHorizontal className="w-4 h-4 shrink-0" />,
    },
    {
      id: 'smtp',
      label: t('settings.tabSmtp'),
      shortLabel: t('settings.tabSmtpShort'),
      desc: t('settings.tabSmtpDesc'),
      icon: <Mail className="w-4 h-4 shrink-0" />,
      badge: smtpEnabled ? t('common.enabled') : t('common.notEnabled'),
    },
    {
      id: 'admins',
      label: t('settings.tabAdmins'),
      shortLabel: t('settings.tabAdminsShort'),
      desc: t('settings.tabAdminsDesc'),
      icon: <Users className="w-4 h-4 shrink-0" />,
      badge: `${admins.length}`,
    },
  ]

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">{t('settings.title')}</h1>
        <p className="text-sm text-slate-500 dark:text-slate-400">{t('settings.subtitle')}</p>
      </div>

      <div className="space-y-5">
        {/* Top Horizontal Tab Navigation */}
        <nav
          aria-label={t('settings.tabsAria')}
          role="tablist"
          className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-1.5 grid grid-cols-3 sm:inline-flex sm:flex-wrap sm:items-center gap-1"
        >
          {subTabs.map((item) => {
            const active = subTab === item.id
            return (
              <button
                key={item.id}
                id={`settings-tab-${item.id}`}
                type="button"
                role="tab"
                aria-selected={active}
                aria-controls={`settings-panel-${item.id}`}
                title={item.desc}
                onClick={() => switchTab(item.id)}
                className={`flex items-center justify-center gap-1.5 sm:gap-2 px-2 sm:px-4 py-2 rounded-[10px] text-xs sm:text-sm transition whitespace-nowrap ${
                  active
                    ? 'bg-indigo-50 dark:bg-indigo-500/15 text-indigo-700 dark:text-indigo-300 border border-indigo-200 dark:border-indigo-500/30 font-semibold'
                    : 'text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 hover:text-slate-900 dark:hover:text-white border border-transparent font-medium'
                }`}
              >
                {item.icon}
                <span>
                  <span className="sm:hidden">{item.shortLabel}</span>
                  <span className="hidden sm:inline">{item.label}</span>
                </span>
                {item.badge && (
                  <span
                    className={`hidden sm:inline-flex items-center text-[11px] px-1.5 py-0.5 rounded font-medium leading-none ${
                      active
                        ? 'bg-indigo-100 dark:bg-indigo-500/25 text-indigo-700 dark:text-indigo-200'
                        : 'bg-slate-100 dark:bg-slate-800 text-slate-500 dark:text-slate-400'
                    }`}
                  >
                    {item.badge}
                  </span>
                )}
              </button>
            )
          })}
        </nav>

        {/* Tab Content Panel */}
        <section
          id={`settings-panel-${subTab}`}
          role="tabpanel"
          aria-labelledby={`settings-tab-${subTab}`}
          className="min-w-0 w-full"
        >
          {loading ? (
            <LoadingState label={t('settings.loading')} />
          ) : loadError ? (
            <div role="alert" className="rounded-xl border border-rose-200 dark:border-rose-500/30 bg-rose-50 dark:bg-rose-500/15 p-5 text-sm text-rose-700 dark:text-rose-300">
              {loadError}
              <button className="ml-4 underline" onClick={load}>{t('common.reload')}</button>
            </div>
          ) : (
            <>
              {subTab === 'general' && settings && (
                <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5 space-y-4">
                  <div>
                    <h2 className="font-semibold text-slate-900 dark:text-slate-100">{t('settings.generalTitle')}</h2>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">{t('settings.generalDesc')}</p>
                  </div>
                  <form onSubmit={saveSettings} className="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
                    <div>
                      <label htmlFor="settings-field-1" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.spoolDir')}</label>
                      <input
                        id="settings-field-1"
                        value={settings.spool_dir}
                        onChange={(e) => setSettings({ ...settings, spool_dir: e.target.value })}
                        className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                      />
                    </div>
                    <div>
                      <label htmlFor="settings-field-2" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.publicBaseUrl')}</label>
                      <input
                        id="settings-field-2"
                        value={settings.public_base_url}
                        onChange={(e) => setSettings({ ...settings, public_base_url: e.target.value })}
                        className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                      />
                    </div>
                    <div>
                      <label htmlFor="settings-field-3" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                        {t('settings.maxFileSize')}
                      </label>
                      <div className="flex gap-1.5">
                        <input
                          id="settings-field-3"
                          type="number"
                          min={0.001}
                          step="any"
                          required
                          value={maxFileSizeField.amount}
                          onChange={(e) =>
                            updateByteField('max_file_size_bytes', setMaxFileSizeField, {
                              ...maxFileSizeField,
                              amount: e.target.value,
                            })
                          }
                          className="flex-1 min-w-0 px-3 py-1.5 border rounded-lg"
                        />
                        <select
                          aria-label={t('settings.maxFileSizeUnitAria')}
                          value={maxFileSizeField.unit}
                          onChange={(e) =>
                            updateByteField('max_file_size_bytes', setMaxFileSizeField, {
                              ...maxFileSizeField,
                              unit: e.target.value as SizeUnit,
                            })
                          }
                          className="w-20 px-2 py-1.5 border rounded-lg bg-white dark:bg-slate-900 font-mono text-xs"
                        >
                          <option value="KB">KB</option>
                          <option value="MB">MB</option>
                          <option value="GB">GB</option>
                        </select>
                      </div>
                      <div className="text-[11px] text-slate-400 mt-1">
                        {t('settings.currentValue', {
                          formatted: formatBytes(settings.max_file_size_bytes),
                          bytes: settings.max_file_size_bytes.toLocaleString(),
                        })}
                      </div>
                    </div>
                    <div>
                      <label htmlFor="settings-field-4" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                        {t('settings.dailyLimit')}
                      </label>
                      <div className="flex gap-1.5">
                        <input
                          id="settings-field-4"
                          type="number"
                          min={0}
                          step="any"
                          required
                          value={dailyLimitField.amount}
                          onChange={(e) =>
                            updateByteField('daily_traffic_limit_bytes', setDailyLimitField, {
                              ...dailyLimitField,
                              amount: e.target.value,
                            })
                          }
                          className="flex-1 min-w-0 px-3 py-1.5 border rounded-lg"
                        />
                        <select
                          aria-label={t('settings.dailyLimitUnitAria')}
                          value={dailyLimitField.unit}
                          onChange={(e) =>
                            updateByteField('daily_traffic_limit_bytes', setDailyLimitField, {
                              ...dailyLimitField,
                              unit: e.target.value as SizeUnit,
                            })
                          }
                          className="w-20 px-2 py-1.5 border rounded-lg bg-white dark:bg-slate-900 font-mono text-xs"
                        >
                          <option value="KB">KB</option>
                          <option value="MB">MB</option>
                          <option value="GB">GB</option>
                        </select>
                      </div>
                      <div className="text-[11px] text-slate-400 mt-1">
                        {settings.daily_traffic_limit_bytes > 0
                          ? t('settings.currentValue', {
                              formatted: formatBytes(settings.daily_traffic_limit_bytes),
                              bytes: settings.daily_traffic_limit_bytes.toLocaleString(),
                            })
                          : t('settings.currentNoLimitBreaker')}
                      </div>
                    </div>
                    <div>
                      <label htmlFor="settings-field-5" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                        {t('settings.maxSpool')}
                      </label>
                      <div className="flex gap-1.5">
                        <input
                          id="settings-field-5"
                          type="number"
                          min={0}
                          step="any"
                          required
                          value={maxSpoolField.amount}
                          onChange={(e) =>
                            updateByteField('max_spool_bytes', setMaxSpoolField, {
                              ...maxSpoolField,
                              amount: e.target.value,
                            })
                          }
                          className="flex-1 min-w-0 px-3 py-1.5 border rounded-lg"
                        />
                        <select
                          aria-label={t('settings.maxSpoolUnitAria')}
                          value={maxSpoolField.unit}
                          onChange={(e) =>
                            updateByteField('max_spool_bytes', setMaxSpoolField, {
                              ...maxSpoolField,
                              unit: e.target.value as SizeUnit,
                            })
                          }
                          className="w-20 px-2 py-1.5 border rounded-lg bg-white dark:bg-slate-900 font-mono text-xs"
                        >
                          <option value="KB">KB</option>
                          <option value="MB">MB</option>
                          <option value="GB">GB</option>
                        </select>
                      </div>
                      <div className="text-[11px] text-slate-400 mt-1">
                        {settings.max_spool_bytes > 0
                          ? t('settings.currentValue', {
                              formatted: formatBytes(settings.max_spool_bytes),
                              bytes: settings.max_spool_bytes.toLocaleString(),
                            })
                          : t('settings.currentUnlimited')}
                      </div>
                    </div>
                    <div>
                      <label htmlFor="settings-field-6" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.syncTimeout')}</label>
                      <input
                        id="settings-field-6"
                        type="number"
                        min={1}
                        value={settings.sync_timeout_sec}
                        onChange={(e) => setSettings({ ...settings, sync_timeout_sec: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label htmlFor="settings-field-7" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.workerConcurrency')}</label>
                      <input
                        id="settings-field-7"
                        type="number"
                        min={1}
                        value={settings.worker_concurrency}
                        onChange={(e) => setSettings({ ...settings, worker_concurrency: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label htmlFor="settings-field-8" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.maxRetry')}</label>
                      <input
                        id="settings-field-8"
                        type="number"
                        min={1}
                        value={settings.max_retry_attempts}
                        onChange={(e) => setSettings({ ...settings, max_retry_attempts: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div className="md:col-span-2 flex flex-wrap items-center justify-between gap-3 pt-4 border-t border-slate-200 dark:border-slate-800">
                      <span role="status" className={`text-xs ${generalDirty ? 'text-amber-600 dark:text-amber-400' : 'text-slate-400'}`}>
                        {generalDirty ? t('settings.dirtyStatus') : t('settings.cleanStatus')}
                      </span>
                      <button
                        type="submit"
                        disabled={savingGeneral || !generalDirty}
                        className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg font-medium transition-opacity"
                      >
                        {savingGeneral ? t('common.saving') : generalDirty ? t('settings.saveGeneralBtn') : t('common.saved')}
                      </button>
                    </div>
                  </form>
                </div>
              )}

              {subTab === 'smtp' && (
                <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5 space-y-4">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <h2 className="font-semibold text-slate-900 dark:text-slate-100">{t('settings.smtpTitle')}</h2>
                      <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">{t('settings.smtpDesc')}</p>
                    </div>
                    <StatusBadge value={smtpEnabled ? 'enabled' : 'disabled'} label={smtpEnabled ? t('common.enabled') : t('common.notEnabled')} />
                  </div>
                  <form onSubmit={saveSMTP} className="space-y-4 text-sm">
                    <label className="inline-flex items-center gap-2 text-slate-800 dark:text-slate-100">
                      <input type="checkbox" checked={smtpEnabled} onChange={(e) => setSmtpEnabled(e.target.checked)} />
                      <span className="font-medium">{t('settings.smtpEnable')}</span>
                    </label>

                    <div className="rounded-xl border border-slate-200 dark:border-slate-800 overflow-hidden">
                      <button
                        type="button"
                        aria-expanded={smtpServerOpen}
                        onClick={() => setSmtpServerOpen((prev) => !prev)}
                        className="w-full px-4 py-3 bg-slate-50/80 dark:bg-slate-800/50 hover:bg-slate-100/80 dark:hover:bg-slate-800/80 flex items-center justify-between gap-3 text-left transition-colors"
                      >
                        <div className="flex items-center gap-2.5 min-w-0">
                          <Server className="w-4 h-4 text-slate-500 dark:text-slate-400 shrink-0" />
                          <div className="min-w-0">
                            <div className="font-medium text-slate-800 dark:text-slate-100">{t('settings.smtpServerSectionTitle')}</div>
                            <div className="text-xs text-slate-500 dark:text-slate-400 truncate mt-0.5">
                              {smtpHost.trim()
                                ? t('settings.smtpServerSummaryConfigured', {
                                    host: smtpHost.trim(),
                                    port: smtpPort,
                                    tls: smtpTLSMode.toUpperCase(),
                                    count: parseToEmails(smtpToEmails).length,
                                  })
                                : t('settings.smtpServerSummaryEmpty')}
                            </div>
                          </div>
                        </div>
                        <span className="inline-flex items-center gap-1 text-xs font-medium text-indigo-600 dark:text-indigo-400 shrink-0">
                          <span>{smtpServerOpen ? t('settings.smtpServerCollapse') : t('settings.smtpServerExpand')}</span>
                          {smtpServerOpen ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
                        </span>
                      </button>
                      {smtpServerOpen && (
                        <div className="p-4 border-t border-slate-200 dark:border-slate-800">
                          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                      <div>
                        <label htmlFor="settings-field-9" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.smtpHost')}</label>
                        <input
                          id="settings-field-9"
                          value={smtpHost}
                          onChange={(e) => setSmtpHost(e.target.value)}
                          placeholder="smtp.qq.com"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label htmlFor="settings-field-10" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.smtpPort')}</label>
                        <input
                          id="settings-field-10"
                          type="number"
                          min={1}
                          max={65535}
                          value={smtpPort}
                          onChange={(e) => setSmtpPort(Number(e.target.value))}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.smtpTls')}</label>
                        <div className="inline-flex rounded-lg border border-slate-300 dark:border-slate-700 overflow-hidden bg-slate-50 dark:bg-slate-800 w-full">
                          {(
                            [
                              { value: 'none', label: t('settings.smtpTlsNone') },
                              { value: 'starttls', label: 'STARTTLS (587)' },
                              { value: 'tls', label: 'TLS (465)' },
                            ] as const
                          ).map((opt) => {
                            const active = smtpTLSMode === opt.value
                            return (
                              <button
                                key={opt.value}
                                type="button"
                                onClick={() => changeSmtpTLSMode(opt.value)}
                                className={`flex-1 px-2.5 py-1.5 text-xs font-medium transition border-r last:border-r-0 border-slate-300 dark:border-slate-700 ${
                                  active
                                    ? 'bg-indigo-600 text-white'
                                    : 'bg-white dark:bg-slate-900 text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800'
                                }`}
                              >
                                {opt.label}
                              </button>
                            )
                          })}
                        </div>
                        {smtpPort === 465 && smtpTLSMode !== 'tls' && (
                          <div className="text-[11px] text-amber-700 dark:text-amber-400 mt-1">{t('settings.smtpPort465Hint')}</div>
                        )}
                        {smtpPort === 587 && smtpTLSMode === 'tls' && (
                          <div className="text-[11px] text-amber-700 dark:text-amber-400 mt-1">{t('settings.smtpPort587Hint')}</div>
                        )}
                      </div>
                      <div>
                        <label htmlFor="settings-field-11" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.smtpUsername')}</label>
                        <input
                          id="settings-field-11"
                          value={smtpUsername}
                          onChange={(e) => setSmtpUsername(e.target.value)}
                          placeholder={t('settings.smtpUsernamePlaceholder')}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <div className="text-[11px] text-slate-400 mt-1">{t('settings.smtpUsernameHint')}</div>
                        {smtpTLSMode === 'none' && smtpUsername.trim() !== '' && (
                          <div className="text-[11px] text-amber-700 dark:text-amber-400 mt-0.5">{t('settings.smtpNoneAuthWarn')}</div>
                        )}
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                          {t('settings.smtpPassword')}{smtpHasPassword ? t('settings.smtpPasswordConfigured') : ''}
                        </label>
                        <div className="flex items-center gap-1.5">
                          <div className="relative flex-1">
                            <input
                              type={
                                smtpEditingPwd
                                  ? smtpShowPwd
                                    ? 'text'
                                    : 'password'
                                  : 'text'
                              }
                              disabled={smtpClearPassword}
                              value={
                                smtpClearPassword
                                  ? ''
                                  : smtpEditingPwd
                                  ? smtpPassword
                                  : smtpHasPassword
                                  ? smtpShowPwd
                                    ? smtpSavedPassword
                                    : '*****'
                                  : smtpPassword
                              }
                              placeholder={smtpHasPassword ? t('settings.smtpPasswordPlaceholderSaved') : t('settings.smtpPasswordPlaceholderNew')}
                              onFocus={() => {
                                if (!smtpClearPassword && smtpHasPassword && !smtpEditingPwd) {
                                  setSmtpEditingPwd(true)
                                  setSmtpPassword('')
                                }
                              }}
                              onBlur={() => {
                                if (smtpEditingPwd && smtpPassword === '' && smtpHasPassword) {
                                  setSmtpEditingPwd(false)
                                }
                              }}
                              onChange={(e) => {
                                if (!smtpEditingPwd) setSmtpEditingPwd(true)
                                setSmtpPassword(e.target.value)
                              }}
                              className="w-full px-3 py-1.5 pr-8 border rounded-lg font-mono text-xs disabled:bg-slate-100 dark:disabled:bg-slate-800"
                            />
                            <button
                              type="button"
                              disabled={smtpClearPassword || (!smtpHasPassword && smtpPassword === '')}
                              onClick={() => setSmtpShowPwd((v) => !v)}
                              title={smtpShowPwd ? t('settings.smtpHidePwd') : t('settings.smtpShowPwd')}
                              className="absolute right-2 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-700 dark:hover:text-slate-200 disabled:opacity-30"
                            >
                              {smtpShowPwd ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                            </button>
                          </div>
                          <button
                            type="button"
                            disabled={smtpClearPassword || (!smtpSavedPassword && smtpPassword === '')}
                            onClick={copySmtpPassword}
                            title={t('settings.smtpCopyPwdTitle')}
                            className="inline-flex items-center gap-1 px-2.5 py-1.5 border border-slate-300 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-40 rounded-lg text-xs text-slate-700 dark:text-slate-200 shrink-0"
                          >
                            {smtpPwdCopied ? (
                              <>
                                <Check className="w-3.5 h-3.5 text-emerald-600 dark:text-emerald-400" />
                                <span className="text-emerald-700 dark:text-emerald-300">{t('common.copied')}</span>
                              </>
                            ) : (
                              <>
                                <Copy className="w-3.5 h-3.5" />
                                <span>{t('common.copy')}</span>
                              </>
                            )}
                          </button>
                        </div>
                        {smtpHasPassword && (
                          <label className="inline-flex items-center gap-1.5 text-[11px] text-slate-500 dark:text-slate-400 mt-1">
                            <input
                              type="checkbox"
                              checked={smtpClearPassword}
                              onChange={(e) => {
                                setSmtpClearPassword(e.target.checked)
                                setSmtpPassword('')
                                setSmtpEditingPwd(false)
                                setSmtpShowPwd(false)
                              }}
                            />
                            <span>{t('settings.smtpClearPwd')}</span>
                          </label>
                        )}
                      </div>
                      <div>
                        <label htmlFor="settings-field-12" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.smtpFromName')}</label>
                        <input
                          id="settings-field-12"
                          value={smtpFromName}
                          onChange={(e) => setSmtpFromName(e.target.value)}
                          placeholder="SafeKeep"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label htmlFor="settings-field-13" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.smtpFromEmail')}</label>
                        <input
                          id="settings-field-13"
                          value={smtpFromEmail}
                          onChange={(e) => setSmtpFromEmail(e.target.value)}
                          placeholder={t('settings.smtpFromEmailPlaceholder')}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label htmlFor="settings-field-14" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('settings.smtpToEmails')}</label>
                          <input
                            id="settings-field-14"
                            value={smtpToEmails}
                            onChange={(e) => setSmtpToEmails(e.target.value)}
                            placeholder="ops@example.com, admin@example.com"
                            className="w-full px-3 py-1.5 border rounded-lg"
                          />
                        </div>
                          </div>
                        </div>
                      )}
                    </div>

                    <div className="space-y-3 pt-1">
                      <div>
                        <h3 className="text-sm font-semibold text-slate-900 dark:text-slate-100">{t('settings.notifyRulesTitle')}</h3>
                        <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">{t('settings.notifyRulesDesc')}</p>
                        {!smtpEnabled && (
                          <p className="text-xs text-amber-700 dark:text-amber-400 mt-1">{t('settings.notifyDisabledHint')}</p>
                        )}
                      </div>

                      {(
                        [
                          {
                            title: t('settings.notifyGroupBackups'),
                            items: [
                              {
                                kind: 'mode' as const,
                                key: 'backup_upload' as const,
                                label: t('settings.notifyBackupUpload'),
                                desc: t('settings.notifyBackupUploadDesc'),
                              },
                              {
                                kind: 'mode' as const,
                                key: 'remote_backup' as const,
                                label: t('settings.notifyRemoteBackup'),
                                desc: t('settings.notifyRemoteBackupDesc'),
                              },
                            ],
                          },
                          {
                            title: t('settings.notifyGroupOps'),
                            items: [
                              {
                                kind: 'mode' as const,
                                key: 'migration_backup' as const,
                                label: t('settings.notifyMigrationBackup'),
                                desc: t('settings.notifyMigrationBackupDesc'),
                              },
                              {
                                kind: 'bool' as const,
                                key: 'upstream_health_check' as const,
                                label: t('settings.notifyUpstreamHealth'),
                                desc: t('settings.notifyUpstreamHealthDesc'),
                              },
                              {
                                kind: 'bool' as const,
                                key: 'migration_export' as const,
                                label: t('settings.notifyMigrationExport'),
                                desc: t('settings.notifyMigrationExportDesc'),
                              },
                              {
                                kind: 'bool' as const,
                                key: 'migration_import' as const,
                                label: t('settings.notifyMigrationImport'),
                                desc: t('settings.notifyMigrationImportDesc'),
                              },
                            ],
                          },
                          {
                            title: t('settings.notifyGroupAlerts'),
                            items: [
                              {
                                kind: 'bool' as const,
                                key: 'circuit_breaker_tripped' as const,
                                label: t('settings.notifyCircuitBreaker'),
                                desc: t('settings.notifyCircuitBreakerDesc'),
                              },
                              {
                                kind: 'bool' as const,
                                key: 'oversize_rejected' as const,
                                label: t('settings.notifyOversize'),
                                desc: t('settings.notifyOversizeDesc'),
                              },
                            ],
                          },
                          {
                            title: t('settings.notifyGroupSecurity'),
                            items: [
                              {
                                kind: 'bool' as const,
                                key: 'client_delete' as const,
                                label: t('settings.notifyClientDelete'),
                                desc: t('settings.notifyClientDeleteDesc'),
                              },
                              {
                                kind: 'bool' as const,
                                key: 'client_key_rotate' as const,
                                label: t('settings.notifyClientKeyRotate'),
                                desc: t('settings.notifyClientKeyRotateDesc'),
                              },
                              {
                                kind: 'bool' as const,
                                key: 'resource_delete' as const,
                                label: t('settings.notifyResourceDelete'),
                                desc: t('settings.notifyResourceDeleteDesc'),
                              },
                              {
                                kind: 'bool' as const,
                                key: 'secrets_reveal' as const,
                                label: t('settings.notifySecretsReveal'),
                                desc: t('settings.notifySecretsRevealDesc'),
                              },
                              {
                                kind: 'bool' as const,
                                key: 'admin_security' as const,
                                label: t('settings.notifyAdminSecurity'),
                                desc: t('settings.notifyAdminSecurityDesc'),
                              },
                            ],
                          },
                        ] as const
                      ).map((group) => (
                        <div key={group.title} className="rounded-xl border border-slate-200 dark:border-slate-800 overflow-hidden">
                          <div className="px-4 py-2 bg-slate-50/80 dark:bg-slate-800/40 border-b border-slate-200 dark:border-slate-800 text-xs font-semibold text-slate-600 dark:text-slate-300">
                            {group.title}
                          </div>
                          <div className="divide-y divide-slate-100 dark:divide-slate-800/80">
                            {group.items.map((item) => (
                              <div key={item.key} className="px-4 py-3 flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2.5">
                                <div className="min-w-0">
                                  <div className="font-medium text-slate-800 dark:text-slate-100 text-sm">{item.label}</div>
                                  <div className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">{item.desc}</div>
                                </div>
                                {item.kind === 'mode' ? (
                                  <div className="inline-flex rounded-lg border border-slate-300 dark:border-slate-700 overflow-hidden bg-slate-50 dark:bg-slate-800 shrink-0 self-start sm:self-center">
                                    {(
                                      [
                                        { value: 'off' as NotifyMode, label: t('settings.notifyModeOff') },
                                        { value: 'failure' as NotifyMode, label: t('settings.notifyModeFailure') },
                                        { value: 'always' as NotifyMode, label: t('settings.notifyModeAlways') },
                                      ]
                                    ).map((opt) => {
                                      const active = notifyRules[item.key] === opt.value
                                      return (
                                        <button
                                          key={opt.value}
                                          type="button"
                                          onClick={() => setNotifyRules((prev) => ({ ...prev, [item.key]: opt.value }))}
                                          className={`px-2.5 py-1 text-xs font-medium transition border-r last:border-r-0 border-slate-300 dark:border-slate-700 ${
                                            active
                                              ? 'bg-indigo-600 text-white'
                                              : 'bg-white dark:bg-slate-900 text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800'
                                          }`}
                                        >
                                          {opt.label}
                                        </button>
                                      )
                                    })}
                                  </div>
                                ) : (
                                  <button
                                    type="button"
                                    role="switch"
                                    aria-checked={notifyRules[item.key]}
                                    aria-label={item.label}
                                    onClick={() => setNotifyRules((prev) => ({ ...prev, [item.key]: !prev[item.key] }))}
                                    className={`relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus:outline-none focus:ring-2 focus:ring-indigo-500/40 self-start sm:self-center ${
                                      notifyRules[item.key] ? 'bg-indigo-600' : 'bg-slate-300 dark:bg-slate-700'
                                    }`}
                                  >
                                    <span
                                      className={`pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow transition ${
                                        notifyRules[item.key] ? 'translate-x-4' : 'translate-x-0'
                                      }`}
                                    />
                                  </button>
                                )}
                              </div>
                            ))}
                          </div>
                        </div>
                      ))}
                    </div>

                    {smtpTesting && (
                      <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/70 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 text-xs flex items-center gap-2">
                        <Loader2 className="w-4 h-4 animate-spin text-indigo-600 dark:text-indigo-400 shrink-0" />
                        <span>{t('settings.smtpTesting')}</span>
                      </div>
                    )}

                    {smtpTestResult && (
                      <div
                        className={`p-3 rounded-lg border text-xs flex items-start gap-2 ${
                          smtpTestResult.ok
                            ? 'bg-emerald-50 dark:bg-emerald-500/15 border-emerald-200 dark:border-emerald-500/30 text-emerald-800 dark:text-emerald-200'
                            : 'bg-rose-50 dark:bg-rose-500/15 border-rose-200 dark:border-rose-500/30 text-rose-800 dark:text-rose-200'
                        }`}
                      >
                        {smtpTestResult.ok ? (
                          <CheckCircle2 className="w-4 h-4 text-emerald-600 dark:text-emerald-400 shrink-0 mt-0.5" />
                        ) : (
                          <XCircle className="w-4 h-4 text-rose-600 dark:text-rose-400 shrink-0 mt-0.5" />
                        )}
                        <div className="min-w-0 flex-1">
                          <div className="font-medium break-all">{smtpTestResult.message}</div>
                          <div className="opacity-75 mt-0.5">{t('settings.smtpTestTime', { time: smtpTestResult.time })}</div>
                        </div>
                      </div>
                    )}

                    <div className="flex justify-end gap-2 pt-2 border-t border-slate-200 dark:border-slate-800">
                      <button
                        type="button"
                        onClick={testSMTP}
                        disabled={smtpTesting || savingSmtp}
                        className="px-4 py-2 border border-slate-300 dark:border-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50 rounded-lg text-sm"
                      >
                        {smtpTesting ? t('settings.smtpSending') : t('settings.smtpSendTest')}
                      </button>
                      <button
                        type="submit"
                        disabled={savingSmtp || !smtpDirty}
                        className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg font-medium transition-opacity"
                      >
                        {savingSmtp ? t('common.saving') : smtpDirty ? t('settings.smtpSaveBtn') : t('common.saved')}
                      </button>
                    </div>
                  </form>
                </div>
              )}

              {subTab === 'admins' && (
                <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5 space-y-4">
                  <div>
                    <h2 className="font-semibold text-slate-900 dark:text-slate-100">{t('settings.adminsTitle')}</h2>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">{t('settings.adminsDesc')}</p>
                  </div>
                  <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
                    <div className="space-y-2">
                      {admins.map((a) => (
                        <div key={a.id} className="flex flex-wrap gap-3 items-center justify-between p-3 bg-slate-50 dark:bg-slate-800/60 rounded-lg text-sm border border-slate-100 dark:border-slate-800">
                          <div>
                            <div className="font-medium text-slate-900 dark:text-slate-100">{a.username}</div>
                            <div className="text-xs text-slate-400">{t('settings.lastLogin', { time: formatTime(a.last_login_at, lang) })}</div>
                          </div>
                          <div className="flex items-center gap-2">
                            <StatusBadge value={a.is_active ? 'enabled' : 'disabled'} label={a.is_active ? t('common.enable') : t('common.disable')} />
                            <button
                              onClick={() => { setResetAdmin(a); setResetPassword('') }}
                              className="text-xs text-slate-600 dark:text-slate-300 hover:underline"
                            >
                              {t('settings.resetPassword')}
                            </button>
                            <button
                              onClick={async () => {
                                if (a.is_active && !await askConfirm(t('settings.disableAdminConfirm', { name: a.username }), { confirmText: t('common.disable'), danger: true })) return
                                try {
                                  await api.updateAdmin(a.id, { is_active: !a.is_active })
                                  toast.success(t('settings.toggledAdminToast', { action: a.is_active ? t('common.disable') : t('common.enable'), name: a.username }))
                                  const res = await api.listAdmins()
                                  setAdmins(res.items || [])
                                } catch (e: any) {
                                  toast.error(e.message || t('settings.updateAdminError'))
                                }
                              }}
                              className="text-xs text-indigo-600 dark:text-indigo-400 hover:underline"
                            >
                              {a.is_active ? t('common.disable') : t('common.enable')}
                            </button>
                          </div>
                        </div>
                      ))}
                    </div>
                    <div className="space-y-4">
                      <form onSubmit={handleCreateAdmin} className="space-y-2.5 text-sm border border-slate-200 dark:border-slate-800 rounded-xl p-4">
                        <div className="font-medium text-slate-800 dark:text-slate-100">{t('settings.addAdminTitle')}</div>
                        <input
                          required
                          minLength={3}
                          aria-label={t('settings.newAdminUserAria')}
                          autoComplete="off"
                          placeholder={t('auth.username')}
                          value={newAdminUser}
                          onChange={(e) => setNewAdminUser(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <input
                          type="password"
                          required
                          minLength={8}
                          aria-label={t('settings.newAdminPassAria')}
                          autoComplete="new-password"
                          placeholder={t('settings.passwordGe8')}
                          value={newAdminPass}
                          onChange={(e) => setNewAdminPass(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <button
                          type="submit"
                          disabled={creatingAdmin || newAdminUser.trim().length < 3 || newAdminPass.length < 8}
                          className="px-4 py-1.5 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg text-xs font-medium"
                        >
                          {creatingAdmin ? t('settings.addingAdmin') : t('settings.addAdminBtn')}
                        </button>
                      </form>
                      <form onSubmit={handleChangeOwnPassword} className="space-y-2.5 text-sm border border-slate-200 dark:border-slate-800 rounded-xl p-4">
                        <div className="font-medium text-slate-800 dark:text-slate-100">{t('settings.changeOwnPwdTitle')}</div>
                        <input
                          type="password"
                          required
                          aria-label={t('settings.currentPwd')}
                          autoComplete="current-password"
                          placeholder={t('settings.currentPwd')}
                          value={oldPwd}
                          onChange={(e) => setOldPwd(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <input
                          type="password"
                          required
                          minLength={8}
                          aria-label={t('settings.newPwd')}
                          autoComplete="new-password"
                          placeholder={t('settings.newPwdGe8')}
                          value={newPwd}
                          onChange={(e) => setNewPwd(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <button
                          type="submit"
                          disabled={changingOwnPwd || !oldPwd || newPwd.length < 8}
                          className="px-4 py-1.5 bg-slate-900 dark:bg-indigo-600 hover:bg-slate-800 dark:hover:bg-indigo-500 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg text-xs font-medium"
                        >
                          {changingOwnPwd ? t('settings.updatingPwd') : t('settings.updatePwdBtn')}
                        </button>
                      </form>
                    </div>
                  </div>
                </div>
              )}
            </>
          )}
        </section>
      </div>

      {resetAdmin && (
        <Modal title={t('settings.resetModalTitle')} onClose={() => setResetAdmin(null)} busy={resettingPassword} size="md">
          <div className="bg-white dark:bg-slate-900 rounded-2xl p-6 space-y-4">
            <h2 className="text-lg font-semibold text-slate-900 dark:text-slate-100">{t('settings.resetModalTitle')}</h2>
            <p className="text-sm text-slate-500 dark:text-slate-400">{t('settings.resetModalDesc', { name: resetAdmin.username })}</p>
            <form
              className="space-y-4"
              onSubmit={async (event) => {
                event.preventDefault()
                if (resettingPassword || resetPassword.length < 8) return
                setResettingPassword(true)
                try {
                  await api.updateAdmin(resetAdmin.id, { password: resetPassword })
                  setResetAdmin(null)
                  setResetPassword('')
                  toast.success(t('settings.resetSuccessToast'))
                } catch (e: any) {
                  toast.error(e.message || t('settings.resetErrorToast'))
                } finally {
                  setResettingPassword(false)
                }
              }}
            >
              <div>
                <label htmlFor="admin-reset-password" className="block text-xs text-slate-600 dark:text-slate-300 mb-2">
                  {t('settings.resetModalLabel')}
                </label>
                <input
                  id="admin-reset-password"
                  type="password"
                  autoComplete="new-password"
                  minLength={8}
                  required
                  value={resetPassword}
                  onChange={(event) => setResetPassword(event.target.value)}
                  className="w-full border rounded-lg px-3 py-2 text-sm"
                />
              </div>
              <div className="flex justify-end gap-2 border-t border-slate-200 dark:border-slate-800 pt-4">
                <button type="button" disabled={resettingPassword} onClick={() => setResetAdmin(null)} className="px-4 py-2 border border-slate-200 dark:border-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-50 dark:hover:bg-slate-800 rounded-lg text-sm">
                  {t('common.cancel')}
                </button>
                <button type="submit" disabled={resettingPassword || resetPassword.length < 8} className="primary-button disabled:opacity-40">
                  {resettingPassword ? t('common.saving') : t('settings.resetConfirmBtn')}
                </button>
              </div>
            </form>
          </div>
        </Modal>
      )}
    </div>
  )
}
