import type { ReactNode } from 'react'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'

// AutoGrid lays its children in equal columns chosen from the space the grid
// actually has, not the window's: with Kobi's panel docked the window stays
// wide while the page halves, and a breakpoint grid kept three columns of
// squeezed charts. `allowed` lists the column counts to try, largest first
// (3 → 2 → 1, never 3 + 1). Until measured — and in tests, where
// ResizeObserver does not exist — the breakpoint classes in `fallback` apply.
export function AutoGrid({
  children,
  minCol,
  allowed,
  gap = 16,
  fallback,
}: {
  children: ReactNode
  minCol: number
  allowed: number[]
  gap?: number
  fallback: string
}) {
  const [ref, width] = useElementWidth<HTMLDivElement>()
  return (
    <div
      ref={ref}
      className={`grid ${width ? '' : fallback}`}
      style={{
        gap,
        ...(width ? { gridTemplateColumns: `repeat(${columnsFor(width, minCol, gap, allowed)}, minmax(0, 1fr))` } : {}),
      }}
    >
      {children}
    </div>
  )
}
