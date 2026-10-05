import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import type { ClusterInfo } from '@/types/kubernetes'
import type { FleetRollup } from '@/hooks/useFleetRollup'
import { api } from '@/services/api'
import { parseClusterDisplayName } from '@/utils/cluster'
import { healthFromInsights, type HealthVerdict, type InsightCounts } from '@/utils/clusterHealth'
import { TooltipHeader, TooltipNote } from '@/components/shared/Tooltip'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { BarList, KPI_COLOR, Legend, SemiGauge, Sparkline, UnitStrip } from '@/components/shared/kpi/MiniCharts'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'

// FleetKpis — the row Fleet opens with, in the site's card anatomy (KpiCard),
// the same family as Home and the cluster Overview. Home asks "what needs
// me?"; this row asks how the fleet is made up, so the four cards are the
// fleet's shape rather than Home's four numbers again:
//
//   clusters → one cell per cluster, coloured by its health verdict
//   spend    → the fleet's run-rate over the last 7 days
//   pods     → where the pods run, per cluster (nodes in the sentence)
//   agents   → how much of the fleet has a live agent channel
//
// Every figure covers the clusters the page lists (the team lens): the
// per-cluster series come down by cluster_id and are folded here.

const KPI_MIN_WIDTH = 300

// Fleet run-rate per cluster ($/h) — COST_BY_CLUSTER from useFleetRollup, as
// a range.
const COST_BY_CLUSTER_RANGE = 'sum by (cluster_id) (node_total_hourly_cost)'

const HOURS_PER_MONTH = 730

const VERDICT_ORDER: Record<HealthVerdict, number> = { critical: 0, warning: 1, healthy: 2, unknown: 3 }
const VERDICT_COLOR: Record<HealthVerdict, string> = {
  critical: KPI_COLOR.err,
  warning: KPI_COLOR.warn,
  healthy: KPI_COLOR.ok,
  unknown: KPI_COLOR.muted,
}

function money(v: number | null): string {
  if (v === null) return '—'
  return `$${Math.round(v).toLocaleString()}`
}

function shortName(c: ClusterInfo): string {
  return parseClusterDisplayName(c).replace(/ \(via agent\)$/, '')
}

function hoursSince(iso?: string | null): number | null {
  if (!iso) return null
  return Math.max(0, (Date.now() - Date.parse(iso)) / 3_600_000)
}

function fmtHours(h: number): string {
  if (h < 1) return `${Math.max(1, Math.round(h * 60))}m`
  if (h < 48) return `${Math.round(h)}h`
  return `${Math.round(h / 24)}d`
}

interface FleetKpisProps {
  clusters: ClusterInfo[]
  rollup: FleetRollup
  insights?: Record<string, InsightCounts>
  // Clusters with any series in the roll-up — the spend caption's
  // denominator: one that ships nothing has no cost for an obvious reason.
  reporting: number
  canManage: boolean
}

