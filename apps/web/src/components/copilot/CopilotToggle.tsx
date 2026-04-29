import { useState } from 'react'
import { Sparkles } from 'lucide-react'
import { useCopilot } from '@/contexts/CopilotContext'
import { KobiSigil } from '@/components/kobi'

// CopilotToggle — floating launcher at the bottom-right.
// Visually matches the AskCopilotButton + Copilot input bezel
// family: green→violet gradient body, color-cycling outer glow at
// idle, soft ping halo so the surface reads as "ambient AI", and
// a shimmer sweep + scale on hover. The point is to feel alive
// without being noisy on a fixed-position element the user sees
// on every page.
export function CopilotToggle() {
  const { config, isOpen, togglePanel } = useCopilot()
  const [hovered, setHovered] = useState(false)

  // Hide entirely when copilot isn't enabled on the backend
  if (!config?.enabled) return null
  if (isOpen) return null

  return (
    <div
      className="fixed bottom-5 right-5 z-[250] flex items-center gap-3"
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      {/* Tooltip — appears to the left of the button on hover.
          Solid card with the standard kb-border so it reads
          correctly in both light and dark mode (accent-tinted
          borders looked muddy in dark). The accent palette comes
          through via the soft outer glow + the gradient title +
          the sparkles icon, which is enough to brand it as AI
          without forcing a colored ring. */}
      <div
        className={`relative bg-kb-card border border-kb-border rounded-lg px-3.5 py-2.5 shadow-xl shadow-kb-accent/15 pointer-events-none transition-all duration-300 ease-out origin-right ${
          hovered
            ? 'opacity-100 translate-x-0 scale-100'
            : 'opacity-0 translate-x-3 scale-95'
        }`}
      >
        <div className="flex items-center gap-1.5">
          <Sparkles className="w-3 h-3 text-kb-accent shrink-0" />
          <span className="text-xs font-semibold bg-gradient-to-r from-kb-accent via-kb-accent to-violet-400 bg-clip-text text-transparent leading-tight whitespace-nowrap">
            Kobi
          </span>
        </div>
        <div className="text-[10px] font-mono text-kb-text-tertiary mt-1 whitespace-nowrap flex items-center gap-1.5">
          <span>Ask about your cluster</span>
          <kbd className="px-1.5 py-px rounded border border-kb-border bg-kb-elevated text-[9px] text-kb-text-secondary shadow-[inset_0_-1px_0_rgba(0,0,0,0.06)]">
            ⌘J
          </kbd>
        </div>
      </div>

      {/* Wrapper carries the cycling outer glow. Sized to the button
          so the glow halos the round shape, not a square box. */}
      <div className="relative w-12 h-12 rounded-full animate-kb-ai-glow">
        {/* Soft ambient halo behind the button — pings outward
            slowly to signal "active AI surface" without the
            urgency of Tailwind's default animate-ping. */}
        <span
          aria-hidden
          className="absolute inset-0 rounded-full bg-kb-accent/25 animate-kb-ai-toggle-halo pointer-events-none motion-reduce:hidden"
        />

        <button
          onClick={togglePanel}
          aria-label="Open Kobi"
          className="group relative w-12 h-12 rounded-full bg-gradient-to-br from-kb-accent via-kb-accent to-violet-500 hover:scale-110 active:scale-95 transition-transform shadow-lg shadow-kb-accent/40 flex items-center justify-center overflow-hidden"
        >
          {/* Shimmer sweep on hover — reuses the same diagonal
              white-band pattern as the AskCopilotButton text
              variant for visual continuity. */}
          <span
            aria-hidden
            className="pointer-events-none absolute inset-0 -translate-x-full bg-gradient-to-r from-transparent via-white/30 to-transparent group-hover:translate-x-full transition-transform duration-700 ease-out"
          />
          <KobiSigil
            state="static"
            inheritColor
            size={22}
            className="relative text-white drop-shadow-[0_1px_2px_rgba(0,0,0,0.25)]"
          />
        </button>
      </div>
    </div>
  )
}
