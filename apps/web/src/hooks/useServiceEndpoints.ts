import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useServiceEndpoints reads kube-state-metrics' endpoint counts and
// reduces them to a per-Service summary. Same gated-signal pattern
// as useNamespaceQuotas / useNodeStress: an empty cluster (no KSM)
// returns an empty map; consumers fall back gracefully.
//
// Single VM round-trip: a label-set query that pulls available and
// not_ready in one shot via {__name__=~...}, joined client-side.
// Two separate queries would compose the same answer with twice the
// VM hits per refetch.
//
// The KSM metric `kube_endpoint_*` is keyed by (namespace, endpoint)
// where `endpoint` matches the Service name 1:1. We surface ready,
// notReady, and total so consumers can render compact summaries
// (e.g. "2/3" with the missing one flagged) without needing a
// second hook.

export interface ServiceEndpointSummary {
  ready: number
  notReady: number
  total: number
}

export type ServiceEndpointsMap = Record<string, ServiceEndpointSummary>

export function useServiceEndpoints() {
  const q = useQuery({
    queryKey: ['service-endpoints'],
    queryFn: () =>
      api.queryMetrics({
        query: '{__name__=~"kube_endpoint_address_(available|not_ready)"}',
      }),
    staleTime: 30_000,
    refetchInterval: 30_000,
    retry: false,
  })

  const map: ServiceEndpointsMap = {}
  for (const series of q.data?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const ns = m.namespace
    const name = m.endpoint
    if (!ns || !name) continue
    const v = parseFloat(series.value?.[1] ?? '0')
    if (!isFinite(v)) continue
    const key = serviceKey(ns, name)
    let entry = map[key]
    if (!entry) {
      entry = { ready: 0, notReady: 0, total: 0 }
      map[key] = entry
    }
    if (m.__name__ === 'kube_endpoint_address_available') {
      entry.ready = v
    } else if (m.__name__ === 'kube_endpoint_address_not_ready') {
      entry.notReady = v
    }
  }
  for (const entry of Object.values(map)) {
    entry.total = entry.ready + entry.notReady
  }

  return {
    endpoints: map,
    anyData: Object.keys(map).length > 0,
    isLoading: q.isLoading,
  }
}

export function serviceKey(namespace: string, name: string): string {
  return `${namespace}/${name}`
}

// classifyEndpoints maps the (ready, total) pair to a severity tier
// the UI can color-code uniformly. The thresholds:
//   - down:    total > 0 but ready === 0 (configured to serve, but
//              nothing is ready — probable selector drift or every
//              backing pod failing readiness)
//   - partial: ready > 0 && ready < total (some backing pods are
//              not ready — degraded but still serving)
//   - empty:   total === 0 (no endpoint addresses; could be ExternalName,
//              Headless, or just a Service nobody has wired up)
//   - healthy: ready === total, total > 0
//
// Consumers use 'down' / 'partial' as the actionable signals.
// 'empty' is informational (often legit — caller must know the
// Service type to interpret), 'healthy' renders quietly.
export type EndpointHealth = 'healthy' | 'partial' | 'down' | 'empty'

export function classifyEndpoints(s: ServiceEndpointSummary | undefined): EndpointHealth {
  if (!s || s.total === 0) return 'empty'
  if (s.ready === 0) return 'down'
  if (s.ready < s.total) return 'partial'
  return 'healthy'
}
