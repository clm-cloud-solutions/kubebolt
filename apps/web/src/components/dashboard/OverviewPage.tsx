import { useClusterOverview } from '@/hooks/useClusterOverview'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { KpiCards } from './KpiCards'
import { OverviewHeader } from './OverviewHeader'
import { ResourceUsagePanel } from './ResourceUsage'
import { WorkloadHealth } from './WorkloadHealth'
import { EventsFeed } from './EventsFeed'
import { NamespaceTiles } from './NamespaceTiles'
import { DashboardSubTabs } from './DashboardSubTabs'

// OverviewPage is the "abro el dashboard en la mañana" scan: 4 KPIs,
// commitment bars, the events + workload-health pair, and namespace
// tiles. No time-series trends, no cluster-wide top consumers — those
// belong to the Capacity tab where the user is in investigation mode
// (own range selector, deeper instrumentation) rather than scanning.
//
// The page intentionally has no RangeSelector: every panel here is
// instantaneous (current state from the overview payload). When the
// user wants "how has this moved?" they pivot to Capacity. Keeps
// Overview fast to read and free of interactive sliders that would
// distract from the scan.
export function OverviewPage() {
  const { data: overview, isLoading, error, refetch, dataUpdatedAt, isFetching } = useClusterOverview()

  if (isLoading) return <LoadingSpinner />
  if (error || !overview) return <ErrorState message={error?.message} onRetry={() => refetch()} />

  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-4 flex-wrap">
        <OverviewHeader overview={overview} />
        <div className="flex items-center gap-3 mt-1">
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
      </div>

      <DashboardSubTabs />

      <KpiCards overview={overview} />

      <ResourceUsagePanel
        cpu={overview.cpu}
        memory={overview.memory}
        metricsAvailable={!overview.health?.checks?.some(c => c.name === 'metrics' && c.status !== 'pass')}
        nodesRestricted={overview.permissions?.nodes === false}
      />

      <div className="grid grid-cols-2 gap-3">
        <EventsFeed events={overview.events?.slice(0, 15) || []} />
        <WorkloadHealth overview={overview} />
      </div>

      {overview.namespaceWorkloads && overview.namespaceWorkloads.length > 0 && (
        <NamespaceTiles namespaceWorkloads={overview.namespaceWorkloads} />
      )}
    </div>
  )
}
