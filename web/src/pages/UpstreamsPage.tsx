import React, { useEffect, useState } from 'react'
import { CheckCircle2, Copy, Link2, Loader2, Plus, Trash2, Upload, Wifi, XCircle } from 'lucide-react'
import { EmptyState, LoadingState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { useConfirm } from '../components/ConfirmDialog'
import { ProxyManager } from '../components/ProxyManager'
import { SecretField } from '../components/SecretField'
import { api } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import { useI18n } from '../contexts/I18nContext'
import { copyText } from '../utils/clipboard'
import type { NetworkProxy, Upstream } from '../types'

interface FormState {
  id?: string
  name: string
  type: 's3' | 'r2' | 'gcs' | 'dropbox'
  enabled: boolean
  // S3 & R2
  account_id: string
  jurisdiction: string
  endpoint: string
  region: string
  bucket: string
  access_key_id: string
  secret_access_key: string
  base_prefix: string
  force_path_style: boolean
  storage_class: string
  // GCS
  service_account_json: string
  // Dropbox
  app_key: string
  app_secret: string
  refresh_token: string
  root_path: string
  // 选择已保存的代理，空字符串表示直连。
  proxy_id: string
}

const emptyForm = (): FormState => ({
  name: '',
  type: 's3',
  enabled: true,
  account_id: '',
  jurisdiction: '',
  endpoint: '',
  region: 'us-east-1',
  bucket: '',
  access_key_id: '',
  secret_access_key: '',
  base_prefix: '',
  force_path_style: false,
  storage_class: '',
  service_account_json: '',
  app_key: '',
  app_secret: '',
  refresh_token: '',
  root_path: '/safekeep',
  proxy_id: '',
})

export const UpstreamsPage: React.FC = () => {
  const askConfirm = useConfirm()
  const toast = useToast()
  const { t } = useI18n()
  const [items, setItems] = useState<Upstream[]>([])
  const [activeTab, setActiveTab] = useState<'upstreams' | 'proxies'>('upstreams')
  const [proxies, setProxies] = useState<NetworkProxy[]>([])
  const [loading, setLoading] = useState(true)
  const [modalOpen, setModalOpen] = useState(false)
  const [form, setForm] = useState<FormState>(emptyForm())
  const [initialFormSnap, setInitialFormSnap] = useState('')
  const [saving, setSaving] = useState(false)
  const [modalErr, setModalErr] = useState('')
  const [testingId, setTestingId] = useState<string | null>(null)
  const [testingProxy, setTestingProxy] = useState(false)
  const [proxyTargetURL, setProxyTargetURL] = useState('')
  const [proxyTestResult, setProxyTestResult] = useState<{ ok: boolean; text: string } | null>(null)
  const [savedSecretFlags, setSavedSecretFlags] = useState({
    secretKey: false,
    serviceAccountJSON: false,
    appSecret: false,
    refreshToken: false,
  })

  const load = async () => {
    setLoading(true)
    try {
      const [res, proxyRes] = await Promise.all([api.listUpstreams(), api.listProxies()])
      setItems(res.items || [])
      setProxies(proxyRes.items || [])
    } catch (err: any) {
      toast.error(err.message || t('upstreams.loadError'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
    const params = new URLSearchParams(window.location.search)
    if (params.get('oauth') === 'success') {
      toast.success(t('upstreams.oauthSuccess'))
      window.history.replaceState({}, '', '/upstreams')
    } else if (params.get('oauth') === 'error') {
      toast.error(t('upstreams.oauthError', { reason: params.get('reason') || t('common.unknownError') }))
      window.history.replaceState({}, '', '/upstreams')
    }
  }, [])

  const openEdit = (u: Upstream) => {
    setModalErr('')
    const nextForm: FormState = {
      id: u.id,
      name: u.name,
      type: u.type,
      enabled: u.enabled,
      account_id: u.config.account_id || '',
      jurisdiction: u.config.jurisdiction || '',
      endpoint: u.config.endpoint || '',
      region: u.config.region || (u.type === 'r2' ? 'auto' : 'us-east-1'),
      bucket: u.config.bucket || '',
      access_key_id: u.config.access_key_id || '',
      secret_access_key: '',
      base_prefix: u.config.base_prefix || '',
      force_path_style: Boolean(u.config.force_path_style),
      storage_class: u.config.storage_class || '',
      service_account_json: '',
      app_key: u.config.app_key || '',
      app_secret: '',
      refresh_token: '',
      root_path: u.config.root_path || '',
      proxy_id: u.proxy_id || '',
    }
    setForm(nextForm)
    setInitialFormSnap(JSON.stringify(nextForm))
    setSavedSecretFlags({
      secretKey: Boolean(u.config?.has_secret_key),
      serviceAccountJSON: Boolean(u.config?.has_service_account_json),
      appSecret: Boolean(u.config?.has_app_secret),
      refreshToken: Boolean(u.config?.has_refresh_token),
    })
    setProxyTestResult(null)
    setProxyTargetURL('')
    setModalOpen(true)
  }

  const defaultProxyTestURL = () => {
    if (form.type === 'dropbox') return 'https://api.dropboxapi.com'
    if (form.type === 'gcs') return 'https://storage.googleapis.com'
    if (form.type === 'r2') {
      const ep = form.endpoint.trim()
      if (ep) return /^https?:\/\//i.test(ep) ? ep : `https://${ep}`
      const acct = form.account_id.trim()
      if (acct) {
        const jur = form.jurisdiction.trim().toLowerCase()
        if (jur === 'eu') return `https://${acct}.eu.r2.cloudflarestorage.com`
        if (jur === 'fedramp') return `https://${acct}.fedramp.r2.cloudflarestorage.com`
        return `https://${acct}.r2.cloudflarestorage.com`
      }
      return 'https://r2.cloudflarestorage.com'
    }
    const ep = form.endpoint.trim()
    if (!ep) return 'https://s3.amazonaws.com'
    return /^https?:\/\//i.test(ep) ? ep : `https://${ep}`
  }

  const handleTestProxy = async () => {
    if (!form.proxy_id || testingProxy) return
    setProxyTestResult(null)
    setTestingProxy(true)
    const targetURL = proxyTargetURL.trim() || defaultProxyTestURL()
    try {
      const res = await api.testUpstreamProxy({
        upstream_id: form.id,
        upstream_type: form.type,
        proxy_id: form.proxy_id,
        target_url: targetURL,
      })
      if (res.status === 'ok') {
        const msg = t('upstreams.proxyTestOk', {
          status: res.http_status ?? '',
          latency: res.latency_ms ?? 0,
          target: res.target_url ?? targetURL,
        })
        setProxyTestResult({ ok: true, text: msg })
        toast.success(t('upstreams.proxyTestOkToast'))
      } else {
        const msg = t('upstreams.proxyTestFail', { error: res.error || t('common.unknownError') })
        setProxyTestResult({ ok: false, text: msg })
        toast.error(msg)
      }
    } catch (err: any) {
      const msg = t('upstreams.proxyTestFail', { error: err.message || t('upstreams.proxyTestReqFail') })
      setProxyTestResult({ ok: false, text: msg })
      toast.error(msg)
    } finally {
      setTestingProxy(false)
    }
  }

  const closeModal = async () => {
    if (JSON.stringify(form) !== initialFormSnap && !await askConfirm(t('upstreams.discardConfirm'), { title: t('common.unsavedTitle'), confirmText: t('common.discardChanges') })) return
    setModalOpen(false)
  }

  const upstreamDirty = !form.id || JSON.stringify(form) !== initialFormSnap

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    if (saving || (form.id && !upstreamDirty)) return
    setModalErr('')
    setSaving(true)
    let config: Record<string, any>
    if (form.type === 's3') {
      config = {
        endpoint: form.endpoint,
        region: form.region,
        bucket: form.bucket,
        access_key_id: form.access_key_id,
        secret_access_key: form.secret_access_key,
        base_prefix: form.base_prefix,
        force_path_style: form.force_path_style,
        storage_class: form.storage_class,
      }
    } else if (form.type === 'r2') {
      config = {
        account_id: form.account_id,
        jurisdiction: form.jurisdiction,
        endpoint: form.endpoint,
        region: form.region || 'auto',
        bucket: form.bucket,
        access_key_id: form.access_key_id,
        secret_access_key: form.secret_access_key,
        base_prefix: form.base_prefix,
        force_path_style: form.force_path_style,
        storage_class: form.storage_class,
      }
    } else if (form.type === 'gcs') {
      config = {
        bucket: form.bucket,
        service_account_json: form.service_account_json,
        base_prefix: form.base_prefix,
        storage_class: form.storage_class,
      }
    } else {
      config = {
        app_key: form.app_key,
        app_secret: form.app_secret,
        refresh_token: form.refresh_token,
        root_path: form.root_path,
      }
    }

    const payload: any = {
      name: form.name,
      type: form.type,
      enabled: form.enabled,
      config,
      proxy_id: form.proxy_id,
    }

    try {
      if (form.id) {
        await api.updateUpstream(form.id, payload)
      } else {
        await api.createUpstream(payload)
      }
      setModalOpen(false)
      toast.success(t('upstreams.savedToast'))
      await load()
    } catch (err: any) {
      setModalErr(err.message || t('upstreams.saveError'))
    } finally {
      setSaving(false)
    }
  }

  const handleTest = async (id: string) => {
    setTestingId(id)
    try {
      const res = await api.testUpstream(id)
      if (res.status === 'ok') {
        toast.success(t('upstreams.testOkToast'))
      } else {
        toast.error(t('upstreams.testFailToast', { error: res.error || t('common.unknownError') }))
      }
      await load()
    } catch (err: any) {
      toast.error(err.message)
    } finally {
      setTestingId(null)
    }
  }

  const handleDropboxOAuth = async (id: string) => {
    try {
      const res = await api.startDropboxOAuth(id)
      window.location.href = res.authorize_url
    } catch (err: any) {
      toast.error(err.message)
    }
  }

  const handleDelete = async (u: Upstream) => {
    if (!await askConfirm(t('upstreams.deleteConfirm', { name: u.name }), { title: t('common.deleteConfirmTitle'), confirmText: t('common.delete'), danger: true })) return
    try {
      await api.deleteUpstream(u.id)
      toast.success(t('upstreams.deletedToast', { name: u.name }))
      await load()
    } catch (err: any) {
      toast.error(err.message)
    }
  }

  const openNew = () => {
    setModalErr('')
    const next = emptyForm()
    setForm(next)
    setInitialFormSnap(JSON.stringify(next))
    setSavedSecretFlags({ secretKey: false, serviceAccountJSON: false, appSecret: false, refreshToken: false })
    setProxyTestResult(null)
    setProxyTargetURL('')
    setModalOpen(true)
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900 dark:text-slate-100">{t('upstreams.title')}</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400">{t('upstreams.subtitle')}</p>
        </div>
        {activeTab === 'upstreams' && <button
          onClick={openNew}
          className="inline-flex items-center gap-1.5 px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm font-medium"
        >
          <Plus className="w-4 h-4" /> {t('upstreams.addBtn')}
        </button>}
      </div>

      <div role="tablist" aria-label={t('upstreams.tabsLabel')} className="flex gap-1 border-b border-slate-200 dark:border-slate-800">
        {(['upstreams', 'proxies'] as const).map((tab) => (
          <button
            key={tab}
            id={`tab-${tab}`}
            type="button"
            role="tab"
            aria-selected={activeTab === tab}
            aria-controls={`panel-${tab}`}
            tabIndex={activeTab === tab ? 0 : -1}
            onClick={() => setActiveTab(tab)}
            onKeyDown={(e) => {
              if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) return
              e.preventDefault()
              const next = e.key === 'Home' ? 'upstreams' : e.key === 'End' ? 'proxies' : tab === 'upstreams' ? 'proxies' : 'upstreams'
              setActiveTab(next)
              document.getElementById(`tab-${next}`)?.focus()
            }}
            className={`px-5 py-3 text-sm font-medium border-b-2 transition-colors ${activeTab === tab ? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-800 dark:hover:text-slate-200'}`}
          >
            {tab === 'upstreams' ? t('upstreams.tabStorage') : t('upstreams.tabProxies')}
            <span className="ml-2 text-xs opacity-70">{tab === 'upstreams' ? items.length : proxies.length}</span>
          </button>
        ))}
      </div>

      {activeTab === 'proxies' ? (
        <div role="tabpanel" id="panel-proxies" aria-labelledby="tab-proxies">
          <ProxyManager proxies={proxies} onChanged={load} />
        </div>
      ) : loading ? (
        <div role="tabpanel" id="panel-upstreams" aria-labelledby="tab-upstreams"><LoadingState label={t('upstreams.loading')} /></div>
      ) : (
        <div role="tabpanel" id="panel-upstreams" aria-labelledby="tab-upstreams" className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {items.length === 0 && (
            <div className="md:col-span-2 bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800">
              <EmptyState
                title={t('upstreams.emptyTitle')}
                description={t('upstreams.emptyDesc')}
                action={<button className="primary-button" onClick={openNew}><Plus className="w-4 h-4" />{t('upstreams.addBtn')}</button>}
              />
            </div>
          )}
          {items.map((u) => (
            <div key={u.id} className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5 space-y-3">
              <div className="flex items-start justify-between">
                <div>
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-semibold text-slate-900 dark:text-slate-100">{u.name}</span>
                    <span className="text-xs px-2 py-0.5 rounded bg-slate-100 dark:bg-slate-800 text-slate-700 dark:text-slate-300 font-mono">
                      {u.type === 'gcs' ? 'GCS' : u.type === 'r2' ? 'R2' : u.type.toUpperCase()}
                    </span>
                    <StatusBadge value={u.enabled ? 'enabled' : 'disabled'} label={u.enabled ? t('common.enable') : t('common.disable')} />
                  </div>
                  <div className="text-xs text-slate-400 mt-1 font-mono">ID: {u.id}</div>
                </div>
                <StatusBadge value={u.last_test_status} />
              </div>

              <div className="text-xs text-slate-600 dark:text-slate-300 space-y-1 bg-slate-50 dark:bg-slate-800/60 rounded-lg p-3">
                {u.type === 's3' ? (
                  <>
                    <div>Bucket: <span className="font-mono">{u.config.bucket}</span> ({u.config.region || 'us-east-1'})</div>
                    {u.config.endpoint && <div>Endpoint: <span className="font-mono">{u.config.endpoint}</span></div>}
                    {u.config.base_prefix && <div>{t('upstreams.prefix')}: <span className="font-mono">{u.config.base_prefix}</span></div>}
                  </>
                ) : u.type === 'r2' ? (
                  <>
                    <div>Bucket: <span className="font-mono">{u.config.bucket}</span> ({u.config.region || 'auto'})</div>
                    {u.config.account_id && <div>Account ID: <span className="font-mono">{u.config.account_id}</span>{u.config.jurisdiction ? ` (${u.config.jurisdiction.toUpperCase()})` : ''}</div>}
                    {(u.config.resolved_endpoint || u.config.endpoint) && (
                      <div>Endpoint: <span className="font-mono break-all">{u.config.resolved_endpoint || u.config.endpoint}</span></div>
                    )}
                    {u.config.base_prefix && <div>{t('upstreams.prefix')}: <span className="font-mono">{u.config.base_prefix}</span></div>}
                  </>
                ) : u.type === 'gcs' ? (
                  <>
                    <div>Bucket: <span className="font-mono">{u.config.bucket}</span></div>
                    {u.config.project_id && <div>Project ID: <span className="font-mono">{u.config.project_id}</span></div>}
                    {u.config.client_email && <div>Service Account: <span className="font-mono break-all">{u.config.client_email}</span></div>}
                    {u.config.storage_class && <div>Storage Class: <span className="font-mono">{u.config.storage_class}</span></div>}
                    {u.config.base_prefix && <div>{t('upstreams.prefix')}: <span className="font-mono">{u.config.base_prefix}</span></div>}
                  </>
                ) : (
                  <>
                    <div>
                      {t('upstreams.authStatus')}:{' '}
                      {u.config.authorized ? (
                        <span className="text-emerald-700 dark:text-emerald-400 font-medium">
                          {t('upstreams.authorized', { email: u.config.account_email || t('upstreams.connected') })}
                        </span>
                      ) : (
                        <span className="text-amber-700 dark:text-amber-400">{t('upstreams.unauthorized')}</span>
                      )}
                    </div>
                    <div>{t('upstreams.rootPath')}: <span className="font-mono">{u.config.root_path || '/'}</span></div>
                    {!u.config.authorized ? (
                      (() => {
                        const port = window.location.port || (window.location.protocol === 'https:' ? '443' : '80')
                        const host = window.location.hostname
                        const isLocal = host === 'localhost' || host === '127.0.0.1'
                        const isNonLocalHTTP = window.location.protocol === 'http:' && !isLocal
                        const targetHost = isLocal ? t('upstreams.sshPlaceholderHost') : host
                        const sshCmd = `ssh -N -L ${port}:127.0.0.1:${port} ${t('upstreams.sshPlaceholderUser')}@${targetHost}`
                        const localhostCallback = `http://localhost:${port}/admin/api/v1/oauth/dropbox/callback`
                        const currentCallback = `${window.location.origin}/admin/api/v1/oauth/dropbox/callback`
                        const copyTargetCallback = isNonLocalHTTP ? localhostCallback : currentCallback

                        return (
                          <div className="mt-1.5 p-2.5 rounded-lg bg-amber-50 dark:bg-amber-500/10 border border-amber-200 dark:border-amber-500/30 text-amber-900 dark:text-amber-200 space-y-2">
                            <div className="font-medium">{t('upstreams.dropboxSetupTitle')}</div>
                            <div className="text-[11px] text-amber-800 dark:text-amber-300 leading-relaxed">
                              {t('upstreams.dropboxSetupDesc1')}
                              <b>Redirect URIs</b>
                              {t('upstreams.dropboxSetupDesc2')}
                              <code className="font-mono">https://</code>
                              {t('upstreams.dropboxSetupDesc3')}
                              <code className="font-mono">http://localhost</code>
                              {t('upstreams.dropboxSetupDesc4')}
                              <b>{t('upstreams.dropboxSetupDesc5')}</b>.
                            </div>
                            <div className="flex items-center gap-1.5 flex-wrap">
                              <code className="font-mono bg-white dark:bg-slate-900 px-1.5 py-0.5 rounded border border-amber-200 dark:border-amber-500/30 text-[11px] break-all">
                                {copyTargetCallback}
                              </code>
                              <button
                                type="button"
                                onClick={async () => {
                                  try {
                                    await copyText(copyTargetCallback)
                                    toast.success(t('upstreams.copiedCallbackToast'))
                                  } catch (e: any) {
                                    toast.error(e.message || t('common.copyFailed'))
                                  }
                                }}
                                className="text-amber-800 dark:text-amber-200 hover:text-amber-950 dark:hover:text-white inline-flex items-center gap-0.5 font-medium"
                                title={t('upstreams.copyCallbackTitle')}
                              >
                                <Copy className="w-3 h-3" /> {t('common.copy')}
                              </button>
                            </div>
                            <div className="pt-1.5 border-t border-amber-200/80 dark:border-amber-500/20 text-[11px] text-amber-900 dark:text-amber-200 space-y-1">
                              <div className="font-medium">
                                {isNonLocalHTTP ? t('upstreams.sshTitleNonLocal') : t('upstreams.sshTitleDefault')}
                              </div>
                              <div className="text-amber-800 dark:text-amber-300">
                                {t('upstreams.sshStep1a')}
                                <code className="font-mono">localhost:{port}</code>
                                {t('upstreams.sshStep1b')}
                              </div>
                              <div className="flex items-center gap-1.5 flex-wrap">
                                <code className="font-mono bg-white dark:bg-slate-900 px-1.5 py-0.5 rounded border border-amber-200 dark:border-amber-500/30 text-[11px] break-all">
                                  {sshCmd}
                                </code>
                                <button
                                  type="button"
                                  onClick={async () => {
                                    try {
                                      await copyText(sshCmd)
                                      toast.success(t('upstreams.copiedSshToast'))
                                    } catch (e: any) {
                                      toast.error(e.message || t('common.copyFailed'))
                                    }
                                  }}
                                  className="text-amber-800 dark:text-amber-200 hover:text-amber-950 dark:hover:text-white inline-flex items-center gap-0.5 font-medium"
                                  title={t('upstreams.copySshTitle')}
                                >
                                  <Copy className="w-3 h-3" /> {t('upstreams.copySshBtn')}
                                </button>
                              </div>
                              <div className="text-amber-800 dark:text-amber-300">
                                {t('upstreams.sshStep2a')}
                                <code className="font-mono">http://localhost:{port}</code>
                                {t('upstreams.sshStep2b')}
                              </div>
                            </div>
                          </div>
                        )
                      })()
                    ) : (
                      <div className="flex items-center gap-1.5 flex-wrap">
                        <span>{t('upstreams.oauthCallbackLabel')}</span>
                        <code className="font-mono bg-white dark:bg-slate-900 px-1.5 py-0.5 rounded border border-slate-200 dark:border-slate-700 text-[11px] break-all">
                          {`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`}
                        </code>
                        <button
                          type="button"
                          onClick={async () => {
                            try {
                              await copyText(`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`)
                              toast.success(t('upstreams.copiedRedirectToast'))
                            } catch (e: any) {
                              toast.error(e.message || t('common.copyFailed'))
                            }
                          }}
                          className="text-indigo-600 dark:text-indigo-400 hover:text-indigo-800 dark:hover:text-indigo-300 inline-flex items-center gap-0.5"
                          title={t('upstreams.copyCallbackTitle')}
                        >
                          <Copy className="w-3 h-3" />
                        </button>
                      </div>
                    )}
                  </>
                )}
                <div>
                  {t('upstreams.networkProxy')}:{' '}
                  {u.proxy_enabled ? (
                    <span className="font-mono text-indigo-700 dark:text-indigo-400">
                      {u.proxy_name && `${u.proxy_name} · `}{u.proxy_type.toUpperCase()}://{u.proxy_host}:{u.proxy_port}
                    </span>
                  ) : (
                    t('upstreams.directNoProxy')
                  )}
                </div>
                {u.last_test_error && <div className="text-rose-600 dark:text-rose-400 break-all">{t('upstreams.lastError')}: {u.last_test_error}</div>}
              </div>

              <div className="flex flex-wrap items-center gap-2 pt-1">
                <button
                  onClick={() => openEdit(u)}
                  className="px-3 py-1.5 text-xs bg-slate-100 dark:bg-slate-800 hover:bg-slate-200 dark:hover:bg-slate-700 rounded-lg text-slate-700 dark:text-slate-200 font-medium"
                >
                  {t('upstreams.editConfig')}
                </button>
                <button
                  onClick={() => handleTest(u.id)}
                  disabled={testingId === u.id}
                  className="inline-flex items-center gap-1 px-3 py-1.5 text-xs bg-indigo-50 dark:bg-indigo-500/15 hover:bg-indigo-100 dark:hover:bg-indigo-500/25 text-indigo-700 dark:text-indigo-300 rounded-lg font-medium"
                >
                  <Wifi className="w-3.5 h-3.5" />
                  {testingId === u.id ? t('upstreams.testing') : t('upstreams.testConnection')}
                </button>
                {u.type === 'dropbox' && (
                  <button
                    onClick={() => handleDropboxOAuth(u.id)}
                    className="inline-flex items-center gap-1 px-3 py-1.5 text-xs bg-sky-50 dark:bg-sky-500/15 hover:bg-sky-100 dark:hover:bg-sky-500/25 text-sky-700 dark:text-sky-300 rounded-lg font-medium"
                  >
                    <Link2 className="w-3.5 h-3.5" />
                    {t('upstreams.dropboxOAuthBtn')}
                  </button>
                )}
                <button
                  onClick={() => handleDelete(u)}
                  className="ml-auto p-1.5 text-slate-400 hover:text-rose-600 dark:hover:text-rose-400 rounded-lg"
                  title={t('common.delete')}
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {modalOpen && (
        <Modal title={form.id ? t('upstreams.modalEditTitle') : t('upstreams.modalAddTitle')} onClose={closeModal} busy={saving}>
          <div className="bg-white dark:bg-slate-900 rounded-2xl max-w-2xl w-full max-h-[90vh] overflow-y-auto p-6">
            <h2 className="text-lg font-bold text-slate-900 dark:text-slate-100 mb-4">
              {form.id ? t('upstreams.modalEditTitle') : t('upstreams.modalAddTitle')}
            </h2>
            {modalErr && (
              <div className="mb-3 p-2.5 rounded-lg bg-rose-50 dark:bg-rose-500/15 border border-rose-200 dark:border-rose-500/30 text-rose-700 dark:text-rose-300 text-xs">
                {modalErr}
              </div>
            )}
            <form onSubmit={handleSave} className="space-y-4 text-sm">
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label htmlFor="upstreams-field-1" className="block font-medium text-slate-700 dark:text-slate-300 mb-1">{t('upstreams.fieldName')}</label>
                  <input
                    id="upstreams-field-1"
                    required
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                    className="w-full px-3 py-2 border rounded-lg"
                    placeholder="prod-s3"
                  />
                </div>
                <div>
                  <label htmlFor="upstreams-field-2" className="block font-medium text-slate-700 dark:text-slate-300 mb-1">{t('upstreams.fieldType')}</label>
                  <select
                    id="upstreams-field-2"
                    disabled={Boolean(form.id)}
                    value={form.type}
                    onChange={(e) => {
                      const nextType = e.target.value as FormState['type']
                      setForm((prev) => ({
                        ...prev,
                        type: nextType,
                        region: nextType === 'r2' ? (prev.region === 'us-east-1' || !prev.region ? 'auto' : prev.region) : nextType === 's3' ? (prev.region === 'auto' || !prev.region ? 'us-east-1' : prev.region) : prev.region,
                      }))
                    }}
                    className="w-full px-3 py-2 border rounded-lg bg-white dark:bg-slate-900"
                  >
                    <option value="s3">{t('upstreams.typeS3')}</option>
                    <option value="r2">Cloudflare R2</option>
                    <option value="gcs">Google Cloud Storage (GCS)</option>
                    <option value="dropbox">Dropbox</option>
                  </select>
                </div>
              </div>

              <label className="inline-flex items-center gap-2 text-slate-700 dark:text-slate-200">
                <input
                  type="checkbox"
                  checked={form.enabled}
                  onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
                />
                <span>{t('upstreams.fieldEnabled')}</span>
              </label>

              {form.type === 's3' ? (
                <div className="border-t border-slate-200 dark:border-slate-800 pt-4 space-y-3">
                  <div className="font-semibold text-slate-800 dark:text-slate-100">{t('upstreams.s3Section')}</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label htmlFor="upstreams-field-3" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">Bucket *</label>
                      <input id="upstreams-field-3" required value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-4" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">Region</label>
                      <input id="upstreams-field-4" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-5" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.s3Endpoint')}</label>
                      <input id="upstreams-field-5" value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} placeholder="https://s3.amazonaws.com" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-6" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.basePrefix')}</label>
                      <input id="upstreams-field-6" value={form.base_prefix} onChange={(e) => setForm({ ...form, base_prefix: e.target.value })} placeholder="backups/" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-7" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">Access Key ID *</label>
                      <input id="upstreams-field-7" required value={form.access_key_id} onChange={(e) => setForm({ ...form, access_key_id: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-8" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                        Secret Access Key {form.id ? t('upstreams.secretMaskHint') : '*'}
                      </label>
                      <SecretField
                        id="upstreams-field-8"
                        required={!form.id}
                        hasSaved={Boolean(form.id && savedSecretFlags.secretKey)}
                        value={form.secret_access_key}
                        onChange={(v) => setForm({ ...form, secret_access_key: v })}
                        onReveal={form.id ? async () => (await api.revealUpstreamSecrets(form.id!)).secret_access_key || '' : undefined}
                      />
                    </div>
                    <div className="col-span-2">
                      <label htmlFor="upstreams-field-9" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.s3StorageClass')}</label>
                      <input id="upstreams-field-9" value={form.storage_class} onChange={(e) => setForm({ ...form, storage_class: e.target.value })} placeholder="STANDARD" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                  </div>
                  <label className="inline-flex items-center gap-2 text-xs text-slate-700 dark:text-slate-300">
                    <input type="checkbox" checked={form.force_path_style} onChange={(e) => setForm({ ...form, force_path_style: e.target.checked })} />
                    <span>{t('upstreams.s3ForcePathStyle')}</span>
                  </label>
                </div>
              ) : form.type === 'r2' ? (
                <div className="border-t border-slate-200 dark:border-slate-800 pt-4 space-y-3">
                  <div className="font-semibold text-slate-800 dark:text-slate-100">{t('upstreams.r2Section')}</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label htmlFor="upstreams-r2-account" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                        {t('upstreams.r2AccountId', { req: form.endpoint.trim() ? t('upstreams.optionalParens') : '*' })}
                      </label>
                      <input id="upstreams-r2-account" required={!form.endpoint.trim()} value={form.account_id} onChange={(e) => setForm({ ...form, account_id: e.target.value })} placeholder={t('upstreams.r2AccountPlaceholder')} className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-jurisdiction" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.r2Jurisdiction')}</label>
                      <select id="upstreams-r2-jurisdiction" value={form.jurisdiction} onChange={(e) => setForm({ ...form, jurisdiction: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg bg-white dark:bg-slate-900 text-xs">
                        <option value="">{t('upstreams.r2JurDefault')}</option>
                        <option value="eu">{t('upstreams.r2JurEu')}</option>
                        <option value="fedramp">FedRAMP</option>
                      </select>
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-bucket" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">Bucket *</label>
                      <input id="upstreams-r2-bucket" required value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-region" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.r2Region')}</label>
                      <input id="upstreams-r2-region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="auto" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-endpoint" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.r2Endpoint')}</label>
                      <input id="upstreams-r2-endpoint" value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} placeholder="https://<account_id>.r2.cloudflarestorage.com" className="w-full px-3 py-1.5 border rounded-lg text-xs font-mono" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-prefix" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.basePrefix')}</label>
                      <input id="upstreams-r2-prefix" value={form.base_prefix} onChange={(e) => setForm({ ...form, base_prefix: e.target.value })} placeholder="backups/" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-ak" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">R2 Access Key ID *</label>
                      <input id="upstreams-r2-ak" required value={form.access_key_id} onChange={(e) => setForm({ ...form, access_key_id: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-sk" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                        R2 Secret Access Key {form.id ? t('upstreams.secretMaskHint') : '*'}
                      </label>
                      <SecretField
                        id="upstreams-r2-sk"
                        required={!form.id}
                        hasSaved={Boolean(form.id && savedSecretFlags.secretKey)}
                        value={form.secret_access_key}
                        onChange={(v) => setForm({ ...form, secret_access_key: v })}
                        onReveal={form.id ? async () => (await api.revealUpstreamSecrets(form.id!)).secret_access_key || '' : undefined}
                      />
                    </div>
                    <div className="col-span-2">
                      <label htmlFor="upstreams-r2-sc" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.r2StorageClass')}</label>
                      <input id="upstreams-r2-sc" value={form.storage_class} onChange={(e) => setForm({ ...form, storage_class: e.target.value })} placeholder="STANDARD" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                  </div>
                  <label className="inline-flex items-center gap-2 text-xs text-slate-700 dark:text-slate-300">
                    <input type="checkbox" checked={form.force_path_style} onChange={(e) => setForm({ ...form, force_path_style: e.target.checked })} />
                    <span>{t('upstreams.r2ForcePathStyle')}</span>
                  </label>
                </div>
              ) : form.type === 'gcs' ? (
                <div className="border-t border-slate-200 dark:border-slate-800 pt-4 space-y-3">
                  <div className="font-semibold text-slate-800 dark:text-slate-100">{t('upstreams.gcsSection')}</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label htmlFor="upstreams-gcs-bucket" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">Bucket *</label>
                      <input id="upstreams-gcs-bucket" required value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} placeholder="my-gcs-bucket" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-gcs-prefix" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.basePrefix')}</label>
                      <input id="upstreams-gcs-prefix" value={form.base_prefix} onChange={(e) => setForm({ ...form, base_prefix: e.target.value })} placeholder="backups/" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div className="col-span-2">
                      <label htmlFor="upstreams-gcs-sc" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.gcsStorageClass')}</label>
                      <input id="upstreams-gcs-sc" value={form.storage_class} onChange={(e) => setForm({ ...form, storage_class: e.target.value })} placeholder="STANDARD" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div className="col-span-2">
                      <div className="flex items-center justify-between mb-1">
                        <label htmlFor="upstreams-gcs-sa" className="block text-xs text-slate-600 dark:text-slate-300">
                          {t('upstreams.gcsSaLabel')} {form.id ? t('upstreams.secretMaskHint') : '*'}
                        </label>
                        <label className="inline-flex items-center gap-1 text-xs text-indigo-600 dark:text-indigo-400 hover:text-indigo-800 dark:hover:text-indigo-300 cursor-pointer font-medium">
                          <Upload className="w-3.5 h-3.5" />
                          <span>{t('upstreams.gcsImportJson')}</span>
                          <input
                            type="file"
                            accept=".json,application/json"
                            className="hidden"
                            onChange={async (e) => {
                              const file = e.target.files?.[0]
                              e.target.value = ''
                              if (!file) return
                              try {
                                const text = await file.text()
                                JSON.parse(text)
                                setForm((prev) => ({ ...prev, service_account_json: text }))
                                toast.success(t('upstreams.gcsLoadedFile', { name: file.name }))
                              } catch {
                                toast.error(t('upstreams.gcsInvalidJson'))
                              }
                            }}
                          />
                        </label>
                      </div>
                      <SecretField
                        id="upstreams-gcs-sa"
                        multiline
                        rows={5}
                        required={!form.id}
                        hasSaved={Boolean(form.id && savedSecretFlags.serviceAccountJSON)}
                        value={form.service_account_json}
                        onChange={(v) => setForm({ ...form, service_account_json: v })}
                        placeholder={form.id && savedSecretFlags.serviceAccountJSON ? t('upstreams.gcsSaPlaceholderSaved') : '{"type":"service_account","project_id":"...","private_key":"...","client_email":"..."}'}
                        onReveal={form.id ? async () => (await api.revealUpstreamSecrets(form.id!)).service_account_json || '' : undefined}
                      />
                    </div>
                  </div>
                  <div className="p-2.5 rounded-lg bg-slate-50 dark:bg-slate-800/60 border border-slate-200 dark:border-slate-700 text-[11px] text-slate-600 dark:text-slate-300 leading-relaxed">
                    {t('upstreams.gcsTip1')}
                    <b>Storage Object Admin</b>
                    {t('upstreams.gcsTip2')}
                    <code className="font-mono">roles/storage.objectAdmin</code>
                    {t('upstreams.gcsTip3')}
                    <code className="font-mono">storage.buckets.get</code>
                    {t('upstreams.gcsTip4')}
                  </div>
                </div>
              ) : (
                <div className="border-t border-slate-200 dark:border-slate-800 pt-4 space-y-3">
                  <div className="font-semibold text-slate-800 dark:text-slate-100">{t('upstreams.dropboxSection')}</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label htmlFor="upstreams-field-10" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">App Key *</label>
                      <input id="upstreams-field-10" required value={form.app_key} onChange={(e) => setForm({ ...form, app_key: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-11" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                        App Secret {form.id ? t('upstreams.secretMaskHint') : '*'}
                      </label>
                      <SecretField
                        id="upstreams-field-11"
                        required={!form.id}
                        hasSaved={Boolean(form.id && savedSecretFlags.appSecret)}
                        value={form.app_secret}
                        onChange={(v) => setForm({ ...form, app_secret: v })}
                        onReveal={form.id ? async () => (await api.revealUpstreamSecrets(form.id!)).app_secret || '' : undefined}
                      />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-12" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.dropboxRootPath')}</label>
                      <input id="upstreams-field-12" value={form.root_path} onChange={(e) => setForm({ ...form, root_path: e.target.value })} placeholder="/safekeep" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-13" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">{t('upstreams.dropboxRefreshToken')}</label>
                      <SecretField
                        id="upstreams-field-13"
                        hasSaved={Boolean(form.id && savedSecretFlags.refreshToken)}
                        value={form.refresh_token}
                        onChange={(v) => setForm({ ...form, refresh_token: v })}
                        onReveal={form.id ? async () => (await api.revealUpstreamSecrets(form.id!)).refresh_token || '' : undefined}
                      />
                    </div>
                  </div>
                  <div className="p-2.5 rounded-lg bg-sky-50 dark:bg-sky-500/10 border border-sky-200 dark:border-sky-500/30 text-xs text-sky-900 dark:text-sky-200 space-y-1">
                    <div>
                      {t('upstreams.dropboxModalTip1')}
                      <b>Redirect URIs</b>
                      {t('upstreams.dropboxModalTip2')}
                    </div>
                    <div className="flex items-center gap-2 flex-wrap">
                      <code className="font-mono bg-white dark:bg-slate-900 px-2 py-0.5 rounded border border-sky-200 dark:border-sky-500/30 text-[11px] break-all">
                        {`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`}
                      </code>
                      <button
                        type="button"
                        onClick={async () => {
                          try {
                            await copyText(`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`)
                            toast.success(t('upstreams.copiedSimpleCallback'))
                          } catch (e: any) {
                            toast.error(e.message || t('common.copyFailed'))
                          }
                        }}
                        className="text-sky-700 dark:text-sky-300 hover:text-sky-900 dark:hover:text-white inline-flex items-center gap-0.5 font-medium"
                      >
                        <Copy className="w-3.5 h-3.5" /> {t('common.copy')}
                      </button>
                    </div>
                    <div className="text-[11px] text-sky-700 dark:text-sky-300">
                      {t('upstreams.dropboxModalNote1')}
                      <code className="font-mono">localhost</code>
                      {t('upstreams.dropboxModalNote2')}
                      <code className="font-mono">127.0.0.1</code>
                      {t('upstreams.dropboxModalNote3')}
                      <code className="font-mono">http://</code>
                      {t('upstreams.dropboxModalNote4')}
                      <code className="font-mono">https://</code>
                      {t('upstreams.dropboxModalNote5')}
                    </div>
                  </div>
                </div>
              )}

              <div className="border-t border-slate-200 dark:border-slate-800 pt-4 space-y-3">
                <label htmlFor="upstream-proxy" className="block font-semibold text-slate-800 dark:text-slate-100">{t('proxies.select')}</label>
                <select id="upstream-proxy" value={form.proxy_id} onChange={(e) => { setForm({ ...form, proxy_id: e.target.value }); setProxyTestResult(null) }} className="w-full px-3 py-1.5 border rounded-lg bg-white dark:bg-slate-900">
                  <option value="">{t('common.direct')}</option>
                  {proxies.map((p) => <option key={p.id} value={p.id}>{p.name} · {p.type.toUpperCase()} · {p.host}:{p.port}</option>)}
                </select>
                <p className="text-xs text-slate-500">{t('proxies.selectHint')}</p>
                {form.proxy_id && (
                  <div>
                    <div className="col-span-3 pt-2 border-t border-slate-100 dark:border-slate-800 space-y-2">
                      <div className="flex flex-wrap items-end gap-2">
                        <div className="flex-1 min-w-[200px]">
                          <label htmlFor="upstreams-field-proxy-target" className="block text-xs text-slate-600 dark:text-slate-300 mb-1">
                            {t('upstreams.proxyTargetLabel', { url: defaultProxyTestURL() })}
                          </label>
                          <input
                            id="upstreams-field-proxy-target"
                            value={proxyTargetURL}
                            onChange={(e) => setProxyTargetURL(e.target.value)}
                            placeholder={defaultProxyTestURL()}
                            className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs"
                          />
                        </div>
                        <button
                          type="button"
                          onClick={handleTestProxy}
                          disabled={testingProxy || !form.proxy_id}
                          className="inline-flex items-center gap-1.5 px-3 py-1.5 border border-indigo-200 dark:border-indigo-500/30 bg-indigo-50 dark:bg-indigo-500/15 hover:bg-indigo-100 dark:hover:bg-indigo-500/25 disabled:opacity-45 disabled:cursor-not-allowed text-indigo-700 dark:text-indigo-300 rounded-lg text-xs font-medium shrink-0"
                        >
                          {testingProxy ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Wifi className="w-3.5 h-3.5" />}
                          <span>{testingProxy ? t('upstreams.testing') : t('upstreams.testProxyBtn')}</span>
                        </button>
                      </div>
                      {proxyTestResult && (
                        <div
                          className={`p-2.5 rounded-lg border text-xs flex items-start gap-2 ${
                            proxyTestResult.ok
                              ? 'bg-emerald-50 dark:bg-emerald-500/15 border-emerald-200 dark:border-emerald-500/30 text-emerald-800 dark:text-emerald-200'
                              : 'bg-rose-50 dark:bg-rose-500/15 border-rose-200 dark:border-rose-500/30 text-rose-800 dark:text-rose-200'
                          }`}
                        >
                          {proxyTestResult.ok ? (
                            <CheckCircle2 className="w-4 h-4 text-emerald-600 dark:text-emerald-400 shrink-0 mt-0.5" />
                          ) : (
                            <XCircle className="w-4 h-4 text-rose-600 dark:text-rose-400 shrink-0 mt-0.5" />
                          )}
                          <span className="break-all">{proxyTestResult.text}</span>
                        </div>
                      )}
                    </div>
                  </div>
                )}
              </div>

              <div className="flex justify-end gap-2 pt-3 border-t border-slate-200 dark:border-slate-800">
                <button type="button" disabled={saving} onClick={closeModal} className="px-4 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50">
                  {t('common.cancel')}
                </button>
                <button
                  type="submit"
                  disabled={saving || (Boolean(form.id) && !upstreamDirty)}
                  className="px-4 py-2 rounded-lg bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white font-medium"
                >
                  {saving ? t('common.saving') : form.id && !upstreamDirty ? t('common.noChanges') : t('upstreams.saveConfigBtn')}
                </button>
              </div>
            </form>
          </div>
        </Modal>
      )}
    </div>
  )
}
