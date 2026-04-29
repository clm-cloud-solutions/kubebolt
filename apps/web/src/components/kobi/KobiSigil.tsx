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
  static: 'text-kb-accent',
  watching: 'text-emerald-400',
  investigating: 'text-amber-400',
  awaiting: 'text-sky-400',
}

interface KobiSigilProps {
  state?: KobiSigilState
  size?: number
  className?: string
  /** Skip the default state-color class, e.g. when caller controls color via `text-*` */
  inheritColor?: boolean
}

export function KobiSigil({
  state = 'static',
  size = 32,
  className = '',
  inheritColor = false,
}: KobiSigilProps) {
  const colorClass = inheritColor ? '' : STATE_COLOR[state]

  // For investigating, we add two ring circles around the dot for the
  // staggered expansion animation, plus an "emphasize" pulse on the
  // diagonals. For awaiting, the dot becomes a dashed marching circle.
  // For watching, the dot pulses in place. For static, no animation.
  const showInvestigatingRings = state === 'investigating'
  const showAwaitingMarch = state === 'awaiting'
  const dotIsPulsing = state === 'watching'
  const diagonalsAnimate = state === 'investigating'

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label={state === 'static' ? 'Kobi' : `Kobi · ${state}`}
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

      {/* Intelligence dot — solid in static / watching / investigating,
          replaced by a dashed marching circle in awaiting. */}
      {showAwaitingMarch ? (
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
