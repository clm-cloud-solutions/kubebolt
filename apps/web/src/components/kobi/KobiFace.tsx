import { useId, type CSSProperties } from 'react'
import './kobi-face.css'

/**
 * KobiFace — Sentinel, Kobi's face. One SVG on the 0 0 120 120 grid.
 *
 * The spec is the brand guidelines (kubebolt-docs, kobi/brand/brand-guidelines.md,
 * «The Face: Sentinel») and the SVGs in kobi/brand/face/. This component copies
 * them, as the site's KobiFace.astro does; if something disagrees, they win.
 *
 * - The variant comes from the size: L ≥ 64, M 32–63, S 20–31, XS < 20.
 *   Small sizes are drawn, not shrunk.
 * - What depends on the screen's density is CSS resolution queries, no JS:
 *   the dot's shapes need 64 device pixels and the forehead K 128.
 * - The theme is the app's `.dark` class; everything else is CSS on
 *   `data-*` attributes (kobi-face.css).
 * - `still` stops it (avatars, icons). Reduced motion stops everything and
 *   leaves each state's resting shape.
 * - `fab` is the floating button: no dot at rest, it floats on its shadow and
 *   lifts on hover or on the focus of the button around it.
 * - `autopilot`: the dot orbits the head, no rings.
 *
 * Only two places show the live state: the floating head and the panel
 * header. Everywhere else the face is still, or Watching.
 */

export type KobiFaceState =
  | 'watching'
  | 'investigating'
  | 'awaiting'
  | 'executing'
  | 'verifying'
  | 'nocause'
  | 'unavailable'

interface KobiFaceProps {
  size: number
  state?: KobiFaceState
  autopilot?: boolean
  still?: boolean
  /** Woken by hover (Ask Kobi): the Watching loop starts near its blink, so a
   * short hover sees one. */
  eager?: boolean
  fab?: boolean
  className?: string
  /** With a label the face is role="img"; without one it is decorative (the
   * text next to it says the state). */
  label?: string
}

// Eye: flat top, round bottom. Two round bars read as a pause sign.
function eye(x: number, y: number, w: number, h: number) {
  const r = w / 2
  return `M${x} ${y} H${x + w} V${y + h - r} A${r} ${r} 0 0 1 ${x} ${y + h - r} Z`
}

interface Geometry {
  shell: [number, number, number, number, number]
  visor: [number, number, number, number, number]
  eyes: [[number, number], [number, number]]
  ew: number
  eh: number
  dy: number
  dr: number
  edge: number
  /** Dot with a ring, gap, ring stroke. */
  ring: [number, number, number]
}

function geometry(v: 'L' | 'M' | 'S' | 'XS', big: boolean): Geometry {
  switch (v) {
    case 'L':
      return {
        shell: [16, 26, 88, 80, 38],
        visor: [25, 42, 70, 46, 23],
        eyes: [[41, 56], [69, 56]],
        ew: 10,
        eh: 14,
        dy: 14,
        dr: big ? 4.5 : 5.5,
        edge: big ? 1 : 1.5,
        ring: big ? [4.5, 2.2, 1.6] : [5.5, 2.4, 2],
      }
    // M and S carry the dot higher than the guidelines' table (13 and 12), as
    // the site does: at 32 and 24 px the table's gap, 6 and 5 units, is 1.6 and
    // 1 px and the dot sticks to the head. Here it is 9 and 8 units.
    case 'M':
      return {
        shell: [16, 26, 88, 80, 38],
        visor: [25, 42, 70, 46, 23],
        eyes: [[40.5, 55.5], [68.5, 55.5]],
        ew: 11,
        eh: 15,
        dy: 10,
        dr: 7,
        edge: 2.2,
        ring: [5, 2, 3.75],
      }
    case 'S':
      return {
        shell: [16, 26, 88, 80, 38],
        visor: [23, 41, 74, 48, 24],
        eyes: [[39, 55], [69, 55]],
        ew: 12,
        eh: 16,
        dy: 9,
        dr: 9,
        edge: 3.2,
        ring: [5, 2, 6],
      }
    case 'XS':
      return {
        shell: [14, 28, 92, 78, 38],
        visor: [21, 40, 78, 50, 25],
        eyes: [[37, 53], [69, 53]],
        ew: 14,
        eh: 18,
        dy: 10,
        dr: 11,
        edge: 4.5,
        ring: [6, 2.5, 7.5],
      }
  }
}

