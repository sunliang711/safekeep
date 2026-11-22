import React, { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { CalendarDays, Check, ChevronDown, ChevronLeft, ChevronRight, Clock3 } from 'lucide-react'
import { useI18n } from '../contexts/I18nContext'
import './AuditFilters.css'

interface FilterOption {
  value: string
  label: string
  group?: string
}

function useFilterPopover(width: number, maximumHeight = 400) {
  const [open, setOpen] = useState(false)
  const trigger = useRef<HTMLButtonElement>(null)
  const panel = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState<React.CSSProperties>({ visibility: 'hidden' })
  const close = (restoreFocus = false) => {
    setOpen(false)
    if (restoreFocus) trigger.current?.focus()
  }

  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      if (!trigger.current || !panel.current) return
      const rect = trigger.current.getBoundingClientRect()
      const panelWidth = Math.min(Math.max(width, rect.width), window.innerWidth - 24)
      const below = window.innerHeight - rect.bottom - 20
      const above = rect.top - 20
      // 浮层挂到 body，并根据可用空间翻转和限宽，避免被列表裁切或超出窄屏。
      const upwards = below < Math.min(panel.current.scrollHeight, maximumHeight) && above > below
      setPosition({
        left: Math.max(12, Math.min(rect.left, window.innerWidth - panelWidth - 12)),
        top: upwards ? undefined : rect.bottom + 8,
        bottom: upwards ? window.innerHeight - rect.top + 8 : undefined,
        width: panelWidth,
        maxHeight: Math.max(80, Math.min(maximumHeight, upwards ? above : below)),
      })
    }
    const outside = (event: Event) => {
      const target = event.target as Node
      if (!trigger.current?.contains(target) && !panel.current?.contains(target)) close()
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        close(true)
      }
    }
    place()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    document.addEventListener('pointerdown', outside)
    document.addEventListener('focusin', outside)
    document.addEventListener('keydown', escape)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
      document.removeEventListener('pointerdown', outside)
      document.removeEventListener('focusin', outside)
      document.removeEventListener('keydown', escape)
    }
  }, [open, width, maximumHeight])

  return { open, setOpen, trigger, panel, position, close }
}

export function AuditSelect({ label, value, options, onChange, wide = false }: {
  label: string
  value: string
  options: FilterOption[]
  onChange: (value: string) => void
  wide?: boolean
}) {
  const id = useId()
  const popover = useFilterPopover(wide ? 300 : 180)
  const [active, setActive] = useState(0)
  const search = useRef({ text: '', at: 0 })
  const selected = options.findIndex((option) => option.value === value)
  const open = () => {
    setActive(Math.max(0, selected))
    popover.setOpen(true)
  }
  const choose = (index: number) => {
    onChange(options[index].value)
    popover.close(true)
  }

  useEffect(() => {
    if (popover.open) document.getElementById(`${id}-${active}`)?.scrollIntoView({ block: 'nearest' })
  }, [active, popover.open, id])

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault()
      if (!popover.open) open()
      else setActive((current) => event.key === 'Home' ? 0 : event.key === 'End' ? options.length - 1 : (current + (event.key === 'ArrowDown' ? 1 : -1) + options.length) % options.length)
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      if (popover.open) choose(active)
      else open()
    } else if (event.key === 'Tab') {
      popover.close()
    } else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
      // 保留原生选择器的键盘前缀定位能力，便于访问较长的动作列表。
      search.current.text = (Date.now() - search.current.at < 700 ? search.current.text : '') + event.key.toLocaleLowerCase()
      search.current.at = Date.now()
      const index = options.findIndex((option) => option.label.toLocaleLowerCase().startsWith(search.current.text))
      if (index >= 0) {
        event.preventDefault()
        popover.setOpen(true)
        setActive(index)
      }
    }
  }

  return (
    <div className={`audit-filter ${wide ? 'audit-filter-wide' : ''}`}>
      <button ref={popover.trigger} type="button" role="combobox" aria-label={label} aria-haspopup="listbox" aria-expanded={popover.open} aria-controls={popover.open ? id : undefined} aria-activedescendant={popover.open ? `${id}-${active}` : undefined} className="audit-filter-trigger" data-filled={!!value} title={options[selected]?.label} onClick={() => popover.open ? popover.close() : open()} onKeyDown={onKeyDown}>
        <span>{options[selected]?.label || label}</span>
        <ChevronDown aria-hidden="true" size={14} className={popover.open ? 'audit-chevron-open' : ''} />
      </button>
      {popover.open && createPortal(
        <div ref={popover.panel} id={id} role="listbox" aria-label={label} className="audit-filter-panel audit-select-menu" style={popover.position}>
          {options.map((option, index) => (
            <React.Fragment key={option.value}>
              {option.group && option.group !== options[index - 1]?.group && <div role="presentation" className="audit-select-group">{option.group}</div>}
              <div id={`${id}-${index}`} role="option" aria-selected={value === option.value} className="audit-select-option" data-active={active === index} onMouseMove={() => setActive(index)} onMouseDown={(event) => event.preventDefault()} onClick={() => choose(index)}>
                <span>{option.label}</span>
                {value === option.value && <Check aria-hidden="true" size={15} />}
              </div>
            </React.Fragment>
          ))}
        </div>,
        document.body
      )}
    </div>
  )
}

