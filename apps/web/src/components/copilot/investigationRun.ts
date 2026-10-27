import {
  parseWorkloadMetrics,
  workloadMetricsHasRenderableData,
  type CopilotMessage,
  type CopilotToolCall,
  type CopilotToolResult,
} from '@/services/copilot/types'
import { connectorShortName, externalTool } from '@/utils/toolName'

// One investigation is one exchange: from an operator question to the next
// one. Derived from the transcript, like externalSources.ts, so it holds live,
// after the `done` rebuild and after a reload.

// isGuide: a call that loads instructions rather than reading anything — a
// skill, or an Azure router call with `learn: true`, which only lists its
// commands. Guides are not reads.
export function isGuide(call: CopilotToolCall): boolean {
  if (call.name === 'load_skill') return true
  const ext = externalTool(call.name)
  return ext?.connectorId === 'azure' && call.input?.learn === true
}

// Proposals are cards of their own, never a read.
const isProposal = (call: CopilotToolCall) => call.name.startsWith('propose_')

// couldNotRead: the call came back without what it asked for — it failed,
// was refused, timed out, or was never sent. Empty and not-found answers are
// reads: they said something.
export function couldNotRead(result: CopilotToolResult | undefined): boolean {
  if (!result) return false
  switch (result.status) {
    case 'error':
    case 'timeout':
    case 'denied':
    case 'not_sent':
    case 'agent_offline':
    case 'unsupported':
      return true
    case undefined:
      return !!result.isError
  }
  return false
}

const isQuestion = (m: CopilotMessage) => m.role === 'user' && !!m.content

// cloudBoundaries returns the calls that leave the cluster for a connector for
// the first time in their exchange: call id → connector id. The panel marks
// each with "Leaves the cluster → AWS …" before its card.
export function cloudBoundaries(messages: CopilotMessage[]): Map<string, string> {
  const out = new Map<string, string>()
  let seen = new Set<string>()
  for (const m of messages) {
    if (isQuestion(m)) {
      seen = new Set()
      continue
    }
    if (m.role !== 'assistant') continue
    for (const call of m.toolCalls ?? []) {
      const ext = externalTool(call.name)
      if (!ext || isGuide(call) || seen.has(ext.connectorId)) continue
      seen.add(ext.connectorId)
      out.set(call.id, ext.connectorId)
    }
  }
  return out
}

export interface InvestigationRun {
  // The answer the run concludes with; the summary sits right above it.
  answerId: string
  // The steps the summary folds: the rounds' texts and cards between the
  // question and the answer. Notices and proposals stay visible.
  stepIds: string[]
  // From the question to the answer, by the server's timestamps; undefined
  // when they do not give a positive span.
  elapsedMs?: number
  reads: number
  // Reads per cloud connector, in first-use order.
  byCloud: Array<[string, number]>
  guides: number
  // What the answer brings below it: metric charts, then proposals.
  charts: number
  proposals: number
  // The result messages that carry the proposals: the panel draws them after
  // the answer.
  proposalIds: string[]
  // The step limit the turn paused at, when it did (the Continue notice is
  // still under the answer).
  pausedAtLimit?: number
  unread: number
}

// investigationRuns returns, per finished exchange that called tools, its
// summary keyed by the answer's id. An exchange is finished when its answer
// carries the turn id the server stamps on `done`: a turn still running, one
// the operator stopped or one that failed has none, and stays unfolded.
export function investigationRuns(messages: CopilotMessage[]): Map<string, InvestigationRun> {
  const out = new Map<string, InvestigationRun>()
  const results = new Map<string, CopilotToolResult>()
  for (const m of messages) for (const r of m.toolResults ?? []) results.set(r.toolCallId, r)

  let start = -1
  const close = (end: number) => {
    if (start < 0) return
    const question = messages[start]
    const span = messages.slice(start + 1, end)
    // The answer: the last assistant message of the exchange, if the server
    // stamped it as the end of the turn.
    let answerIdx = -1
    for (let i = span.length - 1; i >= 0; i--) {
      if (span[i].role === 'assistant') {
        answerIdx = i
        break
      }
    }
    const answer = answerIdx >= 0 ? span[answerIdx] : undefined
    if (!answer?.turnId) return

    let reads = 0
    let guides = 0
    let unread = 0
    let charts = 0
    const clouds = new Map<string, number>()
    for (const m of span) {
      for (const call of m.toolCalls ?? []) {
        if (isProposal(call)) continue
        if (call.name === 'get_workload_metrics' && rendersChart(results.get(call.id))) charts++
        if (isGuide(call)) {
          guides++
          continue
        }
        reads++
        if (couldNotRead(results.get(call.id))) unread++
        const ext = externalTool(call.name)
        if (ext) clouds.set(ext.connectorId, (clouds.get(ext.connectorId) ?? 0) + 1)
      }
    }
    if (reads + guides === 0) return

    const stepIds = span
      .slice(0, answerIdx)
      .filter((m) => !m.kind && m.role !== 'system')
      .filter((m) => !(m.role === 'user' && m.toolResults?.some((r) => isProposalResult(r))))
      .map((m) => m.id)
    const proposalMessages = span.filter((m) => m.role === 'user' && m.toolResults?.some(isProposalResult))
    const notice = span.find((m) => m.kind === 'maxrounds-notice')
    const elapsed = answer.timestamp.getTime() - question.timestamp.getTime()
    out.set(answer.id, {
      answerId: answer.id,
      stepIds,
      elapsedMs: elapsed > 0 ? elapsed : undefined,
      reads,
      byCloud: [...clouds.entries()],
      guides,
      unread,
      charts,
      proposals: proposalMessages.reduce((n, m) => n + m.toolResults!.filter(isProposalResult).length, 0),
      proposalIds: proposalMessages.map((m) => m.id),
      pausedAtLimit: notice ? (notice.maxRoundsLimit ?? 0) : undefined,
    })
  }

  messages.forEach((m, i) => {
    if (isQuestion(m)) {
      close(i)
      start = i
    }
  })
  close(messages.length)
  return out
}

