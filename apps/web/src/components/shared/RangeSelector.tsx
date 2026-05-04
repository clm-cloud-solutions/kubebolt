// Shared time-range selector. Mirrors the option set used internally by
// MetricChart so a single selector at page level can drive every chart
// underneath via MetricChart's `controlledRangeMinutes` prop. Keeping
// the option lookup table in lockstep with MetricChart's defaults is
// the only contract — if MetricChart starts accepting different ranges,
// this component should too.
//
// The "step" (resolution of each data point) is paired with the range
// here rather than guessed downstream, so consumers that don't render a
// MetricChart (e.g. a sparkline panel) can still pick a sensible step.

export interface RangeOption {
  label: string
  minutes: number
  step: string
}

// Mirror of MetricChart.DEFAULT_RANGE_OPTIONS so a page-level selector
// drives every chart consistently. If you add a step here, also extend
// the matching entry in MetricChart so its lookup resolves the right
// resolution when in controlled mode.
export const OVERVIEW_RANGE_OPTIONS: RangeOption[] = [
  { label: '5m', minutes: 5, step: '15s' },
  { label: '15m', minutes: 15, step: '15s' },
  { label: '1h', minutes: 60, step: '30s' },
  { label: '6h', minutes: 360, step: '2m' },
  { label: '24h', minutes: 1440, step: '10m' },
  { label: '7d', minutes: 10080, step: '1h' },
]

interface Props {
  value: number
  onChange: (minutes: number) => void
  options?: RangeOption[]
}

export function RangeSelector({ value, onChange, options = OVERVIEW_RANGE_OPTIONS }: Props) {
  return (
    <div
      className="flex items-center gap-0.5 rounded-md border border-kb-border bg-kb-card p-0.5"
      role="tablist"
      aria-label="Time range"
    >
      {options.map((opt) => {
        const selected = opt.minutes === value
        return (
          <button
            key={opt.minutes}
            role="tab"
            aria-selected={selected}
            onClick={() => onChange(opt.minutes)}
            className={`px-2.5 py-1 text-[10px] font-mono uppercase tracking-[0.06em] rounded transition-colors ${
              selected
                ? 'bg-kb-accent/15 text-kb-accent font-semibold'
                : 'text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary'
            }`}
          >
            {opt.label}
          </button>
        )
      })}
    </div>
  )
}
