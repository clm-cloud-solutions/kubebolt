import { useQuery } from '@tanstack/react-query'
import { RotateCw } from 'lucide-react'
import { api } from '@/services/api'
import { HoverTooltip, TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'

// RestartHistorySparkline — 24h bar chart of pod-container restarts.
//
// Phase 2.5 P25-01 of the dashboard enrichment roadmap. The static
// "Restart Count: 47" we already render is the lifetime informer
// total — useful but mute on temporal pattern. With KSM's
// `kube_pod_container_status_restarts_total` (counter that
// increments on every restart) we can plot a per-hour bucket of
// restarts over the last day and answer the question operators
// actually ask post-incident: "did this start flapping recently
// or has it been broken all week?"
//
// Why bars, not a line:
//   Restarts are discrete events, mostly zero with occasional
//   spikes. A smoothed line interpolates between buckets and
//   visually under-represents single-bucket spikes; a bar chart
//   keeps each bucket's count legible and the all-zero buckets
//   visibly empty (vs a flat baseline that could be mistaken for
//   "no data").
//
// Empty-state policy:
//   1. KSM not scraping (metric absent in VM) → render a dim
//      baseline + a small "no data" hint in the title attribute,
//      so the operator who hovers learns why. Phase 2 docs the
//      fix (enable scrape.discovery.kubeStateMetrics OR rely on
//      annotation-driven discovery).
//   2. KSM scraping but pod has had zero restarts → render the
//      baseline with no bars. Same visual as #1 but the title
//      attribute distinguishes them ("no restarts last 24h" vs
//      "kube-state-metrics not detected").

interface Props {
  namespace: string
  pod: string
  // container scopes the query to a single container's restart
  // counter. When omitted, the sparkline aggregates across all
  // containers in the pod (sum) — useful for pod-level listings
  // where there's no per-container row.
  container?: string
  // Display variant. The same data drives both — the choice is
  // about scanability vs detail.
  //
  //   "chart"  full bar-chart sparkline + suffix "N/24h". Used in
  //            Pod detail Overview where the operator dwells on
  //            one pod and wants the temporal pattern.
  //   "badge"  count + recency-aware icon. The count itself is
  //            colored by recency: red when flapping right now,
  //            amber when sustained-but-stable, muted otherwise
  //            — so a long-lived pod with high lifetime restarts
  //            doesn't scream red on a calm column.
  variant?: 'chart' | 'badge'
  // Lifetime restart count from the informer. Required for
  // variant="badge"; the component renders both count and icon
  // so a single recency analysis decides both colors atomically.
  // Ignored for variant="chart".
  lifetimeCount?: number
  width?: number
  height?: number
  // compact mode hides the "N/24h" suffix on the chart variant.
  // Only effective when variant="chart".
  compact?: boolean
}

const DEFAULT_WIDTH = 140
const DEFAULT_HEIGHT = 18
const BUCKET_HOURS = 24

export function RestartHistorySparkline({
  namespace,
  pod,
  container,
  variant = 'chart',
  lifetimeCount = 0,
  width = DEFAULT_WIDTH,
  height = DEFAULT_HEIGHT,
  compact = false,
}: Props) {
  // Lock end-of-range to a 1-minute boundary so back-to-back renders
  // share the same query result in the TanStack cache (different
  // millisecond `end` would generate distinct queryKeys).
  const nowSec = Math.floor(Date.now() / 1000 / 60) * 60
  const start = nowSec - BUCKET_HOURS * 3600
  const end = nowSec
  // 1-hour buckets across 24h → 24 sample points. `increase()` over
  // [1h] gives the count of restarts in each bucket. The cluster_id
  // injection happens at the backend's scopeQueryByCluster — same
  // path every other UI query uses.
  //
  // Per-container scope when `container` is provided; pod-level
  // sum (across containers) otherwise. The pod-level sum is the
  // honest aggregate for list views — a pod with two containers
  // each restarting once shows 2 in that bucket, not 1.
  //
  // The `* on(...) present_over_time(metric[5m])` filter drops
  // stale series — those that aren't actively reporting samples
  // at evaluation time. Without it, a series that stopped emitting
  // (e.g. KSM-pod re-labels after the honor_labels chart fix
  // orphaned the prior series) would still produce phantom
  // increases inside the rolling 24h window, attributing
  // "restarts" to pods that don't have any. The multiply-by-1
  // pattern preserves the increase value when the series is
  // active and drops it when stale.
  const containerFilter = container ? `,container="${container}"` : ''
  const baseSelector = `kube_pod_container_status_restarts_total{namespace="${namespace}",pod="${pod}"${containerFilter}}`
  // KSM emits the pod identity label as `uid` (not `pod_uid`). The
  // `on(...)` clause must match the actual label name or the join
  // silently degrades to "match on empty string", producing empty
  // results across the board.
  //
  // The `@ end()` modifier pins the staleness check to the END of
  // the query range, NOT to each per-step evaluation. Without it,
  // a series that's stale-now but was alive 5h ago would still
  // contribute its historical increase to the 5h-ago bucket
  // (because at THAT step, present_over_time returned 1). The
  // visible bug: KSM-pod showing FLAPPING with phantom restarts
  // attributed to OLD wrongly-labeled series that stopped after
  // the honor_labels chart fix. Anchoring to end() means "alive
  // now or not at all" — stale series get filtered uniformly
  // across all steps.
  const innerQuery = `(increase(${baseSelector}[1h]) * on(namespace,pod,container,uid) present_over_time(${baseSelector}[5m] @ end()))`
  const query = container ? innerQuery : `sum by (pod) (${innerQuery})`

  const { data, isLoading, error } = useQuery({
    queryKey: ['restart-history', namespace, pod, container ?? '*', start],
    queryFn: () =>
      api.queryMetricsRange({
        query,
        start,
        end,
        step: '1h',
      }),
    refetchInterval: 60_000,
    staleTime: 30_000,
    retry: false,
  })

  // Reduce all matching series (one per pod_uid in case of restarts
  // crossing pod identity changes) into a single 24-bucket vector,
  // taking the max at each timestamp — restarts are monotonic per
  // container life, but pod_uid changes reset the counter, so max
  // captures the highest per-bucket increase across whatever uids
  // KSM has observed.
  const buckets = reduceToBuckets(data?.data?.result, BUCKET_HOURS)
  const totalRestarts = buckets.reduce((s, n) => s + n, 0)
  // The most recent bucket = last hour. Anything > 0 here means
  // the pod restarted within the last hour, which is the
  // "respond now" signal (vs "happened earlier today, currently
  // stable").
  const restartsLastHour = buckets[buckets.length - 1] ?? 0

  // Distinguish KSM-not-scraping from no-restart-pod by looking at
  // the response shape. Empty result vector === no series matched
  // === metric not present. (`error` covers VM unreachable, which
  // is rarer.)
  const noData = !isLoading && !error && (data?.data?.result?.length ?? 0) === 0
  const hint = error
    ? 'failed to load restart history'
    : noData
    ? 'no kube-state-metrics data available — enable scrape.discovery.kubeStateMetrics on the agent'
    : totalRestarts === 0
    ? 'no restarts in last 24h'
    : restartsLastHour > 0
    ? `${restartsLastHour} restart${restartsLastHour === 1 ? '' : 's'} in last 1h · ${totalRestarts} in last 24h`
    : `${totalRestarts} restart${totalRestarts === 1 ? '' : 's'} in last 24h`

  // ── Badge variant — full cell render (count + icon) ────────
  if (variant === 'badge') {
    // Threshold-gated: a single restart is a normal lifecycle event
    // (deploy, scheduler eviction, node maintenance) — flagging it
    // would drown the column in noise on a freshly-rolled cluster
    // (every pod's first start increments the counter 0→1). Two
    // distinct thresholds:
    //
    //   urgent (red): >= 2 restarts in the last hour. Genuine
    //                 flapping that probably needs investigation
    //                 right now.
    //   warn (amber): >= 3 restarts in 24h. Sustained pattern,
    //                 worth a look but not on-call territory.
    //
    // Below either threshold → no icon AND the lifetime count
    // renders muted (no color), regardless of how high it is.
    // A pod with 200 lifetime restarts but stable now is not
    // currently a problem; the column shouldn't scream red at it.
    const urgent = restartsLastHour >= 2
    const sustained = totalRestarts >= 3
    const flagged = !noData && (urgent || sustained)
    const countColorClass = urgent
      ? 'text-status-error'
      : sustained
      ? 'text-status-warn'
      : 'text-kb-text-secondary'
    const iconColorClass = urgent ? 'text-status-error' : 'text-status-warn'

    const countNode = (
      <span className={`text-[11px] font-mono tabular-nums ${countColorClass}`}>
        {lifetimeCount}
      </span>
    )

    if (!flagged) {
      return countNode
    }

    // Tooltip uses the shared design system — TooltipPanel wrapper
    // + TooltipHeader + TooltipRow, same shape as MetricChart and
    // ResourceUsageCell tooltips. Operators get a consistent
    // hover surface across the whole dashboard.
    const headerLabel = urgent ? 'Flapping' : 'Recent activity'
    const tooltipBody = (
      <>
        <TooltipHeader right={headerLabel}>Pod restarts</TooltipHeader>
        <div className="space-y-1">
          <TooltipRow
            color={restartsLastHour > 0 ? '#ef4056' : null}
            label="Last 1h"
            value={String(restartsLastHour)}
          />
          <TooltipRow
            color={totalRestarts > 0 ? '#f5a623' : null}
            label="Last 24h"
            value={String(totalRestarts)}
          />
          <TooltipRow
            color={null}
            label="Lifetime"
            value={String(lifetimeCount)}
          />
        </div>
        {urgent && (
          <div className="mt-2 pt-1.5 border-t border-kb-border/60">
            <TooltipNote>
              ≥2 restarts in 1h — likely flapping, investigate now.
            </TooltipNote>
          </div>
        )}
      </>
    )

    return (
      <HoverTooltip body={tooltipBody} minWidth={220}>
        <span className="inline-flex items-center gap-2">
          {countNode}
          <RotateCw className={`w-3 h-3 ${iconColorClass}`} strokeWidth={2.5} />
        </span>
      </HoverTooltip>
    )
  }

  // ── Chart variant — Pod detail, full sparkline ──────────────
  return (
    <div
      className="inline-flex items-center gap-1.5 text-[11px] text-kb-text-secondary"
      title={hint}
    >
      <BarChart values={buckets} width={width} height={height} hasData={!noData} />
      {!compact && (
        <span className="font-mono text-[10px] text-kb-text-tertiary tabular-nums">
          {noData ? '—' : `${totalRestarts}/24h`}
        </span>
      )}
    </div>
  )
}

// ─── Helpers ────────────────────────────────────────────────────

function reduceToBuckets(
  result: Array<{ metric: Record<string, string>; values?: Array<[number, string]> }> | undefined,
  expectedBuckets: number,
): number[] {
  if (!result || result.length === 0) return new Array(expectedBuckets).fill(0)
  // Build a per-timestamp max across all returned series.
  const byTs = new Map<number, number>()
  for (const s of result) {
    for (const [ts, v] of s.values ?? []) {
      const n = Math.max(0, Math.floor(parseFloat(v) || 0))
      const cur = byTs.get(ts) ?? 0
      if (n > cur) byTs.set(ts, n)
    }
  }
  const sortedTs = [...byTs.keys()].sort((a, b) => a - b)
  const out = sortedTs.map((ts) => byTs.get(ts) ?? 0)
  // Pad / trim to the expected count so the visual stays stable
  // even when VM happens to return one extra/missing bucket due to
  // step alignment.
  if (out.length < expectedBuckets) {
    return [...new Array(expectedBuckets - out.length).fill(0), ...out]
  }
  return out.slice(-expectedBuckets)
}

// ─── BarChart (inline SVG, bar per bucket) ─────────────────────

function BarChart({
  values,
  width,
  height,
  hasData,
}: {
  values: number[]
  width: number
  height: number
  hasData: boolean
}) {
  // Always render a subtle baseline so the chart has a frame —
  // without it, sparse events (1-2 restarts in 24 buckets) read
  // as stray pixels instead of "an event in this bucket". The
  // baseline opacity differs to distinguish "no KSM data"
  // (very dim) from "real zeros" (slightly less dim) — but the
  // primary signal is the title attribute on the parent.
  const max = Math.max(...values, 0)
  const allZero = !hasData || max === 0
  const barGap = 1
  const barWidth = Math.max(1, (width - barGap * (values.length - 1)) / values.length)
  return (
    <svg width={width} height={height} aria-hidden className="shrink-0">
      {/* Bucket grid — light vertical ticks every 6 buckets (= 6h
          on a 24h horizon) to anchor the eye. Hidden below the bars
          but visible against the baseline. */}
      {!allZero &&
        [6, 12, 18].map((b) => {
          const x = b * (barWidth + barGap)
          return (
            <line
              key={`tick-${b}`}
              x1={x}
              y1={0}
              x2={x}
              y2={height - 1}
              stroke="var(--kb-text-tertiary)"
              strokeOpacity={0.08}
              strokeWidth={1}
            />
          )
        })}
      {/* Baseline — always rendered. Gives the chart a frame so a
          single bar in 24 buckets reads as a clear event-marker
          rather than a stray pixel. */}
      <line
        x1={0}
        y1={height - 0.5}
        x2={width}
        y2={height - 0.5}
        stroke="var(--kb-text-tertiary)"
        strokeOpacity={hasData ? 0.35 : 0.18}
        strokeWidth={1}
      />
      {/* Bars — only buckets with restarts > 0 get a visible bar.
          Floor at 3px so a single-bucket spike isn't lost in the
          noise of the baseline. */}
      {!allZero &&
        values.map((v, i) => {
          if (v === 0) return null
          const h = Math.max(3, Math.round((v / max) * (height - 1)))
          const x = i * (barWidth + barGap)
          const y = height - h
          return (
            <rect
              key={i}
              x={x}
              y={y}
              width={barWidth}
              height={h}
              fill="var(--kb-status-warn, #f5a623)"
              opacity={0.9}
            />
          )
        })}
    </svg>
  )
}
