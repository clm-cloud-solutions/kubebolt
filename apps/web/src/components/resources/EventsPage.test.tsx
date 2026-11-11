import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ApiError } from '@/services/api'

const useEventsMock = vi.fn()
vi.mock('@/hooks/useEvents', () => ({ useEvents: (...a: unknown[]) => useEventsMock(...a) }))

import { EventsPage } from './EventsPage'

describe('EventsPage', () => {
  it('says access is restricted when the cluster refuses events, not that loading failed', () => {
    useEventsMock.mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new ApiError(403, 'insufficient permissions to access events'),
      refetch: vi.fn(),
      dataUpdatedAt: 0,
      isFetching: false,
    })
    render(<EventsPage />)
    expect(screen.getByText('Access Restricted')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /retry/i })).toBeNull()
  })

  it('keeps the error state for any other failure', () => {
    useEventsMock.mockReturnValue({
      data: undefined,
      isLoading: false,
      error: new ApiError(500, 'boom'),
      refetch: vi.fn(),
      dataUpdatedAt: 0,
      isFetching: false,
    })
    render(<EventsPage />)
    expect(screen.queryByText('Access Restricted')).toBeNull()
    expect(screen.getByText(/boom/)).toBeTruthy()
  })
})
