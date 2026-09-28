import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'

// Locks the cluster-switch handoff: when Kobi offers to switch cluster and
// re-ask, the question must land in a BRAND-NEW conversation belonging to the
// NEW cluster.
//
// The bug this pins: `scopeKey` is derived from the active cluster in the
// ['clusters'] query, so it only moves when that query refetches. Calling
// `clearHistory()` and then `sendMessage()` on the same tick used the callback
// built from the PREVIOUS render — old conversation id, old transcript — and
// posted the new question into the conversation we had just left. The server
// then re-stamped that conversation with the new cluster, so one conversation
// held two clusters' tool results. In the field it showed up as a conversation
// that had "moved" cluster in the history list.

vi.mock('@/contexts/AuthContext', () => ({
  useAuth: () => ({
    user: { id: 'userA' },
    isLoading: false,
    sessionReady: true,
    isAuthEnabled: true,
    isAuthenticated: true,
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
vi.mock('@/services/api', () => ({
  api: {
    getConversation: (id: string) => getConversation(id),
    getCopilotConfig: () =>
      Promise.resolve({ enabled: true, provider: 'anthropic', model: 'claude' }),
    listClusters: () => Promise.resolve([{ context: 'cluster-a', active: true }]),
    patchConversation: vi.fn(),
    listConversations: vi.fn(),
    deleteConversation: vi.fn(),
  },
}))

// Every send is recorded with the transcript and conversation id it carried —
// that pair IS the thing under test.
interface SentTurn {
  messages: { role: string; content: string }[]
  conversationId: string | null | undefined
}
const sent: SentTurn[] = []
vi.mock('@/services/copilot/chat', () => ({
  sendCopilotChat: async function* (
    messages: { role: string; content: string }[],
    _path: string,
    _signal?: AbortSignal,
    _trigger?: string,
    _usage?: unknown,
    conversationId?: string | null,
  ) {
    sent.push({ messages: messages.map((m) => ({ role: m.role, content: m.content })), conversationId })
    yield { type: 'meta', conversationId: conversationId ?? 'server-new-id' }
    yield { type: 'text', text: 'ok' }
    yield { type: 'done' }
  },
  compactCopilotSession: vi.fn(),
}))

let useCopilot: typeof import('./CopilotContext').useCopilot
let CopilotProvider: typeof import('./CopilotContext').CopilotProvider

const BASE = 'kubebolt-copilot-active-conversation'
const pointerKey = (user: string, cluster: string) => `${BASE}:${user}::${cluster}`

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

const convFixture = (id: string, cluster: string, content: string) => ({
  id,
  clusterId: cluster,
  title: `${id} title`,
  updatedAt: new Date().toISOString(),
  messages: [{ role: 'user', content }],
})

beforeEach(async () => {
  vi.clearAllMocks()
  localStorage.clear()
  sent.length = 0
  const mod = await import('./CopilotContext')
  useCopilot = mod.useCopilot
  CopilotProvider = mod.CopilotProvider
})

describe('askInCluster — the cluster-switch handoff', () => {
  it('waits for the switch to land before asking', async () => {
    localStorage.setItem(pointerKey('userA', 'cluster-a'), 'cA')
    getConversation.mockImplementation((id: string) =>
      id === 'cA'
        ? Promise.resolve(convFixture('cA', 'cluster-a', 'CLUSTER A HISTORY'))
        : Promise.reject(new Error('404')),
    )

    const { qc, wrapper } = makeHarness('cluster-a')
    const { result, rerender } = renderHook(() => useCopilot(), { wrapper })
    await waitFor(() => expect(result.current.conversationId).toBe('cA'))

    // The card fires this the instant the switch mutation resolves — BEFORE
    // the ['clusters'] refetch has told the copilot anything.
    act(() => {
      result.current.askInCluster('cluster-b', 'what are the two criticals?')
    })
    rerender()
    // Nothing may go out yet: the copilot still believes it is on cluster-a.
    await new Promise((r) => setTimeout(r, 20))
    expect(sent).toHaveLength(0)

    // Now the refetch lands, exactly as a real switch does.
    act(() => {
      qc.setQueryData(['clusters'], [{ context: 'cluster-b', active: true }] as never)
    })
    rerender()

    await waitFor(() => expect(sent).toHaveLength(1))
  })

  it('asks in a NEW conversation, carrying none of the old cluster transcript', async () => {
    localStorage.setItem(pointerKey('userA', 'cluster-a'), 'cA')
    getConversation.mockImplementation((id: string) =>
      id === 'cA'
        ? Promise.resolve(convFixture('cA', 'cluster-a', 'CLUSTER A HISTORY'))
        : Promise.reject(new Error('404')),
    )

    const { qc, wrapper } = makeHarness('cluster-a')
    const { result, rerender } = renderHook(() => useCopilot(), { wrapper })
    await waitFor(() => expect(result.current.conversationId).toBe('cA'))

    act(() => {
      result.current.askInCluster('cluster-b', 'what are the two criticals?')
    })
    act(() => {
      qc.setQueryData(['clusters'], [{ context: 'cluster-b', active: true }] as never)
    })
    rerender()

    await waitFor(() => expect(sent).toHaveLength(1))
    // Not appended to the conversation we left.
    expect(sent[0].conversationId).toBeNull()
    // And none of that conversation's messages travelled with it.
    expect(sent[0].messages.map((m) => m.content)).toEqual(['what are the two criticals?'])
    // The saved pointer must not still aim at the old conversation, or a
    // refresh would resume cluster-a's transcript under cluster-b.
    expect(localStorage.getItem(pointerKey('userA', 'cluster-b'))).not.toBe('cA')
  })

  it('gives up with an error rather than asking the wrong cluster', async () => {
    vi.useFakeTimers()
    try {
      const { wrapper } = makeHarness('cluster-a')
      const { result, rerender } = renderHook(() => useCopilot(), { wrapper })
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10)
      })

      act(() => {
        result.current.askInCluster('cluster-b', 'what are the two criticals?')
      })
      rerender()
      // The switch never lands.
      await act(async () => {
        await vi.advanceTimersByTimeAsync(25_000)
      })
      rerender()

      expect(sent).toHaveLength(0)
      expect(result.current.error).toMatch(/did not finish/i)
    } finally {
      vi.useRealTimers()
    }
  })

  it('startFresh is what makes it safe — a plain send still continues the open conversation', async () => {
    localStorage.setItem(pointerKey('userA', 'cluster-a'), 'cA')
    getConversation.mockResolvedValue(convFixture('cA', 'cluster-a', 'CLUSTER A HISTORY'))

    const { wrapper } = makeHarness('cluster-a')
    const { result } = renderHook(() => useCopilot(), { wrapper })
    await waitFor(() => expect(result.current.conversationId).toBe('cA'))

    await act(async () => {
      await result.current.sendMessage('a follow-up on this same cluster')
    })

    expect(sent).toHaveLength(1)
    expect(sent[0].conversationId).toBe('cA')
    expect(sent[0].messages.map((m) => m.content)).toEqual([
      'CLUSTER A HISTORY',
      'a follow-up on this same cluster',
    ])
  })
})
