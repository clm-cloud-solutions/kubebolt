import { useEffect, useState, useSyncExternalStore } from 'react'
import { Sparkles } from 'lucide-react'
import { useCopilot } from '@/contexts/CopilotContext'
import { KobiSigil } from '@/components/kobi'

// CopilotToggle — floating launcher at the bottom-right.
// Visually matches the AskCopilotButton + Copilot input bezel
// family: monochrome green gradient body (brand accent → deep
// green, per the site's "green as an accent" identity), cycling
// outer glow at idle, soft ping halo so the surface reads as
// "ambient AI", and a shimmer sweep + scale on hover. The point is
// to feel alive without being noisy on a fixed-position element
// the user sees on every page.
//
// It sits above everything else, so a side panel whose actions live in a
// bottom-right footer (a connector drawer's Save / Pause) would lose its
// clicks to it: such a panel calls useHideCopilotLauncher while it is open.
export function CopilotToggle() {
  const { config, isOpen, togglePanel } = useCopilot()
  const [hovered, setHovered] = useState(false)
  const held = useSyncExternalStore(subscribeHolds, holdsNow)

  // Hide entirely when copilot isn't enabled on the backend
  if (!config?.enabled) return null
  if (isOpen || held) return null

  // Only the round button takes the pointer. The box also holds the tooltip,
  // invisible until hover but 180px wide: as a pointer target it ate clicks
  // meant for whatever sat under it, on every page.
  return (
    <div className="fixed bottom-5 right-5 z-[250] flex items-center gap-3 pointer-events-none">
      {/* Tooltip — appears to the left of the button on hover.
          Solid card with the standard kb-border so it reads
          correctly in both light and dark mode (accent-tinted
          borders looked muddy in dark). The accent palette comes
          through via the soft outer glow + the gradient title +
          the sparkles icon, which is enough to brand it as AI
          without forcing a colored ring. */}
      <div
        className={`relative bg-kobi-card border border-kobi-border rounded-lg px-3.5 py-2.5 shadow-xl shadow-kobi-accent/15 pointer-events-none transition-all duration-300 ease-out origin-right ${
          hovered
            ? 'opacity-100 translate-x-0 scale-100'
            : 'opacity-0 translate-x-3 scale-95'
        }`}
      >
        <div className="flex items-center gap-1.5">
          <Sparkles className="w-3 h-3 text-kobi-accent shrink-0" />
          <span className="text-xs font-semibold text-kobi-accent leading-tight whitespace-nowrap tracking-tight">
            Kobi
          </span>
        </div>
        <div className="text-[10px] font-mono text-kobi-text-tertiary mt-1 whitespace-nowrap flex items-center gap-1.5">
          <span>Ask about your cluster</span>
          <kbd className="px-1.5 py-px rounded border border-kobi-border bg-kobi-elevated text-[9px] text-kobi-text-secondary shadow-[inset_0_-1px_0_rgba(0,0,0,0.06)]">
            ⌘J
          </kbd>
        </div>
      </div>

      {/* No cycling outer glow here any more: on every page it was the
          brightest thing on screen while nothing asked for Kobi, and it
          competed with the cards that do. The halo and the shadow stay, at
          about half their strength, so the launcher is still findable. */}
      <div
        className="relative w-12 h-12 rounded-full pointer-events-auto"
        onMouseEnter={() => setHovered(true)}
        onMouseLeave={() => setHovered(false)}
      >
        {/* Soft ambient halo behind the button — pings outward
            slowly to signal "active AI surface" without the
            urgency of Tailwind's default animate-ping. */}
        <span
          aria-hidden
          className="absolute inset-0 rounded-full bg-kobi-accent-fill/[0.12] animate-kb-ai-toggle-halo pointer-events-none motion-reduce:hidden"
        />

        {/* accent-fill: solid-control green, one step brighter than the
            text accent in light mode (identical in dark) — #00b864 dulled
            as a fill on the light theme. */}
        <button
          onClick={togglePanel}
          aria-label="Open Kobi"
          className="group relative w-12 h-12 rounded-full bg-gradient-to-br from-kobi-accent-fill to-kobi-accent-deep hover:scale-110 active:scale-95 transition-transform shadow-lg shadow-kobi-accent-fill/20 flex items-center justify-center overflow-hidden"
        >
          {/* Shimmer sweep on hover — reuses the same diagonal
              white-band pattern as the AskCopilotButton text
              variant for visual continuity. */}
          <span
            aria-hidden
            className="pointer-events-none absolute inset-0 -translate-x-full bg-gradient-to-r from-transparent via-white/30 to-transparent group-hover:translate-x-full transition-transform duration-700 ease-out"
          />
          {/* Dark ink on the green fill — white-on-accent fails
              contrast; the site pairs its green with near-black. */}
          <KobiSigil
            state="static"
            inheritColor
            size={22}
            className="relative text-kobi-ink"
          />
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
