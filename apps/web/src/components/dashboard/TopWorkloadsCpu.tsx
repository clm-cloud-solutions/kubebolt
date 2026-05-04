import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ResourceUsageCell } from '@/components/shared/ResourceUsageCell'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'
import { formatCPU } from '@/utils/formatters'
import type { ClusterOverview, WorkloadSummary } from '@/types/kubernetes'

// TopWorkloadsCpu shows the cluster-wide CPU heavy-hitters. Distinct
// from the per-node Top Consumers panel: this aggregates across the
// whole cluster, grouping container samples by their owning workload
// so a 12-replica Deployment shows up once with summed CPU rather
// than 12 rows.
//
// Two data sources: (a) live VM samples for the actual CPU usage and
// the topk ranking, (b) the existing `overview.namespaceWorkloads`
// payload for per-workload CPU requests/limits. We don't add a new
// API call — the overview already aggregates requests/limits per
// workload via informer caches, and using its values keeps usage and
// budget on the same x-axis (millicores) so ResourceUsageCell can
// draw the request/limit markers correctly.
//
// Backstory on the query shape: the agent's meta enricher only walks
// one level up the ownerRef chain (Pod → first controller). For pods
// owned by a Deployment, that lands on the intermediate ReplicaSet —
// `workload_kind=ReplicaSet, workload_name=myapp-7d8b9c5f4`. The UI
// treats Deployments / StatefulSets / DaemonSets as the user-facing
// workload kinds; ReplicaSets aren't a first-class navigable resource.
// We collapse them with `label_replace`: strip the pod-template-hash
// suffix (`-[a-z0-9]{6,12}`) to recover the Deployment name, then
// rename the kind. Series with names that don't fit the pattern (rare
// — custom controllers creating bare ReplicaSets) keep their original
// name but still get the kind rewrite; surfacing them in the list is
// fine, the link target degrades to plain text via WORKLOAD_TYPE_TO_PATH.
// `or` unions in the natively-named StatefulSets and DaemonSets, which
// the agent already emits with the correct labels.
//
// Long-term, the recursive resolution should live in the agent (one
// extra apiserver watch on ReplicaSets, or a kubelet enrichment pass).
// Until then, the PromQL transform keeps the UI honest with the data
// we already have in VM.

interface WorkloadRow {
  namespace: string
  kind: string
  name: string
  // Cores from VM (e.g. 0.05). Multiplied by 1000 when feeding the
  // ResourceUsageCell so it's on the same millicore scale as the
  // overview's request/limit values.
  cores: number
}

interface Props {
  installed: boolean
  overview?: ClusterOverview
  refetchMs?: number
  topN?: number
}

const WORKLOAD_TYPE_TO_PATH: Record<string, string> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
}

