import { describe, it, expect } from 'vitest'
import { act, fireEvent, render, renderHook, screen } from '@testing-library/react'
import type { ResourceItem } from '@/types/kubernetes'
import { PODS_PAGE_SIZE, PodsPager, usePodPages } from './WorkloadPodsPages'

const podsNamed = (names: string[]) => names.map((name) => ({ name, namespace: 'ns' }) as ResourceItem)
const numbered = (n: number) => podsNamed(Array.from({ length: n }, (_, i) => `agent-${i}`))

describe('usePodPages', () => {
  it('sorts by name in natural order, so db-2 comes before db-10', () => {
    const { result } = renderHook(() => usePodPages(podsNamed(['db-10', 'db-2', 'db-0', 'db-1'])))
    expect(result.current.pageItems.map((p) => p.name)).toEqual(['db-0', 'db-1', 'db-2', 'db-10'])
  })

  it('a DaemonSet on 120 nodes is 3 pages of at most 50', () => {
    const { result } = renderHook(() => usePodPages(numbered(120)))
    expect(PODS_PAGE_SIZE).toBe(50)
    expect(result.current.totalPages).toBe(3)
    expect(result.current.pageItems).toHaveLength(50)
    act(() => result.current.setPage(() => 3))
    expect(result.current.pageItems).toHaveLength(20)
    expect(result.current.pageItems[0].name).toBe('agent-100')
  })

  it('moves back to the last page when a refresh shrinks the list', () => {
    const { result, rerender } = renderHook(({ pods }) => usePodPages(pods), { initialProps: { pods: numbered(120) } })
    act(() => result.current.setPage(() => 3))
    rerender({ pods: numbered(60) })
    expect(result.current.page).toBe(2)
    expect(result.current.pageItems).toHaveLength(10)
  })
})

describe('PodsPager', () => {
  it('is hidden with a single page', () => {
    const { container } = render(<PodsPager page={1} totalPages={1} total={12} setPage={() => {}} />)
    expect(container.firstChild).toBeNull()
  })

  it('shows the range and moves between pages', () => {
    let page = 2
    const setPage = (update: (p: number) => number) => { page = update(page) }
    render(<PodsPager page={2} totalPages={3} total={120} setPage={setPage} />)
    expect(screen.getByText('51–100 of 120')).toBeTruthy()
    expect(screen.getByText('2 / 3')).toBeTruthy()
    fireEvent.click(screen.getByTitle('Next page'))
    expect(page).toBe(3)
  })
})
