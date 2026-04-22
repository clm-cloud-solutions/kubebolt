// Types for the admin "Copilot Usage" analytics page. Mirror the
// response shapes of /api/v1/admin/copilot/usage/*.

export interface CopilotUsageSummary {
  range: string
  sessions: number
  errorSessions: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  cacheCreationTokens: number
  totalBilledTokens: number
  cacheHitPct: number
  avgRounds: number
  avgDurationMs: number
  compacts: number
  estimatedUsd: number
  topTools: Array<{
    name: string
    calls: number
    errors: number
    bytes: number
  }>
  topTriggers: Record<string, number>
}

export interface CopilotUsageBucket {
  time: string
  sessions: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  compacts: number
  estimatedUsd: number
}

export interface CopilotSessionEnriched {
  id: string
  timestamp: string
  userId: string
  cluster: string
  provider: string
  model: string
  trigger: string
  reason: string
  rounds: number
  usage: {
    inputTokens: number
    outputTokens: number
    cacheReadTokens?: number
    cacheCreationTokens?: number
  }
  toolCalls: number
  toolResultBytes: number
  durationMs: number
  fallback: boolean
  tools?: Record<string, { calls: number; bytes: number; errors: number; durationMs: number }>
  compacts?: Array<{ turnsFolded: number; tokensBefore: number; tokensAfter: number; model: string }>
  estimatedUsd: number
}
