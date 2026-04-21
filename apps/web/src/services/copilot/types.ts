// Copilot frontend types — mirror the backend `internal/copilot/types.go`.

export type CopilotRole = 'user' | 'assistant' | 'system' | 'tool'

export interface CopilotMessage {
  id: string
  role: CopilotRole
  content: string
  toolCalls?: CopilotToolCall[]
  toolResults?: CopilotToolResult[]
  timestamp: Date
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
}

export interface CopilotConfig {
  enabled: boolean
  provider: string
  model: string
  proxyMode: boolean
  fallback?: { provider: string; model: string }
}
