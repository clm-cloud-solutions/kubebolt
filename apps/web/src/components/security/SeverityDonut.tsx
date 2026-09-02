// SeverityDonut — the shape of the problem, before its size.
//
// A column of counts makes the reader build the proportion in their head; a ring
// hands it over. That is the one thing the reference dashboards (GKE Security
// Posture, CAST AI) do better than a strip of numbers, and it costs no data we
// do not already have.
//
// Drawn with stroke-dasharray on concentric circles rather than a chart library:
// four arcs is not a charting problem, and the app already refuses to pull a
// dependency for something this small. The total sits in the middle because it
// is the number people read first and then decompose.
//
// Bands at zero are skipped entirely — a 0-length arc still renders a seam on
// some browsers, and a legend entry saying "0 low" is a line of text that
// carries nothing.

// Colours come from Tailwind classes, not CSS variables: the status palette
// lives in tailwind.config as literal values and has no --kb-status-* var, so
// var(--kb-status-error) would have silently painted black.
const BANDS = [
  { key: 'critical', stroke: 'stroke-status-error', swatch: 'bg-status-error', label: 'Critical' },
  { key: 'high', stroke: 'stroke-status-warn', swatch: 'bg-status-warn', label: 'High' },
  { key: 'medium', stroke: 'stroke-status-info', swatch: 'bg-status-info', label: 'Medium' },
  { key: 'low', stroke: 'stroke-kb-text-tertiary', swatch: 'bg-kb-text-tertiary', label: 'Low' },
] as const

export type DonutBand = { key: string; stroke: string; swatch: string; label: string }

// `bands` is overridable so a lens with its OWN vocabulary can keep it. Runtime
// events are graded on Falco's syslog ladder (Emergency…Debug), not on the CVE
// severity scale, and remapping them onto critical/high/medium would put words
// in the tool's mouth. Same ring, different legend.
export function SeverityDonut({
  counts,
  size = 104,
  bands: bandDefs = BANDS as readonly DonutBand[],
  unit = 'findings',
}: {
  counts: Record<string, number>
  size?: number
  bands?: readonly DonutBand[]
  unit?: string
}) {
  const bands = bandDefs.map((b) => ({ ...b, n: counts[b.key] ?? 0 })).filter((b) => b.n > 0)
  const total = bands.reduce((a, b) => a + b.n, 0)

  const stroke = 12
  const r = (size - stroke) / 2
  const circumference = 2 * Math.PI * r

  let offset = 0
  const arcs = bands.map((b) => {
    const frac = b.n / total
    const arc = {
      ...b,
      dash: `${frac * circumference} ${circumference}`,
      rotate: (offset / total) * 360 - 90,
    }
    offset += b.n
    return arc
  })

  return (
    <div className="flex items-center gap-4">
      <div className="relative shrink-0" style={{ width: size, height: size }}>
        <svg width={size} height={size} className="block">
          <circle
            cx={size / 2}
            cy={size / 2}
            r={r}
            fill="none"
            className="stroke-kb-elevated"
            strokeWidth={stroke}
          />
          {arcs.map((a) => (
            <circle
              key={a.key}
              cx={size / 2}
              cy={size / 2}
              r={r}
              fill="none"
              className={a.stroke}
              strokeWidth={stroke}
              strokeDasharray={a.dash}
              transform={`rotate(${a.rotate} ${size / 2} ${size / 2})`}
            />
          ))}
        </svg>
        <div className="absolute inset-0 flex flex-col items-center justify-center">
          <span className="text-lg font-semibold tabular-nums text-kb-text-primary leading-none">
            {total}
          </span>
          <span className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">{unit}</span>
        </div>
      </div>
      <div className="space-y-1 min-w-0">
        {bands.length === 0 ? (
          <span className="text-xs text-kb-text-tertiary">Nothing in this view</span>
        ) : (
          bands.map((b) => (
            <div key={b.key} className="flex items-center gap-2 text-xs">
              <span className={`w-2 h-2 rounded-sm shrink-0 ${b.swatch}`} aria-hidden />
              <span className="font-mono tabular-nums text-kb-text-primary w-10 text-right">
                {b.n}
              </span>
              <span className="text-kb-text-tertiary truncate">{b.label}</span>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
