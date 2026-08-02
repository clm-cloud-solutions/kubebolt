import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import { QueryClient } from '@tanstack/react-query'

// The engine has always broadcast insight:new / insight:resolved, but nothing
// consumed them: useWebSocket invalidated clusters, overview, topology and
// resources, never ['insights']. The Insights view therefore relied purely on
// its refetchInterval.
//
// This can NOT be validated by eye. The engine evaluates every 60s
// (--insight-interval), so the broadcast cannot fire sooner than that, and the
// default refetchInterval is 30s — the two paths land within the same window and
// look identical on screen. Attempting it in vivo on 2026-08-02 proved only that
// a human cannot tell them apart. Hence this test.

const handlers = new Set<(e: unknown) => void>()

vi.mock('@/services/websocket', () => ({
  wsManager: {
    connect: vi.fn(),
    subscribe: vi.fn(),
    onMessage: (h: (e: unknown) => void) => {
      handlers.add(h)
      return () => handlers.delete(h)
    },
  },
}))

const { useWebSocket } = await import('./useWebSocket')

let queryClient: QueryClient
let invalidate: ReturnType<typeof vi.spyOn>

vi.mock('@tanstack/react-query', async (orig) => {
  const actual = await orig<typeof import('@tanstack/react-query')>()
  return { ...actual, useQueryClient: () => queryClient }
})

/** Keys passed to invalidateQueries as a plain array of first segments. */
function invalidatedKeys(): string[] {
  return invalidate.mock.calls
    .map((c) => (c[0] as { queryKey?: unknown[] })?.queryKey?.[0])
    .filter((k): k is string => typeof k === 'string')
}

function emit(payload: unknown) {
  handlers.forEach((h) => h(payload))
}

beforeEach(() => {
  handlers.clear()
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  invalidate = vi.spyOn(queryClient, 'invalidateQueries').mockImplementation(() => Promise.resolve())
})

describe('useWebSocket — insight lifecycle reaches the client', () => {
  it.each(['insight:new', 'insight:resolved'])('%s invalidates the insights list', (type) => {
    renderHook(() => useWebSocket(['pods'], 'ctx-1'))

    emit({ type, data: { ruleId: 'liveness-probe-failing', resource: 'Pod/prod/api' } })

    // The list itself, and the overview — it carries InsightCount (the sidebar
    // badge), so leaving it stale makes the badge contradict the list.
    expect(invalidatedKeys()).toEqual(expect.arrayContaining(['insights', 'cluster-overview']))
  })

  it('does not fall through to the resource-detail path', () => {
    // An Insight payload has no .metadata, so without an early return it would
    // reach the debounced overview/topology invalidation and pay for nothing.
    renderHook(() => useWebSocket(['pods'], 'ctx-1'))

    emit({ type: 'insight:resolved', data: { ruleId: 'oom-killed', resource: 'Pod/prod/api' } })

    expect(invalidatedKeys()).not.toContain('topology')
  })

  it('leaves the insights list alone for ordinary resource events', () => {
    // Guards the opposite failure: a cluster under load emits thousands of
    // resource:updated frames (1,449 in 90s while an image-pull pod retried,
    // measured 2026-08-02). Refetching insights on each would be a request storm.
    renderHook(() => useWebSocket(['pods'], 'ctx-1'))

    emit({ type: 'resource:updated', data: { metadata: { namespace: 'prod', name: 'api' } } })

    expect(invalidatedKeys()).not.toContain('insights')
  })
})
