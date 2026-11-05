// KubeBoltLogo — single source of truth for the Split Bolt, KubeBolt's
// mark: two congruent wedges on a 32 grid with a 2.5 gap between them. The
// gap never closes and the mark takes no stroke, gradient or rotation.
// Used in the Sidebar header, the AboutModal hero, and any
// future surfaces that need it. Geometry matches the favicon
// (apps/web/public/favicon.svg), so the browser tab and the in-app
// logo render the same shape — important since the tab icon is the
// first impression a user sees.
//
// Path-only on purpose: the soft-green rounded background is left
// to the call-site wrapper (`bg-kb-accent-light` + `rounded-*`) so
// light/dark theme variables drive the tile color. A baked-in
// background would freeze it to one palette and look wrong in dark
// mode.
//
// `fill="currentColor"` lets the wrapper's `text-kb-accent` class
// drive the bolt color, matching the existing call sites.

interface Props {
  className?: string
}

export function KubeBoltLogo({ className }: Props) {
  return (
    <svg
      viewBox="0 0 32 32"
      className={className}
      aria-hidden
      xmlns="http://www.w3.org/2000/svg"
    >
      <path d="M21.5 3.5 L8.5 14.75 L21.5 14.75 Z M10.5 28.5 L23.5 17.25 L10.5 17.25 Z" fill="currentColor" />
    </svg>
  )
}
