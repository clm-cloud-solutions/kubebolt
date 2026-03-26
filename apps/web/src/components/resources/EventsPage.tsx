import { useState } from 'react'
import { useEvents } from '@/hooks/useEvents'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { formatAge } from '@/utils/formatters'
import type { EventParams } from '@/types/kubernetes'

type EventFilter = 'all' | 'Warning' | 'Normal'

export function EventsPage() {
  const [filter, setFilter] = useState<EventFilter>('all')

  const params: EventParams = {}
  if (filter !== 'all') params.type = filter

  const { data, isLoading, error, refetch, dataUpdatedAt, isFetching } = useEvents(params)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const items = data?.items || []

  const filters: EventFilter[] = ['all', 'Warning', 'Normal']

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <div className="flex items-center gap-3">
          <h1 className="text-lg font-semibold text-kb-text-primary">Events</h1>
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
        <div className="flex gap-1">
          {filters.map((f) => (
            <button
              key={f}
              onClick={() => setFilter(f)}
              className={`px-2.5 py-1 rounded-md text-[10px] font-mono uppercase tracking-[0.06em] border transition-colors ${
                filter === f
                  ? f === 'Warning'
                    ? 'bg-status-warn-dim text-status-warn border-status-warn/20'
                    : f === 'Normal'
                      ? 'bg-status-ok-dim text-status-ok border-status-ok/20'
                      : 'bg-status-info-dim text-status-info border-status-info/20'
                  : 'bg-kb-card text-kb-text-secondary border-kb-border hover:border-kb-border-active'
              }`}
            >
              {f}
            </button>
          ))}
        </div>
      </div>

      <div className="bg-kb-card border border-kb-border rounded-[10px] divide-y divide-kb-border">
        {items.length === 0 && (
          <div className="py-12 text-center text-xs text-kb-text-tertiary font-mono">No events found</div>
        )}
        {items.map((item, i) => {
          const eventType = (item.type as string) || 'Normal'
          return (
            <div key={`${item.namespace}-${item.name}-${item.reason}-${item.createdAt}`} className="flex items-start gap-3 px-4 py-3 hover:bg-kb-card-hover transition-colors">
              <span
                className={`shrink-0 mt-0.5 px-1.5 py-0.5 rounded text-[9px] font-mono uppercase tracking-[0.06em] ${
                  eventType === 'Warning'
                    ? 'bg-status-warn-dim text-status-warn'
                    : 'bg-status-ok-dim text-status-ok'
                }`}
              >
                {eventType}
              </span>
              <div className="flex-1 min-w-0">
                <div className="text-xs text-kb-text-primary">{(item.message as string) || item.name}</div>
                <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">
                  {(item.object as string) || `${item.namespace}/${item.name}`}
                  {item.reason != null ? <span className="ml-2 text-kb-text-secondary">{String(item.reason)}</span> : null}
                </div>
              </div>
              <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
                {item.createdAt ? formatAge(item.createdAt) : item.age || '-'}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
