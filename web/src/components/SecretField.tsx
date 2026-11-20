import React, { useEffect, useState } from 'react'
import { Check, Copy, Eye, EyeOff, Loader2 } from 'lucide-react'
import { useToast } from './Toast'
import { copyText } from '../utils/clipboard'

interface SecretFieldProps {
  id?: string
  value: string
  onChange: (next: string) => void
  hasSaved?: boolean
  onReveal?: () => Promise<string>
  disabled?: boolean
  readOnly?: boolean
  required?: boolean
  placeholder?: string
  autoComplete?: string
  multiline?: boolean
  rows?: number
}

export const SecretField: React.FC<SecretFieldProps> = ({
  id,
  value,
  onChange,
  hasSaved = false,
  onReveal,
  disabled = false,
  readOnly = false,
  required = false,
  placeholder,
  autoComplete = 'off',
  multiline = false,
  rows = 4,
}) => {
  const toast = useToast()
  const [editing, setEditing] = useState(false)
  const [showPlain, setShowPlain] = useState(false)
  const [revealedValue, setRevealedValue] = useState<string | null>(null)
  const [revealing, setRevealing] = useState(false)
  const [copied, setCopied] = useState(false)

  // Reset transient state when switching between items or clearing
  useEffect(() => {
    setEditing(false)
    setShowPlain(false)
    setRevealedValue(null)
  }, [hasSaved, disabled])

  const showingSavedMask = !disabled && hasSaved && !editing
  const displayValue = disabled
    ? ''
    : showingSavedMask
    ? showPlain && revealedValue !== null
      ? revealedValue
      : '*****'
    : value

  const fetchSecret = async (): Promise<string | null> => {
    if (revealedValue !== null) return revealedValue
    if (!onReveal) return null
    setRevealing(true)
    try {
      const secret = await onReveal()
      setRevealedValue(secret)
      return secret
    } catch (e: any) {
      toast.error(e?.message || '获取密钥明文失败')
      return null
    } finally {
      setRevealing(false)
    }
  }

  const handleToggleVisibility = async () => {
    if (disabled || revealing) return
    if (showPlain) {
      setShowPlain(false)
      return
    }
    if (showingSavedMask) {
      const secret = await fetchSecret()
      if (secret === null) return
      if (secret === '') {
        toast.info('当前字段未保存内容')
        return
      }
    }
    setShowPlain(true)
  }

  const handleCopy = async () => {
    if (disabled || revealing) return
    let textToCopy = ''
    if (showingSavedMask) {
      const secret = await fetchSecret()
      if (!secret) {
        toast.info('当前无可复制内容')
        return
      }
      textToCopy = secret
    } else {
      if (!value) return
      textToCopy = value
    }
    try {
      await copyText(textToCopy)
      setCopied(true)
      toast.success('已复制密钥')
      setTimeout(() => setCopied(false), 1500)
    } catch (e: any) {
      toast.error(e?.message || '复制失败，请点击查看后手动复制')
    }
  }

  const canRevealOrCopy = !disabled && (hasSaved || value !== '')

  const commonProps = {
    id,
    autoComplete,
    required,
    disabled,
    readOnly,
    value: displayValue,
    placeholder:
      placeholder ?? (hasSaved ? (readOnly ? '点击右侧查看或复制' : '留空则保持不变，点击直接输入新值') : '请输入'),
    onFocus: () => {
      if (!disabled && !readOnly && hasSaved && !editing) {
        setEditing(true)
        onChange('')
      }
    },
    onBlur: () => {
      if (editing && value === '' && hasSaved) {
        setEditing(false)
      }
    },
    onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      if (readOnly) return
      if (!editing) setEditing(true)
      onChange(e.target.value)
    },
  }

  return (
    <div className={`flex ${multiline ? 'items-start' : 'items-center'} gap-1.5`}>
      <div className="relative flex-1 min-w-0">
        {multiline ? (
          <textarea
            {...commonProps}
            rows={showingSavedMask && !showPlain ? 2 : rows}
            style={!showingSavedMask && !showPlain ? ({ WebkitTextSecurity: 'disc' } as React.CSSProperties) : undefined}
            className="w-full px-3 py-1.5 pr-8 border rounded-lg font-mono text-xs disabled:bg-slate-100"
          />
        ) : (
          <input
            {...commonProps}
            type={showingSavedMask ? 'text' : showPlain ? 'text' : 'password'}
            className="w-full px-3 py-1.5 pr-8 border rounded-lg font-mono text-xs disabled:bg-slate-100"
          />
        )}
        <button
          type="button"
          disabled={!canRevealOrCopy || revealing}
          onClick={handleToggleVisibility}
          title={showPlain ? '隐藏内容' : '查看具体值'}
          aria-label={showPlain ? '隐藏内容' : '查看具体值'}
          className={`absolute right-2 ${multiline ? 'top-2' : 'top-1/2 -translate-y-1/2'} text-slate-400 hover:text-slate-700 disabled:opacity-30`}
        >
          {revealing ? (
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
          ) : showPlain ? (
            <EyeOff className="w-3.5 h-3.5" />
          ) : (
            <Eye className="w-3.5 h-3.5" />
          )}
        </button>
      </div>
      <button
        type="button"
        disabled={!canRevealOrCopy || revealing}
        onClick={handleCopy}
        title="复制"
        aria-label="复制"
        className="inline-flex items-center gap-1 px-2.5 py-1.5 border border-slate-300 hover:bg-slate-50 disabled:opacity-40 disabled:cursor-not-allowed rounded-lg text-xs text-slate-700 shrink-0"
      >
        {copied ? (
          <>
            <Check className="w-3.5 h-3.5 text-emerald-600" />
            <span className="text-emerald-700">已复制</span>
          </>
        ) : (
          <>
            <Copy className="w-3.5 h-3.5" />
            <span>复制</span>
          </>
        )}
      </button>
    </div>
  )
}
