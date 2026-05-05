import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useHubbleAvailable signals whether the cluster has Hubble L7
// metrics actively shipping into VictoriaMetrics. Drives the
// Reliability sub-tab's visibility — we hide the tab entirely when
// L7 isn't there, since an empty Reliability page would be noise.
//
// Detection probe: `count(pod_flow_http_requests_total{source="hubble"})`.
// `count()` over a non-existent metric returns an empty vector (no
// rows), which we read as "no Hubble". A populated vector with a
// positive scalar means at least one series exists. We don't care
// about the exact count — just presence — but reading the value
// avoids false positives on stale series-without-samples.
//
// Cached for 60s with `retry: false`. The signal changes slowly
// (Hubble install / uninstall is a deliberate ops action), so a
// minute-resolution check is plenty. retry-false is important: a
// failing query shouldn't spam VM, and the tab simply stays hidden
// until the next successful check.
export function useHubbleAvailable() {
  const { data, isLoading } = useQuery({
    queryKey: ['hubble-available'],
    queryFn: () =>
      api.queryMetrics({ query: `count(pod_flow_http_requests_total{source="hubble"})` }),
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry: false,
  })

  const result = data?.data?.result ?? []
  const available =
    result.length > 0 && parseFloat(result[0]?.value?.[1] ?? '0') > 0

  return { available, isLoading }
}
