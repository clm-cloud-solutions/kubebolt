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
  model: string
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
