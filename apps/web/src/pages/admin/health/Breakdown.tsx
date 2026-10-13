import { TooltipHeader, TooltipNote, TooltipRow } from '@/components/shared/Tooltip'

// Hover body for the Health KPI cards whose minichart draws a shape but not
// the numbers behind it (sparklines, gauges): the card's `info` shows them.

export type BreakdownRow = { color?: string | null; label: string; value: string | number }

export function Breakdown({ title, right, rows, note }: { title: string; right?: string; rows: BreakdownRow[]; note?: string }) {
  return (
    <>
      <TooltipHeader right={right}>{title}</TooltipHeader>
      <div className="space-y-1">
        {rows.map((r) => (
          <TooltipRow key={r.label} color={r.color ?? null} label={r.label} value={r.value} />
        ))}
      </div>
      {note && <TooltipNote>{note}</TooltipNote>}
    </>
  )
}
