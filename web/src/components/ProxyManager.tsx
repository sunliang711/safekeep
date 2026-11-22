import React, { useState } from 'react'
import { Loader2, Plus, Trash2, Wifi } from 'lucide-react'
import { api } from '../api'
import type { NetworkProxy } from '../types'
import { useI18n } from '../contexts/I18nContext'
import { useToast } from './Toast'
import { Modal } from './Modal'
import { useConfirm } from './ConfirmDialog'
import { SecretField } from './SecretField'

interface ProxyForm {
  id?: string
  name: string
  type: 'http' | 'https' | 'socks5'
  host: string
  port: number
  username: string
  password: string
  clearPassword: boolean
  hasPassword: boolean
}

export function ProxyManager({ proxies, onChanged }: { proxies: NetworkProxy[]; onChanged: () => Promise<void> }) {
  const askConfirm = useConfirm()
  const { t } = useI18n()
  const toast = useToast()
  const [form, setForm] = useState<ProxyForm | null>(null)
  const [initial, setInitial] = useState('')
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)
  const [error, setError] = useState('')
  const [targetURL, setTargetURL] = useState('')
  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null)

  const open = (p?: NetworkProxy) => {
    const next: ProxyForm = {
      id: p?.id, name: p?.name || '', type: p?.type || 'socks5', host: p?.host || '',
      port: p?.port || 1080, username: p?.username || '', password: '',
      clearPassword: false, hasPassword: p?.has_password || false,
    }
    setForm(next)
    setInitial(JSON.stringify(next))
    setError('')
    setTargetURL('')
    setTestResult(null)
  }
  const close = async () => {
    if (JSON.stringify(form) !== initial && !await askConfirm(t('upstreams.discardConfirm'), { title: t('common.unsavedTitle'), confirmText: t('common.discardChanges') })) return
    setForm(null)
  }
  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!form || saving) return
    setSaving(true)
    setError('')
    const payload = {
      name: form.name, type: form.type, host: form.host, port: form.port, username: form.username,
      password: form.clearPassword ? '' : form.password || undefined,
    }
    try {
      if (form.id) await api.updateProxy(form.id, payload)
      else await api.createProxy(payload)
      setForm(null)
      toast.success(t('proxies.saved'))
      await onChanged()
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }
  const remove = async (p: NetworkProxy) => {
    if (!await askConfirm(t('proxies.deleteConfirm', { name: p.name }), { title: t('common.deleteConfirmTitle'), confirmText: t('common.delete'), danger: true })) return
    try {
      await api.deleteProxy(p.id)
      toast.success(t('proxies.deleted'))
      await onChanged()
    } catch (err: any) {
      toast.error(err.message)
    }
  }
  const test = async () => {
    if (!form || testing) return
    setTesting(true)
    setTestResult(null)
    try {
      const result = await api.testUpstreamProxy({
        proxy_id: form.id, proxy_type: form.type, proxy_host: form.host, proxy_port: form.port,
        proxy_username: form.username, proxy_password: form.password,
        clear_proxy_password: form.clearPassword, target_url: targetURL.trim() || undefined,
      })
      const text = result.status === 'ok'
        ? t('upstreams.proxyTestOk', { status: result.http_status ?? '', latency: result.latency_ms ?? 0, target: result.target_url || '' })
        : t('upstreams.proxyTestFail', { error: result.error || t('common.unknownError') })
      setTestResult({ ok: result.status === 'ok', text })
    } catch (err: any) {
      setTestResult({ ok: false, text: err.message })
    } finally {
      setTesting(false)
    }
  }

  return (
    <section className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5 space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold text-slate-900 dark:text-slate-100">{t('proxies.title')}</h2>
          <p className="text-xs text-slate-500 mt-1">{t('proxies.description')}</p>
        </div>
        <button onClick={() => open()} className="inline-flex items-center gap-1.5 px-3 py-1.5 border rounded-lg text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-40"><Plus className="w-4 h-4" />{t('proxies.add')}</button>
      </div>
      {proxies.length === 0 ? <p className="text-sm text-slate-400">{t('proxies.empty')}</p> : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          {proxies.map((p) => (
            <div key={p.id} className="min-w-0 rounded-lg border border-slate-200 dark:border-slate-700 p-3 space-y-2">
              <div className="font-medium text-sm break-all">{p.name}</div>
              <div className="font-mono text-xs text-slate-500 break-all">{p.type.toUpperCase()} · {p.host}:{p.port}</div>
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
                <span className="text-slate-500">{t('proxies.usage', { count: p.upstream_count })}</span>
                <div className="flex items-center gap-3">
                  <button onClick={() => open(p)} className="text-indigo-600 dark:text-indigo-400">{t('common.edit')}</button>
                  <button onClick={() => remove(p)} disabled={p.upstream_count > 0} title={p.upstream_count > 0 ? t('proxies.inUse') : undefined} className="inline-flex items-center gap-1 text-rose-600 disabled:opacity-40 disabled:cursor-not-allowed"><Trash2 className="w-3.5 h-3.5" />{t('common.delete')}</button>
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
      {form && (
        <Modal title={form.id ? t('proxies.edit') : t('proxies.add')} onClose={close} busy={saving || testing}>
          <form onSubmit={save} className="p-6 space-y-4">
            <h2 className="text-lg font-semibold">{form.id ? t('proxies.edit') : t('proxies.add')}</h2>
            {form.id && <p className="text-xs text-slate-500">{t('proxies.editHint')}</p>}
            {error && <div role="alert" className="text-sm text-rose-600">{error}</div>}
            <fieldset disabled={saving || testing} className="space-y-4">
              <div>
                <label htmlFor="proxy-name" className="block text-xs mb-1">{t('proxies.name')} *</label>
                <input id="proxy-name" required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                <div>
                  <label htmlFor="proxy-type" className="block text-xs mb-1">{t('upstreams.proxyProtocol')}</label>
                  <select id="proxy-type" value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value as ProxyForm['type'] })} className="w-full px-3 py-1.5 border rounded-lg bg-white dark:bg-slate-900">
                    <option value="socks5">SOCKS5</option><option value="http">HTTP</option><option value="https">HTTPS</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="proxy-host" className="block text-xs mb-1">{t('upstreams.proxyHost')} *</label>
                  <input id="proxy-host" required value={form.host} onChange={(e) => setForm({ ...form, host: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="proxy-port" className="block text-xs mb-1">{t('upstreams.proxyPort')} *</label>
                  <input id="proxy-port" required type="number" min={1} max={65535} value={form.port} onChange={(e) => setForm({ ...form, port: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
              </div>
              <div>
                <label htmlFor="proxy-user" className="block text-xs mb-1">{t('upstreams.proxyUser')}</label>
                <input id="proxy-user" value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" autoComplete="off" />
              </div>
              <div>
                <label htmlFor="proxy-password" className="block text-xs mb-1">{t('upstreams.proxyPass', { hint: form.id ? t('upstreams.proxyPassSavedHint') : t('upstreams.proxyPassOptionalHint') })}</label>
                <SecretField id="proxy-password" value={form.password} disabled={form.clearPassword} hasSaved={form.hasPassword && !form.clearPassword} onChange={(value) => setForm({ ...form, password: value })} onReveal={form.id ? async () => (await api.revealProxySecrets(form.id!)).password : undefined} />
                {form.id && <label className="inline-flex items-center gap-2 text-xs mt-2"><input type="checkbox" checked={form.clearPassword} onChange={(e) => setForm({ ...form, clearPassword: e.target.checked, password: '' })} />{t('upstreams.clearProxyPass')}</label>}
              </div>
              <div className="border-t border-slate-200 dark:border-slate-700 pt-3 space-y-2">
                <label htmlFor="proxy-target" className="block text-xs">{t('upstreams.proxyTargetLabel', { url: 'https://api.dropboxapi.com' })}</label>
                <div className="flex flex-wrap gap-2">
                  <input id="proxy-target" value={targetURL} onChange={(e) => setTargetURL(e.target.value)} placeholder="https://api.dropboxapi.com" className="flex-1 min-w-0 px-3 py-1.5 border rounded-lg text-xs" />
                  <button type="button" onClick={test} disabled={!form.host.trim() || !form.port} className="inline-flex items-center gap-1.5 px-3 py-1.5 border rounded-lg text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-40">{testing ? <Loader2 className="w-4 h-4 animate-spin" /> : <Wifi className="w-4 h-4" />}{t('upstreams.testProxyBtn')}</button>
                </div>
                {testResult && <p role="status" className={`text-xs break-all ${testResult.ok ? 'text-emerald-600' : 'text-rose-600'}`}>{testResult.text}</p>}
              </div>
            </fieldset>
            <div className="flex justify-end gap-2 border-t border-slate-200 dark:border-slate-700 pt-3">
              <button type="button" disabled={saving || testing} onClick={close} className="inline-flex items-center gap-1.5 px-3 py-1.5 border rounded-lg text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-40">{t('common.cancel')}</button>
              <button type="submit" disabled={saving || testing || (Boolean(form.id) && JSON.stringify(form) === initial)} className="primary-button">{saving ? t('common.saving') : t('upstreams.saveConfigBtn')}</button>
            </div>
          </form>
        </Modal>
      )}
    </section>
  )
}
