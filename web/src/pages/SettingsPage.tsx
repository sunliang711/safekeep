import React, { useEffect, useState } from 'react'
import { Check, CheckCircle2, Copy, Eye, EyeOff, Loader2, Mail, SlidersHorizontal, Users, XCircle } from 'lucide-react'
import { Modal } from '../components/Modal'
import { LoadingState } from '../components/Feedback'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import { copyText } from '../utils/clipboard'
import type { Admin, SMTPConfig, SystemSettings } from '../types'

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

function smtpSnapshot(cfg: {
  enabled: boolean
  host: string
  port: number
  tls_mode: string
  username: string
  from_email: string
  from_name: string
  to_emails: string[]
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
  })
}

export const SettingsPage: React.FC = () => {
  const toast = useToast()
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
      setLoadError(e.message || '加载设置失败')
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
      toast.error('默认单文件上限必须大于 0')
      return
    }
    setSavingGeneral(true)
    try {
      const updated = await api.updateSettings(settings)
      setSettings(updated)
      syncSizeFieldsFromSettings(updated)
      setSavedGeneralSnap(JSON.stringify(updated))
      toast.success('全局运行设置已保存')
    } catch (e: any) {
      toast.error(e.message || '保存全局设置失败')
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
      if (!confirm('系统设置尚未保存，确定离开并放弃修改吗？')) event.preventDefault()
    }
    window.addEventListener('beforeunload', beforeUnload)
    window.addEventListener('safekeep:before-navigate', beforeNavigate)
    return () => {
      window.removeEventListener('beforeunload', beforeUnload)
      window.removeEventListener('safekeep:before-navigate', beforeNavigate)
    }
  }, [generalDirty, smtpDirty])

  const saveSMTP = async (e: React.FormEvent) => {
    e.preventDefault()
    setSavingSmtp(true)
    try {
      const updated = await api.updateSMTP(smtpPayload())
      applySavedSmtp(updated)
      toast.success('SMTP 邮件告警配置已保存')
    } catch (e: any) {
      toast.error(e.message || '保存 SMTP 配置失败')
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
      toast.success('已复制 SMTP 授权码')
      setTimeout(() => setSmtpPwdCopied(false), 1500)
    } catch (e: any) {
      toast.error(e.message || '复制失败，请点击眼睛图标显示后手动复制')
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
    const nowText = new Date().toLocaleTimeString()
    try {
      const res = await api.testSMTP(smtpPayload())
      const targetText = res.to && res.to.length > 0 ? res.to.join(', ') : smtpToEmails || '配置收件人'
      const serverText = res.host ? `${res.host}:${res.port || smtpPort}` : `${smtpHost}:${smtpPort}`
      const successMsg = `测试邮件发送成功！已发送至 ${targetText}（${serverText}）`
      setSmtpTestResult({ ok: true, message: successMsg, time: nowText })
      toast.success(successMsg)
    } catch (e: any) {
      const failMsg = `测试邮件发送失败：${e.message || '未知错误'}`
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
      toast.success('新管理员已创建')
      const res = await api.listAdmins()
      setAdmins(res.items || [])
    } catch (e: any) {
      toast.error(e.message || '创建管理员失败')
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
      toast.success('当前管理员密码已修改')
    } catch (e: any) {
      toast.error(e.message || '修改密码失败')
    } finally {
      setChangingOwnPwd(false)
    }
  }

  const subTabs: { id: SettingsSubTab; label: string; shortLabel: string; desc: string; icon: React.ReactNode; badge?: string }[] = [
    {
      id: 'general',
      label: '全局运行参数',
      shortLabel: '运行参数',
      desc: '单文件上限、每日熔断、并发',
      icon: <SlidersHorizontal className="w-4 h-4 shrink-0" />,
    },
    {
      id: 'smtp',
      label: 'SMTP 邮件告警',
      shortLabel: '邮件告警',
      desc: '超限拒绝与流量熔断通知',
      icon: <Mail className="w-4 h-4 shrink-0" />,
      badge: smtpEnabled ? '已启用' : '未启用',
    },
    {
      id: 'admins',
      label: '管理员账号',
      shortLabel: '管理员',
      desc: '管理员列表与密码管理',
      icon: <Users className="w-4 h-4 shrink-0" />,
      badge: `${admins.length}`,
    },
  ]

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-bold text-slate-900">系统设置</h1>
        <p className="text-sm text-slate-500">管理全局运行参数、SMTP 邮件告警服务与管理员账号</p>
      </div>

      <div className="flex flex-col xl:flex-row gap-6 items-start">
        {/* Left Vertical Tab Navigation */}
        <aside aria-label="设置分类" className="w-full xl:w-60 shrink-0 bg-white rounded-xl border border-slate-200 p-2 grid grid-cols-3 xl:flex xl:flex-col gap-1">
          {subTabs.map((item) => {
            const active = subTab === item.id
            return (
              <button
                key={item.id}
                type="button"
                aria-current={active ? 'page' : undefined}
                onClick={() => switchTab(item.id)}
                className={`flex items-center justify-center xl:justify-between gap-2 px-2 xl:px-3 py-2.5 rounded-lg text-left transition whitespace-nowrap xl:whitespace-normal shrink-0 xl:w-full ${
                  active
                    ? 'bg-indigo-50 text-indigo-700 border border-indigo-200'
                    : 'text-slate-600 hover:bg-slate-50 border border-transparent'
                }`}
              >
                <div className="flex flex-col xl:flex-row items-center xl:items-start gap-2 xl:gap-2.5 min-w-0">
                  <span className="mt-0.5">{item.icon}</span>
                  <div className="min-w-0">
                    <div className={`text-sm ${active ? 'font-semibold' : 'font-medium'}`}><span className="xl:hidden">{item.shortLabel}</span><span className="hidden xl:inline">{item.label}</span></div>
                    <div className="hidden xl:block text-[11px] text-slate-400 mt-0.5 truncate">{item.desc}</div>
                  </div>
                </div>
                {item.badge && (
                  <span
                    className={`hidden xl:inline text-[10px] px-1.5 py-0.5 rounded font-medium shrink-0 ${
                      active ? 'bg-indigo-100 text-indigo-700' : 'bg-slate-100 text-slate-500'
                    }`}
                  >
                    {item.badge}
                  </span>
                )}
              </button>
            )
          })}
        </aside>

        {/* Right Panel */}
        <section className="flex-1 min-w-0 w-full">
          {loading ? (
            <LoadingState label="正在加载系统设置…" />
          ) : loadError ? <div role="alert" className="rounded-xl border border-rose-200 bg-rose-50 p-5 text-sm text-rose-700">{loadError}<button className="ml-4 underline" onClick={load}>重新加载</button></div> : (
            <>
              {subTab === 'general' && settings && (
                <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
                  <div>
                    <h2 className="font-semibold text-slate-900">全局运行参数</h2>
                    <p className="text-xs text-slate-500 mt-0.5">控制本地缓冲、上传限流与熔断策略，修改后立即对新任务生效（Worker 数需重启）</p>
                  </div>
                  <form onSubmit={saveSettings} className="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
                    <div>
                      <label htmlFor="settings-field-1" className="block text-xs text-slate-600 mb-1">临时缓冲目录 (spool_dir)</label>
                      <input id="settings-field-1"
                        value={settings.spool_dir}
                        onChange={(e) => setSettings({ ...settings, spool_dir: e.target.value })}
                        className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                      />
                    </div>
                    <div>
                      <label htmlFor="settings-field-2" className="block text-xs text-slate-600 mb-1">公网回调 Base URL（Dropbox OAuth2）</label>
                      <input id="settings-field-2"
                        value={settings.public_base_url}
                        onChange={(e) => setSettings({ ...settings, public_base_url: e.target.value })}
                        className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                      />
                    </div>
                    <div>
                      <label htmlFor="settings-field-3" className="block text-xs text-slate-600 mb-1">
                        默认单文件上限（默认 100 MB）
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
                          aria-label="默认单文件上限单位"
                          value={maxFileSizeField.unit}
                          onChange={(e) =>
                            updateByteField('max_file_size_bytes', setMaxFileSizeField, {
                              ...maxFileSizeField,
                              unit: e.target.value as SizeUnit,
                            })
                          }
                          className="w-20 px-2 py-1.5 border rounded-lg bg-white font-mono text-xs"
                        >
                          <option value="KB">KB</option>
                          <option value="MB">MB</option>
                          <option value="GB">GB</option>
                        </select>
                      </div>
                      <div className="text-[11px] text-slate-400 mt-1">
                        当前：{formatBytes(settings.max_file_size_bytes)}（{settings.max_file_size_bytes.toLocaleString()} 字节）
                      </div>
                    </div>
                    <div>
                      <label htmlFor="settings-field-4" className="block text-xs text-slate-600 mb-1">
                        每日发送熔断阈值（默认 1 GB，0=不熔断）
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
                          aria-label="每日发送熔断阈值单位"
                          value={dailyLimitField.unit}
                          onChange={(e) =>
                            updateByteField('daily_traffic_limit_bytes', setDailyLimitField, {
                              ...dailyLimitField,
                              unit: e.target.value as SizeUnit,
                            })
                          }
                          className="w-20 px-2 py-1.5 border rounded-lg bg-white font-mono text-xs"
                        >
                          <option value="KB">KB</option>
                          <option value="MB">MB</option>
                          <option value="GB">GB</option>
                        </select>
                      </div>
                      <div className="text-[11px] text-slate-400 mt-1">
                        {settings.daily_traffic_limit_bytes > 0
                          ? `当前：${formatBytes(settings.daily_traffic_limit_bytes)}（${settings.daily_traffic_limit_bytes.toLocaleString()} 字节）`
                          : '当前：不熔断（0 字节）'}
                      </div>
                    </div>
                    <div>
                      <label htmlFor="settings-field-5" className="block text-xs text-slate-600 mb-1">
                        本地缓冲总容量上限（0=不限）
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
                          aria-label="本地缓冲总容量上限单位"
                          value={maxSpoolField.unit}
                          onChange={(e) =>
                            updateByteField('max_spool_bytes', setMaxSpoolField, {
                              ...maxSpoolField,
                              unit: e.target.value as SizeUnit,
                            })
                          }
                          className="w-20 px-2 py-1.5 border rounded-lg bg-white font-mono text-xs"
                        >
                          <option value="KB">KB</option>
                          <option value="MB">MB</option>
                          <option value="GB">GB</option>
                        </select>
                      </div>
                      <div className="text-[11px] text-slate-400 mt-1">
                        {settings.max_spool_bytes > 0
                          ? `当前：${formatBytes(settings.max_spool_bytes)}（${settings.max_spool_bytes.toLocaleString()} 字节）`
                          : '当前：不限（0 字节）'}
                      </div>
                    </div>
                    <div>
                      <label htmlFor="settings-field-6" className="block text-xs text-slate-600 mb-1">同步模式超时（秒，超时转后台）</label>
                      <input id="settings-field-6"
                        type="number"
                        min={1}
                        value={settings.sync_timeout_sec}
                        onChange={(e) => setSettings({ ...settings, sync_timeout_sec: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label htmlFor="settings-field-7" className="block text-xs text-slate-600 mb-1">异步并发 Worker 数（重启生效）</label>
                      <input id="settings-field-7"
                        type="number"
                        min={1}
                        value={settings.worker_concurrency}
                        onChange={(e) => setSettings({ ...settings, worker_concurrency: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label htmlFor="settings-field-8" className="block text-xs text-slate-600 mb-1">失败最大重试次数</label>
                      <input id="settings-field-8"
                        type="number"
                        min={1}
                        value={settings.max_retry_attempts}
                        onChange={(e) => setSettings({ ...settings, max_retry_attempts: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div className="md:col-span-2 flex flex-wrap items-center justify-between gap-3 pt-4 border-t">
                      <span role="status" className={`text-xs ${generalDirty ? 'text-amber-600' : 'text-slate-400'}`}>{generalDirty ? '有未保存的修改' : '当前配置已保存'}</span>
                      <button
                        type="submit"
                        disabled={savingGeneral || !generalDirty}
                        className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg font-medium transition-opacity"
                      >
                        {savingGeneral ? '保存中...' : generalDirty ? '保存全局设置' : '已保存'}
                      </button>
                    </div>
                  </form>
                </div>
              )}

              {subTab === 'smtp' && (
                <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div>
                      <h2 className="font-semibold text-slate-900">SMTP 邮件告警设置</h2>
                      <p className="text-xs text-slate-500 mt-0.5">用于单文件超限拒绝（&gt;100 MiB）与每日流量熔断（&gt;1 GiB）告警通知</p>
                    </div>
                    <StatusBadge value={smtpEnabled ? 'enabled' : 'disabled'} label={smtpEnabled ? '已启用' : '未启用'} />
                  </div>
                  <form onSubmit={saveSMTP} className="space-y-4 text-sm">
                    <label className="inline-flex items-center gap-2">
                      <input type="checkbox" checked={smtpEnabled} onChange={(e) => setSmtpEnabled(e.target.checked)} />
                      <span className="font-medium">启用 SMTP 邮件告警</span>
                    </label>
                    <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                      <div>
                        <label htmlFor="settings-field-9" className="block text-xs text-slate-600 mb-1">SMTP 服务器主机</label>
                        <input id="settings-field-9"
                          value={smtpHost}
                          onChange={(e) => setSmtpHost(e.target.value)}
                          placeholder="smtp.qq.com"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label htmlFor="settings-field-10" className="block text-xs text-slate-600 mb-1">端口</label>
                        <input id="settings-field-10"
                          type="number"
                          min={1}
                          max={65535}
                          value={smtpPort}
                          onChange={(e) => setSmtpPort(Number(e.target.value))}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 mb-1">加密方式</label>
                        <div className="inline-flex rounded-lg border border-slate-300 overflow-hidden bg-slate-50 w-full">
                          {(
                            [
                              { value: 'none', label: '无 (25)' },
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
                                className={`flex-1 px-2.5 py-1.5 text-xs font-medium transition border-r last:border-r-0 border-slate-300 ${
                                  active ? 'bg-indigo-600 text-white' : 'bg-white text-slate-600 hover:bg-slate-50'
                                }`}
                              >
                                {opt.label}
                              </button>
                            )
                          })}
                        </div>
                        {smtpPort === 465 && smtpTLSMode !== 'tls' && (
                          <div className="text-[11px] text-amber-700 mt-1">提示：465 端口通常需选择「TLS」直连加密，否则握手会失败。</div>
                        )}
                        {smtpPort === 587 && smtpTLSMode === 'tls' && (
                          <div className="text-[11px] text-amber-700 mt-1">提示：587 端口通常使用「STARTTLS」加密。</div>
                        )}
                      </div>
                      <div>
                        <label htmlFor="settings-field-11" className="block text-xs text-slate-600 mb-1">SMTP 用户名</label>
                        <input id="settings-field-11"
                          value={smtpUsername}
                          onChange={(e) => setSmtpUsername(e.target.value)}
                          placeholder="alerts@example.com（可留空不认证）"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <div className="text-[11px] text-slate-400 mt-1">可留空（不认证）；未加密（无）连接不能使用密码认证</div>
                        {smtpTLSMode === 'none' && smtpUsername.trim() !== '' && (
                          <div className="text-[11px] text-amber-700 mt-0.5">当前选择「无」加密，认证请求会被拒绝，请改用 STARTTLS/TLS 或清空用户名。</div>
                        )}
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 mb-1">
                          SMTP 密码/授权码 {smtpHasPassword ? '（已配置，默认掩码显示 *****）' : ''}
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
                              placeholder={smtpHasPassword ? '输入新密码/授权码（留空保持不变）' : '请输入 SMTP 密码或授权码'}
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
                              className="w-full px-3 py-1.5 pr-8 border rounded-lg font-mono text-xs disabled:bg-slate-100"
                            />
                            <button
                              type="button"
                              disabled={smtpClearPassword || (!smtpHasPassword && smtpPassword === '')}
                              onClick={() => setSmtpShowPwd((v) => !v)}
                              title={smtpShowPwd ? '隐藏授权码' : '查看授权码'}
                              className="absolute right-2 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-700 disabled:opacity-30"
                            >
                              {smtpShowPwd ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                            </button>
                          </div>
                          <button
                            type="button"
                            disabled={smtpClearPassword || (!smtpSavedPassword && smtpPassword === '')}
                            onClick={copySmtpPassword}
                            title="复制 SMTP 密码/授权码"
                            className="inline-flex items-center gap-1 px-2.5 py-1.5 border border-slate-300 hover:bg-slate-50 disabled:opacity-40 rounded-lg text-xs text-slate-700 shrink-0"
                          >
                            {smtpPwdCopied ? (
                              <>
                                <Check className="w-3.5 h-3.5 text-emerald-600" />
                                <span className="text-emerald-700">已复制</span>
                              </>
                            ) : (
                              <>
                                <Copy className="w-3.5 h-3.5" />
                                <span>复制</span>
                              </>
                            )}
                          </button>
                        </div>
                        {smtpHasPassword && (
                          <label className="inline-flex items-center gap-1.5 text-[11px] text-slate-500 mt-1">
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
                            <span>清除已有 SMTP 密码</span>
                          </label>
                        )}
                      </div>
                      <div>
                        <label htmlFor="settings-field-12" className="block text-xs text-slate-600 mb-1">发件人名称</label>
                        <input id="settings-field-12"
                          value={smtpFromName}
                          onChange={(e) => setSmtpFromName(e.target.value)}
                          placeholder="SafeKeep"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label htmlFor="settings-field-13" className="block text-xs text-slate-600 mb-1">发件人邮箱（支持 `名称 &lt;邮箱&gt;`）</label>
                        <input id="settings-field-13"
                          value={smtpFromEmail}
                          onChange={(e) => setSmtpFromEmail(e.target.value)}
                          placeholder="alerts@example.com 或 SafeKeep <alerts@example.com>"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label htmlFor="settings-field-14" className="block text-xs text-slate-600 mb-1">告警收件人邮箱（多个用逗号、分号或换行分隔）</label>
                        <input id="settings-field-14"
                          value={smtpToEmails}
                          onChange={(e) => setSmtpToEmails(e.target.value)}
                          placeholder="ops@example.com, admin@example.com"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                    </div>
                    {smtpTesting && (
                      <div className="p-3 rounded-lg bg-slate-50 border border-slate-200 text-slate-700 text-xs flex items-center gap-2">
                        <Loader2 className="w-4 h-4 animate-spin text-indigo-600 shrink-0" />
                        <span>正在连接 SMTP 服务器并发送测试邮件（超时 5 秒），请稍候...</span>
                      </div>
                    )}

                    {smtpTestResult && (
                      <div
                        className={`p-3 rounded-lg border text-xs flex items-start gap-2 ${
                          smtpTestResult.ok
                            ? 'bg-emerald-50 border-emerald-200 text-emerald-800'
                            : 'bg-rose-50 border-rose-200 text-rose-800'
                        }`}
                      >
                        {smtpTestResult.ok ? (
                          <CheckCircle2 className="w-4 h-4 text-emerald-600 shrink-0 mt-0.5" />
                        ) : (
                          <XCircle className="w-4 h-4 text-rose-600 shrink-0 mt-0.5" />
                        )}
                        <div className="min-w-0 flex-1">
                          <div className="font-medium break-all">{smtpTestResult.message}</div>
                          <div className="opacity-75 mt-0.5">检测时间：{smtpTestResult.time}</div>
                        </div>
                      </div>
                    )}

                    <div className="flex justify-end gap-2 pt-2 border-t">
                      <button
                        type="button"
                        onClick={testSMTP}
                        disabled={smtpTesting || savingSmtp}
                        className="px-4 py-2 border border-slate-300 hover:bg-slate-50 disabled:opacity-50 rounded-lg text-sm"
                      >
                        {smtpTesting ? '发送中...' : '发送测试邮件'}
                      </button>
                      <button
                        type="submit"
                        disabled={savingSmtp || !smtpDirty}
                        className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg font-medium transition-opacity"
                      >
                        {savingSmtp ? '保存中...' : smtpDirty ? '保存 SMTP 配置' : '已保存'}
                      </button>
                    </div>
                  </form>
                </div>
              )}

              {subTab === 'admins' && (
                <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
                  <div>
                    <h2 className="font-semibold text-slate-900">管理员账号管理</h2>
                    <p className="text-xs text-slate-500 mt-0.5">管理可登录 Web 管理端的管理员账号、重置密码及启停状态</p>
                  </div>
                  <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
                    <div className="space-y-2">
                      {admins.map((a) => (
                        <div key={a.id} className="flex flex-wrap gap-3 items-center justify-between p-3 bg-slate-50 rounded-lg text-sm border border-slate-100">
                          <div>
                            <div className="font-medium text-slate-900">{a.username}</div>
                            <div className="text-xs text-slate-400">最近登录: {formatTime(a.last_login_at)}</div>
                          </div>
                          <div className="flex items-center gap-2">
                            <StatusBadge value={a.is_active ? 'enabled' : 'disabled'} label={a.is_active ? '启用' : '禁用'} />
                            <button
                              onClick={() => { setResetAdmin(a); setResetPassword('') }}
                              className="text-xs text-slate-600 hover:underline"
                            >
                              重置密码
                            </button>
                            <button
                              onClick={async () => {
                                if (a.is_active && !confirm(`确定禁用管理员「${a.username}」吗？`)) return
                                try {
                                  await api.updateAdmin(a.id, { is_active: !a.is_active })
                                  toast.success(`已${a.is_active ? '禁用' : '启用'}管理员 "${a.username}"`)
                                  const res = await api.listAdmins()
                                  setAdmins(res.items || [])
                                } catch (e: any) {
                                  toast.error(e.message || '更新管理员状态失败')
                                }
                              }}
                              className="text-xs text-indigo-600 hover:underline"
                            >
                              {a.is_active ? '禁用' : '启用'}
                            </button>
                          </div>
                        </div>
                      ))}
                    </div>
                    <div className="space-y-4">
                      <form onSubmit={handleCreateAdmin} className="space-y-2.5 text-sm border rounded-xl p-4">
                        <div className="font-medium text-slate-800">新增管理员</div>
                        <input
                          required
                          minLength={3}
                          aria-label="新管理员用户名"
                          autoComplete="off"
                          placeholder="用户名"
                          value={newAdminUser}
                          onChange={(e) => setNewAdminUser(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <input
                          type="password"
                          required
                          minLength={8}
                          aria-label="新管理员密码"
                          autoComplete="new-password"
                          placeholder="密码（>=8位）"
                          value={newAdminPass}
                          onChange={(e) => setNewAdminPass(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <button
                          type="submit"
                          disabled={creatingAdmin || newAdminUser.trim().length < 3 || newAdminPass.length < 8}
                          className="px-4 py-1.5 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg text-xs font-medium"
                        >
                          {creatingAdmin ? '添加中...' : '添加管理员'}
                        </button>
                      </form>
                      <form onSubmit={handleChangeOwnPassword} className="space-y-2.5 text-sm border rounded-xl p-4">
                        <div className="font-medium text-slate-800">修改当前登录密码</div>
                        <input
                          type="password"
                          required
                          aria-label="当前密码"
                          autoComplete="current-password"
                          placeholder="当前密码"
                          value={oldPwd}
                          onChange={(e) => setOldPwd(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <input
                          type="password"
                          required
                          minLength={8}
                          aria-label="新密码"
                          autoComplete="new-password"
                          placeholder="新密码（>=8位）"
                          value={newPwd}
                          onChange={(e) => setNewPwd(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <button
                          type="submit"
                          disabled={changingOwnPwd || !oldPwd || newPwd.length < 8}
                          className="px-4 py-1.5 bg-slate-900 hover:bg-slate-800 disabled:opacity-45 disabled:cursor-not-allowed text-white rounded-lg text-xs font-medium"
                        >
                          {changingOwnPwd ? '更新中...' : '更新密码'}
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
      {resetAdmin && <Modal title="重置管理员密码" onClose={() => setResetAdmin(null)} busy={resettingPassword} size="md">
        <div className="bg-white rounded-2xl p-6 space-y-4">
          <h2 className="text-lg font-semibold text-slate-900">重置管理员密码</h2>
          <p className="text-sm text-slate-500">为「{resetAdmin.username}」设置新密码，保存后立即生效。</p>
          <form className="space-y-4" onSubmit={async (event) => {
            event.preventDefault()
            if (resettingPassword || resetPassword.length < 8) return
            setResettingPassword(true)
            try {
              await api.updateAdmin(resetAdmin.id, { password: resetPassword })
              setResetAdmin(null)
              setResetPassword('')
              toast.success('管理员密码已重置')
            } catch (e: any) {
              toast.error(e.message || '重置密码失败')
            } finally {
              setResettingPassword(false)
            }
          }}>
            <div><label htmlFor="admin-reset-password" className="block text-xs text-slate-600 mb-2">新密码（至少 8 位）</label><input id="admin-reset-password" type="password" autoComplete="new-password" minLength={8} required value={resetPassword} onChange={(event) => setResetPassword(event.target.value)} className="w-full border rounded-lg px-3 py-2 text-sm" /></div>
            <div className="flex justify-end gap-2 border-t pt-4"><button type="button" disabled={resettingPassword} onClick={() => setResetAdmin(null)} className="px-4 py-2 border rounded-lg text-sm">取消</button><button type="submit" disabled={resettingPassword || resetPassword.length < 8} className="primary-button disabled:opacity-40">{resettingPassword ? '保存中…' : '确认重置'}</button></div>
          </form>
        </div>
      </Modal>}
    </div>
  )
}
