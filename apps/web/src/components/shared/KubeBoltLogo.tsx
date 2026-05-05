// KubeBoltLogo — single source of truth for the lightning-bolt brand
// mark used in the Sidebar header, the AboutModal hero, and any
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
      viewBox="0 0 22 22"
      className={className}
      aria-hidden
      xmlns="http://www.w3.org/2000/svg"
    >
      <path d="M12 2L4 12h6l-2 8 10-12h-6l2-6z" fill="currentColor" />
    </svg>
  )
}
