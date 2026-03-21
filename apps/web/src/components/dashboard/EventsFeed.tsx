import type { KubeEvent } from '@/types/kubernetes'
import { formatAge } from '@/utils/formatters'

interface EventsFeedProps {
  events: KubeEvent[]
}

export function EventsFeed({ events }: EventsFeedProps) {
  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <div className="text-[10px] font-mono uppercase tracking-[0.08em] text-[#555770] mb-3">
        Recent Events
      </div>
      <div className="space-y-2 max-h-[280px] overflow-y-auto">
        {events.length === 0 && (
          <div className="text-xs text-[#555770] text-center py-6">No recent events</div>
        )}
        {events.map((event, i) => (
          <div key={`${event.object}-${event.reason}-${event.timestamp}`} className="flex items-start gap-2 py-1.5">
            <span
              className={`shrink-0 mt-0.5 px-1.5 py-0.5 rounded text-[9px] font-mono uppercase tracking-[0.06em] ${
                event.type === 'Warning'
                  ? 'bg-status-warn-dim text-status-warn'
                  : 'bg-status-ok-dim text-status-ok'
              }`}
            >
              {event.type}
            </span>
            <div className="flex-1 min-w-0">
              <div className="text-[11px] text-[#e8e9ed] truncate">{event.message}</div>
              <div className="text-[10px] font-mono text-[#555770] mt-0.5">{event.object}</div>
            </div>
            <span className="text-[10px] font-mono text-[#555770] shrink-0">
              {event.timestamp ? formatAge(event.timestamp) : '-'}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}
