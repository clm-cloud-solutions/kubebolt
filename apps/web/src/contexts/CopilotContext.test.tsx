import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

// Locks the cross-user privacy fix: the CopilotProvider is mounted at the app
// root and does NOT remount on an SPA login, so without an explicit
// user-change reset the previous user's conversation would stay visible to the
// next user who logs in on the same browser. We also key the resume pointer by
// user id so two users never resume each other's conversation.

// Controllable mocked auth user (mutated between rerenders to simulate a
// logout → login as a different user).
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
    patchConversation: vi.fn(),
    listConversations: vi.fn(),
    deleteConversation: vi.fn(),
  },
}))

let useCopilot: typeof import('./CopilotContext').useCopilot
let CopilotProvider: typeof import('./CopilotContext').CopilotProvider

const POINTER = 'kubebolt-copilot-active-conversation'

function makeWrapper() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <CopilotProvider>{children}</CopilotProvider>
      </QueryClientProvider>
    )
  }
}

beforeEach(async () => {
  vi.clearAllMocks()
  localStorage.clear()
  authUser = { id: 'userA' }
  getCopilotConfig.mockResolvedValue({ enabled: true, provider: 'anthropic', model: 'claude' })
  const mod = await import('./CopilotContext')
  useCopilot = mod.useCopilot
  CopilotProvider = mod.CopilotProvider
})

describe('CopilotContext cross-user isolation', () => {
  it('rehydrates the active user’s conversation from a per-user pointer', async () => {
    localStorage.setItem(`${POINTER}:userA`, 'cA')
    getConversation.mockResolvedValue({
      id: 'cA',
      clusterId: 'prod',
      title: 'A conversation',
      updatedAt: new Date().toISOString(),
      messages: [{ role: 'user', content: 'A SECRET' }],
    })

    const { result } = renderHook(() => useCopilot(), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.conversationId).toBe('cA'))
    expect(result.current.messages.some((m) => m.content === 'A SECRET')).toBe(true)
    expect(getConversation).toHaveBeenCalledWith('cA')
  })

  it('clears the previous user’s transcript when the active user changes', async () => {
    localStorage.setItem(`${POINTER}:userA`, 'cA')
    getConversation.mockImplementation((id: string) =>
      id === 'cA'
        ? Promise.resolve({
            id: 'cA',
            clusterId: 'prod',
            title: 'A conversation',
            updatedAt: new Date().toISOString(),
            messages: [{ role: 'user', content: 'A SECRET' }],
          })
        : Promise.reject(new Error('404')),
    )

    const { result, rerender } = renderHook(() => useCopilot(), { wrapper: makeWrapper() })

    // User A's conversation is loaded.
    await waitFor(() => expect(result.current.messages.some((m) => m.content === 'A SECRET')).toBe(true))

    // Simulate logout → login as a different user (no remount, SPA).
    act(() => {
      authUser = { id: 'userB' }
    })
    rerender()

    // User B must NOT see user A's conversation.
    await waitFor(() => expect(result.current.messages.length).toBe(0))
    expect(result.current.conversationId).toBeNull()
    expect(result.current.conversationTitle).toBeNull()
    expect(result.current.messages.some((m) => m.content === 'A SECRET')).toBe(false)
  })

  it('does not reuse one user’s pointer for another user', async () => {
    // Only user A has a saved pointer; user B has none.
    localStorage.setItem(`${POINTER}:userA`, 'cA')
    authUser = { id: 'userB' }
    getConversation.mockRejectedValue(new Error('404'))

    const { result } = renderHook(() => useCopilot(), { wrapper: makeWrapper() })

    // Give the rehydrate effect a chance to run; B has no pointer so nothing
    // is fetched and the transcript stays empty.
    await waitFor(() => expect(getCopilotConfig).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 0))
    expect(getConversation).not.toHaveBeenCalledWith('cA')
    expect(result.current.messages.length).toBe(0)
  })
})
