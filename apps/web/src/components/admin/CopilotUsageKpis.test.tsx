import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import type { CopilotUsageBucket, CopilotUsageSummary } from '@/types/copilotUsage'

// The unit is decided by the backend (GET /copilot/config's spendUnit); here it
// is set directly so each test pins one install.
let unit: 'credits' | 'usd' = 'credits'
vi.mock('@/hooks/useAiSpendUnit', async (orig) => ({
  ...(await orig<typeof import('@/hooks/useAiSpendUnit')>()),
  useAiSpendUnit: () => unit,
}))

const { CopilotUsageKpis } = await import('./CopilotUsageKpis')

const summary: CopilotUsageSummary = {
  range: '7d',
  sessions: 40,
  interactiveSessions: 32,
  errorSessions: 2,
  maxRoundsSessions: 1,
  fallbackSessions: 3,
  errorRate: 5,
  fallbackRate: 7.5,
  inputTokens: 180_000,
  outputTokens: 42_000,
  cacheReadTokens: 900_000,
  cacheCreationTokens: 60_000,
  totalBilledTokens: 222_000,
  cacheHitPct: 79,
  avgRounds: 3.4,
  avgDurationMs: 12_400,
  compacts: 2,
  estimatedUsd: 4.83,
  credits: 966,
  topTools: [],
  topTriggers: {},
}

const buckets: CopilotUsageBucket[] = [0, 1, 2].map((i) => ({
  time: new Date(Date.UTC(2026, 9, 1 + i)).toISOString(),
  sessions: 10 + i,
  inputTokens: 1,
  outputTokens: 1,
  cacheReadTokens: 1,
  compacts: 0,
  estimatedUsd: 1.2 + i,
  credits: 240 + i * 100,
}))

function renderRow() {
  return render(
    <MemoryRouter>
      <CopilotUsageKpis summary={summary} buckets={buckets} range="7d" />
    </MemoryRouter>,
  )
}

describe('CopilotUsageKpis', () => {
  beforeEach(() => {
    unit = 'credits'
  })

  it('shows credits and no dollar figure where the platform runs the AI', () => {
    const { container } = renderRow()
    const text = container.textContent ?? ''
    expect(text).toContain('966')
    expect(text).toContain('credits')
    expect(text).not.toContain('$')
  })

  it('shows the estimated cost where the org brings its own key', () => {
    unit = 'usd'
    const { container } = renderRow()
    const text = container.textContent ?? ''
    expect(text).toContain('$4.83')
    expect(text).not.toContain('966')
  })

  it('keeps every figure of the old reliability tiles', () => {
    const { container } = renderRow()
    const text = container.textContent ?? ''
    // errors, fallback, max rounds, interactive
    expect(text).toMatch(/ended in an error\s*2/)
    expect(text).toMatch(/used the fallback provider\s*3/)
    expect(text).toMatch(/hit the round limit\s*1/)
    expect(text).toContain('32 interactive, 8 automatic')
  })
})
