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
import { Fragment, useState } from 'react'
import { RangeMenu, RangeMenuItem, RangeMenuSeparator } from './RangeMenu'

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
  // 14d/30d (history): coarser steps to stay well under the VM points-per-series
  // cap (14d×2h=168, 30d×6h=120 points). Mirrors MetricChart's DEFAULT_RANGE_OPTIONS.
  { label: '14d', minutes: 20160, step: '2h' },
  { label: '30d', minutes: 43200, step: '6h' },
]

interface Props {
  value: number
  onChange: (minutes: number) => void
  options?: RangeOption[]
}

// The words of each range in the menu; the option's own label ("15m") stays
// for chips and footnotes.
const LONG_LABEL: Record<number, string> = {
  5: 'Last 5 minutes',
  15: 'Last 15 minutes',
  60: 'Last hour',
  360: 'Last 6 hours',
  1440: 'Last 24 hours',
  10080: 'Last 7 days',
  20160: 'Last 14 days',
  43200: 'Last 30 days',
}

export function rangeLongLabel(option: RangeOption): string {
  return LONG_LABEL[option.minutes] ?? `Last ${option.label}`
}

// Ranges of a day or more are history; a line sets them apart from what is
// happening now.
const HISTORY_MINUTES = 7 * 1440

export function RangeSelector({ value, onChange, options = OVERVIEW_RANGE_OPTIONS }: Props) {
  const [open, setOpen] = useState(false)
  const current = options.find((o) => o.minutes === value)
  return (
    <RangeMenu label={current ? rangeLongLabel(current) : 'Time range'} open={open} onOpenChange={setOpen}>
      {options.map((o, i) => (
        <Fragment key={o.minutes}>
          {i > 0 && o.minutes >= HISTORY_MINUTES && options[i - 1].minutes < HISTORY_MINUTES && <RangeMenuSeparator />}
          <RangeMenuItem
            label={rangeLongLabel(o)}
            checked={o.minutes === value}
            onSelect={() => {
              onChange(o.minutes)
              setOpen(false)
            }}
          />
        </Fragment>
      ))}
    </RangeMenu>
  )
}
