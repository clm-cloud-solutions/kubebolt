import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { Globe } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { collapsePodToWorkload } from '@/utils/promql'
import {
  StatusDistBar,
  ClassRates,
  ClassTooltipRows,
  EMPTY_DIST,
  buildDistIndex,
  useWorkloadStatusDist,
  type StatusDistribution,
} from './StatusDistribution'

// TopWorkloadsTraffic — the L7 counterpart to TopWorkloadsCpu.
// Ranks workloads by HTTP requests/second they receive, with two
// per-row visualizations that give the row visual weight beyond the
// scalar numbers:
//
//   1. Status-class distribution bar — stacked horizontal bar showing
//      the proportion of 2xx / 3xx / 4xx / 5xx in the workload's
//      traffic. The bar IS the at-a-glance error signal: a green-
//      dominant bar reads as healthy, a red-tinted one reads as in
//      trouble. Replaces the standalone error-rate chip we had
//      before — the bar carries the same information and adds the
//      success-class shape on top.
//
//   2. Sparkline of req/s over the selected range — gives temporal
//      context to the "current" rate, so the user can tell a steady
//      14 req/s workload from a spiking-then-quiet one.
//
// Latency moves to the tooltip (was inline) — it's secondary signal
// next to "is this thing serving correctly?" and the new bar +
// sparkline take up the room it used.
//
// Four queries: instant topk for ranking, instant breakdown by
// status_class for the dist bar (error rate is derived from this),
// instant latency for the tooltip, and a range query for the
// sparkline. All keyed off (dst_namespace, workload) so the join
// is O(1) per row.

const TOP_N = 10
const REFRESH_MS = 30_000
// Sparkline target width: about 60 px wide, ~40 sample points.
// More than that compresses to one-pixel detail noise; fewer makes
// the curve blocky. 40 is the visual sweet spot for this row size.
const TREND_POINTS = 40
// Floor on the range query step so very short ranges don't hammer
// VM with sub-second buckets.
const TREND_STEP_MIN_S = 15

interface ServiceRow {
  namespace: string
  workload: string
  reqRate: number      // req/s
  errorRatePct: number // 0–100, or NaN when reqRate is 0
  avgLatencyMs: number // ms, or NaN when no latency samples
  distribution: StatusDistribution
  trend: number[]      // req/s sampled at TREND_POINTS evenly spaced points
}

interface Props {
  rangeMinutes: number
}

