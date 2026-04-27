// Copilot frontend types — mirror the backend `internal/copilot/types.go`.

export type CopilotRole = 'user' | 'assistant' | 'system' | 'tool'

export interface CopilotMessage {
  id: string
  role: CopilotRole
  content: string
  toolCalls?: CopilotToolCall[]
  toolResults?: CopilotToolResult[]
  timestamp: Date
  /** Optional message kind; 'compact-notice' renders as an inline banner
   * marking where auto- or manual compaction happened and never gets sent
   * to the provider. */
  kind?: 'compact-notice'
  compactMeta?: {
    turnsFolded: number
    toolResultsStubbed: number
    tokensBefore: number
    tokensAfter: number
    model?: string
    auto: boolean
  }
}

export interface CopilotToolCall {
  id: string
  name: string
  input?: Record<string, unknown>
}

export interface CopilotToolResult {
  toolCallId: string
  content: string
  isError?: boolean
}

// Usage reported by the provider for a single Chat call or an aggregated
// session. Mirrors backend `copilot.Usage`.
export interface CopilotUsage {
  inputTokens: number
  outputTokens: number
  cacheCreationTokens?: number
  cacheReadTokens?: number
}

// SSE event types streamed from /api/v1/copilot/chat
export type CopilotStreamEventType =
  | 'meta'
  | 'tool_call'
  | 'tool_result'
  | 'text'
  | 'error'
  | 'done'
  | 'usage'
  | 'compact'

export interface CopilotStreamEvent {
  type: CopilotStreamEventType
  text?: string
  toolName?: string
  error?: string
  fallback?: boolean
  // "usage" event payload: per-round delta and running session totals
  round?: number
  turn?: CopilotUsage
  session?: CopilotUsage
  // "compact" event payload: auto-compaction occurred mid-session
  turnsFolded?: number
  toolResultsStubbed?: number
  tokensBefore?: number
  tokensAfter?: number
  model?: string
  summary?: string
  // "done" event payload: final messages array for the frontend to replace
  // its state with, preserving the full tool-call history.
  messages?: Array<{
    role: CopilotRole
    content: string
    toolCalls?: CopilotToolCall[]
    toolResults?: CopilotToolResult[]
  }>
}

export interface CompactResponse {
  summary: string
  messages: Array<{
    role: CopilotRole
    content: string
    toolCalls?: CopilotToolCall[]
    toolResults?: CopilotToolResult[]
  }>
  tokensBefore: number
  tokensAfter: number
  turnsFolded: number
  toolResultsStubbed?: number
  model: string
}

// ─── Action Proposals (Copilot execution capacity) ─────────────────
//
// `propose_*` tools on the backend return an ActionProposal payload as the
// tool result content. The Copilot panel detects these by parsing
// CopilotToolResult.content and looking for `kind === "action_proposal"`,
// then renders an interactive card. The LLM never executes mutations —
// the user clicks Execute, and the existing mutation endpoints run under
// the user's RBAC role.
//
// Mirrors the Go `ActionProposal` struct in apps/api/internal/copilot/proposals.go.

export type ActionProposalRisk = 'low' | 'medium' | 'high'

export interface ActionProposalTarget {
  type: string
  namespace: string
  name: string
}

export interface ActionProposal {
  kind: 'action_proposal'
  version: number
  action: string // e.g. "restart_workload", "scale_workload"
  target: ActionProposalTarget
  params: Record<string, unknown>
  summary: string
  rationale: string
  risk: ActionProposalRisk
  reversible: boolean
  // Execution metadata — written by the frontend AFTER the user acts on the
  // card (Execute or Dismiss) so the LLM sees the outcome on the next turn
  // and doesn't re-propose the same action. Absent on freshly-emitted
  // proposals.
  executionStatus?: 'executed' | 'failed' | 'dismissed'
  executionResult?: string | null
  executedAt?: string // ISO timestamp
  // True once the post-Execute progress poller has reached its terminal
  // state (rollout converged / pods drained / etc). When the chat panel
  // re-renders for any reason — typically a follow-up message rebuilding
  // messages[] — we honor this and skip re-polling. Without it, an old
  // card's poller would resume against a now-stale target (e.g. another
  // scale was issued in the meantime) and report a confusing "still in
  // progress" line for an action that already finished cleanly.
  progressSettled?: boolean
}

/**
 * parseActionProposal returns the parsed proposal if `content` is a JSON
 * payload with `kind === "action_proposal"`, otherwise null. Tool results
 * are JSON strings; non-proposal tools return data that just won't match.
 */
export function parseActionProposal(content: string): ActionProposal | null {
  if (!content) return null
  try {
    const parsed = JSON.parse(content)
    if (parsed && typeof parsed === 'object' && parsed.kind === 'action_proposal') {
      return parsed as ActionProposal
    }
  } catch {
    // Not JSON — definitely not a proposal.
  }
  return null
}

export interface CopilotConfig {
  enabled: boolean
  provider: string
  model: string
  proxyMode: boolean
  fallback?: { provider: string; model: string }
  sessionBudget?: number
  compactTrigger?: number
  autoCompact?: boolean
}
