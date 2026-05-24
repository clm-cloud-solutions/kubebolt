import type { ReactNode } from 'react'

// Field is the shared form-row primitive for the Settings tabs.
//
// Layout rules (md+ inline mode):
//   - Label column: fixed at 200px so labels like "DEFAULT REFRESH
//     INTERVAL" and "ACCESS TOKEN TTL (SECONDS)" fit without wrapping.
//     Wider would steal too much from the input col on narrow cards.
//   - Input column: min-w-0 max-w-md so input widths don't sprawl on
//     wide cards (a 1000px-wide text input reads as a mistake) AND
//     short numeric inputs left-align inside that constrained zone,
//     producing a clean visual left edge across all rows.
//   - Vertical alignment: pt-1.5 on the label so its top-line baseline
//     sits where a single-line input's text baseline sits. Cleaner
//     than items-center which floats the label mid-input — that reads
//     as "label drifting" when scanned vertically.
//   - Helper text col-start-2 below input, capped at max-w-md so the
//     prose width matches the input column.
//
// Stacked mode (label-above-input) is the alternative — used when
// Field is rendered inside a narrow grid cell (e.g. inner grid-cols-2
// like Email host/port pair) where 200px of label would eat half the
// input's space.
export function Field({
  label,
  helper,
  dirty,
  stacked,
  children,
}: {
  label: string
  helper?: ReactNode
  dirty?: boolean
  stacked?: boolean
  children: ReactNode
}) {
  if (stacked) {
    return (
      <div className="space-y-1.5">
        <div className="flex items-center gap-2">
          <label className="block text-[11px] font-semibold text-kb-text-primary uppercase tracking-wider">
            {label}
          </label>
          {dirty && <UnsavedChip />}
        </div>
        <div className="min-w-0">{children}</div>
        {helper && (
          <p className="text-[11px] text-kb-text-tertiary leading-relaxed">{helper}</p>
        )}
      </div>
    )
  }
  return (
    <div className="grid grid-cols-1 md:grid-cols-[200px_minmax(0,1fr)] md:gap-x-5 gap-y-1.5">
      <div className="flex items-start gap-2 md:pt-1.5">
        <label className="block text-[11px] font-semibold text-kb-text-primary uppercase tracking-wider">
          {label}
        </label>
        {dirty && <UnsavedChip />}
      </div>
      <div className="min-w-0 max-w-md">{children}</div>
      {helper && (
        <p className="text-[11px] text-kb-text-tertiary leading-relaxed md:col-start-2 max-w-md">
          {helper}
        </p>
      )}
    </div>
  )
}

export function UnsavedChip() {
  return (
    <span className="text-[10px] font-mono font-medium uppercase tracking-wider text-status-warn">
      Unsaved
    </span>
  )
}
