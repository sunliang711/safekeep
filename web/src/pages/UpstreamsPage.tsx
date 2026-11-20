import React, { useEffect, useState } from 'react'
import { CheckCircle2, Copy, Link2, Loader2, Plus, Trash2, Upload, Wifi, XCircle } from 'lucide-react'
import { EmptyState, LoadingState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { SecretField } from '../components/SecretField'
import { api } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
import { copyText } from '../utils/clipboard'
import type { Upstream } from '../types'

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
  // Proxy
  proxy_enabled: boolean
  proxy_type: 'http' | 'https' | 'socks5'
  proxy_host: string
  proxy_port: number
  proxy_username: string
  proxy_password: string
  clear_proxy_password: boolean
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
  proxy_enabled: false,
  proxy_type: 'socks5',
  proxy_host: '127.0.0.1',
  proxy_port: 1080,
  proxy_username: '',
  proxy_password: '',
  clear_proxy_password: false,
})

export const UpstreamsPage: React.FC = () => {
  const toast = useToast()
  const [items, setItems] = useState<Upstream[]>([])
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
    proxyPass: false,
  })

  const load = async () => {
    setLoading(true)
    try {
      const res = await api.listUpstreams()
      setItems(res.items || [])
    } catch (err: any) {
      toast.error(err.message || '加载上游失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
    const params = new URLSearchParams(window.location.search)
    if (params.get('oauth') === 'success') {
      toast.success('Dropbox OAuth2 授权成功，Token 已加密保存。')
      window.history.replaceState({}, '', '/upstreams')
    } else if (params.get('oauth') === 'error') {
      toast.error(`Dropbox OAuth2 授权失败: ${params.get('reason') || '未知错误'}`)
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
      proxy_enabled: u.proxy_enabled,
      proxy_type: (u.proxy_type as any) || 'socks5',
      proxy_host: u.proxy_host || '',
      proxy_port: u.proxy_port || 1080,
      proxy_username: u.proxy_username || '',
      proxy_password: '',
      clear_proxy_password: false,
    }
    setForm(nextForm)
    setInitialFormSnap(JSON.stringify(nextForm))
    setSavedSecretFlags({
      secretKey: Boolean(u.config?.has_secret_key),
      serviceAccountJSON: Boolean(u.config?.has_service_account_json),
      appSecret: Boolean(u.config?.has_app_secret),
      refreshToken: Boolean(u.config?.has_refresh_token),
      proxyPass: Boolean(u.has_proxy_password),
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
    if (!form.proxy_enabled || testingProxy) return
    setProxyTestResult(null)
    setTestingProxy(true)
    const targetURL = proxyTargetURL.trim() || defaultProxyTestURL()
    try {
      const res = await api.testUpstreamProxy({
        upstream_id: form.id,
        upstream_type: form.type,
        proxy_type: form.proxy_type,
        proxy_host: form.proxy_host,
        proxy_port: Number(form.proxy_port),
        proxy_username: form.proxy_username,
        proxy_password: form.proxy_password,
        clear_proxy_password: form.clear_proxy_password,
        target_url: targetURL,
      })
      if (res.status === 'ok') {
        const msg = `代理连接成功（HTTP ${res.http_status} · 延迟 ${res.latency_ms ?? 0} ms · 目标 ${res.target_url}）`
        setProxyTestResult({ ok: true, text: msg })
        toast.success('代理连通性测试通过！')
      } else {
        const msg = `代理测试失败：${res.error || '未知错误'}`
        setProxyTestResult({ ok: false, text: msg })
        toast.error(msg)
      }
    } catch (err: any) {
      const msg = `代理测试失败：${err.message || '请求失败'}`
      setProxyTestResult({ ok: false, text: msg })
      toast.error(msg)
    } finally {
      setTestingProxy(false)
    }
  }

  const closeModal = () => {
    if (JSON.stringify(form) !== initialFormSnap && !confirm('配置尚未保存，确定放弃修改吗？')) return
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
      proxy_enabled: form.proxy_enabled,
      proxy_type: form.proxy_type,
      proxy_host: form.proxy_host,
      proxy_port: Number(form.proxy_port),
      proxy_username: form.proxy_username,
    }
    if (form.clear_proxy_password) {
      payload.proxy_password = ''
    } else if (!form.id || form.proxy_password !== '') {
      payload.proxy_password = form.proxy_password
    }

    try {
      if (form.id) {
        await api.updateUpstream(form.id, payload)
      } else {
        await api.createUpstream(payload)
      }
      setModalOpen(false)
      toast.success('上游配置已保存')
      await load()
    } catch (err: any) {
      setModalErr(err.message || '保存失败')
    } finally {
      setSaving(false)
    }
  }

  const handleTest = async (id: string) => {
    setTestingId(id)
    try {
      const res = await api.testUpstream(id)
      if (res.status === 'ok') {
        toast.success('上游存储及网络代理连通性测试通过！')
      } else {
        toast.error(`连通性测试失败: ${res.error}`)
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
    if (!confirm(`确定删除上游 "${u.name}" 吗？`)) return
    try {
      await api.deleteUpstream(u.id)
      toast.success(`已删除上游 "${u.name}"`)
      await load()
    } catch (err: any) {
      toast.error(err.message)
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900">上游存储与网络代理</h1>
          <p className="text-sm text-slate-500">管理 S3 / Cloudflare R2 / Google Cloud Storage / Dropbox 上游，支持为每个上游单独配置 HTTP / SOCKS5 代理</p>
        </div>
        <button
          onClick={() => {
            setModalErr('')
            const next = emptyForm()
            setForm(next)
            setInitialFormSnap(JSON.stringify(next))
            setSavedSecretFlags({ secretKey: false, serviceAccountJSON: false, appSecret: false, refreshToken: false, proxyPass: false })
            setProxyTestResult(null)
            setProxyTargetURL('')
            setModalOpen(true)
          }}
          className="inline-flex items-center gap-1.5 px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm font-medium"
        >
          <Plus className="w-4 h-4" /> 添加上游存储
        </button>
      </div>

      {loading ? (
        <LoadingState label="正在读取上游存储…" />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {items.length === 0 && <div className="md:col-span-2 bg-white rounded-xl border border-slate-200"><EmptyState title="连接你的第一个云端存储" description="支持 Amazon S3、Cloudflare R2、Google Cloud Storage (GCS) 与 Dropbox。每个上游都可以独立配置网络代理。" action={<button className="primary-button" onClick={() => {
            const next = emptyForm()
            setForm(next)
            setInitialFormSnap(JSON.stringify(next))
            setSavedSecretFlags({ secretKey: false, serviceAccountJSON: false, appSecret: false, refreshToken: false, proxyPass: false })
            setProxyTestResult(null)
            setProxyTargetURL('')
            setModalErr('')
            setModalOpen(true)
          }}><Plus className="w-4 h-4" />添加上游存储</button>} /></div>}
          {items.map((u) => (
            <div key={u.id} className="bg-white rounded-xl border border-slate-200 p-5 space-y-3">
              <div className="flex items-start justify-between">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-semibold text-slate-900">{u.name}</span>
                    <span className="text-xs px-2 py-0.5 rounded bg-slate-100 text-slate-700 font-mono">
                      {u.type === 'gcs' ? 'GCS' : u.type === 'r2' ? 'R2' : u.type.toUpperCase()}
                    </span>
                    <StatusBadge value={u.enabled ? 'enabled' : 'disabled'} label={u.enabled ? '启用' : '禁用'} />
                  </div>
                  <div className="text-xs text-slate-400 mt-1 font-mono">ID: {u.id}</div>
                </div>
                <StatusBadge value={u.last_test_status} />
              </div>

              <div className="text-xs text-slate-600 space-y-1 bg-slate-50 rounded-lg p-3">
                {u.type === 's3' ? (
                  <>
                    <div>Bucket: <span className="font-mono">{u.config.bucket}</span> ({u.config.region || 'us-east-1'})</div>
                    {u.config.endpoint && <div>Endpoint: <span className="font-mono">{u.config.endpoint}</span></div>}
                    {u.config.base_prefix && <div>前缀: <span className="font-mono">{u.config.base_prefix}</span></div>}
                  </>
                ) : u.type === 'r2' ? (
                  <>
                    <div>Bucket: <span className="font-mono">{u.config.bucket}</span> ({u.config.region || 'auto'})</div>
                    {u.config.account_id && <div>Account ID: <span className="font-mono">{u.config.account_id}</span>{u.config.jurisdiction ? ` (${u.config.jurisdiction.toUpperCase()})` : ''}</div>}
                    {(u.config.resolved_endpoint || u.config.endpoint) && (
                      <div>Endpoint: <span className="font-mono break-all">{u.config.resolved_endpoint || u.config.endpoint}</span></div>
                    )}
                    {u.config.base_prefix && <div>前缀: <span className="font-mono">{u.config.base_prefix}</span></div>}
                  </>
                ) : u.type === 'gcs' ? (
                  <>
                    <div>Bucket: <span className="font-mono">{u.config.bucket}</span></div>
                    {u.config.project_id && <div>Project ID: <span className="font-mono">{u.config.project_id}</span></div>}
                    {u.config.client_email && <div>Service Account: <span className="font-mono break-all">{u.config.client_email}</span></div>}
                    {u.config.storage_class && <div>Storage Class: <span className="font-mono">{u.config.storage_class}</span></div>}
                    {u.config.base_prefix && <div>前缀: <span className="font-mono">{u.config.base_prefix}</span></div>}
                  </>
                ) : (
                  <>
                    <div>授权状态: {u.config.authorized ? <span className="text-emerald-700 font-medium">已授权 ({u.config.account_email || '已连接'})</span> : <span className="text-amber-700">未授权</span>}</div>
                    <div>根目录: <span className="font-mono">{u.config.root_path || '/'}</span></div>
                    {!u.config.authorized ? (
                      (() => {
                        const port = window.location.port || (window.location.protocol === 'https:' ? '443' : '80')
                        const host = window.location.hostname
                        const isLocal = host === 'localhost' || host === '127.0.0.1'
                        const isNonLocalHTTP = window.location.protocol === 'http:' && !isLocal
                        const targetHost = isLocal ? '<服务器IP>' : host
                        const sshCmd = `ssh -N -L ${port}:127.0.0.1:${port} <用户名>@${targetHost}`
                        const localhostCallback = `http://localhost:${port}/admin/api/v1/oauth/dropbox/callback`
                        const currentCallback = `${window.location.origin}/admin/api/v1/oauth/dropbox/callback`
                        const copyTargetCallback = isNonLocalHTTP ? localhostCallback : currentCallback

                        return (
                          <div className="mt-1.5 p-2.5 rounded-lg bg-amber-50 border border-amber-200 text-amber-900 space-y-2">
                            <div className="font-medium">授权前请先配置 Dropbox 回调地址：</div>
                            <div className="text-[11px] text-amber-800 leading-relaxed">
                              前往 Dropbox Developer Console → Settings → OAuth 2 → <b>Redirect URIs</b> 添加并保存回调地址。
                              Dropbox 仅支持 <code className="font-mono">https://</code> 或 <code className="font-mono">http://localhost</code>，<b>不支持非 localhost 的 http://（如局域网/公网 IP）</b>。
                            </div>
                            <div className="flex items-center gap-1.5 flex-wrap">
                              <code className="font-mono bg-white px-1.5 py-0.5 rounded border border-amber-200 text-[11px] break-all">
                                {copyTargetCallback}
                              </code>
                              <button
                                type="button"
                                onClick={async () => {
                                  try {
                                    await copyText(copyTargetCallback)
                                    toast.success('已复制回调地址，请粘贴并保存到 Dropbox App Console -> OAuth 2 -> Redirect URIs 后再点击授权。')
                                  } catch (e: any) {
                                    toast.error(e.message || '复制失败')
                                  }
                                }}
                                className="text-amber-800 hover:text-amber-950 inline-flex items-center gap-0.5 font-medium"
                                title="复制回调地址"
                              >
                                <Copy className="w-3 h-3" /> 复制
                              </button>
                            </div>
                            <div className="pt-1.5 border-t border-amber-200/80 text-[11px] text-amber-900 space-y-1">
                              <div className="font-medium">
                                {isNonLocalHTTP
                                  ? '检测到当前正在通过非 localhost 的 HTTP 访问，建议通过 SSH 隧道映射到本地后授权：'
                                  : '若在无 HTTPS 域名的远程/局域网服务器部署，可在本地终端建立 SSH 隧道后使用 localhost 授权：'}
                              </div>
                              <div className="text-amber-800">
                                1. 在本机执行端口转发（将本地 <code className="font-mono">localhost:{port}</code> 映射到目标服务器）：
                              </div>
                              <div className="flex items-center gap-1.5 flex-wrap">
                                <code className="font-mono bg-white px-1.5 py-0.5 rounded border border-amber-200 text-[11px] break-all">
                                  {sshCmd}
                                </code>
                                <button
                                  type="button"
                                  onClick={async () => {
                                    try {
                                      await copyText(sshCmd)
                                      toast.success('已复制 SSH 隧道命令（请将 <用户名> 替换为实际 SSH 登录用户）')
                                    } catch (e: any) {
                                      toast.error(e.message || '复制失败')
                                    }
                                  }}
                                  className="text-amber-800 hover:text-amber-950 inline-flex items-center gap-0.5 font-medium"
                                  title="复制 SSH 隧道命令"
                                >
                                  <Copy className="w-3 h-3" /> 复制命令
                                </button>
                              </div>
                              <div className="text-amber-800">
                                2. 在浏览器改用 <code className="font-mono">http://localhost:{port}</code> 打开管理端，将上方回调地址填入 Dropbox Redirect URIs 并完成授权（授权成功后 Refresh Token 会持久化在服务端，之后可关闭隧道恢复原地址访问）。
                              </div>
                            </div>
                          </div>
                        )
                      })()
                    ) : (
                      <div className="flex items-center gap-1.5 flex-wrap">
                        <span>OAuth 回调 URI:</span>
                        <code className="font-mono bg-white px-1.5 py-0.5 rounded border text-[11px] break-all">
                          {`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`}
                        </code>
                        <button
                          type="button"
                          onClick={async () => {
                            try {
                              await copyText(`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`)
                              toast.success('已复制 Dropbox Redirect URI。')
                            } catch (e: any) {
                              toast.error(e.message || '复制失败')
                            }
                          }}
                          className="text-indigo-600 hover:text-indigo-800 inline-flex items-center gap-0.5"
                          title="复制回调地址"
                        >
                          <Copy className="w-3 h-3" />
                        </button>
                      </div>
                    )}
                  </>
                )}
                <div>
                  网络代理:{' '}
                  {u.proxy_enabled ? (
                    <span className="font-mono text-indigo-700">
                      {u.proxy_type.toUpperCase()}://{u.proxy_host}:{u.proxy_port}
                    </span>
                  ) : (
                    '直连（未启用代理）'
                  )}
                </div>
                {u.last_test_error && <div className="text-rose-600 break-all">最近错误: {u.last_test_error}</div>}
              </div>

              <div className="flex flex-wrap items-center gap-2 pt-1">
                <button
                  onClick={() => openEdit(u)}
                  className="px-3 py-1.5 text-xs bg-slate-100 hover:bg-slate-200 rounded-lg text-slate-700 font-medium"
                >
                  编辑配置
                </button>
                <button
                  onClick={() => handleTest(u.id)}
                  disabled={testingId === u.id}
                  className="inline-flex items-center gap-1 px-3 py-1.5 text-xs bg-indigo-50 hover:bg-indigo-100 text-indigo-700 rounded-lg font-medium"
                >
                  <Wifi className="w-3.5 h-3.5" />
                  {testingId === u.id ? '测试中...' : '测试连接'}
                </button>
                {u.type === 'dropbox' && (
                  <button
                    onClick={() => handleDropboxOAuth(u.id)}
                    className="inline-flex items-center gap-1 px-3 py-1.5 text-xs bg-sky-50 hover:bg-sky-100 text-sky-700 rounded-lg font-medium"
                  >
                    <Link2 className="w-3.5 h-3.5" />
                    Web OAuth2 授权
                  </button>
                )}
                <button
                  onClick={() => handleDelete(u)}
                  className="ml-auto p-1.5 text-slate-400 hover:text-rose-600 rounded-lg"
                  title="删除"
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      {modalOpen && (
        <Modal title={form.id ? '编辑上游存储' : '添加上游存储'} onClose={closeModal} busy={saving}>
          <div className="bg-white rounded-2xl max-w-2xl w-full max-h-[90vh] overflow-y-auto p-6">
            <h2 className="text-lg font-bold text-slate-900 mb-4">
              {form.id ? '编辑上游存储' : '添加上游存储'}
            </h2>
            {modalErr && (
              <div className="mb-3 p-2.5 rounded-lg bg-rose-50 border border-rose-200 text-rose-700 text-xs">
                {modalErr}
              </div>
            )}
            <form onSubmit={handleSave} className="space-y-4 text-sm">
              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label htmlFor="upstreams-field-1" className="block font-medium text-slate-700 mb-1">名称</label>
                  <input id="upstreams-field-1"
                    required
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                    className="w-full px-3 py-2 border rounded-lg"
                    placeholder="prod-s3"
                  />
                </div>
                <div>
                  <label htmlFor="upstreams-field-2" className="block font-medium text-slate-700 mb-1">类型</label>
                  <select id="upstreams-field-2"
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
                    className="w-full px-3 py-2 border rounded-lg bg-white"
                  >
                    <option value="s3">Amazon S3 / 兼容对象存储</option>
                    <option value="r2">Cloudflare R2</option>
                    <option value="gcs">Google Cloud Storage (GCS)</option>
                    <option value="dropbox">Dropbox</option>
                  </select>
                </div>
              </div>

              <label className="inline-flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={form.enabled}
                  onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
                />
                <span>启用该上游存储</span>
              </label>

              {form.type === 's3' ? (
                <div className="border-t pt-4 space-y-3">
                  <div className="font-semibold text-slate-800">S3 存储参数</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label htmlFor="upstreams-field-3" className="block text-xs text-slate-600 mb-1">Bucket *</label>
                      <input id="upstreams-field-3" required value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-4" className="block text-xs text-slate-600 mb-1">Region</label>
                      <input id="upstreams-field-4" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-5" className="block text-xs text-slate-600 mb-1">Endpoint（MinIO/OSS 等可选）</label>
                      <input id="upstreams-field-5" value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} placeholder="https://s3.amazonaws.com" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-6" className="block text-xs text-slate-600 mb-1">全局路径前缀</label>
                      <input id="upstreams-field-6" value={form.base_prefix} onChange={(e) => setForm({ ...form, base_prefix: e.target.value })} placeholder="backups/" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-7" className="block text-xs text-slate-600 mb-1">Access Key ID *</label>
                      <input id="upstreams-field-7" required value={form.access_key_id} onChange={(e) => setForm({ ...form, access_key_id: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-8" className="block text-xs text-slate-600 mb-1">Secret Access Key {form.id ? '（默认掩码 *****，点右侧可查看）' : '*'}</label>
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
                      <label htmlFor="upstreams-field-9" className="block text-xs text-slate-600 mb-1">Storage Class（可选，如 STANDARD_IA / GLACIER）</label>
                      <input id="upstreams-field-9" value={form.storage_class} onChange={(e) => setForm({ ...form, storage_class: e.target.value })} placeholder="STANDARD" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                  </div>
                  <label className="inline-flex items-center gap-2 text-xs">
                    <input type="checkbox" checked={form.force_path_style} onChange={(e) => setForm({ ...form, force_path_style: e.target.checked })} />
                    <span>Force Path Style（MinIO / 自建 S3 推荐开启）</span>
                  </label>
                </div>
              ) : form.type === 'r2' ? (
                <div className="border-t pt-4 space-y-3">
                  <div className="font-semibold text-slate-800">Cloudflare R2 参数</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label htmlFor="upstreams-r2-account" className="block text-xs text-slate-600 mb-1">Cloudflare Account ID {form.endpoint.trim() ? '（可选）' : '*'}</label>
                      <input id="upstreams-r2-account" required={!form.endpoint.trim()} value={form.account_id} onChange={(e) => setForm({ ...form, account_id: e.target.value })} placeholder="32 位 Account ID" className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-jurisdiction" className="block text-xs text-slate-600 mb-1">管辖区（Jurisdiction）</label>
                      <select id="upstreams-r2-jurisdiction" value={form.jurisdiction} onChange={(e) => setForm({ ...form, jurisdiction: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg bg-white text-xs">
                        <option value="">默认（全球）</option>
                        <option value="eu">欧盟（EU）</option>
                        <option value="fedramp">FedRAMP</option>
                      </select>
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-bucket" className="block text-xs text-slate-600 mb-1">Bucket *</label>
                      <input id="upstreams-r2-bucket" required value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-region" className="block text-xs text-slate-600 mb-1">Region（默认 auto）</label>
                      <input id="upstreams-r2-region" value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="auto" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-endpoint" className="block text-xs text-slate-600 mb-1">自定义 Endpoint（留空自动按 Account ID 生成）</label>
                      <input id="upstreams-r2-endpoint" value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} placeholder="https://<account_id>.r2.cloudflarestorage.com" className="w-full px-3 py-1.5 border rounded-lg text-xs font-mono" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-prefix" className="block text-xs text-slate-600 mb-1">全局路径前缀</label>
                      <input id="upstreams-r2-prefix" value={form.base_prefix} onChange={(e) => setForm({ ...form, base_prefix: e.target.value })} placeholder="backups/" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-ak" className="block text-xs text-slate-600 mb-1">R2 Access Key ID *</label>
                      <input id="upstreams-r2-ak" required value={form.access_key_id} onChange={(e) => setForm({ ...form, access_key_id: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-r2-sk" className="block text-xs text-slate-600 mb-1">R2 Secret Access Key {form.id ? '（默认掩码 *****，点右侧可查看）' : '*'}</label>
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
                      <label htmlFor="upstreams-r2-sc" className="block text-xs text-slate-600 mb-1">Storage Class（可选，如 STANDARD / STANDARD_IA）</label>
                      <input id="upstreams-r2-sc" value={form.storage_class} onChange={(e) => setForm({ ...form, storage_class: e.target.value })} placeholder="STANDARD" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                  </div>
                  <label className="inline-flex items-center gap-2 text-xs">
                    <input type="checkbox" checked={form.force_path_style} onChange={(e) => setForm({ ...form, force_path_style: e.target.checked })} />
                    <span>Force Path Style（一般保持关闭，自定义网关或本地测试时可开启）</span>
                  </label>
                </div>
              ) : form.type === 'gcs' ? (
                <div className="border-t pt-4 space-y-3">
                  <div className="font-semibold text-slate-800">Google Cloud Storage (GCS) 参数</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label htmlFor="upstreams-gcs-bucket" className="block text-xs text-slate-600 mb-1">Bucket *</label>
                      <input id="upstreams-gcs-bucket" required value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} placeholder="my-gcs-bucket" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-gcs-prefix" className="block text-xs text-slate-600 mb-1">全局路径前缀</label>
                      <input id="upstreams-gcs-prefix" value={form.base_prefix} onChange={(e) => setForm({ ...form, base_prefix: e.target.value })} placeholder="backups/" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div className="col-span-2">
                      <label htmlFor="upstreams-gcs-sc" className="block text-xs text-slate-600 mb-1">Storage Class（可选，如 STANDARD / NEARLINE / COLDLINE / ARCHIVE）</label>
                      <input id="upstreams-gcs-sc" value={form.storage_class} onChange={(e) => setForm({ ...form, storage_class: e.target.value })} placeholder="STANDARD" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div className="col-span-2">
                      <div className="flex items-center justify-between mb-1">
                        <label htmlFor="upstreams-gcs-sa" className="block text-xs text-slate-600">
                          Service Account JSON 密钥 {form.id ? '（默认掩码 *****，点右侧可查看）' : '*'}
                        </label>
                        <label className="inline-flex items-center gap-1 text-xs text-indigo-600 hover:text-indigo-800 cursor-pointer font-medium">
                          <Upload className="w-3.5 h-3.5" />
                          <span>从 .json 文件导入</span>
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
                                toast.success(`已读取 ${file.name}`)
                              } catch {
                                toast.error('所选文件不是有效的 JSON 格式')
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
                        placeholder={form.id && savedSecretFlags.serviceAccountJSON ? '留空则保持不变，点击可直接粘贴新 Service Account JSON' : '{"type":"service_account","project_id":"...","private_key":"...","client_email":"..."}'}
                        onReveal={form.id ? async () => (await api.revealUpstreamSecrets(form.id!)).service_account_json || '' : undefined}
                      />
                    </div>
                  </div>
                  <div className="p-2.5 rounded-lg bg-slate-50 border border-slate-200 text-[11px] text-slate-600 leading-relaxed">
                    请在 GCP Console → IAM 与管理 → 服务账号 创建 JSON 密钥，并授予目标 Bucket 的 <b>Storage Object Admin</b>（或 <code className="font-mono">roles/storage.objectAdmin</code> + <code className="font-mono">storage.buckets.get</code>）权限。所有请求及 Token 交换均遵循下方代理配置。
                  </div>
                </div>
              ) : (
                <div className="border-t pt-4 space-y-3">
                  <div className="font-semibold text-slate-800">Dropbox 参数</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label htmlFor="upstreams-field-10" className="block text-xs text-slate-600 mb-1">App Key *</label>
                      <input id="upstreams-field-10" required value={form.app_key} onChange={(e) => setForm({ ...form, app_key: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-11" className="block text-xs text-slate-600 mb-1">App Secret {form.id ? '（默认掩码 *****，点右侧可查看）' : '*'}</label>
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
                      <label htmlFor="upstreams-field-12" className="block text-xs text-slate-600 mb-1">Dropbox 根目录</label>
                      <input id="upstreams-field-12" value={form.root_path} onChange={(e) => setForm({ ...form, root_path: e.target.value })} placeholder="/safekeep" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-13" className="block text-xs text-slate-600 mb-1">Refresh Token（可选，默认掩码 *****）</label>
                      <SecretField
                        id="upstreams-field-13"
                        hasSaved={Boolean(form.id && savedSecretFlags.refreshToken)}
                        value={form.refresh_token}
                        onChange={(v) => setForm({ ...form, refresh_token: v })}
                        onReveal={form.id ? async () => (await api.revealUpstreamSecrets(form.id!)).refresh_token || '' : undefined}
                      />
                    </div>
                  </div>
                  <div className="p-2.5 rounded-lg bg-sky-50 border border-sky-200 text-xs text-sky-900 space-y-1">
                    <div>
                      授权前请先前往 Dropbox Developer Console → Settings → OAuth 2 → <b>Redirect URIs</b>，添加并保存以下回调地址：
                    </div>
                    <div className="flex items-center gap-2">
                      <code className="font-mono bg-white px-2 py-0.5 rounded border text-[11px] break-all">
                        {`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`}
                      </code>
                      <button
                        type="button"
                        onClick={async () => {
                          try {
                            await copyText(`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`)
                            toast.success('已复制回调地址')
                          } catch (e: any) {
                            toast.error(e.message || '复制失败')
                          }
                        }}
                        className="text-sky-700 hover:text-sky-900 inline-flex items-center gap-0.5 font-medium"
                      >
                        <Copy className="w-3.5 h-3.5" /> 复制
                      </button>
                    </div>
                    <div className="text-[11px] text-sky-700">
                      注意：Dropbox 仅对 <code className="font-mono">localhost</code> / <code className="font-mono">127.0.0.1</code> 允许 <code className="font-mono">http://</code>，局域网 IP 或域名必须使用 <code className="font-mono">https://</code>。
                    </div>
                  </div>
                </div>
              )}

              <div className="border-t pt-4 space-y-3">
                <label className="inline-flex items-center gap-2 font-semibold text-slate-800">
                  <input type="checkbox" checked={form.proxy_enabled} onChange={(e) => setForm({ ...form, proxy_enabled: e.target.checked })} />
                  <span>为该上游启用独立网络代理（HTTP / SOCKS5）</span>
                </label>
                {form.proxy_enabled && (
                  <div className="grid grid-cols-3 gap-3">
                    <div>
                      <label htmlFor="upstreams-field-14" className="block text-xs text-slate-600 mb-1">代理协议</label>
                      <select id="upstreams-field-14" value={form.proxy_type} onChange={(e) => setForm({ ...form, proxy_type: e.target.value as any })} className="w-full px-3 py-1.5 border rounded-lg bg-white">
                        <option value="socks5">SOCKS5</option>
                        <option value="http">HTTP</option>
                        <option value="https">HTTPS</option>
                      </select>
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-15" className="block text-xs text-slate-600 mb-1">代理主机</label>
                      <input id="upstreams-field-15" required value={form.proxy_host} onChange={(e) => setForm({ ...form, proxy_host: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-16" className="block text-xs text-slate-600 mb-1">端口</label>
                      <input id="upstreams-field-16" type="number" required min={1} max={65535} value={form.proxy_port} onChange={(e) => setForm({ ...form, proxy_port: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label htmlFor="upstreams-field-17" className="block text-xs text-slate-600 mb-1">代理用户名（可选）</label>
                      <input id="upstreams-field-17" value={form.proxy_username} onChange={(e) => setForm({ ...form, proxy_username: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div className="col-span-2">
                      <label htmlFor="upstreams-field-18" className="block text-xs text-slate-600 mb-1">代理密码（{form.id ? '默认掩码 *****，点右侧可查看' : '可选'}）</label>
                      <SecretField
                        id="upstreams-field-18"
                        disabled={form.clear_proxy_password}
                        hasSaved={Boolean(form.id && savedSecretFlags.proxyPass && !form.clear_proxy_password)}
                        value={form.proxy_password}
                        onChange={(v) => setForm({ ...form, proxy_password: v })}
                        onReveal={form.id ? async () => (await api.revealUpstreamSecrets(form.id!)).proxy_password || '' : undefined}
                      />
                      {form.id && (
                        <label className="inline-flex items-center gap-1.5 text-[11px] text-slate-500 mt-1">
                          <input type="checkbox" checked={form.clear_proxy_password} onChange={(e) => setForm({ ...form, clear_proxy_password: e.target.checked, proxy_password: '' })} />
                          <span>清除已有代理密码</span>
                        </label>
                      )}
                    </div>
                    <div className="col-span-3 pt-2 border-t border-slate-100 space-y-2">
                      <div className="flex flex-wrap items-end gap-2">
                        <div className="flex-1 min-w-[200px]">
                          <label htmlFor="upstreams-field-proxy-target" className="block text-xs text-slate-600 mb-1">
                            代理测试目标 URL（可选，默认 {defaultProxyTestURL()}）
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
                          disabled={testingProxy || !form.proxy_host.trim() || !form.proxy_port}
                          className="inline-flex items-center gap-1.5 px-3 py-1.5 border border-indigo-200 bg-indigo-50 hover:bg-indigo-100 disabled:opacity-45 disabled:cursor-not-allowed text-indigo-700 rounded-lg text-xs font-medium shrink-0"
                        >
                          {testingProxy ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Wifi className="w-3.5 h-3.5" />}
                          <span>{testingProxy ? '测试中...' : '测试代理'}</span>
                        </button>
                      </div>
                      {proxyTestResult && (
                        <div
                          className={`p-2.5 rounded-lg border text-xs flex items-start gap-2 ${
                            proxyTestResult.ok
                              ? 'bg-emerald-50 border-emerald-200 text-emerald-800'
                              : 'bg-rose-50 border-rose-200 text-rose-800'
                          }`}
                        >
                          {proxyTestResult.ok ? (
                            <CheckCircle2 className="w-4 h-4 text-emerald-600 shrink-0 mt-0.5" />
                          ) : (
                            <XCircle className="w-4 h-4 text-rose-600 shrink-0 mt-0.5" />
                          )}
                          <span className="break-all">{proxyTestResult.text}</span>
                        </div>
                      )}
                    </div>
                  </div>
                )}
              </div>

              <div className="flex justify-end gap-2 pt-3 border-t">
                <button type="button" disabled={saving} onClick={closeModal} className="px-4 py-2 rounded-lg border text-slate-600 hover:bg-slate-50 disabled:opacity-50">
                  取消
                </button>
                <button
                  type="submit"
                  disabled={saving || (Boolean(form.id) && !upstreamDirty)}
                  className="px-4 py-2 rounded-lg bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white font-medium"
                >
                  {saving ? '保存中...' : form.id && !upstreamDirty ? '无改动' : '保存配置'}
                </button>
              </div>
            </form>
          </div>
        </Modal>
      )}
    </div>
  )
}
