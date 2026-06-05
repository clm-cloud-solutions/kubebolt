import { getAccessToken } from '@/services/api'
import type { CompactResponse, CopilotMessage, CopilotStreamEvent, CopilotUsage } from './types'

// Best-effort IANA timezone of the browser. Falls back to empty string
// when Intl is unavailable (very old runtimes) — the backend treats an
// empty string as "use UTC", which is correct for that scenario.
function resolveBrowserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    return ''
  }
}

// Strip transient UI fields before sending to backend. Compact notices are
// UI-only markers and never leave the client. Exported so the context can
// reuse the exact same wire shape when syncing a transcript outside a chat
// turn (e.g. persisting an action-proposal outcome).
export function serializeMessages(messages: CopilotMessage[]) {
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
  lastRoundUsage?: CopilotUsage | null,
  conversationId?: string | null,
  originatingInsightId?: string | null,
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
      // Seed for the backend's auto-compact threshold check. The last
      // round's provider-reported input (fresh + cached + creation) is the
      // accurate size of what the LLM processed; without it the backend
      // falls back to a chars-per-token approximation that underestimates
      // JSON-heavy tool results and misses the trigger on round 0 of
      // follow-up requests.
      lastRoundUsage: lastRoundUsage ?? undefined,
      // Resume binding: the server persists the transcript under this id so a
      // refresh / re-login can pick the conversation back up. Empty on the
      // first turn of a new conversation — the server mints one and returns it
      // in the `meta` event.
      conversationId: conversationId ?? undefined,
      originatingInsightId: originatingInsightId ?? undefined,
      // Anchor Kobi to the user's clock. Without these the model has no
      // notion of "today" and guesses from its training cutoff — which
      // produces day-off errors on relative-time questions like "ayer
      // a las 10pm" and forces a clarifying round-trip on timezone.
      clientTimezone: resolveBrowserTimezone(),
      clientNow: new Date().toISOString(),
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
  conversationId?: string | null,
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
      // Cross-references the recorded compaction-token usage to this conversation.
      conversationId: conversationId ?? undefined,
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
