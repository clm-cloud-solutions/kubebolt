import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import type { ClusterOverview } from '@/types/kubernetes'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { MetricChart, METRIC_ACCENTS } from '@/components/shared/MetricChart'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { AgentRequiredPlaceholder } from '@/components/shared/AgentRequiredPlaceholder'
import { RangeSelector } from '@/components/shared/RangeSelector'
import { api } from '@/services/api'
import { KpiCards } from './KpiCards'
import { OverviewHeader } from './OverviewHeader'
import { ResourceUsagePanel } from './ResourceUsage'
import { WorkloadHealth } from './WorkloadHealth'
import { EventsFeed } from './EventsFeed'
import { NamespaceTiles } from './NamespaceTiles'
import { TopWorkloadsCpu } from './TopWorkloadsCpu'

export function OverviewPage() {
  const { data: overview, isLoading, error, refetch, dataUpdatedAt, isFetching } = useClusterOverview()

  // Single time-range state shared by every chart on the page. The
  // option set and 15m default match the per-resource Monitor tabs
  // elsewhere in the app — one mental model across views.
  const [rangeMinutes, setRangeMinutes] = useState(15)

  if (isLoading) return <LoadingSpinner />
  if (error || !overview) return <ErrorState message={error?.message} onRetry={() => refetch()} />

  return (
    <div className="space-y-5">
      {/* Title + range/freshness sit on a single row: title left,
          controls right. The title block grounds the page (says "you
          are on Overview, looking at <cluster>") and the controls stay
          visually paired with their related sections by being on the
          same baseline. */}
      <div className="flex items-start justify-between gap-4 flex-wrap">
        <OverviewHeader overview={overview} />
        <div className="flex items-center gap-3 mt-1">
          <RangeSelector value={rangeMinutes} onChange={setRangeMinutes} />
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
      </div>

      {/* Headline KPIs */}
      <KpiCards overview={overview} />

      {/* CPU + Memory commitment bars — sourced from Metrics Server, so
          they keep working without the agent. Distinct from the trends
          below: this is what's reserved/used right now. */}
      <ResourceUsagePanel
        cpu={overview.cpu}
        memory={overview.memory}
        metricsAvailable={!overview.health?.checks?.some(c => c.name === 'metrics' && c.status !== 'pass')}
        nodesRestricted={overview.permissions?.nodes === false}
      />

      {/* Time-series trends + cluster-wide top workloads. Both ride on
          the agent's VictoriaMetrics samples; gated together so the
          empty state explains the trade-off once instead of twice. */}
      <AgentTrendsBlock rangeMinutes={rangeMinutes} overview={overview} />

      {/* Events + Workload Health */}
      <div className="grid grid-cols-2 gap-3">
        <EventsFeed events={overview.events?.slice(0, 15) || []} />
        <WorkloadHealth overview={overview} />
      </div>

      {/* Namespace tile grid — replaces the older per-namespace
          expandable sections. Compact summary by design: pods count
          + health pattern, click drills into /namespaces. */}
      {overview.namespaceWorkloads && overview.namespaceWorkloads.length > 0 && (
        <NamespaceTiles namespaceWorkloads={overview.namespaceWorkloads} />
      )}
    </div>
  )
}

// ─── Agent-dependent block ──────────────────────────────────────

function AgentTrendsBlock({
  rangeMinutes,
  overview,
}: {
  rangeMinutes: number
  overview: ClusterOverview
}) {
  const { data: agent, isLoading } = useQuery({
    queryKey: ['integration', 'agent'],
    queryFn: () => api.getIntegration('agent'),
    refetchInterval: 10_000,
    staleTime: 5_000,
  })

  if (isLoading) return null

  const installed = !!agent && (agent.status === 'installed' || agent.status === 'degraded')

  if (!installed) {
    // Current CPU and memory are already covered by the bars above
    // (ResourceUsagePanel, sourced from Metrics Server), so we don't
    // duplicate them here — just leave a clear "trends + top workloads
    // need the agent" notice in place of both panels.
    return (
      <div className="space-y-2 pt-2">
        <div className="flex items-baseline justify-between">
          <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            Cluster trends · top consumers
          </div>
          <div className="text-[10px] text-kb-text-tertiary">
            current totals shown above · agent unlocks history
          </div>
        </div>
        <AgentRequiredPlaceholder
          title="Time-series trends and cluster-wide top consumers require the KubeBolt Agent"
          description="Current CPU and memory totals are already shown above, sourced from the Kubernetes Metrics Server. Install the agent to unlock historical CPU, memory, network, and filesystem trends, plus a cluster-wide top-workloads view."
          hideWhileLoading
        />
      </div>
    )
  }

  return (
    <div className="space-y-3 pt-2">
      <div className="flex items-baseline justify-between">
        <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
          Cluster trends
        </div>
        <div className="text-[10px] text-kb-text-tertiary">actual usage over selected range</div>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <MetricChart
          title="CPU usage"
          unit="cores"
          query={`sum(node_cpu_usage_cores)`}
          seriesLabel={() => 'cluster total'}
          accents={METRIC_ACCENTS.cpu}
          chartType="area"
          showStats={false}
          height={180}
          controlledRangeMinutes={rangeMinutes}
        />
        <MetricChart
          title="Memory working set"
          unit="bytes"
          query={`sum(node_memory_working_set_bytes)`}
          seriesLabel={() => 'cluster total'}
          accents={METRIC_ACCENTS.memory}
          chartType="area"
          showStats={false}
          height={180}
          controlledRangeMinutes={rangeMinutes}
        />
        <MetricChart
          title="Network activity (RX up / TX down)"
          unit="bytes/s"
          queries={[
            { query: `sum(rate(node_network_receive_bytes_total[1m]))`, prefix: 'RX' },
            { query: `sum(rate(node_network_transmit_bytes_total[1m]))`, prefix: 'TX', negate: true },
          ]}
          seriesLabel={(_labels, prefix) => prefix ?? 'total'}
          accents={METRIC_ACCENTS.networkRxTx}
          chartType="area"
          showStats={false}
          height={180}
          controlledRangeMinutes={rangeMinutes}
        />
        <MetricChart
          title="Filesystem used"
          unit="bytes"
          query={`sum(node_fs_used_bytes)`}
          seriesLabel={() => 'cluster total'}
          accents={METRIC_ACCENTS.filesystem}
          chartType="area"
          showStats={false}
          height={180}
          controlledRangeMinutes={rangeMinutes}
        />
      </div>

      {/* Cluster-wide top consumers — instant query, no range needed.
          Sits next to trends because operators usually go from "is
          something climbing?" (trends) to "who's the biggest" (this) in
          one mental beat. */}
      <TopWorkloadsCpu installed={installed} overview={overview} />
    </div>
  )
}
