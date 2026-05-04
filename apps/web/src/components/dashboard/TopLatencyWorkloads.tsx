import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { Timer } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { collapsePodToWorkload } from '@/utils/promql'
import {
  ClassTooltipRows,
  EMPTY_DIST,
  buildDistIndex,
  distErrorRate,
  useWorkloadStatusDist,
  type StatusDistribution,
} from './StatusDistribution'

// TopLatencyWorkloads — the latency lens on cluster reliability.
// Top Workloads · Traffic answers "who's loaded?", Error Hot-spots
// answers "who's broken?", and this one answers "who's slow?". A
// service that returns 200 OK in 2 seconds is breaking the user
// experience just as surely as one returning 5xx, and neither of
// the other panels surfaces it.
//
// Two queries:
//   1. avg latency per workload — the ranking metric. PromQL's
//      sum/count division yields NaN for workloads with no
//      requests in the window; topk filters those out naturally.
//   2. req/s per workload — secondary signal shown next to the
//      latency. A workload at 800ms avg with 500 req/s is a real
//      problem; the same latency at 0.05 req/s is one slow request
//      pulling the average and probably noise. The number lets the
//      operator weight the signal.
//
// Latency coloring follows fixed thresholds (≤100ms green, ≤500ms
// amber, >500ms red) — same reasoning as ErrorRateChip in
// TopWorkloadsTraffic. Relative scaling would make a hot cluster
// look healthier than it is.
//
// Honest "avg" labeling everywhere: agent emits sum_seconds +
// count, no histogram buckets, so percentiles aren't possible
// (yet). Tooltip says "avg latency" not "p50" — naming the metric
// for what it actually is.

const TOP_N = 10
const REFRESH_MS = 30_000
const TREND_POINTS = 40
const TREND_STEP_MIN_S = 15

interface LatencyRow {
  namespace: string
  workload: string
  avgLatencyMs: number
  reqRate: number
  trend: number[] // avg latency in ms over time, TREND_POINTS evenly spaced
  // Status-class breakdown of the workload's traffic over the
  // same window. Same shape used by TopWorkloadsTraffic — a
  // workload that's both slow AND erroring tells a different
  // story than slow-but-successful.
  distribution: StatusDistribution
}

interface Props {
  rangeMinutes: number
}

