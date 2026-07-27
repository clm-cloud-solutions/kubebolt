import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useCostAvailable probes whether OpenCost cost metrics are shipping
// into VictoriaMetrics for the active cluster — the same gating
// pattern as useHubbleAvailable, applied to the Cost sub-tab.
//
// Cost data only exists on a cluster where (a) OpenCost is deployed
// (bundled sub-chart, existing install, or the promRead cost flag)
// AND (b) the KubeBolt Agent is forwarding its series. On any cluster
// without that, the Cost tab would render empty panels — so we gate
// the tab's visibility on this probe and show an "enable cost
// monitoring" empty state on direct navigation.
//
// Probe: `count(node_total_hourly_cost)`. This is OpenCost's
// authoritative per-node total-cost gauge — emitted for every node
// the moment OpenCost has a pricing source, and the metric the whole
// Cost dashboard anchors its headline spend on. If it's present with
// a positive count, the cluster has cost data; empty vector or zero
// means no OpenCost feed. The backend scopes the query to the active
// cluster (cluster_id injection), so this is per-cluster, not fleet.
//
// LAST-KNOWN persistence: without it the tab pops in a beat after the
// page — the probe starts `false` and only flips true once the query
// resolves, so the Cost tab is absent for the first render(s) then
// appears, which reads as jank. We persist the definitive answer PER
// CLUSTER in localStorage and seed the initial value from it, so on
// every revisit the tab is present from frame one. The live probe
// still runs and corrects the cache if OpenCost was removed. Keyed by
// the active cluster's context (agent:<uid> / file id) because cost
// availability is per-cluster — a cluster with OpenCost and one
// without must not share an answer.
//
// Cached 60s with retry:false — OpenCost install state is a
// deliberate ops action (minute resolution is plenty) and a failing
// query shouldn't spam VM.

interface CostDetectionResult {
  available: boolean
  isLoading: boolean
}

const CACHE_PREFIX = 'kb-cost-available:'

function readCached(clusterKey: string | null): boolean {
  if (!clusterKey) return false
  try {
    return localStorage.getItem(CACHE_PREFIX + clusterKey) === '1'
  } catch {
    return false
  }
}

function writeCached(clusterKey: string | null, value: boolean): void {
  if (!clusterKey) return
  try {
    localStorage.setItem(CACHE_PREFIX + clusterKey, value ? '1' : '0')
  } catch {
    // Storage unavailable (private mode / quota) — the live probe
    // still works, we just lose the no-flash optimization.
  }
}

// promScalarPresent reads a `count(...)` response and returns true
// iff the result vector has at least one row with a positive value.
// Empty vector → metric doesn't exist; zero scalar → metric exists
// but no series — both treated as "not available".
function promScalarPresent(data: unknown): boolean {
  const result = (
    data as { data?: { result?: { value?: [number, string] }[] } } | undefined
  )?.data?.result
  if (!result || result.length === 0) return false
  const raw = result[0]?.value?.[1]
  if (raw === undefined || raw === null) return false
  return parseFloat(raw) > 0
}

export function useCostAvailable(): CostDetectionResult {
  // Active cluster identity for the per-cluster cache key. Shares the
  // ['clusters'] cache with the Topbar / OverviewHeader — no extra
  // round-trip.
  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    refetchInterval: 60_000,
  })
  const clusterKey = clusters?.find((c) => c.active)?.context ?? null

  const { data, isLoading } = useQuery({
    queryKey: ['cost-available', clusterKey],
    queryFn: () => api.queryMetrics({ query: `count(node_total_hourly_cost)` }),
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry: false,
  })

  const resolved = data !== undefined && !isLoading
  const live = promScalarPresent(data)

  // Persist only a definitive answer (query resolved). Effect keeps
  // the write out of render.
  useEffect(() => {
    if (resolved) writeCached(clusterKey, live)
  }, [resolved, live, clusterKey])

  // Before the probe resolves, fall back to this cluster's last-known
  // answer so the tab doesn't flash in. After it resolves, the live
  // answer wins.
  const available = resolved ? live : readCached(clusterKey)
  return { available, isLoading }
}
