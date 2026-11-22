import React, { createContext, useCallback, useContext, useEffect, useId, useRef, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { useI18n } from '../contexts/I18nContext'
import { Modal } from './Modal'

interface ConfirmOptions {
  title?: string
  confirmText?: string
  danger?: boolean
}

type Confirm = (message: string, options?: ConfirmOptions) => Promise<boolean>
const ConfirmContext = createContext<Confirm | null>(null)

export function ConfirmProvider({ children }: { children: React.ReactNode }) {
  const { t } = useI18n()
  const descriptionId = useId()
  const [request, setRequest] = useState<({ message: string } & ConfirmOptions) | null>(null)
  const pending = useRef<((confirmed: boolean) => void) | null>(null)
  const askConfirm = useCallback<Confirm>((message, options = {}) => new Promise((resolve) => {
    // 同一个确认尚未处理时忽略重复触发，避免重复提交危险操作。
    if (pending.current) {
      resolve(false)
      return
    }
    pending.current = resolve
    setRequest({ message, ...options })
  }), [])

  useEffect(() => () => {
    pending.current?.(false)
    pending.current = null
  }, [])

  const settle = (confirmed: boolean) => {
    const resolve = pending.current
    pending.current = null
    setRequest(null)
    resolve?.(confirmed)
  }

  return (
    <ConfirmContext.Provider value={askConfirm}>
      {children}
      {request && (
        <Modal title={request.title || t('common.confirmTitle')} onClose={() => settle(false)} size="md" role="alertdialog" descriptionId={descriptionId}>
          <div className="bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded-2xl p-6 space-y-5">
            <div className="flex items-start gap-3 pr-5">
              <div className={`w-10 h-10 shrink-0 rounded-xl grid place-items-center ${request.danger ? 'bg-rose-50 text-rose-600 dark:bg-rose-500/15 dark:text-rose-400' : 'bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-400'}`}>
                <AlertTriangle aria-hidden="true" className="w-5 h-5" />
              </div>
              <div className="min-w-0">
                <h2 className="text-base font-semibold text-slate-900 dark:text-slate-100">{request.title || t('common.confirmTitle')}</h2>
                <p id={descriptionId} className="mt-2 text-sm leading-6 text-slate-600 dark:text-slate-300 whitespace-pre-line break-words">{request.message}</p>
              </div>
            </div>
            <div className="flex justify-end gap-2.5">
              <button type="button" data-autofocus onClick={() => settle(false)} className="px-4 py-2 rounded-lg border border-slate-200 dark:border-slate-700 text-sm text-slate-600 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800">{t('common.cancel')}</button>
              <button type="button" onClick={() => settle(true)} className={`px-4 py-2 rounded-lg text-sm font-medium text-white ${request.danger ? 'bg-rose-600 hover:bg-rose-700' : 'bg-indigo-600 hover:bg-indigo-500'}`}>{request.confirmText || t('common.confirm')}</button>
            </div>
          </div>
        </Modal>
      )}
    </ConfirmContext.Provider>
  )
}

export function useConfirm(): Confirm {
  const confirm = useContext(ConfirmContext)
  if (!confirm) throw new Error('useConfirm must be used within ConfirmProvider')
  return confirm
}

export interface BeforeNavigateDetail {
  waitUntil: (check: Promise<boolean>) => void
}

export async function allowNavigation(): Promise<boolean> {
  const checks: Promise<boolean>[] = []
  // 原生 confirm 是同步阻塞的；站内确认通过事件收集异步结果后再继续导航。
  const event = new CustomEvent<BeforeNavigateDetail>('safekeep:before-navigate', {
    cancelable: true,
    detail: { waitUntil: (check) => checks.push(check) },
  })
  if (!window.dispatchEvent(event)) return false
  return (await Promise.all(checks)).every(Boolean)
}
