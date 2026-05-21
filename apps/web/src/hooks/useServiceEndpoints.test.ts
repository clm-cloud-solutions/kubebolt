import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

// Tests for the dual-collector (modern kube_endpointslice_* + legacy
// kube_endpoint_*) merge logic in useServiceEndpoints. The legacy
// fallback was added after the yagan-eks-prod-v2 install validation
// surfaced that kube-prometheus-stack's default KSM config emits only
// the legacy collector — without the fallback, every Service in the
// UI rendered as "down" because the modern query returned empty.
//
// The risk we're locking in:
//   1. Modern-only clusters work (the original 1.10.x behavior).
//   2. Legacy-only clusters now work (the yagan-eks-prod-v2 case).
//   3. When both collectors are enabled, modern wins (long-term
//      k8s API shape; EndpointSlice is more granular per-condition).
//   4. The slice → svc hash-strip handles controller-generated
//      slices but doesn't mangle other names.
//   5. An all-empty cluster (no KSM at all) returns an empty map
//      cleanly so consumers can render "no data" instead of crashing.

const apiMock = {
  queryMetrics: vi.fn(),
}

vi.mock('@/services/api', () => ({
  api: {
    queryMetrics: (args: unknown) => apiMock.queryMetrics(args),
  },
}))

// Lazy import after mock is registered.
let useServiceEndpoints: typeof import('./useServiceEndpoints').useServiceEndpoints

beforeEach(async () => {
  apiMock.queryMetrics.mockReset()
  // Re-import every test to get a clean module-level state.
  const mod = await import('./useServiceEndpoints')
  useServiceEndpoints = mod.useServiceEndpoints
})

// ─── Fixtures ────────────────────────────────────────────────────

// Modern: kube_endpointslice_info — `endpointslice` label includes
// the controller-generated 5-char hash suffix.
const modernInfo = (rows: Array<{ namespace: string; endpointslice: string }>) => ({
  data: {
    result: rows.map((r) => ({
      metric: {
        __name__: 'kube_endpointslice_info',
        namespace: r.namespace,
        endpointslice: r.endpointslice,
      },
    })),
  },
})

// Modern: sum by (namespace, endpointslice, ready) (kube_endpointslice_endpoints)
const modernCounts = (
  rows: Array<{ namespace: string; endpointslice: string; ready: 'true' | 'false'; value: number }>,
) => ({
  data: {
    result: rows.map((r) => ({
      metric: {
        namespace: r.namespace,
        endpointslice: r.endpointslice,
        ready: r.ready,
      },
      value: [0, String(r.value)],
    })),
  },
})

// Legacy: kube_endpoint_info — `endpoint` label is the Service name (1:1, no hash).
const legacyInfo = (rows: Array<{ namespace: string; endpoint: string }>) => ({
  data: {
    result: rows.map((r) => ({
      metric: { __name__: 'kube_endpoint_info', namespace: r.namespace, endpoint: r.endpoint },
    })),
  },
})

const legacyCounts = (
  rows: Array<{ namespace: string; endpoint: string; ready: 'true' | 'false'; value: number }>,
) => ({
  data: {
    result: rows.map((r) => ({
      metric: { namespace: r.namespace, endpoint: r.endpoint, ready: r.ready },
      value: [0, String(r.value)],
    })),
  },
})

// queueResponses primes the mock to return one fixture per call in order:
// [sliceInfo, sliceCounts, endpointInfo, endpointCounts]
function queueResponses(
  sliceInfo: unknown,
  sliceCounts: unknown,
  endpointInfo: unknown,
  endpointCounts: unknown,
) {
  apiMock.queryMetrics
    .mockResolvedValueOnce(sliceInfo)
    .mockResolvedValueOnce(sliceCounts)
    .mockResolvedValueOnce(endpointInfo)
    .mockResolvedValueOnce(endpointCounts)
}

// Render-hook wrapper with a per-test QueryClient (retry off, cache disabled).
function wrapper({ children }: { children: React.ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0, staleTime: 0 } },
  })
  return React.createElement(QueryClientProvider, { client: qc }, children)
}

// ─── Tests ───────────────────────────────────────────────────────

