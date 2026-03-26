import { Cpu, MemoryStick, AlertTriangle, ShieldOff } from 'lucide-react'
import type { ResourceUsage as ResourceUsageType } from '@/types/kubernetes'
import { formatPercent, formatCPU, formatMemory } from '@/utils/formatters'
import { getUsageBarColor } from '@/utils/colors'

const emptyUsage: ResourceUsageType = { used: 0, requested: 0, limit: 0, allocatable: 0, percentUsed: 0, percentRequested: 0 }

interface ResourceUsageProps {
  cpu?: ResourceUsageType
  memory?: ResourceUsageType
  metricsAvailable?: boolean
  nodesRestricted?: boolean
}

function UsageCard({
  label,
  icon,
  usage,
  formatFn,
  metricsAvailable,
  nodesRestricted,
}: {
  label: string
  icon: React.ReactNode
  usage: ResourceUsageType
  formatFn: (v: number) => string
  metricsAvailable: boolean
  nodesRestricted?: boolean
}) {
  const requestedPercent = Math.min(100, usage?.percentRequested ?? 0)
  const hasUsageData = metricsAvailable && (usage?.used ?? 0) > 0
  const usedPercent = hasUsageData ? Math.min(100, usage?.percentUsed ?? 0) : 0

  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <div className="flex items-center gap-2 mb-1">
        <div className="text-kb-text-secondary">{icon}</div>
        <span className="text-sm font-semibold text-kb-text-primary">{label}</span>
      </div>
      <div className="text-[11px] font-mono text-kb-text-tertiary mb-3">
        Requests: {formatFn(usage?.requested ?? 0)} / Limits: {formatFn(usage?.limit ?? 0)} / Total: {nodesRestricted ? 'N/A' : formatFn(usage?.allocatable ?? 0)}
      </div>

      <div className="space-y-2.5">
        {/* Requests bar */}
        <div>
          <div className="flex items-center gap-3">
            <span className="text-[10px] font-mono font-medium uppercase tracking-[0.04em] px-2 py-0.5 rounded bg-status-info-dim text-status-info border border-status-info/30 w-[68px] text-center shrink-0">
              Requests
            </span>
            <div className="flex-1 h-2 rounded bg-[var(--kb-bar-track)] overflow-hidden">
              <div
                className="h-full rounded transition-all duration-700"
                style={{ width: `${requestedPercent}%`, background: '#4c9aff' }}
              />
            </div>
            <span className="text-[11px] font-mono text-kb-text-secondary min-w-[70px] text-right">
              {formatFn(usage?.requested ?? 0)}
            </span>
          </div>
          <div className="text-[10px] font-mono text-kb-text-tertiary ml-[80px] mt-0.5">
            {formatPercent(requestedPercent)} of capacity
          </div>
        </div>

        {/* Limits bar */}
        <div>
          <div className="flex items-center gap-3">
            <span className="text-[10px] font-mono font-medium uppercase tracking-[0.04em] px-2 py-0.5 rounded bg-status-error-dim text-status-error border border-status-error/30 w-[68px] text-center shrink-0">
              Limits
            </span>
            <div className="flex-1 h-2 rounded bg-[var(--kb-bar-track)] overflow-hidden">
              <div
                className="h-full rounded transition-all duration-700"
                style={{
                  width: `${Math.min(100, usage?.allocatable ? ((usage?.limit ?? 0) / usage.allocatable) * 100 : 0)}%`,
                  background: '#ef4056',
                }}
              />
            </div>
            <span className="text-[11px] font-mono text-kb-text-secondary min-w-[70px] text-right">
              {formatFn(usage?.limit ?? 0)}
            </span>
          </div>
          <div className="text-[10px] font-mono text-kb-text-tertiary ml-[80px] mt-0.5">
            {formatPercent(usage?.allocatable ? ((usage?.limit ?? 0) / usage.allocatable) * 100 : 0)} of capacity
          </div>
        </div>

        {/* Used bar (only when metrics available) */}
        {hasUsageData && (
          <div>
            <div className="flex items-center gap-3">
              <span className="text-[10px] font-mono font-medium uppercase tracking-[0.04em] px-2 py-0.5 rounded bg-status-ok-dim text-status-ok border border-status-ok/30 w-[68px] text-center shrink-0">
                Used
              </span>
              <div className="flex-1 h-2 rounded bg-[var(--kb-bar-track)] overflow-hidden">
                <div
                  className="h-full rounded transition-all duration-700"
                  style={{ width: `${usedPercent}%`, background: getUsageBarColor(usedPercent) }}
                />
              </div>
              <span className="text-[11px] font-mono text-kb-text-secondary min-w-[70px] text-right">
                {formatFn(usage?.used ?? 0)}
              </span>
            </div>
            <div className="text-[10px] font-mono text-kb-text-tertiary ml-[80px] mt-0.5">
              {formatPercent(usedPercent)} of capacity
            </div>
          </div>
        )}
      </div>

      {/* No metrics warning */}
      {!metricsAvailable && !nodesRestricted && (
        <div className="flex items-center gap-2 mt-3 px-2 py-1.5 rounded bg-status-warn-dim/50 text-status-warn">
          <AlertTriangle className="w-3 h-3 shrink-0" />
          <span className="text-[10px] font-mono">Metrics Server not detected — usage data unavailable</span>
        </div>
      )}

      {/* No node access warning */}
      {nodesRestricted && (
        <div className="flex items-center gap-2 mt-3 px-2 py-1.5 rounded border border-kb-border bg-kb-elevated text-kb-text-secondary">
          <ShieldOff className="w-3 h-3 shrink-0 text-status-warn" />
          <span className="text-[10px] font-mono">No access to Nodes — capacity data unavailable</span>
        </div>
      )}

      {/* Available */}
      {!nodesRestricted && (
        <div className="text-[11px] font-mono text-kb-text-tertiary mt-2">
          Available: {formatFn(Math.max(0, (usage?.allocatable ?? 0) - (usage?.requested ?? 0)))}
        </div>
      )}
    </div>
  )
}

export function ResourceUsagePanel({ cpu, memory, metricsAvailable = true, nodesRestricted }: ResourceUsageProps) {
  return (
    <div className="grid grid-cols-2 gap-3">
      <UsageCard label="CPU Usage" icon={<Cpu className="w-4 h-4" />} usage={cpu ?? emptyUsage} formatFn={formatCPU} metricsAvailable={metricsAvailable} nodesRestricted={nodesRestricted} />
      <UsageCard label="Memory Usage" icon={<MemoryStick className="w-4 h-4" />} usage={memory ?? emptyUsage} formatFn={formatMemory} metricsAvailable={metricsAvailable} nodesRestricted={nodesRestricted} />
    </div>
  )
}
