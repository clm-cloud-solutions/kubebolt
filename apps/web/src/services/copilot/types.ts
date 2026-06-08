// Copilot frontend types — mirror the backend `internal/copilot/types.go`.

export type CopilotRole = 'user' | 'assistant' | 'system' | 'tool'

export interface CopilotMessage {
  id: string
  role: CopilotRole
  content: string
  toolCalls?: CopilotToolCall[]
  toolResults?: CopilotToolResult[]
  timestamp: Date
  /** Optional message kind. 'compact-notice' marks where compaction happened;
   * 'maxrounds-notice' marks where a turn hit the tool-step limit and offers a
   * Continue control. Neither is ever sent to the provider. */
  kind?: 'compact-notice' | 'maxrounds-notice'
  compactMeta?: {
    turnsFolded: number
    toolResultsStubbed: number
    tokensBefore: number
    tokensAfter: number
    model?: string
    auto: boolean
  }
  /** Set on a 'maxrounds-notice' message: the step limit that was reached, so
   * the banner can show "reached the step limit (N)". */
  maxRoundsLimit?: number
  /** Set on the assistant turn the user stopped via the Stop button. The UI
   * renders a "Stopped by you" marker so a half-finished (or empty) answer
   * reads as a deliberate cancel, not a hang or a completed reply. */
  cancelled?: boolean
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
  // "meta" / "done" payload: the persisted conversation id. Sent early in a
  // `meta` event (so a mid-stream refresh can still resume) and echoed on
  // `done`. Empty/absent when persistence isn't wired (auth/BoltDB disabled).
  conversationId?: string
  // "usage" event payload: per-round delta and running session totals
  round?: number
  turn?: CopilotUsage
  session?: CopilotUsage
  // "meta" event payload: the turn hit the tool-call step limit; value is the
  // limit N. The UI renders a deterministic "reached step limit" notice with a
  // Continue control regardless of what the model's closing text says.
  maxRoundsReached?: number
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
    timestamp?: string
  }>
}

