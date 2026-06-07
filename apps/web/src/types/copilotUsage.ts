// Types for the admin "Copilot Usage" analytics page. Mirror the
// response shapes of /api/v1/admin/copilot/usage/*.

export interface CopilotUsageSummary {
  range: string
  sessions: number
  // Excludes AUX records (auto_title / manual_compact) — honest adoption count.
  interactiveSessions: number
  errorSessions: number
  maxRoundsSessions: number
  fallbackSessions: number
  errorRate: number // % of sessions with reason != "done"
  fallbackRate: number // % of sessions that used the fallback provider
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

// Breakdown-by-X: ?groupBy=user|trigger|cluster|model|reason|conversation on the
// summary endpoint returns one summary per group, sorted by cost desc.
export type BreakdownDimension = 'user' | 'trigger' | 'cluster' | 'model' | 'reason' | 'conversation'

export interface CopilotUsageBreakdown {
  range: string
  groupBy: BreakdownDimension
  groups: Array<{ key: string; summary: CopilotUsageSummary }>
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
  conversationId?: string
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
