import { useQuery } from '@tanstack/react-query'
import { api, ApiError, type CoverageResponse } from '@/services/api'

export type { CoverageSource, CoverageResponse } from '@/services/api'

interface Params {
  enabled?: boolean
}

// useCoverage polls /api/v1/coverage so the dashboard can surface
// which observability sources are actively shipping samples for
// the active cluster. Phase 2 Day 5 of the Universal Data Plane
// Plan.
//
// The banner that consumes this hook is informational, not
// actionable: it doesn't gate UI features (those have their own
// 503/empty-state handling). It exists so the operator knows
// what they have without grepping logs — a helm install that
// silently dropped node-exporter scraping should be visible
// from the dashboard, not just from `kubectl logs`.
//
// 60s polling cadence — sources don't flip rapidly, and the
// instant queries each cost ~5ms at VM. Keeping the cadence
// loose avoids competing with the heavier dashboard polls.
export function useCoverage({ enabled = true }: Params = {}) {
  return useQuery<CoverageResponse>({
    queryKey: ['coverage'],
    queryFn: () => api.getCoverage(),
    enabled,
    refetchInterval: 60_000,
    staleTime: 30_000,
    // 4xx (e.g. cluster disconnected) shouldn't retry — the parent
    // component re-mounts and re-fires on its own when the cluster
    // comes back online via the cluster:connected WS event.
    retry: (failureCount, err) => {
      if (err instanceof ApiError && err.status >= 400 && err.status < 500) return false
      return failureCount < 2
    },
  })
}
