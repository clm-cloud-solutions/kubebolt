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

// Tint a hero wash is built from. 'ok' keeps the brand accent so every card
// that already passed `hero` renders byte-identically; the others let a card
// wash itself in its OWN state, which is what makes a strip react instead of
// staying uniformly grey until you read the numbers.
const HERO_TINT: Record<StripAccent, string> = {
  ok: 'var(--kb-accent)',
  warn: '#f5a623',
  crit: '#ef4056',
  info: '#4c9aff',
  default: 'var(--kb-border-active)',
}

// First stop of the wash. 'ok' uses the existing --kb-accent-light token
// (each theme derives its own); the rest color-mix off the fixed status hex,
// which is single-valued app-wide, against the card so light mode doesn't get
// a saturated block.
// The alarm tones wash LIGHTER than the brand hero. Green marks an opportunity
// the reader may take or leave; amber and red mark something already wrong, and
// they arrive alongside a coloured value, a coloured sub-line and a coloured
// icon on the same card. At the accent's own 14% the card stopped reading as a
// card and started reading as a warning banner.
function heroWash(accent: StripAccent): { background: string; borderColor: string } {
  const tint = HERO_TINT[accent]
  const first =
    accent === 'ok' ? 'var(--kb-accent-light)' : `color-mix(in srgb, ${tint} 8%, transparent)`
  return {
    background: `linear-gradient(160deg, ${first}, var(--kb-card) 50%)`,
    borderColor: `color-mix(in srgb, ${tint} ${accent === 'ok' ? 25 : 18}%, var(--kb-border))`,
  }
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
  // `true` washes the card in the brand accent — the page's headline
  // OPPORTUNITY (rightsizing, run-rate). An accent name washes it in that
  // STATE instead, so a card can go amber or red on its own reading. Pass it
  // conditionally: a strip where every card is washed has no hierarchy left,
  // and one washed green at rest just adds noise to a page that is fine.
  hero?: boolean | StripAccent
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
  const heroAccent: StripAccent | null = hero === true ? 'ok' : hero || null
  return (
    <div
      className={`relative rounded-[10px] border p-4 min-w-0 ${
        heroAccent ? '' : 'border-kb-border bg-kb-card'
      }`}
      // Hero = the same accent-gradient wash the Overview efficiency
      // band uses (existing tokens via color-mix — no new CSS vars),
      // so "the page's headline opportunity" reads identically across
      // sub-tabs.
      style={heroAccent ? heroWash(heroAccent) : undefined}
    >
      <div className="flex items-center gap-1.5 mb-2">
        {/* The icon follows the wash. A red-washed card with a green glyph in
            the corner reads as two states at once. */}
        {icon && (
          <span
            className="shrink-0 text-kb-accent"
            style={heroAccent && heroAccent !== 'ok' ? { color: HERO_TINT[heroAccent] } : undefined}
          >
            {icon}
          </span>
        )}
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
