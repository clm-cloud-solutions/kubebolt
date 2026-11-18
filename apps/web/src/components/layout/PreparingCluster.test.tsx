import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ClusterProgress } from '@/types/kubernetes'

const progress = vi.fn<[], Promise<ClusterProgress>>()
vi.mock('@/services/api', () => ({
  api: { getClusterProgress: () => progress() },
}))
vi.mock('@/components/shared/MetricChart', () => ({
  MetricChart: ({ title }: { title: string }) => <div data-testid="metric-chart">{title}</div>,
  METRIC_ACCENTS: { cpu: ['#0f0'], memory: ['#00f'] },
}))

import { PreparingCluster } from './PreparingCluster'

const loading: ClusterProgress = {
  context: 'agent:c1',
  state: 'connecting',
  startedAt: new Date(Date.now() - 12_000).toISOString(),
  agents: 2,
  accessChecked: true,
  readable: 29,
  probed: 31,
  types: [
    { kind: 'Pod', synced: true, loaded: 0, total: 0 },
    { kind: 'Deployment', synced: true, loaded: 612, total: 612 },
    { kind: 'ReplicaSet', synced: false, loaded: 2000, total: 5632 },
  ],
}

function renderIt() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <PreparingCluster name="prod-k3s" context="agent:c1" />
    </QueryClientProvider>,
  )
}

describe('PreparingCluster', () => {
  beforeEach(() => progress.mockReset())

  it('says what is happening, why, how far, and what has happened', async () => {
    progress.mockResolvedValue(loading)
    renderIt()
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent('Loading 5,632 ReplicaSets, the largest collection in this cluster'),
    )
    expect(screen.getByText(/keeps a live copy of the cluster/)).toBeInTheDocument()
    expect(screen.getByRole('progressbar')).toHaveAttribute('aria-valuenow')
    expect(screen.getByText('ReplicaSets · 2,000 of 5,632')).toBeInTheDocument()
    expect(screen.getByText('2 kinds not readable')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByText('2 agents connected')).toBeInTheDocument())
    expect(screen.getByText('Access checked · 2 kinds not readable')).toBeInTheDocument()
    expect(screen.getAllByTestId('metric-chart')).toHaveLength(2)
    expect(screen.getByText('Metrics come from the agents, not from this load.')).toBeInTheDocument()
    // What travels is said as it is: Secret values are loaded, never shown.
    expect(screen.getByText(/through the agent’s outbound channel/)).toHaveTextContent(
      /Secret values are never displayed, and revealing one takes an editor and is audited/,
    )
  })

  it('says when a connect stopped and when it tries again', async () => {
    progress.mockResolvedValue({
      ...loading,
      state: 'failed',
      error: 'no response from the cluster for 25s',
      retryAt: new Date(Date.now() + 30_000).toISOString(),
    })
    renderIt()
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('The connect stopped'))
    expect(screen.getByText(/no response from the cluster for 25s KubeBolt tries again at/)).toBeInTheDocument()
  })
})
