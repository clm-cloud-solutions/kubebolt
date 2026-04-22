import { getAccessToken } from '@/services/api'
import type { CompactResponse, CopilotMessage, CopilotStreamEvent } from './types'

// Strip transient UI fields before sending to backend. Compact notices are
// UI-only markers and never leave the client.
function serializeMessages(messages: CopilotMessage[]) {
  return messages
    .filter((m) => m.kind !== 'compact-notice')
    .map((m) => ({
      role: m.role,
      content: m.content,
      toolCalls: m.toolCalls?.map((tc) => ({
        id: tc.id,
        name: tc.name,
        input: tc.input ?? {},
      })),
      toolResults: m.toolResults?.map((tr) => ({
        toolCallId: tr.toolCallId,
        content: tr.content,
        isError: tr.isError,
      })),
    }))
}

/**
 * sendCopilotChat opens an SSE stream to /api/v1/copilot/chat and yields
 * each event as it arrives. The conversation continues server-side via
 * the tool calling loop — the client just consumes events until 'done'.
 */
export async function* sendCopilotChat(
  messages: CopilotMessage[],
  currentPath: string,
  signal?: AbortSignal,
  trigger?: string,
): AsyncGenerator<CopilotStreamEvent> {
  const token = getAccessToken()
  const res = await fetch('/api/v1/copilot/chat', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify({
      messages: serializeMessages(messages),
      currentPath,
      trigger,
    }),
    signal,
  })

  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText)
    throw new Error(`copilot chat failed (${res.status}): ${text}`)
  }
  if (!res.body) {
    throw new Error('copilot chat: no response body')
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  try {
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })

      // SSE events are separated by double newlines
      let eventEnd: number
      while ((eventEnd = buffer.indexOf('\n\n')) !== -1) {
        const rawEvent = buffer.slice(0, eventEnd)
        buffer = buffer.slice(eventEnd + 2)
        const parsed = parseSSEEvent(rawEvent)
        if (parsed) yield parsed
      }
    }
  } finally {
    reader.releaseLock()
  }
}

/**
 * compactCopilotSession requests a server-side summarization of the current
 * message array. Used by the "New session with summary" button to reset the
 * conversation while preserving the context the user cares about.
 */
export async function compactCopilotSession(
  messages: CopilotMessage[],
  resetAll: boolean,
): Promise<CompactResponse> {
  const token = getAccessToken()
  const res = await fetch('/api/v1/copilot/compact', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify({
      messages: serializeMessages(messages),
      resetAll,
    }),
  })
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText)
    throw new Error(`copilot compact failed (${res.status}): ${text}`)
  }
  return (await res.json()) as CompactResponse
}

function parseSSEEvent(raw: string): CopilotStreamEvent | null {
  let event = 'message'
  let data = ''
  for (const line of raw.split('\n')) {
    if (line.startsWith('event:')) {
      event = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      data += line.slice(5).trim()
    }
  }
  if (!data) return null
  try {
    const parsed = JSON.parse(data)
    return { type: event as CopilotStreamEvent['type'], ...parsed }
  } catch {
    return { type: event as CopilotStreamEvent['type'], text: data }
  }
}
