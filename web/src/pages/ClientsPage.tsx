import { useReadOnly } from '../contexts/PermissionContext'
import React, { useEffect, useState } from 'react'
import { Check, ChevronDown, ChevronRight, Copy, Eye, KeyRound, Plus, RefreshCw, Terminal, Trash2 } from 'lucide-react'
import { EmptyState, LoadingState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { useConfirm } from '../components/ConfirmDialog'
import { SecretField } from '../components/SecretField'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import { useI18n } from '../contexts/I18nContext'
import { copyText } from '../utils/clipboard'
import type { Client, Upstream } from '../types'

interface ClientForm {
  id?: string
  name: string
  description: string
  enabled: boolean
  default_mode: 'sync' | 'async'
  path_prefix: string
  max_file_size_mb: number
  allowed_mime_types: string
  rate_limit_rpm: number
  webhook_url: string
  webhook_secret: string
  expires_at: string
  upstreams: { upstream_id: string; enabled: boolean; is_default: boolean; priority: number }[]
}

export const ClientsPage: React.FC = () => {
  const readOnly = useReadOnly()
  const askConfirm = useConfirm()
  const toast = useToast()
  const { t, lang } = useI18n()
  const [clients, setClients] = useState<Client[]>([])
  const [upstreams, setUpstreams] = useState<Upstream[]>([])
  const [loading, setLoading] = useState(true)
  const [modalOpen, setModalOpen] = useState(false)
  const [form, setForm] = useState<ClientForm | null>(null)
  const [initialFormSnap, setInitialFormSnap] = useState('')
  const [saving, setSaving] = useState(false)
  const [revealedKey, setRevealedKey] = useState<{ key: string; clientName: string; isNew?: boolean } | null>(null)
  const [revealingKeyId, setRevealingKeyId] = useState<string | null>(null)
  const [editingHasAPIKey, setEditingHasAPIKey] = useState(false)
  const [editingHasWebhookSecret, setEditingHasWebhookSecret] = useState(false)
  const [guideOpen, setGuideOpen] = useState(false)
  const [guideTab, setGuideTab] = useState<'sync-upload' | 'async-upload' | 'raw-stream' | 'query-download'>('sync-upload')
  const [copiedId, setCopiedId] = useState<string | null>(null)
  const [error, setError] = useState('')

  const load = async () => {
    setLoading(true)
    try {
      const [cRes, uRes] = await Promise.all([api.listClients(), api.listUpstreams()])
      setClients(cRes.items || [])
      setUpstreams(uRes.items || [])
    } catch (err: any) {
      toast.error(err.message || t('clients.loadError'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const openNew = () => {
    setError('')
    const nextForm: ClientForm = {
      name: '',
      description: '',
      enabled: true,
      default_mode: 'async',
      path_prefix: '',
      max_file_size_mb: 0,
      allowed_mime_types: '',
      rate_limit_rpm: 0,
      webhook_url: '',
      webhook_secret: '',
      expires_at: '',
      upstreams: upstreams.map((u, idx) => ({
        upstream_id: u.id,
        enabled: true,
        is_default: true,
        priority: (idx + 1) * 10,
      })),
    }
    setForm(nextForm)
    setInitialFormSnap(JSON.stringify(nextForm))
    setEditingHasAPIKey(false)
    setEditingHasWebhookSecret(false)
    setModalOpen(true)
  }

  const openEdit = (c: Client) => {
    setError('')
    const boundMap = new Map(c.upstreams.map((u) => [u.upstream_id, u]))
    const nextForm: ClientForm = {
      id: c.id,
      name: c.name,
      description: c.description,
      enabled: c.enabled,
      default_mode: c.default_mode,
      path_prefix: c.path_prefix,
      max_file_size_mb: c.max_file_size_bytes > 0 ? Math.round(c.max_file_size_bytes / (1024 * 1024)) : 0,
      allowed_mime_types: (c.allowed_mime_types || []).join(', '),
      rate_limit_rpm: c.rate_limit_rpm,
      webhook_url: c.webhook_url,
      webhook_secret: '',
      expires_at: c.expires_at ? new Date(c.expires_at).toISOString().slice(0, 16) : '',
      upstreams: upstreams.map((u, idx) => {
        const b = boundMap.get(u.id)
        return {
          upstream_id: u.id,
          enabled: Boolean(b),
          is_default: b ? b.is_default : false,
          priority: b ? b.priority : (idx + 1) * 10,
        }
      }),
    }
    setForm(nextForm)
    setInitialFormSnap(JSON.stringify(nextForm))
    setEditingHasAPIKey(Boolean(c.has_api_key))
    setEditingHasWebhookSecret(Boolean(c.has_webhook_secret))
    setModalOpen(true)
  }

  const closeModal = async () => {
    if (JSON.stringify(form) !== initialFormSnap && !await askConfirm(t('clients.discardConfirm'), { title: t('common.unsavedTitle'), confirmText: t('common.discardChanges') })) return
    setModalOpen(false)
  }

  const clientDirty = form ? !form.id || JSON.stringify(form) !== initialFormSnap : false

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!form || saving || (form.id && !clientDirty)) return
    setError('')
    setSaving(true)
    const selectedUpstreams = form.upstreams
      .filter((u) => u.enabled)
      .map((u) => ({ upstream_id: u.upstream_id, is_default: u.is_default, priority: Number(u.priority) }))

    const allowedMimes = form.allowed_mime_types
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)

    const payload: any = {
      name: form.name,
      description: form.description,
      enabled: form.enabled,
      default_mode: form.default_mode,
      path_prefix: form.path_prefix,
      max_file_size_bytes: form.max_file_size_mb > 0 ? form.max_file_size_mb * 1024 * 1024 : 0,
      allowed_mime_types: allowedMimes,
      rate_limit_rpm: Number(form.rate_limit_rpm),
      webhook_url: form.webhook_url,
      expires_at: form.expires_at ? new Date(form.expires_at).toISOString() : null,
      upstreams: selectedUpstreams,
    }
    if (!form.id || form.webhook_secret !== '') {
      payload.webhook_secret = form.webhook_secret
    }

    try {
      if (form.id) {
        await api.updateClient(form.id, payload)
        toast.success(t('clients.savedToast', { name: form.name }))
      } else {
        const res = await api.createClient(payload)
        setRevealedKey({ key: res.api_key, clientName: form.name, isNew: true })
        toast.success(t('clients.createdToast', { name: form.name }))
      }
      setModalOpen(false)
      await load()
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const handleRevealKey = async (c: Client) => {
    if (revealingKeyId) return
    if (c.has_api_key === false) {
      toast.info(t('clients.legacyKeyInfo', { name: c.name }))
      return
    }
    setRevealingKeyId(c.id)
    try {
      const res = await api.revealClientSecrets(c.id)
      if (!res.api_key) {
        toast.info(t('clients.noKeyInfo', { name: c.name }))
        return
      }
      setRevealedKey({ key: res.api_key, clientName: c.name })
    } catch (err: any) {
      toast.error(err.message || t('clients.revealError'))
    } finally {
      setRevealingKeyId(null)
    }
  }

  const handleRotate = async (c: Client) => {
    if (!await askConfirm(t('clients.rotateConfirm', { name: c.name }), { danger: true })) return
    try {
      const res = await api.rotateClientKey(c.id)
      setRevealedKey({ key: res.api_key, clientName: c.name, isNew: true })
      toast.success(t('clients.rotatedToast', { name: c.name }))
      await load()
    } catch (err: any) {
      toast.error(err.message)
    }
  }

  const handleDelete = async (c: Client) => {
    if (!await askConfirm(t('clients.deleteConfirm', { name: c.name }), { title: t('common.deleteConfirmTitle'), confirmText: t('common.delete'), danger: true })) return
    try {
      await api.deleteClient(c.id)
      toast.success(t('clients.deletedToast', { name: c.name }))
      await load()
    } catch (err: any) {
      toast.error(err.message)
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">{t('clients.title')}</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">{t('clients.subtitle')}</p>
        </div>
        <button disabled={readOnly}
          onClick={openNew}
          className="inline-flex items-center gap-1.5 px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm font-medium"
        >
          <Plus className="w-4 h-4" /> {t('clients.createBtn')}
        </button>
      </div>

      {(() => {
        const baseUrl = typeof window !== 'undefined' ? window.location.origin : 'http://localhost:8333'
        const sampleKey = 'sk_your_api_key'
        const copyCmd = async (id: string, cmd: string) => {
          try {
            await copyText(cmd)
            setCopiedId(id)
            setTimeout(() => setCopiedId((cur) => (cur === id ? null : cur)), 1500)
          } catch (e: any) {
            toast.error(e.message || t('common.copyFailed'))
          }
        }
        const examples = [
          {
            id: 'sync-upload' as const,
            label: t('clients.ex1.label'),
            title: t('clients.ex1.title'),
            desc: t('clients.ex1.desc'),
            cmd: `curl -X POST "${baseUrl}/api/v1/backups?mode=sync&path=daily" \\\n  -H "X-API-Key: ${sampleKey}" \\\n  -F "file=@./backup.tar.gz"`,
          },
          {
            id: 'async-upload' as const,
            label: t('clients.ex2.label'),
            title: t('clients.ex2.title'),
            desc: t('clients.ex2.desc'),
            cmd: `curl -X POST "${baseUrl}/api/v1/backups?mode=async&targets=prod-s3,dropbox" \\\n  -H "X-API-Key: ${sampleKey}" \\\n  -F "file=@./archive.zip" \\\n  -F 'metadata={"env":"prod","source":"cron"}'`,
          },
          {
            id: 'raw-stream' as const,
            label: t('clients.ex3.label'),
            title: t('clients.ex3.title'),
            desc: t('clients.ex3.desc'),
            cmd: `curl -X POST "${baseUrl}/api/v1/backups?mode=sync&path=logs" \\\n  -H "X-API-Key: ${sampleKey}" \\\n  -H "X-File-Name: app.log" \\\n  --data-binary @./app.log`,
          },
          {
            id: 'query-download' as const,
            label: t('clients.ex4.label'),
            title: t('clients.ex4.title'),
            desc: t('clients.ex4.desc'),
            cmd: `${t('clients.ex4.commentList')}\ncurl -H "X-API-Key: ${sampleKey}" "${baseUrl}/api/v1/backups?limit=20"\n\n${t('clients.ex4.commentOne')}\ncurl -H "X-API-Key: ${sampleKey}" "${baseUrl}/api/v1/backups/<task_id>"\n\n${t('clients.ex4.commentDownload')}\ncurl -H "X-API-Key: ${sampleKey}" -o restored.bin "${baseUrl}/api/v1/backups/<task_id>/download"\n\n${t('clients.ex4.commentUpstreams')}\ncurl -H "X-API-Key: ${sampleKey}" "${baseUrl}/api/v1/upstreams"`,
          },
        ]
        const activeExample = examples.find((ex) => ex.id === guideTab) || examples[0]

        return (
          <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 overflow-hidden">
            <button
              type="button"
              onClick={() => setGuideOpen((v) => !v)}
              className="w-full px-4 py-3 flex items-center justify-between text-left hover:bg-slate-50 dark:hover:bg-slate-800/60 transition-colors"
            >
              <div className="flex items-center gap-2 flex-wrap">
                <Terminal className="w-4 h-4 text-indigo-600 dark:text-indigo-400" />
                <span className="text-sm font-semibold text-slate-800 dark:text-slate-100">{t('clients.guideTitle')}</span>
                <span className="text-xs text-slate-400">{t('clients.guideSubtitle')}</span>
              </div>
              {guideOpen ? (
                <ChevronDown className="w-4 h-4 text-slate-400 shrink-0" />
              ) : (
                <ChevronRight className="w-4 h-4 text-slate-400 shrink-0" />
              )}
            </button>

            {guideOpen && (
              <div className="px-4 pb-4 pt-3 border-t border-slate-100 dark:border-slate-800 space-y-3 text-xs">
                <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/60 border border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-300 space-y-1">
                  <div>
                    <span className="font-semibold text-slate-800 dark:text-slate-100">{t('clients.guideAuthLabel')}</span>
                    {t('clients.guideAuthText1')}
                    <code className="px-1 py-0.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded font-mono">X-API-Key: sk_...</code>
                    {t('clients.guideAuthText2')}
                    <code className="px-1 py-0.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded font-mono">Authorization: Bearer sk_...</code>.
                  </div>
                  <div>
                    <span className="font-semibold text-slate-800 dark:text-slate-100">{t('clients.guideParamsLabel')}</span>
                    <code className="px-1 py-0.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded font-mono">mode=sync|async</code>
                    {t('clients.guideParamsMode')}
                    <code className="px-1 py-0.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded font-mono">targets</code>
                    {t('clients.guideParamsTargets')}
                    <code className="px-1 py-0.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded font-mono">path</code>
                    {t('clients.guideParamsPath')}
                    <code className="px-1 py-0.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded font-mono">rename</code> /{' '}
                    <code className="px-1 py-0.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded font-mono">X-Backup-Rename</code>
                    {t('clients.guideParamsRename')}
                    <code className="px-1 py-0.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded font-mono">dedup=false</code>
                    {t('clients.guideParamsDedup')}
                  </div>
                </div>

                <div className="flex flex-wrap gap-1.5 border-b border-slate-200 dark:border-slate-800 pb-2">
                  {examples.map((ex) => {
                    const active = ex.id === activeExample.id
                    return (
                      <button
                        key={ex.id}
                        type="button"
                        onClick={() => setGuideTab(ex.id)}
                        className={`px-3 py-1.5 rounded-lg font-medium transition ${
                          active
                            ? 'bg-indigo-600 text-white shadow-sm'
                            : 'bg-slate-100 dark:bg-slate-800 text-slate-600 dark:text-slate-300 hover:bg-slate-200 dark:hover:bg-slate-700 hover:text-slate-900 dark:hover:text-white'
                        }`}
                      >
                        {ex.label}
                      </button>
                    )
                  })}
                </div>

                <div className="space-y-2">
                  <div className="text-slate-600 dark:text-slate-300">{activeExample.desc}</div>
                  <div className="border border-slate-200 dark:border-slate-700 rounded-lg overflow-hidden bg-slate-900 dark:bg-slate-950 text-slate-100">
                    <div className="flex items-center justify-between px-3 py-2 bg-slate-800 dark:bg-slate-900 border-b border-slate-700">
                      <span className="text-[11px] font-medium text-slate-200">{activeExample.title}</span>
                      <button
                        type="button"
                        onClick={() => copyCmd(activeExample.id, activeExample.cmd)}
                        className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-slate-700 hover:bg-slate-600 text-[11px] text-white shrink-0"
                      >
                        {copiedId === activeExample.id ? (
                          <>
                            <Check className="w-3 h-3 text-emerald-400" />
                            <span>{t('common.copied')}</span>
                          </>
                        ) : (
                          <>
                            <Copy className="w-3 h-3" />
                            <span>{t('common.copy')}</span>
                          </>
                        )}
                      </button>
                    </div>
                    <pre className="p-3 font-mono text-[11px] leading-relaxed overflow-x-auto text-emerald-300">{activeExample.cmd}</pre>
                  </div>
                </div>
              </div>
            )}
          </div>
        )
      })()}

      {loading ? (
        <LoadingState label={t('clients.loading')} />
      ) : (
        <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 overflow-x-auto">
          <table className="clients-table w-full text-sm">
            <thead className="bg-slate-50 dark:bg-slate-950/60 text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-800">
              <tr>
                <th className="text-left px-4 py-3">{t('clients.colName')}</th>
                <th className="text-left px-4 py-3">{t('clients.colKeyPrefix')}</th>
                <th className="text-left px-4 py-3">{t('clients.colModePrefix')}</th>
                <th className="text-left px-4 py-3">{t('clients.colUpstreams')}</th>
                <th className="text-left px-4 py-3">{t('clients.colQuota')}</th>
                <th className="text-left px-4 py-3">{t('clients.colStatus')}</th>
                <th className="text-right px-4 py-3">{t('clients.colActions')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {clients.map((c) => (
                <tr key={c.id}>
                  <td className="px-4 py-3">
                    <div className="font-medium text-slate-900 dark:text-slate-100">{c.name}</div>
                    {c.description && <div className="text-xs text-slate-400">{c.description}</div>}
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-slate-700 dark:text-slate-300">{c.key_prefix}...</td>
                  <td className="px-4 py-3">
                    <StatusBadge value={c.default_mode} />
                    {c.path_prefix && <div className="text-xs text-slate-500 dark:text-slate-400 font-mono mt-1">/{c.path_prefix}</div>}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex flex-wrap gap-1">
                      {c.upstreams.map((u) => (
                        <span
                          key={u.upstream_id}
                          className={`text-xs px-2 py-0.5 rounded border ${
                            u.is_default
                              ? 'bg-indigo-50 dark:bg-indigo-500/15 text-indigo-700 dark:text-indigo-300 border-indigo-200 dark:border-indigo-500/30'
                              : 'bg-slate-50 dark:bg-slate-800 text-slate-600 dark:text-slate-300 border-slate-200 dark:border-slate-700'
                          }`}
                        >
                          {u.upstream_name} {u.is_default ? t('clients.tagDefault') : t('clients.tagOptional')}
                        </span>
                      ))}
                    </div>
                  </td>
                  <td className="px-4 py-3 text-xs text-slate-600 dark:text-slate-300">
                    <div>{t('clients.quotaMaxFile')}: {c.max_file_size_bytes > 0 ? formatBytes(c.max_file_size_bytes) : t('common.globalDefault')}</div>
                    <div>{t('clients.quotaRateLimit')}: {c.rate_limit_rpm > 0 ? `${c.rate_limit_rpm} req/m` : t('common.unlimited')}</div>
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge value={c.enabled ? 'enabled' : 'disabled'} label={c.enabled ? t('common.enable') : t('common.disable')} />
                    <div className="text-[11px] text-slate-400 mt-1">{formatTime(c.last_used_at, lang)}</div>
                  </td>
                  <td className="px-4 py-3 text-right space-x-2 whitespace-nowrap">
                    <button
                      type="button"
                      disabled={readOnly || (revealingKeyId === c.id)}
                      onClick={() => handleRevealKey(c)}
                      className="text-xs text-slate-700 dark:text-slate-300 hover:text-indigo-600 dark:hover:text-indigo-400 hover:underline inline-flex items-center gap-0.5 disabled:opacity-50"
                    >
                      <Eye className="w-3 h-3" /> {revealingKeyId === c.id ? t('clients.readingKey') : t('clients.viewKey')}
                    </button>
                    <button disabled={readOnly} type="button" onClick={() => openEdit(c)} className="text-xs text-indigo-600 dark:text-indigo-400 hover:underline">
                      {t('common.edit')}
                    </button>
                    <button disabled={readOnly} type="button" onClick={() => handleRotate(c)} className="text-xs text-amber-600 dark:text-amber-400 hover:underline inline-flex items-center gap-0.5">
                      <RefreshCw className="w-3 h-3" /> {t('clients.rotateKey')}
                    </button>
                    <button disabled={readOnly} type="button" aria-label={t('clients.deleteAria', { name: c.name })} onClick={() => handleDelete(c)} className="text-xs text-rose-600 dark:text-rose-400 hover:underline inline-flex items-center">
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </td>
                </tr>
              ))}
              {clients.length === 0 && (
                <tr>
                  <td colSpan={7} className="text-center">
                    <EmptyState
                      title={t('clients.emptyTitle')}
                      description={t('clients.emptyDesc')}
                      action={<button disabled={readOnly} className="primary-button" onClick={openNew}><Plus className="w-4 h-4" />{t('clients.emptyAction')}</button>}
                    />
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {modalOpen && form && (
        <Modal title={form.id ? t('clients.modalEditTitle') : t('clients.modalCreateTitle')} onClose={closeModal} busy={saving}>
          <div className="bg-white dark:bg-slate-900 rounded-2xl max-w-2xl w-full max-h-[90vh] overflow-y-auto p-6">
            <h2 className="text-lg font-bold text-slate-900 dark:text-slate-100 mb-4">
              {form.id ? t('clients.modalEditTitle') : t('clients.modalCreateTitle')}
            </h2>
            {error && (
              <div className="mb-3 p-2.5 bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 text-xs rounded-lg">
                {error}
              </div>
            )}
            <form onSubmit={handleSave} className="space-y-4 text-sm">
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label htmlFor="clients-field-1" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldName')}</label>
                  <input id="clients-field-1" required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-2" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldMode')}</label>
                  <select id="clients-field-2" value={form.default_mode} onChange={(e) => setForm({ ...form, default_mode: e.target.value as any })} className="w-full px-3 py-1.5 border rounded-lg bg-white dark:bg-slate-900">
                    <option value="async">{t('clients.modeAsync')}</option>
                    <option value="sync">{t('clients.modeSync')}</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="clients-field-3" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldPathPrefix')}</label>
                  <input id="clients-field-3" value={form.path_prefix} onChange={(e) => setForm({ ...form, path_prefix: e.target.value })} placeholder="tenant-acme" className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-4" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldDesc')}</label>
                  <input id="clients-field-4" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-5" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldMaxFileMb')}</label>
                  <input id="clients-field-5" type="number" min={0} value={form.max_file_size_mb} onChange={(e) => setForm({ ...form, max_file_size_mb: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-6" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldRateLimit')}</label>
                  <input id="clients-field-6" type="number" min={0} value={form.rate_limit_rpm} onChange={(e) => setForm({ ...form, rate_limit_rpm: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-7" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldWebhookUrl')}</label>
                  <input id="clients-field-7" value={form.webhook_url} onChange={(e) => setForm({ ...form, webhook_url: e.target.value })} placeholder="https://example.com/hook" className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-8" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                    {t('clients.fieldWebhookSecret')}{form.id ? t('upstreams.secretMaskHint') : ''}
                  </label>
                  <SecretField
                    id="clients-field-8"
                    hasSaved={Boolean(form.id && editingHasWebhookSecret)}
                    value={form.webhook_secret}
                    onChange={(v) => setForm({ ...form, webhook_secret: v })}
                    onReveal={form.id ? async () => (await api.revealClientSecrets(form.id!)).webhook_secret || '' : undefined}
                  />
                </div>
                <div>
                  <label htmlFor="clients-field-9" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldAllowedMimes')}</label>
                  <input id="clients-field-9" value={form.allowed_mime_types} onChange={(e) => setForm({ ...form, allowed_mime_types: e.target.value })} placeholder="image/*,application/pdf" className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
                </div>
                <div>
                  <label htmlFor="clients-field-10" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('clients.fieldExpiresAt')}</label>
                  <input id="clients-field-10" type="datetime-local" value={form.expires_at} onChange={(e) => setForm({ ...form, expires_at: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                {form.id && (
                  <div className="col-span-2">
                    <label htmlFor="clients-field-api-key" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                      {t('clients.fieldApiKeyLabel')}
                    </label>
                    {editingHasAPIKey ? (
                      <SecretField
                        id="clients-field-api-key"
                        readOnly
                        hasSaved
                        value=""
                        onChange={() => {}}
                        onReveal={async () => (await api.revealClientSecrets(form.id!)).api_key || ''}
                      />
                    ) : (
                      <div className="px-3 py-1.5 rounded-lg border border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-800 text-xs text-slate-500 dark:text-slate-400">
                        {t('clients.legacyKeyBox')}
                      </div>
                    )}
                  </div>
                )}
              </div>

              <label className="inline-flex items-center gap-2 text-slate-700 dark:text-slate-200">
                <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
                <span>{t('clients.fieldEnabled')}</span>
              </label>

              <div className="border-t border-slate-200 dark:border-slate-800 pt-3">
                <div className="font-semibold text-slate-800 dark:text-slate-100 mb-2">{t('clients.bindUpstreamsTitle')}</div>
                <div className="space-y-2">
                  {form.upstreams.length === 0 && (
                    <div className="p-3 rounded-xl bg-amber-50 dark:bg-amber-500/10 border border-amber-200 dark:border-amber-500/30 text-amber-800 dark:text-amber-200 text-xs leading-6">
                      {t('clients.noUpstreamsNotice')}
                    </div>
                  )}
                  {form.upstreams.map((uCfg, idx) => {
                    const meta = upstreams.find((x) => x.id === uCfg.upstream_id)
                    if (!meta) return null
                    return (
                      <div key={meta.id} className="flex items-center justify-between p-2.5 rounded-lg bg-slate-50 dark:bg-slate-800/60 border border-slate-200 dark:border-slate-700 text-xs">
                        <label className="flex items-center gap-2 font-medium text-slate-800 dark:text-slate-100">
                          <input
                            type="checkbox"
                            checked={uCfg.enabled}
                            onChange={(e) => {
                              const next = [...form.upstreams]
                              next[idx] = { ...uCfg, enabled: e.target.checked }
                              setForm({ ...form, upstreams: next })
                            }}
                          />
                          <span>{meta.name}</span>
                          <span className="text-slate-400">({meta.type.toUpperCase()})</span>
                        </label>
                        {uCfg.enabled && (
                          <div className="flex items-center gap-4 text-slate-700 dark:text-slate-300">
                            <label className="flex items-center gap-1">
                              <input
                                type="checkbox"
                                checked={uCfg.is_default}
                                onChange={(e) => {
                                  const next = [...form.upstreams]
                                  next[idx] = { ...uCfg, is_default: e.target.checked }
                                  setForm({ ...form, upstreams: next })
                                }}
                              />
                              <span>{t('clients.joinDefaultGroup')}</span>
                            </label>
                            <label className="flex items-center gap-1">
                              <span>{t('clients.priority')}</span>
                              <input
                                type="number"
                                className="w-16 px-2 py-0.5 border rounded bg-white dark:bg-slate-900"
                                value={uCfg.priority}
                                onChange={(e) => {
                                  const next = [...form.upstreams]
                                  next[idx] = { ...uCfg, priority: Number(e.target.value) }
                                  setForm({ ...form, upstreams: next })
                                }}
                              />
                            </label>
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              </div>

              <div className="flex justify-end gap-2 pt-3 border-t border-slate-200 dark:border-slate-800">
                <button type="button" disabled={saving} onClick={closeModal} className="px-4 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50">
                  {t('common.cancel')}
                </button>
                <button
                  type="submit"
                  disabled={saving || (Boolean(form.id) && !clientDirty)}
                  className="px-4 py-2 rounded-lg bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white font-medium"
                >
                  {saving ? t('common.saving') : form.id && !clientDirty ? t('common.noChanges') : t('common.save')}
                </button>
              </div>
            </form>
          </div>
        </Modal>
      )}

      {revealedKey && (
        <Modal title={t('clients.revealModalTitle')} onClose={() => setRevealedKey(null)} size="md">
          <div className="bg-white dark:bg-slate-900 rounded-2xl w-full p-6 space-y-4">
            <div className="flex items-center gap-2 text-indigo-700 dark:text-indigo-400 font-semibold pr-8">
              <KeyRound className="w-5 h-5" />
              <span>
                {revealedKey.isNew
                  ? t('clients.revealNewTitle', { name: revealedKey.clientName })
                  : t('clients.revealExistingTitle', { name: revealedKey.clientName })}
              </span>
            </div>
            <p className="text-xs text-slate-500 dark:text-slate-400">{t('clients.revealDesc')}</p>
            <div className="flex items-center gap-2 bg-slate-900 dark:bg-slate-950 border border-slate-800 text-emerald-400 p-3 rounded-lg font-mono text-xs break-all">
              <span className="flex-1 min-w-0 select-all">{revealedKey.key}</span>
              <button
                type="button"
                onClick={async () => {
                  try {
                    await copyText(revealedKey.key)
                    toast.success(t('clients.copiedApiKey'))
                  } catch (e: any) {
                    toast.error(e.message || t('common.copyFailed'))
                  }
                }}
                className="p-1.5 bg-slate-800 hover:bg-slate-700 rounded text-white shrink-0"
                title={t('common.copy')}
                aria-label={t('clients.copyApiKeyAria')}
              >
                <Copy className="w-4 h-4" />
              </button>
            </div>
            <div className="flex justify-end">
              <button type="button" onClick={() => setRevealedKey(null)} className="px-4 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm">
                {t('common.close')}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
