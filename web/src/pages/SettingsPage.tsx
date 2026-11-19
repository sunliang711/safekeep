import React, { useEffect, useState } from 'react'
import { Mail, SlidersHorizontal, Users } from 'lucide-react'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import type { Admin, SystemSettings } from '../types'

type SettingsSubTab = 'general' | 'smtp' | 'admins'

export const SettingsPage: React.FC = () => {
  const [subTab, setSubTab] = useState<SettingsSubTab>('general')
  const [loading, setLoading] = useState(true)
  const [settings, setSettings] = useState<SystemSettings | null>(null)
  const [admins, setAdmins] = useState<Admin[]>([])

  // SMTP state
  const [smtpEnabled, setSmtpEnabled] = useState(false)
  const [smtpHost, setSmtpHost] = useState('')
  const [smtpPort, setSmtpPort] = useState(587)
  const [smtpTLSMode, setSmtpTLSMode] = useState<'starttls' | 'tls' | 'none'>('starttls')
  const [smtpUsername, setSmtpUsername] = useState('')
  const [smtpPassword, setSmtpPassword] = useState('')
  const [smtpHasPassword, setSmtpHasPassword] = useState(false)
  const [smtpClearPassword, setSmtpClearPassword] = useState(false)
  const [smtpFromEmail, setSmtpFromEmail] = useState('')
  const [smtpFromName, setSmtpFromName] = useState('SafeKeep')
  const [smtpToEmails, setSmtpToEmails] = useState('')
  const [smtpTesting, setSmtpTesting] = useState(false)

  // Admins state
  const [newAdminUser, setNewAdminUser] = useState('')
  const [newAdminPass, setNewAdminPass] = useState('')
  const [oldPwd, setOldPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')

  const [msg, setMsg] = useState('')
  const [err, setErr] = useState('')

  const load = async () => {
    setLoading(true)
    try {
      const [s, a, smtp] = await Promise.all([api.getSettings(), api.listAdmins(), api.getSMTP()])
      setSettings(s)
      setAdmins(a.items || [])
      setSmtpEnabled(smtp.enabled)
      setSmtpHost(smtp.host || '')
      setSmtpPort(smtp.port || 587)
      setSmtpTLSMode(smtp.tls_mode || 'starttls')
      setSmtpUsername(smtp.username || '')
      setSmtpHasPassword(smtp.has_password)
      setSmtpPassword('')
      setSmtpClearPassword(false)
      setSmtpFromEmail(smtp.from_email || '')
      setSmtpFromName(smtp.from_name || 'SafeKeep')
      setSmtpToEmails((smtp.to_emails || []).join(', '))
    } catch (e: any) {
      setErr(e.message || '加载设置失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const switchTab = (next: SettingsSubTab) => {
    setSubTab(next)
    setMsg('')
    setErr('')
  }

  const saveSettings = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!settings) return
    setMsg('')
    setErr('')
    try {
      const updated = await api.updateSettings(settings)
      setSettings(updated)
      setMsg('全局运行设置已保存')
    } catch (e: any) {
      setErr(e.message)
    }
  }

  const smtpPayload = () => ({
    enabled: smtpEnabled,
    host: smtpHost,
    port: Number(smtpPort),
    tls_mode: smtpTLSMode,
    username: smtpUsername,
    ...(smtpClearPassword ? { clear_password: true } : smtpPassword !== '' ? { password: smtpPassword } : {}),
    from_email: smtpFromEmail,
    from_name: smtpFromName,
    to_emails: smtpToEmails
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean),
  })

  const saveSMTP = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg('')
    setErr('')
    try {
      const updated = await api.updateSMTP(smtpPayload())
      setSmtpHasPassword(updated.has_password)
      setSmtpPassword('')
      setSmtpClearPassword(false)
      setMsg('SMTP 邮件告警配置已保存')
    } catch (e: any) {
      setErr(e.message)
    }
  }

  const testSMTP = async () => {
    setMsg('')
    setErr('')
    setSmtpTesting(true)
    try {
      await api.testSMTP(smtpPayload())
      setMsg('测试邮件发送成功，请检查收件箱！')
    } catch (e: any) {
      setErr(`测试邮件发送失败: ${e.message}`)
    } finally {
      setSmtpTesting(false)
    }
  }

  const handleCreateAdmin = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg('')
    setErr('')
    try {
      await api.createAdmin(newAdminUser, newAdminPass)
      setNewAdminUser('')
      setNewAdminPass('')
      setMsg('新管理员已创建')
      await load()
    } catch (e: any) {
      setErr(e.message)
    }
  }

  const handleChangeOwnPassword = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg('')
    setErr('')
    try {
      await api.changePassword(oldPwd, newPwd)
      setOldPwd('')
      setNewPwd('')
      setMsg('当前管理员密码已修改')
    } catch (e: any) {
      setErr(e.message)
    }
  }

  const subTabs: { id: SettingsSubTab; label: string; desc: string; icon: React.ReactNode; badge?: string }[] = [
    {
      id: 'general',
      label: '全局运行参数',
      desc: '单文件上限、每日熔断、并发',
      icon: <SlidersHorizontal className="w-4 h-4 shrink-0" />,
    },
    {
      id: 'smtp',
      label: 'SMTP 邮件告警',
      desc: '超限拒绝与流量熔断通知',
      icon: <Mail className="w-4 h-4 shrink-0" />,
      badge: smtpEnabled ? '已启用' : '未启用',
    },
    {
      id: 'admins',
      label: '管理员账号',
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

      {msg && <div className="p-3 bg-emerald-50 border border-emerald-200 text-emerald-800 rounded-lg text-sm">{msg}</div>}
      {err && <div className="p-3 bg-rose-50 border border-rose-200 text-rose-800 rounded-lg text-sm">{err}</div>}

      <div className="flex flex-col md:flex-row gap-6 items-start">
        {/* Left Vertical Tab Navigation */}
        <aside className="w-full md:w-60 shrink-0 bg-white rounded-xl border border-slate-200 p-2 flex md:flex-col gap-1 overflow-x-auto">
          {subTabs.map((item) => {
            const active = subTab === item.id
            return (
              <button
                key={item.id}
                type="button"
                onClick={() => switchTab(item.id)}
                className={`flex items-center justify-between gap-2 px-3 py-2.5 rounded-lg text-left transition whitespace-nowrap md:whitespace-normal shrink-0 md:w-full ${
                  active
                    ? 'bg-indigo-50 text-indigo-700 border border-indigo-200'
                    : 'text-slate-600 hover:bg-slate-50 border border-transparent'
                }`}
              >
                <div className="flex items-start gap-2.5 min-w-0">
                  <span className="mt-0.5">{item.icon}</span>
                  <div className="min-w-0">
                    <div className={`text-sm ${active ? 'font-semibold' : 'font-medium'}`}>{item.label}</div>
                    <div className="hidden md:block text-[11px] text-slate-400 mt-0.5 truncate">{item.desc}</div>
                  </div>
                </div>
                {item.badge && (
                  <span
                    className={`text-[10px] px-1.5 py-0.5 rounded font-medium shrink-0 ${
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
            <div className="bg-white rounded-xl border border-slate-200 p-6 text-sm text-slate-500">正在加载系统设置...</div>
          ) : (
            <>
              {subTab === 'general' && settings && (
                <div className="bg-white rounded-xl border border-slate-200 p-5 space-y-4">
                  <div>
                    <h2 className="font-semibold text-slate-900">全局运行参数</h2>
                    <p className="text-xs text-slate-500 mt-0.5">控制本地缓冲、上传限流与熔断策略，修改后立即对新任务生效（Worker 数需重启）</p>
                  </div>
                  <form onSubmit={saveSettings} className="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">临时缓冲目录 (spool_dir)</label>
                      <input
                        value={settings.spool_dir}
                        onChange={(e) => setSettings({ ...settings, spool_dir: e.target.value })}
                        className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">公网回调 Base URL（Dropbox OAuth2）</label>
                      <input
                        value={settings.public_base_url}
                        onChange={(e) => setSettings({ ...settings, public_base_url: e.target.value })}
                        className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">
                        默认单文件上限（字节，当前 {formatBytes(settings.max_file_size_bytes)}，默认 100 MiB）
                      </label>
                      <input
                        type="number"
                        min={1}
                        value={settings.max_file_size_bytes}
                        onChange={(e) => setSettings({ ...settings, max_file_size_bytes: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">
                        每日发送熔断阈值（字节，当前 {settings.daily_traffic_limit_bytes > 0 ? formatBytes(settings.daily_traffic_limit_bytes) : '不熔断'}，默认 1 GiB）
                      </label>
                      <input
                        type="number"
                        min={0}
                        value={settings.daily_traffic_limit_bytes}
                        onChange={(e) => setSettings({ ...settings, daily_traffic_limit_bytes: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">本地缓冲总容量上限（字节，0=不限）</label>
                      <input
                        type="number"
                        min={0}
                        value={settings.max_spool_bytes}
                        onChange={(e) => setSettings({ ...settings, max_spool_bytes: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">同步模式超时（秒，超时转后台）</label>
                      <input
                        type="number"
                        min={1}
                        value={settings.sync_timeout_sec}
                        onChange={(e) => setSettings({ ...settings, sync_timeout_sec: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">异步并发 Worker 数（重启生效）</label>
                      <input
                        type="number"
                        min={1}
                        value={settings.worker_concurrency}
                        onChange={(e) => setSettings({ ...settings, worker_concurrency: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">失败最大重试次数</label>
                      <input
                        type="number"
                        min={1}
                        value={settings.max_retry_attempts}
                        onChange={(e) => setSettings({ ...settings, max_retry_attempts: Number(e.target.value) })}
                        className="w-full px-3 py-1.5 border rounded-lg"
                      />
                    </div>
                    <div className="md:col-span-2 flex justify-end pt-2 border-t">
                      <button type="submit" className="px-4 py-2 bg-slate-900 hover:bg-slate-800 text-white rounded-lg font-medium">
                        保存全局设置
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
                        <label className="block text-xs text-slate-600 mb-1">SMTP 服务器主机</label>
                        <input
                          value={smtpHost}
                          onChange={(e) => setSmtpHost(e.target.value)}
                          placeholder="smtp.qq.com"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 mb-1">端口</label>
                        <input
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
                        <select
                          value={smtpTLSMode}
                          onChange={(e) => setSmtpTLSMode(e.target.value as any)}
                          className="w-full px-3 py-1.5 border rounded-lg bg-white"
                        >
                          <option value="starttls">STARTTLS (常用 587)</option>
                          <option value="tls">SSL/TLS 直连 (常用 465)</option>
                          <option value="none">不加密 (内网/测试 25)</option>
                        </select>
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 mb-1">SMTP 用户名</label>
                        <input
                          value={smtpUsername}
                          onChange={(e) => setSmtpUsername(e.target.value)}
                          placeholder="alerts@example.com"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 mb-1">
                          SMTP 密码/授权码 {smtpHasPassword ? '（已配置，留空保持不变）' : ''}
                        </label>
                        <input
                          type="password"
                          disabled={smtpClearPassword}
                          value={smtpPassword}
                          onChange={(e) => setSmtpPassword(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg disabled:bg-slate-100"
                        />
                        {smtpHasPassword && (
                          <label className="inline-flex items-center gap-1.5 text-[11px] text-slate-500 mt-1">
                            <input
                              type="checkbox"
                              checked={smtpClearPassword}
                              onChange={(e) => {
                                setSmtpClearPassword(e.target.checked)
                                setSmtpPassword('')
                              }}
                            />
                            <span>清除已有 SMTP 密码</span>
                          </label>
                        )}
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 mb-1">发件人名称</label>
                        <input
                          value={smtpFromName}
                          onChange={(e) => setSmtpFromName(e.target.value)}
                          placeholder="SafeKeep"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 mb-1">发件人邮箱</label>
                        <input
                          value={smtpFromEmail}
                          onChange={(e) => setSmtpFromEmail(e.target.value)}
                          placeholder="alerts@example.com"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                      <div>
                        <label className="block text-xs text-slate-600 mb-1">告警收件人邮箱（多个用逗号分隔）</label>
                        <input
                          value={smtpToEmails}
                          onChange={(e) => setSmtpToEmails(e.target.value)}
                          placeholder="ops@example.com, admin@example.com"
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                      </div>
                    </div>
                    <div className="flex justify-end gap-2 pt-2 border-t">
                      <button
                        type="button"
                        onClick={testSMTP}
                        disabled={smtpTesting}
                        className="px-4 py-2 border border-slate-300 hover:bg-slate-50 rounded-lg text-sm"
                      >
                        {smtpTesting ? '发送中...' : '发送测试邮件'}
                      </button>
                      <button type="submit" className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg font-medium">
                        保存 SMTP 配置
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
                        <div key={a.id} className="flex items-center justify-between p-3 bg-slate-50 rounded-lg text-sm border border-slate-100">
                          <div>
                            <div className="font-medium text-slate-900">{a.username}</div>
                            <div className="text-xs text-slate-400">最近登录: {formatTime(a.last_login_at)}</div>
                          </div>
                          <div className="flex items-center gap-2">
                            <StatusBadge value={a.is_active ? 'enabled' : 'disabled'} label={a.is_active ? '启用' : '禁用'} />
                            <button
                              onClick={async () => {
                                const pwd = prompt(`为管理员 "${a.username}" 输入新密码（至少 8 位）：`)
                                if (!pwd) return
                                try {
                                  await api.updateAdmin(a.id, { password: pwd })
                                  setMsg(`已重置管理员 "${a.username}" 密码`)
                                } catch (e: any) {
                                  setErr(e.message)
                                }
                              }}
                              className="text-xs text-slate-600 hover:underline"
                            >
                              重置密码
                            </button>
                            <button
                              onClick={async () => {
                                try {
                                  await api.updateAdmin(a.id, { is_active: !a.is_active })
                                  await load()
                                } catch (e: any) {
                                  setErr(e.message)
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
                          placeholder="用户名"
                          value={newAdminUser}
                          onChange={(e) => setNewAdminUser(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <input
                          type="password"
                          required
                          minLength={8}
                          placeholder="密码（>=8位）"
                          value={newAdminPass}
                          onChange={(e) => setNewAdminPass(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <button type="submit" className="px-4 py-1.5 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-xs font-medium">
                          添加管理员
                        </button>
                      </form>
                      <form onSubmit={handleChangeOwnPassword} className="space-y-2.5 text-sm border rounded-xl p-4">
                        <div className="font-medium text-slate-800">修改当前登录密码</div>
                        <input
                          type="password"
                          required
                          placeholder="当前密码"
                          value={oldPwd}
                          onChange={(e) => setOldPwd(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <input
                          type="password"
                          required
                          minLength={8}
                          placeholder="新密码（>=8位）"
                          value={newPwd}
                          onChange={(e) => setNewPwd(e.target.value)}
                          className="w-full px-3 py-1.5 border rounded-lg"
                        />
                        <button type="submit" className="px-4 py-1.5 bg-slate-900 hover:bg-slate-800 text-white rounded-lg text-xs font-medium">
                          更新密码
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
    </div>
  )
}
