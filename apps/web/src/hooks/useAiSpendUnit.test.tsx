import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { eeAiSpendUnitDefault } from '@/ee/registry'

// What GET /copilot/config answers in each test; null = it never answers.
let answer: Record<string, unknown> | null = {}

vi.mock('@/contexts/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'u1' }, sessionReady: true }),
}))
vi.mock('@/services/api', async (orig) => {
  const real = await orig<typeof import('@/services/api')>()
  return {
    ...real,
    api: {
      ...real.api,
      getCopilotConfig: () => (answer == null ? new Promise(() => {}) : Promise.resolve(answer)),
    },
  }
})

const { useAiSpendUnit } = await import('./useAiSpendUnit')

function renderUnit() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return renderHook(() => useAiSpendUnit(), { wrapper })
}

describe('useAiSpendUnit', () => {
  beforeEach(() => {
    answer = {}
  })

  it("starts at the edition's unit while the config loads", () => {
    answer = null
    expect(renderUnit().result.current).toBe(eeAiSpendUnitDefault)
  })

  it('follows the backend once it answers', async () => {
    for (const unit of ['usd', 'credits'] as const) {
      answer = { spendUnit: unit }
      const { result } = renderUnit()
      await waitFor(() => expect(result.current).toBe(unit))
    }
  })

  it("keeps the edition's unit when the backend sends no unit, or one it does not know", async () => {
    for (const a of [{}, { spendUnit: 'eur' }]) {
      answer = a
      const { result } = renderUnit()
      // Give the query time to settle: the unit must not move.
      await new Promise((r) => setTimeout(r, 20))
      expect(result.current).toBe(eeAiSpendUnitDefault)
    }
  })
})
