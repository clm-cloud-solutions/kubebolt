import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Activity } from 'lucide-react'
import { api } from '@/services/api'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { useHubbleAvailable } from '@/hooks/useHubbleAvailable'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { MetricChart } from '@/components/shared/MetricChart'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { RangeSelector } from '@/components/shared/RangeSelector'
import { DashboardSubTabs } from './DashboardSubTabs'
import { OverviewHeader } from './OverviewHeader'
import { TopWorkloadsTraffic } from './TopWorkloadsTraffic'
import { ErrorHotspots } from './ErrorHotspots'
import { TopLatencyWorkloads } from './TopLatencyWorkloads'
import { NetworkDrops } from './NetworkDrops'

// ReliabilityPage is the third dashboard sub-tab — the L7 lens on
// what the cluster is actually serving. Driven entirely by Hubble
// HTTP metrics (pod_flow_http_* with source="hubble"); the tab is
// gated on Hubble being present and shipping into VM, so reaching
// this page implies the data is there.
//
// Three panels, structured from "is the cluster healthy right now"
// (the chart at top) → "which workloads are involved" (top
// receivers) → "where exactly are errors concentrated" (hot-spots).
// Same range selector + freshness indicator as Overview / Capacity
// so the user keeps one mental model across sub-tabs.
//
// Direct-nav defense: if a user lands on /reliability with Hubble
// no longer detected (e.g. agent restarted, Hubble uninstalled),
// we show an explanatory empty state instead of empty panels.
export function ReliabilityPage() {
  const { data: overview, isLoading, error, refetch, dataUpdatedAt, isFetching } = useClusterOverview()
  const [rangeMinutes, setRangeMinutes] = useState(15)
  const { available: hubbleAvailable, isLoading: hubbleLoading } = useHubbleAvailable()

  // Volume context for the chart's tooltip — req/s broken down by
  // status_class over the same time window. The chart shows error
  // PERCENTAGES; this gives the user "how many absolute requests
  // does that percentage represent at this point in time?". Run
  // even when the chart is also fetching — TanStack dedupes by
  // queryKey but these are distinct queries (page-level breakdown
  // vs chart-level percentages).
  const breakdownStepS = stepSecForRange(rangeMinutes)
  const breakdownQuery = useQuery({
    queryKey: ['reliability', 'volume-breakdown', rangeMinutes, breakdownStepS],
    queryFn: () => {
      const end = Math.floor(Date.now() / 1000)
      const start = end - rangeMinutes * 60
      return api.queryMetricsRange({
        query: `sum by (status_class) (rate(pod_flow_http_requests_total{source="hubble"}[1m]))`,
        start,
        end,
        step: `${breakdownStepS}s`,
      })
    },
    refetchInterval: 30_000,
    retry: false,
    enabled: hubbleAvailable,
  })

  // Build a {timestamp → ClassRates} index. Same metric exposed
  // two ways: per-class for the chart's split lines, and total /
  // errors for the volume rows in the tooltip. We compute both
  // here so the renderer just looks up.
  const volumeIndex = useMemo(
    () => buildVolumeIndex(breakdownQuery.data?.data?.result),
    [breakdownQuery.data],
  )

  if (isLoading) return <LoadingSpinner />
  if (error || !overview) return <ErrorState message={error?.message} onRetry={() => refetch()} />

  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-4 flex-wrap">
        <OverviewHeader overview={overview} />
        <div className="flex items-center gap-3 mt-1">
          <RangeSelector value={rangeMinutes} onChange={setRangeMinutes} />
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
      </div>

      <DashboardSubTabs />

      {!hubbleAvailable && !hubbleLoading ? (
        <HubbleMissingPlaceholder />
      ) : (
        <>
          {/* Cluster-wide error rate over time, split by class —
              4xx (amber, client errors) and 5xx (red, server
              errors) as separate series. Splitting the curve
              answers the first question every operator asks of an
              error spike: "is this them or me?" Tooltip shows both
              classes at the hovered timestamp; absent class
              renders as 0%, no gap, so the user can compare across
              points without phantom missing data.
              NaN windows (no traffic in interval) render as gaps;
              that's honest about "we have no signal" rather than
              0% which would falsely suggest "perfectly healthy". */}
          <MetricChart
            title="Cluster error rate"
            icon={<Activity className="w-4 h-4" />}
            unit="percent"
            queries={[
              { query: errorRateByClassQuery('client_err'), prefix: '4xx' },
              { query: errorRateByClassQuery('server_err'), prefix: '5xx' },
            ]}
            seriesLabel={(_labels, prefix) => prefix ?? ''}
            accents={['#f59e0b', '#ef4056']}
            chartType="area"
            showStats={false}
            height={200}
            controlledRangeMinutes={rangeMinutes}
            tooltipExtra={(t) => renderVolumeRows(volumeIndex, t, breakdownStepS)}
          />

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
            <TopWorkloadsTraffic rangeMinutes={rangeMinutes} />
            <ErrorHotspots rangeMinutes={rangeMinutes} />
          </div>

          {/* Latency + L4 drops — different reliability dimensions
              than the error rate above. Latency catches slow-but-
              successful services that the error rate misses; L4
              drops catch silent NetworkPolicy violations and
              connection refusals that never reach the application
              layer at all. */}
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
            <TopLatencyWorkloads rangeMinutes={rangeMinutes} />
            <NetworkDrops rangeMinutes={rangeMinutes} />
          </div>
        </>
      )}
    </div>
  )
}