function localDate(date: Date): string {
  return `${String(date.getFullYear()).padStart(4, '0')}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

export function AuditDateTime({ label, value, onChange }: {
  label: string
  value: string
  onChange: (value: string) => void
}) {
  const { t, lang } = useI18n()
  const id = useId()
  const popover = useFilterPopover(320, 480)
  const [draftDate, setDraftDate] = useState(() => localDate(new Date()))
  const [hours, setHours] = useState('00')
  const [minutes, setMinutes] = useState('00')
  const [month, setMonth] = useState(() => new Date(new Date().getFullYear(), new Date().getMonth(), 1))
  const locale = lang === 'en' ? 'en-US' : 'zh-CN'
  const today = localDate(new Date())
  const start = new Date(month.getFullYear(), month.getMonth(), 1 - month.getDay(), 12)
  const days = Array.from({ length: 42 }, (_, index) => new Date(start.getFullYear(), start.getMonth(), start.getDate() + index, 12))
  const weekdays = Array.from({ length: 7 }, (_, index) => new Intl.DateTimeFormat(locale, { weekday: 'narrow' }).format(new Date(2024, 0, 7 + index)))
  const focusDate = days.some((day) => localDate(day) === draftDate) ? draftDate : localDate(month)
  const validTime = /^\d{1,2}$/.test(hours) && Number(hours) < 24 && /^\d{1,2}$/.test(minutes) && Number(minutes) < 60

  const open = () => {
    const date = value ? new Date(value) : new Date()
    setDraftDate(localDate(date))
    setHours(value ? String(date.getHours()).padStart(2, '0') : '00')
    setMinutes(value ? String(date.getMinutes()).padStart(2, '0') : '00')
    setMonth(new Date(date.getFullYear(), date.getMonth(), 1))
    popover.setOpen(true)
  }

  useEffect(() => {
    if (popover.open) popover.panel.current?.querySelector<HTMLButtonElement>('[data-day][tabindex="0"]')?.focus()
  }, [popover.open])

  const selectDay = (date: Date, focus = false) => {
    const next = localDate(date)
    setDraftDate(next)
    setMonth(new Date(date.getFullYear(), date.getMonth(), 1))
    if (focus) requestAnimationFrame(() => popover.panel.current?.querySelector<HTMLButtonElement>(`[data-day="${next}"]`)?.focus())
  }

  const onDayKeyDown = (event: React.KeyboardEvent, day: Date) => {
    const steps: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7, Home: -day.getDay(), End: 6 - day.getDay() }
    if (event.key in steps) {
      event.preventDefault()
      selectDay(new Date(day.getFullYear(), day.getMonth(), day.getDate() + steps[event.key], 12), true)
    } else if (event.key === 'PageUp' || event.key === 'PageDown') {
      event.preventDefault()
      const next = new Date(day.getFullYear(), day.getMonth() + (event.key === 'PageUp' ? -1 : 1), 1, 12)
      next.setDate(Math.min(day.getDate(), new Date(next.getFullYear(), next.getMonth() + 1, 0).getDate()))
      selectDay(next, true)
    }
  }

  return (
    <div className="audit-filter audit-filter-date">
      <button ref={popover.trigger} type="button" aria-label={value ? `${label}: ${value.replace('T', ' ')}` : label} aria-haspopup="dialog" aria-expanded={popover.open} aria-controls={popover.open ? id : undefined} className="audit-filter-trigger" data-filled={!!value} onClick={() => popover.open ? popover.close() : open()}>
        <CalendarDays aria-hidden="true" size={15} />
        <span>{value ? value.replace('T', ' ') : label}</span>
        <ChevronDown aria-hidden="true" size={14} className={popover.open ? 'audit-chevron-open' : ''} />
      </button>
      {popover.open && createPortal(
        <div ref={popover.panel} id={id} role="dialog" aria-label={label} className="audit-filter-panel audit-date-panel" style={popover.position} onKeyDown={(event) => {
          // 日期面板是非模态浮层，Tab 离开时将焦点接回筛选栏。
          if (event.key !== 'Tab') return
          const controls = Array.from(popover.panel.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input') || []).filter((element) => element.tabIndex >= 0)
          if ((!event.shiftKey && event.target === controls[controls.length - 1]) || (event.shiftKey && event.target === controls[0])) {
            event.preventDefault()
            const next = Array.from(document.querySelectorAll<HTMLElement>('a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex="0"]')).filter((element) => element.tabIndex >= 0 && element.getClientRects().length > 0 && !popover.panel.current?.contains(element))
            const index = next.indexOf(popover.trigger.current!)
            popover.close(true)
            if (!event.shiftKey) next[index + 1]?.focus()
          }
        }}>
          <div className="audit-date-heading">{label}</div>
          <div className="audit-calendar-header">
            <button type="button" className="audit-calendar-nav" aria-label={t('audit.datePreviousMonth')} onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}><ChevronLeft aria-hidden="true" size={16} /></button>
            <span aria-live="polite">{new Intl.DateTimeFormat(locale, { year: 'numeric', month: 'long' }).format(month)}</span>
            <button type="button" className="audit-calendar-nav" aria-label={t('audit.dateNextMonth')} onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}><ChevronRight aria-hidden="true" size={16} /></button>
          </div>
          <div className="audit-calendar-grid">
            {weekdays.map((day, index) => <span key={index} className="audit-calendar-weekday" aria-hidden="true">{day}</span>)}
            {days.map((day) => {
              const key = localDate(day)
              return <button key={key} type="button" data-day={key} data-selected={key === draftDate} data-today={key === today} data-outside={day.getMonth() !== month.getMonth()} tabIndex={key === focusDate ? 0 : -1} aria-label={new Intl.DateTimeFormat(locale, { dateStyle: 'full' }).format(day)} aria-pressed={key === draftDate} aria-current={key === today ? 'date' : undefined} className="audit-calendar-day" onClick={() => selectDay(day, true)} onKeyDown={(event) => onDayKeyDown(event, day)}>{day.getDate()}</button>
            })}
          </div>
          <div className="audit-date-time">
            <span><Clock3 aria-hidden="true" size={15} />{t('audit.dateTime')}</span>
            <div>
              <input type="text" inputMode="numeric" maxLength={2} aria-label={t('audit.dateHour')} aria-invalid={!/^\d{1,2}$/.test(hours) || Number(hours) > 23} value={hours} onChange={(event) => setHours(event.target.value.replace(/\D/g, ''))} onBlur={() => { if (hours) setHours(hours.padStart(2, '0')) }} />
              <span>:</span>
              <input type="text" inputMode="numeric" maxLength={2} aria-label={t('audit.dateMinute')} aria-invalid={!/^\d{1,2}$/.test(minutes) || Number(minutes) > 59} value={minutes} onChange={(event) => setMinutes(event.target.value.replace(/\D/g, ''))} onBlur={() => { if (minutes) setMinutes(minutes.padStart(2, '0')) }} />
            </div>
          </div>
          <div className="audit-date-footer">
            <button type="button" className="audit-date-text" onClick={() => { onChange(''); popover.close(true) }}>{t('common.clear')}</button>
            <button type="button" className="audit-date-text" onClick={() => selectDay(new Date(), true)}>{t('audit.dateToday')}</button>
            <button type="button" className="audit-date-apply" disabled={!validTime} onClick={() => {
              // 保持 datetime-local 的本地时间格式，由审计页统一转为 ISO 查询参数。
              onChange(`${draftDate}T${hours.padStart(2, '0')}:${minutes.padStart(2, '0')}`)
              popover.close(true)
            }}>{t('audit.dateApply')}</button>
          </div>
        </div>,
        document.body
      )}
    </div>
  )
}
