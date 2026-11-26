import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { CostRightsizing } from './CostRightsizing'
import type { Recommendation } from '@/hooks/useRightSizing'

vi.mock('@/components/copilot/AskCopilotButton', () => ({ AskCopilotButton: () => null }))

const over = (name: string, namespace: string, kind: string, request: number, suggest: number, managedBy?: string): Recommendation => ({
  name,
  namespace,
  kind,
  severity: 'warning',
  reason: 'over-provisioned',
  cpu: { state: 'over', request, limit: 0, p95: suggest - 5, suggest },
  mem: { state: 'ok', request: 0, limit: 0, p95: 0, suggest: 0 },
  managedBy,
})

describe('CostRightsizing', () => {
  // P17 (plans-12): what the cloud provider reconciles is named, priced at
  // nothing and listed last; only the rest is priced.
  it('names the provider\'s workloads and does not price them', () => {
    render(
      <MemoryRouter>
        <CostRightsizing
          recs={[over('azure-cns', 'kube-system', 'DaemonSet', 4000, 300, 'aks'), over('web', 'shop', 'Deployment', 2000, 600)]}
          rates={{ cpuCoreHourly: 0.03, ramGiBHourly: 0.004, available: true }}
          savingsMonthly={30.66}
          rightSizing="recs"
          minHistoryHours={48}
        />
      </MemoryRouter>,
    )
    const rows = screen.getAllByRole('listitem')
    expect(rows[0].textContent).toContain('web')
    expect(rows[0].textContent).toMatch(/\/mo/)
    expect(rows[1].textContent).toContain('azure-cns')
    expect(rows[1].textContent).toContain('managed by AKS · only the provider can resize it')
    expect(rows[1].textContent).toContain('not counted')
    expect(rows[1].textContent).not.toMatch(/\/mo/)
  })

  // What rightsizing frees is the price of capacity, not a saving: the bill
  // falls only once nodes go (26-nov).
  it('prices what it frees at list prices and never calls it a saving', () => {
    const { container } = render(
      <MemoryRouter>
        <CostRightsizing
          recs={[over('web', 'shop', 'Deployment', 2000, 600)]}
          rates={{ cpuCoreHourly: 0.03, ramGiBHourly: 0.004, available: true }}
          savingsMonthly={30.66}
          rightSizing="recs"
          minHistoryHours={48}
        />
      </MemoryRouter>,
    )
    expect(screen.getByText('Rightsizing: capacity freed')).toBeTruthy()
    expect(screen.getByText(/Freed in all, at list prices · a saving once nodes go/)).toBeTruthy()
    expect(container.textContent).not.toMatch(/saving[s]? *−|−\$|Total reclaimable/)
  })
})
