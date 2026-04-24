import { useQueries } from '@tanstack/react-query'
import {
  ComposedChart,
  Area,
  Line,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
  ReferenceLine,
} from 'recharts'
import { useId, useMemo, useState } from 'react'

import { api, ApiError, type PromRangeResponse } from '@/services/api'
import { LoadingSpinner } from './LoadingSpinner'
import { ErrorState } from './ErrorState'

// ─── Public types ───────────────────────────────────────────────────────────

type UnitKind = 'bytes' | 'bytes/s' | 'cores' | 'count'

interface QuerySpec {
  query: string
  // Multiply each value by -1 (used to draw TX traffic below the zero axis
  // in combined RX/TX charts). Display/tooltip still shows absolute value.
  negate?: boolean
  // Prepended to each series name to disambiguate when multiple queries
  // return the same labels (e.g. "RX" vs "TX").
  prefix?: string
}

interface ReferenceLineSpec {
  y: number
  label: string
  color?: string
  // Optional override for the header toggle label. Defaults to the first
  // space-separated word of `label`. Useful when the label has punctuation
  // that doesn't make a clean toggle pill (e.g. "request / limit 100m").
  shortLabel?: string
}

interface RangeOption {
  label: string
  minutes: number
  step: string
}

interface MetricChartProps {
  title: string

  // One of these two must be provided.
  query?: string
  queries?: QuerySpec[]

  unit?: UnitKind

  transform?: (v: number) => number

  seriesLabel?: (labels: Record<string, string>, prefix?: string) => string

  referenceLines?: ReferenceLineSpec[]

  defaultRangeMinutes?: number
  rangeOptions?: RangeOption[]

  refetchMs?: number

  height?: number
  showStats?: boolean

  // Colors to use in order for each series. Falls back to DEFAULT_COLORS
  // once exhausted. Use this to give each chart a distinctive accent
  // (e.g. CPU amber, Memory blue) instead of everything defaulting to the
  // brand green.
  accents?: readonly string[]

  // "line" draws only the stroke (lighter, used for CPU / RAM / Network).
  // "area" draws stroke + gradient fill (used for volume-like metrics like
  // Filesystem). Defaults to "area" for backward compatibility.
  chartType?: 'line' | 'area'
}

// ─── Defaults ───────────────────────────────────────────────────────────────

const DEFAULT_RANGE_OPTIONS: RangeOption[] = [
  { label: '5m', minutes: 5, step: '15s' },
  { label: '15m', minutes: 15, step: '15s' },
  { label: '1h', minutes: 60, step: '30s' },
  { label: '6h', minutes: 360, step: '2m' },
  { label: '24h', minutes: 1440, step: '10m' },
]

const DEFAULT_COLORS = [
  '#22c55e', // green
  '#3b82f6', // blue
  '#a855f7', // violet
  '#ef4444', // red
  '#f97316', // orange
  '#06b6d4', // cyan
  '#ec4899', // pink
  '#eab308', // yellow
]

// Semantic palettes exported for call sites that want per-metric accents.
// First color is used for single-series charts or as the "primary" in
// multi-series. Remaining DEFAULT_COLORS are appended after to avoid
// collisions when a chart has many containers.
export const METRIC_ACCENTS = {
  cpu: ['#22c55e'],                      // green
  memory: ['#3b82f6'],                    // blue
  filesystem: ['#a855f7'],                // violet
  networkRxTx: ['#eab308', '#f97316'],   // yellow (RX), orange (TX) — warm family, visibly distinct
} as const

// ─── Formatting helpers ─────────────────────────────────────────────────────

interface UnitScale {
  divisor: number
  label: string
}

