import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// Pods per cluster over the last 24h, one point every 30 min — the range form
// of useFleetRollup's PODS_BY_CLUSTER. Home sums it into the fleet's pod
// sparkline; Fleet draws one line per cluster card. One query, one cache
// entry: both pages read the same series, so they cannot disagree.
const PODS_BY_CLUSTER_RANGE =
  'count by (cluster_id) (count by (cluster_id, namespace, pod) (container_cpu_usage_seconds_total))'

export function useFleetPodsTrend(enabled: boolean): Map<string, [number, number][]> {
  const q = useQuery({
    queryKey: ['fleet-pods-trend'],
    queryFn: () => {
      const end = Math.floor(Date.now() / 1000)
      return api.queryMetricsRange({ query: PODS_BY_CLUSTER_RANGE, start: end - 86400, end, step: '30m', scope: 'fleet' })
    },
    enabled,
    refetchInterval: 5 * 60_000,
    staleTime: 60_000,
  })
  const out = new Map<string, [number, number][]>()
  for (const s of q.data?.data?.result ?? []) {
    const id = s.metric.cluster_id
    if (id) out.set(id, s.values.map(([t, v]) => [Number(t), Number(v)]))
  }
  return out
}
