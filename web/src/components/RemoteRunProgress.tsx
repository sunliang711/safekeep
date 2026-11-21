import { Loader2 } from 'lucide-react'
import { formatTime } from '../api'
import { useI18n } from '../contexts/I18nContext'
import type { TranslationKey } from '../i18n/zh'
import type { RemoteRun } from '../types'

const STAGES: Record<string, TranslationKey> = {
  queued: 'remote.progress.queued',
  checking: 'remote.progress.checking',
  archiving: 'remote.progress.archiving',
  verifying: 'remote.progress.verifying',
  uploading: 'remote.progress.uploading',
  retry_wait: 'remote.progress.retryWait',
}

export function RemoteRunProgress({ run, compact = false }: { run: RemoteRun; compact?: boolean }) {
  const { t } = useI18n()
  const running = run.status === 'running'
  const progress = run.progress
  const directoryIndex = progress?.directory_index || 0
  const upstreamIndex = progress?.upstream_index || 0
  const archive = run.archives[directoryIndex - 1]
  const upload = archive?.uploads[upstreamIndex - 1]
  const completed = run.archives.filter(item => item.status === 'succeeded').length
  const uploads = run.archives.flatMap(item => item.uploads)
  const uploaded = uploads.filter(item => item.status === 'succeeded').length
  const stage = running ? t(STAGES[progress?.stage || ''] || 'remote.progress.running') : t('remote.progress.finished')
  const color = running ? 'bg-indigo-500' : run.status === 'succeeded' ? 'bg-emerald-500' : run.status === 'partial_success' ? 'bg-amber-500' : 'bg-rose-500'

  return <div className={compact ? 'space-y-2 min-w-0' : 'rounded-xl border border-slate-200 dark:border-slate-700 p-4 space-y-3'}>
    <div role="status" aria-live="polite" className="text-sm space-y-1">
      <p className="flex items-center gap-2 font-medium">{running && <Loader2 className="w-4 h-4 shrink-0 animate-spin text-indigo-500" />}{stage}</p>
      {running && archive && <p className="text-xs text-slate-500 break-all">{t('remote.progress.directory', { current: directoryIndex, total: run.archives.length })} · {archive.source_path}</p>}
      {running && archive && upload && <p className="text-xs text-slate-500 break-words">{t('remote.progress.upstream', { current: upstreamIndex, total: archive.uploads.length })} · {upload.upstream_name} · {t('remote.attempts', { count: upload.attempts })}</p>}
      {running && progress?.stage === 'retry_wait' && progress.retry_at && <p className="text-xs text-amber-600 dark:text-amber-400">{t('remote.progress.retryAt', { time: formatTime(progress.retry_at) })}</p>}
    </div>
    <div role="progressbar" aria-label={t('remote.progress.directories')} aria-valuemin={0} aria-valuemax={Math.max(1, run.archives.length)} aria-valuenow={completed} className="h-1.5 rounded-full overflow-hidden bg-slate-100 dark:bg-slate-800">
      <div className={`h-full rounded-full transition-all ${color}`} style={{ width: `${run.archives.length ? completed / run.archives.length * 100 : 0}%` }} />
    </div>
    <p className="text-xs text-slate-500">{t('remote.progress.counts', { completed, total: run.archives.length, uploaded, uploads: uploads.length })}</p>
  </div>
}
