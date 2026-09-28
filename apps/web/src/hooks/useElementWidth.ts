import { useLayoutEffect, useRef, useState } from 'react'

// useElementWidth reports an element's rendered width and follows it.
//
// Tailwind's breakpoints (sm/xl…) answer "how wide is the WINDOW". A dashboard
// row needs "how wide is MY space": with Kobi's panel docked, the window is
// still ~2000px, so `xl:grid-cols-4` kept four columns in ~1000px and the KPI
// cards clipped their legends ("critica insigh", "Runni"). Tailwind 3.4 has no
// container queries without a plugin; this is the one-hook alternative.
//
// Returns 0 until measured, and where ResizeObserver does not exist (tests):
// callers keep their breakpoint classes for that case.
export function useElementWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [width, setWidth] = useState(0)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el || typeof ResizeObserver === 'undefined') return
    setWidth(el.getBoundingClientRect().width)
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width
      if (w !== undefined) setWidth(w)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, width] as const
}

// columnsFor picks how many equal columns of at least `minCol` px fit in
// `width`, among the allowed counts (largest first) — so a four-card row goes
// 4 → 2 → 1 and never 3 + 1.
export function columnsFor(width: number, minCol: number, gap: number, allowed: number[]): number {
  for (const n of allowed) {
    if (n * minCol + (n - 1) * gap <= width) return n
  }
  return allowed[allowed.length - 1]
}