export function TopWorkloadsCpu({ installed, overview, refetchMs = 30_000, topN = 6 }: Props) {
  const enabled = installed
  const query = [
    `topk(${topN}, sum by (workload_kind, workload_name, pod_namespace) (`,
    `  label_replace(`,
    `    label_replace(`,
    `      container_cpu_usage_cores{workload_kind="ReplicaSet",workload_name!=""},`,
    `      "workload_name", "$1", "workload_name", "^(.+)-[a-z0-9]{6,12}$"`,
    `    ),`,
    `    "workload_kind", "Deployment", "workload_kind", "ReplicaSet"`,
    `  )`,
    `  or container_cpu_usage_cores{workload_kind=~"StatefulSet|DaemonSet",workload_name!=""}`,
    `))`,
  ].join(' ')

  const { data, isLoading, error } = useQuery({
    queryKey: ['top-workloads-cpu', topN],
    queryFn: () => api.queryMetrics({ query }),
    refetchInterval: refetchMs,
    enabled,
    retry: false,
  })

  if (!installed) return null

  // Build a lookup keyed by namespace/kind/name for the overview's
  // workload summaries so each topk row can pull its requests/limits
  // in O(1). The overview pre-aggregates both DaemonSets and
  // StatefulSets under the same `namespaceWorkloads.workloads[]`
  // shape; matching by all three keys avoids name collisions across
  // namespaces (e.g. multiple "redis" Deployments in different ns).
  const workloadIndex = new Map<string, WorkloadSummary>()
  for (const nsw of overview?.namespaceWorkloads ?? []) {
    for (const w of nsw.workloads ?? []) {
      workloadIndex.set(`${w.namespace}/${w.kind}/${w.name}`, w)
    }
  }

  const rows: WorkloadRow[] = (data?.data?.result ?? [])
    .map((s) => ({
      namespace: s.metric.pod_namespace ?? '',
      kind: s.metric.workload_kind ?? '',
      name: s.metric.workload_name ?? '',
      cores: parseFloat(s.value?.[1] ?? '0'),
    }))
    .filter((r) => r.name && !Number.isNaN(r.cores))
    .sort((a, b) => b.cores - a.cores)

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-baseline justify-between mb-3">
        <h4 className="text-xs font-mono uppercase tracking-wider text-kb-text-secondary">
          Top workloads · CPU
        </h4>
        <span className="text-[10px] font-mono text-kb-text-tertiary">
          cluster-wide · top {topN}
        </span>
      </div>

      {isLoading && (
        <div className="py-6">
          <LoadingSpinner size="sm" />
        </div>
      )}

      {error && !isLoading && (
        <div className="text-[11px] text-status-warn font-mono py-3">
          Query failed — VictoriaMetrics unreachable or empty. Check the agent's connection state.
        </div>
      )}

      {!isLoading && !error && rows.length === 0 && (
        <div className="text-[11px] text-kb-text-tertiary py-3">
          No workload samples yet. The agent ships every 15s — give it a moment after install.
        </div>
      )}

      {!isLoading && !error && rows.length > 0 && (
        <ul className="space-y-2">
          {rows.map((r, i) => {
            const path = WORKLOAD_TYPE_TO_PATH[r.kind]
            const matched = workloadIndex.get(`${r.namespace}/${r.kind}/${r.name}`)
            // VM cores → millicores so it lines up with the
            // overview's request/limit which are in millicores.
            const usageMilli = Math.round(r.cores * 1000)
            const requestMilli = matched?.cpu?.requested ?? 0
            const limitMilli = matched?.cpu?.limit ?? 0
            const hasSpecs = requestMilli > 0 || limitMilli > 0

            const cellClass =
              'flex items-center gap-3 px-2 py-1.5 rounded transition-colors hover:bg-kb-card-hover'
            const inner = (
              <>
                <span className="text-[10px] font-mono text-kb-text-tertiary w-4 text-right tabular-nums">
                  {i + 1}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-1.5 truncate mb-1">
                    <span className="text-xs text-kb-text-primary truncate">{r.name}</span>
                    <span className="text-[10px] font-mono text-kb-text-tertiary truncate">
                      {r.namespace}
                    </span>
                  </div>
                  {hasSpecs ? (
                    // Workload has CPU requests or limits → real
                    // budget exists, ResourceUsageCell colors the
                    // bar by % of denominator (request fallback to
                    // limit) and shows markers in the tooltip.
                    <ResourceUsageCell
                      type="cpu"
                      usage={usageMilli}
                      request={requestMilli}
                      limit={limitMilli}
                      percent={0}
                      size="lg"
                    />
                  ) : (
                    // Workload has neither requests nor limits.
                    // Coloring the bar red because the workload is
                    // top-of-list would be misleading — there's no
                    // budget to be near. Use a neutral bar sized to
                    // its share of the heaviest consumer, with a
                    // tooltip that calls out the missing specs so
                    // the user can spot misconfigured workloads.
                    <NoSpecsBar
                      usageMilli={usageMilli}
                      topUsageMilli={Math.round(rows[0].cores * 1000)}
                      workload={r.name}
                    />
                  )}
                </div>
              </>
            )
            return (
              <li key={`${r.namespace}/${r.kind}/${r.name}`}>
                {path ? (
                  <Link
                    to={`/${path}/${encodeURIComponent(r.namespace)}/${encodeURIComponent(r.name)}`}
                    className={cellClass}
                  >
                    {inner}
                  </Link>
                ) : (
                  <div className={cellClass}>{inner}</div>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

// NoSpecsBar — bar variant for workloads with neither CPU requests
// nor limits. Same green as the well-behaved rows: there's no budget
// to be "saturated" against, but coloring it differently (gray) read
// as "broken" / "missing" rather than "fine but unbounded". Green
// keeps the visual rhythm of the list and the tooltip carries the
// nuance ("no specs defined, bar is relative to top consumer").
function NoSpecsBar({
  usageMilli,
  topUsageMilli,
  workload,
}: {
  usageMilli: number
  topUsageMilli: number
  workload: string
}) {
  const pct = topUsageMilli > 0 ? Math.min(100, (usageMilli / topUsageMilli) * 100) : 0
  return (
    <HoverTooltip
      offset={4}
      body={
        <>
          <TooltipHeader right="no specs">{workload}</TooltipHeader>
          <div className="space-y-1">
            <TooltipRow color="#22d68a" label="Used" value={formatCPU(usageMilli)} />
            <TooltipRow color={null} label="Request" value="not set" />
            <TooltipRow color={null} label="Limit" value="not set" />
          </div>
        </>
      }
    >
      <div className="flex items-center gap-1.5">
        <div
          className="flex-1 relative rounded-full overflow-hidden h-[7px]"
          style={{ background: 'var(--kb-bar-track)' }}
        >
          <div
            className="absolute inset-y-0 left-0 rounded-full bg-status-ok transition-all duration-500"
            style={{ width: `${pct}%` }}
          />
        </div>
        <span className="text-[11px] font-mono text-kb-text-secondary">
          {formatCPU(usageMilli)}
        </span>
      </div>
    </HoverTooltip>
  )
}
