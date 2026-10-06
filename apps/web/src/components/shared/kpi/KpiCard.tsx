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
// the bar): the figure takes the state colour and the card lights from its
// left edge. Only then — a row where everything glows has no signal left.

interface KpiCardProps {
  value: ReactNode
  unit?: ReactNode
  description?: ReactNode
  viz?: ReactNode
  caption?: ReactNode
  primary?: boolean
  // The number asks for someone: 'crit' (red) or 'warn' (amber).
  alert?: 'warn' | 'crit'
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

export function KpiCard({ value, unit, description, viz, caption, primary, alert, link, info, restricted, compact }: KpiCardProps) {
  const lit = alert && !restricted ? alert : undefined
  const card = (
    <div
      className={`kb-kpi ${compact ? 'kb-kpi-compact' : ''} ${primary ? 'kb-kpi-primary' : ''} ${lit ? `kb-kpi-alert ${lit === 'warn' ? 'kb-kpi-alert-warn' : ''}` : ''} flex flex-col min-w-0 h-full ${restricted ? 'opacity-60' : ''}`}
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
          style={lit ? { color: lit === 'crit' ? '#ef4056' : '#f5a623' } : undefined}
        >
          {restricted ? '—' : <CountUp value={value} />}
        </span>
        {unit && !restricted && <span className={`${compact ? 'text-sm' : 'text-lg'} text-kb-text-secondary truncate`}>{unit}</span>}
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
            <div className={`mt-auto ${compact ? 'pt-2 text-[10.5px]' : 'pt-3 text-[11px]'} leading-relaxed font-mono text-kb-text-tertiary`}>
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
