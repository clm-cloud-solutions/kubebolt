import type { NamespaceWorkload } from '@/types/kubernetes'
import { DeploymentCard } from './DeploymentCard'

interface NamespaceSectionProps {
  namespaceWorkload: NamespaceWorkload
}

export function NamespaceSection({ namespaceWorkload }: NamespaceSectionProps) {
  const workloads = namespaceWorkload.workloads ?? []
  const totalPods = workloads.reduce((sum, w) => sum + (w.pods?.length ?? 0), 0)

  return (
    <div className="animate-fade-up">
      {/* Namespace header */}
      <div className="flex items-center gap-3 mb-3 pb-2 border-b border-kb-border">
        <span className="text-sm font-semibold text-kb-text-primary">
          {namespaceWorkload.namespace}
        </span>
        <span className="text-[9px] font-mono px-2 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary uppercase tracking-[0.04em]">
          namespace
        </span>
        <span className="ml-auto text-[11px] font-mono text-kb-text-tertiary">
          {workloads.length} deployments · {totalPods} pods
        </span>
      </div>

      {/* Workload cards grid */}
      <div className="grid gap-2" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(195px, 1fr))' }}>
        {workloads.map((workload) => (
          <DeploymentCard key={`${workload.namespace}-${workload.name}`} workload={workload} />
        ))}
      </div>
    </div>
  )
}
