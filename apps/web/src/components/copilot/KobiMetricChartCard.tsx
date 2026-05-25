import { useId, useMemo } from 'react'
import { Activity, MinusCircle } from 'lucide-react'
import {
  ResponsiveContainer,
  AreaChart,
  Area,
  LineChart,
  Line,
  XAxis,
  YAxis,
  Tooltip,
  ReferenceLine,
  CartesianGrid,
} from 'recharts'
import {
  formatValue,
  METRIC_ACCENTS,
  pickScale,
  type UnitKind,
  type UnitScale,
} from '@/components/shared/MetricChart'
import type {
  WorkloadMetricKey,
  WorkloadMetricsEntry,
  WorkloadMetricsResponse,
  WorkloadMetricsTrendPoint,
} from '@/services/copilot/types'

// Spec #08 V1 — snapshot of what Kobi just analyzed.
//
// Renders one card per get_workload_metrics tool result. The data comes
// pre-computed from the backend (no PromQL fetch here) so what the user
// sees in the chart matches exactly what Kobi cited in prose. The chart
// is intentionally read-only — no range selector, no refresh, no live
// stream. For "live monitoring" the user goes to the Monitor tab.
//
// Two visual modes per metric:
//   - aggregate-only: single sparkline + summary chips
//   - aggregate + per-container: top sparkline = workload total,
//       below it N compact rows (one per container, capped at 4)

// Containers shown before the "+N more" affordance kicks in. Beyond this
// the operator should call get_workload_metrics with kind=Pod for the
// specific pods of interest.
const MAX_PER_CONTAINER_ROWS = 4

// METRIC_ORDER controls the vertical stacking when a call requests multiple
// metrics. CPU first because operators read it first in the prose, memory
// second (the second most common question), network last (less frequent
// and conceptually distinct from compute saturation).
const METRIC_ORDER: WorkloadMetricKey[] = ['cpu', 'memory', 'network_rx', 'network_tx']

const METRIC_DISPLAY: Record<WorkloadMetricKey, { label: string; accent: string }> = {
  cpu: { label: 'CPU', accent: METRIC_ACCENTS.cpu[0] },
  memory: { label: 'Memory', accent: METRIC_ACCENTS.memory[0] },
  network_rx: { label: 'Network RX', accent: METRIC_ACCENTS.networkRxTx[0] },
  network_tx: { label: 'Network TX', accent: METRIC_ACCENTS.networkRxTx[1] },
}

interface Props {
  data: WorkloadMetricsResponse
}

export function KobiMetricChartCard({ data }: Props) {
  // Defensive guard: data.metrics could legally be null/undefined if the
  // backend ever changes shape; treat absent metrics as "no metrics".
  const metrics = data.metrics ?? {}
  const presentMetrics = METRIC_ORDER.filter((k) => metrics[k] != null)

  // Empty states — the header always renders so the operator sees the
  // workload the tool was called against, but no charts when there's
  // nothing to chart. Note the double `?.` on `trend` — the backend's
  // older versions could marshal trend as null when there were zero
  // samples (Go nil slice → JSON null); the regression sentinel test in
  // workload_metrics_executor_test.go pins the backend's non-nil
  // contract, but we keep the frontend defensive as belt-and-suspenders.
  const isEmpty =
    data.podsResolved === 0 ||
    presentMetrics.every((k) => (metrics[k]?.trend?.length ?? 0) === 0)

  return (
    <div className="rounded-lg border border-kb-border bg-kb-bg/40 overflow-hidden">
      <header className="flex items-center gap-2 px-3 py-2 border-b border-kb-border">
        <div className="w-6 h-6 rounded-lg bg-kb-bg flex items-center justify-center shrink-0">
          <Activity className="w-3.5 h-3.5 text-kb-accent" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-[11px] text-kb-text-tertiary font-mono uppercase tracking-wide">
            {formatHeaderIdentity(data)}
          </div>
          <div className="text-[10px] text-kb-text-tertiary">
            last {data.range}
            {formatScopeChip(data)}
            {data.note && <span className="ml-1">· {data.note}</span>}
          </div>
        </div>
      </header>

      {isEmpty ? (
        <EmptyState note={emptyStateNote(data)} />
      ) : (
        <div className="flex flex-col">
          {presentMetrics.map((key) => {
            const entry = metrics[key]
            if (!entry) return null
            return <MetricBlock key={key} metricKey={key} entry={entry} />
          })}
        </div>
      )}
    </div>
  )
}