// errorRateByClassQuery — percentage of HTTP traffic that landed
// in a single error class (4xx or 5xx) over the same denominator
// (total requests). 4xx + 5xx series stack to the same total
// error rate the user saw before, but split so the tooltip can
// answer "client mistakes or server breakage?" without leaving
// the chart.
//
// clamp_min on the denominator avoids the divide-by-zero NaN
// explosion when there's briefly no traffic — but we only clamp
// to 1e-9 (effectively "barely positive") so a real zero-traffic
// window still shows near-zero error rate, not a fake spike.
//
// Why `[1m]` rate window: the chart's outer step is set by the
// range selector; using a tight 1m rate gives the curve responsive
// shape, while the chart's own smoothing handles the overall span.
function errorRateByClassQuery(statusClass: 'client_err' | 'server_err'): string {
  return [
    `100 *`,
    `  sum(rate(pod_flow_http_requests_total{source="hubble", status_class="${statusClass}"}[1m]))`,
    `/`,
    `  clamp_min(sum(rate(pod_flow_http_requests_total{source="hubble"}[1m])), 1e-9)`,
  ].join(' ')
}

// ─── Volume tooltip helpers ─────────────────────────────────────

interface VolumeEntry {
  total: number     // req/s across all status classes
  errors: number    // req/s of client_err + server_err
  byClass: Record<string, number>
}

// Mirrors MetricChart's DEFAULT_RANGE_OPTIONS step lookup. Kept in
// sync by hand because exporting the table from MetricChart would
// invite drift in the other direction (tests, other charts that
// may pick their own step). When that table changes, update here
// too.
function stepSecForRange(rangeMinutes: number): number {
  if (rangeMinutes <= 15) return 15
  if (rangeMinutes <= 60) return 30
  if (rangeMinutes <= 360) return 120
  if (rangeMinutes <= 1440) return 600
  return 3600
}

// Build a {timestampSec → {total, errors, byClass}} map from a
// range query result that's `sum by (status_class) (rate(...))`.
// One entry per unique timestamp; classes with NaN values are
// skipped, so a class that briefly has no samples doesn't poison
// the total.
function buildVolumeIndex(
  result: Array<{ metric: Record<string, string>; values?: Array<[number, string]> }> | undefined,
): Map<number, VolumeEntry> {
  const map = new Map<number, VolumeEntry>()
  if (!result) return map
  for (const series of result) {
    const cls = series.metric.status_class ?? 'unknown'
    const isError = cls === 'client_err' || cls === 'server_err'
    for (const point of series.values ?? []) {
      const ts = point[0]
      const v = parseFloat(point[1])
      if (!Number.isFinite(v)) continue
      let entry = map.get(ts)
      if (!entry) {
        entry = { total: 0, errors: 0, byClass: {} }
        map.set(ts, entry)
      }
      entry.total += v
      if (isError) entry.errors += v
      entry.byClass[cls] = v
    }
  }
  return map
}

// Render the volume rows for a hovered timestamp. Fuzzy lookup
// with ±step/2 tolerance — chart's series and our breakdown query
// run with the same step, so timestamps usually align exactly,
// but float rounding around `now` can produce 1s-off keys. Returns
// null when no entry is in range; MetricChart's tooltipExtra
// container then doesn't render the divider.
function renderVolumeRows(
  volumeIndex: Map<number, VolumeEntry>,
  hoveredTimestampSec: number,
  stepSec: number,
): React.ReactNode {
  if (volumeIndex.size === 0) return null
  const tolerance = stepSec / 2
  let bestEntry: VolumeEntry | null = null
  let bestDist = Infinity
  for (const [ts, entry] of volumeIndex) {
    const d = Math.abs(ts - hoveredTimestampSec)
    if (d < bestDist && d <= tolerance) {
      bestEntry = entry
      bestDist = d
    }
  }
  if (!bestEntry) return null
  return (
    <>
      <div className="flex items-center gap-2">
        <span className="text-kb-text-tertiary text-[10px] uppercase tracking-[0.06em]">
          Volume
        </span>
        <span className="ml-auto tabular-nums font-mono text-kb-text-primary">
          {formatRate(bestEntry.total)}
        </span>
      </div>
      <div className="flex items-center gap-2">
        <span
          className="w-2 h-2 rounded-full flex-shrink-0"
          style={{ background: '#ef4056' }}
        />
        <span className="text-kb-text-secondary">Errors</span>
        <span className="ml-auto tabular-nums font-mono text-kb-text-primary">
          {formatRate(bestEntry.errors)}
        </span>
      </div>
    </>
  )
}

function formatRate(reqPerSec: number): string {
  if (!Number.isFinite(reqPerSec)) return '—'
  if (reqPerSec === 0) return '0 req/s'
  if (reqPerSec < 1) return `${reqPerSec.toFixed(2)} req/s`
  if (reqPerSec < 10) return `${reqPerSec.toFixed(1)} req/s`
  return `${Math.round(reqPerSec)} req/s`
}

// HubbleMissingPlaceholder — direct-nav fallback. The tab itself
// hides when Hubble isn't detected, so seeing this means either a
// stale link, a URL-typed visit, or Hubble disappeared mid-session
// (the polling will hide the tab on the next tick). Copy explains
// what the panels need to populate.
function HubbleMissingPlaceholder() {
  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-8 text-center space-y-2">
      <h3 className="text-sm font-semibold text-kb-text-primary">
        Reliability needs Hubble L7 visibility
      </h3>
      <p className="text-xs text-kb-text-secondary max-w-md mx-auto">
        This view is populated by Hubble's HTTP flow telemetry shipped through the
        KubeBolt Agent. Once Cilium + Hubble are running and the agent has L7 enabled,
        the panels here populate automatically.
      </p>
    </div>
  )
}
