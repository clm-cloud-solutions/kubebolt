import { useResources } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { StatusBadge } from './StatusBadge'
import { FolderOpen } from 'lucide-react'
import type { ResourceItem } from '@/types/kubernetes'

function NamespaceCard({ ns }: { ns: ResourceItem }) {
  const podCount = (ns.podCount as number) ?? 0
  const deploymentCount = (ns.deploymentCount as number) ?? 0
  const serviceCount = (ns.serviceCount as number) ?? 0

  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4 hover:bg-kb-card-hover transition-colors">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <FolderOpen className="w-4 h-4 text-[#a78bfa]" />
          <span className="text-sm font-mono text-[#e8e9ed]">{ns.name}</span>
        </div>
        <StatusBadge status={ns.status || 'Active'} />
      </div>

      <div className="grid grid-cols-3 gap-2">
        <div className="bg-kb-bg rounded-md p-2 text-center">
          <div className="text-sm font-semibold text-[#e8e9ed]">{podCount}</div>
          <div className="text-[9px] font-mono text-[#555770] uppercase tracking-[0.08em]">Pods</div>
        </div>
        <div className="bg-kb-bg rounded-md p-2 text-center">
          <div className="text-sm font-semibold text-[#e8e9ed]">{deploymentCount}</div>
          <div className="text-[9px] font-mono text-[#555770] uppercase tracking-[0.08em]">Deploys</div>
        </div>
        <div className="bg-kb-bg rounded-md p-2 text-center">
          <div className="text-sm font-semibold text-[#e8e9ed]">{serviceCount}</div>
          <div className="text-[9px] font-mono text-[#555770] uppercase tracking-[0.08em]">Services</div>
        </div>
      </div>
    </div>
  )
}

export function NamespacesPage() {
  const { data, isLoading, error, refetch } = useResources('namespaces')

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const namespaces = data?.items || []

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-lg font-semibold text-[#e8e9ed]">Namespaces</h1>
        <span className="text-[10px] font-mono text-[#555770] uppercase tracking-[0.08em]">
          {namespaces.length} namespaces
        </span>
      </div>
      <div className="grid grid-cols-3 gap-3">
        {namespaces.map((ns) => (
          <NamespaceCard key={ns.name} ns={ns} />
        ))}
      </div>
    </div>
  )
}
