import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import { ConversationList } from './ConversationList'

// A conversation stores the CONTEXT NAME of the cluster it was held on. For an
// agent-proxy cluster that name is `agent:<uid>`, so the history list was
// printing rows stamped `AGENT:5368E0D2-0A3…` — an identifier, not a name. It
// tells the operator nothing and reads like a bug.

vi.mock('@/contexts/CopilotContext', async () => {
  const actual = await vi.importActual<typeof import('@/contexts/CopilotContext')>(
    '@/contexts/CopilotContext',
  )
  return {
    ...actual,
    useCopilot: () => ({
      resumeConversation: vi.fn(),
      newConversation: vi.fn(),
      conversationId: null,
      activeClusterContext: 'agent:5368e0d2-0a38-490d-afac-04cd73bb9d04',
    }),
  }
})

const listConversations = vi.fn()
const listClusters = vi.fn()
const getClusterNames = vi.fn()
vi.mock('@/services/api', () => ({
  api: {
    listConversations: (...a: unknown[]) => listConversations(...a),
    listClusters: () => listClusters(),
    getClusterNames: () => getClusterNames(),
    deleteConversation: vi.fn(),
    patchConversation: vi.fn(),
  },
}))

function renderList() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <ConversationList onClose={vi.fn()} />
    </QueryClientProvider>,
  )
}

const row = {
  id: 'c1',
  title: 'flota consulta insights críticos',
  preview: 'Como esta mi flota?',
  updatedAt: new Date().toISOString(),
  messageCount: 8,
  clusterId: 'agent:5368e0d2-0a38-490d-afac-04cd73bb9d04',
}

beforeEach(() => {
  vi.clearAllMocks()
  listConversations.mockResolvedValue([row])
  getClusterNames.mockResolvedValue({})
})

describe('ConversationList cluster chip', () => {
  it('names the cluster instead of printing its identifier', async () => {
    listClusters.mockResolvedValue([
      {
        context: 'agent:5368e0d2-0a38-490d-afac-04cd73bb9d04',
        name: 'kind-kubebolt-dev',
        displayName: 'kind-kubebolt-dev',
        source: 'agent-proxy',
        active: true,
      },
    ])

    renderList()

    await waitFor(() => expect(screen.getByText(/kind-kubebolt-dev/)).toBeTruthy())
    expect(screen.queryByText(/5368e0d2/i)).toBeNull()
  })

  it('draws no chip at all when the cluster can no longer be named', async () => {
    // De-registered cluster: neither the live list nor the durable map knows it.
    // A raw `agent:<uid>` in its place is noise, so the chip is dropped and the
    // row still reads (time · N msg).
    listClusters.mockResolvedValue([])

    renderList()

    await waitFor(() => expect(screen.getByText(/flota consulta insights/)).toBeTruthy())
    expect(screen.queryByText(/5368e0d2/i)).toBeNull()
    expect(screen.getByText(/8 msg/i)).toBeTruthy()
  })

  it('drops the chip for an agent cluster that registered without a name', async () => {
    // The cluster IS live, but it never sent `kubebolt.io/cluster-name`, so the
    // only label available is its own context — which is the identifier again.
    listClusters.mockResolvedValue([
      {
        context: 'agent:5368e0d2-0a38-490d-afac-04cd73bb9d04',
        name: 'agent:5368e0d2-0a38-490d-afac-04cd73bb9d04',
        source: 'agent-proxy',
        active: true,
      },
    ])

    renderList()

    await waitFor(() => expect(screen.getByText(/flota consulta insights/)).toBeTruthy())
    expect(screen.queryByText(/5368e0d2/i)).toBeNull()
  })

  it('keeps a plain kubeconfig context — that one IS a name', async () => {
    listConversations.mockResolvedValue([{ ...row, clusterId: 'kind-kubebolt-lab' }])
    listClusters.mockResolvedValue([{ context: 'kind-kubebolt-lab', name: 'kind-kubebolt-lab' }])

    renderList()

    await waitFor(() => expect(screen.getByText('kind-kubebolt-lab')).toBeTruthy())
  })
})
