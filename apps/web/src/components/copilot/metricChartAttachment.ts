import {
  parseWorkloadMetrics,
  workloadMetricsHasRenderableData,
  type CopilotMessage,
  type WorkloadMetricsResponse,
} from '@/services/copilot/types'

export interface MetricChartCard {
  id: string
  data: WorkloadMetricsResponse
}

// Spec #08 — given a chat message array, compute which `get_workload_metrics`
// chart cards should attach to each text-bearing assistant turn.
//
// Attachment rule: a `get_workload_metrics` tool call attaches to the FIRST
// text-bearing assistant turn that STRICTLY FOLLOWS the turn that issued
// the call. Same-turn attachment is forbidden — Claude bundles "I'll check
// this" text + tool_call in a single assistant message, so attaching there
// would render the chart above its explanatory prose, before the data is
// even available in reading order. Charts belong with the analysis text
// that interprets them, not with the "preparing to investigate" preamble.
//
// Conversation block scoping: a call only attaches within the same block
// (the run from the most recent user-content message to the current turn).
// Cross-block attachment would surface a stale chart on an unrelated
// follow-up question.
//
// Dedup: each `tc.id` claims its slot at the first eligible attachment,
// so later text turns in the same block never re-render the same chart.
// Calls whose result is unparseable or marked isError are skipped, as
// are calls whose parsed payload has no renderable data (no agent
// connected, KSM absent, podsResolved=0) — the LLM's prose covers those.
// Note that an unrenderable-but-parseable result still claims the slot,
// preventing a later text turn from trying to render it as a chart.
//
// Returns a Map keyed by message id; absent keys mean "no charts attach
// here." Caller renders `attachments.get(message.id) ?? []`.
export function computeMetricChartAttachments(
  messages: CopilotMessage[]
): Map<string, MetricChartCard[]> {
  const resultByCallId = new Map<string, { content: string; isError?: boolean }>()
  for (const m of messages) {
    if (m.role === 'user' && m.toolResults) {
      for (const tr of m.toolResults) {
        resultByCallId.set(tr.toolCallId, { content: tr.content, isError: tr.isError })
      }
    }
  }

  const renderedMetricCallIds = new Set<string>()
  const out = new Map<string, MetricChartCard[]>()

  for (let idx = 0; idx < messages.length; idx++) {
    const m = messages[idx]
    if (m.role !== 'assistant' || !m.content) continue

    let blockStart = 0
    for (let i = idx - 1; i >= 0; i--) {
      if (messages[i].role === 'user' && messages[i].content) {
        blockStart = i + 1
        break
      }
    }

    // Strict `<` — same-turn attachment is the bug this guards against.
    const cards: MetricChartCard[] = []
    for (let i = blockStart; i < idx; i++) {
      const msg = messages[i]
      if (msg.role !== 'assistant' || !msg.toolCalls) continue
      for (const tc of msg.toolCalls) {
        if (tc.name !== 'get_workload_metrics') continue
        if (renderedMetricCallIds.has(tc.id)) continue
        const tr = resultByCallId.get(tc.id)
        if (!tr || tr.isError) continue
        const parsed = parseWorkloadMetrics(tr.content)
        if (parsed) renderedMetricCallIds.add(tc.id)
        if (parsed && workloadMetricsHasRenderableData(parsed)) {
          cards.push({ id: tc.id, data: parsed })
        }
      }
    }

    if (cards.length > 0) out.set(m.id, cards)
  }

  return out
}
