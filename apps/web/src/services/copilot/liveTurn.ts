import type { CopilotMessage, CopilotToolCall, CopilotToolResult } from './types'

// The turn in flight is built in the shape the server's `done` returns: per
// round, an assistant message with that round's text and tool calls, then a
// user message with its tool results. The panel draws both the same way, so
// when `done` replaces the transcript nothing moves.

// liveId names a round's message: `a` is the assistant's, `u` the results'.
export function liveId(turnKey: string, round: number, part: 'a' | 'u'): string {
  return `${turnKey}-r${round}-${part}`
}

// upsert updates the message with `id`, or appends it built from `create`.
function upsert(
  prev: CopilotMessage[],
  id: string,
  create: () => CopilotMessage,
  update: (m: CopilotMessage) => CopilotMessage,
): CopilotMessage[] {
  const i = prev.findIndex((m) => m.id === id)
  if (i === -1) return [...prev, update(create())]
  const next = prev.slice()
  next[i] = update(prev[i])
  return next
}

function assistant(id: string): CopilotMessage {
  return { id, role: 'assistant', content: '', timestamp: new Date() }
}

export function applyLiveText(prev: CopilotMessage[], turnKey: string, round: number, text: string) {
  return upsert(prev, liveId(turnKey, round, 'a'), () => assistant(liveId(turnKey, round, 'a')), (m) => ({
    ...m,
    content: m.content + text,
  }))
}

export function applyLiveToolCall(prev: CopilotMessage[], turnKey: string, round: number, call: CopilotToolCall) {
  return upsert(prev, liveId(turnKey, round, 'a'), () => assistant(liveId(turnKey, round, 'a')), (m) => ({
    ...m,
    toolCalls: [...(m.toolCalls ?? []), call],
  }))
}

export function applyLiveToolResult(
  prev: CopilotMessage[],
  turnKey: string,
  round: number,
  result: CopilotToolResult,
) {
  const id = liveId(turnKey, round, 'u')
  return upsert(
    prev,
    id,
    () => ({ id, role: 'user', content: '', timestamp: new Date() }),
    (m) => ({ ...m, toolResults: [...(m.toolResults ?? []), result] }),
  )
}

// markLiveCancelled tags where the operator stopped the turn. After a round's
// results the model was already on the next round, so the marker goes on that
// round's (empty) message, below the cards; otherwise on the round in flight.
export function markLiveCancelled(prev: CopilotMessage[], turnKey: string, round: number) {
  return markLiveEnd(prev, turnKey, round, { cancelled: true })
}

// markLiveFailed tags where the turn ended in an error, in the same place a
// stop would go: the investigation's last row says what failed.
export function markLiveFailed(
  prev: CopilotMessage[],
  turnKey: string,
  round: number,
  failure: NonNullable<CopilotMessage['failure']>,
) {
  return markLiveEnd(prev, turnKey, round, { failure })
}

function markLiveEnd(
  prev: CopilotMessage[],
  turnKey: string,
  round: number,
  end: Pick<CopilotMessage, 'cancelled' | 'failure'>,
) {
  const last = prev[prev.length - 1]
  if (last?.id === liveId(turnKey, round, 'u')) {
    return [...prev, { ...assistant(liveId(turnKey, round + 1, 'a')), ...end }]
  }
  return upsert(prev, liveId(turnKey, round, 'a'), () => assistant(liveId(turnKey, round, 'a')), (m) => ({
    ...m,
    ...end,
  }))
}

// keepIds gives each message of the server's transcript the id the same
// message had on screen, position by position while the roles agree, so the
// rebuild on `done` keeps every card mounted (and an open one open). A
// compaction rewrites the history: from the first mismatch the server's
// messages keep their own ids. How an earlier turn ended (stopped, failed) is
// the browser's own record: it rides along with the id.
export function keepIds(onScreen: CopilotMessage[], rebuilt: CopilotMessage[]): CopilotMessage[] {
  let aligned = true
  return rebuilt.map((m, i) => {
    const prev = onScreen[i]
    aligned = aligned && prev !== undefined && prev.role === m.role
    if (!aligned) return m
    const out: CopilotMessage = { ...m, id: prev.id }
    if (prev.cancelled) out.cancelled = true
    if (prev.failure) out.failure = prev.failure
    return out
  })
}

// LivePhase is what the turn is waiting on: `model` while the model writes
// its next round (before the first one, or after a round's results), `tools`
// while a call runs (its card spins), `idle` otherwise.
export type LivePhase = 'model' | 'tools' | 'idle'

export function livePhase(messages: CopilotMessage[]): LivePhase {
  const last = messages[messages.length - 1]
  if (!last) return 'idle'
  if (last.role === 'assistant') {
    if (last.toolCalls?.length) return 'tools'
    return last.content === '' ? 'model' : 'idle'
  }
  if (last.role === 'user' && last.toolResults?.length && !last.content) {
    const calls = messages[messages.length - 2]?.toolCalls ?? []
    const done = new Set(last.toolResults.map((r) => r.toolCallId))
    return calls.every((c) => done.has(c.id)) ? 'model' : 'tools'
  }
  return 'idle'
}
