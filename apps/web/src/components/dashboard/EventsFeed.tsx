import type { KubeEvent } from '@/types/kubernetes'
import { formatAge } from '@/utils/formatters'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'

interface EventsFeedProps {
  events: KubeEvent[]
}

export function EventsFeed({ events }: EventsFeedProps) {
  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <div className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary mb-3">
        Recent Events
      </div>
      <div className="space-y-2 max-h-[280px] overflow-y-auto">
        {events.length === 0 && (
          <div className="text-xs text-kb-text-tertiary text-center py-6">No recent events</div>
        )}
        {events.map((event) => {
          const isWarning = event.type === 'Warning'
          return (
            <div
              key={`${event.object}-${event.reason}-${event.timestamp}`}
              className="flex items-start gap-2 py-1.5"
            >
              <span
                className={`shrink-0 mt-0.5 px-1.5 py-0.5 rounded text-[9px] font-mono uppercase tracking-[0.06em] ${
                  isWarning
                    ? 'bg-status-warn-dim text-status-warn'
                    : 'bg-status-ok-dim text-status-ok'
                }`}
              >
                {event.type}
              </span>
              <div className="flex-1 min-w-0">
                <div className="text-[11px] text-kb-text-primary truncate">{event.message}</div>
                <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">{event.object}</div>
              </div>
              {/* Ask Kobi for non-Normal events. Kubernetes only emits
                  "Normal" and "Warning" event types (there's no
                  "Error" level), so the gate is just isWarning. The
                  button hides itself when Kobi isn't configured (see
                  AskCopilotButton's null-render path), so the layout
                  stays clean on OSS installs without an API key. */}
              {isWarning && (
                <AskCopilotButton
                  variant="icon"
                  payload={{
                    type: 'warning_event',
                    event: {
                      reason: event.reason,
                      message: event.message,
                      object: event.object,
                      namespace: event.namespace || undefined,
                      count: event.count,
                      lastSeen: event.timestamp,
                    },
                  }}
                  label={`Ask Kobi about ${event.reason}`}
                  className="shrink-0 mt-0.5"
                />
              )}
              <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0 mt-0.5">
                {event.timestamp ? formatAge(event.timestamp) : '-'}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