// Density thresholds. The dot's shapes need size × dpr ≥ 64; the K, ≥ 128.
// "hi2" shows from 2×, "hi15" from 1.5×.
function bucket(size: number, need: number) {
  if (size >= need) return 'all'
  if (size * 1.5 >= need) return 'hi15'
  if (size * 2 >= need) return 'hi2'
  return 'none'
}

const r2 = (n: number) => +n.toFixed(2)

export function KobiFace({
  size,
  state = 'watching',
  autopilot = false,
  still = false,
  eager = false,
  fab = false,
  className,
  label,
}: KobiFaceProps) {
  const uid = useId().replace(/[^a-zA-Z0-9_-]/g, '')
  const v = size >= 64 ? 'L' : size >= 32 ? 'M' : size >= 20 ? 'S' : 'XS'
  const big = size >= 96
  const G = geometry(v, big)

  const [sx, sy, sw, sh, srx] = G.shell
  const [vx, vy, vw, vh, vrx] = G.visor
  const [rd, gap, w] = G.ring
  const R = r2(rd + gap + w / 2)
  const exW = r2(w * 1.25)
  const exR = r2(rd + gap / 2 + exW / 2)
  // In Autopilot the dot rests at the top center, (60, 14), at any size: it
  // is the point the orbit passes through.
  const dotY = autopilot ? 14 : G.dy

  const shapes = bucket(size, 64)
  const kmark = v === 'L' ? bucket(size, 128) : 'none'
  const gloss = size >= 96
  const groundShadow = size >= 120 && !fab
  // Gaze offset, in grid units.
  const gaze = size >= 64 ? 2.5 : 1.5
  // The orbit: dashed and faint. From 64 px, stroke 0.8; below, about 0.5
  // screen px.
  const orbitW = size >= 64 ? 0.8 : r2(64 / size)

  const shadowId = `kf-sh-${uid}`
  // The floating button: 1.5 px of bob and 3 px of lift, in grid units.
  const u = 120 / size
  const style = {
    '--kf-rz': +(rd / G.dr).toFixed(3),
    '--kf-w0': +(rd / R).toFixed(3),
    '--g': `${gaze}px`,
    ...(fab ? { '--kf-bob': `${-r2(1.5 * u)}px`, '--kf-lift': `${-r2(3 * u)}px`, '--kf-rise': '10px' } : {}),
  } as CSSProperties

  return (
    <svg
      className={['kf', className].filter(Boolean).join(' ')}
      data-state={state}
      data-v={v}
      data-shapes={shapes}
      data-k={kmark}
      data-auto={autopilot ? '' : undefined}
      data-still={still ? '' : undefined}
      data-eager={eager ? '' : undefined}
      data-tiny={size <= 24 ? '' : undefined}
      data-fab={fab ? '' : undefined}
      width={size}
      height={size}
      viewBox="0 0 120 120"
      style={style}
      {...(label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': true })}
      focusable="false"
    >
      {fab && (
        <>
          <defs>
            <radialGradient id={shadowId}>
              <stop offset="0" className="kf-sh0" />
              <stop offset="1" className="kf-sh1" />
            </radialGradient>
            <radialGradient id={`${shadowId}-pool`}>
              <stop offset="0" className="kf-pool0" />
              <stop offset="1" className="kf-pool1" />
            </radialGradient>
          </defs>
          {/* On dark, a black shadow is lost on the near-black background.
              Under it goes a pool of neutral light: the lit floor the shadow
              reads on. On light the pool is hidden and the shadow stays. */}
          <g className="kf-shadow">
            <ellipse className="kf-pool" cx="60" cy="114.4" rx="46" ry="9" fill={`url(#${shadowId}-pool)`} />
            <ellipse cx="60" cy="114.4" rx="33.75" ry="5.6" fill={`url(#${shadowId})`} />
          </g>
        </>
      )}
      {groundShadow && <ellipse className="kf-ground" cx="60" cy="114" rx="26" ry="3.2" />}

      <g className="kf-head">
        <g className="kf-body">
          {/* The thin edge belongs to the light theme (CSS drops it on dark).
              The floating button keeps it: it passes over dark and light
              surfaces, and on dark it does not show. */}
          <rect className="kf-shell" x={sx} y={sy} width={sw} height={sh} rx={srx} strokeWidth={G.edge} />
          {v === 'L' && (
            <g className="kf-k" transform="translate(50.99 25.36) scale(0.54)">
              <line x1="9" y1="6" x2="9" y2="26" />
              <line x1="11.5" y1="14" x2="20" y2="6" />
              <line x1="11.5" y1="18" x2="20" y2="26" />
              <circle cx="23.5" cy="16" r="1.76" />
            </g>
          )}
          <rect className="kf-visor" x={vx} y={vy} width={vw} height={vh} rx={vrx} />
          {gloss && <path className="kf-gloss" d="M35 51 Q40 47 47 47" />}
          <g className="kf-eyes">
            <path className="kf-eye" d={eye(G.eyes[0][0], G.eyes[0][1], G.ew, G.eh)} />
            <path className="kf-eye kf-eye-r" d={eye(G.eyes[1][0], G.eyes[1][1], G.ew, G.eh)} />
          </g>
        </g>
      </g>

      {autopilot &&
        (size >= 64 ? (
          <circle className="kf-orbit" cx="60" cy="66" r="52" strokeWidth={orbitW} strokeDasharray="2 4" opacity="0.45" />
        ) : (
          <circle
            className="kf-orbit"
            cx="60"
            cy="66"
            r="52"
            strokeWidth={orbitW}
            pathLength={100}
            strokeDasharray="2 3"
            opacity="0.55"
          />
        ))}

      <g className="kf-pres">
        {!autopilot && (
          <g className="kf-rings">
            <circle className="kf-ring kf-inv" cx="60" cy={dotY} r={R} strokeWidth={w} />
            <circle className="kf-ring kf-wave" cx="60" cy={dotY} r={R} strokeWidth={w} />
            <circle className="kf-ring kf-wave kf-wave-b" cx="60" cy={dotY} r={R} strokeWidth={w} />
            <circle
              className="kf-ring kf-aw"
              cx="60"
              cy={dotY}
              r={R}
              strokeWidth={r2(w * 0.9)}
              pathLength={100}
              strokeDasharray="8.333 8.333"
            />
            <circle className="kf-ring kf-ex" cx="60" cy={dotY} r={exR} strokeWidth={exW} />
            <circle
              className="kf-ring kf-ver"
              cx="60"
              cy={dotY}
              r={R}
              strokeWidth={w}
              pathLength={100}
              strokeDasharray="100 100"
              strokeDashoffset="25"
            />
          </g>
        )}
        <circle className="kf-dot" cx="60" cy={dotY} r={G.dr} />
      </g>
    </svg>
  )
}

/** A small dot in a state's color, for text that says the state (the
 * floating button's pill, the chat's «Investigating» strip). `halo` adds a
 * faint ring of the same color around it. */
export function KobiStateDot({ state, halo, className }: { state: KobiFaceState; halo?: boolean; className?: string }) {
  return (
    <span
      aria-hidden
      className={['kf-statedot', className].filter(Boolean).join(' ')}
      data-state={state}
      data-halo={halo ? '' : undefined}
    />
  )
}

// ── Icon adapters ────────────────────────────────────────────────────
//
// Drop-in replacements for lucide icons in tab strips, menus and headers
// (anything typed ComponentType<{ className?: string }>). The size comes from
// the Tailwind width class the caller already passes (w-3.5 → 14 px, w-5 →
// 20 px), so the variant matches what is drawn. A tab that wants the face
// dimmed with its text marks itself data-tab-state="inactive".
function iconSize(className?: string) {
  const m = className?.match(/(?:^|\s)w-(\d+(?:\.\d+)?)(?=\s|$)/)
  return m ? Math.round(parseFloat(m[1]) * 4) : 16
}

/** Kobi's mark in tabs, menus and headers: the face, still. */
export function KobiIcon({ className }: { className?: string }) {
  return <KobiFace size={iconSize(className)} still className={className} />
}

/** Kobi Autopilot's mark: the face with its dot orbiting, everywhere. */
export function KobiAutopilotIcon({ className }: { className?: string }) {
  return <KobiFace size={iconSize(className)} autopilot className={className} />
}
