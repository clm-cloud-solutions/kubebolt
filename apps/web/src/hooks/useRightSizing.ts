import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { ClusterOverview } from '@/types/kubernetes'

// useRightSizing reads the right-sizing recommendations from the backend
// (GET /right-sizing, engine in apps/api/internal/rightsizing).
//
// The rules used to be computed here, in the browser. Kobi's get_right_sizing
// needs the same answer, and two copies in two languages drift — so the
// engine moved to the backend once and every surface reads it: the Capacity
// strip and panel, the Overview efficiency band, and the Cost page. They all
// share this queryKey, so they dedupe into one request.
//
// Rules (per resource, CPU and memory independently) — documented in full in
// the Go package:
//   1. NEAR-LIMIT  P95 ≥ 0.8 × limit (and within 50m / 100Mi of it) → critical
//   2. OVER-PROV   P95 < 0.5 × request, gap above 50m / 100Mi       → warning
//   3. NO-SPECS    no request, no limit, usage above the floor       → info
//
// NOTE on money: the totals are reclaimable cores/bytes, not $/mo. Currency
// needs per-node pricing (OpenCost; useClusterCost converts).

export type Severity = 'critical' | 'warning' | 'info'
export type ResourceState = 'over' | 'near-limit' | 'no-specs' | 'ok'

export interface ResourceFinding {
  request: number
  limit: number
  p95: number
  // 'over' | 'near-limit' | 'no-specs' | 'ok'
  state: ResourceState
  // Recommended new value when state is over/near-limit; 0 otherwise.
  // For 'over' it's the suggested request; for 'near-limit' the
  // suggested limit.
  suggest: number
}

export interface Recommendation {
  namespace: string
  kind: string
  name: string
  severity: Severity
  reason: string
  // CPU values are in millicores; memory in bytes.
  cpu: ResourceFinding
  mem: ResourceFinding
}

export interface RightSizingTotals {
  count: number
  // Σ(request − suggested request) across over-provisioned findings —
  // what the cluster could hand back by applying the recommendations.
  reclaimCpuMilli: number
  reclaimMemBytes: number
}

export interface RightSizingResult {
  recs: Recommendation[]
  totals: RightSizingTotals
  isLoading: boolean
  error: Error | null
  // Days of usage history the P95 window actually spans. The P95 is computed
  // over [7d], but a freshly-connected cluster only has a few hours — so this
  // is what the recommendation is REALLY based on. undefined while loading.
  windowDays?: number
  // True when windowDays < CONFIDENCE_DAYS: the P95 hasn't seen enough daily
  // cycles yet (a low-load snapshot masquerading as a 7d baseline), so the
  // reclaim / savings figures are optimistic and should read as PRELIMINARY
  // rather than something to act on. Surfaced as a badge by consumers.
  preliminary: boolean
}

interface RightSizingResponse {
  recs: Recommendation[]
  totals: RightSizingTotals
  windowDays?: number
  preliminary: boolean
}

const EMPTY_TOTALS: RightSizingTotals = { count: 0, reclaimCpuMilli: 0, reclaimMemBytes: 0 }

// `overview` is no longer read — the backend has the workloads' specs — and
// stays in the signature so the eight consumers do not change.
export function useRightSizing(installed: boolean, _overview?: ClusterOverview): RightSizingResult {
  // The backend caches the P95 computation for 5 minutes per cluster; polling
  // faster would only re-read that cache.
  const q = useQuery({
    queryKey: ['rightsizing'],
    queryFn: () => api.getRightSizing<RightSizingResponse>(),
    staleTime: 5 * 60_000,
    refetchInterval: 5 * 60_000,
    enabled: installed,
    retry: false,
  })
  return {
    recs: q.data?.recs ?? [],
    totals: q.data?.totals ?? EMPTY_TOTALS,
    isLoading: q.isLoading,
    error: (q.error as Error | null) ?? null,
    windowDays: q.data?.windowDays,
    preliminary: q.data?.preliminary ?? false,
  }
}
