import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { PlanCard } from './PlanCard'
import { consolidationPlan, kyvernoPlan } from '@/components/kobiPlans/planFixture'

const { MockApiError, getPlan } = vi.hoisted(() => {
  class MockApiError extends Error {
    status: number
    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  }
  return { MockApiError, getPlan: vi.fn() }
})
vi.mock('@/services/api', () => ({
  api: { getPlan: (...a: unknown[]) => getPlan(...a) },
  ApiError: MockApiError,
}))
vi.mock('@/contexts/AuthContext', () => ({ useAuth: () => ({ hasRole: () => true }) }))

function renderCard(outcome: 'created' | 'existing_live_plan' | 'existing_same_scope' = 'created') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <PlanCard planRef={{ id: 'pln_d6551a4c', outcome }} />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('PlanCard', () => {
  it('reads the plan and shows its own figures', async () => {
    getPlan.mockResolvedValue(kyvernoPlan())
    renderCard()
    expect(await screen.findByText('Right-sizing: namespace kyverno')).toBeTruthy()
    expect(getPlan).toHaveBeenCalledWith('pln_d6551a4c')
    expect(screen.getByText(/6[.,]97/)).toBeTruthy()
    // The money is the price of what it frees, not a saving.
    expect(screen.getByText(/330m CPU · list prices · a saving only once nodes go/)).toBeTruthy()
    expect(screen.queryByText(/SAVINGS/)).toBeNull()
    expect(screen.getByText(/P95 of the last 7 days \(3.1 days of data\)/)).toBeTruthy()
    expect(screen.getByText('Open plan').closest('a')?.getAttribute('href')).toBe('/plans/pln_d6551a4c')
    // Export opens the export: a real button now, not a Soon.
    const exportButton = screen.getByRole('button', { name: /Export/ })
    expect((exportButton as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByTitle('Export — coming soon')).toBeNull()
  })

  it('says the saving is in capacity without OpenCost, and warns on low confidence', async () => {
    getPlan.mockResolvedValue(
      kyvernoPlan({
        impact: { estimated: { cpuMilli: 330, memBytes: 0, money: null, moneyUnavailable: 'no_cost_data' }, observed: null },
        engine: { name: 'rightsizing', windowDays: 0.9, lowConfidence: true, computedAt: '2026-11-25T18:00:00Z' },
      }),
    )
    renderCard()
    expect(await screen.findByText('330m CPU')).toBeTruthy()
    expect(screen.getByText(/Install OpenCost/)).toBeTruthy()
    expect(screen.getByText(/Low confidence/)).toBeTruthy()
  })

  it('shows the live plan the organization keeps, with discard', async () => {
    getPlan.mockResolvedValue(kyvernoPlan())
    renderCard('existing_live_plan')
    expect(await screen.findByText(/one live plan per cluster/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Discard' })).toBeTruthy()
    // Revising is in the plan's view now, not a Soon.
    expect(screen.queryByTitle('Revise — coming soon')).toBeNull()
  })

  it('says a live plan already covers the same workloads', async () => {
    getPlan.mockResolvedValue(kyvernoPlan())
    renderCard('existing_same_scope')
    expect(await screen.findByText(/already covers these workloads/)).toBeTruthy()
    expect(screen.queryByText(/one live plan per cluster/)).toBeNull()
  })

  it('says when the plan is gone', async () => {
    getPlan.mockRejectedValue(new MockApiError(404, 'plan not found'))
    renderCard()
    expect(await screen.findByText('This plan is no longer available.')).toBeTruthy()
  })

  it('shows a consolidation plan as the bill it lowers and the plan it waits for', async () => {
    getPlan.mockResolvedValue(consolidationPlan())
    renderCard()
    expect(await screen.findByText('ON THE CLUSTER BILL')).toBeTruthy()
    expect(screen.getByText(/^−\u2060€?129[.,]40(\s?€)?\/month$/)).toBeTruthy()
    expect(screen.getByText('5 → 4 nodes · list prices · less with commitments')).toBeTruthy()
    expect(screen.getByText(/Waits for Right-sizing · prod-eks/)).toBeTruthy()
    expect(screen.queryByText('CAPACITY IT FREES')).toBeNull()
  })
})
