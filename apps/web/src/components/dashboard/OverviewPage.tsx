import { useClusterOverview } from '@/hooks/useClusterOverview'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { Phase2Placeholder } from '@/components/shared/Phase2Placeholder'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
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

      {/* Network + Resource Utilization (Phase 2) */}
      <div className="grid grid-cols-2 gap-3">
        <Phase2Placeholder title="Network Monitoring" description="Real-time network traffic analysis between pods and services" />
        <Phase2Placeholder title="Resource Utilization Trends" description="Historical CPU, memory, and storage usage patterns" />
      </div>

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
