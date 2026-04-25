import { useQuery } from '@tanstack/react-query'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { MetricChart, METRIC_ACCENTS } from '@/components/shared/MetricChart'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { AgentRequiredPlaceholder } from '@/components/shared/AgentRequiredPlaceholder'
import { api } from '@/services/api'
import { SummaryCards } from './SummaryCards'
import { ResourceUsagePanel } from './ResourceUsage'
import { WorkloadHealth } from './WorkloadHealth'
import { EventsFeed } from './EventsFeed'
import { NamespaceSection } from './NamespaceSection'

export function OverviewPage() {
  const { data: overview, isLoading, error, refetch, dataUpdatedAt, isFetching } = useClusterOverview()

  if (isLoading) return <LoadingSpinner />
  if (error || !overview) return <ErrorState message={error?.message} onRetry={() => refetch()} />

  return (
    <div className="space-y-4">
      {/* Freshness indicator */}
      <div className="flex justify-end">
        <DataFreshnessIndicator
          dataUpdatedAt={dataUpdatedAt}
isFetching={isFetching}
        />
      </div>

      {/* Summary cards */}
      <SummaryCards overview={overview} />

      {/* CPU + Memory Usage */}
      <ResourceUsagePanel
        cpu={overview.cpu}
        memory={overview.memory}
        metricsAvailable={!overview.health?.checks?.some(c => c.name === 'metrics' && c.status !== 'pass')}
        nodesRestricted={overview.permissions?.nodes === false}
      />

      {/* Events + Workload Health */}
      <div className="grid grid-cols-2 gap-3">
        <EventsFeed events={overview.events?.slice(0, 15) || []} />
        <WorkloadHealth overview={overview} />
      </div>

      {/* Time-series view of cluster resources from the agent.
          Distinguished from the CPU/Memory commitment bars above:
          those show what's reserved and used *right now* (works
          without the agent, via Metrics Server); these show how it
          *moved* over the selected window. The whole panel hides
          itself when the agent isn't installed — current values are
          already covered above, and rendering empty charts here
          would be misleading. */}
      <ClusterTrendsPanel />

      {/* Namespace Workload Sections */}
      {overview.namespaceWorkloads && overview.namespaceWorkloads.length > 0 && (
        <div className="space-y-5 mt-2">
          <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            Workloads por namespace
          </div>
          {overview.namespaceWorkloads.map((nsw) => (
            <NamespaceSection key={nsw.namespace} namespaceWorkload={nsw} />
          ))}
        </div>
      )}
    </div>
  )
}

// ─── Cluster trends ─────────────────────────────────────────────

function ClusterTrendsPanel() {
  const { data: agent, isLoading } = useQuery({
    queryKey: ['integration', 'agent'],
    queryFn: () => api.getIntegration('agent'),
    refetchInterval: 10_000,
    staleTime: 5_000,
  })

  if (isLoading) return null

  const installed = agent && (agent.status === 'installed' || agent.status === 'degraded')

  if (!installed) {
    // Current CPU and memory are already covered by the bars above
    // (ResourceUsagePanel, sourced from Metrics Server), so we don't
    // duplicate them here — just leave a clear "trends need the
    // agent" notice in place of the chart grid.
    return (
      <div className="space-y-2 pt-2">
        <div className="flex items-baseline justify-between">
          <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            Cluster trends
          </div>
          <div className="text-[10px] text-kb-text-tertiary">
            current totals shown above · agent unlocks history
          </div>
        </div>
        <AgentRequiredPlaceholder
          title="Time-series trends require the KubeBolt Agent"
          description="Current CPU and memory totals are already shown above, sourced from the Kubernetes Metrics Server. Install the agent to unlock historical CPU, memory, network, and filesystem trends with a selectable range."
          hideWhileLoading
        />
      </div>
    )
  }

  return (
    <div className="space-y-2 pt-2">
      <div className="flex items-baseline justify-between">
        <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
          Cluster trends
        </div>
        <div className="text-[10px] text-kb-text-tertiary">
          actual usage over time · range selectable per chart
        </div>
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
        />
      </div>
    </div>
  )
}