describe('useServiceEndpoints — dual-collector merge', () => {
  it('modern-only cluster: populates map from kube_endpointslice_*', async () => {
    queueResponses(
      modernInfo([
        { namespace: 'default', endpointslice: 'web-abc12' },
        { namespace: 'default', endpointslice: 'api-9zyx3' },
      ]),
      modernCounts([
        { namespace: 'default', endpointslice: 'web-abc12', ready: 'true', value: 3 },
        { namespace: 'default', endpointslice: 'web-abc12', ready: 'false', value: 1 },
        { namespace: 'default', endpointslice: 'api-9zyx3', ready: 'true', value: 2 },
      ]),
      legacyInfo([]),
      legacyCounts([]),
    )

    const { result } = renderHook(() => useServiceEndpoints(), { wrapper })
    await waitFor(() => expect(result.current.anyData).toBe(true))

    // Hash suffix stripped — `web-abc12` → `web`, `api-9zyx3` → `api`
    expect(result.current.endpoints['default/web']).toEqual({ ready: 3, notReady: 1, total: 4 })
    expect(result.current.endpoints['default/api']).toEqual({ ready: 2, notReady: 0, total: 2 })
  })

  it('legacy-only cluster: populates map from kube_endpoint_* (yagan-eks-prod-v2 case)', async () => {
    queueResponses(
      modernInfo([]),
      modernCounts([]),
      legacyInfo([
        { namespace: 'monitoring', endpoint: 'alertmanager-operated' },
        { namespace: 'argocd', endpoint: 'argo-argocd-applicationset-controller' },
      ]),
      legacyCounts([
        {
          namespace: 'monitoring',
          endpoint: 'alertmanager-operated',
          ready: 'true',
          value: 3,
        },
        {
          namespace: 'argocd',
          endpoint: 'argo-argocd-applicationset-controller',
          ready: 'true',
          value: 1,
        },
      ]),
    )

    const { result } = renderHook(() => useServiceEndpoints(), { wrapper })
    await waitFor(() => expect(result.current.anyData).toBe(true))

    // Endpoint name == Service name; no hash to strip.
    expect(result.current.endpoints['monitoring/alertmanager-operated']).toEqual({
      ready: 3,
      notReady: 0,
      total: 3,
    })
    expect(result.current.endpoints['argocd/argo-argocd-applicationset-controller']).toEqual({
      ready: 1,
      notReady: 0,
      total: 1,
    })
  })

  it('both collectors enabled: modern wins on overlap, legacy fills gaps', async () => {
    queueResponses(
      modernInfo([{ namespace: 'default', endpointslice: 'web-abc12' }]),
      modernCounts([
        { namespace: 'default', endpointslice: 'web-abc12', ready: 'true', value: 5 },
      ]),
      legacyInfo([
        // Legacy ALSO lists 'web' — modern's value (5) must win.
        { namespace: 'default', endpoint: 'web' },
        // Legacy ONLY lists 'orphan-svc' — modern didn't cover it, legacy fills.
        { namespace: 'default', endpoint: 'orphan-svc' },
      ]),
      legacyCounts([
        // Legacy says 'web' has 99 ready — should be IGNORED (modern wins).
        { namespace: 'default', endpoint: 'web', ready: 'true', value: 99 },
        { namespace: 'default', endpoint: 'orphan-svc', ready: 'true', value: 2 },
      ]),
    )

    const { result } = renderHook(() => useServiceEndpoints(), { wrapper })
    await waitFor(() => expect(result.current.anyData).toBe(true))

    // Modern's value (5) wins — NOT 99.
    expect(result.current.endpoints['default/web']).toEqual({ ready: 5, notReady: 0, total: 5 })
    // Legacy-only entry filled in.
    expect(result.current.endpoints['default/orphan-svc']).toEqual({
      ready: 2,
      notReady: 0,
      total: 2,
    })
  })

  it('zero-endpoint Service registers as 0/0 (not missing from map)', async () => {
    // info reports the Service exists; counts row is absent because
    // the selector matches no pods → no `kube_endpoint_address` series.
    queueResponses(
      modernInfo([]),
      modernCounts([]),
      legacyInfo([{ namespace: 'default', endpoint: 'lonely-svc' }]),
      legacyCounts([]),
    )

    const { result } = renderHook(() => useServiceEndpoints(), { wrapper })
    await waitFor(() => expect(result.current.isLoading).toBe(false))

    // The Service appears in the map but with zero counts —
    // EndpointHealthCell renders this as 'down' (intentional alarm).
    expect(result.current.endpoints['default/lonely-svc']).toEqual({
      ready: 0,
      notReady: 0,
      total: 0,
    })
  })

  it('all queries empty (cluster without KSM): returns empty map gracefully', async () => {
    queueResponses(modernInfo([]), modernCounts([]), legacyInfo([]), legacyCounts([]))

    const { result } = renderHook(() => useServiceEndpoints(), { wrapper })
    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(result.current.endpoints).toEqual({})
    expect(result.current.anyData).toBe(false)
  })

  it('hash-strip leaves non-controller slices unchanged', async () => {
    // A user-created EndpointSlice named without the controller hash
    // shape (e.g. just "custom") should pass through the strip
    // unchanged. We don't make assumptions about its mapping to a
    // Service — it just won't match a Service row, which is correct.
    queueResponses(
      modernInfo([
        { namespace: 'default', endpointslice: 'web-abc12' },
        // 4-char suffix doesn't match the 5-char regex → name kept as-is.
        { namespace: 'default', endpointslice: 'short-abc1' },
        // No suffix at all → kept as-is.
        { namespace: 'default', endpointslice: 'custom' },
      ]),
      modernCounts([]),
      legacyInfo([]),
      legacyCounts([]),
    )

    const { result } = renderHook(() => useServiceEndpoints(), { wrapper })
    await waitFor(() => expect(result.current.anyData).toBe(true))

    expect(result.current.endpoints['default/web']).toBeDefined() // hash stripped
    expect(result.current.endpoints['default/short-abc1']).toBeDefined() // 4-char survives
    expect(result.current.endpoints['default/custom']).toBeDefined() // no-suffix survives
  })

  it('issues exactly 4 queries (parallel, one per family × shape)', async () => {
    queueResponses(modernInfo([]), modernCounts([]), legacyInfo([]), legacyCounts([]))

    const { result } = renderHook(() => useServiceEndpoints(), { wrapper })
    await waitFor(() => expect(result.current.isLoading).toBe(false))

    expect(apiMock.queryMetrics).toHaveBeenCalledTimes(4)
    const queries = apiMock.queryMetrics.mock.calls.map((c) => (c[0] as { query: string }).query)
    expect(queries).toContain('kube_endpointslice_info')
    expect(queries).toContain('sum by (namespace, endpointslice, ready) (kube_endpointslice_endpoints)')
    expect(queries).toContain('kube_endpoint_info')
    expect(queries).toContain('sum by (namespace, endpoint, ready) (kube_endpoint_address)')
  })
})
