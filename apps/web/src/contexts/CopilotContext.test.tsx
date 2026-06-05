import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

// Locks the cross-scope privacy/isolation fixes for Kobi. The CopilotProvider
// is mounted at the app root and does NOT remount on an SPA login or a cluster
// switch, so without an explicit scope reset the previous scope's conversation
// would stay visible:
//   - a different USER logging in on the same browser, and
//   - the same user switching to a different CLUSTER.
// The resume pointer is keyed by (user, cluster); state resets when either
// changes; the new scope rehydrates its own pointer.

let authUser: { id: string } | null = { id: 'userA' }

vi.mock('@/contexts/AuthContext', () => ({
  useAuth: () => ({
    user: authUser,
    isLoading: false,
    isAuthEnabled: true,
    isAuthenticated: !!authUser,
  }),
}))

vi.mock('react-router-dom', () => ({ useLocation: () => ({ pathname: '/' }) }))

vi.mock('@/hooks/useCopilotLayout', () => ({
  useCopilotLayout: () => ({
    layout: { mode: 'docked', dockedWidth: 400, floatingWidth: 400, floatingHeight: 600 },
    toggleMode: vi.fn(),
    setDockedWidth: vi.fn(),
    setFloatingSize: vi.fn(),
  }),
}))

const getConversation = vi.fn()
const getCopilotConfig = vi.fn()
vi.mock('@/services/api', () => ({
  api: {
    getConversation: (id: string) => getConversation(id),
    getCopilotConfig: () => getCopilotConfig(),
    listClusters: () => Promise.resolve([{ context: 'cluster-a', active: true }]),
    patchConversation: vi.fn(),
    listConversations: vi.fn(),
    deleteConversation: vi.fn(),
  },
}))

let useCopilot: typeof import('./CopilotContext').useCopilot
let CopilotProvider: typeof import('./CopilotContext').CopilotProvider

const BASE = 'kubebolt-copilot-active-conversation'
const pointerKey = (user: string, cluster: string) => `${BASE}:${user}::${cluster}`

// makeHarness creates a QueryClient pre-seeded with an active cluster, so the
// test controls the active cluster directly (a real cluster switch invalidates
// the ['clusters'] query; here we setQueryData to the same effect).
function makeHarness(activeCluster: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(['clusters'], [{ context: activeCluster, active: true }] as never)
  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>
      <CopilotProvider>{children}</CopilotProvider>
    </QueryClientProvider>
  )
  return { qc, wrapper }
}

const convFixture = (id: string, cluster: string, secret: string) => ({
  id,
  clusterId: cluster,
  title: `${id} title`,
  updatedAt: new Date().toISOString(),
  messages: [{ role: 'user', content: secret }],
})

beforeEach(async () => {
  vi.clearAllMocks()
  localStorage.clear()
  authUser = { id: 'userA' }
  getCopilotConfig.mockResolvedValue({ enabled: true, provider: 'anthropic', model: 'claude' })
  const mod = await import('./CopilotContext')
  useCopilot = mod.useCopilot
  CopilotProvider = mod.CopilotProvider
})

describe('CopilotContext scope isolation', () => {
  it('rehydrates the active (user, cluster) conversation from its pointer', async () => {
    localStorage.setItem(pointerKey('userA', 'cluster-a'), 'cA')
    getConversation.mockResolvedValue(convFixture('cA', 'cluster-a', 'A SECRET'))

    const { wrapper } = makeHarness('cluster-a')
    const { result } = renderHook(() => useCopilot(), { wrapper })

    await waitFor(() => expect(result.current.conversationId).toBe('cA'))
    expect(result.current.messages.some((m) => m.content === 'A SECRET')).toBe(true)
    expect(result.current.activeClusterContext).toBe('cluster-a')
  })

  it('clears the transcript when the active USER changes', async () => {
    localStorage.setItem(pointerKey('userA', 'cluster-a'), 'cA')
    getConversation.mockImplementation((id: string) =>
      id === 'cA' ? Promise.resolve(convFixture('cA', 'cluster-a', 'A SECRET')) : Promise.reject(new Error('404')),
    )

    const { wrapper } = makeHarness('cluster-a')
    const { result, rerender } = renderHook(() => useCopilot(), { wrapper })
    await waitFor(() => expect(result.current.messages.some((m) => m.content === 'A SECRET')).toBe(true))

    act(() => {
      authUser = { id: 'userB' }
    })
    rerender()

    await waitFor(() => expect(result.current.messages.length).toBe(0))
    expect(result.current.conversationId).toBeNull()
    expect(result.current.messages.some((m) => m.content === 'A SECRET')).toBe(false)
  })

  it('clears the transcript when the active CLUSTER changes (and resumes per-cluster)', async () => {
    // userA has a conversation in cluster-a but NONE in cluster-b.
    localStorage.setItem(pointerKey('userA', 'cluster-a'), 'cA')
    getConversation.mockImplementation((id: string) =>
      id === 'cA' ? Promise.resolve(convFixture('cA', 'cluster-a', 'CLUSTER A SECRET')) : Promise.reject(new Error('404')),
    )

    const { qc, wrapper } = makeHarness('cluster-a')
    const { result, rerender } = renderHook(() => useCopilot(), { wrapper })

    // Cluster A's conversation is loaded.
    await waitFor(() => expect(result.current.messages.some((m) => m.content === 'CLUSTER A SECRET')).toBe(true))

    // Switch to cluster-b (no conversation there).
    act(() => {
      qc.setQueryData(['clusters'], [{ context: 'cluster-b', active: true }] as never)
    })
    rerender()

    await waitFor(() => expect(result.current.activeClusterContext).toBe('cluster-b'))
    // Must NOT carry cluster A's conversation into cluster B.
    await waitFor(() => expect(result.current.messages.length).toBe(0))
    expect(result.current.conversationId).toBeNull()
    expect(result.current.messages.some((m) => m.content === 'CLUSTER A SECRET')).toBe(false)

    // Switch back to cluster-a → its conversation resumes again.
    act(() => {
      qc.setQueryData(['clusters'], [{ context: 'cluster-a', active: true }] as never)
    })
    rerender()
    await waitFor(() => expect(result.current.messages.some((m) => m.content === 'CLUSTER A SECRET')).toBe(true))
  })
})
