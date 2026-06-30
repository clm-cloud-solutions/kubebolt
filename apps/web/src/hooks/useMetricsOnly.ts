import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useMetricsOnly reports whether the ACTIVE cluster is metrics-only — its agent ships
// metrics but advertises no kube-proxy, so there is no live-resource connector. The UI
// uses it to show the metrics dashboards (Capacity / Reliability query VictoriaMetrics
// directly) while hiding/degrading the resource views (resource lists, Map, Kobi,
// kubectl-ops) that need the connector. Reuses the already-cached ['clusters'] query,
// so it adds no extra request.
export function useMetricsOnly(): boolean {
  const { data: clusters } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })
  return clusters?.find((c) => c.active)?.mode === 'metrics-only'
}
