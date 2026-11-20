import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

// Finding #81: Home lists the person's latest conversations from every
// cluster. One from another cluster must open THERE — resumed only once the
// copilot's scope reached that cluster, or the scope change would wipe it and
// the next question would run against the cluster being left.

vi.mock('@/contexts/AuthContext', () => ({
  useAuth: () => ({
    user: { id: 'userA' },
    isLoading: false,
    sessionReady: true,
    isAuthEnabled: true,
    isAuthenticated: true,
  }),
}))

vi.mock('react-router-dom', () => ({ useLocation: () => ({ pathname: '/home' }) }))

vi.mock('@/hooks/useCopilotLayout', () => ({
  useCopilotLayout: () => ({
    layout: { mode: 'docked', dockedWidth: 400, floatingWidth: 400, floatingHeight: 600 },
    toggleMode: vi.fn(),
    setDockedWidth: vi.fn(),
    setFloatingSize: vi.fn(),
  }),
}))

const getConversation = vi.fn((id: string) =>
  id === 'conv-b'
    ? Promise.resolve({
        id: 'conv-b',
        title: 'Why is checkout slow?',
        clusterId: 'cluster-b',
        messages: [{ role: 'user', content: 'Why is checkout slow?' }],
        createdAt: '2026-11-13T09:00:00Z',
        updatedAt: '2026-11-13T09:05:00Z',
      })
    : Promise.reject(new Error('404')),
)

vi.mock('@/services/api', () => ({
  api: {
    getConversation: (id: string) => getConversation(id),
    getCopilotConfig: () => Promise.resolve({ enabled: true, provider: 'anthropic', model: 'claude' }),
    listClusters: () => Promise.resolve([]),
    patchConversation: vi.fn(),
    listConversations: vi.fn(),
    deleteConversation: vi.fn(),
  },
}))

vi.mock('@/services/copilot/chat', () => ({
  sendCopilotChat: vi.fn(),
  compactCopilotSession: vi.fn(),
}))

let useCopilot: typeof import('./CopilotContext').useCopilot
let CopilotProvider: typeof import('./CopilotContext').CopilotProvider

beforeEach(async () => {
  vi.clearAllMocks()
  localStorage.clear()
  const mod = await import('./CopilotContext')
  useCopilot = mod.useCopilot
  CopilotProvider = mod.CopilotProvider
})

function harness() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  qc.setQueryData(['clusters'], [
    { context: 'cluster-a', active: true },
    { context: 'cluster-b', active: false },
  ] as never)
  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>
      <CopilotProvider>{children}</CopilotProvider>
    </QueryClientProvider>
  )
  return { qc, ...renderHook(() => useCopilot(), { wrapper }) }
}

describe('resumeInCluster', () => {
  it('waits for the switch to land, then opens the conversation on its cluster', async () => {
    const { qc, result } = harness()
    await waitFor(() => expect(result.current.activeClusterContext).toBe('cluster-a'))

    act(() => result.current.resumeInCluster('cluster-b', 'conv-b'))
    // Still on cluster A: nothing is resumed into the wrong scope.
    await new Promise((r) => setTimeout(r, 30))
    expect(getConversation).not.toHaveBeenCalledWith('conv-b')
    expect(result.current.conversationId).toBeNull()

    // The switch lands.
    act(() => {
      qc.setQueryData(['clusters'], [
        { context: 'cluster-a', active: false },
        { context: 'cluster-b', active: true },
      ] as never)
    })
    await waitFor(() => expect(result.current.conversationId).toBe('conv-b'))
    expect(result.current.activeClusterContext).toBe('cluster-b')
    expect(result.current.isOpen).toBe(true)
  })
})
