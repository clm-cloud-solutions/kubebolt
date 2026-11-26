import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { PlansListPage } from './PlansListPage'

const { listPlans, listClusters, openPanel, getPlanBillPath } = vi.hoisted(() => ({
  listPlans: vi.fn(),
  listClusters: vi.fn(),
  openPanel: vi.fn(),
  getPlanBillPath: vi.fn(),
}))
vi.mock('@/services/api', () => ({
  api: {
    listPlans: (...a: unknown[]) => listPlans(...a),
    listClusters: () => listClusters(),
    getPlanBillPath: (...a: unknown[]) => getPlanBillPath(...a),
  },
}))
vi.mock('@/contexts/AuthContext', () => ({ useAuth: () => ({ user: { email: 'ana@example.com' } }) }))
vi.mock('@/contexts/CopilotContext', () => ({ useCopilot: () => ({ openPanel }) }))
// The edition's flag, switchable per test: the same tests run in both builds.
const edition = vi.hoisted(() => ({ consolidation: true }))
vi.mock('@/ee/registry', () => ({
  get eeConsolidationPlans() {
    return edition.consolidation
  },
}))

const row = {
  id: 'pln_1', title: 'Plan de ahorro Kyverno', kind: 'rightsizing', clusterId: 'c-dev', clusterName: 'kind-kubebolt-dev',
  version: 1, status: 'draft', effectiveStatus: 'draft', createdAt: '2026-11-25T19:00:00Z',
  expiresAt: new Date(Date.now() + 24 * 3_600_000).toISOString(), createdBy: 'admin',
  estimated: { cpuMilli: 330, memBytes: 0, money: { monthly: 6.97, currency: 'eur' } }, stepCount: 4, appliedCount: 0, excludedCount: 0, maxRisk: 'medium',
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <PlansListPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  getPlanBillPath.mockResolvedValue({ clusterId: 'c-dev', bill: null, rightsizing: [], consolidation: [], reachable: null })
  listClusters.mockResolvedValue([{ name: 'agent:c-dev', context: 'agent:c-dev', clusterId: 'c-dev', displayName: 'kind-kubebolt-dev' }])
})