function emptyStateNote(data: WorkloadMetricsResponse): string {
  if (data.podsResolved === 0) {
    return data.workload.kind === 'Node'
      ? 'Node not found in the queried window.'
      : 'No active pods in the queried window.'
  }
  return 'No samples in the queried range.'
}

// formatHeaderIdentity prints the workload identity in the uppercase mono
// strip at the top of the card. Nodes are cluster-scoped so we drop the
// "namespace/" prefix; everything else keeps the conventional "ns/name".
function formatHeaderIdentity(data: WorkloadMetricsResponse): string {
  if (data.workload.kind === 'Node') {
    return `Node · ${data.workload.name}`
  }
  return `${data.workload.kind} · ${data.workload.namespace}/${data.workload.name}`
}

// formatScopeChip renders the count + entity-type chip after the range.
// For nodes the count is always 0 or 1 — the chip stays singular and we
// drop the count when 1 (just "node"). For workloads/pods we keep the
// existing "N pods" phrasing.
function formatScopeChip(data: WorkloadMetricsResponse): React.ReactNode {
  if (data.workload.kind === 'Node') {
    // The header already names the node by identity; appending the count
    // would be redundant ("Node · X · 1 node"). Skip the chip entirely.
    return null
  }
  return (
    <>
      {' · '}
      {data.podsResolved} pod{data.podsResolved === 1 ? '' : 's'}
    </>
  )
}

function EmptyState({ note }: { note: string }) {
  return (
    <div className="flex items-center gap-2 px-3 py-4 text-[11px] text-kb-text-tertiary">
      <MinusCircle className="w-3.5 h-3.5 shrink-0" />
      <span>{note}</span>
    </div>
  )
}

// ─── One metric (CPU/Memory/Network*) — header strip + aggregate chart + optional per-container ───

interface MetricBlockProps {
  metricKey: WorkloadMetricKey
  entry: WorkloadMetricsEntry
}

function MetricBlock({ metricKey, entry }: MetricBlockProps) {
  const display = METRIC_DISPLAY[metricKey]
  const unitKind = vmUnitToChartUnit(entry.unit)
  // Trend defensively defaulted to [] so a backend that ever emits null
  // doesn't crash the chart-card render. Same belt-and-suspenders as the
  // empty-state check in the parent.
  const trend = entry.trend ?? []
  const summary = entry.summary ?? { min: 0, avg: 0, max: 0, p95: 0 }
  const scale = useMemo(() => {
    // Pick scale based on the larger of: max usage, limit (if any).
    // This keeps the y-axis in MiB when the workload is using bytes but
    // its limit is in GiB — the threshold line stays on-scale.
    const ceiling = Math.max(
      summary.max,
      entry.limit ?? 0,
      entry.request ?? 0,
    )
    return pickScale(ceiling || 1, unitKind)
  }, [summary.max, entry.limit, entry.request, unitKind])

  return (
    <div className="border-b border-kb-border last:border-b-0">
      <MetricStrip
        label={display.label}
        summary={summary}
        request={entry.request}
        limit={entry.limit}
        utilization={entry.utilizationPercent}
        scale={scale}
      />
      <ChartArea
        points={trend}
        accent={display.accent}
        request={entry.request}
        limit={entry.limit}
        scale={scale}
        height={80}
      />
      {entry.perContainer && Object.keys(entry.perContainer).length > 0 && (
        <PerContainerRows
          containers={entry.perContainer}
          accent={display.accent}
          scale={scale}
        />
      )}
    </div>
  )
}

