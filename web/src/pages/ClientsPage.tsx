import React, { useEffect, useState } from 'react'
import { Check, ChevronDown, ChevronRight, Copy, KeyRound, Plus, RefreshCw, Terminal, Trash2 } from 'lucide-react'
import { EmptyState, LoadingState } from '../components/Feedback'
import { Modal } from '../components/Modal'
import { SecretField } from '../components/SecretField'
import { api, formatBytes, formatTime } from '../api'
import { StatusBadge } from '../components/Badge'
import { useToast } from '../components/Toast'
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
  const toast = useToast()
  const [clients, setClients] = useState<Client[]>([])
  const [upstreams, setUpstreams] = useState<Upstream[]>([])
  const [loading, setLoading] = useState(true)
  const [modalOpen, setModalOpen] = useState(false)
  const [form, setForm] = useState<ClientForm | null>(null)
  const [initialFormSnap, setInitialFormSnap] = useState('')
  const [saving, setSaving] = useState(false)
  const [revealedKey, setRevealedKey] = useState<string | null>(null)
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
      toast.error(err.message || '加载客户列表失败')
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
    setEditingHasWebhookSecret(Boolean(c.has_webhook_secret))
    setModalOpen(true)
  }

  const closeModal = () => {
    if (JSON.stringify(form) !== initialFormSnap && !confirm('客户配置尚未保存，确定放弃修改吗？')) return
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
        toast.success(`客户 "${form.name}" 配置已保存`)
      } else {
        const res = await api.createClient(payload)
        setRevealedKey(res.api_key)
        toast.success(`客户 "${form.name}" 已创建`)
      }
      setModalOpen(false)
      await load()
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const handleRotate = async (c: Client) => {
    if (!confirm(`确定轮换客户 "${c.name}" 的 API Key 吗？旧 Key 将立即失效。`)) return
    try {
      const res = await api.rotateClientKey(c.id)
      setRevealedKey(res.api_key)
      toast.success(`已为 "${c.name}" 生成新 API Key`)
      await load()
    } catch (err: any) {
      toast.error(err.message)
    }
  }

  const handleDelete = async (c: Client) => {
    if (!confirm(`确定删除客户 "${c.name}"？关联的备份任务记录也会被清除。`)) return
    try {
      await api.deleteClient(c.id)
      toast.success(`已删除客户 "${c.name}"`)
      await load()
    } catch (err: any) {
      toast.error(err.message)
    }
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

      {(() => {
        const baseUrl = typeof window !== 'undefined' ? window.location.origin : 'http://localhost:8333'
        const sampleKey = 'sk_your_api_key'
        const copyCmd = async (id: string, cmd: string) => {
          try {
            await copyText(cmd)
            setCopiedId(id)
            setTimeout(() => setCopiedId((cur) => (cur === id ? null : cur)), 1500)
          } catch (e: any) {
            toast.error(e.message || '复制失败')
          }
        }
        const examples = [
          {
            id: 'sync-upload' as const,
            label: '1. 同步上传',
            title: '同步上传（mode=sync，等待结果；超时自动转后台）',
            desc: '适合中小文件或需要立即确认备份结果的场景；超过系统同步超时时间后自动转为后台异步执行并返回 202。',
            cmd: `curl -X POST "${baseUrl}/api/v1/backups?mode=sync&path=daily" \\\n  -H "X-API-Key: ${sampleKey}" \\\n  -F "file=@./backup.tar.gz"`,
          },
          {
            id: 'async-upload' as const,
            label: '2. 异步上传',
            title: '异步上传并指定目标上游（省略 mode 则使用客户默认传输模式）',
            desc: '流式落盘后立即返回 202 Accepted 与 task_id；可通过 targets 指定该客户已绑定上游的子集。',
            cmd: `curl -X POST "${baseUrl}/api/v1/backups?mode=async&targets=prod-s3,dropbox" \\\n  -H "X-API-Key: ${sampleKey}" \\\n  -F "file=@./archive.zip" \\\n  -F 'metadata={"env":"prod","source":"cron"}'`,
          },
          {
            id: 'raw-stream' as const,
            label: '3. 原始二进制流上传',
            title: '原始二进制流上传（非 multipart，通过 Header 指定文件名）',
            desc: '直接将文件内容作为请求体流式上传，使用 X-File-Name 传递文件名（参数也可用 X-Backup-Mode / X-Backup-Path 请求头）。',
            cmd: `curl -X POST "${baseUrl}/api/v1/backups?mode=sync&path=logs" \\\n  -H "X-API-Key: ${sampleKey}" \\\n  -H "X-File-Name: app.log" \\\n  --data-binary @./app.log`,
          },
          {
            id: 'query-download' as const,
            label: '4. 查询与下载恢复',
            title: '查询任务列表 / 任务详情 / 下载恢复 / 查看可用上游',
            desc: '按客户 API Key 隔离查询自身任务记录与上游绑定列表，并可直接通过网关回源下载恢复文件。',
            cmd: `# 查询当前客户的任务列表\ncurl -H "X-API-Key: ${sampleKey}" "${baseUrl}/api/v1/backups?limit=20"\n\n# 查询单个任务状态\ncurl -H "X-API-Key: ${sampleKey}" "${baseUrl}/api/v1/backups/<task_id>"\n\n# 下载恢复文件（可选 ?upstream_id=... 指定回源上游）\ncurl -H "X-API-Key: ${sampleKey}" -o restored.bin "${baseUrl}/api/v1/backups/<task_id>/download"\n\n# 查看当前 Key 绑定的可用上游\ncurl -H "X-API-Key: ${sampleKey}" "${baseUrl}/api/v1/upstreams"`,
          },
        ]
        const activeExample = examples.find((ex) => ex.id === guideTab) || examples[0]

        return (
          <div className="bg-white rounded-xl border border-slate-200 overflow-hidden">
            <button
              type="button"
              onClick={() => setGuideOpen((v) => !v)}
              className="w-full px-4 py-3 flex items-center justify-between text-left hover:bg-slate-50 transition-colors"
            >
              <div className="flex items-center gap-2">
                <Terminal className="w-4 h-4 text-indigo-600" />
                <span className="text-sm font-semibold text-slate-800">API Key 调用说明（curl 示例）</span>
                <span className="text-xs text-slate-400">支持同步 / 异步上传、流式上传、查询与下载恢复</span>
              </div>
              {guideOpen ? (
                <ChevronDown className="w-4 h-4 text-slate-400 shrink-0" />
              ) : (
                <ChevronRight className="w-4 h-4 text-slate-400 shrink-0" />
              )}
            </button>

            {guideOpen && (
              <div className="px-4 pb-4 pt-3 border-t border-slate-100 space-y-3 text-xs">
                <div className="p-3 rounded-lg bg-slate-50 border border-slate-200 text-slate-600 space-y-1">
                  <div>
                    <span className="font-semibold text-slate-800">鉴权方式：</span>
                    请求头传 <code className="px-1 py-0.5 bg-white border rounded font-mono">X-API-Key: sk_...</code> 或{' '}
                    <code className="px-1 py-0.5 bg-white border rounded font-mono">Authorization: Bearer sk_...</code>。
                  </div>
                  <div>
                    <span className="font-semibold text-slate-800">常用参数：</span>
                    <code className="px-1 py-0.5 bg-white border rounded font-mono">mode=sync|async</code>（不传时使用客户的“默认传输模式”）、
                    <code className="px-1 py-0.5 bg-white border rounded font-mono">targets</code>（逗号分隔的上游名称或 ID，需在绑定列表内；留空走默认上游组）、
                    <code className="px-1 py-0.5 bg-white border rounded font-mono">path</code>（相对子目录）、
                    <code className="px-1 py-0.5 bg-white border rounded font-mono">dedup=false</code>（跳过 SHA-256 秒传去重）。
                  </div>
                </div>

                <div className="flex flex-wrap gap-1.5 border-b border-slate-200 pb-2">
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
                            : 'bg-slate-100 text-slate-600 hover:bg-slate-200 hover:text-slate-900'
                        }`}
                      >
                        {ex.label}
                      </button>
                    )
                  })}
                </div>

                <div className="space-y-2">
                  <div className="text-slate-600">{activeExample.desc}</div>
                  <div className="border border-slate-200 rounded-lg overflow-hidden bg-slate-900 text-slate-100">
                    <div className="flex items-center justify-between px-3 py-2 bg-slate-800 border-b border-slate-700">
                      <span className="text-[11px] font-medium text-slate-200">{activeExample.title}</span>
                      <button
                        type="button"
                        onClick={() => copyCmd(activeExample.id, activeExample.cmd)}
                        className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-slate-700 hover:bg-slate-600 text-[11px] text-white shrink-0"
                      >
                        {copiedId === activeExample.id ? (
                          <>
                            <Check className="w-3 h-3 text-emerald-400" />
                            <span>已复制</span>
                          </>
                        ) : (
                          <>
                            <Copy className="w-3 h-3" />
                            <span>复制</span>
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
        <LoadingState label="正在读取客户策略…" />
      ) : (
        <div className="bg-white rounded-xl border border-slate-200 overflow-x-auto">
          <table className="clients-table w-full text-sm">
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
                    <button aria-label={`删除客户 ${c.name}`} onClick={() => handleDelete(c)} className="text-xs text-rose-600 hover:underline inline-flex items-center">
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </td>
                </tr>
              ))}
              {clients.length === 0 && (
                <tr>
                  <td colSpan={7} className="text-center">
                    <EmptyState title="为你的应用创建访问凭证" description="为不同客户设置独立的备份策略、存储范围和 API Key。" action={<button className="primary-button" onClick={openNew}><Plus className="w-4 h-4" />创建客户与 API Key</button>} />
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {modalOpen && form && (
        <Modal title={form.id ? '编辑客户配置' : '创建客户与 API Key'} onClose={closeModal} busy={saving}>
          <div className="bg-white rounded-2xl max-w-2xl w-full max-h-[90vh] overflow-y-auto p-6">
            <h2 className="text-lg font-bold text-slate-900 mb-4">{form.id ? '编辑客户配置' : '创建客户与 API Key'}</h2>
            {error && <div className="mb-3 p-2.5 bg-rose-50 text-rose-700 text-xs rounded-lg">{error}</div>}
            <form onSubmit={handleSave} className="space-y-4 text-sm">
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label htmlFor="clients-field-1" className="block text-xs text-slate-600 mb-1">客户名称 *</label>
                  <input id="clients-field-1" required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-2" className="block text-xs text-slate-600 mb-1">默认传输模式</label>
                  <select id="clients-field-2" value={form.default_mode} onChange={(e) => setForm({ ...form, default_mode: e.target.value as any })} className="w-full px-3 py-1.5 border rounded-lg bg-white">
                    <option value="async">async（异步队列，推荐）</option>
                    <option value="sync">sync（同步等待，超时转后台）</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="clients-field-3" className="block text-xs text-slate-600 mb-1">租户隔离路径前缀</label>
                  <input id="clients-field-3" value={form.path_prefix} onChange={(e) => setForm({ ...form, path_prefix: e.target.value })} placeholder="tenant-acme" className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-4" className="block text-xs text-slate-600 mb-1">备注说明</label>
                  <input id="clients-field-4" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-5" className="block text-xs text-slate-600 mb-1">单文件大小上限 MB（0=跟随全局）</label>
                  <input id="clients-field-5" type="number" min={0} value={form.max_file_size_mb} onChange={(e) => setForm({ ...form, max_file_size_mb: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-6" className="block text-xs text-slate-600 mb-1">每分钟限流 RPM（0=不限）</label>
                  <input id="clients-field-6" type="number" min={0} value={form.rate_limit_rpm} onChange={(e) => setForm({ ...form, rate_limit_rpm: Number(e.target.value) })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-7" className="block text-xs text-slate-600 mb-1">异步回调 Webhook URL</label>
                  <input id="clients-field-7" value={form.webhook_url} onChange={(e) => setForm({ ...form, webhook_url: e.target.value })} placeholder="https://example.com/hook" className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
                <div>
                  <label htmlFor="clients-field-8" className="block text-xs text-slate-600 mb-1">Webhook 签名密钥 {form.id ? '（默认掩码 *****，点右侧可查看）' : ''}</label>
                  <SecretField
                    id="clients-field-8"
                    hasSaved={Boolean(form.id && editingHasWebhookSecret)}
                    value={form.webhook_secret}
                    onChange={(v) => setForm({ ...form, webhook_secret: v })}
                    onReveal={form.id ? async () => (await api.revealClientSecrets(form.id!)).webhook_secret || '' : undefined}
                  />
                </div>
                <div>
                  <label htmlFor="clients-field-9" className="block text-xs text-slate-600 mb-1">允许的 MIME 类型（逗号分隔，空=不限）</label>
                  <input id="clients-field-9" value={form.allowed_mime_types} onChange={(e) => setForm({ ...form, allowed_mime_types: e.target.value })} placeholder="image/*,application/pdf" className="w-full px-3 py-1.5 border rounded-lg font-mono text-xs" />
                </div>
                <div>
                  <label htmlFor="clients-field-10" className="block text-xs text-slate-600 mb-1">API Key 过期时间（可选）</label>
                  <input id="clients-field-10" type="datetime-local" value={form.expires_at} onChange={(e) => setForm({ ...form, expires_at: e.target.value })} className="w-full px-3 py-1.5 border rounded-lg" />
                </div>
              </div>

              <label className="inline-flex items-center gap-2">
                <input type="checkbox" checked={form.enabled} onChange={(e) => setForm({ ...form, enabled: e.target.checked })} />
                <span>启用该客户</span>
              </label>

              <div className="border-t pt-3">
                <div className="font-semibold text-slate-800 mb-2">绑定上游存储策略（允许范围 & 默认扇出组）</div>
                <div className="space-y-2">
                  {form.upstreams.length === 0 && <div className="p-3 rounded-xl bg-amber-50 text-amber-800 text-xs leading-6">尚未添加上游存储。可先保存客户，再前往「上游存储与代理」添加存储并绑定，之后即可上传备份。</div>}
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
                <button type="button" disabled={saving} onClick={closeModal} className="px-4 py-2 rounded-lg border text-slate-600 disabled:opacity-50">
                  取消
                </button>
                <button
                  type="submit"
                  disabled={saving || (Boolean(form.id) && !clientDirty)}
                  className="px-4 py-2 rounded-lg bg-indigo-600 hover:bg-indigo-700 disabled:opacity-45 disabled:cursor-not-allowed text-white font-medium"
                >
                  {saving ? '保存中...' : form.id && !clientDirty ? '无改动' : '保存'}
                </button>
              </div>
            </form>
          </div>
        </Modal>
      )}

      {revealedKey && (
        <Modal title="保存 API Key" onClose={() => setRevealedKey(null)} dismissible={false}>
          <div className="bg-white rounded-2xl max-w-lg w-full p-6 space-y-4">
            <div className="flex items-center gap-2 text-amber-700 font-semibold">
              <KeyRound className="w-5 h-5" />
              <span>请立即复制并妥善保存此 API Key</span>
            </div>
            <p className="text-xs text-slate-500">出于安全原因，服务端仅保存其 SHA-256 哈希，关闭窗口后将无法再次查看完整明文。</p>
            <div className="flex items-center gap-2 bg-slate-900 text-emerald-400 p-3 rounded-lg font-mono text-xs break-all">
              <span className="flex-1 min-w-0 select-all">{revealedKey}</span>
              <button
                onClick={async () => {
                  try {
                    await copyText(revealedKey)
                    toast.success('已复制 API Key')
                  } catch (e: any) {
                    toast.error(e.message || '复制失败')
                  }
                }}
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
        </Modal>
      )}
    </div>
  )
}