export function TopLatencyWorkloads({ rangeMinutes }: Props) {
  const RATE_WINDOW = `${rangeMinutes}m`
  const trendStepS = Math.max(TREND_STEP_MIN_S, Math.floor((rangeMinutes * 60) / TREND_POINTS))
  const trendWindow = `${trendStepS * 2}s`

  // topk on the latency expression. Workloads with no requests
  // produce NaN (sum/count = 0/0), which topk skips, so we don't
  // need an extra "had requests" filter.
  const topkLatencyQuery =
    `topk(${TOP_N},`
    + ` sum by (dst_namespace, workload) (${collapsePodToWorkload(
      `rate(pod_flow_http_latency_seconds_sum{source="hubble"}[${RATE_WINDOW}])`,
    )})`
    + ` / `
    + `sum by (dst_namespace, workload) (${collapsePodToWorkload(
      `rate(pod_flow_http_latency_seconds_count{source="hubble"}[${RATE_WINDOW}])`,
    )})`
    + `)`

  // Per-workload req/s — secondary context for the row.
  const reqRateQuery = `sum by (dst_namespace, workload) (${collapsePodToWorkload(
    `rate(pod_flow_http_requests_total{source="hubble"}[${RATE_WINDOW}])`,
  )})`

  // Range query for the per-row latency sparkline. Same divide
  // pattern but unwrapped so VM evaluates it at each step.
  const trendQuery =
    `sum by (dst_namespace, workload) (${collapsePodToWorkload(
      `rate(pod_flow_http_latency_seconds_sum{source="hubble"}[${trendWindow}])`,
    )})`
    + ` / `
    + `sum by (dst_namespace, workload) (${collapsePodToWorkload(
      `rate(pod_flow_http_latency_seconds_count{source="hubble"}[${trendWindow}])`,
    )})`

  const latQ = useQuery({
    queryKey: ['reliability', 'top-latency', 'topk', RATE_WINDOW],
    queryFn: () => api.queryMetrics({ query: topkLatencyQuery }),
    refetchInterval: REFRESH_MS,
    retry: false,
  })
  const reqQ = useQuery({
    queryKey: ['reliability', 'top-latency', 'req', RATE_WINDOW],
    queryFn: () => api.queryMetrics({ query: reqRateQuery }),
    refetchInterval: REFRESH_MS,
    retry: false,
  })
  const trendQ = useQuery({
    queryKey: ['reliability', 'top-latency', 'trend', RATE_WINDOW, trendStepS],
    queryFn: () => {
      const end = Math.floor(Date.now() / 1000)
      const start = end - rangeMinutes * 60
      return api.queryMetricsRange({
        query: trendQuery,
        start,
        end,
        step: `${trendStepS}s`,
      })
    },
    refetchInterval: REFRESH_MS,
    retry: false,
  })

  // Same shared hook as TopWorkloadsTraffic — TanStack Query
  // dedupes the fetch via the matching queryKey, so both panels
  // pay for one round-trip to VM regardless of how many are
  // mounted simultaneously.
  const distQ = useWorkloadStatusDist(rangeMinutes)

  const isLoading = latQ.isLoading
  const error = latQ.error

  const reqIndex = useMemo(() => buildReqIndex(reqQ.data?.data?.result), [reqQ.data])
  const trendIndex = useMemo(
    () => buildTrendIndex(trendQ.data?.data?.result, TREND_POINTS),
    [trendQ.data],
  )
  const distIndex = useMemo(() => buildDistIndex(distQ.data?.data?.result), [distQ.data])

  const rows: LatencyRow[] = (latQ.data?.data?.result ?? [])
    .map((s) => {
      const namespace = s.metric.dst_namespace ?? ''
      const workload = s.metric.workload ?? ''
      const latencySec = parseFloat(s.value?.[1] ?? 'NaN')
      if (!Number.isFinite(latencySec) || latencySec <= 0) return null
      const k = `${namespace}/${workload}`
      const reqRate = reqIndex.get(k) ?? 0
      const trend = trendIndex.get(k) ?? new Array<number>(TREND_POINTS).fill(0)
      const distribution = distIndex.get(k) ?? EMPTY_DIST
      return {
        namespace,
        workload,
        avgLatencyMs: latencySec * 1000,
        reqRate,
        trend,
        distribution,
      }
    })
    .filter((r): r is LatencyRow => r !== null && r.workload !== '')
    .sort((a, b) => b.avgLatencyMs - a.avgLatencyMs)

  // Build Kobi payload from visible rows. Includes the status
  // breakdown when present — a slow workload that's also erroring
  // tells the LLM where to look first.
  const kobiRows = rows.map((r) => {
    const blob: Record<string, string | number> = {
      workload: `${r.namespace}/${r.workload}`,
      avg_latency_ms: roundTo(r.avgLatencyMs, 1),
      req_per_sec: roundTo(r.reqRate, 2),
    }
    const errPct = distErrorRate(r.distribution)
    if (Number.isFinite(errPct)) blob.error_rate_pct = roundTo(errPct, 2)
    if (r.distribution.success > 0) blob.rate_2xx = roundTo(r.distribution.success, 2)
    if (r.distribution.redirect > 0) blob.rate_3xx = roundTo(r.distribution.redirect, 2)
    if (r.distribution.clientErr > 0) blob.rate_4xx = roundTo(r.distribution.clientErr, 2)
    if (r.distribution.serverErr > 0) blob.rate_5xx = roundTo(r.distribution.serverErr, 2)
    return blob
  })

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-kb-text-secondary shrink-0">
            <Timer className="w-4 h-4" />
          </span>
          <h4 className="text-sm font-semibold text-kb-text-primary truncate">
            Top Workloads · Latency
          </h4>
          {rows.length > 0 && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'top_latency',
                rangeLabel: RATE_WINDOW,
                rows: kobiRows,
              }}
              variant="icon"
              label="Ask Kobi about slow workloads"
            />
          )}
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
          avg over {RATE_WINDOW} · top {TOP_N}
        </span>
      </div>

      {isLoading && (
        <div className="py-6">
          <LoadingSpinner size="sm" />
        </div>
      )}

      {error && !isLoading && (
        <div className="text-[11px] text-status-warn font-mono py-3">
          Query failed — VictoriaMetrics unreachable or Hubble metrics not yet shipped.
        </div>
      )}

      {!isLoading && !error && rows.length === 0 && (
        <div className="text-[11px] text-kb-text-tertiary py-3">
          No HTTP traffic with latency samples in the selected range. Widen the range or
          generate some requests to see latency rankings.
        </div>
      )}

      {!isLoading && !error && rows.length > 0 && (
        <ul className="space-y-2">
          {rows.map((r, i) => (
            <li key={`${r.namespace}/${r.workload}`}>
              <LatencyRowEl row={r} rank={i + 1} />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function LatencyRowEl({ row, rank }: { row: LatencyRow; rank: number }) {
  const color = latencyColor(row.avgLatencyMs)
  // Min / max from the trend array — gives the user a sense of
  // latency variability without a new query. Filter zeros (gaps
  // when a workload had no requests during that window) so the
  // min reflects observed traffic, not "nothing happened".
  const observedSamples = row.trend.filter((v) => v > 0)
  const latencyMin = observedSamples.length > 0 ? Math.min(...observedSamples) : NaN
  const latencyMax = observedSamples.length > 0 ? Math.max(...observedSamples) : NaN
  // Suppress the range label when min ≈ max (within 5% of avg) —
  // a flat curve has no story to tell, and showing "1.1..1.1" is
  // just noise.
  const variabilityVisible =
    Number.isFinite(latencyMin)
    && Number.isFinite(latencyMax)
    && row.avgLatencyMs > 0
    && (latencyMax - latencyMin) / row.avgLatencyMs > 0.05
  return (
    <HoverTooltip body={<RowTooltip row={row} latencyMin={latencyMin} latencyMax={latencyMax} />}>
      <div className="flex items-center gap-3 px-2 py-1.5 rounded transition-colors hover:bg-kb-card-hover">
        <span className="text-[10px] font-mono text-kb-text-tertiary w-4 text-right tabular-nums">
          {rank}
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-1.5 truncate">
            <span className="text-xs text-kb-text-primary truncate">{row.workload}</span>
            <span className="text-[10px] font-mono text-kb-text-tertiary truncate">
              {row.namespace}
            </span>
          </div>
          {/* Mid row carries the latency-specific signals only.
              Status_class breakdown is intentionally NOT here —
              Top Workloads · Traffic right above already shows
              the same workloads with the same dist bar and chips,
              so repeating them here was pure visual duplication.
              What stays unique to this panel: how the latency
              moved over the window (sparkline, prominent at
              160×20) and how stable it was (min..max range
              text). */}
          <div className="flex items-center gap-2 mt-1">
            <Sparkline values={row.trend} width={160} height={20} color={color} />
            {variabilityVisible && (
              <span className="text-[10px] font-mono text-kb-text-tertiary tabular-nums shrink-0">
                {formatLatency(latencyMin)}..{formatLatency(latencyMax)}
              </span>
            )}
            <span className="text-[10px] font-mono text-kb-text-tertiary tabular-nums shrink-0 ml-auto">
              {formatRate(row.reqRate)}
            </span>
          </div>
        </div>
        <span
          className="text-[12px] font-mono shrink-0 tabular-nums font-semibold"
          style={{ color }}
        >
          {formatLatency(row.avgLatencyMs)}
        </span>
      </div>
    </HoverTooltip>
  )
}

function RowTooltip({
  row,
  latencyMin,
  latencyMax,
}: {
  row: LatencyRow
  latencyMin: number
  latencyMax: number
}) {
  const errPct = distErrorRate(row.distribution)
  return (
    <>
      <TooltipHeader right={row.namespace}>{row.workload}</TooltipHeader>
      <div className="space-y-1">
        <TooltipRow
          color={latencyColor(row.avgLatencyMs)}
          label="Avg latency"
          value={formatLatency(row.avgLatencyMs)}
        />
        {Number.isFinite(latencyMin) && (
          <TooltipRow color={null} label="Min" value={formatLatency(latencyMin)} />
        )}
        {Number.isFinite(latencyMax) && (
          <TooltipRow color={null} label="Max" value={formatLatency(latencyMax)} />
        )}
        <TooltipRow color="#94a3b8" label="Requests" value={formatRate(row.reqRate)} />
        {Number.isFinite(errPct) && (
          <TooltipRow
            color={errPct < 1 ? '#22c55e' : errPct < 5 ? '#f59e0b' : '#ef4056'}
            label="Error rate"
            value={`${errPct.toFixed(errPct < 1 ? 2 : 1)}%`}
          />
        )}
        <div className="h-px bg-kb-border/60 my-1.5" />
        <ClassTooltipRows dist={row.distribution} />
      </div>
    </>
  )
}

// Sparkline — same shape as TopWorkloadsTraffic's, but accepts a
// color so the curve picks up the row's latency color (green /
// amber / red). When all-zero, draws a dim baseline.
function Sparkline({
  values,
  width,
  height,
  color,
}: {
  values: number[]
  width: number
  height: number
  color: string
}) {
  const max = Math.max(...values, 0)
  const hasShape = max > 0 && values.some((v) => v > 0)
  if (!hasShape) {
    return (
      <svg width={width} height={height} aria-hidden className="shrink-0">
        <line
          x1={0}
          y1={height - 1}
          x2={width}
          y2={height - 1}
          stroke="var(--kb-text-tertiary)"
          strokeOpacity={0.4}
          strokeWidth={1}
        />
      </svg>
    )
  }
  const padY = 1
  const usableH = height - padY * 2
  const stepX = values.length > 1 ? width / (values.length - 1) : 0
  const points = values.map((v, i) => {
    const x = i * stepX
    const y = padY + (1 - v / max) * usableH
    return `${x.toFixed(1)},${y.toFixed(1)}`
  })
  const path = `M ${points.join(' L ')}`
  const lastX = (values.length - 1) * stepX
  const fillPath = `${path} L ${lastX.toFixed(1)},${height} L 0,${height} Z`
  return (
    <svg width={width} height={height} aria-hidden className="shrink-0">
      <path d={fillPath} fill={color} fillOpacity={0.18} />
      <path d={path} stroke={color} strokeWidth={1} fill="none" strokeLinejoin="round" />
    </svg>
  )
}

// ─── Helpers ────────────────────────────────────────────────────

function buildReqIndex(
  result: Array<{ metric: Record<string, string>; value?: [number, string] }> | undefined,
): Map<string, number> {
  const map = new Map<string, number>()
  if (!result) return map
  for (const s of result) {
    const ns = s.metric.dst_namespace
    const wl = s.metric.workload
    if (!ns || !wl) continue
    const v = parseFloat(s.value?.[1] ?? '0')
    if (Number.isFinite(v)) map.set(`${ns}/${wl}`, v)
  }
  return map
}

function buildTrendIndex(
  result: Array<{ metric: Record<string, string>; values?: Array<[number, string]> }> | undefined,
  targetPoints: number,
): Map<string, number[]> {
  const map = new Map<string, number[]>()
  if (!result) return map
  for (const s of result) {
    const ns = s.metric.dst_namespace
    const wl = s.metric.workload
    if (!ns || !wl) continue
    const raw = (s.values ?? [])
      .map((p) => parseFloat(p[1]))
      // Convert seconds → ms here so the sparkline domain matches
      // what the user sees in the row label.
      .map((v) => (Number.isFinite(v) ? v * 1000 : 0))
    if (raw.length === 0) continue
    map.set(`${ns}/${wl}`, resampleTo(raw, targetPoints))
  }
  return map
}

function resampleTo(values: number[], targetN: number): number[] {
  if (values.length === targetN) return values
  if (values.length === 0) return new Array<number>(targetN).fill(0)
  if (values.length === 1) return new Array<number>(targetN).fill(values[0])
  const out = new Array<number>(targetN)
  const stride = (values.length - 1) / (targetN - 1)
  for (let i = 0; i < targetN; i++) {
    const idx = i * stride
    const lo = Math.floor(idx)
    const hi = Math.min(values.length - 1, Math.ceil(idx))
    const t = idx - lo
    out[i] = values[lo] * (1 - t) + values[hi] * t
  }
  return out
}

// Latency thresholds: 100ms is roughly the upper bound for
// "feels instant", 500ms is where users start noticing delay.
// Above 500ms is "slow enough to investigate". Cluster-wide
// averages, so individual outliers can pull the number — but
// a sustained high avg means a real problem.
function latencyColor(ms: number): string {
  if (!Number.isFinite(ms)) return '#94a3b8'
  if (ms <= 100) return '#22c55e'  // green
  if (ms <= 500) return '#f59e0b'  // amber
  return '#ef4056'                  // red
}

function formatLatency(ms: number): string {
  if (!Number.isFinite(ms)) return '— ms'
  if (ms < 1) return '<1 ms'
  if (ms < 10) return `${ms.toFixed(1)} ms`
  if (ms < 1000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(2)} s`
}

function formatRate(reqPerSec: number): string {
  if (!Number.isFinite(reqPerSec)) return '—'
  if (reqPerSec === 0) return '0 req/s'
  if (reqPerSec < 1) return `${reqPerSec.toFixed(2)} req/s`
  if (reqPerSec < 10) return `${reqPerSec.toFixed(1)} req/s`
  return `${Math.round(reqPerSec)} req/s`
}

function roundTo(v: number, decimals: number): number {
  const f = Math.pow(10, decimals)
  return Math.round(v * f) / f
}
