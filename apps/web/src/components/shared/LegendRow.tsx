import { Link } from 'react-router-dom'
import { ArrowRight } from 'lucide-react'

// LegendRow — dot + label + value line used as the breakdown grammar
// in dashboard cards (ResourceUsage bullet legend, KPI ring cards).
// `labelWidth` aligns the value column across rows; `warn` appends an
// amber qualifier like "(over capacity)". Pass `to` to make the row a
// deep link into a pre-filtered list view — label and value brighten
// on hover and a small arrow appears, matching the "view all →"
// affordance the KPI headers already use.
interface LegendRowProps {
  color: string
  label: string
  value: string
  warn?: string
  labelWidth?: number
  to?: string
}

export function LegendRow({ color, label, value, warn, labelWidth = 64, to }: LegendRowProps) {
  const inner = (
    <>
      <span className="w-2 h-2 rounded-full shrink-0" style={{ background: color }} />
      <span
        className={`text-kb-text-secondary shrink-0 ${to ? 'group-hover:text-kb-text-primary transition-colors' : ''}`}
        style={{ width: labelWidth }}
      >
        {label}
      </span>
      <span
        className={`text-kb-text-tertiary truncate ${to ? 'group-hover:text-kb-text-secondary transition-colors' : ''}`}
      >
        {value}
      </span>
      {warn && <span className="text-status-warn shrink-0">({warn})</span>}
      {to && (
        <ArrowRight className="w-3 h-3 shrink-0 text-kb-text-tertiary opacity-0 group-hover:opacity-100 transition-opacity" />
      )}
    </>
  )

  if (to) {
    return (
      <Link to={to} className="flex items-center gap-2 text-xs font-mono group" title={`View ${label.toLowerCase()}`}>
        {inner}
      </Link>
    )
  }
  return <div className="flex items-center gap-2 text-xs font-mono">{inner}</div>
}
