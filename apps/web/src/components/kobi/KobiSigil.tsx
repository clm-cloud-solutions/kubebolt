/**
 * Kobi Sigil — the visual mark of Kobi.
 *
 * A deconstructed K with an intelligence dot. Three operational states +
 * a static variant. Animations come from `assets/kobi/sigil/kobi-animations.css`
 * (imported once at app init from `main.tsx`).
 *
 * Color is controlled via `currentColor`. The `state` prop sets a default
 * Tailwind text color for that state; override by passing `className`.
 */

export type KobiSigilState =
  | 'static' // semantic accent color, no animation
  | 'watching' // emerald — idle, monitoring
  | 'investigating' // amber — active streaming / tool calls
  | 'awaiting' // sky — proposal pending operator action

const STATE_COLOR: Record<KobiSigilState, string> = {
  // Kobi brand tokens, not the app-wide kb-accent — the sigil is a
  // Kobi-brand surface wherever it appears. Colors are theme-tiered in
  // globals.css (400 tier on dark, 700 tier on light): the fixed 400s
  // used before landed at 1.5–2.1:1 on light surfaces — invisible,
  // especially amber during state changes.
  static: 'text-kobi-sigil-static',
  watching: 'text-kobi-sigil-watching',
  investigating: 'text-kobi-sigil-investigating',
  awaiting: 'text-kobi-sigil-awaiting',
}

interface KobiSigilProps {
  state?: KobiSigilState
  size?: number
  className?: string
  /** Skip the default state-color class, e.g. when caller controls color via `text-*` */
  inheritColor?: boolean
  /**
   * Autonomous-mode variant (Kobi Autopilot): the intelligence dot ORBITS
   * the K on a faint dashed path — Kobi moving on its own — vs the chat's
   * fixed dot. Brand rule: same character, two modes. Overrides the
   * per-state dot treatments (the orbit IS the state signal here).
   */
  autonomous?: boolean
}

export function KobiSigil({
  state = 'static',
  size = 32,
  className = '',
  inheritColor = false,
  autonomous = false,
}: KobiSigilProps) {
  const colorClass = inheritColor ? '' : STATE_COLOR[state]

  // For investigating, we add two ring circles around the dot for the
  // staggered expansion animation, plus an "emphasize" pulse on the
  // diagonals. For awaiting, the dot becomes a dashed marching circle.
  // For watching, the dot pulses in place. For static, no animation.
  // The autonomous variant replaces all dot treatments with the orbit.
  const showInvestigatingRings = state === 'investigating' && !autonomous
  const showAwaitingMarch = state === 'awaiting' && !autonomous
  const dotIsPulsing = state === 'watching' && !autonomous
  const diagonalsAnimate = state === 'investigating'

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label={autonomous ? 'Kobi Autopilot' : state === 'static' ? 'Kobi' : `Kobi · ${state}`}
      className={`${colorClass} ${className}`.trim()}
    >
      {/* Vertical spine — the only stroke that never animates */}
      <line
        x1="9"
        y1="6"
        x2="9"
        y2="26"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="square"
      />

      {/* Upper diagonal */}
      <line
        x1="11.5"
        y1="14"
        x2="20"
        y2="6"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="square"
        className={diagonalsAnimate ? 'kobi-diagonals-investigating' : undefined}
      />

      {/* Lower diagonal */}
      <line
        x1="11.5"
        y1="18"
        x2="20"
        y2="26"
        stroke="currentColor"
        strokeWidth="1.8"
        strokeLinecap="square"
        className={diagonalsAnimate ? 'kobi-diagonals-investigating' : undefined}
      />

      {/* Autonomous orbit — faint dashed path + the dot circling the K.
          SMIL rotate keeps user-unit coordinates (CSS transform-origin on
          scaled SVGs is unreliable); 6s linear reads calm, not frantic.
          Radius 9.5 clears the spine (x=9) on the left pass — at 7.5 the
          dot crossed the same-color stroke and vanished (user QA). The
          orbit centers at x=13.5 (left of the viewBox center; tuned by
          eye with the user — at 16 the composition read right-heavy),
          which also parks the resting dot at x=23, essentially the fixed
          dot's home (23.5) in the chat variant. */}
      {autonomous && (
        <>
          <circle
            cx="13.5"
            cy="16"
            r="9.5"
            fill="none"
            stroke="currentColor"
            strokeWidth="0.6"
            strokeDasharray="1 2.5"
            opacity="0.3"
          />
          <circle cx="23" cy="16" r="2" fill="currentColor">
            <animateTransform
              attributeName="transform"
              type="rotate"
              from="0 13.5 16"
              to="360 13.5 16"
              dur="6s"
              repeatCount="indefinite"
            />
          </circle>
        </>
      )}

      {/* Intelligence dot — solid in static / watching / investigating,
          replaced by a dashed marching circle in awaiting. */}
      {autonomous ? null : showAwaitingMarch ? (
        <circle
          cx="23.5"
          cy="16"
          r="1.76"
          fill="none"
          stroke="currentColor"
          strokeWidth="1"
          strokeDasharray="1.5 1.5"
          className="kobi-dot-awaiting"
        />
      ) : (
        <circle
          cx="23.5"
          cy="16"
          r="1.76"
          fill="currentColor"
          className={dotIsPulsing ? 'kobi-dot-watching' : undefined}
        />
      )}

      {/* Investigating-only: two staggered rings expanding outward from
          the dot. They share the same stroke color so they read as
          extensions of the intelligence presence. */}
      {showInvestigatingRings && (
        <>
          <circle
            cx="23.5"
            cy="16"
            r="1.76"
            fill="none"
            stroke="currentColor"
            strokeWidth="0.7"
            className="kobi-ring-investigating"
          />
          <circle
            cx="23.5"
            cy="16"
            r="1.76"
            fill="none"
            stroke="currentColor"
            strokeWidth="0.7"
            className="kobi-ring-investigating-delayed"
          />
        </>
      )}
    </svg>
  )
}

// ── Icon adapters ────────────────────────────────────────────────────
//
// Drop-in replacements for lucide icons in tab strips and headers
// (anything typed ComponentType<{ className?: string }>). They inherit
// currentColor from the surrounding text like lucide does; the Tailwind
// w-*/h-* classes override the SVG's own width/height attributes.

/** Kobi Copilot mark — fixed intelligence dot. */
export function KobiSigilIcon({ className }: { className?: string }) {
  return <KobiSigil inheritColor className={className} />
}

/** Kobi Autopilot mark — the dot orbits the K (autonomous mode). */
export function KobiAutopilotIcon({ className }: { className?: string }) {
  return <KobiSigil autonomous inheritColor className={className} />
}
