import React, { useEffect, useState } from 'react'
import { CheckCircle2, Copy, Link2, Plus, Trash2, Wifi, XCircle } from 'lucide-react'
import { api } from '../api'
import { StatusBadge } from '../components/Badge'
import type { Upstream } from '../types'

interface FormState {
  id?: string
  name: string
  type: 's3' | 'dropbox'
  enabled: boolean
  // S3
  endpoint: string
  region: string
  bucket: string
  access_key_id: string
  secret_access_key: string
  base_prefix: string
  force_path_style: boolean
  storage_class: string
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
  endpoint: '',
  region: 'us-east-1',
  bucket: '',
  access_key_id: '',
  secret_access_key: '',
  base_prefix: '',
  force_path_style: false,
  storage_class: '',
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
  const [items, setItems] = useState<Upstream[]>([])
  const [loading, setLoading] = useState(true)
  const [modalOpen, setModalOpen] = useState(false)
  const [form, setForm] = useState<FormState>(emptyForm())
  const [msg, setMsg] = useState<{ type: 'ok' | 'err'; text: string } | null>(null)
  const [modalErr, setModalErr] = useState('')
  const [testingId, setTestingId] = useState<string | null>(null)

  const load = async () => {
    setLoading(true)
    try {
      const res = await api.listUpstreams()
      setItems(res.items || [])
    } catch (err: any) {
      setMsg({ type: 'err', text: err.message || '加载上游失败' })
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
    const params = new URLSearchParams(window.location.search)
    if (params.get('oauth') === 'success') {
      setMsg({ type: 'ok', text: 'Dropbox OAuth2 授权成功，Token 已加密保存。' })
      window.history.replaceState({}, '', '/upstreams')
    } else if (params.get('oauth') === 'error') {
      setMsg({ type: 'err', text: `Dropbox OAuth2 授权失败: ${params.get('reason') || '未知错误'}` })
      window.history.replaceState({}, '', '/upstreams')
    }
  }, [])

