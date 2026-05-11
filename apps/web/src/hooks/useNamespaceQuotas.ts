import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useNamespaceQuotas reads ResourceQuota usage from VictoriaMetrics
// (kube-state-metrics scrape) and reduces it to a per-namespace
// summary. Returned shape is keyed by namespace so consumers
// (NamespaceTile, NamespacesPage card) can do a single map lookup.
//
// We intentionally do ONE bare query (`kube_resourcequota`) and join
// `type=used` / `type=hard` rows client-side. That's a hair more JS
// but a single VM round-trip and lets the consumer decide which
// resources to surface — versus running a `used / on(...) hard`
// query per namespace, which would N×fanout the request rate.
//
// The metric is optional: clusters without ResourceQuotas applied
// emit zero series, the hook returns an empty map, and consumers
// gate their UI on `anyQuotas`. Same gated-signal pattern as
// useHubbleAvailable (P25-01) — no backdrop noise on clusters that
// don't use the feature.

export interface QuotaResource {
  resource: string
  used: number
  hard: number
  pct: number
}

export interface NamespaceQuotaSummary {
  namespace: string
  quotaName: string
  items: QuotaResource[]
  maxPct: number
  maxResource: string
}

export type NamespaceQuotasMap = Record<string, NamespaceQuotaSummary>

interface QueryEntry {
  ns: string
  rq: string
  resource: string
  used?: number
  hard?: number
}

export function useNamespaceQuotas() {
  const q = useQuery({
    queryKey: ['namespace-quotas'],
    queryFn: () => api.queryMetrics({ query: 'kube_resourcequota' }),
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry: false,
  })

  // Stage 1: collapse the flat metric series into (ns, rq, resource)
  // tuples carrying both `used` and `hard` values.
  const byKey = new Map<string, QueryEntry>()
  for (const series of q.data?.data?.result ?? []) {
    const m = series.metric as Record<string, string>
    const ns = m.namespace
    const rq = m.resourcequota
    const type = m.type
    const resource = m.resource
    if (!ns || !rq || !type || !resource) continue
    const key = `${ns}|${rq}|${resource}`
    const v = parseFloat(series.value?.[1] ?? '0')
    if (!isFinite(v)) continue
    let entry = byKey.get(key)
    if (!entry) {
      entry = { ns, rq, resource }
      byKey.set(key, entry)
    }
    if (type === 'used') entry.used = v
    else if (type === 'hard') entry.hard = v
  }

  // Stage 2: derive per-namespace summary. A namespace can have
  // multiple ResourceQuotas (rare but legal); we collapse them into
  // one summary, taking the worst max across all RQs. The quotaName
  // shown in the UI prefers the RQ that owns the most-constrained
  // resource, so the user sees the actually-relevant binding.
  const map: NamespaceQuotasMap = {}
  for (const entry of byKey.values()) {
    if (entry.used == null || entry.hard == null) continue
    if (entry.hard <= 0) continue
    const pct = Math.min(100, (entry.used / entry.hard) * 100)
    const item: QuotaResource = {
      resource: entry.resource,
      used: entry.used,
      hard: entry.hard,
      pct,
    }
    let s = map[entry.ns]
    if (!s) {
      s = {
        namespace: entry.ns,
        quotaName: entry.rq,
        items: [],
        maxPct: 0,
        maxResource: entry.resource,
      }
      map[entry.ns] = s
    }
    s.items.push(item)
    if (pct > s.maxPct) {
      s.maxPct = pct
      s.maxResource = entry.resource
      s.quotaName = entry.rq
    }
  }

  // Stage 3: sort items so the tooltip lists worst-first.
  for (const s of Object.values(map)) {
    s.items.sort((a, b) => b.pct - a.pct)
  }

  return {
    quotas: map,
    anyQuotas: Object.keys(map).length > 0,
    isLoading: q.isLoading,
  }
}

// formatQuotaValue humanizes a quota resource value based on the
// resource name. ResourceQuota uses three numeric conventions:
//   - CPU resources (`*.cpu`):    cores (e.g. 1.1 == 1100m)
//   - Memory/storage resources:   bytes
//   - Counts (pods, services...): integer
// The output keeps two-decimal precision only when needed, so values
// like `7 / 20` stay clean and don't render as `7.00 / 20.00`.
export function formatQuotaValue(resource: string, val: number): string {
  if (resource.endsWith('.cpu') || resource === 'cpu') {
    if (val >= 1) return val.toFixed(val % 1 === 0 ? 0 : 2).replace(/\.?0+$/, '')
    return `${Math.round(val * 1000)}m`
  }
  if (
    resource.endsWith('.memory') ||
    resource === 'memory' ||
    resource.endsWith('.storage') ||
    resource === 'storage' ||
    resource === 'requests.ephemeral-storage' ||
    resource === 'limits.ephemeral-storage'
  ) {
    return formatBytes(val)
  }
  // Default: integer counts. The toFixed(0) guards against floating
  // edge cases from VM (e.g. `services` arriving as 2.0).
  return val.toFixed(0)
}

function formatBytes(b: number): string {
  if (b >= 1024 ** 3) return `${(b / 1024 ** 3).toFixed(b >= 10 * 1024 ** 3 ? 0 : 1)}Gi`
  if (b >= 1024 ** 2) return `${(b / 1024 ** 2).toFixed(b >= 10 * 1024 ** 2 ? 0 : 1)}Mi`
  if (b >= 1024) return `${(b / 1024).toFixed(0)}Ki`
  return `${b}`
}
