import { Cpu, MemoryStick, AlertTriangle, ShieldOff } from 'lucide-react'
import type { ResourceUsage as ResourceUsageType } from '@/types/kubernetes'
import { formatPercent, formatCPU, formatMemory } from '@/utils/formatters'
import { getUsageBarColor } from '@/utils/colors'
import { DonutGauge } from '@/components/shared/DonutGauge'
import { LegendRow } from '@/components/shared/LegendRow'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'

const emptyUsage: ResourceUsageType = { used: 0, requested: 0, limit: 0, allocatable: 0, percentUsed: 0, percentRequested: 0 }

interface ResourceUsageProps {
  cpu?: ResourceUsageType
  memory?: ResourceUsageType
  metricsAvailable?: boolean
  nodesRestricted?: boolean
}

// UsageCard answers the panel's two questions with two distinct
// visuals instead of three stacked bars:
//   - "¿cuánto consumo ahora?" → DonutGauge with Used % of capacity
//     (graded color, same thresholds as the resource-list cells).
//   - "¿cómo está dimensionado?" → one bullet bar on the capacity
//     axis: Requests as the fill, Limits as a marker tick — the same
//     bar-with-markers grammar ResourceUsageCell already taught the
//     user in list views.
// Limits can legitimately exceed capacity (overcommit); a gauge
// saturates there but the bullet bar clamps the marker at 100% and
// the legend flags "over capacity", so the signal survives.
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
  const allocatable = usage?.allocatable ?? 0
  const requested = usage?.requested ?? 0
  const limit = usage?.limit ?? 0
  const requestedPercent = Math.min(100, usage?.percentRequested ?? 0)
  const limitPercent = allocatable > 0 ? (limit / allocatable) * 100 : 0
  const overCommitted = allocatable > 0 && limit > allocatable
  const hasUsageData = metricsAvailable && (usage?.used ?? 0) > 0
  const usedPercent = hasUsageData ? Math.min(100, usage?.percentUsed ?? 0) : 0

  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <div className="flex items-center gap-2 mb-1">
        <div className="text-kb-text-secondary">{icon}</div>
        <span className="text-sm font-semibold text-kb-text-primary">{label}</span>
      </div>
      <div className="text-[11px] font-mono text-kb-text-tertiary mb-3">
        Capacity: {nodesRestricted ? 'N/A' : formatFn(allocatable)}
      </div>

      {/* Tooltip anchors on the gauge+bar block (not the whole card)
          so it only appears when the cursor is over the data, not
          while crossing the card on the way elsewhere. Same rows and
          colors as the ResourceUsageCell tooltip in list views, plus
          the capacity-axis context (Available / Capacity). */}
      <HoverTooltip
        body={
          <>
            <TooltipHeader right={hasUsageData ? `${Math.round(usedPercent)}% used` : undefined}>
              {label}
            </TooltipHeader>
            <div className="space-y-1">
              {hasUsageData && (
                <TooltipRow
                  color={getUsageBarColor(usedPercent)}
                  label="Used"
                  value={`${formatFn(usage?.used ?? 0)} (${formatPercent(usedPercent)})`}
                />
              )}
              {requested > 0 && (
                <TooltipRow
                  color="#4c9aff"
                  label="Requests"
                  value={`${formatFn(requested)} (${formatPercent(requestedPercent)})`}
                />
              )}
              {limit > 0 && (
                <TooltipRow
                  color="#ef4056"
                  label="Limits"
                  value={`${formatFn(limit)} (${formatPercent(limitPercent)})`}
                />
              )}
              {!nodesRestricted && (
                <>
                  <TooltipRow
                    color={null}
                    label="Available"
                    value={formatFn(Math.max(0, allocatable - requested))}
                  />
                  <TooltipRow color={null} label="Capacity" value={formatFn(allocatable)} />
                </>
              )}
            </div>
          </>
        }
      >
      <div className="flex items-center gap-8 py-2">
        {/* Used gauge — instantaneous consumption against capacity.
            Sized to anchor the card on wide screens; at 116px the
            graded color is readable from across the room, which is
            the whole point of a gauge on a status dashboard. */}
        <div className="flex flex-col items-center gap-2 shrink-0">
          <DonutGauge
            percent={usedPercent}
            color={getUsageBarColor(usedPercent)}
            size={116}
            strokeWidth={10}
          >
            {hasUsageData ? (
              <>
                <span className="text-2xl font-semibold text-kb-text-primary tabular-nums leading-none">
                  {Math.round(usedPercent)}%
                </span>
                <span className="text-[10px] font-mono text-kb-text-tertiary mt-1">used</span>
              </>
            ) : (
              <span className="text-base font-mono text-kb-text-tertiary">—</span>
            )}
          </DonutGauge>
          <span className="text-xs font-mono text-kb-text-secondary">
            {hasUsageData ? formatFn(usage?.used ?? 0) : 'no data'}
          </span>
        </div>

        {/* Bullet bar — Requests fill + Limits marker on the capacity axis */}
        <div className="flex-1 min-w-0">
          <div className="relative">
            <div className="h-3 rounded-full overflow-hidden" style={{ background: 'var(--kb-bar-track)' }}>
              <div
                className="h-full rounded-full transition-all duration-700"
                style={{ width: `${requestedPercent}%`, background: '#4c9aff' }}
              />
            </div>
            {limitPercent > 0 && (
              <div
                className="absolute top-[-3px] bottom-[-3px] w-[2px] rounded-full bg-status-error"
                style={{ left: `${Math.min(100, limitPercent)}%`, transform: 'translateX(-1px)' }}
              />
            )}
          </div>

          <div className="mt-3 space-y-1.5">
            <LegendRow
              color="#4c9aff"
              label="Requests"
              value={`${formatFn(requested)} · ${formatPercent(requestedPercent)} of capacity`}
            />
            <LegendRow
              color="#ef4056"
              label="Limits"
              value={`${formatFn(limit)} · ${formatPercent(limitPercent)} of capacity`}
              warn={overCommitted ? 'over capacity' : undefined}
            />
          </div>

          {!nodesRestricted && (
            <div className="text-xs font-mono text-kb-text-tertiary mt-2.5">
              Available: {formatFn(Math.max(0, allocatable - requested))}
            </div>
          )}
        </div>
      </div>
      </HoverTooltip>

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
