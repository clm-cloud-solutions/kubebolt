import type { ClusterOverview } from '@/types/kubernetes'

interface WorkloadHealthProps {
  overview: ClusterOverview
}

function HealthRow({ label, ready, total }: { label: string; ready: number; total: number }) {
  const percent = total > 0 ? (ready / total) * 100 : 100
  const notReady = total - ready

  return (
    <div className="flex items-center gap-3">
      <span className="text-[11px] text-kb-text-secondary w-24 shrink-0">{label}</span>
      <div className="flex-1 h-2 rounded-full overflow-hidden bg-[var(--kb-bar-track)]">
        <div className="flex h-full">
          <div
            className="h-full bg-status-ok transition-all duration-500"
            style={{ width: `${percent}%` }}
          />
          {notReady > 0 && (
            <div
              className="h-full bg-status-error transition-all duration-500"
              style={{ width: `${((notReady) / total) * 100}%` }}
            />
          )}
        </div>
      </div>
      <span className="text-[10px] font-mono text-kb-text-secondary w-12 text-right shrink-0">
        {ready}/{total}
      </span>
    </div>
  )
}

export function WorkloadHealth({ overview }: WorkloadHealthProps) {
  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <div className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary mb-4">
        Workload Health
      </div>
      <div className="space-y-3">
        <HealthRow label="Deployments" ready={overview.deployments?.ready ?? 0} total={overview.deployments?.total ?? 0} />
        <HealthRow label="StatefulSets" ready={overview.statefulSets?.ready ?? 0} total={overview.statefulSets?.total ?? 0} />
        <HealthRow label="DaemonSets" ready={overview.daemonSets?.ready ?? 0} total={overview.daemonSets?.total ?? 0} />
        <HealthRow label="Jobs" ready={overview.jobs?.ready ?? 0} total={overview.jobs?.total ?? 0} />
      </div>
    </div>
  )
}