function pickScale(absMax: number, unit?: UnitKind): UnitScale {
  if (unit === 'bytes' || unit === 'bytes/s') {
    const suffix = unit === 'bytes/s' ? '/s' : ''
    if (absMax < 1024) return { divisor: 1, label: 'B' + suffix }
    if (absMax < 1024 * 1024) return { divisor: 1024, label: 'KiB' + suffix }
    if (absMax < 1024 * 1024 * 1024) return { divisor: 1024 * 1024, label: 'MiB' + suffix }
    return { divisor: 1024 * 1024 * 1024, label: 'GiB' + suffix }
  }
  if (unit === 'cores') {
    // Use millicores when every interesting value is below 100m.
    if (absMax > 0 && absMax < 0.1) return { divisor: 0.001, label: 'm' }
    return { divisor: 1, label: 'cores' }
  }
  return { divisor: 1, label: '' }
}

function formatValue(v: number | null | undefined, scale: UnitScale, useAbs = false): string {
  if (v == null || Number.isNaN(v)) return '—'
  const scaled = (useAbs ? Math.abs(v) : v) / scale.divisor
  const absScaled = Math.abs(scaled)
  let fixed: string
  if (absScaled >= 100) fixed = scaled.toFixed(0)
  else if (absScaled >= 10) fixed = scaled.toFixed(1)
  else if (absScaled >= 1) fixed = scaled.toFixed(2)
  else fixed = scaled.toFixed(3)
  return `${fixed}${scale.label ? ' ' + scale.label : ''}`
}

// Time formatters come in two shapes: axis (compact, for crowded
// tick labels) and tooltip (verbose, shown one at a time). Both
// branch on whether the chart's data range crosses midnight — in
// the 24h view the first and last label can sit on different days,
// and "23:10:00" → "06:06:40" → "23:10:00" with no date cue is
// ambiguous about which is yesterday/today/tomorrow.
function formatTimeAxis(unixSec: number, spansDays: boolean): string {
  const d = new Date(unixSec * 1000)
  const hh = d.getHours().toString().padStart(2, '0')
  const mm = d.getMinutes().toString().padStart(2, '0')
  if (spansDays) {
    // "MM/DD HH:MM" — drop seconds to keep the tick narrow.
    return `${d.getMonth() + 1}/${d.getDate()} ${hh}:${mm}`
  }
  const ss = d.getSeconds().toString().padStart(2, '0')
  return `${hh}:${mm}:${ss}`
}

function formatTimeTooltip(unixSec: number, spansDays: boolean): string {
  const d = new Date(unixSec * 1000)
  const hh = d.getHours().toString().padStart(2, '0')
  const mm = d.getMinutes().toString().padStart(2, '0')
  const ss = d.getSeconds().toString().padStart(2, '0')
  const time = `${hh}:${mm}:${ss}`
  if (spansDays) {
    return `${d.getMonth() + 1}/${d.getDate()} ${time}`
  }
  return time
}

function defaultSeriesLabel(labels: Record<string, string>, prefix?: string): string {
  const core =
    labels.container ||
    labels.interface ||
    labels.volume ||
    labels.node ||
    Object.entries(labels)
      .filter(([k]) => k !== '__name__')
      .map(([k, v]) => `${k}=${v}`)
      .join(', ') ||
    'series'
  return prefix ? `${prefix} ${core}` : core
}

// ─── Data shape ─────────────────────────────────────────────────────────────

interface ChartPoint {
  t: number
  [series: string]: number
}

interface SeriesInfo {
  name: string
  color: string
  // True when the query for this series had negate=true — the values are
  // inverted so they render below the zero line. Used to flip the gradient
  // direction so the fill is densest at the peak (away from zero), not
  // near the baseline.
  negated?: boolean
  current?: number
  min?: number
  max?: number
  avg?: number
}

// ─── Component ──────────────────────────────────────────────────────────────

