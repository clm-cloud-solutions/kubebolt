import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { enterSkipped, markEnteredAt } from '@/utils/enterOnce'

// CountUp — a KPI figure that counts up from 0 the first time a number shows,
// the site's figure animation (WeekBento) in the app's KPI rows.
//
// It plays once per page per sign-in (utils/enterOnce.ts): the first visit to a
// page after signing in counts, a later visit shows the value as is. A
// data refresh does NOT replay it either — the card stays mounted and the new value
// is shown as is; recounting every 30 s would read as "something changed".
// The first value it sees may be a placeholder ("—", "…") while the data
// loads: it waits for the first real number, and a 0 does not use up the
// turn either (there is nothing to count, and the real value often follows).
//
// Strings keep their shape: "$743", "9.5", "980m", "1,234" count with the same
// prefix, suffix, decimals and grouping. Anything without a number renders
// untouched. Reduced motion, and the test environment, show the final value.

const NUM = /-?\d[\d,]*(?:\.\d+)?/
const DURATION = 1000
const BASE_DELAY = 120
const STAGGER = 70

// Same curve as the minicharts' CSS (cubic-bezier(0.22, 1, 0.36, 1)), close
// enough as an ease-out cubic.
const ease = (p: number) => 1 - Math.pow(1 - p, 3)

function staticOnly(): boolean {
  if (import.meta.env.MODE === 'test') return true
  try {
    return window.matchMedia('(prefers-reduced-motion: reduce)').matches
  } catch {
    return true
  }
}

// The card's place in its row, from the --kb-enter-i the grid sets on its
// children (globals.css), so the count starts with its card's charts.
function enterDelay(el: HTMLElement): number {
  const i = parseInt(getComputedStyle(el).getPropertyValue('--kb-enter-i'), 10)
  return BASE_DELAY + (Number.isFinite(i) ? i : 0) * STAGGER
}

export function CountUp({ value }: { value: ReactNode }) {
  const ref = useRef<HTMLSpanElement>(null)
  const played = useRef(false)
  // What the animation shows; null = show the value itself.
  const [shown, setShown] = useState<string | null>(null)
  const text = typeof value === 'number' ? String(value) : typeof value === 'string' ? value : null

  useLayoutEffect(() => {
    if (played.current || text == null || !ref.current) return
    const m = text.match(NUM)
    if (!m || m.index == null) return
    const raw = m[0]
    const target = parseFloat(raw.replace(/,/g, ''))
    if (!Number.isFinite(target) || target === 0) return
    played.current = true
    // The page has now shown real figures: it counts as entered (once per
    // page per sign-in, utils/enterOnce.ts), whether or not it animates.
    markEnteredAt(ref.current)
    if (staticOnly() || enterSkipped(ref.current)) return

    const prefix = text.slice(0, m.index)
    const suffix = text.slice(m.index + raw.length)
    const decimals = (raw.split('.')[1] ?? '').length
    const grouped = raw.includes(',')
    const fmt = (v: number) =>
      grouped
        ? v.toLocaleString('en-US', { minimumFractionDigits: decimals, maximumFractionDigits: decimals })
        : v.toFixed(decimals)

    setShown(prefix + fmt(0) + suffix)
    const start = performance.now() + enterDelay(ref.current)
    let raf = 0
    let done = false
    const tick = (now: number) => {
      const p = Math.min(1, Math.max(0, (now - start) / DURATION))
      if (p >= 1) {
        done = true
        setShown(null)
        return
      }
      setShown(prefix + fmt(target * ease(p)) + suffix)
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => {
      cancelAnimationFrame(raf)
      // Cut short (StrictMode's double mount in dev, or a new value mid-count):
      // the turn is not used up, so the next run counts again.
      if (!done) played.current = false
      setShown(null)
    }
  }, [text])

  return <span ref={ref}>{shown ?? value}</span>
}
