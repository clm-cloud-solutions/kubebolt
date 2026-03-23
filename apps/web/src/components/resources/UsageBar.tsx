import { getUsageBarColor } from '@/utils/colors'

interface UsageBarProps {
  percent: number
  height?: number
  showLabel?: boolean
  className?: string
}

export function UsageBar({ percent, height = 4, showLabel = false, className = '' }: UsageBarProps) {
  const clamped = Math.max(0, Math.min(100, percent))
  const color = getUsageBarColor(clamped)

  return (
    <div className={`flex items-center gap-2 ${className}`}>
      <div className="flex-1 rounded-full overflow-hidden" style={{ height, background: 'var(--kb-bar-track)' }}>
        <div
          className="h-full rounded-full transition-all duration-500"
          style={{ width: `${clamped}%`, background: color }}
        />
      </div>
      {showLabel && (
        <span className="text-[10px] font-mono text-kb-text-secondary w-8 text-right">{Math.round(clamped)}%</span>
      )}
    </div>
  )
}
