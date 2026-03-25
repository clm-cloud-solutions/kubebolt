import { Link } from 'react-router-dom'
import type { WorkloadSummary } from '@/types/kubernetes'
import { getDotColor, getUsageBarColor } from '@/utils/colors'
import { formatCPU, formatMemory } from '@/utils/formatters'

const kindToRoute: Record<string, string> = {
  Deployment: 'deployments', StatefulSet: 'statefulsets', DaemonSet: 'daemonsets',
  Job: 'jobs', CronJob: 'cronjobs',
}

interface DeploymentCardProps {
  workload: WorkloadSummary
}

function getCardStatus(w: WorkloadSummary): 'ok' | 'warn' | 'error' {
  if (w.readyReplicas === 0 && w.replicas > 0) return 'error'
  if (w.readyReplicas < w.replicas) return 'warn'
  return 'ok'
}

const statusBorderColor = {
  ok: 'bg-status-ok',
  warn: 'bg-status-warn',
  error: 'bg-status-error',
}

export function DeploymentCard({ workload }: DeploymentCardProps) {
  const status = getCardStatus(workload)
  const cpuUsed = workload.cpu?.used ?? 0
  const memUsed = workload.memory?.used ?? 0
  const cpuPct = workload.cpu?.percentUsed ?? 0
  const memPct = workload.memory?.percentUsed ?? 0
  const pods = workload.pods ?? []
  const hasMetrics = cpuUsed > 0 || memUsed > 0

  return (
    <Link
      to={`/${kindToRoute[workload.kind] ?? 'deployments'}/${workload.namespace}/${workload.name}`}
      className="block bg-kb-card border border-kb-border rounded-[9px] p-3 hover:bg-kb-card-hover hover:-translate-y-px transition-all cursor-pointer relative overflow-hidden"
    >
      {/* Left status bar */}
      <div className={`absolute left-0 top-0 bottom-0 w-[3px] rounded-l-[9px] ${statusBorderColor[status]}`} />

      {/* Name */}
      <div className="text-[12px] font-medium text-kb-text-primary truncate mb-1.5 pl-1">
        {workload.name}
      </div>

      {/* Type + replicas */}
      <div className="flex items-center justify-between mb-2 pl-1">
        <span className="text-[9px] font-mono uppercase tracking-[0.04em] text-kb-text-tertiary">
          {workload.kind}
        </span>
        <span className="text-[10px] font-mono text-kb-text-secondary">
          {workload.readyReplicas}/{workload.replicas}
        </span>
      </div>

      {/* Pod dots */}
      {pods.length > 0 && (
        <div className="flex gap-1 flex-wrap mb-2 pl-1">
          {pods.map((pod) => (
            <div
              key={pod.name}
              className={`w-[9px] h-[9px] rounded-full ${getDotColor(pod.status)} ${
                pod.ready ? 'shadow-[0_0_5px_rgba(34,214,138,0.4)]' : ''
              }`}
              title={`${pod.name}: ${pod.status}`}
            />
          ))}
        </div>
      )}

      {/* CPU / Memory micro bars */}
      {hasMetrics ? (
        <div className="flex gap-1.5 mt-2 pl-1">
          <div className="flex-1">
            <div className="flex items-center justify-between mb-0.5">
              <span className="text-[8px] font-mono text-kb-text-tertiary uppercase">CPU</span>
              <span className="text-[8px] font-mono text-kb-text-secondary">{formatCPU(cpuUsed)}</span>
            </div>
            <div className="h-[3px] rounded-sm overflow-hidden" style={{ background: 'var(--kb-bar-track)' }}>
              <div
                className="h-full rounded-sm transition-all duration-700"
                style={{ width: `${Math.max(2, Math.min(100, cpuPct))}%`, background: getUsageBarColor(cpuPct) }}
              />
            </div>
          </div>
          <div className="flex-1">
            <div className="flex items-center justify-between mb-0.5">
              <span className="text-[8px] font-mono text-kb-text-tertiary uppercase">MEM</span>
              <span className="text-[8px] font-mono text-kb-text-secondary">{formatMemory(memUsed)}</span>
            </div>
            <div className="h-[3px] rounded-sm overflow-hidden" style={{ background: 'var(--kb-bar-track)' }}>
              <div
                className="h-full rounded-sm transition-all duration-700"
                style={{ width: `${Math.max(2, Math.min(100, memPct))}%`, background: getUsageBarColor(memPct) }}
              />
            </div>
          </div>
        </div>
      ) : (
        <div className="flex gap-1.5 mt-2 pl-1">
          <div className="flex-1">
            <div className="text-[8px] font-mono text-kb-text-tertiary uppercase mb-0.5">CPU</div>
            <div className="h-[3px] rounded-sm" style={{ background: 'var(--kb-bar-track)' }} />
          </div>
          <div className="flex-1">
            <div className="text-[8px] font-mono text-kb-text-tertiary uppercase mb-0.5">MEM</div>
            <div className="h-[3px] rounded-sm" style={{ background: 'var(--kb-bar-track)' }} />
          </div>
        </div>
      )}
    </Link>
  )
}
