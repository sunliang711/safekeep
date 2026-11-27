import React, { useEffect, useRef, useState } from 'react'
import { Copy, Loader2, Plus, RefreshCw, Server, ShieldCheck } from 'lucide-react'
import { api, formatBytes, formatTime, isReauthenticating } from '../api'
import { StatusBadge } from '../components/Badge'
import { Modal } from '../components/Modal'
import { RemoteRunProgress } from '../components/RemoteRunProgress'
import { useToast } from '../components/Toast'
import { useI18n } from '../contexts/I18nContext'
import { copyText } from '../utils/clipboard'
import { CRON_PRESETS, describeCron, formatCronPresetLabel, matchCronPreset } from '../utils/cron'
import type { RemoteHost, RemoteJob, RemoteRun, Upstream } from '../types'

type HostForm = { id?: string; name: string; address: string; port: number; username: string }
type JobForm = Omit<RemoteJob, 'id'> & { id?: string }

export function RemoteBackupsPage() {
  const { t, lang } = useI18n()
  const toast = useToast()
  const [hosts, setHosts] = useState<RemoteHost[]>([])
  const [jobs, setJobs] = useState<RemoteJob[]>([])
  const [runs, setRuns] = useState<RemoteRun[]>([])
  const [upstreams, setUpstreams] = useState<Upstream[]>([])
  const [tab, setTab] = useState<'jobs' | 'hosts' | 'runs'>('jobs')
  const [hostForm, setHostForm] = useState<HostForm | null>(null)
  const [jobForm, setJobForm] = useState<JobForm | null>(null)
  const [preset, setPreset] = useState('custom')
  const [preview, setPreview] = useState<{ schedule: string; times: string[]; error: string; loading: boolean }>({ schedule: '', times: [], error: '', loading: false })
  const [guide, setGuide] = useState<RemoteHost | null>(null)
  const [detailID, setDetailID] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<{ kind: 'hosts' | 'jobs'; id: string; name: string } | null>(null)
  const [busy, setBusy] = useState(false)
  const [startingID, setStartingID] = useState<string | null>(null)
  const [runPollError, setRunPollError] = useState(false)
  const runRequestSeq = useRef(0)
  const [checking, setChecking] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const detail = runs.find(run => run.id === detailID)
  const previewSchedule = jobForm ? `CRON_TZ=${jobForm.timezone.trim()} ${jobForm.cron_expr.trim()}` : ''
  const previewLoading = !!jobForm && (preview.schedule !== previewSchedule || preview.loading)
  const previewError = preview.schedule === previewSchedule ? preview.error : ''
  const previewTimes = preview.schedule === previewSchedule ? preview.times : []
  const scheduleTime = (raw: string, timezone: string) => {
    try { return new Date(raw).toLocaleString(lang === 'en' ? 'en-US' : 'zh-CN', { hour12: false, timeZone: timezone }) }
    // Go 支持的部分时区名称无法被浏览器识别，此时保留带偏移量的原始时间。
    catch { return raw }
  }

  const load = async () => {
    const runSeq = ++runRequestSeq.current
    try {
      const [h, j, r, u] = await Promise.all([api.listRemoteHosts(), api.listRemoteJobs(), api.listRemoteRuns(), api.listUpstreams()])
      setHosts(h.items); setJobs(j.items); setUpstreams(u.items)
      if (runSeq === runRequestSeq.current) setRuns(previous => {
        const selected = previous.find(run => run.id === detailID)
        return selected && !r.items.some(run => run.id === selected.id) ? [...r.items, selected] : r.items
      })
    } catch (e: any) { toast.error(e.message) } finally { setLoading(false) }
  }
  useEffect(() => { void load() }, [])
  useEffect(() => {
    if (!jobs.some(job => job.enabled)) return
    const timer = window.setInterval(() => {
      if (isReauthenticating()) return
      void api.listRemoteJobs().then(j => setJobs(j.items)).catch(() => {})
    }, 30000)
    return () => window.clearInterval(timer)
  }, [jobs.some(job => job.enabled)])
  useEffect(() => {
    if (!jobForm) return
    let cancelled = false
    const validationError = !jobForm.cron_expr.trim() ? t('cron.errEmptyExpr') : !jobForm.timezone.trim() ? t('remote.invalidTimezone') : ''
    setPreview({ schedule: previewSchedule, times: [], error: validationError, loading: !validationError })
    if (validationError) return
    // 防抖预览并丢弃过期响应，确保切换表达式或时区后只显示当前计划。
    const timer = window.setTimeout(async () => {
      try {
        const result = await api.previewCronSchedule(previewSchedule)
        if (!cancelled) setPreview({ schedule: previewSchedule, times: result.next_runs || [], error: '', loading: false })
      } catch (e: any) {
        if (!cancelled) setPreview({ schedule: previewSchedule, times: [], error: e.message || t('cron.errInvalidExpr'), loading: false })
      }
    }, 180)
    return () => { cancelled = true; window.clearTimeout(timer) }
  }, [!!jobForm, previewSchedule, t])
  useEffect(() => {
    if (!runs.some(run => run.status === 'running')) return
    let cancelled = false
    let timer: number
    // 等待上一次请求完成后再轮询，避免慢响应覆盖新进度。
    const refresh = async () => {
      const runSeq = ++runRequestSeq.current
      try {
        const response = await api.listRemoteRuns()
        if (detailID && !response.items.some(run => run.id === detailID)) response.items.push(await api.getRemoteRun(detailID))
        if (!cancelled && runSeq === runRequestSeq.current) { setRuns(response.items); setRunPollError(false) }
      } catch { if (!cancelled && runSeq === runRequestSeq.current) setRunPollError(true) }
      if (!cancelled) timer = window.setTimeout(refresh, 1000)
    }
    timer = window.setTimeout(refresh, 1000)
    return () => { cancelled = true; window.clearTimeout(timer) }
  }, [runs.some(run => run.status === 'running'), detailID])

  const action = async (fn: () => Promise<unknown>, message?: string) => {
    setBusy(true); setError('')
    try { await fn(); if (message) toast.success(message); await load() }
    catch (e: any) { setError(e.message); toast.error(e.message) }
    finally { setBusy(false) }
  }
  const startJob = async (job: RemoteJob) => {
    setStartingID(job.id)
    await action(async () => {
      const run = await api.runRemoteJob(job.id)
      runRequestSeq.current++
      setRuns(previous => [run, ...previous.filter(item => item.id !== run.id)])
      setRunPollError(false); setDetailID(run.id); setTab('runs')
    }, t('remote.started'))
    setStartingID(null)
  }
  const newJob = () => {
    setError('')
    setPreset('0 0 * * *')
    setJobForm({ name: '', host_id: hosts[0]?.id || '', directories: [{ path: '', excludes: [] }], upstream_ids: [], parent_path: hosts[0]?.name || '', cron_expr: '0 0 * * *', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC', enabled: true, timeout_sec: 3600 })
  }
  const field = (label: string, input: React.ReactNode) => <label className="block text-sm font-medium space-y-2"><span>{label}</span>{input}</label>
  const hostName = (id: string) => hosts.find(host => host.id === id)?.name || id
  const encodePath = (source: string) => source.replace(/[~!\\:/]/g, c => ({ '~': '~~', '!': '~!', '\\': '~^', ':': '~;', '/': '!' }[c]!))

  return <div className="space-y-6">
    <div className="flex flex-wrap items-center justify-between gap-4">
      <div><h1 className="text-2xl font-semibold flex items-center gap-3"><Server className="w-6 h-6 text-indigo-500" />{t('nav.remoteBackups')}</h1><p className="text-sm text-slate-500 mt-2">{t('remote.subtitle')}</p></div>
      <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" onClick={() => void load()}><RefreshCw className="w-4 h-4" />{t('common.refresh')}</button>
    </div>
    <div className="flex flex-wrap gap-2" role="tablist" aria-label={t('nav.remoteBackups')}>
      {(['jobs', 'hosts', 'runs'] as const).map(value => <button key={value} role="tab" aria-selected={tab === value} className={tab === value ? 'primary-button' : 'inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50'} onClick={() => setTab(value)}>{t(`remote.${value}`)}</button>)}
    </div>
    {loading ? <p role="status">{t('common.loading')}</p> : <>
      {tab === 'hosts' && <div className="space-y-4">
        <div className="flex justify-end"><button className="primary-button" onClick={() => { setError(''); setHostForm({ name: '', address: '', port: 22, username: '' }) }}><Plus className="w-4 h-4" />{t('remote.addHost')}</button></div>
        {!hosts.length && <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-10 text-center text-slate-500">{t('remote.emptyHosts')}</div>}
        {hosts.map(host => <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5 space-y-3" key={host.id}>
          <div className="flex flex-wrap items-center justify-between gap-3"><div className="flex items-center gap-3"><h2 className="font-semibold">{host.name}</h2><StatusBadge value={host.health_status} /></div><div className="flex flex-wrap gap-2">
            <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" onClick={() => setGuide(host)}>{t('remote.deployKey')}</button>
            <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" disabled={checking !== null} onClick={async () => { setChecking(host.id); try { const checked = await api.checkRemoteHost(host.id); setHosts(prev => prev.map(h => h.id === checked.id ? checked : h)); checked.health_status === 'ok' ? toast.success(t('remote.checkPassed')) : toast.error(checked.health_error) } catch (e: any) { toast.error(e.message) } finally { setChecking(null) } }}><ShieldCheck className="w-4 h-4" />{checking === host.id ? t('remote.checking') : t('remote.check')}</button>
            <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" onClick={() => { setError(''); setHostForm(host) }}>{t('common.edit')}</button>
            <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50 text-rose-500" onClick={() => setDeleting({ kind: 'hosts', id: host.id, name: host.name })}>{t('common.delete')}</button>
          </div></div>
          <p className="font-mono text-sm break-all">{host.username}@{host.address}:{host.port}</p>
          <p className="text-xs text-slate-500">{t('remote.tool')}: {host.archive_tool || '—'} · {host.platform || '—'} · {t('remote.checkedAt')}: {formatTime(host.checked_at)}</p>
          {host.fingerprint && <p className="text-xs font-mono break-all">{host.fingerprint}</p>}
          {host.health_error && <p className="text-sm text-rose-500 whitespace-pre-wrap break-words">{host.health_error}</p>}
        </div>)}
      </div>}
      {tab === 'jobs' && <div className="space-y-4">
        <div className="flex justify-end"><button className="primary-button" disabled={!hosts.length} onClick={newJob}><Plus className="w-4 h-4" />{t('remote.addJob')}</button></div>
        {!jobs.length && <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-10 text-center space-y-3 text-slate-500"><p>{hosts.length ? t('remote.emptyJobs') : t('remote.emptyHosts')}</p>{!hosts.length && <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" onClick={() => setTab('hosts')}>{t('remote.addHost')}</button>}</div>}
        {jobs.map(job => <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 p-5 space-y-3" key={job.id}>
          <div className="flex flex-wrap justify-between items-center gap-3"><div className="flex gap-3 items-center"><h2 className="font-semibold">{job.name}</h2><StatusBadge value={job.enabled ? 'enabled' : 'disabled'} /></div><div className="flex flex-wrap gap-2">
            <button className="primary-button" disabled={busy || runs.some(run => run.job_id === job.id && run.status === 'running')} onClick={() => void startJob(job)}>{startingID === job.id ? <><Loader2 className="w-4 h-4 animate-spin" />{t('remote.starting')}</> : runs.some(run => run.job_id === job.id && run.status === 'running') ? t('cron.running') : t('remote.run')}</button>
            <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" onClick={() => { setError(''); setPreset(matchCronPreset(job.cron_expr)); setJobForm(JSON.parse(JSON.stringify(job))) }}>{t('common.edit')}</button>
            <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50 text-rose-500" onClick={() => setDeleting({ kind: 'jobs', id: job.id, name: job.name })}>{t('common.delete')}</button>
          </div></div>
          <p className="text-sm text-slate-500">{hostName(job.host_id)} · <span className="font-mono">{job.cron_expr}</span> · {job.timezone}</p>
          {describeCron(job.cron_expr, lang) && <p className="text-xs text-slate-500">{describeCron(job.cron_expr, lang)}</p>}
          <p className="text-xs text-slate-500">{t('cron.colNextRun')}: {job.enabled ? job.next_run_at ? `${scheduleTime(job.next_run_at, job.timezone)} (${job.timezone})` : '—' : t('cron.statusPaused')}</p>
          {runs.find(run => run.job_id === job.id && run.status === 'running') && <RemoteRunProgress run={runs.find(run => run.job_id === job.id && run.status === 'running')!} compact />}
          <div className="text-sm space-y-1">{job.directories.map(dir => <p key={dir.path} className="font-mono break-all">{dir.path}</p>)}</div>
          <p className="text-xs text-slate-500 break-all">{t('remote.destination')}: {job.parent_path}/YYYY/MM/ · {job.upstream_ids.map(id => upstreams.find(u => u.id === id)?.name || id).join(', ')}</p>
          {(() => { const last = runs.find(run => run.job_id === job.id); return last ? <button className="text-sm text-indigo-500 flex items-center gap-2" onClick={() => { setDetailID(last.id); setTab('runs') }}><StatusBadge value={last.status} />{formatTime(last.started_at)}</button> : null })()}
        </div>)}
      </div>}
      {tab === 'runs' && <div className="bg-white dark:bg-slate-900 rounded-xl border border-slate-200 dark:border-slate-800 overflow-x-auto">
        <table className="w-full text-sm text-left"><thead><tr className="border-b border-slate-200 dark:border-slate-700">{[t('remote.jobName'), t('remote.startedAt'), t('remote.result'), t('remote.directories'), t('common.details')].map(label => <th key={label} className="p-4 font-medium">{label}</th>)}</tr></thead><tbody>
          {runs.map(run => <tr key={run.id} className="border-b border-slate-100 dark:border-slate-800"><td className="p-4">{run.job_name}</td><td className="p-4 whitespace-nowrap">{formatTime(run.started_at)}</td><td className="p-4 space-y-2"><StatusBadge value={run.status} />{run.status === 'running' && <RemoteRunProgress run={run} compact />}</td><td className="p-4">{run.archives.length}</td><td className="p-4"><button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" onClick={() => setDetailID(run.id)}>{t('common.details')}</button></td></tr>)}
          {!runs.length && <tr><td colSpan={5} className="p-10 text-center text-slate-500">{t('remote.emptyRuns')}</td></tr>}
        </tbody></table>
        {runPollError && <p role="alert" className="p-4 text-sm text-amber-600">{t('remote.progress.refreshFailed')}</p>}
      </div>}
    </>}
    {hostForm && <Modal title={t('remote.hostConfig')} busy={busy} onClose={() => setHostForm(null)}>
      <form className="p-6 space-y-4 bg-white dark:bg-slate-900 rounded-2xl max-h-[calc(100dvh-40px)] overflow-y-auto" onSubmit={e => { e.preventDefault(); void action(async () => { const h = await api.saveRemoteHost(hostForm.id, hostForm); setHostForm(null); if (!hostForm.id) setGuide(h) }, t('common.saved')) }}>
        <h2 className="text-lg font-semibold">{t('remote.hostConfig')}</h2>
        {field(t('remote.name'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" required value={hostForm.name} onChange={e => setHostForm({ ...hostForm, name: e.target.value })} />)}
        {field(t('remote.address'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" required value={hostForm.address} onChange={e => setHostForm({ ...hostForm, address: e.target.value })} placeholder="192.168.1.10" />)}
        <div className="grid grid-cols-2 gap-4">{field(t('remote.port'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" required type="number" min="1" max="65535" value={hostForm.port} onChange={e => setHostForm({ ...hostForm, port: Number(e.target.value) })} />)}{field(t('remote.username'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" required value={hostForm.username} onChange={e => setHostForm({ ...hostForm, username: e.target.value })} />)}</div>
        {error && <p role="alert" className="text-sm text-rose-500">{error}</p>}
        <div className="flex justify-end gap-2"><button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" type="button" disabled={busy} onClick={() => setHostForm(null)}>{t('common.cancel')}</button><button className="primary-button" disabled={busy}>{busy ? t('common.saving') : t('remote.saveHost')}</button></div>
      </form>
    </Modal>}
    {guide && <Modal title={t('remote.deployKey')} onClose={() => setGuide(null)}>
      <div className="p-6 space-y-4 bg-white dark:bg-slate-900 rounded-2xl max-h-[calc(100dvh-40px)] overflow-y-auto"><h2 className="text-lg font-semibold">{t('remote.deployKey')} · {guide.name}</h2><p className="text-sm">{t('remote.deployIntro', { user: guide.username })}</p>
        <pre className="p-3 rounded-lg bg-slate-100 dark:bg-slate-800 text-xs whitespace-pre-wrap break-all">{guide.public_key}</pre>
        <button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" onClick={() => void copyText(guide.public_key).then(() => toast.success(t('common.copied'))).catch(e => toast.error(e.message))}><Copy className="w-4 h-4" />{t('remote.copyPublicKey')}</button>
        <p className="text-sm">{t('remote.posixGuide')}</p><pre className="p-3 rounded-lg bg-slate-100 dark:bg-slate-800 text-xs whitespace-pre-wrap">{`mkdir -p ~/.ssh\nchmod 700 ~/.ssh\n# ${t('remote.appendKey')}\nchmod 600 ~/.ssh/authorized_keys`}</pre>
        <p className="text-sm whitespace-pre-wrap">{t('remote.windowsGuide')}</p><p className="text-xs text-slate-500">{t('remote.trustGuide')}</p>
        <div className="flex justify-end"><button className="primary-button" onClick={() => { setGuide(null); setTab('hosts') }}>{t('common.close')}</button></div>
      </div>
    </Modal>}
    {jobForm && <Modal title={t('remote.jobConfig')} size="xl" busy={busy} onClose={() => setJobForm(null)}>
      <form className="p-6 space-y-4 bg-white dark:bg-slate-900 rounded-2xl max-h-[calc(100dvh-40px)] overflow-y-auto" onSubmit={e => { e.preventDefault(); if (previewLoading || previewError) return; void action(async () => { await api.saveRemoteJob(jobForm.id, { ...jobForm, directories: jobForm.directories.map(dir => ({ ...dir, excludes: dir.excludes.filter(pattern => pattern !== '') })) }); setJobForm(null) }, t('common.saved')) }}>
        <h2 className="text-lg font-semibold">{t('remote.jobConfig')}</h2>
        <div className="grid sm:grid-cols-2 gap-4">{field(t('remote.jobName'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" required value={jobForm.name} onChange={e => setJobForm({ ...jobForm, name: e.target.value })} />)}{field(t('remote.host'), <select className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" required value={jobForm.host_id} onChange={e => setJobForm({ ...jobForm, host_id: e.target.value })}>{hosts.map(host => <option key={host.id} value={host.id}>{host.name}</option>)}</select>)}</div>
        <fieldset className="space-y-3"><legend className="font-medium text-sm mb-2">{t('remote.directories')}</legend>{jobForm.directories.map((dir, index) => <div key={index} className="rounded-xl border border-slate-200 dark:border-slate-700 p-3 space-y-3">
          <div className="flex gap-2 items-end flex-wrap"><div className="flex-1 min-w-0">{field(t('remote.sourcePath'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full font-mono" required value={dir.path} onChange={e => setJobForm({ ...jobForm, directories: jobForm.directories.map((d, i) => i === index ? { ...d, path: e.target.value } : d) })} placeholder="/var/lib/mysql" />)}</div><button type="button" className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" disabled={jobForm.directories.length === 1} onClick={() => setJobForm({ ...jobForm, directories: jobForm.directories.filter((_, i) => i !== index) })}>{t('common.delete')}</button></div>
          {field(t('remote.excludes'), <textarea className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full font-mono text-sm" rows={3} value={dir.excludes.join('\n')} onChange={e => setJobForm({ ...jobForm, directories: jobForm.directories.map((d, i) => i === index ? { ...d, excludes: e.target.value.split('\n') } : d) })} placeholder={'*.log\n./cache\n./node_modules'} />)}
        </div>)}<p className="text-xs text-slate-500">{t('remote.excludeHelp')} {t('remote.pathLimit')}</p><button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" type="button" onClick={() => setJobForm({ ...jobForm, directories: [...jobForm.directories, { path: '', excludes: [] }] })}><Plus className="w-4 h-4" />{t('remote.addDirectory')}</button></fieldset>
        {field(t('remote.parentPath'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" required value={jobForm.parent_path} onChange={e => setJobForm({ ...jobForm, parent_path: e.target.value })} placeholder="vm001" />)}
        <p className="text-xs font-mono text-slate-500 break-all">{jobForm.parent_path || 'vm001'}/YYYY/MM/{encodePath(jobForm.directories[0]?.path || '/var/lib/mysql')}_&lt;{t('remote.runID')}&gt;.tar.gz</p>
        <fieldset><legend className="font-medium text-sm mb-2">{t('remote.upstreams')}</legend><div className="flex flex-wrap gap-4">{upstreams.filter(u => u.enabled).map(upstream => <label className="flex items-center gap-2 text-sm" key={upstream.id}><input type="checkbox" checked={jobForm.upstream_ids.includes(upstream.id)} onChange={e => setJobForm({ ...jobForm, upstream_ids: e.target.checked ? [...jobForm.upstream_ids, upstream.id] : jobForm.upstream_ids.filter(id => id !== upstream.id) })} />{upstream.name}</label>)}</div>{!upstreams.some(u => u.enabled) && <p className="text-sm text-amber-600">{t('remote.noUpstreams')}</p>}</fieldset>
        <div className="grid sm:grid-cols-2 gap-4">
          {field(t('cron.fieldSchedule'), <select className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" value={preset} onChange={e => { setPreset(e.target.value); if (e.target.value !== 'custom') setJobForm({ ...jobForm, cron_expr: e.target.value }) }}>{CRON_PRESETS.map(option => <option key={option.value} value={option.value}>{formatCronPresetLabel(option, t(option.labelKey), lang)}</option>)}</select>)}
          {field(t('remote.timezone'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" required value={jobForm.timezone} onChange={e => setJobForm({ ...jobForm, timezone: e.target.value })} placeholder="Asia/Singapore" />)}
        </div>
        {preset === 'custom' && <div>
          {field(t('remote.schedule'), <input className={`border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full font-mono ${previewError ? 'border-rose-400' : ''}`} required value={jobForm.cron_expr} onChange={e => setJobForm({ ...jobForm, cron_expr: e.target.value })} aria-invalid={!!previewError} />)}
          <p className="text-xs text-slate-400 mt-1">{t('cron.fieldExprHint')}<span className="font-mono">0 0 * * *</span></p>
        </div>}
        <div aria-live="polite" aria-busy={previewLoading}>
          <div className="flex flex-wrap items-center gap-2 mb-2"><span className="text-sm font-medium">{t('cron.nextRuns')}</span><span className="text-xs text-slate-500">{jobForm.timezone}</span>{previewLoading && <Loader2 className="w-3 h-3 animate-spin text-slate-400" />}</div>
          {previewError ? <p role="alert" className="text-xs text-rose-500">{previewError}</p> : <div className="rounded-lg bg-slate-50 dark:bg-slate-800/60 border border-slate-200/70 dark:border-slate-700 px-3 py-2 space-y-1">{previewTimes.length ? previewTimes.map(raw => <div key={raw} className="text-xs font-mono text-slate-500 dark:text-slate-400">{scheduleTime(raw, jobForm.timezone.trim())}</div>) : <p className="text-xs text-slate-400">{previewLoading ? t('common.loading') : '—'}</p>}</div>}
          {!jobForm.enabled && <p className="text-xs text-slate-500 mt-1">{t('remote.pausedPreview')}</p>}
        </div>
        {field(t('remote.timeout'), <input className="border rounded-lg px-3 py-2 bg-white dark:bg-slate-900 w-full" type="number" min="1" max="86400" required value={jobForm.timeout_sec} onChange={e => setJobForm({ ...jobForm, timeout_sec: Number(e.target.value) })} />)}
        <p className="text-xs text-slate-500">{t('remote.limits')}</p>
        <label className="flex gap-2 items-center text-sm"><input type="checkbox" checked={jobForm.enabled} onChange={e => setJobForm({ ...jobForm, enabled: e.target.checked })} />{t('remote.enableSchedule')}</label>
        {error && <p role="alert" className="text-sm text-rose-500">{error}</p>}
        <div className="flex justify-end gap-2"><button type="button" className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" disabled={busy} onClick={() => setJobForm(null)}>{t('common.cancel')}</button><button className="primary-button" disabled={busy || previewLoading || !!previewError}>{busy ? t('common.saving') : t('common.save')}</button></div>
      </form>
    </Modal>}
    {detail && <Modal title={t('remote.runDetail')} size="xl" busy={busy} onClose={() => setDetailID(null)}>
      <div className="p-6 space-y-4 bg-white dark:bg-slate-900 rounded-2xl max-h-[calc(100dvh-40px)] overflow-y-auto"><div className="flex flex-wrap gap-3 items-center"><h2 className="text-lg font-semibold">{detail.job_name}</h2><StatusBadge value={detail.status} /></div><p className="font-mono text-xs break-all">{detail.id}</p><p className="text-sm text-slate-500">{formatTime(detail.started_at)} → {formatTime(detail.finished_at)}</p>
        <RemoteRunProgress run={detail} />
        {runPollError && detail.status === 'running' && <p role="alert" className="text-sm text-amber-600">{t('remote.progress.refreshFailed')}</p>}
        {detail.error && <p role="alert" className="text-sm text-rose-500 break-words">{detail.error}</p>}
        {detail.archives.map(archive => <div key={archive.source_path} className="border border-slate-200 dark:border-slate-700 rounded-xl p-4 space-y-3"><div className="flex flex-wrap gap-2 justify-between"><span className="font-mono text-sm break-all">{archive.source_path}</span><StatusBadge value={archive.status} /></div><p className="text-xs text-slate-500">{formatBytes(archive.size)}</p>{archive.error && <p className="text-sm text-rose-500 break-words">{archive.error}</p>}{archive.uploads.map(upload => <div key={upload.upstream_id} className="space-y-1 text-xs"><div className="flex gap-2 items-center"><span>{upload.upstream_name}</span><StatusBadge value={upload.status} /><span className="text-slate-500">{t('remote.attempts', { count: upload.attempts })}</span></div><p className="font-mono break-all text-slate-500">{upload.remote_path}</p>{upload.error && <p className="text-rose-500 break-words">{upload.error}</p>}</div>)}</div>)}
        <p className="text-xs text-slate-500">{t('remote.retryHelp')}</p><div className="flex justify-end gap-2">{detail.status !== 'succeeded' && detail.status !== 'running' && <button className="primary-button" disabled={busy} onClick={() => void action(async () => { const run = await api.retryRemoteRun(detail.id); runRequestSeq.current++; setRuns(previous => previous.map(item => item.id === run.id ? run : item)); setRunPollError(false) }, t('remote.started'))}>{t('remote.retryUploads')}</button>}<button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" disabled={busy} onClick={() => setDetailID(null)}>{t('common.close')}</button></div>
      </div>
    </Modal>}
    {deleting && <Modal title={t('common.delete')} busy={busy} size="md" onClose={() => setDeleting(null)}><div className="p-6 space-y-4 bg-white dark:bg-slate-900 rounded-2xl max-h-[calc(100dvh-40px)] overflow-y-auto"><h2 className="text-lg font-semibold">{t('common.delete')} · {deleting.name}</h2><p className="text-sm">{t('remote.deleteConfirm')}</p><div className="flex justify-end gap-2"><button className="inline-flex items-center justify-center gap-2 px-3 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-50" disabled={busy} onClick={() => setDeleting(null)}>{t('common.cancel')}</button><button className="primary-button" disabled={busy} onClick={() => void action(async () => { deleting.kind === 'hosts' ? await api.deleteRemoteHost(deleting.id) : await api.deleteRemoteJob(deleting.id); setDeleting(null) })}>{t('common.delete')}</button></div></div></Modal>}
  </div>
}
