import type { CSSProperties, ReactNode } from 'react'
import { Link } from 'react-router-dom'

// Minicharts for KpiCard. One per metric, picked for what the metric has to
// say — a row of four cards where every chart is the same bar reads as a
// template. All of them take theme tokens for their neutrals so they work in
// both themes; status colours are the app's fixed status hexes.
//
// Every chart FILLS when its row mounts (the kb-enter-* classes in
// globals.css): arcs draw, bars grow, lines reveal left to right, cells and
// marks pop in. Only the value animates — tracks are already there.
//
// ONE green for chart fills: `ok`, the app's status-ok (Tailwind status.ok).
// The brand accent is deliberately not here — it is #00e07a in dark but
// #009a54 in light, so bars drawn with it sat next to status-green bars in a
// visibly different green on the light theme. The accent stays for buttons,
// links and the primary card's bevel.
export const KPI_COLOR = {
  ok: '#22d68a',
  warn: '#f5a623',
  err: '#ef4056',
  info: '#4c9aff',
  muted: 'var(--kb-text-tertiary)',
  track: 'var(--kb-bar-track)',
} as const

// Ends — the mono labels under a chart's two ends ("24h ago … now").
export function Ends({ left, right }: { left?: ReactNode; right?: ReactNode }) {
  return (
    <div className="mt-1.5 flex justify-between gap-3 text-[10.5px] font-mono text-kb-text-tertiary min-w-0">
      <span className="truncate">{left}</span>
      <span className="truncate text-right">{right}</span>
    </div>
  )
}

// Legend — dot + text lines beside a gauge or a ring.
export function Legend({ rows }: { rows: { color: string; label: ReactNode }[] }) {
  return (
    <div className="space-y-1 text-[11.5px] font-mono text-kb-text-secondary min-w-0">
      {rows.map((r, i) => (
        <div key={i} className="flex items-center gap-2 whitespace-nowrap">
          <span className="w-1.5 h-1.5 rounded-full shrink-0" style={{ background: r.color }} />
          <span className="truncate">{r.label}</span>
        </div>
      ))}
    </div>
  )
}