  const openEdit = (u: Upstream) => {
    setModalErr('')
    setForm({
      id: u.id,
      name: u.name,
      type: u.type,
      enabled: u.enabled,
      endpoint: u.config.endpoint || '',
      region: u.config.region || 'us-east-1',
      bucket: u.config.bucket || '',
      access_key_id: u.config.access_key_id || '',
      secret_access_key: '',
      base_prefix: u.config.base_prefix || '',
      force_path_style: Boolean(u.config.force_path_style),
      storage_class: u.config.storage_class || '',
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
    })
    setModalOpen(true)
  }

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg(null)
    setModalErr('')
    const config =
      form.type === 's3'
        ? {
            endpoint: form.endpoint,
            region: form.region,
            bucket: form.bucket,
            access_key_id: form.access_key_id,
            secret_access_key: form.secret_access_key,
            base_prefix: form.base_prefix,
            force_path_style: form.force_path_style,
            storage_class: form.storage_class,
          }
        : {
            app_key: form.app_key,
            app_secret: form.app_secret,
            refresh_token: form.refresh_token,
            root_path: form.root_path,
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
      setMsg({ type: 'ok', text: '上游配置已保存' })
      await load()
    } catch (err: any) {
      setModalErr(err.message || '保存失败')
    }
  }

  const handleTest = async (id: string) => {
    setTestingId(id)
    setMsg(null)
    try {
      const res = await api.testUpstream(id)
      if (res.status === 'ok') {
        setMsg({ type: 'ok', text: '上游存储及网络代理连通性测试通过！' })
      } else {
        setMsg({ type: 'err', text: `连通性测试失败: ${res.error}` })
      }
      await load()
    } catch (err: any) {
      setMsg({ type: 'err', text: err.message })
    } finally {
      setTestingId(null)
    }
  }

  const handleDropboxOAuth = async (id: string) => {
    try {
      const res = await api.startDropboxOAuth(id)
      window.location.href = res.authorize_url
    } catch (err: any) {
      setMsg({ type: 'err', text: err.message })
    }
  }

  const handleDelete = async (u: Upstream) => {
    if (!confirm(`确定删除上游 "${u.name}" 吗？`)) return
    try {
      await api.deleteUpstream(u.id)
      await load()
    } catch (err: any) {
      setMsg({ type: 'err', text: err.message })
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900">上游存储与网络代理</h1>
          <p className="text-sm text-slate-500">管理 S3 / Dropbox 上游，支持为每个上游单独配置 HTTP / SOCKS5 代理</p>
        </div>
        <button
          onClick={() => {
            setModalErr('')
            setForm(emptyForm())
            setModalOpen(true)
          }}
          className="inline-flex items-center gap-1.5 px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm font-medium"
        >
          <Plus className="w-4 h-4" /> 添加上游存储
        </button>
      </div>

      {msg && (
        <div
          className={`p-3 rounded-lg text-sm flex items-center gap-2 border ${
            msg.type === 'ok'
              ? 'bg-emerald-50 text-emerald-800 border-emerald-200'
              : 'bg-rose-50 text-rose-800 border-rose-200'
          }`}
        >
          {msg.type === 'ok' ? <CheckCircle2 className="w-4 h-4" /> : <XCircle className="w-4 h-4" />}
          <span>{msg.text}</span>
        </div>
      )}

      {loading ? (
        <div className="text-sm text-slate-500">加载中...</div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {items.map((u) => (
            <div key={u.id} className="bg-white rounded-xl border border-slate-200 p-5 space-y-3">
              <div className="flex items-start justify-between">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-semibold text-slate-900">{u.name}</span>
                    <span className="text-xs px-2 py-0.5 rounded bg-slate-100 text-slate-700 font-mono">
                      {u.type.toUpperCase()}
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
                ) : (
                  <>
                    <div>授权状态: {u.config.authorized ? <span className="text-emerald-700 font-medium">已授权 ({u.config.account_email || '已连接'})</span> : <span className="text-amber-700">未授权</span>}</div>
                    <div>根目录: <span className="font-mono">{u.config.root_path || '/'}</span></div>
                    {!u.config.authorized ? (
                      <div className="mt-1.5 p-2.5 rounded-lg bg-amber-50 border border-amber-200 text-amber-900 space-y-1">
                        <div className="font-medium">授权前请先配置 Dropbox 回调地址：</div>
                        <div className="text-[11px] text-amber-800">
                          前往 Dropbox Developer Console → Settings → OAuth 2 → <b>Redirect URIs</b>，添加并保存以下地址（非 <code className="font-mono">localhost</code>/<code className="font-mono">127.0.0.1</code> 地址需使用 <code className="font-mono">https://</code>）：
                        </div>
                        <div className="flex items-center gap-1.5 flex-wrap">
                          <code className="font-mono bg-white px-1.5 py-0.5 rounded border border-amber-200 text-[11px] break-all">
                            {`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`}
                          </code>
                          <button
                            type="button"
                            onClick={() => {
                              navigator.clipboard.writeText(`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`)
                              setMsg({ type: 'ok', text: '已复制回调地址，请粘贴并保存到 Dropbox App Console -> OAuth 2 -> Redirect URIs 后再点击授权。' })
                            }}
                            className="text-amber-800 hover:text-amber-950 inline-flex items-center gap-0.5 font-medium"
                            title="复制回调地址"
                          >
                            <Copy className="w-3 h-3" /> 复制
                          </button>
                        </div>
                      </div>
                    ) : (
                      <div className="flex items-center gap-1.5 flex-wrap">
                        <span>OAuth 回调 URI:</span>
                        <code className="font-mono bg-white px-1.5 py-0.5 rounded border text-[11px] break-all">
                          {`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`}
                        </code>
                        <button
                          type="button"
                          onClick={() => {
                            navigator.clipboard.writeText(`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`)
                            setMsg({ type: 'ok', text: '已复制 Dropbox Redirect URI。' })
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
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center p-4 z-50">
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
                  <label className="block font-medium text-slate-700 mb-1">名称</label>
                  <input
                    required
                    value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })}
                    className="w-full px-3 py-2 border rounded-lg"
                    placeholder="prod-s3"
                  />
                </div>
                <div>
                  <label className="block font-medium text-slate-700 mb-1">类型</label>
                  <select
                    disabled={Boolean(form.id)}
                    value={form.type}
                    onChange={(e) => setForm({ ...form, type: e.target.value as any })}
                    className="w-full px-3 py-2 border rounded-lg bg-white"
                  >
                    <option value="s3">Amazon S3 / 兼容对象存储</option>
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
                      <label className="block text-xs text-slate-600 mb-1">Bucket *</label>
                      <input required value={form.bucket} onChange={(e) => setForm({ ...form, bucket: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">Region</label>
                      <input value={form.region} onChange={(e) => setForm({ ...form, region: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">Endpoint（MinIO/R2/OSS 可选）</label>
                      <input value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} placeholder="https://s3.amazonaws.com" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">全局路径前缀</label>
                      <input value={form.base_prefix} onChange={(e) => setForm({ ...form, base_prefix: e.target.value })} placeholder="backups/" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">Access Key ID *</label>
                      <input required value={form.access_key_id} onChange={(e) => setForm({ ...form, access_key_id: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">Secret Access Key {form.id ? '（留空则保持不变）' : '*'}</label>
                      <input type="password" required={!form.id} value={form.secret_access_key} onChange={(e) => setForm({ ...form, secret_access_key: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div className="col-span-2">
                      <label className="block text-xs text-slate-600 mb-1">Storage Class（可选，如 STANDARD_IA / GLACIER）</label>
                      <input value={form.storage_class} onChange={(e) => setForm({ ...form, storage_class: e.target.value })} placeholder="STANDARD" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                  </div>
                  <label className="inline-flex items-center gap-2 text-xs">
                    <input type="checkbox" checked={form.force_path_style} onChange={(e) => setForm({ ...form, force_path_style: e.target.checked })} />
                    <span>Force Path Style（MinIO / 自建 S3 推荐开启）</span>
                  </label>
                </div>
              ) : (
                <div className="border-t pt-4 space-y-3">
                  <div className="font-semibold text-slate-800">Dropbox 参数</div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">App Key *</label>
                      <input required value={form.app_key} onChange={(e) => setForm({ ...form, app_key: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">App Secret {form.id ? '（留空不变）' : '*'}</label>
                      <input type="password" required={!form.id} value={form.app_secret} onChange={(e) => setForm({ ...form, app_secret: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">Dropbox 根目录</label>
                      <input value={form.root_path} onChange={(e) => setForm({ ...form, root_path: e.target.value })} placeholder="/safekeep" className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">Refresh Token（可选，也可保存后走 Web OAuth2 获取）</label>
                      <input type="password" value={form.refresh_token} onChange={(e) => setForm({ ...form, refresh_token: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
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
                        onClick={() => navigator.clipboard.writeText(`${window.location.origin}/admin/api/v1/oauth/dropbox/callback`)}
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
                      <label className="block text-xs text-slate-600 mb-1">代理协议</label>
                      <select value={form.proxy_type} onChange={(e) => setForm({ ...form, proxy_type: e.target.value as any })} className="w-full px-3 py-1.5 border rounded-lg bg-white">
                        <option value="socks5">SOCKS5</option>
                        <option value="http">HTTP</option>
                        <option value="https">HTTPS</option>
                      </select>
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">代理主机</label>
                      <input required value={form.proxy_host} onChange={(e) => setForm({ ...form, proxy_host: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">端口</label>
                      <input type="number" required min={1} max={65535} value={form.proxy_port} onChange={(e) => setForm({ ...form, proxy_port: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-600 mb-1">代理用户名（可选）</label>
                      <input value={form.proxy_username} onChange={(e) => setForm({ ...form, proxy_username: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                    </div>
                    <div className="col-span-2">
                      <label className="block text-xs text-slate-600 mb-1">代理密码（{form.id ? '留空保持不变' : '可选'}）</label>
                      <input type="password" disabled={form.clear_proxy_password} value={form.proxy_password} onChange={(e) => setForm({ ...form, proxy_password: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg disabled:bg-slate-100" />
                      {form.id && (
                        <label className="inline-flex items-center gap-1.5 text-[11px] text-slate-500 mt-1">
                          <input type="checkbox" checked={form.clear_proxy_password} onChange={(e) => setForm({ ...form, clear_proxy_password: e.target.checked, proxy_password: '' })} />
                          <span>清除已有代理密码</span>
                        </label>
                      )}
                    </div>
                  </div>
                )}
              </div>

              <div className="flex justify-end gap-2 pt-3 border-t">
                <button type="button" onClick={() => setModalOpen(false)} className="px-4 py-2 rounded-lg border text-slate-600 hover:bg-slate-50">
                  取消
                </button>
                <button type="submit" className="px-4 py-2 rounded-lg bg-indigo-600 hover:bg-indigo-700 text-white font-medium">
                  保存配置
                </button>
              </div>
            </form>
          </div>
        </div>
      )}
    </div>
  )
}
