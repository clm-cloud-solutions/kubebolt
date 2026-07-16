import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { AlertTriangle } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { formatAge, formatMemory } from '@/utils/formatters'

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
// Table columns beyond the identity: the container's MEMORY LIMIT
// (the ceiling it hit — the first thing an operator asks) and its
// RESTART count (chronic OOM loop vs one-off spike). Both come from
// KSM series we already ingest, joined client-side by
// namespace/pod/container.
//
// Why current-state and not a 7d window: the gauge `last_terminated_
// reason` only ever holds the LATEST reason, so a container that
// OOMed 3 days ago and then crashed for a different reason wouldn't
// be visible here even with `max_over_time(...)`. Showing
// "currently in OOM-recovered state" is the cleanest reading of
// what KSM emits — the Insights engine's oomKilled rule (informer-
// state) covers any-time-recent detection in parallel. Same reason
// a freshly REPLACED pod disappears from here: the series belongs
// to the live pod, not to history.
//
// Gating: an empty result reads "No OOMKills detected" rather than
// hiding the panel — operators want to know "are we OOM-free right
// now" as much as "who's OOMing", and an absent panel doesn't
// answer the first question.

interface OOMRow {
  namespace: string
  pod: string
  container: string
  timestamp: number // unix seconds
  memLimit?: number // bytes
  restarts?: number
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

const LIMITS_QUERY = 'kube_pod_container_resource_limits{resource="memory"}'
const RESTARTS_QUERY = 'kube_pod_container_status_restarts_total'

export function RecentOOMKills({ installed, topN = 10 }: Props) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['recent-oom-kills'],
    queryFn: () => api.queryMetrics({ query: QUERY }),
    refetchInterval: 30_000,
    enabled: installed,
    retry: false,
  })

  const hasHits = (data?.data?.result?.length ?? 0) > 0
  // Enrichment (limits + restarts) fetches only when there's
  // something to enrich — the common no-OOM case costs one query,
  // not three.
  const detailQ = useQuery({
    queryKey: ['recent-oom-kills', 'detail'],
    queryFn: async () => {
      const [limits, restarts] = await Promise.all([
        api.queryMetrics({ query: LIMITS_QUERY }),
        api.queryMetrics({ query: RESTARTS_QUERY }),
      ])
      return { limits, restarts }
    },
    refetchInterval: 60_000,
    enabled: installed && hasHits,
    retry: false,
  })

  if (!installed) return null

  const limitIdx = buildContainerIndex(detailQ.data?.limits?.data?.result)
  const restartIdx = buildContainerIndex(detailQ.data?.restarts?.data?.result)

  const rows: OOMRow[] = (data?.data?.result ?? [])
    .map((s) => {
      const key = containerKey(s.metric)
      return {
        namespace: s.metric.namespace ?? '',
        pod: s.metric.pod ?? '',
        container: s.metric.container ?? '',
        timestamp: parseFloat(s.value?.[1] ?? '0'),
        memLimit: limitIdx.get(key),
        restarts: restartIdx.get(key),
      }
    })
    .filter((r) => r.namespace && r.pod && r.timestamp > 0)
    .sort((a, b) => b.timestamp - a.timestamp)
    .slice(0, topN)

  // Kobi row blobs — mirror the visible columns (limit, restarts,
  // ago) so the LLM reasons over exactly what the user is looking at.
  const kobiRows = rows.map(buildRowBlob)

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
          {rows.length > 0 && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'recent_oomkills',
                rangeLabel: 'current last-termination state',
                rows: kobiRows,
              }}
              variant="icon"
              label="Ask Kobi about these OOMKills"
            />
          )}
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
        <div className="overflow-x-auto">
          <table className="w-full text-[11px]">
            <thead>
              <tr className="text-left text-[10px] font-mono font-semibold uppercase tracking-[0.07em] text-kb-text-secondary">
                <th className="pb-2 pr-3">Pod</th>
                <th className="pb-2 pr-3">Container</th>
                <th className="pb-2 pr-3">Namespace</th>
                <th className="pb-2 pr-3 text-right">Mem limit</th>
                <th className="pb-2 pr-3 text-right">Restarts</th>
                <th className="pb-2 pr-4 text-right">When</th>
                {/* Reserved fixed-width Kobi slot — same rationale as
                    DeploysList: the button mounts async, and reserving
                    the lane keeps the table width stable (no
                    horizontal-scrollbar flash). Wider than Deploys'
                    (w-12) because this table is right-heavy: When is
                    its last data column and the button needs breathing
                    room beside the timestamp. */}
                <th className="pb-2 w-12 min-w-[48px]" aria-label="Ask Kobi" />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const finishedAt = new Date(r.timestamp * 1000).toISOString()
                return (
                  <tr
                    key={`${r.namespace}/${r.pod}/${r.container}`}
                    className="group border-t border-kb-border transition-colors hover:bg-kb-card-hover"
                  >
                    <td className="py-1.5 pr-3 max-w-[200px]">
                      <Link
                        to={`/pods/${encodeURIComponent(r.namespace)}/${encodeURIComponent(r.pod)}`}
                        className="text-kb-text-primary font-medium truncate block hover:text-kb-accent transition-colors"
                      >
                        {r.pod}
                      </Link>
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-kb-text-secondary truncate max-w-[140px]">
                      {r.container}
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-kb-text-tertiary truncate max-w-[120px]">
                      {r.namespace}
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-status-error text-right tabular-nums whitespace-nowrap">
                      {r.memLimit != null ? formatMemory(r.memLimit) : '—'}
                    </td>
                    <td className="py-1.5 pr-3 font-mono text-right tabular-nums">
                      <span
                        className={
                          (r.restarts ?? 0) >= 5 ? 'text-status-warn' : 'text-kb-text-secondary'
                        }
                      >
                        {r.restarts != null ? r.restarts : '—'}
                      </span>
                    </td>
                    <td className="py-1.5 pr-4 font-mono text-kb-text-tertiary text-right tabular-nums whitespace-nowrap">
                      {formatAge(finishedAt)} ago
                    </td>
                    <td className="py-1.5 w-12 min-w-[48px] text-center">
                      <AskCopilotButton
                        payload={{
                          type: 'panel_inquiry',
                          panel: 'recent_oomkills',
                          rangeLabel: 'current last-termination state',
                          rows: [buildRowBlob(r)],
                        }}
                        variant="icon"
                        label="Ask Kobi about this OOMKill"
                        className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity"
                      />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// Kobi blob for one OOM row — shared by the panel-level and per-row
// buttons so the two prompt shapes can't drift.
function buildRowBlob(r: OOMRow): Record<string, string | number> {
  const blob: Record<string, string | number> = {
    pod: `${r.namespace}/${r.pod}`,
    container: r.container,
    oom_killed_ago: formatAge(new Date(r.timestamp * 1000).toISOString()),
  }
  if (r.memLimit != null) blob.mem_limit_bytes = r.memLimit
  if (r.restarts != null) blob.restarts = r.restarts
  return blob
}

function containerKey(metric: Record<string, string>): string {
  return `${metric.namespace}/${metric.pod}/${metric.container}`
}

// Build a container→value lookup from an instant vector.
function buildContainerIndex(
  result: Array<{ metric: Record<string, string>; value: [number, string] }> | undefined,
): Map<string, number> {
  const map = new Map<string, number>()
  if (!result) return map
  for (const s of result) {
    if (!s.metric.namespace || !s.metric.pod || !s.metric.container) continue
    const v = parseFloat(s.value?.[1] ?? '')
    if (!Number.isFinite(v)) continue
    map.set(containerKey(s.metric), v)
  }
  return map
}
