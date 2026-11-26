import type { ReactNode } from 'react'

// The column header every page table shares (Fleet, Plans): one string of
// classes, so no table drifts from the others. Data cells read `TD` (text) and
// `TD_NUM` (numbers, right-aligned): the size of the resource lists' cells.

export const TD = 'px-3 py-2.5 text-xs font-mono'
export const TD_NUM = `${TD} tabular-nums text-right`
// `kb-row` (globals.css) lights the row on hover in both themes. `TR` is a
// row that opens something; `TR_STATIC` one that only reads.
export const TR_STATIC = 'kb-row border-b border-kb-border last:border-0'
export const TR = `${TR_STATIC} cursor-pointer`

export function Th({ children, right, className = '' }: { children: ReactNode; right?: boolean; className?: string }) {
  return (
    <th
      className={`px-3 py-2.5 text-[10px] font-mono font-medium uppercase tracking-[0.08em] text-kb-text-secondary ${
        right ? 'text-right' : ''
      } ${className}`}
    >
      {children}
    </th>
  )
}
