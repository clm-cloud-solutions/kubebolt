import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import type { NamespaceWorkload } from '@/types/kubernetes'
import { NamespaceTiles, TILE_LIMIT } from './NamespaceTiles'

vi.mock('@/hooks/useNamespaceQuotas', () => ({
  useNamespaceQuotas: () => ({ quotas: {} }),
  formatQuotaValue: () => '',
}))

// One namespace with a single one-pod Deployment, ready or not.
function ns(name: string, ready: boolean): NamespaceWorkload {
  return {
    namespace: name,
    workloads: [{
      name: `${name}-app`, kind: 'Deployment', namespace: name,
      replicas: 1, readyReplicas: ready ? 1 : 0, status: ready ? 'Running' : 'Degraded',
      pods: [{ name: `${name}-pod`, status: ready ? 'Running' : 'CrashLoopBackOff', ready } as never],
    }],
  } as NamespaceWorkload
}

const tiles = () => screen.queryAllByRole('link').map((a) => a.textContent ?? '')

describe('NamespaceTiles', () => {
  it('shows the first TILE_LIMIT namespaces, the ones needing attention first, and the rest on demand', () => {
    const list = [
      ...Array.from({ length: 128 }, (_, i) => ns(`app-${String(i).padStart(3, '0')}`, true)),
      ns('zz-broken', false), ns('zz-broken-2', false), ns('zz-broken-3', false),
    ]
    render(<MemoryRouter><NamespaceTiles namespaceWorkloads={list} /></MemoryRouter>)

    expect(tiles()).toHaveLength(TILE_LIMIT)
    // Alphabetically last, but critical: they lead.
    expect(tiles().slice(0, 3).every((t) => t.startsWith('zz-broken'))).toBe(true)

    fireEvent.click(screen.getByText('Show all 131 namespaces'))
    expect(tiles()).toHaveLength(131)
    fireEvent.click(screen.getByText('Show fewer'))
    expect(tiles()).toHaveLength(TILE_LIMIT)
  })

  it('says when the cut still hides namespaces that need attention', () => {
    const list = Array.from({ length: TILE_LIMIT + 3 }, (_, i) => ns(`broken-${String(i).padStart(2, '0')}`, false))
    render(<MemoryRouter><NamespaceTiles namespaceWorkloads={list} /></MemoryRouter>)
    expect(screen.getByText(`Show all ${TILE_LIMIT + 3} namespaces · 3 more need attention`)).toBeTruthy()
  })

  it('has no button when everything fits', () => {
    render(<MemoryRouter><NamespaceTiles namespaceWorkloads={[ns('a', true), ns('b', true)]} /></MemoryRouter>)
    expect(tiles()).toHaveLength(2)
    expect(screen.queryByText(/Show all/)).toBeNull()
  })
})
