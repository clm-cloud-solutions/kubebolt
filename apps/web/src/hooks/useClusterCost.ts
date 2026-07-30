import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useClusterCost owns the per-cluster cost model rendered by the Cost
// sub-tab (design/kubebolt-cost-redesign.html). Every figure derives
// from OpenCost metrics already in VictoriaMetrics — the backend
// scopes each query to the active cluster (cluster_id injection), so
// this is per-cluster cost, never fleet.
//
// Anchoring principle: the HEADLINE spend anchors on
// `node_total_hourly_cost` — OpenCost's own authoritative per-node
// total — so the top-line number matches OpenCost exactly. Per-
// namespace / per-workload attribution then DECOMPOSES that spend by
// each container's requested allocation share, priced with the
// node's cpu/ram rates:
//     $/h(container) = cpu_alloc·node_cpu_hourly_cost
//                    + (mem_alloc_bytes/GiB)·node_ram_hourly_cost
// The gap between the node total and the summed attribution is the
// IDLE / unallocated cost — surfaced as the waste KPI rather than
// hidden. (Attribution ≤ node total by construction: you can't
// allocate more than the node costs.)
//
// Convention: memory priced per GiB (1024³) to line up with the
// reclaimMemBytes convention in useRightSizing, so the $/mo savings
// estimate and the attribution use one ruler.
//
// Metrics used (all forwarded by the agent's OpenCost exporter /
// promRead cost matchers, all under allowlisted prefixes):
//   node_total_hourly_cost   node_cpu_hourly_cost   node_ram_hourly_cost
//   container_cpu_allocation  container_memory_allocation_bytes
//   kubecost_load_balancer_cost  kubecost_network_*_egress_cost
//   kubecost_node_is_spot    kube_pod_status_phase
//   container_cpu_usage_seconds_total (efficiency numerator)

// HOURS_PER_MONTH — OpenCost's convention for projecting an hourly
// rate to a monthly figure (730 = 365×24/12). One ruler for every
// $/h → $/mo conversion in the Cost surfaces.
export const HOURS_PER_MONTH = 730

const GIB = 1024 * 1024 * 1024

// ─── Public shapes ───────────────────────────────────────────────

export interface NamespaceCost {
  namespace: string
  monthly: number
  // CPU used / CPU allocated over the last 5m, as a percentage.
  // The commitment-efficiency signal the mockup shows beside each
  // namespace bar. null when the namespace has allocation but no
  // usage samples yet (just-deployed / RBAC-restricted).
  efficiencyPct: number | null
  // Raw per-resource used/allocated so the bar tooltip can explain
  // each resource the way Capacity's tooltips do. CPU in cores,
  // memory in bytes; null when the metric has no samples.
  cpuUsed: number | null
  cpuAllocated: number | null
  memUsed: number | null
  memAllocated: number | null
}

export interface WorkloadCost {
  namespace: string
  workload: string
  monthly: number
}

export interface NodeCost {
  node: string
  monthly: number
  isSpot: boolean
}

export interface ClusterCost {
  // Authoritative headline: sum of node_total_hourly_cost.
  hourly: number
  monthly: number
  // Σ attribution — what's accounted to running workloads.
  allocatedMonthly: number
  // monthly − allocatedMonthly: capacity you pay for but nothing
  // requested. The waste headline.
  idleMonthly: number
  idlePct: number | null
  // Blended CPU used/allocated across the cluster (efficiency KPI).
  efficiencyPct: number | null
  costPerPod: number | null
  podCount: number | null
  // LB + network egress ($/mo) — surfaced separately, not folded
  // into the compute headline (mockup's Network trend tab).
  networkMonthly: number
  byNamespace: NamespaceCost[]
  byWorkload: WorkloadCost[]
  byNode: NodeCost[]
  isLoading: boolean
  error: Error | null
}

export interface NodeRates {
  // $/core/hour and $/GiB/hour, averaged across the cluster's nodes.
  // Averaging is fine for savings estimates: rates are near-identical
  // within an instance type and this yields one stable $/unit.
  cpuCoreHourly: number
  ramGiBHourly: number
  available: boolean
}

// ─── PromQL ──────────────────────────────────────────────────────

