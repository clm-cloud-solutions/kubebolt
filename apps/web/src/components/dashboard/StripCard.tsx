import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { Info } from 'lucide-react'
import { HoverTooltip } from '@/components/shared/Tooltip'

// StripCard is the shared card grammar for the summary strips that
// sit at the top of the Capacity and Reliability sub-tabs (design/
// kubebolt-{capacity,reliability}-redesign.html): a scan layer the
// user reads BEFORE diving into the charts below. Every number shown
// here is derived from the same sources as the detail panels
// underneath — the strip summarizes the page, it does not introduce
// new data.
//
// Anatomy: uppercase mono label · big tabular value · one sub-line
// (state / attribution / CTA) · optional right-aligned sparkline.
// `hero` tints the card with the accent wash — reserved for the one
// card per strip that carries the page's headline opportunity.

export type StripAccent = 'ok' | 'warn' | 'crit' | 'info' | 'default'

const VALUE_COLOR: Record<StripAccent, string> = {
  ok: 'text-status-ok',
  warn: 'text-status-warn',
  crit: 'text-status-error',
  info: 'text-status-info',
  default: 'text-kb-text-primary',
}

const SUB_COLOR: Record<StripAccent, string> = {
  ok: 'text-status-ok',
  warn: 'text-status-warn',
  crit: 'text-status-error',
  info: 'text-status-info',
  default: 'text-kb-text-tertiary',
}

const SPARK_STROKE: Record<StripAccent, string> = {
  ok: '#22d68a',
  warn: '#f5a623',
  crit: '#ef4056',
  info: '#4c9aff',
  default: 'var(--kb-text-tertiary)',
}

interface StripCardProps {
  label: string
  icon?: ReactNode
  // Explanatory tooltip body, shown on hover over a small ⓘ next to
  // the label. Reserved for cards whose number's meaning or
  // derivation isn't obvious from the label (e.g. "avg not p99",
  // "reclaimable = Σ recs"). Compose from the shared Tooltip
  // primitives (TooltipHeader / TooltipRow / TooltipNote) so it reads
  // like every other tooltip in the app. Omit for self-evident cards.
  info?: ReactNode
  value: ReactNode
  valueSuffix?: string
  valueAccent?: StripAccent
  sub?: ReactNode
  subAccent?: StripAccent
  // Wrap the sub-line in a Link — used for the "N recs →" CTA.
  subTo?: string
  hero?: boolean
  // Normalized-or-not series for the corner sparkline; the card
  // scales it. Fewer than 2 points renders nothing.
  spark?: number[]
  sparkAccent?: StripAccent
}

export function StripCard({
  label,
  icon,
  info,
  value,
  valueSuffix,
  valueAccent = 'default',
  sub,
  subAccent = 'default',
  subTo,
  hero = false,
  spark,
  sparkAccent = 'default',
}: StripCardProps) {
  const subLine = sub != null && (
    <div className={`text-[11px] font-mono truncate ${SUB_COLOR[subAccent]}`}>{sub}</div>
  )
  return (
    <div
      className={`relative rounded-[10px] border p-4 min-w-0 ${
        hero ? '' : 'border-kb-border bg-kb-card'
      }`}
      // Hero = the same accent-gradient wash the Overview efficiency
      // band uses (existing tokens via color-mix — no new CSS vars),
      // so "the page's headline opportunity" reads identically across
      // sub-tabs.
      style={
        hero
          ? {
              background: 'linear-gradient(160deg, var(--kb-accent-light), var(--kb-card) 55%)',
              borderColor: 'color-mix(in srgb, var(--kb-accent) 25%, transparent)',
            }
          : undefined
      }
    >
      <div className="flex items-center gap-1.5 mb-2">
        {icon && <span className="text-kb-accent shrink-0">{icon}</span>}
        <span className="text-[10px] font-mono uppercase tracking-[0.09em] text-kb-text-tertiary truncate">
          {label}
        </span>
        {info && (
          <HoverTooltip body={info} interactive minWidth={220}>
            <button
              type="button"
              aria-label={`About ${label}`}
              className="shrink-0 text-kb-text-tertiary hover:text-kb-text-secondary transition-colors"
            >
              <Info className="w-3 h-3" />
            </button>
          </HoverTooltip>
        )}
      </div>
      <div className={`text-2xl font-semibold tabular-nums leading-none ${VALUE_COLOR[valueAccent]}`}>
        {value}
        {valueSuffix && (
          <span className="text-sm font-normal text-kb-text-tertiary ml-1">{valueSuffix}</span>
        )}
      </div>
      <div className="mt-1.5 flex items-end justify-between gap-2 min-w-0">
        {subTo && sub != null ? (
          <Link to={subTo} className="min-w-0 hover:opacity-80 transition-opacity">
            {subLine}
          </Link>
        ) : (
          subLine || <span />
        )}
        {spark && spark.length >= 2 && (
          <Sparkline values={spark} stroke={SPARK_STROKE[sparkAccent]} />
        )}
      </div>
    </div>
  )
}

// Sparkline — 52×20 polyline, min/max normalized with a flat-series
// guard (a constant series draws a midline instead of NaN).
function Sparkline({ values, stroke }: { values: number[]; stroke: string }) {
  const W = 52
  const H = 20
  const min = Math.min(...values)
  const max = Math.max(...values)
  const span = max - min
  const points = values
    .map((v, i) => {
      const x = (i / (values.length - 1)) * W
      const y = span > 0 ? H - 2 - ((v - min) / span) * (H - 4) : H / 2
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
  return (
    <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} className="shrink-0" aria-hidden>
      <polyline fill="none" stroke={stroke} strokeWidth="1.5" points={points} />
    </svg>
  )
}
