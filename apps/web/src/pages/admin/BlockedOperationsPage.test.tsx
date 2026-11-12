import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { api, ApiError, type BlockedOperationsView } from '@/services/api'
import { BlockedOperationsPage, summarize } from './BlockedOperationsPage'

const view = (connectors: Record<string, string[]> = {}): BlockedOperationsView => ({
  connectors,
  available: [
    { id: 'aws', name: 'Amazon Web Services' },
    { id: 'azure', name: 'Microsoft Azure' },
  ],
})

function renderTab() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <BlockedOperationsPage />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('BlockedOperationsPage', () => {
  it('shows each connector with what it blocks, and saves only the one that changed', async () => {
    vi.spyOn(api, 'getBlockedOperations').mockResolvedValue(view({ azure: ['*'] }))
    const put = vi.spyOn(api, 'putBlockedOperations').mockResolvedValue(view({ aws: ['s3.getobject'], azure: ['*'] }))
    renderTab()
    expect(await screen.findByText('Amazon Web Services')).toBeTruthy()
    expect(screen.getByText('nothing blocked')).toBeTruthy()
    expect(screen.getByText('paused: every call is refused')).toBeTruthy()

    fireEvent.change(document.getElementById('blocked-aws')!, { target: { value: ' S3.GetObject \n\n' } })
    fireEvent.click(screen.getByRole('button', { name: /Save/ }))
    await waitFor(() => expect(put).toHaveBeenCalledWith({ connectors: { aws: ['s3.getobject'] } }))
    expect(await screen.findByText(/applies from the next call/)).toBeTruthy()
  })

  it('shows a refused entry under its connector', async () => {
    vi.spyOn(api, 'getBlockedOperations').mockResolvedValue(view())
    vi.spyOn(api, 'putBlockedOperations').mockRejectedValue(
      new ApiError(400, 'validation failed', { field: 'connectors.aws', message: '"s3 get" is not an operation name' }),
    )
    renderTab()
    await screen.findByText('Amazon Web Services')
    fireEvent.change(document.getElementById('blocked-aws')!, { target: { value: 's3 get' } })
    fireEvent.click(screen.getByRole('button', { name: /Save/ }))
    expect(await screen.findByText(/is not an operation name/)).toBeTruthy()
  })

  it('summarizes a list', () => {
    expect(summarize([])).toBe('nothing blocked')
    expect(summarize(['s3.*', 'kms.*'])).toBe('2 blocked')
    expect(summarize(['s3.*', '*'])).toBe('paused: every call is refused')
  })
})
