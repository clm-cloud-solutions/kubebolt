import { useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { getUsageBarColor } from '@/utils/colors'
import { formatCPU, formatMemory } from '@/utils/formatters'

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

      {/* Tooltip rendered via portal to escape overflow:hidden containers */}
      {tooltip && hasTooltipContent && createPortal(
        <div
          className="fixed z-[9999] pointer-events-none"
          style={{
            left: tooltip.x,
            top: tooltip.above ? undefined : tooltip.y,
            bottom: tooltip.above ? `calc(100vh - ${tooltip.y}px)` : undefined,
          }}
        >
          <div className="bg-kb-card border border-kb-border rounded-md shadow-lg px-3 py-2 whitespace-nowrap">
            <div className="space-y-1 text-[10px] font-mono">
              {usage > 0 && (
                <div className="flex items-center gap-2">
                  <span className="w-1.5 h-1.5 rounded-full" style={{ background: getUsageBarColor(usagePercent) }} />
                  <span className="text-kb-text-tertiary w-12">Used</span>
                  <span className="text-kb-text-primary">{formatFn(usage)}</span>
                  {denom > 0 && <span className="text-kb-text-tertiary">({Math.round(usagePercent)}%)</span>}
                </div>
              )}
              {request > 0 && (
                <div className="flex items-center gap-2">
                  <span className="w-1.5 h-1.5 rounded-full bg-status-info" />
                  <span className="text-kb-text-tertiary w-12">Request</span>
                  <span className="text-kb-text-primary">{formatFn(request)}</span>
                </div>
              )}
              {limit > 0 && (
                <div className="flex items-center gap-2">
                  <span className="w-1.5 h-1.5 rounded-full bg-status-error" />
                  <span className="text-kb-text-tertiary w-12">Limit</span>
                  <span className="text-kb-text-primary">{formatFn(limit)}</span>
                </div>
              )}
            </div>
          </div>
        </div>,
        document.body
      )}
    </div>
  )
}
