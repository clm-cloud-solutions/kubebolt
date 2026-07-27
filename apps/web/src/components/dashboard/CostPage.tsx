import { DollarSign, LineChart } from 'lucide-react'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { useDashboardRange } from '@/hooks/useDashboardRange'
import { useAgentInstalled } from '@/hooks/useAgentInstalled'
import { useCostAvailable } from '@/hooks/useCostAvailable'
import {
  useClusterCost,
  useNodeRates,
  estimateMonthlySavings,
  NETWORK_COST_QUERY,
  ALLOCATED_TOTAL_QUERY,
} from '@/hooks/useClusterCost'
import { useRightSizing } from '@/hooks/useRightSizing'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { MetricChart } from '@/components/shared/MetricChart'
import { RangeSelector } from '@/components/shared/RangeSelector'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { DashboardSubTabs } from './DashboardSubTabs'
import { OverviewHeader } from './OverviewHeader'
import { CostKpis } from './CostKpis'
import { CostBreakdown } from './CostBreakdown'
import { CostRightsizing } from './CostRightsizing'

// CostPage is the fourth dashboard sub-tab — the per-cluster FinOps
// lens on the spend the other tabs generate (design/
// kubebolt-cost-redesign.html). It's gated on OpenCost shipping cost
// series into VM (useCostAvailable), same present-or-hidden rule as
// Reliability's Hubble gate, and carries a Beta pill: available to
// every plan while in beta; the Team-tier gate lands here later.
//
// Everything is client-side over the generic PromQL proxy — no cost-
// specific API route. The backend scopes each query to the active
// cluster (cluster_id injection), so this is per-cluster cost. The
// $/mo savings figure comes from the SAME useRightSizing engine the
// Capacity panel uses, priced at the cluster's node rates — one
// engine, two lenses (sizing on Capacity, savings here).
//
// Direct-nav defense: if reached on a cluster with no cost feed
// (stale link, agent restarted, OpenCost uninstalled), an explanatory
// empty state renders instead of empty panels.
export function CostPage() {
  const { data: overview, isLoading, error, refetch, dataUpdatedAt, isFetching } = useClusterOverview()
  // Shared session-scoped range — sticky across the sub-tab switch.
  const [rangeMinutes, setRangeMinutes] = useDashboardRange()
  const { available: costAvailable, isLoading: costLoading } = useCostAvailable()
  const { installed } = useAgentInstalled()

  // Cost model + node rates, only fetched once the cluster is known to
  // have OpenCost data. Rates feed the $/mo pricing of the shared
  // right-sizing recommendations.
  const cost = useClusterCost(costAvailable)
  const rates = useNodeRates(costAvailable)
  const { recs, totals, preliminary, windowDays } = useRightSizing(installed, overview)
  const savingsMonthly = estimateMonthlySavings(
    totals.reclaimCpuMilli,
    totals.reclaimMemBytes,
    rates,
  )

  if (isLoading) return <LoadingSpinner />
  if (error || !overview) return <ErrorState message={error?.message} onRetry={() => refetch()} />

  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-4 flex-wrap">
        <OverviewHeader
          overview={overview}
          tab="Cost"
          badge={
            <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-kb-accent-light text-kb-accent text-[9px] font-mono uppercase tracking-[0.08em] shrink-0">
              <DollarSign className="w-2.5 h-2.5" />
              OpenCost · Beta
            </span>
          }
        />
        <div className="flex items-center gap-3 mt-1">
          <RangeSelector value={rangeMinutes} onChange={setRangeMinutes} />
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
      </div>

      <DashboardSubTabs />

      {!costAvailable && !costLoading ? (
        <CostUnavailablePlaceholder />
      ) : (
        <>
          {/* Scan layer — spend / waste / savings / unit cost /
              efficiency, all derived from the panels below. */}
          <CostKpis
            cost={cost}
            savingsMonthly={savingsMonthly}
            recCount={totals.count}
            ratesAvailable={rates.available}
            preliminary={preliminary}
            windowDays={windowDays}
          />

          {/* Cost trends — one enclosing card for the two run-rate charts, the
              same "big panel" treatment as Capacity's Cluster trends. The charts
              inside go `recessed` so they read as panels within the card rather
              than cards-in-a-card. */}
          <div className="rounded-xl border border-kb-border bg-kb-card p-4">
            <div className="flex items-center justify-between gap-3 flex-wrap mb-3">
              <div className="flex items-center gap-2 min-w-0">
                <span className="text-kb-text-secondary shrink-0">
                  <LineChart className="w-4 h-4" />
                </span>
                <h3 className="text-sm font-semibold text-kb-text-primary">Cost trends</h3>
              </div>
              <span className="text-[10px] text-kb-text-tertiary">run-rate over selected range</span>
            </div>
            {/* Two side-by-side lenses on the same $ figures. Left = where the
                money goes (spend by component). Right = how much of what you pay
                workloads actually claim. They answer different questions, so they
                sit apart rather than sharing one over-loaded chart. */}
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
              {/* Spend run-rate over the range, in $/day (hourly cost ×24),
                  split by OpenCost's cost components:
                    Compute = node_total_hourly_cost (CPU + RAM + GPU)
                    Network = LB + egress (kubecost_network_*)
                    Storage = pv_hourly_cost (persistent volumes)
                  Storage falls back to $0 (`or vector(0)`) on clusters where
                  OpenCost emits no PV cost — e.g. kind's local-path storage
                  has no disk pricing — and lights up automatically on cloud
                  clusters with priced EBS/PD/Azure disks. A gauge-based
                  run-rate reads flat when nothing scales — honest: the line
                  moves when node count or pricing changes, which is exactly
                  when spend changes. */}
              <MetricChart
                title="Spend run-rate"
                icon={<DollarSign className="w-4 h-4" />}
                unit="usd"
                unitNote="$/day"
                transform={(v) => v * 24}
                queries={[
                  { query: 'sum(node_total_hourly_cost)', prefix: 'Compute', accent: '#22d68a' },
                  { query: NETWORK_COST_QUERY, prefix: 'Network', accent: '#4c9aff' },
                  { query: 'sum(pv_hourly_cost) or vector(0)', prefix: 'Storage', accent: '#a855f7' },
                ]}
                seriesLabel={(_labels, prefix) => prefix ?? ''}
                chartType="area"
                showStats={false}
                height={190}
                controlledRangeMinutes={rangeMinutes}
                recessed
              />

              {/* Allocated vs total cost — the total node cost you pay (the
                  ceiling) against the compute workloads actually claim, both in
                  $/day. Overlaid areas (NOT stacked): Allocated (slate) fills the
                  bottom; the band up to Total cost (blue) is idle you're paying
                  for. Palette mirrors Capacity's node-total-vs-pods charts — blue
                  ceiling (#3b82f6 = METRIC_ACCENTS.memory[0]), slate subset
                  (#94a3b8) — so the two "total vs share" charts read alike. Unlike
                  the KPI's idle/waste (a snapshot) this shows how utilization moved
                  over the range. Same sum(node_total_hourly_cost) + attribution
                  join the KPIs derive from, as query_range; no new metrics. Total
                  cost is listed first so Allocated renders on top and the idle band
                  shows through above it. */}
              <MetricChart
                title="Allocated vs total cost"
                icon={<DollarSign className="w-4 h-4" />}
                unit="usd"
                unitNote="$/day"
                transform={(v) => v * 24}
                queries={[
                  { query: 'sum(node_total_hourly_cost)', prefix: 'Total cost', accent: '#3b82f6' },
                  { query: ALLOCATED_TOTAL_QUERY, prefix: 'Allocated', accent: '#94a3b8' },
                ]}
                seriesLabel={(_labels, prefix) => prefix ?? ''}
                chartType="area"
                showStats={false}
                height={190}
                controlledRangeMinutes={rangeMinutes}
                recessed
              />
            </div>
          </div>

          <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
            <CostBreakdown cost={cost} />
            <CostRightsizing recs={recs} rates={rates} savingsMonthly={savingsMonthly} preliminary={preliminary} windowDays={windowDays} />
          </div>
        </>
      )}
    </div>
  )
}

