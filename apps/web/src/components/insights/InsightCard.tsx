import { AlertTriangle, AlertCircle, Info } from 'lucide-react'
import type { Insight } from '@/types/kubernetes'
import { formatAge } from '@/utils/formatters'

interface InsightCardProps {
  insight: Insight
}

const severityConfig = {
  critical: {
    icon: <AlertCircle className="w-4 h-4" />,
    bg: 'bg-status-error-dim',
    text: 'text-status-error',
    border: 'border-status-error/20',
  },
  warning: {
    icon: <AlertTriangle className="w-4 h-4" />,
    bg: 'bg-status-warn-dim',
    text: 'text-status-warn',
    border: 'border-status-warn/20',
  },
  info: {
    icon: <Info className="w-4 h-4" />,
    bg: 'bg-status-info-dim',
    text: 'text-status-info',
    border: 'border-status-info/20',
  },
}

export function InsightCard({ insight }: InsightCardProps) {
  const config = severityConfig[insight.severity]

  return (
    <div className={`bg-kb-card border ${config.border} rounded-[10px] p-4`}>
      <div className="flex items-start gap-3">
        <div className={`shrink-0 mt-0.5 p-1.5 rounded-lg ${config.bg} ${config.text}`}>
          {config.icon}
        </div>
        <div className="flex-1 min-w-0">
          <div className="flex items-center justify-between gap-2 mb-1">
            <h3 className="text-sm font-medium text-[#e8e9ed] truncate">{insight.title}</h3>
            <span className={`shrink-0 px-2 py-0.5 rounded-full text-[9px] font-mono uppercase tracking-[0.06em] ${config.bg} ${config.text}`}>
              {insight.severity}
            </span>
          </div>
          <p className="text-xs text-[#8b8d9a] mb-2">{insight.message}</p>
          {insight.suggestion && (
            <div className="bg-kb-bg rounded-md px-3 py-2 mb-2">
              <span className="text-[10px] font-mono text-[#555770] uppercase tracking-[0.06em]">Suggestion: </span>
              <span className="text-[11px] text-[#8b8d9a]">{insight.suggestion}</span>
            </div>
          )}
          <div className="flex items-center gap-3 text-[10px] font-mono text-[#555770]">
            <span>{insight.resource}</span>
            {insight.namespace && <span>{insight.namespace}</span>}
            <span>{formatAge(insight.lastSeen)}</span>
          </div>
        </div>
      </div>
    </div>
  )
}
