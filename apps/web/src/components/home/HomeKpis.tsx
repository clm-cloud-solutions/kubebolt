import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import type { ClusterInfo } from '@/types/kubernetes'
import type { FleetRollup } from '@/hooks/useFleetRollup'
import { api } from '@/services/api'
import { TooltipHeader, TooltipNote } from '@/components/shared/Tooltip'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { KPI_COLOR, Legend, RingStat, Sparkline, SplitBar, StatusList } from '@/components/shared/kpi/MiniCharts'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'
import { useFleetPodsTrend } from '@/hooks/useFleetPodsTrend'

// HomeKpis — the four cards under Home's header, in the site's card anatomy
// (KpiCard). Same four numbers the strip showed — clusters, pods, monthly
// spend, critical findings — each with the minichart that tells its story:
//
//   clusters  → WHICH ones need you and why (unreachable / no agent)
//   pods      → the fleet's pod count over the last 24h
//   spend     → how much of the run-rate pays for capacity nothing requested
//   findings  → critical as a share of critical + high
//
// Every figure folds over the clusters the page describes (the team lens):
// the per-cluster series come down by cluster_id and are summed here, the
// same way useFleetRollup does it, so a chart never covers a different set
// than the number above it.

const KPI_MIN_WIDTH = 300

// Allocated compute cost ($/h) per cluster — the Cost page's
// ALLOCATED_TOTAL_QUERY, grouped by cluster. The join carries cluster_id as
// well as node: two clusters can name a node the same way, and a node-only
// join across the fleet would pair one cluster's containers with another's
// rates.
const ALLOCATED_BY_CLUSTER = [
  'sum by (cluster_id) (container_cpu_allocation * on(cluster_id, node) group_left max by (cluster_id, node) (node_cpu_hourly_cost))',
  '+',
  'sum by (cluster_id) ((container_memory_allocation_bytes / 1024 / 1024 / 1024) * on(cluster_id, node) group_left max by (cluster_id, node) (node_ram_hourly_cost))',
].join(' ')

function money(v: number | null): string {
  if (v === null) return '—'
  return `$${Math.round(v).toLocaleString()}`
}

interface HomeKpisProps {
  clusters: ClusterInfo[]
  visibleIds: string[]
  rollup: FleetRollup
  critical: number
  high: number
  canSeeCost: boolean
  costIsPromo: boolean
}

