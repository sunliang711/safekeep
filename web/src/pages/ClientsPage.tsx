import React, { useEffect, useState } from 'react'
import { Copy, KeyRound, Plus, RefreshCw, Trash2 } from 'lucide-react'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
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
  const [clients, setClients] = useState<Client[]>([])
  const [upstreams, setUpstreams] = useState<Upstream[]>([])
  const [loading, setLoading] = useState(true)
  const [modalOpen, setModalOpen] = useState(false)
  const [form, setForm] = useState<ClientForm | null>(null)
  const [revealedKey, setRevealedKey] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [pageErr, setPageErr] = useState('')

  const load = async () => {
    setLoading(true)
    setPageErr('')
    try {
      const [cRes, uRes] = await Promise.all([api.listClients(), api.listUpstreams()])
      setClients(cRes.items || [])
      setUpstreams(uRes.items || [])
    } catch (err: any) {
      setPageErr(err.message || '加载客户列表失败')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
  }, [])

  const openNew = () => {
    setError('')
    setForm({
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
    })
    setModalOpen(true)
  }

  const openEdit = (c: Client) => {
    setError('')
    const boundMap = new Map(c.upstreams.map((u) => [u.upstream_id, u]))
    setForm({
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
    })
    setModalOpen(true)
  }

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!form) return
    setError('')
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
      } else {
        const res = await api.createClient(payload)
        setRevealedKey(res.api_key)
      }
      setModalOpen(false)
      await load()
    } catch (err: any) {
      setError(err.message)
    }
  }

  const handleRotate = async (c: Client) => {
    if (!confirm(`确定轮换客户 "${c.name}" 的 API Key 吗？旧 Key 将立即失效。`)) return
    try {
      const res = await api.rotateClientKey(c.id)
      setRevealedKey(res.api_key)
      await load()
    } catch (err: any) {
      alert(err.message)
    }
  }

  const handleDelete = async (c: Client) => {
    if (!confirm(`确定删除客户 "${c.name}"？关联的备份任务记录也会被清除。`)) return
    await api.deleteClient(c.id)
    await load()
  }

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-bold text-slate-900">客户与 API Key 管理</h1>
          <p className="text-sm text-slate-500">为不同客户分配独立 API Key，绑定默认上游策略组与允许子集</p>
        </div>
        <button
          onClick={openNew}
          className="inline-flex items-center gap-1.5 px-3.5 py-2 bg-indigo-600 hover:bg-indigo-700 text-white rounded-lg text-sm font-medium"
        >
          <Plus className="w-4 h-4" /> 创建客户 & Key
        </button>
      </div>

      {pageErr && <div className="p-3 bg-rose-50 border border-rose-200 text-rose-700 rounded-lg text-sm">{pageErr}</div>}

      {loading ? (
        <div className="text-sm text-slate-500">加载中...</div>
      ) : (
        <div className="bg-white rounded-xl border border-slate-200 overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 text-xs text-slate-500 border-b">
              <tr>
                <th className="text-left px-4 py-3">客户名称</th>
                <th className="text-left px-4 py-3">Key 前缀</th>
                <th className="text-left px-4 py-3">默认模式 / 前缀</th>
                <th className="text-left px-4 py-3">绑定上游</th>
                <th className="text-left px-4 py-3">配额 / 限流</th>
                <th className="text-left px-4 py-3">状态 / 最近使用</th>
                <th className="text-right px-4 py-3">操作</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {clients.map((c) => (
                <tr key={c.id}>
                  <td className="px-4 py-3">
                    <div className="font-medium text-slate-900">{c.name}</div>
                    {c.description && <div className="text-xs text-slate-400">{c.description}</div>}
                  </td>
                  <td className="px-4 py-3 font-mono text-xs text-slate-700">{c.key_prefix}...</td>
                  <td className="px-4 py-3">
                    <StatusBadge value={c.default_mode} />
                    {c.path_prefix && <div className="text-xs text-slate-500 font-mono mt-1">/{c.path_prefix}</div>}
                  </td>
                  <td className="px-4 py-3">
                    <div className="flex flex-wrap gap-1">
                      {c.upstreams.map((u) => (
                        <span
                          key={u.upstream_id}
                          className={`text-xs px-2 py-0.5 rounded border ${
                            u.is_default
                              ? 'bg-indigo-50 text-indigo-700 border-indigo-200'
                              : 'bg-slate-50 text-slate-600 border-slate-200'
                          }`}
                        >
                          {u.upstream_name} {u.is_default ? '(默认)' : '(可选)'}
                        </span>
                      ))}
                    </div>
                  </td>
                  <td className="px-4 py-3 text-xs text-slate-600">
                    <div>单文件: {c.max_file_size_bytes > 0 ? formatBytes(c.max_file_size_bytes) : '全局默认'}</div>
                    <div>限流: {c.rate_limit_rpm > 0 ? `${c.rate_limit_rpm} req/m` : '不限'}</div>
                  </td>
                  <td className="px-4 py-3">
                    <StatusBadge value={c.enabled ? 'enabled' : 'disabled'} label={c.enabled ? '启用' : '禁用'} />
                    <div className="text-[11px] text-slate-400 mt-1">{formatTime(c.last_used_at)}</div>
                  </td>
                  <td className="px-4 py-3 text-right space-x-2 whitespace-nowrap">
                    <button onClick={() => openEdit(c)} className="text-xs text-indigo-600 hover:underline">
                      编辑
                    </button>
                    <button onClick={() => handleRotate(c)} className="text-xs text-amber-600 hover:underline inline-flex items-center gap-0.5">
                      <RefreshCw className="w-3 h-3" /> 轮换 Key
                    </button>
                    <button onClick={() => handleDelete(c)} className="text-xs text-rose-600 hover:underline inline-flex items-center">
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </td>
                </tr>
              ))}
              {clients.length === 0 && (
                <tr>
                  <td colSpan={7} className="px-4 py-8 text-center text-slate-400">
                    暂无客户，点击右上角创建第一个 API Key。
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {modalOpen && form && (
        <div className="fixed inset-0 bg-black/40 flex items-center justify-center p-4 z-50">
          <div className="bg-white rounded-2xl max-w-2xl w-full max-h-[90vh] overflow-y-auto p-6">
            <h2 className="text-lg font-bold text-slate-900 mb-4">{form.id ? '编辑客户配置' : '创建客户与 API Key'}</h2>
            {error && <div className="mb-3 p-2.5 bg-rose-50 text-rose-700 text-xs rounded-lg">{error}</div>}
            <form onSubmit={handleSave} className="space-y-4 text-sm">
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-slate-600 mb-1">客户名称 *</label>
                  <input required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">默认传输模式</label>
                  <select value={form.default_mode} onChange={(e) => setForm({ ...form, default_mode: e.target.value as any })} className="w-full px-3 py-1.5 border rounded-lg bg-white">
                    <option value="async">async（异步队列，推荐）</option>
                    <option value="sync">sync（同步等待，超时转后台）</option>
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">租户隔离路径前缀</label>
                  <input value={form.path_prefix} onChange={(e) => setForm({ ...form, path_prefix: e.target.value })} placeholder="tenant-acme" className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">备注说明</label>
                  <input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">单文件大小上限 MB（0=跟随全局）</label>
                  <input type="number" min={0} value={form.max_file_size_mb} onChange={(e) => setForm({ ...form, max_file_size_mb: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">每分钟限流 RPM（0=不限）</label>
                  <input type="number" min={0} value={form.rate_limit_rpm} onChange={(e) => setForm({ ...form, rate_limit_rpm: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">异步回调 Webhook URL</label>
                  <input value={form.webhook_url} onChange={(e) => setForm({ ...form, webhook_url: e.target.value })} placeholder="https://example.com/hook" className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">Webhook 签名密钥 {form.id ? '（留空不变）' : ''}</label>
                  <input type="password" value={form.webhook_secret} onChange={(e) => setForm({ ...form, webhook_secret: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">允许的 MIME 类型（逗号分隔，空=不限）</label>
                  <input value={form.allowed_mime_types} onChange={(e) => setForm({ ...form, allowed_mime_types: e.target.value })} placeholder="image/*,application/pdf" className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
                </div>
                <div>
                  <label className="block text-xs text-slate-600 mb-1">API Key 过期时间（可选）</label>
                  <input type="datetime-local" value={form.expires_at} onChange={(e) => setForm({ ...form, expires_at: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
              </div>

              <label className="inline-flex items-center gap-2">
                <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
                <span>启用该客户</span>
              </label>

              <div className="border-t pt-3">
                <div className="font-semibold text-slate-800 mb-2">绑定上游存储策略（允许范围 & 默认扇出组）</div>
                <div className="space-y-2">
                  {form.upstreams.map((uCfg, idx) => {
                    const meta = upstreams.find((x) => x.id === uCfg.upstream_id)
                    if (!meta) return null
                    return (
                      <div key={meta.id} className="flex items-center justify-between p-2.5 rounded-lg bg-slate-50 border text-xs">
                        <label className="flex items-center gap-2 font-medium">
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
                          <div className="flex items-center gap-4">
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
                              <span>加入默认组</span>
                            </label>
                            <label className="flex items-center gap-1">
                              <span>回源优先级</span>
                              <input
                                type="number"
                                className="w-16 px-2 py-0.5 border rounded bg-white"
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

              <div className="flex justify-end gap-2 pt-3 border-t">
                <button type="button" onClick={() => setModalOpen(false)} className="px-4 py-2 rounded-lg border text-slate-600">
                  取消
                </button>
                <button type="submit" className="px-4 py-2 rounded-lg bg-indigo-600 text-white font-medium">
                  保存
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {revealedKey && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
          <div className="bg-white rounded-2xl max-w-lg w-full p-6 space-y-4">
            <div className="flex items-center gap-2 text-amber-700 font-semibold">
              <KeyRound className="w-5 h-5" />
              <span>请立即复制并妥善保存此 API Key</span>
            </div>
            <p className="text-xs text-slate-500">出于安全原因，服务端仅保存其 SHA-256 哈希，关闭窗口后将无法再次查看完整明文。</p>
            <div className="flex items-center gap-2 bg-slate-900 text-emerald-400 p-3 rounded-lg font-mono text-xs break-all">
              <span className="flex-1">{revealedKey}</span>
              <button
                onClick={() => navigator.clipboard.writeText(revealedKey)}
                className="p-1.5 bg-slate-800 hover:bg-slate-700 rounded text-white"
                title="复制"
              >
                <Copy className="w-4 h-4" />
              </button>
            </div>
            <div className="flex justify-end">
              <button onClick={() => setRevealedKey(null)} className="px-4 py-2 bg-indigo-600 text-white rounded-lg text-sm">
                我已保存
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
