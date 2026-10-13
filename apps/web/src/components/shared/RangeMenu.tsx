import { useEffect, type ReactNode } from 'react'
import { CalendarDays, Check, ChevronDown, Lock } from 'lucide-react'

// RangeMenu — the dropdown every time-range control of the app is drawn with:
// a button naming the range in force, and a list of ranges under it. The
// shared RangeSelector (the cluster dashboards, Cost, Ingest Activity) and
// Platform › Operations' control both use it, so they read the same.
export function RangeMenu({
  label,
  open,
  onOpenChange,
  children,
}: {
  label: ReactNode
  open: boolean
  onOpenChange: (open: boolean) => void
  children: ReactNode
}) {
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onOpenChange(false)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onOpenChange])

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => onOpenChange(!open)}
        aria-haspopup="menu"
        aria-expanded={open}
        className="inline-flex items-center gap-1.5 h-7 px-2.5 rounded-md border border-kb-border bg-kb-card text-[12px] text-kb-text-primary hover:border-kb-border-active transition-colors whitespace-nowrap"
      >
        <CalendarDays className="w-3.5 h-3.5 text-kb-text-tertiary" />
        {label}
        <ChevronDown className="w-3.5 h-3.5 text-kb-text-tertiary" />
      </button>
      {open && (
        <>
          <div className="fixed inset-0 z-30" onClick={() => onOpenChange(false)} aria-hidden />
          <div role="menu" className="absolute z-40 mt-1.5 right-0 w-64 bg-kb-card border border-kb-border rounded-lg shadow-lg p-1">
            {children}
          </div>
        </>
      )}
    </div>
  )
}

// One range of the list. A disabled one (beyond the plan's history) keeps its
// place with a lock, and says why on hover.
export function RangeMenuItem({
  label,
  checked,
  disabled,
  hint,
  onSelect,
}: {
  label: string
  checked: boolean
  disabled?: boolean
  hint?: string
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      role="menuitemradio"
      aria-checked={checked}
      aria-disabled={disabled || undefined}
      disabled={disabled}
      title={disabled ? hint : undefined}
      onClick={onSelect}
      className={`w-full text-left px-2.5 py-1.5 rounded-md text-[12px] inline-flex items-center justify-between gap-2 transition-colors ${
        disabled
          ? 'text-kb-text-tertiary cursor-not-allowed'
          : `hover:bg-kb-card-hover hover:text-kb-text-primary ${checked ? 'text-kb-text-primary bg-kb-elevated' : 'text-kb-text-secondary'}`
      }`}
    >
      {label}
      {disabled ? <Lock className="w-3 h-3" /> : checked ? <Check className="w-3.5 h-3.5 text-kb-accent" /> : null}
    </button>
  )
}

export function RangeMenuSeparator() {
  return <div role="separator" className="my-1 border-t border-kb-border" />
}

// A line under the list: why some ranges are locked.
export function RangeMenuNote({ children }: { children: ReactNode }) {
  return <p className="px-2.5 pt-1.5 pb-1 text-[10.5px] leading-snug text-kb-text-tertiary">{children}</p>
}