// Sparkline — a series over a window, the latest point marked in the accent.
// A flat series draws a midline: on a stable cluster that IS the reading
// ("steady"), and the ends say so instead of inventing movement. `fromZero`
// anchors the scale at 0 for rates, where fitting lo–hi would draw a 10 %
// wobble as a saw blade next to a sentence that says "steady".
export function Sparkline({ values, height = 46, window, left, right = 'now', fromZero }: { values: number[]; height?: number; window?: string; left?: ReactNode; right?: ReactNode; fromZero?: boolean }) {
  if (values.length < 2) return null
  const W = 300
  const lo = fromZero ? Math.min(0, ...values) : Math.min(...values)
  const hi = Math.max(...values)
  const span = hi - lo
  const pts = values.map((v, i) => [
    (i / (values.length - 1)) * W,
    span > 0 ? height - 5 - ((v - lo) / span) * (height - 12) : height / 2,
  ])
  const line = pts.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)},${y.toFixed(1)}`).join('')
  const [lx, ly] = pts[pts.length - 1]
  return (
    <div>
      <svg viewBox={`0 0 ${W} ${height}`} width="100%" height={height} preserveAspectRatio="none" className="overflow-visible block kb-enter-reveal" aria-hidden>
        <path d={`${line}L${W},${height}L0,${height}Z`} fill="var(--kb-text-primary)" opacity={0.05} />
        <path d={line} fill="none" stroke="var(--kb-text-tertiary)" strokeWidth={1.6} vectorEffect="non-scaling-stroke" />
        <circle cx={lx} cy={ly} r={3.6} fill={KPI_COLOR.ok} />
      </svg>
      <Ends left={left ?? `${window ? `${window} · ` : ''}${hi > Math.min(...values) ? `${endLabel(Math.min(...values))}–${endLabel(hi)}` : `steady at ${endLabel(hi)}`}`} right={right} />
    </div>
  )
}

// endLabel formats a sparkline's low/high for its ends line: thousands
// grouped, whole numbers from 10 up, one decimal below.
function endLabel(v: number): string {
  const d = Math.abs(v) >= 10 || Number.isInteger(v) ? 0 : 1
  return v.toLocaleString('en-US', { minimumFractionDigits: d, maximumFractionDigits: d })
}

// SemiGauge — a 0–100 score as a half ring, with its breakdown beside it.
export function SemiGauge({ percent, color, legend }: { percent: number; color: string; legend?: ReactNode }) {
  const r = 52
  const c = Math.PI * r
  const f = Math.max(0, Math.min(100, percent)) / 100 * c
  const arc = 'M10,62 A52,52 0 0 1 114,62'
  return (
    <div className="flex items-end gap-4 min-w-0">
      <svg width={124} height={68} viewBox="0 0 124 68" className="shrink-0" aria-hidden>
        <path d={arc} fill="none" stroke={KPI_COLOR.track} strokeWidth={9} strokeLinecap="round" />
        <path className="kb-enter-arc" d={arc} fill="none" stroke={color} strokeWidth={9} strokeLinecap="round" strokeDasharray={`${f} ${c}`} />
      </svg>
      {legend && <div className="pb-1 min-w-0">{legend}</div>}
    </div>
  )
}

// RingStat — a whole split into parts (e.g. critical vs high), legend beside.
export function RingStat({ parts, legend, size = 64 }: { parts: { value: number; color: string }[]; legend?: ReactNode; size?: number }) {
  const r = size / 2 - 7
  const c = 2 * Math.PI * r
  const total = parts.reduce((s, p) => s + p.value, 0) || 1
  let offset = 0
  return (
    <div className="flex items-center gap-4 min-w-0">
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} className="shrink-0 -rotate-90" aria-hidden>
        <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke={KPI_COLOR.track} strokeWidth={9} />
        {parts.map((p, i) => {
          const len = (p.value / total) * c
          const el = (
            <circle key={i} className="kb-enter-arc" cx={size / 2} cy={size / 2} r={r} fill="none" stroke={p.color} strokeWidth={9}
              strokeDasharray={`${len} ${c}`} strokeDashoffset={-offset} />
          )
          offset += len
          return el
        })}
      </svg>
      {legend && <div className="min-w-0">{legend}</div>}
    </div>
  )
}

// NodeRings — one row per node: a double ring (CPU inside, memory outside)
// and the two percentages. Shows the busiest few; the caption carries "+N".
export function NodeRings({ nodes }: { nodes: { name: string; cpu: number; mem: number }[] }) {
  const ring = (pct: number, color: string, r: number, w: number) => {
    const c = 2 * Math.PI * r
    return (
      <>
        <circle cx={20} cy={20} r={r} fill="none" stroke={KPI_COLOR.track} strokeWidth={w} />
        <circle className="kb-enter-arc" cx={20} cy={20} r={r} fill="none" stroke={color} strokeWidth={w} strokeLinecap="round"
          strokeDasharray={`${Math.max(1.5, (Math.min(100, pct) / 100) * c)} ${c}`} />
      </>
    )
  }
  return (
    <div className="space-y-2">
      {nodes.map((n) => (
        <div key={n.name} className="flex items-center gap-3 min-w-0">
          <svg width={34} height={34} viewBox="0 0 40 40" className="shrink-0 -rotate-90" aria-hidden>
            {ring(n.mem, n.mem >= 90 ? KPI_COLOR.warn : 'var(--kb-text-tertiary)', 16, 4)}
            {ring(n.cpu, n.cpu >= 90 ? KPI_COLOR.warn : KPI_COLOR.ok, 9.5, 4)}
          </svg>
          <span className="text-[12px] font-mono text-kb-text-primary truncate min-w-0 flex-1">{n.name}</span>
          <span className="text-[11.5px] font-mono text-kb-text-secondary whitespace-nowrap">
            {Math.round(n.cpu)}% cpu · {Math.round(n.mem)}% mem
          </span>
        </div>
      ))}
    </div>
  )
}

// StatusList — a short list of things, each with a state dot, a value and
// optionally a link (the clusters that need you, the open insights by rule).
export function StatusList({ rows }: { rows: { color: string; label: ReactNode; value?: ReactNode; valueColor?: string; to?: string }[] }) {
  return (
    <div>
      {rows.map((r, i) => {
        const inner = (
          <>
            <span className="flex items-center gap-2 min-w-0">
              <span className="w-1.5 h-1.5 rounded-full shrink-0" style={{ background: r.color }} />
              <span className="truncate">{r.label}</span>
            </span>
            {r.value != null && (
              <span className="shrink-0" style={r.valueColor ? { color: r.valueColor } : undefined}>{r.value}</span>
            )}
          </>
        )
        const cls = 'kb-enter-rise flex items-center justify-between gap-3 py-[5px] text-[12px] font-mono text-kb-text-primary border-b border-kb-border last:border-b-0 min-w-0'
        const enter = { '--kb-enter-d': `${i * 60}ms` } as CSSProperties
        return r.to ? (
          <Link key={i} to={r.to} className={`${cls} hover:text-kb-accent transition-colors`} style={enter}>{inner}</Link>
        ) : (
          <div key={i} className={cls} style={enter}>{inner}</div>
        )
      })}
    </div>
  )
}

// SplitBar — one whole in two or three parts (used vs idle spend).
// `hatched` marks the part the reader pays for without using.
export function SplitBar({ parts, left, right }: { parts: { value: number; color?: string; hatched?: boolean }[]; left?: ReactNode; right?: ReactNode }) {
  return (
    <div>
      <div className="h-2.5 rounded-full flex gap-0.5 overflow-hidden" style={{ background: KPI_COLOR.track }}>
        {parts.filter((p) => p.value > 0).map((p, i) => (
          <span
            key={i}
            className="kb-enter-grow h-full rounded-full"
            style={{
              flex: p.value,
              background: p.hatched
                ? 'repeating-linear-gradient(135deg, var(--kb-text-tertiary) 0 4px, transparent 4px 8px)'
                : p.color,
              opacity: p.hatched ? 0.55 : 1,
              '--kb-enter-d': `${i * 140}ms`,
            } as CSSProperties}
          />
        ))}
      </div>
      <Ends left={left} right={right} />
    </div>
  )
}

// UnitStrip — one cell per member of a set (the fleet's clusters), coloured
// by its state, with the legend underneath. Counts hide which ones; cells
// keep the shape of the set — four clusters read as four, forty as forty.
export function UnitStrip({ cells, legend }: { cells: { color: string; hatched?: boolean; title?: string }[]; legend?: ReactNode }) {
  const h = cells.length > 24 ? 12 : 18
  return (
    <div>
      <div className="flex gap-1">
        {cells.map((c, i) => (
          <span
            key={i}
            title={c.title}
            className="kb-enter-rise flex-1 min-w-[6px] rounded-[5px]"
            style={{
              height: h,
              background: c.hatched
                ? 'repeating-linear-gradient(135deg, var(--kb-text-tertiary) 0 3px, transparent 3px 7px)'
                : c.color,
              opacity: c.hatched ? 0.5 : 1,
              '--kb-enter-d': `${Math.min(i, 12) * 45}ms`,
            } as CSSProperties}
          />
        ))}
      </div>
      {legend && <div className="mt-2">{legend}</div>}
    </div>
  )
}

// BarList — a few named values as bars on one scale (pods per cluster).
// `max` fixes the scale (percentages against 100) instead of the largest row.
export function BarList({ rows, color = KPI_COLOR.ok, max: fixedMax }: { rows: { label: ReactNode; value: number; display?: ReactNode }[]; color?: string; max?: number }) {
  const max = fixedMax ?? Math.max(1, ...rows.map((r) => r.value))
  return (
    <div className="space-y-1.5">
      {rows.map((r, i) => (
        <div key={i} className="grid grid-cols-[minmax(0,max-content)_minmax(48px,1fr)_auto] items-center gap-2.5 text-[11.5px] font-mono">
          <span className="truncate text-kb-text-secondary">{r.label}</span>
          <span className="h-1.5 rounded-full" style={{ background: KPI_COLOR.track }}>
            <span
              className="kb-enter-grow block h-full rounded-full"
              style={{ width: `${Math.max(2, (r.value / max) * 100)}%`, background: color, '--kb-enter-d': `${i * 80}ms` } as CSSProperties}
            />
          </span>
          <span className="tabular-nums text-kb-text-primary text-right">{r.display ?? r.value}</span>
        </div>
      ))}
    </div>
  )
}

// EventTrack — when discrete events happened inside a window (OOMKills), as
// dots on a track. An empty track is a reading too: the ends still say which
// window was looked at, so "none" never reads as "not checked".
export function EventTrack({ events, from, to, color = KPI_COLOR.err, left, right = 'now' }: { events: number[]; from: number; to: number; color?: string; left?: ReactNode; right?: ReactNode }) {
  const span = to - from
  return (
    <div>
      <div className="relative h-4">
        <div className="absolute inset-x-0 top-1/2 h-[3px] -translate-y-1/2 rounded-full" style={{ background: KPI_COLOR.track }} />
        {span > 0 &&
          events
            .filter((t) => t >= from && t <= to)
            .map((t, i) => (
              <span
                key={i}
                className="kb-enter-pop absolute top-1/2 w-[9px] h-[9px] rounded-full -translate-x-1/2 -translate-y-1/2"
                style={{ left: `${((t - from) / span) * 100}%`, background: color, '--kb-enter-d': `${300 + Math.min(i, 10) * 40}ms` } as CSSProperties}
              />
            ))}
      </div>
      <Ends left={left} right={right} />
    </div>
  )
}
