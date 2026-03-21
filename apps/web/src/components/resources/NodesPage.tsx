import { useResources } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { StatusBadge } from './StatusBadge'
import { UsageBar } from './UsageBar'
import { Phase2Placeholder } from '@/components/shared/Phase2Placeholder'
import { Server } from 'lucide-react'
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
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4 hover:bg-kb-card-hover transition-colors">
      {/* Header */}
      <div className="flex items-center gap-2.5 mb-3">
        <div className={`w-2.5 h-2.5 rounded-full ${node.status === 'Ready' ? 'bg-status-ok' : 'bg-status-error'}`} />
        <div className="flex-1 min-w-0">
          <div className="text-[13px] font-semibold text-[#e8e9ed] truncate">{node.name}</div>
          <div className="text-[10px] font-mono text-[#555770]">{(node.labels as Record<string, string>)?.['node.kubernetes.io/instance-type'] || ''}</div>
        </div>
      </div>

      {/* Bars */}
      <div className="space-y-2.5">
        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-[#555770]">CPU</span>
            <span className="text-[10px] font-mono text-[#8b8d9a]">
              {hasMetrics ? `${Math.round(cpuPercent)}% · ${formatCPU(cpuUsage)}/${formatCPU(cpuAlloc)}` : `${formatCPU(cpuAlloc)} alloc`}
            </span>
          </div>
          <UsageBar percent={cpuPercent} height={6} />
        </div>

        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-[#555770]">Mem</span>
            <span className="text-[10px] font-mono text-[#8b8d9a]">
              {hasMetrics ? `${Math.round(memPercent)}% · ${formatMemory(memUsage)}/${formatMemory(memAlloc)}` : `${formatMemory(memAlloc)} alloc`}
            </span>
          </div>
          <UsageBar percent={memPercent} height={6} />
        </div>

        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-[#555770]">Pods</span>
            <span className="text-[10px] font-mono text-[#8b8d9a]">{podCount}/{podCapacity}</span>
          </div>
          <UsageBar percent={podCapacity > 0 ? (podCount / podCapacity) * 100 : 0} height={6} />
        </div>
      </div>

      {/* Footer */}
      <div className="mt-3 text-[10px] font-mono text-[#555770]">
        {kubeletVersion}{containerRuntime ? ` · ${containerRuntime}` : ''}
      </div>
    </div>
  )
}

export function NodesPage() {
  const { data, isLoading, error, refetch } = useResources('nodes')

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const nodes = data?.items || []

  return (
    <div>
      <div className="flex items-center gap-3 mb-4">
        <h1 className="text-lg font-semibold text-[#e8e9ed]">Nodes</h1>
        <span className="text-[10px] font-mono px-2.5 py-0.5 rounded bg-kb-elevated text-[#555770]">
          {nodes.length} total
        </span>
      </div>
      <div className="grid grid-cols-3 gap-3 mb-5">
        {nodes.map((node) => (
          <NodeCard key={node.name} node={node} />
        ))}
      </div>
      <Phase2Placeholder
        title="Disk I/O & Network per node"
        description="Detailed node metrics require KubeBolt Agent"
      />
    </div>
  )
}