// CostUnavailablePlaceholder — direct-nav fallback. The tab hides
// when OpenCost isn't detected, so reaching this means a stale link,
// a URL-typed visit, or OpenCost disappeared mid-session (polling
// will hide the tab on the next tick). Copy explains what the panels
// need — mirrors Reliability's Hubble placeholder in shape.
function CostUnavailablePlaceholder() {
  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-8 text-center space-y-3">
      <div className="inline-flex items-center justify-center w-10 h-10 rounded-full bg-kb-accent-light mb-1">
        <DollarSign className="w-5 h-5 text-kb-accent" />
      </div>
      <h3 className="text-sm font-semibold text-kb-text-primary">
        Cost visibility needs OpenCost
      </h3>
      <div className="text-xs text-kb-text-secondary max-w-lg mx-auto space-y-2">
        <p>
          This view is populated by OpenCost's cost allocation metrics shipped through the
          KubeBolt Agent. Enable cost monitoring on this cluster — a bundled OpenCost
          sub-chart, an existing OpenCost install the agent scrapes, or the promRead cost
          flag — and the panels here populate automatically once the first samples flow.
        </p>
        <p>
          <a
            href="https://github.com/clm-cloud-solutions/kubebolt/tree/main/deploy/helm/kubebolt-agent#opencost"
            target="_blank"
            rel="noreferrer"
            className="text-kb-accent hover:underline"
          >
            Agent docs — enabling OpenCost →
          </a>
        </p>
      </div>
    </div>
  )
}