interface MetricStripProps {
  label: string
  summary: WorkloadMetricsEntry['summary']
  request?: number
  limit?: number
  utilization?: WorkloadMetricsEntry['utilizationPercent']
  scale: UnitScale
}

function MetricStrip({
  label,
  summary,
  request,
  limit,
  utilization,
  scale,
}: MetricStripProps) {
  return (
    <div className="flex items-center justify-between gap-2 px-3 py-1.5">
      <div className="flex items-center gap-2">
        <span className="text-[10px] font-mono uppercase tracking-wider text-kb-text-secondary">
          {label}
        </span>
        <span className="text-[11px] text-kb-text-secondary">
          avg <span className="text-kb-text-primary font-medium">{formatValue(summary.avg, scale, true)}</span>
        </span>
        <span className="text-[11px] text-kb-text-secondary">
          max <span className="text-kb-text-primary font-medium">{formatValue(summary.max, scale, true)}</span>
        </span>
      </div>
      <div className="flex items-center gap-2">
        {limit != null && (
          <span className="text-[10px] text-kb-text-tertiary">
            limit {formatValue(limit, scale, true)}
          </span>
        )}
        {utilization?.vsLimit != null && (
          <UtilizationChip percent={utilization.vsLimit} threshold="limit" />
        )}
        {utilization?.vsRequest != null && utilization.vsLimit == null && (
          <UtilizationChip percent={utilization.vsRequest} threshold="request" />
        )}
        {!limit && !request && (
          <span className="text-[10px] text-kb-text-tertiary italic">no limits</span>
        )}
      </div>
    </div>
  )
}

function UtilizationChip({
  percent,
  threshold,
}: {
  percent: number
  threshold: 'request' | 'limit'
}) {
  // Color thresholds match the dashboard's RightSizingPanel convention:
  //   <70% → muted (under-using is fine)
  //   70–95% → warning (approaching the line)
  //   ≥ 95% → danger (already saturating)
  const colorClass =
    percent >= 95
      ? 'bg-kb-danger/15 text-kb-danger border-kb-danger/30'
      : percent >= 70
        ? 'bg-kb-warning/15 text-kb-warning border-kb-warning/30'
        : 'bg-kb-bg text-kb-text-tertiary border-kb-border'
  return (
    <span
      className={`text-[10px] font-mono px-1.5 py-0.5 rounded border ${colorClass}`}
      title={`${percent.toFixed(0)}% of ${threshold}`}
    >
      {percent.toFixed(0)}% / {threshold}
    </span>
  )
}

// ─── Sparkline ───────────────────────────────────────────────────────

interface ChartAreaProps {
  points: WorkloadMetricsTrendPoint[]
  accent: string
  request?: number
  limit?: number
  scale: UnitScale
  height: number
}

