import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ChevronDown, History } from 'lucide-react'
import type { KubeEvent } from '@/types/kubernetes'
import { api } from '@/services/api'
import { formatAge } from '@/utils/formatters'
import { groupEvents, type EventGroup } from '@/utils/eventGroups'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'

interface EventsFeedProps {
  events: KubeEvent[]
}

// EventsFeed — the Overview's Recent Events, folded by the object they are
// about (utils/eventGroups). An object with one event renders the plain row;
// one with several renders a single row that names the problem, with its raw
// events behind "N related events" — kept, because a diagnosis reads them.
// A Deployment's rollout is its own kind of row, and says so when a pod of the
// new ReplicaSet started failing right after it.
export function EventsFeed({ events }: EventsFeedProps) {
  // The ReplicaSets created in the last hour tell a rollout from a scale-up
  // (utils/eventGroups rolloutOf). An hour: what Kubernetes keeps of events.
  const deploysQ = useQuery({
    queryKey: ['deploys', 60],
    queryFn: () => api.getDeploys({ windowMinutes: 60 }),
    refetchInterval: 30_000,
    staleTime: 15_000,
  })
  const groups = groupEvents(events, deploysQ.data ?? [])
  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <div className="flex items-center gap-2 mb-3">
        <span className="text-kb-text-secondary shrink-0">
          <History className="w-4 h-4" />
        </span>
        <h4 className="text-sm font-semibold text-kb-text-primary">Recent Events</h4>
      </div>
      <div className="space-y-2 max-h-[280px] overflow-y-auto">
        {groups.length === 0 && (
          <div className="text-xs text-kb-text-tertiary text-center py-6">No recent events</div>
        )}
        {groups.map((g) => (
          <EventRow key={g.key} group={g} />
        ))}
      </div>
    </div>
  )
}

function EventRow({ group }: { group: EventGroup }) {
  const [open, setOpen] = useState(false)
  const isWarning = group.type === 'Warning'
  const rollout = isWarning ? null : group.rollout
  const lead = group.lead
  const related = group.events.length
  const deployment = group.object.split('/')[1]
  return (
    <div className="flex items-start gap-2 py-1.5">
      <span
        className={`shrink-0 mt-0.5 px-1.5 py-0.5 rounded text-[9px] font-mono uppercase tracking-[0.06em] ${
          isWarning
            ? 'bg-status-warn-dim text-status-warn'
            : rollout
              ? 'bg-status-info-dim text-status-info'
              : 'bg-status-ok-dim text-status-ok'
        }`}
      >
        {rollout ? 'Rollout' : group.type}
      </span>
      <div className="flex-1 min-w-0">
        <div className="text-[12px] text-kb-text-primary truncate" title={lead.message}>
          {rollout ? (
            <>
              <span className="font-semibold">Rollout</span>
              <span className="text-kb-text-secondary"> — new replica set {rollout.replicaSet}</span>
            </>
          ) : group.summary ? (
            <>
              <span className="font-semibold">{group.summary}</span>
              <span className="text-kb-text-secondary"> — {lead.message}</span>
            </>
          ) : (
            lead.message
          )}
        </div>
        <div className="flex items-center gap-1.5 text-[11px] font-mono text-kb-text-tertiary mt-0.5 min-w-0">
          {rollout ? (
            <Link
              to={`/deployments/${group.namespace}/${deployment}?tab=history`}
              className="truncate hover:text-kb-text-primary transition-colors"
              title={`${group.namespace}/${group.object} — revision history`}
            >
              {group.object}
            </Link>
          ) : (
            <span className="truncate" title={`${group.namespace}/${group.object}`}>
              {group.object}
            </span>
          )}
          {rollout?.then && (
            <>
              <span className="shrink-0">·</span>
              <span className="shrink-0 text-status-warn">
                then {rollout.then.summary ? rollout.then.summary.toLowerCase() : 'its pods failing'}{' '}
                {afterLabel(rollout.then.afterS)} later
              </span>
            </>
          )}
          {related > 1 && (
            <>
              <span className="shrink-0">·</span>
              <button
                type="button"
                onClick={() => setOpen((v) => !v)}
                aria-expanded={open}
                className="shrink-0 inline-flex items-center gap-0.5 hover:text-kb-text-primary transition-colors"
              >
                {related} related events
                <ChevronDown className={`w-3 h-3 transition-transform ${open ? 'rotate-180' : ''}`} />
              </button>
            </>
          )}
        </div>
        {open && (
          <div className="mt-1.5 pl-2.5 border-l border-kb-border space-y-1">
            {group.events.map((e) => (
              <div key={`${e.reason}-${e.timestamp}-${e.message}`} className="flex items-start gap-2 text-[11px] min-w-0">
                <span className={`font-mono shrink-0 ${e.type === 'Warning' ? 'text-status-warn' : 'text-kb-text-tertiary'}`}>
                  {e.reason}
                </span>
                <span className="flex-1 min-w-0 truncate text-kb-text-secondary" title={e.message}>
                  {e.message}
                </span>
                {e.count > 1 && <span className="font-mono text-kb-text-tertiary shrink-0">×{e.count}</span>}
                <span className="font-mono text-kb-text-tertiary shrink-0">{e.timestamp ? formatAge(e.timestamp) : '-'}</span>
              </div>
            ))}
          </div>
        )}
      </div>
      {/* Ask Kobi for a group with a Warning, about its lead event. Kubernetes
          only emits "Normal" and "Warning" event types, so the gate is just
          isWarning; the button hides itself when Kobi isn't configured. */}
      {isWarning && (
        <AskCopilotButton
          variant="icon"
          payload={{
            type: 'warning_event',
            event: {
              reason: lead.reason,
              message: lead.message,
              object: lead.object,
              namespace: lead.namespace || undefined,
              count: lead.count,
              lastSeen: lead.timestamp,
            },
          }}
          label={`Ask Kobi about ${group.summary ?? lead.reason}`}
          className="shrink-0 mt-0.5"
        />
      )}
      <span className="text-[11px] font-mono text-kb-text-tertiary shrink-0 mt-0.5">
        {group.events[0].timestamp ? formatAge(group.events[0].timestamp) : '-'}
      </span>
    </div>
  )
}

const afterLabel = (s: number) => (s < 60 ? `${s}s` : `${Math.round(s / 60)}m`)