export function HomeKpis({ clusters, visibleIds, rollup, critical, high, canSeeCost, costIsPromo }: HomeKpisProps) {
  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()
  const visible = new Set(visibleIds)

  const podsTrend = useFleetPodsTrend(visibleIds.length > 0)
  const allocatedQ = useQuery({
    queryKey: ['home-kpi', 'allocated'],
    queryFn: () => api.queryMetrics({ query: ALLOCATED_BY_CLUSTER, scope: 'fleet' }),
    enabled: canSeeCost && rollup.costAvailable,
    refetchInterval: 5 * 60_000,
    staleTime: 60_000,
  })

  // ── Clusters ──────────────────────────────────────────────────────────
  const isHealthy = (c: ClusterInfo) => c.status === 'connected' || !!c.agentConnected
  const unhealthy = clusters
    .filter((c) => !isHealthy(c))
    .map((c) => ({ name: c.displayName || c.name, crit: c.status === 'error' }))
    .sort((a, b) => Number(b.crit) - Number(a.crit))
  const healthy = clusters.length - unhealthy.length
  // Three rows at most — the Overview's insights list sets the row height and
  // a fourth row here would stretch every card beside it.
  const shownBad = unhealthy.slice(0, healthy > 0 ? 2 : 3)
  const clusterRows = [
    ...shownBad.map((u) => ({
      color: u.crit ? KPI_COLOR.err : KPI_COLOR.warn,
      label: u.name,
      value: u.crit ? 'unreachable' : 'no agent',
      valueColor: u.crit ? KPI_COLOR.err : KPI_COLOR.warn,
      to: '/fleet',
    })),
    ...(healthy > 0
      ? [{ color: KPI_COLOR.ok, label: `${healthy} healthy`, value: unhealthy.length > shownBad.length ? `+${unhealthy.length - shownBad.length} more` : undefined, to: '/fleet' }]
      : []),
  ]

  // ── Pods: sum the visible clusters' series per timestamp ──────────────
  const podTotals = new Map<number, number>()
  for (const [id, values] of podsTrend) {
    if (!visible.has(id)) continue
    for (const [t, v] of values) podTotals.set(t, (podTotals.get(t) ?? 0) + v)
  }
  const podSeries = [...podTotals.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => Math.round(v))
  const podsByCluster = clusters
    // " (via agent)" says how the cluster is connected, not which one it is —
    // dropped so two names fit the one-line caption.
    .map((c) => ({
      name: (c.displayName || c.name).replace(/ \(via agent\)$/, ''),
      pods: c.clusterId ? rollup.byCluster[c.clusterId]?.pods ?? null : null,
    }))
    .filter((c): c is { name: string; pods: number } => c.pods != null)
    .sort((a, b) => b.pods - a.pods)

  // ── Spend: used vs idle over the clusters that report cost ────────────
  let allocated: number | null = null
  for (const r of (allocatedQ.data?.data?.result ?? []) as { metric?: Record<string, string>; value?: [number, string] }[]) {
    if (!visible.has(r.metric?.cluster_id ?? '')) continue
    const v = Number(r.value?.[1])
    if (Number.isFinite(v)) allocated = (allocated ?? 0) + v * 730
  }
  const spend = rollup.fleetSpendMonthly
  const idle = spend != null && allocated != null ? Math.max(0, spend - allocated) : null
  const idlePct = idle != null && spend ? Math.round((idle / spend) * 100) : null
  const reporting = visibleIds.filter((id) => rollup.byCluster[id]?.costMonthly != null).length

  // ── Findings ──────────────────────────────────────────────────────────
  const critPct = critical + high > 0 ? Math.round((critical / (critical + high)) * 100) : 0

  return (
    <div
      ref={gridRef}
      className={`grid gap-4 ${gridWidth ? '' : 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'}`}
      style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, KPI_MIN_WIDTH, 16, [4, 2, 1])}, minmax(0, 1fr))` } : undefined}
    >
      <KpiCard
        primary
        alert={unhealthy.some((u) => u.crit) ? 'crit' : unhealthy.length > 0 ? 'warn' : undefined}
        value={clusters.length}
        unit={clusters.length === 1 ? 'cluster' : 'clusters'}
        description={unhealthy.length > 0 ? `${unhealthy.length} need your attention` : 'all reporting'}
        viz={<StatusList rows={clusterRows} />}
        caption={
          <Link to="/fleet" className="hover:text-kb-text-primary transition-colors">
            open Fleet →
          </Link>
        }
        info={
          <>
            <TooltipHeader>Clusters you can see</TooltipHeader>
            <TooltipNote>
              Narrowed by the team lens in the user menu — the same set Fleet and the switcher show. Not every
              cluster in the org.
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        value={rollup.totalPods ?? '—'}
        unit="pods"
        description={rollup.totalPods != null ? `running on ${rollup.totalNodes ?? '—'} nodes` : 'waiting for samples'}
        viz={podSeries.length >= 2 ? <Sparkline values={podSeries} window="24h" /> : undefined}
        caption={
          podsByCluster.length > 0
            ? podsByCluster.slice(0, 2).map((c) => `${c.name} ${c.pods}`).join(' · ') +
              (podsByCluster.length > 2 ? ` · +${podsByCluster.length - 2}` : '')
            : undefined
        }
        info={
          <>
            <TooltipHeader right="live">Pods across your clusters</TooltipHeader>
            <TooltipNote>
              Counted from the container samples each agent ships, not from a periodic inventory — so it moves with
              the fleet and a cluster whose agent has gone quiet simply stops contributing instead of reporting a
              stale figure. Nodes come from the same stream.
            </TooltipNote>
          </>
        }
      />

      {!canSeeCost ? (
        <KpiCard
          value="—"
          unit="/mo"
          description="cost visibility"
          caption={
            <Link to="/account" className="hover:text-kb-text-primary transition-colors">
              arrives with the Team plan →
            </Link>
          }
        />
      ) : (
        <KpiCard
          value={rollup.costAvailable ? money(spend) : '—'}
          unit="/mo"
          description={
            !rollup.costAvailable
              ? 'no cost data yet'
              : idlePct != null
                ? `${idlePct}% of it pays for idle capacity`
                : 'monthly run-rate'
          }
          viz={
            idle != null && allocated != null ? (
              <SplitBar
                parts={[
                  { value: Math.round(allocated), color: KPI_COLOR.ok },
                  { value: Math.round(idle), hatched: true },
                ]}
                left={`used ${money(allocated)}`}
                right={`idle ${money(idle)}`}
              />
            ) : undefined
          }
          caption={
            rollup.costAvailable
              ? `OpenCost · ${reporting} of ${clusters.length} ${clusters.length === 1 ? 'cluster reports' : 'clusters report'}${costIsPromo ? ' · limited time' : ''}`
              : 'install OpenCost on a cluster to see spend'
          }
          info={
            <>
              <TooltipHeader right={costIsPromo ? 'limited time' : 'OpenCost'}>Fleet run-rate</TooltipHeader>
              <TooltipNote>
                OpenCost’s hourly node cost projected to a month, folded from the clusters above. Idle is the node
                cost no container’s CPU or memory request accounts for — the same split the Cost page shows.
                {costIsPromo && ' Included in Free for a limited time.'}
              </TooltipNote>
            </>
          }
        />
      )}

      <KpiCard
        alert={critical > 0 ? 'crit' : undefined}
        value={critical}
        unit="critical"
        description={critical > 0 ? 'security findings' : 'nothing critical open'}
        viz={
          critical + high > 0 ? (
            <RingStat
              parts={[
                { value: critical, color: KPI_COLOR.err },
                { value: high, color: 'var(--kb-text-tertiary)' },
              ]}
              legend={
                <Legend
                  rows={[
                    { color: KPI_COLOR.err, label: `${critical} critical · ${critPct}%` },
                    { color: 'var(--kb-text-tertiary)', label: `${high.toLocaleString()} high` },
                  ]}
                />
              }
            />
          ) : undefined
        }
        caption={
          <Link to="/security" className="hover:text-kb-text-primary transition-colors">
            open Security →
          </Link>
        }
        info={
          <>
            <TooltipHeader right="open">Critical findings</TooltipHeader>
            <TooltipNote>
              Open findings at critical severity across the clusters above — CVEs and exposed secrets from the
              scanners installed on each one. A cluster with no scanner contributes nothing, so a zero here means
              "nothing found", not "nothing looked".
            </TooltipNote>
          </>
        }
      />
    </div>
  )
}