describe('PlansListPage', () => {
  it('lists the live plans, marked as a beta, each linking to its view', async () => {
    listPlans.mockResolvedValue({ plans: [row] })
    renderPage()
    const link = await screen.findByText('Plan de ahorro Kyverno')
    expect(link.closest('a')?.getAttribute('href')).toBe('/plans/pln_1')
    expect(listPlans).toHaveBeenCalledWith({ status: ['draft', 'exported'], cluster: undefined, limit: 200 })
    expect(screen.getByText('Beta')).toBeTruthy()
    // What it frees: the money and what it is the price of, never «savings».
    expect(screen.getByText('Frees')).toBeTruthy()
    expect(screen.queryByText('Savings')).toBeNull()
    expect(screen.getByText(/6[.,]97/)).toBeTruthy()
    expect(screen.getByText('330m CPU')).toBeTruthy()
    expect(screen.getAllByText('kind-kubebolt-dev').length).toBeGreaterThan(0)
  })

  it('tells a plan under way from one untouched', async () => {
    listPlans.mockResolvedValue({
      plans: [
        row,
        { ...row, id: 'pln_2', title: 'Plan a medias', effectiveStatus: 'exported', appliedCount: 1 },
        { ...row, id: 'pln_3', title: 'Plan hecho', effectiveStatus: 'exported', appliedCount: 4 },
      ],
    })
    renderPage()
    await screen.findByText('Plan a medias')
    expect(screen.getByText('1 applied')).toBeTruthy()
    expect(screen.getByText('all applied')).toBeTruthy()
    // An untouched plan says nothing more than its count.
    expect(screen.getAllByText(/^(\d+|all) applied$/)).toHaveLength(2)
  })

  it('says a discarded plan was discarded, and when an unexported draft expired', async () => {
    listPlans.mockResolvedValue({
      plans: [
        { ...row, id: 'pln_d', title: 'Plan descartado', status: 'expired', effectiveStatus: 'expired', effectiveExpiredReason: 'discarded' },
        { ...row, id: 'pln_e', title: 'Plan caducado', status: 'expired', effectiveStatus: 'expired', effectiveExpiredReason: 'unexported', expiresAt: '2026-11-20T10:00:00Z' },
      ],
    })
    renderPage()
    await screen.findByText('Plan descartado')
    expect(screen.getByText('DISCARDED')).toBeTruthy()
    expect(screen.getByText('EXPIRED')).toBeTruthy()
    // The expired draft shows the day it expired; the discarded plan, none.
    const expiredRow = screen.getByText('Plan caducado').closest('tr')!
    expect(expiredRow.textContent).toMatch(/20/)
    const discardedRow = screen.getByText('Plan descartado').closest('tr')!
    expect(discardedRow.lastElementChild?.textContent).toBe('—')
  })

  it('reads every plan, or one cluster, when asked', async () => {
    listPlans.mockResolvedValue({ plans: [row] })
    renderPage()
    await screen.findByText('Plan de ahorro Kyverno')
    fireEvent.click(screen.getByRole('tab', { name: 'All' }))
    await waitFor(() => expect(listPlans).toHaveBeenCalledWith({ status: undefined, cluster: undefined, limit: 200 }))
    fireEvent.change(screen.getByLabelText('Cluster'), { target: { value: 'c-dev' } })
    await waitFor(() => expect(listPlans).toHaveBeenCalledWith({ status: undefined, cluster: 'c-dev', limit: 200 }))
  })

  it('says how to get a first plan, and opens Kobi on request', async () => {
    listPlans.mockResolvedValue({ plans: [] })
    renderPage()
    fireEvent.click(await screen.findByText('Ask Kobi for a plan'))
    expect(openPanel).toHaveBeenCalled()
  })

  it('ties a consolidation plan to the right-sizing plan it waits for, and draws the bill\'s path', async () => {
    const rs = { ...row, id: 'pln_rs', title: 'Right-sizing · prod-eks', status: 'exported', effectiveStatus: 'exported', stepCount: 3, appliedCount: 1,
      leadsTo: [{ id: 'pln_cons', title: 'Consolidation · prod-eks' }] }
    const cons = { ...row, id: 'pln_cons', title: 'Consolidation · prod-eks', kind: 'consolidation', stepCount: 1, nodesRemoved: 1,
      estimated: { cpuMilli: 4000, memBytes: 0, money: { monthly: 129.4, currency: 'eur' } }, dependsOn: [{ id: 'pln_rs', title: 'Right-sizing · prod-eks' }] }
    listPlans.mockResolvedValue({ plans: [cons, rs] })
    getPlanBillPath.mockResolvedValue({
      clusterId: 'c-dev', clusterName: 'prod-eks', bill: { monthly: 1210, currency: 'eur' },
      rightsizing: [{ id: 'pln_rs', title: 'Right-sizing · prod-eks', kind: 'rightsizing', effectiveStatus: 'exported', stepCount: 3, appliedCount: 1,
        estimated: { cpuMilli: 1690, memBytes: 0, money: null } }],
      consolidation: [{ id: 'pln_cons', title: 'Consolidation · prod-eks', kind: 'consolidation', effectiveStatus: 'draft', stepCount: 1, appliedCount: 0,
        estimated: { cpuMilli: 4000, memBytes: 0, money: null }, monthly: { monthly: 129.4, currency: 'eur' }, pools: [{ pool: 'general', nodesBefore: 5, nodesAfter: 4 }] }],
      reachable: { monthly: 1080.6, currency: 'eur' },
    })
    renderPage()
    await screen.findAllByText('Consolidation · prod-eks')
    expect(screen.getByText('depends on')).toBeInTheDocument()
    expect(screen.getByText('leads to')).toBeInTheDocument()
    expect(screen.getByText('waits for right-sizing')).toBeInTheDocument()
    expect(screen.getByText('1 node')).toBeInTheDocument()
    expect(screen.getByText(/^−\u2060€?129[.,]40(\s?€)?\/mo bill$/)).toBeInTheDocument()
    // The only cluster with live plans: its bill's path, by removing one node.
    expect(await screen.findByText(/a month, by removing one node$/)).toBeInTheDocument()
    expect(getPlanBillPath).toHaveBeenCalledWith('c-dev')
    expect(screen.getByText('pool general, 5 → 4 nodes')).toBeInTheDocument()
    expect(screen.getByText('frees 1.69 cores CPU, left idle · 1 of 3 steps applied')).toBeInTheDocument()
    // Without a price, right-sizing shows no figure, not even a zero.
    expect(screen.queryByText('not in total')).not.toBeInTheDocument()
    expect(screen.getAllByText('Soon')).toHaveLength(2)
    expect(screen.queryByText(/≈/)).not.toBeInTheDocument()
  })

  it('prices the capacity right-sizing frees, hatched and out of the total', async () => {
    listPlans.mockResolvedValue({ plans: [{ ...row, status: 'exported', effectiveStatus: 'exported', appliedCount: 4 }] })
    getPlanBillPath.mockResolvedValue({
      clusterId: 'c-dev', clusterName: 'kind-kubebolt-dev', bill: { monthly: 766.2, currency: 'eur' }, consolidation: [],
      rightsizing: [{ id: 'pln_1', title: 'Plan de ahorro Kyverno', kind: 'rightsizing', effectiveStatus: 'exported', stepCount: 4, appliedCount: 4,
        estimated: { cpuMilli: 785, memBytes: 153092096, money: { monthly: 41.2, currency: 'eur' } } }],
      reachable: { monthly: 766.2, currency: 'eur' },
    })
    renderPage()
    expect(await screen.findByText('not in total')).toBeInTheDocument()
    expect(screen.getByText(/^41[.,]20$/)).toBeInTheDocument()
    expect(screen.getByText('freed 785m CPU · 146 MiB memory, left idle')).toBeInTheDocument()
    expect(screen.getByText(/the nodes stay, so it is not in the total until consolidation removes one/)).toBeInTheDocument()
    // The bill does not move: nothing reachable to show apart.
    expect(screen.getByText(/a month today: no node is planned to go yet$/)).toBeInTheDocument()
    expect(screen.queryByText('Reachable today', { selector: 'div' })).not.toBeInTheDocument()
  })

  it('draws no bill\'s path in the community edition', async () => {
    edition.consolidation = false
    try {
      listPlans.mockResolvedValue({ plans: [row] })
      renderPage()
      await screen.findByText('Plan de ahorro Kyverno')
      expect(getPlanBillPath).not.toHaveBeenCalled()
    } finally {
      edition.consolidation = true
    }
  })

  it('adds up the fleet when several clusters have live plans, each cluster opening its own path', async () => {
    const lab = { ...row, id: 'pln_lab', title: 'Consolidation · lab', kind: 'consolidation', clusterId: 'c-lab', clusterName: 'kind-kubebolt-lab' }
    const old = { ...row, id: 'pln_old', title: 'Right-sizing · old', clusterId: 'c-old', clusterName: 'old' }
    listPlans.mockResolvedValue({ plans: [row, lab, old] })
    const cons = (id: string, pool: string, before: number, after: number, monthly: number) => ({
      id, title: id, kind: 'consolidation', effectiveStatus: 'draft', stepCount: 1, appliedCount: 0,
      estimated: { cpuMilli: 0, memBytes: 0, money: null }, monthly: { monthly, currency: 'eur' }, pools: [{ pool, nodesBefore: before, nodesAfter: after }],
    })
    getPlanBillPath.mockImplementation((cluster?: string) =>
      Promise.resolve(
        cluster
          ? { clusterId: cluster, clusterName: 'kind-kubebolt-lab', bill: { monthly: 800, currency: 'eur' }, rightsizing: [], consolidation: [cons('pln_lab', 'general', 5, 3, 200)], reachable: { monthly: 600, currency: 'eur' } }
          : {
              clusterId: '', bill: { monthly: 2010, currency: 'eur' }, reachable: { monthly: 1680.6, currency: 'eur' },
              rightsizing: [{ id: 'pln_1', title: 'Plan de ahorro Kyverno', kind: 'rightsizing', effectiveStatus: 'draft', stepCount: 4, appliedCount: 0, estimated: { cpuMilli: 330, memBytes: 0, money: null } }],
              consolidation: [cons('pln_lab', 'general', 5, 3, 200), cons('pln_dev', 'workers', 5, 4, 129.4)],
              clusters: [
                { clusterId: 'c-lab', clusterName: 'kind-kubebolt-lab', bill: { monthly: 800, currency: 'eur' }, reachable: { monthly: 600, currency: 'eur' }, off: 200, nodes: 2, plans: 1 },
                { clusterId: 'c-dev', clusterName: 'kind-kubebolt-dev', bill: { monthly: 1210, currency: 'eur' }, reachable: { monthly: 1080.6, currency: 'eur' }, off: 129.4, nodes: 1, plans: 2 },
              ],
              unpriced: [{ clusterId: 'c-old', clusterName: 'old' }],
            },
      ),
    )
    renderPage()
    expect(await screen.findByText(/a month, by removing 3 nodes in 2 clusters$/)).toBeInTheDocument()
    expect(getPlanBillPath).toHaveBeenCalledWith(undefined)
    expect(screen.getByText(/2 clusters with live plans/)).toBeInTheDocument()
    expect(screen.getByText('3 nodes less in 2 clusters')).toBeInTheDocument()
    // Several plans in a column: none of them is its link.
    expect(screen.getByText('Consolidation').closest('a')).toBeNull()
    expect(screen.getByText(/^−\u2060€?200[.,]00(\s?€)? · 2 nodes$/)).toBeInTheDocument()
    expect(screen.getByText('old has live plans but no OpenCost prices: not in these figures.')).toBeInTheDocument()

    // A cluster's row opens its own path, and narrows the list to it.
    fireEvent.click(screen.getByRole('button', { name: "Open the bill's path of kind-kubebolt-lab" }))
    expect(await screen.findByText(/^The bill's path · kind-kubebolt-lab$/)).toBeInTheDocument()
    expect(getPlanBillPath).toHaveBeenCalledWith('c-lab')
    expect((screen.getByRole('combobox', { name: 'Cluster' }) as HTMLSelectElement).value).toBe('c-lab')
  })
})
