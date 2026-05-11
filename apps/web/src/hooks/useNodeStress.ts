import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useNodeStress reads node-exporter load average + PSI pressure
// metrics and reduces them to a per-node summary keyed by node
// name. Same gated-signal philosophy as useNamespaceQuotas: when
// node-exporter isn't shipping (no scrape sidecar, agent uninstalled,
// or restricted RBAC) the hook returns an empty map and consumers
// silently fall back. No misleading zeros.
//
// Loaded as a section-level fetch by NodesPage so 3 instant queries
// run once for the whole list, not N. Each card reads its own slice
// from the map.
//
// Why instant queries (vs range): the list view shows a present-tense
// row ("load 7.6 / 7.7 / 7.8"), not a trend. A range query would
// pay extra bytes for data we'd discard. The detail-view charts
// (NodeMonitorCharts) own the historical view via MetricChart.

export interface NodeStress {
  load1: number
  load5: number
  load15: number
  psiCpu: number    // fraction of last 1m at least one task waited (0-1)
  psiIo: number
  psiMemory: number
}

export type NodeStressMap = Record<string, NodeStress>

// Severity bands for PSI "some" pressure, applied uniformly across
// cpu/io/memory. The lower bound matches the kernel's own folklore
// for "this is being noticed by the scheduler"; the upper bound is
// where we'd want oncall to look at the node. Operators can still
// see the raw rate values in the detail view chart.
export const PSI_WARN = 0.1
export const PSI_CRIT = 0.3

export function useNodeStress() {
  // Three parallel queries, one TanStack Query each so VM responses
  // can interleave — versus a single composite query (which would
  // fan-in but couple the caches and refetch cycles). Default cache
  // is 30s which matches the 30s refetchInterval used by other list
  // views.
  const load = useQuery({
    queryKey: ['node-stress', 'load'],
    queryFn: () => api.queryMetrics({ query: '{__name__=~"node_load(1|5|15)"}' }),
    staleTime: 30_000,
    refetchInterval: 30_000,
    retry: false,
  })

  const psiWaiting = useQuery({
    queryKey: ['node-stress', 'psi-waiting'],
    queryFn: () =>
      api.queryMetrics({
        query:
          'rate({__name__=~"node_pressure_(cpu|io|memory)_waiting_seconds_total"}[1m])',
      }),
    staleTime: 30_000,
    refetchInterval: 30_000,
    retry: false,
  })

  const map: NodeStressMap = {}
  const ensure = (node: string): NodeStress => {
    if (!map[node]) {
      map[node] = { load1: 0, load5: 0, load15: 0, psiCpu: 0, psiIo: 0, psiMemory: 0 }
    }
    return map[node]
  }

  for (const series of load.data?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const node = m.node || m.instance
    if (!node) continue
    const v = parseFloat(series.value?.[1] ?? '0')
    if (!isFinite(v)) continue
    const entry = ensure(node)
    if (m.__name__ === 'node_load1') entry.load1 = v
    else if (m.__name__ === 'node_load5') entry.load5 = v
    else if (m.__name__ === 'node_load15') entry.load15 = v
  }

  for (const series of psiWaiting.data?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const node = m.node || m.instance
    if (!node) continue
    const v = parseFloat(series.value?.[1] ?? '0')
    if (!isFinite(v)) continue
    const entry = ensure(node)
    // The metric name carries the pressure axis. Rate result is
    // dimensionless (seconds-per-second).
    if (m.__name__ === 'node_pressure_cpu_waiting_seconds_total') entry.psiCpu = v
    else if (m.__name__ === 'node_pressure_io_waiting_seconds_total') entry.psiIo = v
    else if (m.__name__ === 'node_pressure_memory_waiting_seconds_total') entry.psiMemory = v
  }

  return {
    stress: map,
    anyData: Object.keys(map).length > 0,
    isLoading: load.isLoading || psiWaiting.isLoading,
  }
}

// Severity classifier used by the card-level PSI indicator.
// Returns 'crit' / 'warn' / null (no signal). The "any axis trips"
// rule keeps the indicator gated on the worst pressure, which is
// what an operator wants to see at a glance.
export function classifyPSI(s: NodeStress): 'warn' | 'crit' | null {
  const worst = Math.max(s.psiCpu, s.psiIo, s.psiMemory)
  if (worst >= PSI_CRIT) return 'crit'
  if (worst >= PSI_WARN) return 'warn'
  return null
}
