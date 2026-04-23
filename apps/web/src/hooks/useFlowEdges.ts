import { useQuery } from '@tanstack/react-query'
import { api, ApiError, type FlowEdgesResponse } from '@/services/api'

export type { FlowEdge, FlowEdgesResponse } from '@/services/api'

interface Params {
  namespace?: string
  windowMinutes?: number
  enabled?: boolean
}

// useFlowEdges polls the backend flow-edges endpoint, which aggregates
// pod_flow_events_total into per-pair rates. Returns an empty edges
// array (not an error) when no traffic observability source is emitting
// data yet — the UI treats "no flows" as a neutral state, not a failure.
export function useFlowEdges({ namespace, windowMinutes = 5, enabled = true }: Params = {}) {
  return useQuery<FlowEdgesResponse>({
    queryKey: ['flow-edges', namespace ?? 'all', windowMinutes],
    queryFn: () => api.getFlowEdges({ namespace, windowMinutes }),
    enabled,
    // Short poll so the map feels alive without hammering VM.
    refetchInterval: 15_000,
    // Don't retry on 4xx — those indicate config issues the user has to fix.
    retry: (failureCount, err) => {
      if (err instanceof ApiError && err.status >= 400 && err.status < 500) return false
      return failureCount < 2
    },
  })
}
