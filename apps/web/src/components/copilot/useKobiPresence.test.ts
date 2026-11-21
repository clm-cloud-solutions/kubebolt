import { describe, it, expect } from 'vitest'
import { kobiPresenceOf } from './useKobiPresence'
import type { CopilotMessage } from '@/services/copilot/types'

const at = new Date()
const prompt = (content: string): CopilotMessage => ({ id: content, role: 'user', content, timestamp: at })

describe('kobiPresenceOf', () => {
  it('watches when nothing is in flight or pending', () => {
    expect(kobiPresenceOf([prompt('hi')], false, [])).toEqual({ state: 'watching', label: 'Watching' })
  })

  it('names the call the turn is on, not one from an earlier turn', () => {
    const messages: CopilotMessage[] = [
      prompt('first'),
      { id: 'a1', role: 'assistant', content: '', timestamp: at, toolCalls: [{ id: 'c1', name: 'get_events', input: {} }] },
      { id: 't1', role: 'user', content: '', timestamp: at, toolResults: [{ toolCallId: 'c1', content: '[]' }] },
      { id: 'a2', role: 'assistant', content: 'done', timestamp: at },
      prompt('second'),
    ]
    // Thinking, before the first call of the new turn: no detail yet.
    expect(kobiPresenceOf(messages, true, [])).toEqual({ state: 'investigating', label: 'Investigating', detail: undefined })
    // Waiting for a free slot: nothing runs yet and nothing is asked of the
    // person, so it is neither Investigating nor Awaiting: Watching, in words.
    expect(kobiPresenceOf(messages, true, [], true)).toEqual({ state: 'watching', label: 'Waiting for a free slot' })

    messages.push({
      id: 'a3',
      role: 'assistant',
      content: '',
      timestamp: at,
      toolCalls: [{ id: 'c2', name: 'get_pod_logs', input: { namespace: 'shop', name: 'catalog-api-f958778f8-vg55l' } }],
    })
    const p = kobiPresenceOf(messages, true, ['get_pod_logs'])
    expect(p.state).toBe('investigating')
    expect(p.detail).toMatch(/^Logs/)
  })

  it("is unavailable while the latest question was refused for the month's credits", () => {
    const refused: CopilotMessage[] = [
      prompt('why is checkout slow?'),
      { id: 'a1', role: 'assistant', content: '', timestamp: at, failure: { message: 'spent', kind: 'credits' } },
    ]
    expect(kobiPresenceOf(refused, false, [])).toEqual({ state: 'unavailable', label: 'Out of AI credits' })
    // Busy or a plain failure leave Kobi available.
    refused[1] = { ...refused[1], failure: { message: 'busy', kind: 'busy' } }
    expect(kobiPresenceOf(refused, false, []).state).toBe('watching')
    // A later question that went through brings it back.
    refused[1] = { ...refused[1], failure: { message: 'spent', kind: 'credits' } }
    refused.push(prompt('again'), { id: 'a2', role: 'assistant', content: 'here it is', timestamp: at })
    expect(kobiPresenceOf(refused, false, []).state).toBe('watching')
  })

  it('awaits while a proposal has no decision, and investigating wins over it', () => {
    const proposal = JSON.stringify({ kind: 'action_proposal', action: 'restart_workload' })
    const messages: CopilotMessage[] = [
      prompt('restart it'),
      { id: 'a1', role: 'assistant', content: '', timestamp: at, toolCalls: [{ id: 'c1', name: 'propose_restart_workload', input: {} }] },
      { id: 't1', role: 'user', content: '', timestamp: at, toolResults: [{ toolCallId: 'c1', content: proposal }] },
    ]
    expect(kobiPresenceOf(messages, false, [])).toEqual({ state: 'awaiting', label: 'Awaiting your approval' })
    expect(kobiPresenceOf(messages, true, []).state).toBe('investigating')

    const decided = JSON.stringify({ kind: 'action_proposal', action: 'restart_workload', executionStatus: 'executed' })
    messages[2] = { ...messages[2], toolResults: [{ toolCallId: 'c1', content: decided }] }
    expect(kobiPresenceOf(messages, false, []).state).toBe('watching')
  })
})
