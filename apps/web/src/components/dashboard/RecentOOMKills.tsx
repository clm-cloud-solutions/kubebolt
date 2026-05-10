import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { AlertTriangle } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { formatAge } from '@/utils/formatters'

// RecentOOMKills surfaces the cluster-wide OOMKill heat map.
//
// Data source:
//   kube_pod_container_status_last_terminated_reason{reason="OOMKilled"}
//   == 1   → flag for "the previous run of this container was OOM"
//   kube_pod_container_status_last_terminated_timestamp
//          → the unix timestamp of that previous run
// Joining the two by (uid, namespace, pod, container) gives a list
// of containers currently reporting OOMKilled as their last
// termination, each tagged with WHEN that happened. We sort by
// timestamp desc and slice to the top 10 — recent first since "what
// just happened" is more actionable than "what's been broken
// forever".
//
// Why current-state and not a 7d window: the gauge `last_terminated_
// reason` only ever holds the LATEST reason, so a container that
// OOMed 3 days ago and then crashed for a different reason wouldn't
// be visible here even with `max_over_time(...)`. Showing
// "currently in OOM-recovered state" is the cleanest reading of
// what KSM emits — the Insights engine's oomKilled rule (informer-
// state) covers any-time-recent detection in parallel.
//
// Gating: an empty result reads "No OOMKills detected" rather than
// hiding the panel — operators want to know "are we OOM-free right
// now" as much as "who's OOMing", and an absent panel doesn't
// answer the first question.

interface OOMRow {
  namespace: string
  pod: string
  container: string
  timestamp: number  // unix seconds
}

interface Props {
  installed: boolean
  topN?: number
}

const QUERY = [
  'kube_pod_container_status_last_terminated_timestamp',
  '* on(uid, namespace, pod, container)',
  '(kube_pod_container_status_last_terminated_reason{reason="OOMKilled"} == 1)',
].join(' ')

export function RecentOOMKills({ installed, topN = 10 }: Props) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['recent-oom-kills'],
    queryFn: () => api.queryMetrics({ query: QUERY }),
    refetchInterval: 30_000,
    enabled: installed,
    retry: false,
  })

  if (!installed) return null

  const rows: OOMRow[] = (data?.data?.result ?? [])
    .map((s) => ({
      namespace: s.metric.namespace ?? '',
      pod: s.metric.pod ?? '',
      container: s.metric.container ?? '',
      timestamp: parseFloat(s.value?.[1] ?? '0'),
    }))
    .filter((r) => r.namespace && r.pod && r.timestamp > 0)
    .sort((a, b) => b.timestamp - a.timestamp)
    .slice(0, topN)

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-status-error shrink-0">
            <AlertTriangle className="w-4 h-4" />
          </span>
          <h4 className="text-sm font-semibold text-kb-text-primary truncate">
            Recent OOMKills
          </h4>
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
          last termination · top {topN}
        </span>
      </div>

      {isLoading && (
        <div className="py-6">
          <LoadingSpinner size="sm" />
        </div>
      )}

      {error && !isLoading && (
        <div className="text-[11px] text-status-warn font-mono py-3">
          Query failed — kube-state-metrics may not be scraping.
        </div>
      )}

      {!isLoading && !error && rows.length === 0 && (
        <div className="text-[11px] text-kb-text-tertiary py-3">
          No OOMKills detected — every container's last termination was something else.
        </div>
      )}

      {!isLoading && !error && rows.length > 0 && (
        <ul className="space-y-1.5">
          {rows.map((r, i) => {
            const finishedAt = new Date(r.timestamp * 1000).toISOString()
            const ago = formatAge(finishedAt)
            return (
              <li
                key={`${r.namespace}/${r.pod}/${r.container}`}
                className="flex items-center gap-3 px-2 py-1.5 rounded transition-colors hover:bg-kb-card-hover"
              >
                <span className="text-[10px] font-mono text-kb-text-tertiary w-4 text-right tabular-nums">
                  {i + 1}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-1.5 truncate">
                    <Link
                      to={`/pods/${encodeURIComponent(r.namespace)}/${encodeURIComponent(r.pod)}`}
                      className="text-xs text-kb-text-primary truncate hover:underline"
                    >
                      {r.pod}
                    </Link>
                    <span className="text-[10px] font-mono text-kb-text-tertiary truncate">
                      {r.namespace} · {r.container}
                    </span>
                  </div>
                </div>
                <span className="text-[10px] font-mono text-kb-text-tertiary tabular-nums shrink-0">
                  {ago} ago
                </span>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
