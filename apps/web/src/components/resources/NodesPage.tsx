import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { useResources } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { StatusBadge } from './StatusBadge'
import { UsageBar } from './UsageBar'
import { AgentRequiredPlaceholder } from '@/components/shared/AgentRequiredPlaceholder'
import { MetricChart } from '@/components/shared/MetricChart'
import { api } from '@/services/api'
import { formatCPU, formatMemory } from '@/utils/formatters'
import type { ResourceItem } from '@/types/kubernetes'

function NodeCard({ node }: { node: ResourceItem }) {
  const cpuPercent = Number(node.cpuPercent ?? 0)
  const memPercent = Number(node.memoryPercent ?? 0)
  const cpuUsage = Number(node.cpuUsage ?? 0)
  const memUsage = Number(node.memoryUsage ?? 0)
  const cpuAlloc = Number(node.cpuAllocatable ?? 0)
  const memAlloc = Number(node.memoryAllocatable ?? 0)
  const podCount = Number(node.podCount ?? 0)
  const podCapacity = Number(node.podCapacity ?? 110)
  const kubeletVersion = (node.kubeletVersion as string) ?? ''
  const containerRuntime = (node.containerRuntime as string) ?? ''
  const hasMetrics = cpuUsage > 0 || memUsage > 0

  return (
    <Link to={`/nodes/_/${node.name}`} className="block bg-kb-card border border-kb-border rounded-[10px] p-4 hover:bg-kb-card-hover transition-colors">
      {/* Header */}
      <div className="flex items-center gap-2.5 mb-3">
        <div className={`w-2.5 h-2.5 rounded-full ${node.status === 'Ready' ? 'bg-status-ok' : 'bg-status-error'}`} />
        <div className="flex-1 min-w-0">
          <div className="text-[13px] font-semibold text-kb-text-primary truncate">{node.name}</div>
          <div className="text-[10px] font-mono text-kb-text-tertiary">{(node.labels as Record<string, string>)?.['node.kubernetes.io/instance-type'] || ''}</div>
        </div>
      </div>

      {/* Bars */}
      <div className="space-y-2.5">
        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-kb-text-tertiary">CPU</span>
            <span className="text-[10px] font-mono text-kb-text-secondary">
              {hasMetrics ? `${Math.round(cpuPercent)}% · ${formatCPU(cpuUsage)}/${formatCPU(cpuAlloc)}` : `${formatCPU(cpuAlloc)} alloc`}
            </span>
          </div>
          <UsageBar percent={cpuPercent} height={6} />
        </div>

        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-kb-text-tertiary">Mem</span>
            <span className="text-[10px] font-mono text-kb-text-secondary">
              {hasMetrics ? `${Math.round(memPercent)}% · ${formatMemory(memUsage)}/${formatMemory(memAlloc)}` : `${formatMemory(memAlloc)} alloc`}
            </span>
          </div>
          <UsageBar percent={memPercent} height={6} />
        </div>

        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-kb-text-tertiary">Pods</span>
            <span className="text-[10px] font-mono text-kb-text-secondary">{podCount}/{podCapacity}</span>
          </div>
          <UsageBar percent={podCapacity > 0 ? (podCount / podCapacity) * 100 : 0} height={6} />
        </div>
      </div>

      {/* Footer */}
      <div className="mt-3 text-[10px] font-mono text-kb-text-tertiary">
        {kubeletVersion}{containerRuntime ? ` · ${containerRuntime}` : ''}
      </div>
    </Link>
  )
}

export function NodesPage() {
  const { data, isLoading, error, refetch, dataUpdatedAt, isFetching } = useResources('nodes')

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const nodes = data?.items || []

  return (
    <div>
      <div className="flex items-center gap-3 mb-4">
        <h1 className="text-lg font-semibold text-kb-text-primary">Nodes</h1>
        <span className="text-[10px] font-mono px-2.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary">
          {nodes.length} total
        </span>
        <div className="ml-auto">
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
      </div>
      <div className="grid grid-cols-3 gap-3 mb-5">
        {nodes.map((node) => (
          <NodeCard key={node.name} node={node} />
        ))}
      </div>
      <NodeFleetCharts />
    </div>
  )
}

// NodeFleetCharts renders per-node disk and network activity in two
// multi-series charts. The agent emits node_fs_used_bytes and
// node_network_{receive,transmit}_bytes_total with a `node` label,
// which MetricChart auto-picks up as one series per node without
// any extra config. When the agent is absent we fall back to the
// install prompt — same information, no misleading empty charts.
function NodeFleetCharts() {
  const { data: agent, isLoading } = useQuery({
    queryKey: ['integration', 'agent'],
    queryFn: () => api.getIntegration('agent'),
    refetchInterval: 10_000,
    staleTime: 5_000,
  })

  if (isLoading) return null

  const installed = agent && (agent.status === 'installed' || agent.status === 'degraded')
  if (!installed) {
    return (
      <AgentRequiredPlaceholder
        title="Disk I/O & Network per node"
        description="Detailed node metrics require the KubeBolt Agent. Install it from Administration → Integrations to unlock per-node disk and network charts on this page."
      />
    )
  }

  // Network chart mirrors the Overview's "RX up / TX down" layout
  // but scoped per-node. Using two separate queries (rather than
  // adding inside the rate()) avoids a PromQL vector-match failure
  // when RX and TX series carry different auxiliary labels: the
  // outer sum by (node) collapses each side to one series per node
  // first, after which the halves can share one chart cleanly with
  // TX negated below the axis.
  //
  // No accents override: with N nodes × 2 directions we'd end up
  // with all RX lines in one color and all TX in another, losing
  // the per-node distinction. The default palette picks a unique
  // hue per series ("RX worker", "TX worker", "RX control-plane",
  // "TX control-plane"), and the series label keeps the direction
  // visible in the tooltip and legend.
  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <MetricChart
        title="Filesystem used per node"
        unit="bytes"
        query="node_fs_used_bytes"
        chartType="area"
      />
      <MetricChart
        title="Network activity per node (RX up / TX down)"
        unit="bytes/s"
        queries={[
          { query: 'sum by (node) (rate(node_network_receive_bytes_total[1m]))', prefix: 'RX' },
          { query: 'sum by (node) (rate(node_network_transmit_bytes_total[1m]))', prefix: 'TX', negate: true },
        ]}
        chartType="area"
      />
    </div>
  )
}
