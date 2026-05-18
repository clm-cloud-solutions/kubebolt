import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useServiceEndpoints reads kube-state-metrics' EndpointSlice counts and
// reduces them to a per-Service summary. Same gated-signal pattern
// as useNamespaceQuotas / useNodeStress: an empty cluster (no KSM)
// returns an empty map; consumers fall back gracefully.
//
// KSM 2.10+ removed the legacy `kube_endpoint_*` collector in favour
// of `kube_endpointslice_*` (the EndpointSlice API is the long-term
// shape; legacy Endpoints is frozen). We derive the Service name by
// stripping the controller-generated 5-char hash suffix from the
// endpointslice name — every controller-created EndpointSlice follows
// `<service-name>-<5-char-hash>`. Custom user-created EndpointSlices
// that don't follow this shape fall through the strip unchanged and
// just won't match a Service row, which is the correct behaviour.
//
// Two VM round-trips: one for existence (info) so empty selectors
// still register a 0/0 entry, one for counts (endpoints) grouped
// server-side by (namespace, endpointslice, ready) to keep the
// payload bounded as the cluster grows.

export interface ServiceEndpointSummary {
  ready: number
  notReady: number
  total: number
}

export type ServiceEndpointsMap = Record<string, ServiceEndpointSummary>

// Strip the controller-generated hash suffix to derive the Service
// name from an EndpointSlice name. The hash is 5 chars [a-z0-9]+ for
// kube-controller-manager-created slices.
function endpointSliceToService(slice: string): string {
  return slice.replace(/-[a-z0-9]{5}$/, '')
}

export function useServiceEndpoints() {
  const q = useQuery({
    queryKey: ['service-endpoints'],
    queryFn: async () => {
      const [info, counts] = await Promise.all([
        api.queryMetrics({ query: 'kube_endpointslice_info' }),
        api.queryMetrics({
          query: 'sum by (namespace, endpointslice, ready) (kube_endpointslice_endpoints)',
        }),
      ])
      return { info, counts }
    },
    staleTime: 30_000,
    refetchInterval: 30_000,
    retry: false,
  })

  const map: ServiceEndpointsMap = {}
  // Pass 1: every EndpointSlice that exists registers a zero entry,
  // so a Service whose selector matches no pods (zero endpoint rows)
  // still surfaces as 0/0 instead of being missing from the map.
  for (const series of q.data?.info?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const ns = m.namespace
    const slice = m.endpointslice
    if (!ns || !slice) continue
    const svc = endpointSliceToService(slice)
    const key = serviceKey(ns, svc)
    if (!map[key]) map[key] = { ready: 0, notReady: 0, total: 0 }
  }
  // Pass 2: tally ready vs notReady from the grouped counts.
  for (const series of q.data?.counts?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const ns = m.namespace
    const slice = m.endpointslice
    if (!ns || !slice) continue
    const v = parseFloat(series.value?.[1] ?? '0')
    if (!isFinite(v)) continue
    const svc = endpointSliceToService(slice)
    const key = serviceKey(ns, svc)
    let entry = map[key]
    if (!entry) {
      entry = { ready: 0, notReady: 0, total: 0 }
      map[key] = entry
    }
    if (m.ready === 'true') entry.ready += v
    else entry.notReady += v
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
