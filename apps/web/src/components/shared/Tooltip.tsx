import { useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { createPortal } from 'react-dom'

// Visual styling shared by the dashboard's tooltips. Matches the
// MetricChart Recharts tooltip, the cluster-map traffic tooltip, and
// the resource-usage cell tooltip — three places that previously each
// inlined their own copy of the same JSX. New surfaces should compose
// these so the whole app reads as one design.
//
// Usage:
//   <HoverTooltip
//     body={
//       <>
//         <TooltipHeader>{title}</TooltipHeader>
//         <TooltipRow color="#22d68a" label="Used" value="42m" />
//       </>
//     }
//   >
//     <span>hover me</span>
//   </HoverTooltip>
//
// Or, when the caller already manages mouse state, render
// <TooltipPanel> + rows directly inside their own portal.

export function TooltipPanel({ children, className = '' }: { children: ReactNode; className?: string }) {
  return (
    <div
      className={`bg-kb-elevated/95 backdrop-blur border border-kb-border rounded-md px-3 py-2 text-[11px] shadow-xl ${className}`}
    >
      {children}
    </div>
  )
}

export function TooltipHeader({ children, right }: { children: ReactNode; right?: ReactNode }) {
  return (
    <div className="text-kb-text-primary font-mono font-semibold text-[12px] tabular-nums mb-2 pb-1.5 border-b border-kb-border/60 flex items-baseline justify-between gap-3">
      <span className="truncate">{children}</span>
      {right && <span className="text-[10px] font-normal uppercase tracking-wider text-kb-text-tertiary shrink-0">{right}</span>}
    </div>
  )
}

interface TooltipRowProps {
  // Hex / CSS color string for the leading dot. Pass `null` to omit
  // the dot (e.g. for plain footer notes).
  color?: string | null
  label: string
  value?: ReactNode
}

export function TooltipRow({ color, label, value }: TooltipRowProps) {
  return (
    <div className="flex items-center gap-2">
      {color != null && (
        <span className="w-2 h-2 rounded-full flex-shrink-0" style={{ background: color }} />
      )}
      <span className="text-kb-text-secondary truncate">{label}</span>
      {value !== undefined && (
        <span className="ml-auto tabular-nums font-mono text-kb-text-primary">{value}</span>
      )}
    </div>
  )
}

export function TooltipNote({ children }: { children: ReactNode }) {
  return <div className="text-[10px] text-kb-text-tertiary leading-snug">{children}</div>
}

// HoverTooltip wraps a trigger element and renders the body in a
// portal positioned just below the trigger when the mouse enters.
// Pointer-events on the tooltip are off by default — anything that
// needs interaction inside (e.g. an Ask Kobi button) should pass
// `interactive` so the tooltip stays open while the cursor is over
// it. The default suits the static-info case where any mouseleave
// dismisses cleanly.
interface HoverTooltipProps {
  body: ReactNode
  children: ReactNode
  interactive?: boolean
  // Pixel offset below the trigger's bottom edge. 6px reads as
  // "anchored but not crowding" in our type sizes.
  offset?: number
  // Override min-width on the panel — default 200px. Useful when
  // the tooltip is intentionally narrow (single-line).
  minWidth?: number
}

// Position threshold for the auto-flip below → above. The tooltip's
// content height varies, but ~240px covers our typical body (header
// + 3-6 rows + ~8px slack from viewport edge). Triggers near the
// bottom of the viewport flip to an above-anchored tooltip so they
// don't render off-screen — observed on the last row of the
// namespace tiles section, which sits at the bottom of the page.
const FLIP_THRESHOLD_PX = 240

type AnchoredPos =
  | { kind: 'below'; x: number; top: number }
  | { kind: 'above'; x: number; bottom: string }

export function HoverTooltip({
  body,
  children,
  interactive = false,
  offset = 6,
  minWidth = 200,
}: HoverTooltipProps) {
  const triggerRef = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<AnchoredPos | null>(null)

  function handleEnter() {
    const node = triggerRef.current
    if (!node) return
    const rect = node.getBoundingClientRect()
    const spaceBelow = window.innerHeight - rect.bottom
    if (spaceBelow < FLIP_THRESHOLD_PX) {
      // Anchor the tooltip's BOTTOM edge `offset` px above the
      // trigger's top edge. CSS `bottom` measures from the viewport
      // bottom, so 100vh - rect.top + offset puts the tooltip's
      // bottom edge `offset` px above rect.top.
      setPos({
        kind: 'above',
        x: rect.left,
        bottom: `calc(100vh - ${rect.top - offset}px)`,
      })
    } else {
      setPos({
        kind: 'below',
        x: rect.left,
        top: rect.bottom + offset,
      })
    }
  }

  function handleLeave() {
    setPos(null)
  }

  return (
    <>
      {/* Wrapper is a real layout box (NOT display:contents) so that
          getBoundingClientRect returns the trigger's actual coords.
          A contents wrapper would collapse to 0×0 and the portal'd
          tooltip would anchor at viewport (0,0). The wrapper takes
          the natural display of its parent context — block in a
          grid cell, inline-block in a flex row — by leaving display
          unset and letting the children dictate sizing. */}
      <div
        ref={triggerRef}
        onMouseEnter={handleEnter}
        onMouseLeave={handleLeave}
      >
        {children}
      </div>
      {pos &&
        createPortal(
          <div
            className={`fixed z-[9999] ${interactive ? 'pointer-events-auto' : 'pointer-events-none'}`}
            style={{
              left: pos.x,
              top: pos.kind === 'below' ? pos.top : undefined,
              bottom: pos.kind === 'above' ? pos.bottom : undefined,
              minWidth,
            }}
            onMouseEnter={interactive ? handleEnter : undefined}
            onMouseLeave={interactive ? handleLeave : undefined}
          >
            <TooltipPanel>{body}</TooltipPanel>
          </div>,
          document.body,
        )}
    </>
  )
}
