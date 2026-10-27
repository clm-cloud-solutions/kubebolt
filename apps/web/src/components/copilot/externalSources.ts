import type { CopilotMessage } from '@/services/copilot/types'
import { externalTool, connectorDisplayName } from '@/utils/toolName'

// Which cloud connectors an answer read from, for the footnote under it
// ("Read from AWS · read-only · credentials never shared with the model").
//
// Derived from the transcript, not sent by the server: the tool names already
// say it (mcp__aws__call), and the transcript is what survives both the
// `done` rebuild and a conversation reload. It also works with tool cards
// hidden (showToolCalls=false), which is exactly when the operator would
// otherwise have no way to know Kobi left the cluster.
//
// Scope: one exchange — from a user question to the next one. The footnote
// attaches to the LAST text-bearing assistant turn of the exchange, the
// answer, so it sits under the conclusion rather than the "let me check"
// preamble. While the answer streams it moves down with the text.
//
// A call whose result came back as an error did not read anything, so it
// does not count; a call with no recorded result (still running) does.
//
// Returns message id → connector ids in first-use order.
export function computeExternalSources(messages: CopilotMessage[]): Map<string, string[]> {
  const failed = new Set<string>()
  for (const m of messages) {
    if (m.role === 'user' && m.toolResults) {
      for (const tr of m.toolResults) if (tr.isError) failed.add(tr.toolCallId)
    }
  }

  const out = new Map<string, string[]>()
  let connectors: string[] = []
  let answerId: string | null = null
  const flush = () => {
    if (answerId && connectors.length > 0) out.set(answerId, connectors)
    connectors = []
    answerId = null
  }

  for (const m of messages) {
    if (m.role === 'user' && m.content) {
      flush()
      continue
    }
    if (m.role !== 'assistant') continue
    for (const tc of m.toolCalls ?? []) {
      if (failed.has(tc.id)) continue
      const ext = externalTool(tc.name)
      if (ext && !connectors.includes(ext.connectorId)) connectors.push(ext.connectorId)
    }
    if (m.content) answerId = m.id
  }
  flush()
  return out
}

// "AWS", "AWS and Google Cloud", "AWS, Google Cloud and Azure".
export function externalSourcesLabel(connectorIds: string[]): string {
  const names = connectorIds.map(connectorDisplayName)
  if (names.length <= 1) return names.join('')
  return `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}