export function MetricChart({
  title,
  query,
  queries,
  unit,
  transform,
  seriesLabel = defaultSeriesLabel,
  referenceLines,
  defaultRangeMinutes = 15,
  rangeOptions = DEFAULT_RANGE_OPTIONS,
  refetchMs = 15_000,
  height = 220,
  showStats = true,
  accents,
  chartType = 'area',
}: MetricChartProps) {
  const palette = accents && accents.length > 0
    ? [...accents, ...DEFAULT_COLORS.filter(c => !accents.includes(c))]
    : DEFAULT_COLORS
  const [rangeMinutes, setRangeMinutes] = useState(defaultRangeMinutes)
  const [hidden, setHidden] = useState<Set<string>>(new Set())
  const [hiddenRefs, setHiddenRefs] = useState<Set<string>>(new Set())
  const gradPrefix = useId().replace(/:/g, '') // unique prefix per chart instance
  const effectiveRefs = referenceLines?.filter(rl => !hiddenRefs.has(rl.label))

  const toggleRef = (label: string) => {
    setHiddenRefs(prev => {
      const next = new Set(prev)
      if (next.has(label)) next.delete(label)
      else next.add(label)
      return next
    })
  }

  const active = rangeOptions.find(r => r.minutes === rangeMinutes) ?? rangeOptions[1]
  const step = active.step

  const allQueries: QuerySpec[] = queries ?? (query ? [{ query }] : [])

  const results = useQueries({
    queries: allQueries.map((spec, idx) => ({
      queryKey: ['metrics-range', spec.query, rangeMinutes, step, idx],
      queryFn: (): Promise<PromRangeResponse> => {
        const end = Math.floor(Date.now() / 1000)
        const start = end - rangeMinutes * 60
        return api.queryMetricsRange({ query: spec.query, start, end, step })
      },
      refetchInterval: refetchMs,
      retry: (failureCount: number, err: unknown) => {
        if (err instanceof ApiError && err.status >= 400 && err.status < 500) return false
        return failureCount < 2
      },
    })),
  })

  const isLoading = results.some(r => r.isLoading)
  const error = results.find(r => r.error)?.error
  const refetchAll = () => results.forEach(r => r.refetch())

  const { points, series, scale } = useMemo(() => {
    const allSeries: SeriesInfo[] = []
    const pointsMap = new Map<number, ChartPoint>()
    let absMax = 0

    results.forEach((result, qIdx) => {
      const spec = allQueries[qIdx]
      const data = result.data?.data?.result ?? []

      // Track which series names we've already used across all queries to
      // make every line uniquely keyed.
      data.forEach(s => {
        const baseName = seriesLabel(s.metric, spec?.prefix)
        // Resolve collisions by appending an index.
        let name = baseName
        let n = 1
        while (allSeries.some(x => x.name === name)) {
          n++
          name = `${baseName} (${n})`
        }
        const color = palette[allSeries.length % palette.length]
        const info: SeriesInfo = { name, color, negated: !!spec?.negate }

        const seen: number[] = []
        s.values.forEach(([t, vStr]) => {
          let v = parseFloat(vStr)
          if (Number.isNaN(v)) return
          if (transform) v = transform(v)
          if (spec?.negate) v = -v
          seen.push(v)
          if (Math.abs(v) > absMax) absMax = Math.abs(v)
          let pt = pointsMap.get(t)
          if (!pt) {
            pt = { t }
            pointsMap.set(t, pt)
          }
          pt[name] = v
        })
        if (seen.length > 0) {
          info.current = seen[seen.length - 1]
          info.min = seen.reduce((a, b) => (a < b ? a : b))
          info.max = seen.reduce((a, b) => (a > b ? a : b))
          info.avg = seen.reduce((a, b) => a + b, 0) / seen.length
        }
        allSeries.push(info)
      })
    })

    // Fold reference lines into the scale domain so the axis accommodates them.
    effectiveRefs?.forEach(rl => {
      if (Math.abs(rl.y) > absMax) absMax = Math.abs(rl.y)
    })

    const scale = pickScale(absMax, unit)

    const sortedPoints = Array.from(pointsMap.values()).sort((a, b) => a.t - b.t)

    return { points: sortedPoints, series: allSeries, scale }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [results, allQueries, effectiveRefs, seriesLabel, transform, unit, accents])

  const toggleSeries = (name: string) => {
    setHidden(prev => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })
  }

  const visibleSeries = series.filter(s => !hidden.has(s.name))
  const hasData = points.length > 0 && series.length > 0

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3 flex-wrap">
        <h4 className="text-xs font-mono uppercase tracking-wider text-kb-text-secondary">
          {title}
        </h4>
        <div className="flex items-center gap-3">
          {/* Ref toggles are only meaningful when series are actually rendering. */}
          {hasData && referenceLines && referenceLines.length > 0 && (
            <div className="flex items-center gap-1.5">
              {referenceLines.map((rl) => {
                const shortLabel = rl.shortLabel ?? rl.label.split(' ')[0]
                const visible = !hiddenRefs.has(rl.label)
                const color = rl.color ?? 'var(--kb-text-tertiary)'
                return (
                  <button
                    key={rl.label}
                    onClick={() => toggleRef(rl.label)}
                    className={`group flex items-center gap-1.5 px-2 py-1 rounded border text-[10px] font-mono transition-all ${
                      visible
                        ? 'border-kb-border bg-kb-elevated/40 text-kb-text-primary hover:border-kb-border-active'
                        : 'border-kb-border text-kb-text-tertiary opacity-60 hover:opacity-100'
                    }`}
                    title={`${visible ? 'Hide' : 'Show'}: ${rl.label}`}
                  >
                    <span
                      className="relative inline-block w-6 h-[1px]"
                      style={{
                        backgroundImage: visible
                          ? `repeating-linear-gradient(to right, ${color} 0, ${color} 3px, transparent 3px, transparent 6px)`
                          : 'repeating-linear-gradient(to right, var(--kb-border) 0, var(--kb-border) 3px, transparent 3px, transparent 6px)',
                        height: '1.5px',
                      }}
                    />
                    <span className="capitalize">{shortLabel}</span>
                  </button>
                )
              })}
            </div>
          )}
          {/* Range selector is always visible — otherwise a wide-range
              query that returns no data traps the user with no way back
              to a working window. */}
          <div className="flex items-center gap-1">
            {rangeOptions.map(opt => {
              const selected = opt.minutes === rangeMinutes
              return (
                <button
                  key={opt.minutes}
                  onClick={() => setRangeMinutes(opt.minutes)}
                  className={`px-2 py-0.5 text-[10px] font-mono rounded border transition-colors ${
                    selected
                      ? 'bg-kb-accent/20 border-kb-accent text-kb-accent font-semibold'
                      : 'border-kb-border text-kb-text-secondary hover:border-kb-border-active'
                  }`}
                >
                  {opt.label}
                </button>
              )
            })}
          </div>
        </div>
      </div>

      {isLoading && <LoadingSpinner size="sm" />}

      {error && !isLoading && (
        <ErrorState
          title="Chart data unavailable"
          message={error instanceof Error ? error.message : 'Unknown error'}
          onRetry={refetchAll}
        />
      )}

      {!isLoading && !error && !hasData && (
        <div className="flex flex-col items-center justify-center py-8 gap-1 text-xs text-kb-text-secondary">
          <span>No data in the selected range.</span>
          <span className="text-[11px] text-kb-text-tertiary">
            Try a narrower window — the pod may be younger than {active.label}, or the agent may still be warming up.
          </span>
        </div>
      )}

      {!isLoading && !error && hasData && (() => {
        // Detect whether the data range crosses a calendar-day
        // boundary, so tick/tooltip formatters can include a date
        // when needed. 24h and 7d ranges almost always cross
        // midnight; 5m-1h ranges almost never do.
        const first = points[0]?.t ?? 0
        const last = points[points.length - 1]?.t ?? 0
        const spansDays =
          first && last
            ? new Date(first * 1000).toDateString() !== new Date(last * 1000).toDateString()
            : false
        const axisFmt = (v: number) => formatTimeAxis(v, spansDays)
        const tipFmt = (v: number) => formatTimeTooltip(v, spansDays)
        return (
        <div className={`grid gap-3 ${showStats && series.length > 0 ? 'lg:grid-cols-[1fr_130px]' : 'grid-cols-1'}`}>
          <div style={{ height: `clamp(160px, 30vh, ${height}px)` }} className="w-full">
            <ResponsiveContainer width="100%" height="100%">
              <ComposedChart data={points} margin={{ top: 4, right: 8, left: 8, bottom: 4 }}>
                {chartType === 'area' && (
                  <defs>
                    {series.map((s, i) => (
                      // Negated series render below the zero line; Recharts
                      // applies the gradient top-to-bottom of the whole
                      // chart, so reverse the stops to keep the fill
                      // densest near the series peak (away from zero).
                      <linearGradient key={`g-${i}`} id={`${gradPrefix}-${i}`} x1="0" y1="0" x2="0" y2="1">
                        {s.negated ? (
                          <>
                            <stop offset="0%" stopColor={s.color} stopOpacity={0} />
                            <stop offset="50%" stopColor={s.color} stopOpacity={0.1} />
                            <stop offset="100%" stopColor={s.color} stopOpacity={0.3} />
                          </>
                        ) : (
                          <>
                            <stop offset="0%" stopColor={s.color} stopOpacity={0.3} />
                            <stop offset="50%" stopColor={s.color} stopOpacity={0.1} />
                            <stop offset="100%" stopColor={s.color} stopOpacity={0} />
                          </>
                        )}
                      </linearGradient>
                    ))}
                  </defs>
                )}
                <CartesianGrid strokeDasharray="3 3" stroke="var(--kb-border)" opacity={0.25} />
                <XAxis
                  dataKey="t"
                  type="number"
                  domain={['dataMin', 'dataMax']}
                  tickFormatter={axisFmt}
                  tick={{ fill: 'var(--kb-text-secondary)', fontSize: 10 }}
                  stroke="var(--kb-border)"
                  tickCount={5}
                  minTickGap={40}
                  // Push the first/last ticks inward so they don't
                  // overlap the Y-axis value labels at the bottom-left
                  // corner (visible as a "126 KiB/s 06:19:30" stacked
                  // glyph before this was added).
                  padding={{ left: 24, right: 8 }}
                />
                <YAxis
                  tickFormatter={(v) => formatValue(v, scale, true)}
                  tick={{ fill: 'var(--kb-text-secondary)', fontSize: 10 }}
                  stroke="var(--kb-border)"
                  width={60}
                  // Extend domain to include reference lines so they stay
                  // visible. 10% headroom prevents the dashed line from
                  // being clipped at the chart edge.
                  domain={[
                    (dataMin: number) => {
                      const refMin = Math.min(0, ...(effectiveRefs?.map(r => r.y) ?? [0]))
                      return Math.min(dataMin, refMin) * 1.05
                    },
                    (dataMax: number) => {
                      const refMax = Math.max(0, ...(effectiveRefs?.map(r => r.y) ?? [0]))
                      return Math.max(dataMax, refMax) * 1.1
                    },
                  ]}
                />
                <Tooltip
                  cursor={{ stroke: 'var(--kb-border-active)', strokeWidth: 1 }}
                  content={({ active, payload, label }) => {
                    if (!active || !payload?.length) return null
                    return (
                      <div className="bg-kb-elevated/95 backdrop-blur border border-kb-border rounded-md px-3 py-2 text-[11px] shadow-xl min-w-[160px]">
                        <div className="text-kb-text-primary font-mono font-semibold text-[12px] tabular-nums mb-2 pb-1.5 border-b border-kb-border/60">
                          {tipFmt(label as number)}
                        </div>
                        <div className="space-y-1">
                          {payload.map((p, i) => (
                            <div key={i} className="flex items-center gap-2">
                              <span
                                className="w-2 h-2 rounded-full flex-shrink-0"
                                style={{ background: p.color as string }}
                              />
                              <span className="text-kb-text-secondary truncate max-w-[140px]">
                                {p.name}
                              </span>
                              <span className="ml-auto tabular-nums font-mono text-kb-text-primary">
                                {formatValue(p.value as number, scale, true)}
                              </span>
                            </div>
                          ))}
                        </div>
                      </div>
                    )
                  }}
                />
                {/* Zero divider for charts that have negated series
                    (e.g. network RX up / TX down). Subtle so it frames the
                    baseline without competing with the data. */}
                {allQueries.some(q => q.negate) && (
                  <ReferenceLine
                    y={0}
                    stroke="var(--kb-text-tertiary)"
                    strokeWidth={1}
                    strokeOpacity={0.35}
                    ifOverflow="visible"
                  />
                )}
                {effectiveRefs?.map((rl, i) => (
                  <ReferenceLine
                    key={`ref-${i}`}
                    y={rl.y}
                    stroke={rl.color ?? 'var(--kb-text-tertiary)'}
                    strokeDasharray="4 4"
                    strokeWidth={1.25}
                    ifOverflow="extendDomain"
                    label={{
                      value: rl.label,
                      position: 'insideTopRight',
                      fill: rl.color ?? 'var(--kb-text-tertiary)',
                      fontSize: 10,
                    }}
                  />
                ))}
                {series.map((s, i) =>
                  chartType === 'area' ? (
                    <Area
                      key={s.name}
                      type="monotone"
                      dataKey={s.name}
                      stroke={s.color}
                      strokeWidth={1.75}
                      fill={`url(#${gradPrefix}-${i})`}
                      fillOpacity={1}
                      dot={false}
                      isAnimationActive={false}
                      connectNulls
                      hide={hidden.has(s.name)}
                    />
                  ) : (
                    <Line
                      key={s.name}
                      type="monotone"
                      dataKey={s.name}
                      stroke={s.color}
                      strokeWidth={1.75}
                      dot={false}
                      isAnimationActive={false}
                      connectNulls
                      hide={hidden.has(s.name)}
                    />
                  ),
                )}
              </ComposedChart>
            </ResponsiveContainer>
          </div>

          {showStats && series.length > 0 && (
            <StatsPanel
              series={series}
              visibleSeries={visibleSeries}
              scale={scale}
              onToggle={toggleSeries}
              hidden={hidden}
            />
          )}
        </div>
        )
      })()}
    </div>
  )
}

