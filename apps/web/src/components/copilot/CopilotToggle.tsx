import { useEffect, useId, useState, useSyncExternalStore } from 'react'
import { useCopilot } from '@/contexts/CopilotContext'
import { KobiFace, KobiStateDot } from '@/components/kobi'
import { useKobiPresence } from './useKobiPresence'

// CopilotToggle — the floating head at the bottom-right: Kobi's face at
// 64 px, floating on its own shadow with a slow bob, no disc behind it (brand
// guidelines, «Where the face lives in the product»).
//
// It is one of the two places that show Kobi's live state. The dot is hidden
// at rest: floating over the page it would land on a table row and read as
// that row's status. So the eyes act the state and, whenever Kobi is not
// watching, a pill beside the head says it («● Investigating · Logs ·
// catalog-api»). On hover or keyboard focus the head lifts and the dot rises
// into place with its live state; on touch screens, with no hover, the pill
// always carries it.
//
// It sits above everything else, so a side panel whose actions live in a
// bottom-right footer (a connector drawer's Save / Pause) would lose its
// clicks to it: such a panel calls useHideCopilotLauncher while it is open.
export function CopilotToggle() {
  const { config, isOpen, togglePanel } = useCopilot()
  const presence = useKobiPresence()
  const [hovered, setHovered] = useState(false)
  const held = useSyncExternalStore(subscribeHolds, holdsNow)
  const pillId = useId()

  // Hide entirely when copilot isn't enabled on the backend
  if (!config?.enabled) return null
  if (isOpen || held) return null

  const busy = presence.state !== 'watching'

  // Only the head takes the pointer. The box also holds the tooltip and the
  // pill, 180px wide or more: as a pointer target it ate clicks meant for
  // whatever sat under it, on every page.
  return (
    <div className="fixed bottom-5 right-5 z-[250] flex items-center gap-3 pointer-events-none">
      {/* The tooltip and the pill share one slot to the left of the head:
          hover shows the tooltip, and the pill steps back while it does. */}
      <div className="grid justify-items-end items-center">
        <div
          className={`[grid-area:1/1] relative bg-kobi-card border border-kobi-border rounded-lg px-3.5 py-2.5 shadow-xl shadow-black/10 transition-all duration-300 ease-out origin-right ${
            hovered ? 'opacity-100 translate-x-0 scale-100' : 'opacity-0 translate-x-3 scale-95'
          }`}
        >
          <span className="block text-xs font-semibold text-kobi-accent leading-tight whitespace-nowrap tracking-tight">Kobi</span>
          <div className="text-[10px] font-mono text-kobi-text-tertiary mt-1 whitespace-nowrap flex items-center gap-1.5">
            <span>Ask about your cluster</span>
            <kbd className="px-1.5 py-px rounded border border-kobi-border bg-kobi-elevated text-[9px] text-kobi-text-secondary shadow-[inset_0_-1px_0_rgba(0,0,0,0.06)]">
              ⌘J
            </kbd>
          </div>
        </div>
        {busy && (
          <div
            id={pillId}
            className={`[grid-area:1/1] inline-flex items-center gap-1.5 max-w-[280px] rounded-full bg-kobi-card border border-kobi-border pl-2.5 pr-3 py-1 shadow-lg shadow-black/10 text-[11px] font-medium text-kobi-text-secondary whitespace-nowrap transition-opacity duration-200 ${
              hovered ? 'opacity-0' : 'opacity-100'
            }`}
          >
            <KobiStateDot state={presence.state} />
            <span className="truncate">
              {presence.label}
              {presence.detail && <span className="text-kobi-text-tertiary"> · {presence.detail}</span>}
            </span>
          </div>
        )}
      </div>

      <div
        className="relative w-16 h-16 pointer-events-auto"
        onMouseEnter={() => setHovered(true)}
        onMouseLeave={() => setHovered(false)}
      >
        <button
          type="button"
          onClick={togglePanel}
          onFocus={() => setHovered(true)}
          onBlur={() => setHovered(false)}
          aria-label="Open Kobi"
          aria-describedby={busy ? pillId : undefined}
          className="relative block w-16 h-16 rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-kobi-accent/60"
        >
          <KobiFace size={64} state={presence.state} fab />
        </button>
      </div>
    </div>
  )
}

// Panels that ask the launcher to step aside. A count, not a flag: a panel
// closing must not bring the launcher back over another that is still open.
let holds = 0
const listeners = new Set<() => void>()

function subscribeHolds(onChange: () => void): () => void {
  listeners.add(onChange)
  return () => {
    listeners.delete(onChange)
  }
}

function holdsNow(): boolean {
  return holds > 0
}

function setHolds(next: number) {
  holds = next
  for (const l of listeners) l()
}

// useHideCopilotLauncher hides the floating Kobi launcher for as long as the
// calling component is mounted (and `active`).
export function useHideCopilotLauncher(active = true) {
  useEffect(() => {
    if (!active) return
    setHolds(holds + 1)
    return () => setHolds(holds - 1)
  }, [active])
}
