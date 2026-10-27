import { describe, it, expect } from 'vitest'
import {
  applyLiveText,
  applyLiveToolCall,
  applyLiveToolResult,
  keepIds,
  liveId,
  livePhase,
  markLiveCancelled,
  markLiveFailed,
} from './liveTurn'
import type { CopilotMessage } from './types'

const user: CopilotMessage = { id: 'u0', role: 'user', content: 'why?', timestamp: new Date(0) }
const call = (id: string) => ({ id, name: 'get_events', input: {} })
const result = (id: string) => ({ toolCallId: id, content: '{}', status: 'ok', durationMs: 10 })

describe('liveTurn', () => {
  it('appends each round in order: its text and calls, then its results', () => {
    let m: CopilotMessage[] = [user]
    m = applyLiveText(m, 't', 0, 'Checking.')
    m = applyLiveToolCall(m, 't', 0, call('a'))
    m = applyLiveToolCall(m, 't', 0, call('b'))
    m = applyLiveToolResult(m, 't', 0, result('a'))
    m = applyLiveToolResult(m, 't', 0, result('b'))
    m = applyLiveText(m, 't', 1, 'Done.')
    expect(m.map((x) => [x.id, x.role])).toEqual([
      ['u0', 'user'],
      [liveId('t', 0, 'a'), 'assistant'],
      [liveId('t', 0, 'u'), 'user'],
      [liveId('t', 1, 'a'), 'assistant'],
    ])
    expect(m[1]).toMatchObject({ content: 'Checking.', toolCalls: [call('a'), call('b')] })
    expect(m[2].toolResults).toEqual([result('a'), result('b')])
    expect(m[3].content).toBe('Done.')
  })

  it('appends a second text of the same round (the token-limit note)', () => {
    let m = applyLiveText([user], 't', 0, 'The answer')
    m = applyLiveText(m, 't', 0, ' … [cut]')
    expect(m[1].content).toBe('The answer … [cut]')
  })

  it('knows what the turn is waiting on', () => {
    expect(livePhase([user, { id: 'a', role: 'assistant', content: '', timestamp: new Date() }])).toBe('model')
    let m = applyLiveToolCall([user], 't', 0, call('a'))
    m = applyLiveToolCall(m, 't', 0, call('b'))
    expect(livePhase(m)).toBe('tools')
    m = applyLiveToolResult(m, 't', 0, result('a'))
    expect(livePhase(m)).toBe('tools') // b still runs
    m = applyLiveToolResult(m, 't', 0, result('b'))
    expect(livePhase(m)).toBe('model') // the model reads both
    expect(livePhase(applyLiveText(m, 't', 1, 'Done.'))).toBe('idle')
  })

  it('stops on the round in flight, or after the results on the next one', () => {
    const running = applyLiveToolCall([user], 't', 0, call('a'))
    expect(markLiveCancelled(running, 't', 0)[1]).toMatchObject({ cancelled: true })

    const read = applyLiveToolResult(running, 't', 0, result('a'))
    const stopped = markLiveCancelled(read, 't', 0)
    expect(stopped).toHaveLength(4)
    expect(stopped[3]).toMatchObject({ id: liveId('t', 1, 'a'), role: 'assistant', content: '', cancelled: true })
  })

  it('marks a failed turn where a stop would go', () => {
    const running = applyLiveToolCall([user], 't', 0, call('a'))
    const failure = { message: 'The AI provider is overloaded.', afterMs: 58_000 }
    expect(markLiveFailed(running, 't', 0, failure)[1]).toMatchObject({ failure })
    const read = applyLiveToolResult(running, 't', 0, result('a'))
    expect(markLiveFailed(read, 't', 0, failure)[3]).toMatchObject({ id: liveId('t', 1, 'a'), failure })
  })

  it('keeps how an earlier turn ended through the rebuild', () => {
    const onScreen: CopilotMessage[] = [
      user,
      { id: 'live-a', role: 'assistant', content: '', timestamp: new Date(), failure: { message: 'x' } },
      { id: 'live-b', role: 'assistant', content: '', timestamp: new Date(), cancelled: true },
    ]
    const rebuilt = keepIds(
      onScreen,
      (['user', 'assistant', 'assistant'] as const).map((role, i) => ({ id: `srv-${i}`, role, content: '', timestamp: new Date() })),
    )
    expect(rebuilt[1]).toMatchObject({ id: 'live-a', failure: { message: 'x' } })
    expect(rebuilt[2]).toMatchObject({ id: 'live-b', cancelled: true })
  })

  it('keeps the ids on screen while the roles agree', () => {
    const onScreen: CopilotMessage[] = [
      user,
      { id: 'live-a', role: 'assistant', content: 'x', timestamp: new Date() },
      { id: 'live-u', role: 'user', content: '', timestamp: new Date() },
    ]
    const fromServer = (roles: Array<'user' | 'assistant'>) =>
      roles.map((role, i) => ({ id: `srv-${i}`, role, content: '', timestamp: new Date() }) as CopilotMessage)

    expect(keepIds(onScreen, fromServer(['user', 'assistant', 'user', 'assistant'])).map((m) => m.id)).toEqual([
      'u0',
      'live-a',
      'live-u',
      'srv-3',
    ])
    // A compaction rewrote the history: from the first mismatch, the server's.
    expect(keepIds(onScreen, fromServer(['user', 'user', 'assistant'])).map((m) => m.id)).toEqual([
      'u0',
      'srv-1',
      'srv-2',
    ])
  })
})