export interface CompactResponse {
  summary: string
  messages: Array<{
    role: CopilotRole
    content: string
    toolCalls?: CopilotToolCall[]
    toolResults?: CopilotToolResult[]
    timestamp?: string
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

// Known action kinds. Stays a union of literals so a typo at a dispatch
// switch fails the typecheck. New propose_* tools added on the backend
// must extend this union AND add a runProposal case in
// ActionProposalCard.tsx.
export type ActionProposalAction =
  | 'restart_workload'
  | 'debug_pod'
  | 'scale_workload'
  | 'rollback_deployment'
  | 'delete_resource'
  | 'set_resources'
  | 'set_image'
  | 'set_env'
  | 'patch_hpa'

export interface ActionProposal {
  kind: 'action_proposal'
  version: number
  action: ActionProposalAction
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
  // Terminal NON-convergence: the poller hit the configurable progress
  // timeout while the action was applied-but-not-converged (e.g. scale 3→4
  // blocked by a namespace ResourceQuota). Distinct from progressSettled
  // (which also covers clean completion) so the card can render an honest
  // "did not converge" state instead of a spinner that never resolves, and
  // so re-mounts don't re-arm the poller. Set once, alongside progressSettled.
  progressOutcome?: 'stalled'
  // Human-readable last-observed progress at the moment of the stall, e.g.
  // "Scale to 4: 2/4 ready". Surfaced in the terminal card + fed to Kobi.
  progressDetail?: string
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

// ─── Workload metrics tool (spec #07 + chart card spec #08) ──────────
//
// `get_workload_metrics` tool result content. The Copilot panel detects
// these and renders an inline KobiMetricChartCard alongside the assistant
// text. Mirrors the Go `workloadMetricsResponse` shape in
// apps/api/internal/copilot/workload_metrics_executor.go.
//
// Empty / error cases the parser tolerates:
//   - `{"error": "..."}` → not a metrics response, ignored
//   - `podsResolved: 0` → valid response, card renders empty-state note
//   - missing `request`/`limit`/`utilizationPercent` → KSM absent path,
//     card renders without threshold lines

export type WorkloadMetricKey = 'cpu' | 'memory' | 'network_rx' | 'network_tx'

export type WorkloadMetricUnit = 'cores' | 'bytes' | 'bytes/sec'

export interface WorkloadMetricsSummary {
  min: number
  avg: number
  max: number
  p95: number
}

export interface WorkloadMetricsTrendPoint {
  t: string // RFC3339
  v: number
}

export interface WorkloadMetricsUtilization {
  vsRequest?: number
  vsLimit?: number
}

export interface WorkloadMetricsContainerEntry {
  summary: WorkloadMetricsSummary
  trend: WorkloadMetricsTrendPoint[]
}

export interface WorkloadMetricsEntry {
  unit: WorkloadMetricUnit
  summary: WorkloadMetricsSummary
  trend: WorkloadMetricsTrendPoint[]
  request?: number
  limit?: number
  utilizationPercent?: WorkloadMetricsUtilization
  perContainer?: Record<string, WorkloadMetricsContainerEntry>
}

export interface WorkloadMetricsResponse {
  workload: { kind: string; namespace: string; name: string }
  range: string
  end: string
  podsResolved: number
  metrics: Partial<Record<WorkloadMetricKey, WorkloadMetricsEntry>>
  note?: string
}

/**
 * workloadMetricsHasRenderableData reports whether a parsed metrics
 * response carries enough signal to render a meaningful chart. Returns
 * false when:
 *
 *   - the response has no metrics object (defensive)
 *   - every metric's trend is empty/null (no agent data, KSM absent,
 *     cluster too new, podsResolved=0)
 *
 * In those cases the chat panel suppresses the chart card entirely —
 * the LLM's prose already explains what's missing ("kube-state-metrics
 * not detected", "workload has no running pods", etc.) and an empty
 * card is visual noise. Used by CopilotPanel to filter before render.
 */
export function workloadMetricsHasRenderableData(data: WorkloadMetricsResponse): boolean {
  if (!data.metrics) return false
  for (const key of Object.keys(data.metrics) as WorkloadMetricKey[]) {
    const entry = data.metrics[key]
    if (entry?.trend && entry.trend.length > 0) {
      return true
    }
  }
  return false
}

/**
 * parseWorkloadMetrics extracts a typed WorkloadMetricsResponse from a
 * tool result's content string. Returns null when the content isn't a
 * workload-metrics payload — used by the chat panel to decide whether
 * to render the inline chart card without crashing on unrelated tool
 * outputs (error JSON, get_resource_detail, etc).
 *
 * Detection rule: parsed object has `workload` (with kind/namespace/name)
 * AND `metrics` as an object. Narrower than just "has metrics" so we
 * don't false-positive on other tools that might use the word.
 */
export function parseWorkloadMetrics(content: string): WorkloadMetricsResponse | null {
  if (!content) return null
  try {
    const parsed = JSON.parse(content)
    if (
      parsed &&
      typeof parsed === 'object' &&
      parsed.workload &&
      typeof parsed.workload === 'object' &&
      typeof parsed.workload.kind === 'string' &&
      typeof parsed.workload.namespace === 'string' &&
      typeof parsed.workload.name === 'string' &&
      parsed.metrics &&
      typeof parsed.metrics === 'object'
    ) {
      return parsed as WorkloadMetricsResponse
    }
  } catch {
    // Not JSON or malformed — definitely not a metrics response.
  }
  return null
}

// ─── Conversation history (persist + resume) ───────────────────────
//
// Conversations are personal (per user). The list endpoint returns metadata
// only (ConversationSummary); the detail endpoint returns the full transcript
// (ConversationDetail) so the panel can rehydrate and resume.

export interface ConversationSummary {
  id: string
  title: string
  clusterId: string
  preview: string
  messageCount: number
  createdAt: string // RFC3339
  updatedAt: string // RFC3339
  provider?: string
  model?: string
  trigger?: string
  originatingInsightId?: string
  archived?: boolean
}

export interface ConversationDetail {
  id: string
  tenantId: string
  userId: string
  clusterId: string
  title: string
  createdAt: string
  updatedAt: string
  provider?: string
  model?: string
  messages: Array<{
    role: CopilotRole
    content?: string
    toolCalls?: CopilotToolCall[]
    toolResults?: CopilotToolResult[]
    timestamp?: string
  }>
  lastRoundUsage?: CopilotUsage
  trigger?: string
  originatingInsightId?: string
  archived?: boolean
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
  // How long (ms) the UI polls an executed action for convergence before
  // declaring it stalled and asking Kobi to investigate. Server-side:
  // KUBEBOLT_AI_ACTION_PROGRESS_TIMEOUT. Falls back to a client default
  // when absent (older backend).
  actionProgressTimeoutMs?: number
  // When true (default), the chat panel renders persistent collapsible
  // cards for each tool call (name + status + result). When false, the
  // panel keeps only the final assistant text and a transient loading
  // indicator. Server-side: KUBEBOLT_AI_SHOW_TOOL_CALLS.
  showToolCalls?: boolean
}
