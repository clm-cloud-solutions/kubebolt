import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import { livePhase } from '@/services/copilot/liveTurn'
import type { CopilotStreamEvent } from '@/services/copilot/types'

// Locks the live turn: while Kobi works, the transcript is built in the shape
// the server's `done` returns, so each card completes as its call returns and
// nothing moves or remounts when the turn ends.

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

vi.mock('@/services/api', () => ({
  api: {
    getConversation: () => Promise.reject(new Error('404')),
    getCopilotConfig: () => Promise.resolve({ enabled: true, provider: 'anthropic', model: 'claude' }),
    listClusters: () => Promise.resolve([{ context: 'cluster-a', active: true }]),
    patchConversation: vi.fn(),
    listConversations: vi.fn(),
    deleteConversation: vi.fn(),
  },
}))

// The stream the test plays: events, and 'pause' marks where it waits for the
// test to look (release()) — or for the operator to stop it.
type Step = Partial<CopilotStreamEvent> | 'pause'
let script: Step[] = []
let releases: Array<() => void> = []
const release = () => releases.shift()?.()

vi.mock('@/services/copilot/chat', () => ({
  sendCopilotChat: async function* (_m: unknown, _p: string, signal?: AbortSignal) {
    for (const step of script) {
      if (step === 'pause') {
        await new Promise<void>((resolve, reject) => {
          releases.push(resolve)
          signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')))
        })
        continue
      }
      yield step
    }
  },
  compactCopilotSession: vi.fn(),
}))

let useCopilot: typeof import('./CopilotContext').useCopilot
let CopilotProvider: typeof import('./CopilotContext').CopilotProvider

function harness() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(['clusters'], [{ context: 'cluster-a', active: true }] as never)
  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>
      <CopilotProvider>{children}</CopilotProvider>
    </QueryClientProvider>
  )
  return renderHook(() => useCopilot(), { wrapper })
}

const call = { id: 'c1', name: 'get_events', input: { namespace: 'shop' } }
const result = { toolCallId: 'c1', content: '{"items":[]}', status: 'empty', durationMs: 120 }

beforeEach(async () => {
  vi.clearAllMocks()
  localStorage.clear()
  releases = []
  const mod = await import('./CopilotContext')
  useCopilot = mod.useCopilot
  CopilotProvider = mod.CopilotProvider
})