// rendersChart: a get_workload_metrics result the panel draws as a chart
// (metricChartAttachment.ts applies the same test).
function rendersChart(r: CopilotToolResult | undefined): boolean {
  if (!r || r.isError) return false
  const parsed = parseWorkloadMetrics(r.content)
  return !!parsed && workloadMetricsHasRenderableData(parsed)
}

// turnEndings says how each unfinished exchange ended, keyed by the ids of its
// assistant messages: 'stopped' when the operator stopped it, 'failed' when it
// ended in an error. A card whose call never came back says which.
export function turnEndings(messages: CopilotMessage[]): Map<string, 'stopped' | 'failed'> {
  const out = new Map<string, 'stopped' | 'failed'>()
  let span: CopilotMessage[] = []
  const close = () => {
    const end = span.some((m) => m.failure) ? 'failed' : span.some((m) => m.cancelled) ? 'stopped' : undefined
    if (end) for (const m of span) if (m.role === 'assistant') out.set(m.id, end)
    span = []
  }
  for (const m of messages) {
    if (isQuestion(m)) close()
    else span.push(m)
  }
  close()
  return out
}

// A proposal's result renders as its own actionable card: it is never folded.
function isProposalResult(r: CopilotToolResult): boolean {
  try {
    return JSON.parse(r.content)?.kind === 'action_proposal'
  } catch {
    return false
  }
}

// 0:07 · 1:12 · 12:03
export function formatClock(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`
}

// cloudScope says where a call went, for the boundary: the region the call
// named, else the connector's (AWS region, Azure subscription, GCP project),
// which only an admin has loaded.
export function cloudScope(
  connectorId: string,
  input: Record<string, unknown> | undefined,
  connectorFields?: Record<string, string>,
): string {
  if (connectorId === 'aws') {
    const region = typeof input?.region === 'string' ? input.region : ''
    return region || connectorFields?.region || ''
  }
  if (connectorId === 'azure') {
    const sub = connectorFields?.subscriptionId ?? ''
    return sub ? `subscription ${sub.slice(0, 8)}…` : ''
  }
  if (connectorId === 'gcp') {
    for (const v of Object.values(input ?? {})) {
      const m = typeof v === 'string' ? /(?:^|\/)projects\/([^/]+)/.exec(v) : null
      if (m) return m[1]
    }
    return connectorFields?.projectId ?? ''
  }
  return ''
}

// guideLabel names a guide: the skill it loads, or the Azure command group
// whose commands it lists.
export function guideLabel(call: CopilotToolCall): string {
  if (call.name === 'load_skill') {
    const name = typeof call.input?.name === 'string' ? call.input.name : ''
    return name || 'skill'
  }
  const ext = externalTool(call.name)
  return ext ? `${connectorShortName(ext.connectorId)} ${ext.tool.replace(/_/g, ' ')} commands` : call.name
}

export interface LiveInvestigation {
  // When the question was sent (the client's clock while it runs).
  startedAt: Date
  reads: number
  byCloud: Array<[string, number]>
  // Clouds with a read in flight right now, in first-use order.
  cloudsNow: string[]
}

// liveInvestigation reads the question in flight: the last one in the
// transcript and everything after it.
export function liveInvestigation(messages: CopilotMessage[]): LiveInvestigation | undefined {
  let start = -1
  for (let i = messages.length - 1; i >= 0; i--) {
    if (isQuestion(messages[i])) {
      start = i
      break
    }
  }
  if (start < 0) return undefined
  const span = messages.slice(start + 1)
  const answered = new Set<string>()
  for (const m of span) for (const r of m.toolResults ?? []) answered.add(r.toolCallId)

  let reads = 0
  const clouds = new Map<string, number>()
  const now: string[] = []
  for (const m of span) {
    for (const call of m.toolCalls ?? []) {
      if (isProposal(call) || isGuide(call)) continue
      reads++
      const ext = externalTool(call.name)
      if (!ext) continue
      clouds.set(ext.connectorId, (clouds.get(ext.connectorId) ?? 0) + 1)
      if (!answered.has(call.id) && !now.includes(ext.connectorId)) now.push(ext.connectorId)
    }
  }
  return { startedAt: messages[start].timestamp, reads, byCloud: [...clouds.entries()], cloudsNow: now }
}
