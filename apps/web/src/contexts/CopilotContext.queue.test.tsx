import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import type { CopilotStreamEvent } from '@/services/copilot/types'

// Found live: a question that found Kobi's turns full read as Investigating
// for 15 s, then counted down and asked again, and «Kobi is busy» came after
// about 40 s. The server now keeps it in line inside the stream: its first
// event says so, and the refusal arrives as an error of kind busy.

vi.mock('@/contexts/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'userA' }, isLoading: false, sessionReady: true, isAuthEnabled: true, isAuthenticated: true }),
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
vi.mock('@/services/api', () => ({
  api: {
    getConversation: () => Promise.reject(new Error('404')),
    getCopilotConfig: () => Promise.resolve({ enabled: true, provider: 'anthropic', model: 'claude' }),
    listClusters: () => Promise.resolve([]),
    patchConversation: vi.fn(),
    listConversations: vi.fn(),
    deleteConversation: vi.fn(),
  },
}))

let release: () => void = () => {}
const sendCopilotChat = vi.fn(async function* (): AsyncGenerator<CopilotStreamEvent> {
  yield { type: 'queued', limit: 'person', max: 3, waitSeconds: 30 }
  await new Promise<void>((r) => (release = r))
  yield {
    type: 'error',
    kind: 'busy',
    error: 'You already have 3 questions running with Kobi, the most one person can have at once. Ask again when one of them finishes, or stop one.',
  }
  yield { type: 'done' }
})
vi.mock('@/services/copilot/chat', async (orig) => ({
  ...(await orig<typeof import('@/services/copilot/chat')>()),
  sendCopilotChat: () => sendCopilotChat(),
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
  qc.setQueryData(['clusters'], [{ context: 'cluster-a', active: true }] as never)
  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>
      <CopilotProvider>{children}</CopilotProvider>
    </QueryClientProvider>
  )
  return renderHook(() => useCopilot(), { wrapper })
}

describe('a question that finds the turns full', () => {
  it('waits in line from the first event, then ends as busy with the server’s words', async () => {
    const { result } = harness()
    await waitFor(() => expect(result.current.activeClusterContext).toBe('cluster-a'))

    let sent: Promise<void> = Promise.resolve()
    act(() => {
      sent = result.current.sendMessage('why is checkout failing?')
    })
    await waitFor(() => expect(result.current.turnWait).not.toBeNull())
    expect(result.current.turnWait).toMatchObject({ limit: 'person', max: 3 })
    expect(result.current.turnWait!.until).toBeGreaterThan(Date.now() + 25_000)

    await act(async () => {
      release()
      await sent
    })
    expect(result.current.turnWait).toBeNull()
    expect(result.current.isLoading).toBe(false)
    const failure = [...result.current.messages].reverse().find((m) => m.failure)?.failure
    expect(failure?.kind).toBe('busy')
    expect(failure?.message).toMatch(/^You already have 3 questions running/)
  })
})