describe('the live turn', () => {
  it('builds each round as done will return it, and keeps it mounted on done', async () => {
    script = [
      { type: 'meta', conversationId: 'conv-1' },
      { type: 'text', text: 'Checking the events.', round: 0 },
      { type: 'tool_call', id: 'c1', toolName: 'get_events', input: { namespace: 'shop' }, round: 0 },
      'pause',
      { type: 'tool_result', id: 'c1', round: 0, status: 'empty', durationMs: 120, content: '{"items":[]}' },
      'pause',
      { type: 'text', text: 'Nothing failed in shop.', round: 1 },
      {
        type: 'done',
        conversationId: 'conv-1',
        turnId: 't1',
        messages: [
          { role: 'user', content: 'why is checkout failing?' },
          { role: 'assistant', content: 'Checking the events.', toolCalls: [call] },
          { role: 'user', content: '', toolResults: [result] },
          { role: 'assistant', content: 'Nothing failed in shop.', turnId: 't1' },
        ],
      },
    ]
    const { result: copilot } = harness()
    await waitFor(() => expect(copilot.current.isLoading).toBe(false))

    act(() => {
      void copilot.current.sendMessage('why is checkout failing?')
    })

    // The call is running: its round's text and its card, no result yet.
    await waitFor(() => expect(releases).toHaveLength(1))
    let m = copilot.current.messages
    expect(m.map((x) => x.role)).toEqual(['user', 'assistant'])
    expect(m[1].content).toBe('Checking the events.')
    expect(m[1].toolCalls).toEqual([call])
    expect(livePhase(m)).toBe('tools')

    act(() => release())
    // The result is in: the card completes; the model is on its next round.
    await waitFor(() => expect(releases).toHaveLength(1))
    m = copilot.current.messages
    expect(m.map((x) => x.role)).toEqual(['user', 'assistant', 'user'])
    expect(m[2].toolResults).toEqual([{ ...result, isError: undefined }])
    expect(livePhase(m)).toBe('model')
    const idsBeforeDone = m.map((x) => x.id)

    act(() => release())
    await waitFor(() => expect(copilot.current.isLoading).toBe(false))
    m = copilot.current.messages
    // Same shape, same ids: the cards on screen stay where they are.
    expect(m.map((x) => x.role)).toEqual(['user', 'assistant', 'user', 'assistant'])
    expect(m.slice(0, 3).map((x) => x.id)).toEqual(idsBeforeDone)
    expect(m[3].content).toBe('Nothing failed in shop.')
    expect(m[3].turnId).toBe('t1')
  })

  it('marks where the operator stopped, under the cards already in', async () => {
    script = [
      { type: 'tool_call', id: 'c1', toolName: 'get_events', input: { namespace: 'shop' }, round: 0 },
      { type: 'tool_result', id: 'c1', round: 0, status: 'empty', durationMs: 120, content: '{"items":[]}' },
      'pause',
    ]
    const { result: copilot } = harness()
    await waitFor(() => expect(copilot.current.isLoading).toBe(false))

    act(() => {
      void copilot.current.sendMessage('why is checkout failing?')
    })
    await waitFor(() => expect(releases).toHaveLength(1))
    act(() => copilot.current.cancelMessage())
    await waitFor(() => expect(copilot.current.isLoading).toBe(false))

    const m = copilot.current.messages
    expect(m.map((x) => x.role)).toEqual(['user', 'assistant', 'user', 'assistant'])
    expect(m[3]).toMatchObject({ content: '', cancelled: true })
    expect(copilot.current.error).toBeNull()
  })

  it('ends a failed question with a row that says why, not a banner', async () => {
    script = [
      { type: 'tool_call', id: 'c1', toolName: 'get_events', input: { namespace: 'shop' }, round: 0 },
      { type: 'tool_result', id: 'c1', round: 0, status: 'ok', durationMs: 90, content: '{"items":[]}' },
      { type: 'error', error: 'The AI provider is overloaded. Try again in a minute.' },
      { type: 'done' },
    ]
    const { result: copilot } = harness()
    await waitFor(() => expect(copilot.current.isLoading).toBe(false))

    await act(() => copilot.current.sendMessage('why is checkout failing?'))

    const m = copilot.current.messages
    expect(m.map((x) => x.role)).toEqual(['user', 'assistant', 'user', 'assistant'])
    expect(m[3].failure?.message).toBe('The AI provider is overloaded. Try again in a minute.')
    expect(m[3].failure?.afterMs).toBeGreaterThanOrEqual(0)
    expect(copilot.current.error).toBeNull()
  })

  it('names the escalation model once the conversation leaves the cluster, and keeps it', async () => {
    script = [
      { type: 'meta', conversationId: 'conv-1' },
      { type: 'tool_call', id: 'c1', toolName: 'mcp__aws__call', input: { service: 'ec2', operation: 'DescribeRouteTables' }, round: 0 },
      { type: 'meta', escalated: 'claude-sonnet-5-5' },
      { type: 'tool_result', id: 'c1', round: 0, status: 'ok', durationMs: 900, content: '{}' },
      { type: 'text', text: 'Done.', round: 1 },
      { type: 'done', conversationId: 'conv-1', turnId: 't1', messages: [] },
    ]
    const { result: copilot } = harness()
    await waitFor(() => expect(copilot.current.isLoading).toBe(false))
    expect(copilot.current.escalatedModel).toBeNull()

    act(() => {
      void copilot.current.sendMessage('why?')
    })
    await waitFor(() => expect(copilot.current.isLoading).toBe(false))
    expect(copilot.current.escalatedModel).toBe('claude-sonnet-5-5')

    // A later question in the same conversation keeps it while it runs.
    script = [{ type: 'text', text: 'Still.', round: 0 }, 'pause']
    act(() => {
      void copilot.current.sendMessage('and now?')
    })
    await waitFor(() => expect(releases).toHaveLength(1))
    expect(copilot.current.escalatedModel).toBe('claude-sonnet-5-5')
    act(() => copilot.current.cancelMessage())
    await waitFor(() => expect(copilot.current.isLoading).toBe(false))

    act(() => copilot.current.newConversation())
    expect(copilot.current.escalatedModel).toBeNull()
  })
})