// Rate window mirrors the page's range selector — if the user
// asked for "last 1h", the topk is computed over that hour, so the
// numbers in this panel agree with the trend chart above. The
// queryKey carries the window so changing the selector invalidates
// the cache and re-fetches.
export function TopWorkloadsTraffic({ rangeMinutes }: Props) {
  const RATE_WINDOW = `${rangeMinutes}m`

  // Sparkline range query parameters. step is sized so that the
  // returned series has ~TREND_POINTS samples; the rate window is
  // 2× the step so each sample sees enough history to be smooth
  // without losing responsiveness.
  const trendStepS = Math.max(TREND_STEP_MIN_S, Math.floor((rangeMinutes * 60) / TREND_POINTS))
  const trendWindow = `${trendStepS * 2}s`

  const totalQuery = `topk(${TOP_N}, sum by (dst_namespace, workload) (${collapsePodToWorkload(
    `rate(pod_flow_http_requests_total{source="hubble"}[${RATE_WINDOW}])`,
  )}))`

  // Breakdown by status_class — same metric as totalQuery but
  // without the topk wrapper and with status_class as a grouping
  // dimension. Frontend filters to just the top-10 keys identified
  // by totalQuery, so cardinality stays small even on big clusters.
  // Avg latency = sum-of-sum / sum-of-count. NaN when count is zero
  // for that pair (no requests in the window). Filtered client-side
  // so the row just shows "—" instead of a misleading 0ms.
  const latQuery =
    `sum by (dst_namespace, workload) (${collapsePodToWorkload(
      `rate(pod_flow_http_latency_seconds_sum{source="hubble"}[${RATE_WINDOW}])`,
    )})`
    + ` / `
    + `sum by (dst_namespace, workload) (${collapsePodToWorkload(
      `rate(pod_flow_http_latency_seconds_count{source="hubble"}[${RATE_WINDOW}])`,
    )})`

  const trendQuery = `sum by (dst_namespace, workload) (${collapsePodToWorkload(
    `rate(pod_flow_http_requests_total{source="hubble"}[${trendWindow}])`,
  )})`

  const totalQ = useQuery({
    queryKey: ['reliability', 'top-traffic', 'total', RATE_WINDOW],
    queryFn: () => api.queryMetrics({ query: totalQuery }),
    refetchInterval: REFRESH_MS,
    retry: false,
  })
  // Status-class breakdown shared with TopLatencyWorkloads — same
  // queryKey ⇒ TanStack Query dedupes the fetch.
  const distQ = useWorkloadStatusDist(rangeMinutes)
  const latQ = useQuery({
    queryKey: ['reliability', 'top-traffic', 'latency', RATE_WINDOW],
    queryFn: () => api.queryMetrics({ query: latQuery }),
    refetchInterval: REFRESH_MS,
    retry: false,
  })
  const trendQ = useQuery({
    queryKey: ['reliability', 'top-traffic', 'trend', RATE_WINDOW, trendStepS],
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

  const isLoading = totalQ.isLoading
  const error = totalQ.error

  // Memo so the indices below don't rebuild on every render unless
  // their source data changed — the per-row downstream renders use
  // these maps and otherwise React would discard the work each tick.
  const distIndex = useMemo(() => buildDistIndex(distQ.data?.data?.result), [distQ.data])
  const latIndex = useMemo(() => buildLatIndex(latQ.data?.data?.result), [latQ.data])
  const trendIndex = useMemo(() => buildTrendIndex(trendQ.data?.data?.result, TREND_POINTS), [trendQ.data])

  const rows: ServiceRow[] = (totalQ.data?.data?.result ?? [])
    .map((s) => {
      const namespace = s.metric.dst_namespace ?? ''
      const workload = s.metric.workload ?? ''
      const reqRate = parseFloat(s.value?.[1] ?? '0')
      const k = keyOf(namespace, workload)
      const distribution = (k && distIndex.get(k)) || EMPTY_DIST
      const errRate = distribution.clientErr + distribution.serverErr
      const errorRatePct = reqRate > 0 ? (errRate / reqRate) * 100 : NaN
      const avgLatencyMs = k ? latIndex.get(k) ?? NaN : NaN
      const trend = (k && trendIndex.get(k)) || new Array<number>(TREND_POINTS).fill(0)
      return { namespace, workload, reqRate, errorRatePct, avgLatencyMs, distribution, trend }
    })
    .filter((r) => r.workload && Number.isFinite(r.reqRate))
    .sort((a, b) => b.reqRate - a.reqRate)

  // Kobi panel-level payload — same shape across rows so the LLM
  // sees a uniform table. Numbers stay in their native units; the
  // model is fine reading raw req/s and percentages, and skipping
  // the formatting layer keeps the prompt compact.
  const kobiRows = rows.map((r) => buildKobiRow(r))

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-kb-text-secondary shrink-0">
            <Globe className="w-4 h-4" />
          </span>
          <h4 className="text-sm font-semibold text-kb-text-primary truncate">
            Top Workloads · Traffic
          </h4>
          {rows.length > 0 && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'top_workloads_traffic',
                rangeLabel: RATE_WINDOW,
                rows: kobiRows,
              }}
              variant="icon"
              label="Ask Kobi about top HTTP workloads"
            />
          )}
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
          cluster-wide · top {TOP_N}
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
          No HTTP traffic in the selected range. Hubble is up but nothing's talking yet —
          generate some requests, or widen the range.
        </div>
      )}

      {!isLoading && !error && rows.length > 0 && (
        <ul className="space-y-2">
          {rows.map((r, i) => (
            <li key={`${r.namespace}/${r.workload}`}>
              <ServiceRowEl row={r} rank={i + 1} />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function ServiceRowEl({ row, rank }: { row: ServiceRow; rank: number }) {
  return (
    <HoverTooltip body={<RowTooltip row={row} />}>
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
          {/* Visual row beneath name: distribution bar takes the
              available width, sparkline pinned at fixed width on the
              right so the curves line up vertically across rows. */}
          <div className="flex items-center gap-2 mt-1">
            <StatusDistBar dist={row.distribution} />
            <Sparkline values={row.trend} width={56} height={14} />
          </div>
          {/* Numeric breakdown — what the bar shows visually, in
              precise rates. Only classes with traffic render; an
              all-2xx workload shows a single green chip. Rate
              format omits "/s" since the row's headline rate on
              the far right already carries that unit. */}
          <ClassRates dist={row.distribution} />
        </div>
        <span className="text-[11px] font-mono text-kb-text-secondary shrink-0 tabular-nums">
          {formatRate(row.reqRate)}
        </span>
      </div>
    </HoverTooltip>
  )
}

function RowTooltip({ row }: { row: ServiceRow }) {
  return (
    <>
      <TooltipHeader right={row.namespace}>{row.workload}</TooltipHeader>
      <div className="space-y-1">
        <TooltipRow color="#94a3b8" label="Requests" value={`${formatRate(row.reqRate)}`} />
        <TooltipRow
          color={errorRateColor(row.errorRatePct)}
          label="Error rate"
          value={
            Number.isFinite(row.errorRatePct)
              ? `${row.errorRatePct.toFixed(row.errorRatePct < 1 ? 2 : 1)}%`
              : '—'
          }
        />
        <TooltipRow color={null} label="Avg latency" value={formatLatency(row.avgLatencyMs)} />
        <div className="h-px bg-kb-border/60 my-1.5" />
        <ClassTooltipRows dist={row.distribution} />
      </div>
    </>
  )
}

// Build the Kobi blob for one row. Includes the most actionable
// numbers — total rate, error rate %, latency, and the status
// class breakdown — so the LLM has enough to make a judgment
// without re-querying.
function buildKobiRow(r: ServiceRow): Record<string, string | number> {
  const blob: Record<string, string | number> = {
    workload: `${r.namespace}/${r.workload}`,
    req_per_sec: roundTo(r.reqRate, 2),
  }
  if (Number.isFinite(r.errorRatePct)) blob.error_rate_pct = roundTo(r.errorRatePct, 2)
  if (Number.isFinite(r.avgLatencyMs)) blob.avg_latency_ms = roundTo(r.avgLatencyMs, 1)
  // Per-class rates only when non-zero — keeps the prompt compact
  // and signals to the LLM that absent classes mean truly zero
  // (not "we didn't query for this").
  if (r.distribution.success > 0) blob.rate_2xx = roundTo(r.distribution.success, 2)
  if (r.distribution.redirect > 0) blob.rate_3xx = roundTo(r.distribution.redirect, 2)
  if (r.distribution.clientErr > 0) blob.rate_4xx = roundTo(r.distribution.clientErr, 2)
  if (r.distribution.serverErr > 0) blob.rate_5xx = roundTo(r.distribution.serverErr, 2)
  return blob
}

function roundTo(v: number, decimals: number): number {
  const f = Math.pow(10, decimals)
  return Math.round(v * f) / f
}

// ─── Sparkline ──────────────────────────────────────────────────

function Sparkline({ values, width, height }: { values: number[]; width: number; height: number }) {
  // Empty / all-zero series → flat dim line so the row stays
  // visually aligned even when there's no movement.
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
  // Map values to SVG path. y inverted (SVG 0 is top). 1px padding
  // top/bottom so the peak doesn't clip and the trough doesn't sit
  // on the axis line.
  const padY = 1
  const usableH = height - padY * 2
  const stepX = values.length > 1 ? width / (values.length - 1) : 0
  const points = values.map((v, i) => {
    const x = i * stepX
    const y = padY + (1 - v / max) * usableH
    return `${x.toFixed(1)},${y.toFixed(1)}`
  })
  const path = `M ${points.join(' L ')}`
  // Area fill below the line — adds visual weight without needing
  // a separate svg element. Last point closes back to baseline.
  const lastX = (values.length - 1) * stepX
  const fillPath = `${path} L ${lastX.toFixed(1)},${height} L 0,${height} Z`
  // Sparkline color picks the success-class green so all rows
  // share a visual baseline; per-row error coloring lives in the
  // dist bar above instead.
  const SPARK_COLOR = '#22c55e'
  return (
    <svg width={width} height={height} aria-hidden className="shrink-0">
      <path d={fillPath} fill={SPARK_COLOR} fillOpacity={0.18} />
      <path d={path} stroke={SPARK_COLOR} strokeWidth={1} fill="none" strokeLinejoin="round" />
    </svg>
  )
}

// ─── Helpers ────────────────────────────────────────────────────

function buildLatIndex(
  result: Array<{ metric: Record<string, string>; value?: [number, string] }> | undefined,
): Map<string, number> {
  const map = new Map<string, number>()
  if (!result) return map
  for (const s of result) {
    const k = keyOf(s.metric.dst_namespace, s.metric.workload)
    if (!k) continue
    const v = parseFloat(s.value?.[1] ?? 'NaN')
    if (Number.isFinite(v) && v > 0) map.set(k, v * 1000)
  }
  return map
}

// Normalize each series' values[] into a fixed-length numeric
// array. VM range responses can have variable point counts (gaps
// when no data); we backfill with 0 so all sparklines have aligned
// X axes. Length mismatches between series are squeezed/stretched
// to TREND_POINTS by linear sampling.
function buildTrendIndex(
  result: Array<{ metric: Record<string, string>; values?: Array<[number, string]> }> | undefined,
  targetPoints: number,
): Map<string, number[]> {
  const map = new Map<string, number[]>()
  if (!result) return map
  for (const s of result) {
    const k = keyOf(s.metric.dst_namespace, s.metric.workload)
    if (!k) continue
    const raw = (s.values ?? [])
      .map((p) => parseFloat(p[1]))
      .map((v) => (Number.isFinite(v) ? v : 0))
    if (raw.length === 0) continue
    map.set(k, resampleTo(raw, targetPoints))
  }
  return map
}

function resampleTo(values: number[], targetN: number): number[] {
  if (values.length === targetN) return values
  if (values.length === 0) return new Array<number>(targetN).fill(0)
  if (values.length === 1) return new Array<number>(targetN).fill(values[0])
  // Linear sample: pick targetN points evenly across the source
  // index range. Good enough for visualization; not for analysis.
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

// Fixed thresholds: 1% and 5%. These aren't industry-universal but
// they map to the gut-feel "fine / suspicious / on fire" most
// operators reach for. We deliberately don't auto-scale per-cluster
// because a relative scale would make a hot cluster look healthier
// than it is.
function errorRateColor(pct: number): string {
  if (!Number.isFinite(pct) || pct < 1) return '#22c55e'   // green
  if (pct < 5) return '#f59e0b'                              // amber
  return '#ef4056'                                           // red
}

function formatRate(reqPerSec: number): string {
  if (!Number.isFinite(reqPerSec)) return '—'
  if (reqPerSec === 0) return '0 req/s'
  if (reqPerSec < 1) return `${reqPerSec.toFixed(2)} req/s`
  if (reqPerSec < 10) return `${reqPerSec.toFixed(1)} req/s`
  return `${Math.round(reqPerSec)} req/s`
}

function formatLatency(ms: number): string {
  if (!Number.isFinite(ms)) return '— ms'
  if (ms < 1) return '<1 ms'
  if (ms < 100) return `${ms.toFixed(1)} ms`
  if (ms < 10_000) return `${Math.round(ms)} ms`
  return `${(ms / 1000).toFixed(1)} s`
}

function keyOf(ns: string | undefined, workload: string | undefined): string | null {
  if (!ns || !workload) return null
  return `${ns}/${workload}`
}