export function FleetKpis({ clusters, rollup, insights, reporting, canManage }: FleetKpisProps) {
  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()
  const ids = clusters.map((c) => c.clusterId).filter((id): id is string => !!id)
  const visible = new Set(ids)

  const spendQ = useQuery({
    queryKey: ['fleet-kpi', 'spend-trend'],
    queryFn: () => {
      const end = Math.floor(Date.now() / 1000)
      return api.queryMetricsRange({ query: COST_BY_CLUSTER_RANGE, start: end - 7 * 86400, end, step: '3h', scope: 'fleet' })
    },
    enabled: rollup.costAvailable,
    refetchInterval: 10 * 60_000,
    staleTime: 5 * 60_000,
  })

  // ── Clusters: one cell each, worst first ──────────────────────────────
  const verdicts = clusters.map((c) => ({
    c,
    v: healthFromInsights(c.clusterId ? insights?.[c.clusterId] : undefined),
  }))
  const tally = { critical: 0, warning: 0, healthy: 0, unknown: 0 }
  for (const { v } of verdicts) tally[v]++
  const cells = [...verdicts]
    .sort((a, b) => VERDICT_ORDER[a.v] - VERDICT_ORDER[b.v])
    .map(({ c, v }) => ({ color: VERDICT_COLOR[v], hatched: v === 'unknown', title: `${shortName(c)} — ${v === 'unknown' ? 'no data' : v}` }))
  const clustersSentence =
    tally.critical > 0
      ? `${tally.critical} with criticals`
      : tally.warning > 0
        ? `${tally.warning} with warnings`
        : tally.unknown > 0
          ? `${tally.unknown} not evaluated yet`
          : 'all healthy'

  // ── Spend: the visible clusters' run-rate per timestamp ──────────────
  const spendTotals = new Map<number, number>()
  for (const s of spendQ.data?.data?.result ?? []) {
    if (!visible.has(s.metric.cluster_id ?? '')) continue
    for (const [t, v] of s.values) spendTotals.set(t, (spendTotals.get(t) ?? 0) + Number(v) * HOURS_PER_MONTH)
  }
  const spendSeries = [...spendTotals.entries()].sort((a, b) => a[0] - b[0]).map(([, v]) => Math.round(v))
  const spenders = clusters
    .map((c) => ({ name: shortName(c), cost: c.clusterId ? rollup.byCluster[c.clusterId]?.costMonthly ?? null : null }))
    .filter((c): c is { name: string; cost: number } => c.cost != null)
    .sort((a, b) => b.cost - a.cost)
  const spend = rollup.fleetSpendMonthly
  const topShare = spenders.length > 0 && spend ? Math.round((spenders[0].cost / spend) * 100) : null
  const spendLo = spendSeries.length ? Math.min(...spendSeries) : 0
  const spendHi = spendSeries.length ? Math.max(...spendSeries) : 0

  // ── Pods per cluster ──────────────────────────────────────────────────
  const podRows = clusters
    .map((c) => ({ name: shortName(c), pods: c.clusterId ? rollup.byCluster[c.clusterId]?.pods ?? null : null }))
    .filter((c): c is { name: string; pods: number } => c.pods != null)
    .sort((a, b) => b.pods - a.pods)
  const reportingPods = podRows.length

  // ── Agents ────────────────────────────────────────────────────────────
  const live = clusters.filter((c) => c.agentConnected).length
  const offline = clusters.filter((c) => !c.agentConnected && c.source === 'agent-proxy')
  const longestOffline = offline.reduce<number | null>((m, c) => {
    const h = hoursSince(c.lastSeen)
    return h == null ? m : Math.max(m ?? 0, h)
  }, null)
  const direct = clusters.length - live - offline.length
  const livePct = clusters.length ? Math.round((live / clusters.length) * 100) : 0

  return (
    <div
      ref={gridRef}
      className={`grid gap-4 ${gridWidth ? '' : 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'}`}
      style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, KPI_MIN_WIDTH, 16, [4, 2, 1])}, minmax(0, 1fr))` } : undefined}
    >
      <KpiCard
        primary
        alert={tally.critical > 0 ? 'crit' : tally.warning > 0 ? 'warn' : undefined}
        value={clusters.length}
        unit={clusters.length === 1 ? 'cluster' : 'clusters'}
        description={clustersSentence}
        viz={
          cells.length > 0 ? (
            <UnitStrip
              cells={cells}
              legend={
                <div className="flex flex-wrap gap-x-3 gap-y-1">
                  {(['critical', 'warning', 'healthy', 'unknown'] as HealthVerdict[])
                    .filter((v) => tally[v] > 0)
                    .map((v) => (
                      <Legend
                        key={v}
                        rows={[{ color: VERDICT_COLOR[v], label: `${tally[v]} ${v === 'unknown' ? 'no data' : v}` }]}
                      />
                    ))}
                </div>
              }
            />
          ) : undefined
        }
        caption="worst first · from active insights"
        info={
          <>
            <TooltipHeader>Fleet health</TooltipHeader>
            <TooltipNote>
              One cell per cluster you can see, coloured by its active insights — the same verdict its own Overview
              shows. A cluster nobody has evaluated yet says "no data", never "healthy".
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        value={rollup.costAvailable ? money(spend) : '—'}
        unit="/mo"
        description={
          !rollup.costAvailable
            ? 'no cost data yet'
            : spenders.length === 1
              ? `all of it on ${spenders[0].name}`
              : topShare != null
                ? `${spenders[0].name} is ${topShare}% of it`
                : 'monthly run-rate'
        }
        viz={
          spendSeries.length >= 2 ? (
            <Sparkline
              values={spendSeries}
              left={spendHi > spendLo ? `7d · ${money(spendLo)}–${money(spendHi)}` : `7d · steady at ${money(spendLo)}`}
            />
          ) : undefined
        }
        // How much of the fleet the figure covers, said only when partial —
        // "2 of 2 clusters" is noise, and a KPI that qualifies itself when
        // nothing is wrong teaches people to ignore the qualifier.
        caption={
          !rollup.costAvailable
            ? 'install OpenCost on a cluster to see spend'
            : spenders.length < reporting
              ? `OpenCost · ${spenders.length} of ${reporting} reporting ${reporting === 1 ? 'cluster' : 'clusters'}`
              : 'OpenCost · run-rate'
        }
        info={
          <>
            <TooltipHeader right="OpenCost">Fleet spend</TooltipHeader>
            <TooltipNote>
              OpenCost's hourly node cost projected to a month (× 730), summed over the clusters above. Clusters without
              OpenCost contribute nothing, so the figure covers only the ones the caption counts.
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        value={rollup.totalPods ?? '—'}
        unit="pods"
        description={
          rollup.totalPods != null
            ? `on ${rollup.totalNodes ?? '—'} ${rollup.totalNodes === 1 ? 'node' : 'nodes'} · ${reportingPods} ${reportingPods === 1 ? 'cluster' : 'clusters'}`
            : 'waiting for samples'
        }
        viz={
          podRows.length > 0 ? (
            <BarList rows={podRows.slice(0, 3).map((r) => ({ label: r.name, value: r.pods }))} />
          ) : undefined
        }
        caption={podRows.length > 3 ? `+${podRows.length - 3} more clusters` : 'counted from the samples each agent ships'}
      />

      <KpiCard
        alert={offline.length > 0 ? 'warn' : undefined}
        value={live}
        unit={`/ ${clusters.length} agents live`}
        description={live === clusters.length ? 'every cluster reporting' : 'partial coverage'}
        viz={
          clusters.length > 0 ? (
            <SemiGauge
              percent={livePct}
              color={live === clusters.length ? KPI_COLOR.ok : KPI_COLOR.warn}
              legend={
                <Legend
                  rows={[
                    { color: KPI_COLOR.ok, label: `${live} live` },
                    ...(offline.length > 0
                      ? [
                          {
                            color: KPI_COLOR.warn,
                            label: `${offline.length} offline${longestOffline != null ? ` · ${fmtHours(longestOffline)}` : ''}`,
                          },
                        ]
                      : []),
                    ...(direct > 0 ? [{ color: KPI_COLOR.muted, label: `${direct} direct` }] : []),
                  ]}
                />
              }
            />
          ) : undefined
        }
        caption={
          canManage ? (
            <Link to="/admin/agents" className="hover:text-kb-text-primary transition-colors">
              Agents &amp; Ingest →
            </Link>
          ) : (
            'channel liveness, not reachability'
          )
        }
        info={
          <>
            <TooltipHeader right="live">Live agents</TooltipHeader>
            <TooltipNote>
              Clusters with an agent channel open right now. Channel liveness, not cluster reachability — a cluster
              connected through a kubeconfig counts as "direct" and never as live here.
            </TooltipNote>
          </>
        }
      />
    </div>
  )
}
