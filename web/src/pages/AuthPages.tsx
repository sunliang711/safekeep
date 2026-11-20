import React, { useState } from 'react'
import { ArrowRight, Cloud, Eye, EyeOff, Loader2, LockKeyhole, ShieldCheck } from 'lucide-react'
import { api } from '../api'
import type { Admin } from '../types'

interface Props {
  mode: 'setup' | 'login'
  version?: string
  onSuccess: (admin: Admin) => void
}

export const AuthPage: React.FC<Props> = ({ mode, version = 'dev', onSuccess }) => {
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
      setError('两次输入的密码不一致')
      return
    }
    setLoading(true)
    try {
      const res =
        mode === 'setup' ? await api.setupInit(username, password) : await api.login(username, password)
      onSuccess(res.admin)
    } catch (err: any) {
      setError(err.message === 'invalid username or password' ? '用户名或密码不正确，请重试' : err.message || '操作失败，请稍后重试')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen grid lg:grid-cols-2 bg-[#f7f9fc]">
      <div className="hidden lg:flex flex-col justify-between relative overflow-hidden bg-[#111b34] text-white p-12 xl:p-16">
        <div className="absolute -right-32 top-1/4 w-96 h-96 bg-indigo-500/10 rounded-full blur-3xl" />
        <div className="flex items-center gap-3 font-semibold text-2xl"><ShieldCheck className="w-8 h-8 text-indigo-400" />SafeKeep<span className="text-indigo-400">.</span></div>
        <div className="relative py-16 max-w-lg">
          <div className="w-16 h-16 bg-indigo-400/10 border border-indigo-400/20 rounded-2xl grid place-items-center mb-8"><Cloud className="w-8 h-8 text-indigo-300" /></div>
          <div className="text-[40px] xl:text-5xl font-semibold leading-tight tracking-tight">让每一份数据，<br /><span className="text-indigo-300">都有安全的归处。</span></div>
          <p className="text-slate-400 text-sm leading-7 mt-6">连接你的云端存储，统一管理文件备份、传输策略与恢复。一处掌控，安心守护。</p>
          <div className="flex gap-5 text-xs text-slate-300 mt-8"><span>多云存储</span><span className="text-slate-600">/</span><span>加密迁移</span><span className="text-slate-600">/</span><span>审计追踪</span></div>
        </div>
        <div className="text-xs text-slate-500">SafeKeep · 多云文件备份网关 · <span className="font-mono">{versionLabel}</span></div>
      </div>
      <div className="flex flex-col items-center justify-center p-6 sm:p-12">
      <div className="w-full max-w-md bg-white rounded-2xl border border-slate-200/80 shadow-xl shadow-slate-200/30 p-6 sm:p-9">
        <div className="flex items-center gap-3 mb-6">
          <div className="w-10 h-10 rounded-xl bg-indigo-600 text-white flex items-center justify-center">
            <ShieldCheck className="w-6 h-6" />
          </div>
          <div>
            <h1 className="text-xl font-bold text-slate-900">
              {mode === 'setup' ? '创建管理账号' : '欢迎回来'}
            </h1>
            <p className="text-xs text-slate-500">
              {mode === 'setup'
                ? '设置首个管理员账号，开始管理你的云端备份'
                : '多云文件备份服务管理中心'}
            </p>
          </div>
        </div>

        {error && (
          <div role="alert" className="mb-4 p-3 rounded-lg bg-rose-50 border border-rose-200 text-rose-700 text-sm">{error}</div>
        )}

        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label htmlFor="auth-username" className="block text-sm font-medium text-slate-700 mb-1">用户名</label>
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
              className="w-full px-3 py-2 border border-slate-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
            />
          </div>
          <div>
            <label htmlFor="auth-password" className="block text-sm font-medium text-slate-700 mb-1">密码{mode === 'setup' ? '（至少 8 位）' : ''}</label>
            <div className="relative">
            <input
              id="auth-password"
              autoComplete={mode === 'setup' ? 'new-password' : 'current-password'}
              type={showPassword ? 'text' : 'password'}
              required
              minLength={8}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="请输入密码"
              className="w-full pl-3 pr-11 py-2 border border-slate-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
            />
            <button type="button" aria-label={showPassword ? '隐藏密码' : '显示密码'} onClick={() => setShowPassword(!showPassword)} className="absolute right-2 top-1/2 -translate-y-1/2 p-1.5 text-slate-400 hover:text-indigo-600">{showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}</button>
            </div>
          </div>
          {mode === 'setup' && (
            <div>
              <label htmlFor="auth-confirm" className="block text-sm font-medium text-slate-700 mb-1">确认密码</label>
              <input
                id="auth-confirm"
                autoComplete="new-password"
                type={showPassword ? 'text' : 'password'}
                required
                minLength={8}
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                placeholder="••••••••"
                className="w-full px-3 py-2 border border-slate-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
              />
            </div>
          )}
          <button
            type="submit"
            disabled={loading}
            className="w-full flex justify-center items-center gap-2 py-2.5 bg-indigo-600 hover:bg-indigo-700 disabled:opacity-50 text-white rounded-lg text-sm font-medium transition"
          >
            {loading && <Loader2 className="w-4 h-4 animate-spin" />}
            {loading ? '正在验证…' : mode === 'setup' ? '创建账号并开始使用' : '登录控制台'}
            {!loading && <ArrowRight className="w-4 h-4" />}
          </button>
        </form>
      </div>
      <div className="flex items-center gap-1.5 text-xs text-slate-400 mt-6"><LockKeyhole className="w-3.5 h-3.5" />凭证加密存储 · 仅限授权管理员访问</div>
      </div>
    </div>
  )
}
