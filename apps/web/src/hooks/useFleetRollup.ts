import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useFleetRollup — the ALTITUDE-1 (fleet/account) numbers the Fleet page needs,
// read in ONE round-trip per metric instead of N per-cluster queries.
//
// The per-cluster Cost dashboard (`useClusterCost`) reads altitude 2: the
// backend pins `cluster_id` to the active cluster, so it can only ever describe
// one cluster. The Fleet grid needs the same families aggregated `by
// (cluster_id)` across the org, which is what `scope: 'fleet'` unlocks — the
// backend then skips cluster_id injection but STILL pins tenant_id, so the
// widening stays inside the caller's org (see scopeQueryForRequest).
//
// Join key: `cluster_id` is the kube-system namespace UID — the same value the
// agent stamps on every sample AND the value `ClusterInfo.clusterId` carries.
// Clusters whose UID the backend hasn't learned (direct-kubeconfig contexts
// that were never probed) simply won't match a row and render "—", which is
// correct: we have no series for them either.
//
// Every field is nullable on purpose. A cluster without OpenCost has no
// `node_total_hourly_cost`, and a cluster whose agent isn't shipping kubelet
// stats has no `container_*`. Rendering "—" beats rendering a confident 0.

const HOURS_PER_MONTH = 730

// Monthly spend comes from OpenCost's authoritative per-node gauge — one series
// per node carrying its hourly cost. Cost genuinely needs OpenCost, so a cluster
// without it renders "—", which is honest: there is no price to show.
const COST_BY_CLUSTER = 'sum by (cluster_id) (node_total_hourly_cost)'

// Node count does NOT need OpenCost, and used to be derived from the same cost
// gauge — so a perfectly healthy cluster without the integration reported "—"
// nodes, which reads as missing data rather than as a missing add-on. How many
// nodes a cluster has is not a cost question.
//
// `node_load1` instead, because it is the one node-scoped series available in
// BOTH agent topologies: Mode A synthesises it from /proc (which is also why GMP
// clusters, where nothing scrapes node-exporter, still report), and Mode C picks
// it up from node-exporter with the `node` label that promread's K8sNodeIndex
// stamps onto every `node_*` sample.
//
// Not container_cpu_usage_seconds_total (what PODS uses): promread only stamps
// `node` on samples whose name starts with `node_`, so on a metrics-only cluster
// the container stream arrives without it and the inner grouping would collapse
// to a single row — reporting 1 node for every cluster.
//
// The inner aggregation dedupes by `node`, not `instance`: a cluster running
// both topologies receives each node twice (once from the agent, once from
// node-exporter) under different instances. Verified against a two-cluster
// setup — by `node` gives the true 1 and 2, by `instance` gives 1 and 3.
const NODES_BY_CLUSTER = 'count by (cluster_id) (count by (cluster_id, node) (node_load1))'

// Pod count per cluster, from the kubelet/cadvisor stream every agent ships
// (so it works without KSM / promread). The inner aggregation collapses each
// pod's many container series — and the pod-level series cadvisor also emits —
// into one row per (cluster, namespace, pod); the outer one counts those rows.
// Grouped on namespace+pod rather than the `pod_uid` enrichment label, which
// the collector only stamps when the kubelet exposes it.
// Gasto de referencia para el delta.
//
// `avg_over_time(...[1d] offset Nd)` y NO un `offset Nd` a secas. La serie de
// coste tiene HUECOS —medido en el cluster de desarrollo: 147 puntos donde
// caben 560, porque el stack se para y arranca— y un offset puntual que caiga
// en un hueco devuelve vacío, así que el delta desaparecería la mayor parte del
// tiempo sin que nada explique por qué. Promediar un día entero salta los
// huecos y además suaviza el ruido de un pico puntual, que es lo que se quiere
// comparar: el nivel de gasto, no el instante.
const costBaselineQuery = (offsetDays: number) =>
  `avg_over_time((sum by (cluster_id) (node_total_hourly_cost))[1d:1h] offset ${offsetDays}d)`

// Ventana de comparación por retención del plan. NO es una decisión de
// producto: con 15 días guardados no se puede comparar contra hace 30, así que
// el plan que compra retención compra también el horizonte del delta.
export function deltaWindowDays(retentionDays?: number): number {
  if (!retentionDays || retentionDays >= 90) return 30
  if (retentionDays >= 30) return 30
  return 7
}

const PODS_BY_CLUSTER =
  'count by (cluster_id) (count by (cluster_id, namespace, pod) (container_cpu_usage_seconds_total))'

export interface FleetClusterRollup {
  costMonthly: number | null
  nodes: number | null
  pods: number | null
  /**
   * Variación del gasto frente a la ventana de comparación, en tanto por uno
   * (0.08 = +8%). null cuando no hay suficiente histórico para comparar.
   */
  costDelta: number | null
}

export interface FleetRollup {
  /** Keyed by cluster_id (kube-system UID). */
  byCluster: Record<string, FleetClusterRollup>
  fleetSpendMonthly: number | null
  totalPods: number | null
  totalNodes: number | null
  /** False when no cluster in the fleet reports cost — hides the spend KPI. */
  costAvailable: boolean
  isLoading: boolean
}

interface PromRow {
  metric?: Record<string, string>
  value?: [number, string]
}