// Per-(namespace,pod,node) $/h attribution. Two sum-by-then-add
// halves so the `+` matches on the reduced {namespace,pod,node}
// label set (validated on live VM data). group_left pulls the node's
// rate onto each container series by the shared `node` label.
// `max by (node)` collapses node_*_hourly_cost to ONE series per node:
// OpenCost pod restarts within the range leave stale + fresh cost
// series in VM, and group_left errors ("duplicate time series on the
// right side") unless the right side is unique per join label.
const ATTRIBUTION_QUERY = [
  'sum by (namespace, pod, node) (',
  '  container_cpu_allocation * on(node) group_left max by (node) (node_cpu_hourly_cost)',
  ')',
  '+',
  'sum by (namespace, pod, node) (',
  '  (container_memory_allocation_bytes / 1024 / 1024 / 1024) * on(node) group_left max by (node) (node_ram_hourly_cost)',
  ')',
].join(' ')

// Network + LB cost. `or` unions the families so absent ones (no NAT
// gateway on this cluster) simply don't contribute instead of
// nulling the whole expression the way `sum(a)+sum(b)` would when a
// is empty. All names start with `kube` → allowlisted. Exported so
// the spend-trend chart plots the same figure the KPI derives from.
export const NETWORK_COST_QUERY = [
  'sum(',
  '  kubecost_load_balancer_cost',
  '  or kubecost_network_internet_egress_cost',
  '  or kubecost_network_nat_gateway_egress_cost',
  '  or kubecost_network_region_egress_cost',
  '  or kubecost_network_zone_egress_cost',
  ')',
].join(' ')

// Cluster-wide allocated compute cost ($/h) — ATTRIBUTION_QUERY collapsed to a
// single cluster total (no per-namespace/pod/node grouping). Paired with
// sum(node_total_hourly_cost) it drives the commitment chart: the gap
// (node total − allocated) is idle/waste over time. group_left pulls each
// node's rate onto its container series by the shared `node` label. Same two
// summed halves as ATTRIBUTION_QUERY so the totals reconcile with the KPIs.
// `max by (node)` dedupes node_*_hourly_cost (see ATTRIBUTION_QUERY) so the
// range query survives OpenCost pod restarts.
export const ALLOCATED_TOTAL_QUERY = [
  'sum(container_cpu_allocation * on(node) group_left max by (node) (node_cpu_hourly_cost))',
  '+',
  'sum((container_memory_allocation_bytes / 1024 / 1024 / 1024) * on(node) group_left max by (node) (node_ram_hourly_cost))',
].join(' ')

// ─── Parse helpers ───────────────────────────────────────────────

type Vec = Array<{ metric: Record<string, string>; value?: [number, string] }>

function vec(data: unknown): Vec {
  return ((data as { data?: { result?: Vec } } | undefined)?.data?.result ?? []) as Vec
}

function scalar(data: unknown): number | null {
  const rows = vec(data)
  if (rows.length === 0) return null
  const raw = rows[0]?.value?.[1]
  if (raw === undefined) return null
  const n = parseFloat(raw)
  return Number.isFinite(n) ? n : null
}

// shortenPodName strips the ReplicaSet + pod hash suffixes so
// per-pod attribution rolls up to the user-visible workload:
// "payments-api-3b1c9-x7f2" → "payments-api". Mirrors the helper in
// CapacityStrip.
function shortenPodName(pod: string): string {
  return pod.replace(/-[a-z0-9]{6,12}-[a-z0-9]{5}$/, '').replace(/-[a-z0-9]{5}$/, '')
}

// ─── Node rates (shared with Capacity's $/mo savings) ────────────

export function useNodeRates(enabled = true): NodeRates {
  const cpuQ = useQuery({
    queryKey: ['cost', 'rate', 'cpu'],
    queryFn: () => api.queryMetrics({ query: 'avg(node_cpu_hourly_cost)' }),
    staleTime: 60_000,
    refetchInterval: 5 * 60_000,
    enabled,
    retry: false,
  })
  const ramQ = useQuery({
    queryKey: ['cost', 'rate', 'ram'],
    queryFn: () => api.queryMetrics({ query: 'avg(node_ram_hourly_cost)' }),
    staleTime: 60_000,
    refetchInterval: 5 * 60_000,
    enabled,
    retry: false,
  })
  const cpuCoreHourly = scalar(cpuQ.data) ?? 0
  const ramGiBHourly = scalar(ramQ.data) ?? 0
  return { cpuCoreHourly, ramGiBHourly, available: cpuCoreHourly > 0 || ramGiBHourly > 0 }
}

