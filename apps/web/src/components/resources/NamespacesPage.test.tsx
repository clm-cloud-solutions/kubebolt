import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import type { ResourceParams } from '@/types/kubernetes'
import { NamespacesPage } from './NamespacesPage'

// The page asks the API for one page at a time; the mock answers like the
// API does for a cluster with 131 namespaces, filtering by name.
const ALL = Array.from({ length: 131 }, (_, i) => `ns-${String(i).padStart(3, '0')}`)
const calls: ResourceParams[] = []

vi.mock('@/hooks/useResources', () => ({
  useResources: (_type: string, params: ResourceParams = {}) => {
    calls.push(params)
    const matched = ALL.filter((n) => !params.search || n.includes(params.search))
    const page = params.page ?? 1
    const limit = params.limit ?? 50
    const items = matched.slice((page - 1) * limit, page * limit).map((name) => ({ name, namespace: '', status: 'Active', age: '1d' }))
    return { data: { kind: 'namespaces', items, total: matched.length }, isLoading: false, error: null, refetch: vi.fn(), dataUpdatedAt: 0, isFetching: false }
  },
}))
vi.mock('@/hooks/useNamespaceQuotas', () => ({ useNamespaceQuotas: () => ({ quotas: {} }), formatQuotaValue: () => '' }))
vi.mock('@/components/shared/DataFreshnessIndicator', () => ({ DataFreshnessIndicator: () => null }))

describe('NamespacesPage', () => {
  beforeEach(() => { calls.length = 0 })

  it('counts every namespace and pages through all of them', () => {
    render(<NamespacesPage />)
    expect(screen.getByText('131 total')).toBeTruthy()
    expect(screen.getByText('1–50 of 131')).toBeTruthy()
    expect(screen.getByText('ns-000')).toBeTruthy()

    fireEvent.click(screen.getByTitle('Next page'))
    fireEvent.click(screen.getByTitle('Next page'))
    expect(screen.getByText('101–131 of 131')).toBeTruthy()
    expect(screen.getByText('ns-130')).toBeTruthy()
    expect(calls[calls.length - 1]).toMatchObject({ page: 3, limit: 50 })
  })

  it('filters by name on the server and starts again on page 1', () => {
    vi.useFakeTimers()
    try {
      render(<NamespacesPage />)
      fireEvent.click(screen.getByTitle('Next page'))
      fireEvent.change(screen.getByLabelText('Filter namespaces'), { target: { value: 'ns-12' } })
      act(() => { vi.advanceTimersByTime(300) })
      expect(calls[calls.length - 1]).toMatchObject({ search: 'ns-12', page: 1 })
      expect(screen.getByText('10 total')).toBeTruthy()
      expect(screen.queryByTitle('Next page')).toBeNull()

      fireEvent.change(screen.getByLabelText('Filter namespaces'), { target: { value: 'nope' } })
      act(() => { vi.advanceTimersByTime(300) })
      expect(screen.getByText(/No namespace matches/)).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })
})