/** Folds a `by (cluster_id)` instant vector into { [clusterId]: number }. */
function indexByCluster(data: unknown): Record<string, number> {
  const rows = (data as { data?: { result?: PromRow[] } } | undefined)?.data?.result
  if (!rows) return {}
  const out: Record<string, number> = {}
  for (const row of rows) {
    const id = row.metric?.cluster_id
    const raw = row.value?.[1]
    if (!id || raw === undefined) continue
    const n = parseFloat(raw)
    if (Number.isFinite(n)) out[id] = n
  }
  return out
}


// `visibleClusterIds` narrows the TOTALS to a subset — pass the cluster_ids the
// caller is actually showing. This exists because the team selector is a lens
// the user picks ("All teams" / Team A / Team B): with a team focused, the
// cluster list is filtered client-side by filterClustersByActiveTeam, but the
// backend roll-up is scoped by tenant_id (the ORG) and knows nothing about
// teams — VictoriaMetrics carries no team label at all. Summing server-side
// would print the whole org's spend above a list showing one team's clusters,
// so the totals are folded from the per-cluster rows instead. The numbers then
// match the list by construction.
//
// Omit it and the totals cover every cluster in the org, which is correct for
// "All teams".
export function useFleetRollup(
  enabled = true,
  visibleClusterIds?: string[],
  /** Retención del plan, en días — fija el horizonte del delta de coste. */
  retentionDays?: number,
): FleetRollup {
  const deltaDays = deltaWindowDays(retentionDays)
  const opts = {
    enabled,
    staleTime: 60_000,
    refetchInterval: 60_000,
    // A fleet with no OpenCost / no agent metrics answers with an empty vector,
    // not an error — but a genuinely unreachable VM shouldn't be retried into
    // a stall on a page that renders fine without these numbers.
    retry: false,
  }

  const cost = useQuery({
    queryKey: ['fleet-rollup', 'cost'],
    queryFn: () => api.queryMetrics({ query: COST_BY_CLUSTER, scope: 'fleet' }),
    ...opts,
  })
  const nodes = useQuery({
    queryKey: ['fleet-rollup', 'nodes'],
    queryFn: () => api.queryMetrics({ query: NODES_BY_CLUSTER, scope: 'fleet' }),
    ...opts,
  })
  const pods = useQuery({
    queryKey: ['fleet-rollup', 'pods'],
    queryFn: () => api.queryMetrics({ query: PODS_BY_CLUSTER, scope: 'fleet' }),
    ...opts,
  })
  // La referencia del delta. Cadencia mucho más lenta que el resto: compara
  // contra hace semanas, así que refrescarla cada minuto sería pagar una
  // consulta cara por un número que no puede haber cambiado.
  const costBefore = useQuery({
    queryKey: ['fleet-rollup', 'cost-before', deltaDays],
    queryFn: () => api.queryMetrics({ query: costBaselineQuery(deltaDays), scope: 'fleet' }),
    ...opts,
    staleTime: 15 * 60_000,
    refetchInterval: 15 * 60_000,
  })

  const costIdx = indexByCluster(cost.data)
  const costBeforeIdx = indexByCluster(costBefore.data)
  const nodeIdx = indexByCluster(nodes.data)
  const podIdx = indexByCluster(pods.data)

  const byCluster: Record<string, FleetClusterRollup> = {}
  for (const id of new Set([
    ...Object.keys(costIdx),
    ...Object.keys(nodeIdx),
    ...Object.keys(podIdx),
  ])) {
    const now = costIdx[id]
    const before = costBeforeIdx[id]
    byCluster[id] = {
      costMonthly: now !== undefined ? now * HOURS_PER_MONTH : null,
      nodes: nodeIdx[id] ?? null,
      pods: podIdx[id] ?? null,
      // Sin referencia no hay delta: null, nunca 0. Un «0%» afirmaría que el
      // gasto no se movió, cuando la verdad es que no había con qué comparar —
      // un cluster dado de alta esta semana, o una retención más corta que la
      // ventana. Y `before > 0` porque dividir por cero da Infinity y la UI lo
      // pintaría como un salto espectacular.
      costDelta:
        now !== undefined && before !== undefined && before > 0 ? now / before - 1 : null,
    }
  }

  // Totals are folded from the per-cluster rows, narrowed to what the caller
  // shows. Never from a server-side sum() — see the note on visibleClusterIds.
  const inScope = visibleClusterIds
    ? (id: string) => visibleClusterIds.includes(id)
    : () => true

  const narrow = (idx: Record<string, number>): number | null => {
    const values = Object.entries(idx)
      .filter(([id]) => inScope(id))
      .map(([, v]) => v)
    return values.length === 0 ? null : values.reduce((a, b) => a + b, 0)
  }

  const hourly = narrow(costIdx)

  return {
    byCluster,
    fleetSpendMonthly: hourly !== null ? hourly * HOURS_PER_MONTH : null,
    totalPods: narrow(podIdx),
    totalNodes: narrow(nodeIdx),
    // "Cost data exists" must also respect the lens: a fleet whose only
    // OpenCost-enabled cluster belongs to another team has no cost data from
    // where this user is standing.
    costAvailable: Object.keys(costIdx).some(inScope),
    isLoading: cost.isLoading || nodes.isLoading || pods.isLoading,
  }
}
