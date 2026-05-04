import { useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { getUsageBarColor } from '@/utils/colors'
import { formatCPU, formatMemory } from '@/utils/formatters'
import { TooltipPanel, TooltipRow } from './Tooltip'

interface ResourceUsageCellProps {
  usage: number
  request: number
  limit: number
  percent: number
  type: 'cpu' | 'memory'
  size?: 'sm' | 'lg'
}

export function ResourceUsageCell({ usage, request, limit, percent, type, size = 'sm' }: ResourceUsageCellProps) {
  const formatFn = type === 'cpu' ? formatCPU : formatMemory
  const cellRef = useRef<HTMLDivElement>(null)
  const [tooltip, setTooltip] = useState<{ x: number; y: number; above: boolean } | null>(null)

  if (usage === 0 && request === 0 && limit === 0) {
    return <span className="text-kb-text-tertiary text-[11px] font-mono">—</span>
  }

  const denom = limit || request
  const usagePercent = denom > 0 ? (usage / denom) * 100 : percent
  const requestPercent = limit > 0 && request > 0 ? (request / limit) * 100 : 0
  const limitPercent = denom > 0 && limit > 0 ? 100 : 0

  function handleMouseEnter() {
    if (cellRef.current) {
      const rect = cellRef.current.getBoundingClientRect()
      const above = rect.top > 120
      setTooltip({
        x: rect.left,
        y: above ? rect.top - 6 : rect.bottom + 6,
        above,
      })
    }
  }

  function handleMouseLeave() {
    setTooltip(null)
  }

  const hasTooltipContent = usage > 0 || request > 0 || limit > 0

  return (
    <div
      ref={cellRef}
      className="relative flex items-center gap-1.5"
      onMouseEnter={handleMouseEnter}
      onMouseLeave={handleMouseLeave}
    >
      <div className={size === 'lg' ? 'flex-1' : 'w-16'}>
        <div className={`relative rounded-full overflow-hidden ${size === 'lg' ? 'h-[7px]' : 'h-[5px]'}`} style={{ background: 'var(--kb-bar-track)' }}>
          {requestPercent > 0 && requestPercent < 100 && (
            <div
              className="absolute top-0 bottom-0 w-[1px] bg-status-info/70 z-10"
              style={{ left: `${Math.min(requestPercent, 100)}%` }}
            />
          )}
          {limitPercent > 0 && usagePercent < 200 && (
            <div
              className="absolute top-[-1px] bottom-[-1px] w-[2px] rounded-full bg-status-error/70 z-10"
              style={{ left: `${Math.min(100, limitPercent)}%`, transform: 'translateX(-1px)' }}
            />
          )}
          <div
            className="h-full rounded-full transition-all duration-500"
            style={{ width: `${Math.min(usagePercent, 100)}%`, background: getUsageBarColor(usagePercent) }}
          />
        </div>
      </div>
      <span className="text-[11px] font-mono text-kb-text-secondary">
        {usage > 0 ? formatFn(usage) : formatFn(request)}
      </span>

      {/* Tooltip via portal to escape overflow:hidden parents.
          Visual matches the Cluster Map and MetricChart tooltips —
          shared TooltipPanel + TooltipRow so the whole dashboard
          reads as one design instead of three near-identical
          inlined copies. */}
      {tooltip && hasTooltipContent && createPortal(
        <div
          className="fixed z-[9999] pointer-events-none"
          style={{
            left: tooltip.x,
            top: tooltip.above ? undefined : tooltip.y,
            bottom: tooltip.above ? `calc(100vh - ${tooltip.y}px)` : undefined,
          }}
        >
          <TooltipPanel className="whitespace-nowrap">
            <div className="space-y-1">
              {usage > 0 && (
                <TooltipRow
                  color={getUsageBarColor(usagePercent)}
                  label="Used"
                  value={
                    denom > 0
                      ? `${formatFn(usage)} (${Math.round(usagePercent)}%)`
                      : formatFn(usage)
                  }
                />
              )}
              {request > 0 && (
                <TooltipRow color="#4c9aff" label="Request" value={formatFn(request)} />
              )}
              {limit > 0 && (
                <TooltipRow color="#ef4056" label="Limit" value={formatFn(limit)} />
              )}
            </div>
          </TooltipPanel>
        </div>,
        document.body
      )}
    </div>
  )
}
