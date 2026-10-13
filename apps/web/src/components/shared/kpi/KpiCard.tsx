import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { ArrowRight, ShieldOff } from 'lucide-react'
import { HoverTooltip } from '@/components/shared/Tooltip'
import { CountUp } from './CountUp'

// KpiCard — the site's card anatomy for the row a page opens with (Home,
// the cluster Overview). It replaces the label-on-top / small-number card:
//
//   figure + unit   "83 pods", "90 / 100" — the number IS the headline
//   description     one sentence saying what the number means right now
//   viz             a minichart chosen for THAT metric (see MiniCharts.tsx);
//                   every card in a row should tell its story a different way
//   caption         a mono line underneath: attribution, the breakdown, links
//
// Colour lives in the viz and the caption: amber and red mark what asks for
// the reader, green what is fine, grey everything else. `primary` draws the
// green bevel the sign-in card carries — one per row. `alert` is for the card
// whose NUMBER needs someone right now (criticals open, a node down, 5xx over
// the bar): the card lights from its left edge. Only then — a row where
// everything glows has no signal left.
//
// The figure itself never takes the state colour. A big red number shouts
// louder than the glow, the chip and the bar that already carry the state,
// and it read as alarming even when the number was fine (an 85 health score,
// 36 runtime events). The state lives in the frame; the number stays a number.
// One fact lights one card: when two cards derive from the same condition
// (the health verdict and the insights behind it), only one of them does.
//
// `status` puts the verdict in words next to the figure (a chip: Critical,
// Warning, Healthy) for a figure that is a score, not a count of problems: the
// 85 is fine, the state is what is critical. `unknown` is a verdict that cannot
// be given (a cluster out of contact): a grey chip and a grey bevel, never the
// green of a card that says all is well.

const STATUS_CHIP = {
  ok: 'bg-status-ok-dim text-status-ok',
  warn: 'bg-status-warn-dim text-status-warn',
  crit: 'bg-status-error-dim text-status-error',
  unknown: 'bg-kb-elevated text-kb-text-secondary',
} as const

interface KpiCardProps {
  value: ReactNode
  unit?: ReactNode
  description?: ReactNode
  viz?: ReactNode
  caption?: ReactNode
  primary?: boolean
  // The number asks for someone: 'crit' (red) or 'warn' (amber).
  alert?: 'warn' | 'crit'
  // The state behind a score, as a chip beside the figure (see above).
  status?: { label: string; tone: 'ok' | 'warn' | 'crit' | 'unknown' }
  // What the figure says about the state when the card has no chip (Security's
  // critical findings): the primary card's bevel takes it. Never lights.
  verdict?: 'ok' | 'warn' | 'crit'
  // Small "view all →" in the top-right corner.
  link?: { text: string; to: string }
  // Hover tooltip over the whole card (e.g. the health score's breakdown).
  info?: ReactNode
  // The connected identity cannot read this resource: the card keeps its
  // place in the row and says so instead of showing a zero.
  restricted?: boolean
  // A secondary row under the page's headline (the detailed analytics under
  // Usage & Credits' pool): smaller figure, sentence and padding, so the
  // headline row stays the first thing read.
  compact?: boolean
}

export function KpiCard({ value, unit, description, viz, caption, primary, alert, status, verdict: verdictProp, link, info, restricted, compact }: KpiCardProps) {
  const lit = alert && !restricted ? alert : undefined
  // The primary card's bevel is the verdict: green while its chip (or its
  // verdict, for a card without one) says all is well, amber or red otherwise
  // (no halo, no tint). A lit card already carries its state in the glow.
  const tone = status?.tone ?? verdictProp
  const verdict = primary && !lit && tone && tone !== 'ok' ? tone : undefined
  const brand = primary && !verdict
  const card = (
    <div
      className={`kb-kpi ${compact ? 'kb-kpi-compact' : ''} ${brand ? 'kb-kpi-primary' : ''} ${verdict ? `kb-kpi-verdict kb-kpi-verdict-${verdict}` : ''} ${lit ? `kb-kpi-alert ${lit === 'warn' ? 'kb-kpi-alert-warn' : ''}` : ''} flex flex-col min-w-0 h-full ${restricted ? 'opacity-60' : ''}`}
    >
      {link && !restricted && (
        <Link
          to={link.to}
          className={`absolute ${compact ? 'top-3 right-4' : 'top-4 right-5'} inline-flex items-center gap-1 text-[10px] font-mono text-kb-text-tertiary hover:text-kb-text-primary transition-colors`}
        >
          {link.text}
          <ArrowRight className="w-2.5 h-2.5" />
        </Link>
      )}
      <div className={`flex items-baseline ${compact ? 'gap-1.5' : 'gap-2'} min-w-0 pr-16`}>
        <span
          className={`font-display ${compact ? 'text-[28px] 2xl:text-[32px]' : 'text-[40px] 2xl:text-[48px]'} font-semibold leading-none tracking-[-0.035em] tabular-nums text-kb-text-primary`}
        >
          {restricted ? '—' : <CountUp value={value} />}
        </span>
        {unit && !restricted && <span className={`${compact ? 'text-sm' : 'text-lg'} text-kb-text-secondary truncate`}>{unit}</span>}
        {status && !restricted && (
          <span
            className={`self-center ml-1 px-1.5 py-0.5 rounded text-[10px] font-mono font-medium uppercase tracking-[0.08em] ${STATUS_CHIP[status.tone]}`}
          >
            {status.label}
          </span>
        )}
      </div>
      {restricted ? (
        <div className="mt-3 flex items-center gap-1.5 text-sm text-kb-text-secondary">
          <ShieldOff className="w-4 h-4 text-status-warn" />
          No access — insufficient permissions
        </div>
      ) : (
        <>
          {description && (
            <div className={`${compact ? 'mt-1 text-[13px]' : 'mt-1.5 text-[15px]'} leading-snug text-kb-text-primary`}>{description}</div>
          )}
          {/* The caption is pinned to the bottom so a row lines up on its
              captions; the chart centres in whatever the tallest card leaves
              over, so a shorter chart never sits on a block of empty card. */}
          {viz && <div className={`my-auto ${compact ? 'pt-3' : 'pt-4'}`}>{viz}</div>}
          {caption && (
            <div className={`mt-auto ${compact ? 'pt-2 text-[11px]' : 'pt-3 text-[12px]'} leading-relaxed font-mono text-kb-text-tertiary`}>
              {caption}
            </div>
          )}
        </>
      )}
    </div>
  )
  if (!info || restricted) return card
  return (
    <HoverTooltip body={info}>
      <div className="h-full">{card}</div>
    </HoverTooltip>
  )
}