// ─── Stats panel ────────────────────────────────────────────────────────────

function StatsPanel({
  series,
  scale,
  onToggle,
  hidden,
}: {
  series: SeriesInfo[]
  visibleSeries: SeriesInfo[]
  scale: UnitScale
  onToggle: (name: string) => void
  hidden: Set<string>
}) {
  const singleSeries = series.length === 1
  return (
    <div className="text-[10px] font-mono overflow-y-auto max-h-[220px] space-y-1.5">
      {series.map(s => {
        const isHidden = hidden.has(s.name)
        return (
          <button
            key={s.name}
            onClick={singleSeries ? undefined : () => onToggle(s.name)}
            disabled={singleSeries}
            className={`w-full text-left px-2 py-1.5 rounded border transition-all ${
              isHidden
                ? 'border-kb-border opacity-40 hover:opacity-70'
                : singleSeries
                  ? 'border-kb-border cursor-default'
                  : 'border-kb-border hover:border-kb-border-active cursor-pointer'
            }`}
            title={singleSeries ? undefined : isHidden ? 'Click to show' : 'Click to hide'}
          >
            {!singleSeries && (
              <div className="flex items-center gap-1.5 mb-1 truncate">
                <span
                  className="w-1.5 h-1.5 rounded-full flex-shrink-0"
                  style={{ background: s.color }}
                />
                <span className="truncate text-kb-text-primary text-[10px]">{s.name}</span>
              </div>
            )}
            <div className="space-y-0.5">
              <StatRow label="now" value={formatValue(s.current, scale, true)} emphasize />
              <StatRow label="avg" value={formatValue(s.avg, scale, true)} />
              <StatRow label="max" value={formatValue(s.max, scale, true)} />
              <StatRow label="min" value={formatValue(s.min, scale, true)} />
            </div>
          </button>
        )
      })}
    </div>
  )
}

function StatRow({ label, value, emphasize }: { label: string; value: string; emphasize?: boolean }) {
  return (
    <div className="flex items-baseline justify-between gap-1">
      <span className="text-kb-text-tertiary">{label}</span>
      <span className={`tabular-nums ${emphasize ? 'text-kb-text-primary' : 'text-kb-text-secondary'}`}>
        {value}
      </span>
    </div>
  )
}