function ChartArea({ points, accent, request, limit, scale, height }: ChartAreaProps) {
  // useId gives a stable per-instance SVG gradient identifier so multiple
  // charts on the same screen don't fight over a single `<defs>` ID.
  const rawId = useId()
  const gradientId = `kobi-chart-grad-${rawId.replace(/:/g, '')}`

  // Recharts expects numeric timestamps for the X axis; convert ISO once
  // and memoize so the chart doesn't recompute on every parent re-render.
  const chartData = useMemo(() => {
    return points.map((p) => ({
      ts: new Date(p.t).getTime(),
      value: p.v,
      // Pre-scale here so tooltip and axis read from the same numbers
      // and don't drift if formatValue's rounding ever changes.
      scaled: p.v / scale.divisor,
    }))
  }, [points, scale.divisor])

  // Domain ends at max(observed, limit) so the threshold line is always
  // on-canvas. Floor of 0 because no metric we support goes negative.
  const yMax = Math.max(
    ...chartData.map((d) => d.scaled),
    (limit ?? 0) / scale.divisor,
  )

  return (
    <div style={{ width: '100%', height }} className="px-3 pb-2">
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={chartData} margin={{ top: 4, right: 8, bottom: 0, left: 8 }}>
          {/* Gradient mirrors MetricChart's 'area' convention (peak 0.3 →
              baseline 0). Densest near the trend line, fully transparent
              at the zero axis so the threshold lines stay legible. */}
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={accent} stopOpacity={0.3} />
              <stop offset="50%" stopColor={accent} stopOpacity={0.1} />
              <stop offset="100%" stopColor={accent} stopOpacity={0} />
            </linearGradient>
          </defs>
          <CartesianGrid stroke="var(--kb-border)" strokeOpacity={0.3} vertical={false} />
          <XAxis
            type="number"
            dataKey="ts"
            domain={['dataMin', 'dataMax']}
            tickFormatter={(v) => formatTimeTick(v as number)}
            tick={{ fontSize: 9, fill: 'var(--kb-text-tertiary)' }}
            axisLine={{ stroke: 'var(--kb-border)' }}
            tickLine={{ stroke: 'var(--kb-border)' }}
            minTickGap={40}
          />
          <YAxis
            type="number"
            domain={[0, yMax > 0 ? Math.ceil(yMax * 1.1 * 100) / 100 : 1]}
            tick={{ fontSize: 9, fill: 'var(--kb-text-tertiary)' }}
            tickFormatter={(v) => `${v}`}
            axisLine={false}
            tickLine={false}
            width={32}
          />
          <Tooltip
            content={(payload) => (
              <ChartTooltip
                active={payload.active}
                payload={payload.payload}
                label={payload.label}
                scale={scale}
                limit={limit}
              />
            )}
          />
          {request != null && (
            <ReferenceLine
              y={request / scale.divisor}
              stroke="var(--kb-text-tertiary)"
              strokeDasharray="3 3"
              strokeOpacity={0.6}
              label={{
                value: 'req',
                position: 'insideTopRight',
                fontSize: 9,
                fill: 'var(--kb-text-tertiary)',
              }}
            />
          )}
          {limit != null && (
            <ReferenceLine
              y={limit / scale.divisor}
              stroke="var(--kb-danger, #ef4444)"
              strokeDasharray="4 3"
              strokeOpacity={0.7}
              label={{
                value: 'limit',
                position: 'insideTopRight',
                fontSize: 9,
                fill: 'var(--kb-danger, #ef4444)',
              }}
            />
          )}
          <Area
            type="monotone"
            dataKey="scaled"
            stroke={accent}
            strokeWidth={1.5}
            fill={`url(#${gradientId})`}
            fillOpacity={1}
            isAnimationActive={false}
          />
        </AreaChart>
      </ResponsiveContainer>
    </div>
  )
}