// estimateMonthlySavings turns reclaimable capacity (from
// useRightSizing) into $/mo at the cluster's node rates. Pure so both
// the Cost savings panel and the Capacity strip compute the same
// figure. Returns 0 when rates are unknown (no OpenCost) — callers
// fall back to the cores/GiB headline.
export function estimateMonthlySavings(
  reclaimCpuMilli: number,
  reclaimMemBytes: number,
  rates: NodeRates,
): number {
  const cores = reclaimCpuMilli / 1000
  const gib = reclaimMemBytes / GIB
  return (cores * rates.cpuCoreHourly + gib * rates.ramGiBHourly) * HOURS_PER_MONTH
}

// ─── Main hook ───────────────────────────────────────────────────

export function useClusterCost(enabled = true): ClusterCost {
  const totalQ = useQuery({
    queryKey: ['cost', 'total'],
    queryFn: () => api.queryMetrics({ query: 'sum(node_total_hourly_cost)' }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const attrQ = useQuery({
    queryKey: ['cost', 'attribution'],
    queryFn: () => api.queryMetrics({ query: ATTRIBUTION_QUERY }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const usageQ = useQuery({
    queryKey: ['cost', 'usage-by-ns'],
    queryFn: () =>
      api.queryMetrics({
        // [1h] not [5m]: efficiency is a COST signal — "how much of what
        // I pay for do I typically use" — so it must reflect steady-state
        // usage, not a 5-minute snapshot. A short window makes a bursty
        // workload swing between 3% and 88% as spikes come and go (a
        // real sm-store burst to 156m was caught mid-spike in-vivo). An
        // hourly average is stable and representative of what's billed.
        //
        // container!="" excludes cadvisor's per-POD rollup series (the
        // empty-container one), which otherwise DOUBLE-COUNTS every
        // namespace's usage. The allocation metric (OpenCost) has no
        // such rollup, so only the usage side needs the guard.
        query: 'sum by (namespace) (rate(container_cpu_usage_seconds_total{container!=""}[1h]))',
      }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const allocQ = useQuery({
    queryKey: ['cost', 'alloc-by-ns'],
    queryFn: () =>
      api.queryMetrics({ query: 'sum by (namespace) (container_cpu_allocation)' }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const memUsageQ = useQuery({
    queryKey: ['cost', 'mem-usage-by-ns'],
    queryFn: () =>
      api.queryMetrics({
        // container!="" — same cadvisor per-POD rollup double-count as
        // the CPU usage query above (sm-store read 810 MiB vs the real
        // 542 MiB without it).
        query: 'sum by (namespace) (container_memory_working_set_bytes{container!=""})',
      }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const memAllocQ = useQuery({
    queryKey: ['cost', 'mem-alloc-by-ns'],
    queryFn: () =>
      api.queryMetrics({ query: 'sum by (namespace) (container_memory_allocation_bytes)' }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const podsQ = useQuery({
    queryKey: ['cost', 'running-pods'],
    queryFn: () =>
      api.queryMetrics({ query: 'count(kube_pod_status_phase{phase="Running"} == 1)' }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const netQ = useQuery({
    queryKey: ['cost', 'network'],
    queryFn: () => api.queryMetrics({ query: NETWORK_COST_QUERY }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const nodeQ = useQuery({
    queryKey: ['cost', 'by-node'],
    queryFn: () => api.queryMetrics({ query: 'node_total_hourly_cost' }),
    staleTime: 30_000,
    refetchInterval: 60_000,
    enabled,
    retry: false,
  })
  const spotQ = useQuery({
    queryKey: ['cost', 'spot'],
    queryFn: () => api.queryMetrics({ query: 'kubecost_node_is_spot' }),
    staleTime: 5 * 60_000,
    refetchInterval: 5 * 60_000,
    enabled,
    retry: false,
  })

  const isLoading =
    totalQ.isLoading || attrQ.isLoading || nodeQ.isLoading || podsQ.isLoading
  const error =
    (totalQ.error as Error | null) ??
    (attrQ.error as Error | null) ??
    null

  const hourly = scalar(totalQ.data) ?? 0
  const monthly = hourly * HOURS_PER_MONTH
  const networkMonthly = (scalar(netQ.data) ?? 0) * HOURS_PER_MONTH
  const podCount = scalar(podsQ.data)

  // Attribution: fold per-(ns,pod,node) $/h into namespace / workload
  // totals. Each row is one pod on one node.
  const nsMonthly = new Map<string, number>()
  const wlMonthly = new Map<string, { namespace: string; workload: string; monthly: number }>()
  let allocatedHourly = 0
  for (const row of vec(attrQ.data)) {
    const perHour = parseFloat(row.value?.[1] ?? '0')
    if (!Number.isFinite(perHour)) continue
    allocatedHourly += perHour
    const ns = row.metric.namespace || 'unknown'
    nsMonthly.set(ns, (nsMonthly.get(ns) ?? 0) + perHour * HOURS_PER_MONTH)
    const wl = shortenPodName(row.metric.pod || '')
    if (wl) {
      const key = `${ns}/${wl}`
      const cur = wlMonthly.get(key) ?? { namespace: ns, workload: wl, monthly: 0 }
      cur.monthly += perHour * HOURS_PER_MONTH
      wlMonthly.set(key, cur)
    }
  }
  const allocatedMonthly = allocatedHourly * HOURS_PER_MONTH
  const idleMonthly = Math.max(0, monthly - allocatedMonthly)
  const idlePct = monthly > 0 ? (idleMonthly / monthly) * 100 : null

  // Per-namespace used/allocated for CPU (cores) and memory (bytes).
  const byNsIndex = (data: unknown): Map<string, number> => {
    const m = new Map<string, number>()
    for (const row of vec(data)) {
      const v = parseFloat(row.value?.[1] ?? '0')
      if (Number.isFinite(v)) m.set(row.metric.namespace || 'unknown', v)
    }
    return m
  }
  const usedByNs = byNsIndex(usageQ.data) // CPU cores
  const allocByNs = byNsIndex(allocQ.data) // CPU cores
  const memUsedByNs = byNsIndex(memUsageQ.data) // bytes
  const memAllocByNs = byNsIndex(memAllocQ.data) // bytes

  const nsEff = (ns: string): number | null => {
    const alloc = allocByNs.get(ns) ?? 0
    const used = usedByNs.get(ns)
    if (alloc <= 0 || used === undefined || !Number.isFinite(used)) return null
    return Math.min(100, (used / alloc) * 100)
  }

  const byNamespace: NamespaceCost[] = [...nsMonthly.entries()]
    .map(([namespace, m]) => ({
      namespace,
      monthly: m,
      efficiencyPct: nsEff(namespace),
      cpuUsed: usedByNs.get(namespace) ?? null,
      cpuAllocated: allocByNs.get(namespace) ?? null,
      memUsed: memUsedByNs.get(namespace) ?? null,
      memAllocated: memAllocByNs.get(namespace) ?? null,
    }))
    .sort((a, b) => b.monthly - a.monthly)

  const byWorkload: WorkloadCost[] = [...wlMonthly.values()].sort((a, b) => b.monthly - a.monthly)

  // Blended cluster efficiency = Σ used / Σ allocated cores.
  let totUsed = 0
  let totAlloc = 0
  for (const v of usedByNs.values()) if (Number.isFinite(v)) totUsed += v
  for (const v of allocByNs.values()) if (Number.isFinite(v)) totAlloc += v
  const efficiencyPct = totAlloc > 0 ? Math.min(100, (totUsed / totAlloc) * 100) : null

  const costPerPod = podCount && podCount > 0 ? monthly / podCount : null

  // Per-node full cost (authoritative), tagged spot from
  // kubecost_node_is_spot (== 1 → spot).
  const spotNodes = new Set<string>()
  for (const row of vec(spotQ.data)) {
    if (parseFloat(row.value?.[1] ?? '0') === 1) spotNodes.add(row.metric.node || '')
  }
  const byNode: NodeCost[] = vec(nodeQ.data)
    .map((row) => ({
      node: row.metric.node || row.metric.instance || 'unknown',
      monthly: parseFloat(row.value?.[1] ?? '0') * HOURS_PER_MONTH,
      isSpot: spotNodes.has(row.metric.node || ''),
    }))
    .filter((n) => Number.isFinite(n.monthly))
    .sort((a, b) => b.monthly - a.monthly)

  return {
    hourly,
    monthly,
    allocatedMonthly,
    idleMonthly,
    idlePct,
    efficiencyPct,
    costPerPod,
    podCount,
    networkMonthly,
    byNamespace,
    byWorkload,
    byNode,
    isLoading,
    error,
  }
}
