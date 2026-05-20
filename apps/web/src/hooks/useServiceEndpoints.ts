import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useServiceEndpoints reads kube-state-metrics' endpoint counts and
// reduces them to a per-Service summary. Same gated-signal pattern
// as useNamespaceQuotas / useNodeStress: an empty cluster (no KSM)
// returns an empty map; consumers fall back gracefully.
//
// Dual-collector support — KSM offers TWO ways to expose endpoint
// data and different clusters configure different ones:
//
//   1. `kube_endpointslice_*` — modern, keyed on EndpointSlice objects
//      (one per Service after kube-controller-manager splits, named
//      `<svc>-<5-char-hash>`). KSM enables this with
//      `--resources=...,endpointslices,...`. Preferred when present
//      because the EndpointSlice API is the long-term k8s shape.
//
//   2. `kube_endpoint_*` — legacy, keyed on the deprecated Endpoints
//      object (one per Service, name = Service name, no hash strip).
//      KSM enables this with `--resources=...,endpoints,...`.
//
// kube-prometheus-stack's bundled KSM ships with the LEGACY collector
// in its `--resources` flag (deliberate, for backwards-compat with
// existing Grafana dashboards) and does NOT enable endpointslices
// by default. So a stock kube-prometheus-stack install yields only
// `kube_endpoint_*` series, and a hook that queries only the modern
// family returns an empty map — making every Service look "without
// endpoints" in the UI.
//
// We query both and prefer modern when both exist. This makes the
// hook work out-of-the-box on:
//   - clusters running stock prometheus-community KSM with the
//     endpointslices collector (modern path)
//   - clusters running kube-prometheus-stack defaults (legacy path)
//   - clusters that enabled both (modern wins)
//
// Four VM round-trips total, but they fire in parallel via
// Promise.all so wall-clock cost is the slowest single round-trip.
// When a collector isn't enabled, its query returns an empty result
// set in milliseconds and contributes nothing to the merge.

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
      const [sliceInfo, sliceCounts, endpointInfo, endpointCounts] = await Promise.all([
        // Modern (preferred) — EndpointSlice-keyed series.
        api.queryMetrics({ query: 'kube_endpointslice_info' }),
        api.queryMetrics({
          query: 'sum by (namespace, endpointslice, ready) (kube_endpointslice_endpoints)',
        }),
        // Legacy fallback — Endpoints-keyed series. kube-prometheus-
        // stack defaults emit these.
        api.queryMetrics({ query: 'kube_endpoint_info' }),
        api.queryMetrics({
          query: 'sum by (namespace, endpoint, ready) (kube_endpoint_address)',
        }),
      ])
      return { sliceInfo, sliceCounts, endpointInfo, endpointCounts }
    },
    staleTime: 30_000,
    refetchInterval: 30_000,
    retry: false,
  })

  const map: ServiceEndpointsMap = {}
  // Track which keys came from the modern collector so we don't
  // double-count when both collectors are enabled. Modern wins;
  // legacy fills in only for Services modern didn't cover.
  const modernKeys = new Set<string>()

  // ── Pass 1a: modern info → register zero entries (preferred).
  for (const series of q.data?.sliceInfo?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const ns = m.namespace
    const slice = m.endpointslice
    if (!ns || !slice) continue
    const svc = endpointSliceToService(slice)
    const key = serviceKey(ns, svc)
    if (!map[key]) map[key] = { ready: 0, notReady: 0, total: 0 }
    modernKeys.add(key)
  }
  // ── Pass 1b: modern counts → tally ready vs notReady.
  for (const series of q.data?.sliceCounts?.data?.result ?? []) {
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
    modernKeys.add(key)
  }
  // ── Pass 2a: legacy info → register zero entries ONLY for
  // Services the modern pass didn't cover. Endpoint name = Service
  // name (1:1, no hash to strip).
  for (const series of q.data?.endpointInfo?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const ns = m.namespace
    const ep = m.endpoint
    if (!ns || !ep) continue
    const key = serviceKey(ns, ep)
    if (modernKeys.has(key)) continue
    if (!map[key]) map[key] = { ready: 0, notReady: 0, total: 0 }
  }
  // ── Pass 2b: legacy counts → tally ready vs notReady for keys
  // not already populated by the modern pass.
  for (const series of q.data?.endpointCounts?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const ns = m.namespace
    const ep = m.endpoint
    if (!ns || !ep) continue
    const v = parseFloat(series.value?.[1] ?? '0')
    if (!isFinite(v)) continue
    const key = serviceKey(ns, ep)
    if (modernKeys.has(key)) continue
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