function formatTimeTick(unixMs: number): string {
  const d = new Date(unixMs)
  return `${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
}

interface ChartTooltipProps {
  active?: boolean
  payload?: Array<{ payload?: { ts?: number; value?: number; scaled?: number } }>
  label?: string | number
  scale: UnitScale
  limit?: number
}

function ChartTooltip({ active, payload, scale, limit }: ChartTooltipProps) {
  if (!active || !payload || payload.length === 0) return null
  const datum = payload[0]?.payload
  if (!datum) return null
  const value = datum.value ?? 0
  const ts = datum.ts ?? 0
  const utilPct = limit && limit > 0 ? (value / limit) * 100 : null
  return (
    <div className="bg-kb-surface border border-kb-border rounded px-2 py-1.5 text-[10px] shadow-md">
      <div className="text-kb-text-tertiary font-mono">
        {new Date(ts).toLocaleTimeString()}
      </div>
      <div className="text-kb-text-primary font-medium mt-0.5">
        {formatValue(value, scale, true)}
      </div>
      {utilPct != null && (
        <div className="text-kb-text-secondary mt-0.5">
          {utilPct.toFixed(0)}% of limit
        </div>
      )}
    </div>
  )
}

// ─── Per-container rows ──────────────────────────────────────────────

interface PerContainerRowsProps {
  containers: NonNullable<WorkloadMetricsEntry['perContainer']>
  accent: string
  scale: UnitScale
}

function PerContainerRows({ containers, accent, scale }: PerContainerRowsProps) {
  // Sort by max usage descending so the dominant container is visually
  // first. Cap at MAX_PER_CONTAINER_ROWS and surface the count of hidden
  // ones — beyond that the operator should narrow to kind=Pod.
  const sorted = useMemo(() => {
    return Object.entries(containers).sort((a, b) => b[1].summary.max - a[1].summary.max)
  }, [containers])
  const visible = sorted.slice(0, MAX_PER_CONTAINER_ROWS)
  const hidden = sorted.length - visible.length

  return (
    <div className="border-t border-kb-border bg-kb-bg/30">
      <div className="px-3 py-1 text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary">
        per container
      </div>
      <div className="flex flex-col">
        {visible.map(([name, c]) => (
          <ContainerRow key={name} name={name} entry={c} accent={accent} scale={scale} />
        ))}
        {hidden > 0 && (
          <div className="px-3 py-1.5 text-[10px] text-kb-text-tertiary italic border-t border-kb-border">
            +{hidden} more container{hidden === 1 ? '' : 's'} hidden — narrow to kind=Pod for full detail
          </div>
        )}
      </div>
    </div>
  )
}

interface ContainerRowProps {
  name: string
  entry: { summary: WorkloadMetricsEntry['summary']; trend: WorkloadMetricsTrendPoint[] }
  accent: string
  scale: UnitScale
}

function ContainerRow({ name, entry, accent, scale }: ContainerRowProps) {
  // Defensive against null trend (same null-slice → JSON null pattern as
  // the top-level entry — KSM-less or empty containers could land here).
  const trend = entry.trend ?? []
  const summary = entry.summary ?? { min: 0, avg: 0, max: 0, p95: 0 }
  const data = useMemo(
    () => trend.map((p) => ({ ts: new Date(p.t).getTime(), scaled: p.v / scale.divisor })),
    [trend, scale.divisor],
  )
  return (
    <div className="grid grid-cols-[auto_1fr_auto] items-center gap-2 px-3 py-1.5 border-t border-kb-border first:border-t-0">
      <span className="text-[11px] font-mono text-kb-text-primary truncate max-w-[140px]" title={name}>
        {name}
      </span>
      <div style={{ height: 24 }}>
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={data} margin={{ top: 2, right: 0, bottom: 2, left: 0 }}>
            <Line
              type="monotone"
              dataKey="scaled"
              stroke={accent}
              strokeWidth={1.2}
              dot={false}
              isAnimationActive={false}
            />
          </LineChart>
        </ResponsiveContainer>
      </div>
      <span className="text-[10px] text-kb-text-secondary font-mono">
        max <span className="text-kb-text-primary">{formatValue(summary.max, scale, true)}</span>
      </span>
    </div>
  )
}

// ─── Unit kind mapping ───────────────────────────────────────────────

// The backend emits unit strings that almost match MetricChart's UnitKind
// but not exactly — backend uses `bytes/sec` while MetricChart uses
// `bytes/s`. Translate so the shared formatter can scale correctly.
function vmUnitToChartUnit(u: string): UnitKind {
  switch (u) {
    case 'cores':
      return 'cores'
    case 'bytes':
      return 'bytes'
    case 'bytes/sec':
      return 'bytes/s'
    default:
      return 'count'
  }
}
