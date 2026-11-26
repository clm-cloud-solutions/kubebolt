import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { PlanDetailPage } from './PlanDetailPage'
import { consolidationPlan, kyvernoPlan } from './planFixture'

const { MockApiError, getPlan, discardPlan, reviseStep, verifyPlan } = vi.hoisted(() => {
  class MockApiError extends Error {
    status: number
    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  }
  return { MockApiError, getPlan: vi.fn(), discardPlan: vi.fn(), reviseStep: vi.fn(), verifyPlan: vi.fn() }
})
vi.mock('@/contexts/AuthContext', () => ({ useAuth: () => ({ user: { email: 'ana@example.com' }, hasRole: () => true }) }))
const { askInCluster, openPanel } = vi.hoisted(() => ({ askInCluster: vi.fn(), openPanel: vi.fn() }))
vi.mock('@/contexts/CopilotContext', () => ({
  useCopilot: () => ({ config: { enabled: true }, askInCluster, openPanel, activeClusterContext: 'agent:c-dev' }),
}))
vi.mock('@/hooks/useOpenInCluster', () => ({ useOpenInCluster: () => ({ targetFor: () => null, switchTo: async () => true }) }))
const plan = vi.hoisted(() => ({ isFree: false, consolidation: true }))
vi.mock('@/hooks/usePlan', () => ({ usePlan: () => ({ isFree: plan.isFree }) }))
// The edition's flag, switchable per test: the same tests run in both builds.
vi.mock('@/ee/registry', () => ({
  get eeConsolidationPlans() {
    return plan.consolidation
  },
}))
vi.mock('@/services/api', () => ({
  api: {
    getPlan: (...a: unknown[]) => getPlan(...a),
    discardPlan: (...a: unknown[]) => discardPlan(...a),
    reviseStep: (...a: unknown[]) => reviseStep(...a),
    verifyPlan: (...a: unknown[]) => verifyPlan(...a),
  },
  ApiError: MockApiError,
}))

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/plans/pln_d6551a4c']}>
        <Routes>
          <Route path="/plans/:id" element={<PlanDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('PlanDetailPage', () => {
  it('verifies an exported plan now, and shows each check\'s outcome and what failed', async () => {
    const plan = kyvernoPlan()
    const appliedAt = new Date(Date.now() - 2 * 86_400_000).toISOString()
    const exported = { ...plan, status: 'exported', effectiveStatus: 'exported', exportedAt: appliedAt }
    getPlan.mockResolvedValue({ ...exported, steps: plan.steps.map((s) => ({ ...s, status: 'applied', appliedAt })) })
    verifyPlan.mockResolvedValue({
      ...exported,
      notes: [...plan.notes, { version: 1, at: new Date().toISOString(), text: 'Step 1 (cleanup) failed its checks: a container was OOMKilled. Roll it back from the plan.' }],
      steps: plan.steps.map((s) => ({
        ...s,
        status: 'applied',
        appliedAt,
        verification: {
          ...s.verification,
          result: { checkedAt: new Date().toISOString(), passed: false, checks: { rollout_complete: 'ok', no_oom_killed: 'failed' }, detail: 'a container was OOMKilled' },
        },
      })),
    })
    renderPage()
    // Applied two days ago: measuring, and the checks are due.
    expect(await screen.findByText('measuring · day 3 of 7')).toBeTruthy()
    expect(screen.getByText(/checked from/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /Verify now/ }))
    await waitFor(() => expect(verifyPlan).toHaveBeenCalledWith(plan.id))
    expect(await screen.findByText(/Failed: a container was OOMKilled\. Roll it back/)).toBeTruthy()
    expect(screen.getByText('✗')).toBeTruthy()
    expect(screen.getByText('✓')).toBeTruthy()
    // The click says what the check found, in its own words.
    const toast = await screen.findByRole('status')
    expect(toast.textContent).toContain('Checked now')
    expect(toast.textContent).toContain('Step 1 (cleanup) failed its checks')
  })

  it('says when a check found nothing new, so the click never reads as dead', async () => {
    const plan = kyvernoPlan({ status: 'exported', effectiveStatus: 'exported', exportedAt: new Date().toISOString() })
    getPlan.mockResolvedValue(plan)
    verifyPlan.mockResolvedValue(plan)
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /Verify now/ }))
    expect((await screen.findByRole('status')).textContent).toContain('Nothing changed since the last check')
  })

  it('shows the observed impact on the cluster bill once the plan is measured', async () => {
    const plan = kyvernoPlan()
    getPlan.mockResolvedValue({
      ...plan,
      status: 'verified',
      effectiveStatus: 'verified',
      impact: {
        ...plan.impact,
        observed: {
          measuredAt: '2026-12-03T10:00:00Z', windowDays: 7, cpuMilliFreed: 80, memBytesFreed: 0,
          allocatedCost: { before: 5.04, after: 3.1, currency: 'eur' },
          clusterCost: { before: 96.5, after: 96.5, currency: 'eur', attributed: false },
        },
      },
      baseline: { capturedAt: '2026-11-26T10:00:00Z', windowDays: 7, clusterCost: { monthly: 96.5, currency: 'eur', source: 'opencost' }, clusterCostCovers: 'bill' },
    })
    renderPage()
    // Freed requests on the same nodes are no saving: the bill did not move.
    expect(await screen.findByText('on the cluster bill: no saving yet')).toBeTruthy()
    expect(screen.getByText(/96[.,]50.*→.*96[.,]50.* · 80m CPU freed · 7 days measured/)).toBeTruthy()
    expect(screen.queryByText(/savings/)).toBeNull()
    expect(screen.getByText(/whether and how to apply each step is your decision/)).toBeTruthy()
  })

  it('lets a person set a pending step\'s values, and says why the plan refused one', async () => {
    const plan = kyvernoPlan()
    getPlan.mockResolvedValue(plan)
    reviseStep.mockRejectedValueOnce(new MockApiError(422, 'cpu request 5m is under the 16m per pod the engine measured as the P95; the step keeps 20m'))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Edit values' }))
    expect(screen.getByText(/at least 16m \(P95 per pod\) · under 100m \(today\)/)).toBeTruthy()
    const input = screen.getByLabelText('cpu request')
    fireEvent.change(input, { target: { value: '5m' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save values' }))
    expect(await screen.findByText(/under the 16m per pod/)).toBeTruthy()
    expect(reviseStep).toHaveBeenCalledWith(plan.id, 's3', { containers: [{ container: 'kyverno', requests: { cpu: '5m' } }] })

    reviseStep.mockResolvedValueOnce({ ...plan, version: 2 })
    fireEvent.change(input, { target: { value: '30m' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save values' }))
    await waitFor(() => expect(reviseStep).toHaveBeenLastCalledWith(plan.id, 's3', { containers: [{ container: 'kyverno', requests: { cpu: '30m' } }] }))
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Save values' })).toBeNull())
  })

  it('discards a live plan once the person confirms', async () => {
    const plan = kyvernoPlan()
    getPlan.mockResolvedValue(plan)
    discardPlan.mockResolvedValue({ ...plan, status: 'expired', effectiveStatus: 'expired', effectiveExpiredReason: 'discarded' })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Discard' }))
    expect(screen.getByText(/Nothing changes in the cluster/)).toBeTruthy()
    expect(discardPlan).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Discard plan' }))
    await waitFor(() => expect(discardPlan).toHaveBeenCalledWith(plan.id))
    // An ended plan offers no discard, and says it was discarded.
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Discard' })).toBeNull())
    expect(screen.getByText('DISCARDED')).toBeTruthy()
  })

  it('offers no discard for a plan with every step applied', async () => {
    const plan = kyvernoPlan()
    getPlan.mockResolvedValue({ ...plan, status: 'exported', effectiveStatus: 'exported', steps: plan.steps.map((s) => ({ ...s, status: 'applied' })) })
    renderPage()
    await screen.findByText('Right-sizing: namespace kyverno')
    expect(screen.queryByRole('button', { name: 'Discard' })).toBeNull()
  })

  it('shows the plan, its steps and what was left out, in words', async () => {
    getPlan.mockResolvedValue({ ...kyvernoPlan(), createdBy: 'Ana Pérez' })
    renderPage()
    expect(await screen.findByText('Right-sizing: namespace kyverno')).toBeTruthy()
    // The author by their display name, standing out from the sentence.
    const author = screen.getByText('Ana Pérez')
    expect(author.className).toMatch(/text-kb-text-primary/)
    expect(author.closest('p')?.textContent).toMatch(/^Created by Ana Pérez in Kobi's chat on /)
    // The step: the change with what it was, its owner and rollback in words.
    expect(screen.getByText('cpu request 100m → 20m')).toBeTruthy()
    expect(screen.getByText('owner: Helm · release kyverno')).toBeTruthy()
    expect(screen.getByText('medium risk · a single replica')).toBeTruthy()
    expect(screen.getByText(/the next helm upgrade undoes a change/)).toBeTruthy()
    // Helm owns it: the undo is a helm rollback, never a kubectl command the
    // next upgrade would overwrite.
    expect(screen.getByText('helm rollback kyverno -n kyverno')).toBeTruthy()
    expect(screen.queryByText(/kubectl -n kyverno set resources/)).toBeNull()
    expect(screen.getByText(/Apply in the values of release kyverno/)).toBeTruthy()
    // Excluded, with the reason in words and the provider named.
    expect(screen.getAllByText('Managed by the provider').length).toBeGreaterThan(0)
    expect(screen.getByText(/provider · AKS/)).toBeTruthy()
    // Export is built: a button. What is not built is visible, disabled,
    // with Soon; a draft has nothing to verify yet. No code reaches the person.
    expect((screen.getByRole('button', { name: /Export/ }) as HTMLButtonElement).disabled).toBe(false)
    for (const label of ['Open a PR', 'Run with Autopilot']) {
      expect(screen.getByTitle(`${label} — coming soon`)).toBeTruthy()
    }
    expect(screen.queryByRole('button', { name: /Verify now/ })).toBeNull()
    expect(screen.queryByText(/single_pod|provider_managed|helm:kyverno/)).toBeNull()
    // A beta: the badge and the feedback form.
    expect(screen.getByText('Beta')).toBeTruthy()
    expect(screen.getByText('Tell us how plans work for you').closest('a')?.getAttribute('href')).toBe('https://kubebolt.io/feedback?email=ana%40example.com')
    expect(screen.getByText('Plans').closest('a')?.getAttribute('href')).toBe('/plans')
  })

  it('says when the plan does not exist', async () => {
    getPlan.mockRejectedValue(new MockApiError(404, 'plan not found'))
    renderPage()
    expect(await screen.findByText('Plan not found')).toBeTruthy()
  })

  it('shows a consolidation plan: the bill, the plan it waits for, why the node is left over, and the pool step', async () => {
    getPlan.mockResolvedValue(consolidationPlan())
    renderPage()
    await screen.findByText('Consolidation · prod-eks')
    // Amounts in the runner's locale: «−129,40 €» or «−€129.40».
    expect(screen.getByText(/^−\u2060€?129[.,]40(\s?€)?$/)).toBeInTheDocument()
    expect(screen.getByText('on the cluster bill, at list prices')).toBeInTheDocument()
    expect(screen.getByText(/^€?1[.,]?210[.,]00(\s?€)? → €?1[.,]?080[.,]60(\s?€)? · less with commitments$/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Right-sizing · prod-eks' })).toHaveAttribute('href', '/plans/pln_d6551a4c')
    expect(screen.getByText(/2 of its 3 steps are still to do/)).toBeInTheDocument()
    expect(screen.getByText('Why one node of pool general is left over')).toBeInTheDocument()
    expect(screen.getByText('leaves the pool')).toBeInTheDocument()
    expect(screen.getByText(/With today's requests, none is left over/)).toBeInTheDocument()
    expect(screen.getByText('WAITS FOR RIGHT-SIZING')).toBeInTheDocument()
    expect(screen.getByText('owner: not identified')).toBeInTheDocument()
    expect(screen.getByText('medium risk · one node less of headroom')).toBeInTheDocument()
    expect(screen.getByText('nodes 5 → 4')).toBeInTheDocument()
    expect(screen.getByText('the pool stays at 4 nodes')).toBeInTheDocument()
    expect(screen.getByTitle('Cordon and drain — coming soon')).toHaveAttribute('aria-disabled', 'true')
    expect(screen.getByText('A pod that cannot move')).toBeInTheDocument()
    // Never «savings», and no set-resources wording on a pool.
    expect(screen.queryByText(/saving/i)).not.toBeInTheDocument()
    expect(screen.queryByText('Edit values')).not.toBeInTheDocument()
  })

  it('links a right-sizing plan to the consolidation that removes the node, or offers to ask Kobi for one', async () => {
    const rs = kyvernoPlan({ status: 'exported', effectiveStatus: 'exported' })
    getPlan.mockResolvedValue({
      ...rs,
      leadsTo: [{ id: 'pln_cons', title: 'Consolidation · prod-eks', kind: 'consolidation', effectiveStatus: 'draft', stepCount: 1, appliedCount: 0,
        estimated: { cpuMilli: 4000, memBytes: 0, money: { monthly: 129.4, currency: 'eur' } } }],
    })
    const { unmount } = renderPage()
    await screen.findByText('What this plan frees lowers the bill once a node goes')
    expect(screen.getByRole('link', { name: /Open the consolidation/ })).toHaveAttribute('href', '/plans/pln_cons')
    unmount()

    getPlan.mockResolvedValue(rs)
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: /Ask Kobi for a consolidation plan/ }))
    await waitFor(() => expect(askInCluster).toHaveBeenCalledWith('agent:c-dev', expect.stringContaining('consolidation plan')))
    expect(openPanel).toHaveBeenCalled()
  })

  it('tells a Free org that consolidation plans come with the paid plans, with nothing to ask', async () => {
    plan.isFree = true
    try {
      getPlan.mockResolvedValue(kyvernoPlan({ status: 'exported', effectiveStatus: 'exported' }))
      renderPage()
      expect(await screen.findByText('Consolidation plans come with the paid plans.')).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /Ask Kobi for a consolidation plan/ })).not.toBeInTheDocument()
    } finally {
      plan.isFree = false
    }
  })

  it('tells the community edition that consolidation plans come with KubeBolt Cloud and Enterprise', async () => {
    plan.consolidation = false
    try {
      getPlan.mockResolvedValue(kyvernoPlan({ status: 'exported', effectiveStatus: 'exported' }))
      renderPage()
      expect(await screen.findByText('Consolidation plans come with KubeBolt Cloud and Enterprise.')).toBeInTheDocument()
      expect(screen.getByText('The capacity it frees sits idle on the same nodes')).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: /Ask Kobi for a consolidation plan/ })).not.toBeInTheDocument()
    } finally {
      plan.consolidation = true
    }
  })

  it('shows a pool step half done when its nodes go one at a time', async () => {
    const plan = consolidationPlan({ status: 'exported', effectiveStatus: 'exported', exportedAt: new Date().toISOString(), dependsOnPlans: [] })
    const step = plan.steps[0]
    const params = step.params as unknown as { nodes: { name: string; zone?: string }[]; nodesAfter: number }
    params.nodes = [...params.nodes, { name: 'ip-10-0-1-88', zone: 'us-east-1a' }]
    params.nodesAfter = 3
    plan.steps = [{ ...step, riskReasons: ['less_headroom'], nodesGone: ['ip-10-0-3-41'] }]
    getPlan.mockResolvedValue(plan)
    renderPage()
    expect(await screen.findByText('IN PROGRESS · 1 OF 2')).toBeInTheDocument()
    expect(screen.getByText('✓ gone')).toBeInTheDocument()
    expect(screen.getByText(/1 of 2 nodes gone; 1 more to go/)).toBeInTheDocument()
  })
})
