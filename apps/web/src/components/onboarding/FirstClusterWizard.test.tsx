import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

// The first-cluster wizard: the token comes before the command, the command is
// one block with nothing left to fill in, the address the agent dials is
// confirmed before anything is issued, and the wait ends when the cluster
// shows up.

const switchMutate = vi.fn()
vi.mock('@/hooks/useSwitchCluster', () => ({ useSwitchCluster: () => ({ mutate: switchMutate, isPending: false }) }))
vi.mock('@/components/admin/AddClusterWizard', () => ({ AddClusterWizard: () => <div>advanced wizard</div> }))
vi.mock('@/components/admin/AddKubeconfigModal', () => ({ AddKubeconfigModal: () => <div>kubeconfig import</div> }))

const issue = vi.fn()
let clusters: { context: string; name: string; displayName?: string }[] = []
let defaults: Record<string, unknown> = {}
vi.mock('@/services/api', async (importOriginal) => {
  const orig = await importOriginal<typeof import('@/services/api')>()
  return {
    ...orig,
    api: {
      getAgentInstallDefaults: () => Promise.resolve(defaults),
      issueAgentTokenAndMaterializeSecret: (b: unknown) => issue(b),
      listClusters: () => Promise.resolve(clusters),
    },
  }
})

import { FirstClusterWizard } from './FirstClusterWizard'

let qc: QueryClient
function wrap(ui: ReactNode) {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  clusters = []
  defaults = { deploymentMode: 'external', externalEndpoint: 'kubebolt.lan:9090', agentNamespace: 'kubebolt-system' }
  issue.mockReset()
  switchMutate.mockReset()
})

async function nameAndContinue(name = 'prod-eu') {
  const input = await screen.findByLabelText('Name your cluster')
  fireEvent.change(input, { target: { value: name } })
  const cont = screen.getByRole('button', { name: /continue/i })
  await waitFor(() => expect(cont).not.toBeDisabled())
  fireEvent.click(cont)
}

describe('FirstClusterWizard', () => {
  it('issues the token itself and shows one block with nothing left to fill in', async () => {
    issue.mockResolvedValue({ token: 'kbi_supersecret' })
    wrap(<FirstClusterWizard onClose={() => {}} />)
    expect(screen.getByRole('button', { name: /continue/i })).toBeDisabled()
    await nameAndContinue()

    const copyAll = await screen.findByRole('button', { name: /copy all/i })
    expect(copyAll).toBeInTheDocument()
    const block = document.querySelector('pre')!.textContent!
    expect(block).toContain('helm upgrade --install kubebolt-agent')
    expect(block).toContain('--set cluster.name=prod-eu')
    expect(block).toContain('--set backendUrl=kubebolt.lan:9090')
    expect(block).toContain('--set tls.enabled=false')
    expect(block).not.toMatch(/<[A-Za-z_-]+>/)
    // Masked until the person asks to see it.
    expect(block).not.toContain('kbi_supersecret')
  })

  // Self-hosted on a laptop the browser reaches the API on localhost, so the
  // backend has no address to offer: the laptop path suggests the one Docker
  // Desktop gives a local cluster, and a loopback address is refused.
  it('suggests host.docker.internal on the laptop path and refuses localhost', async () => {
    defaults = { deploymentMode: 'external', agentNamespace: 'kubebolt-system' }
    issue.mockResolvedValue({ token: 't' })
    wrap(<FirstClusterWizard initialWhere="local" onClose={() => {}} />)
    const address = await screen.findByLabelText('Where the agent reaches KubeBolt')
    expect(address).toHaveValue('host.docker.internal:9090')

    fireEvent.change(address, { target: { value: 'localhost:9090' } })
    expect(screen.getByText(/own pod/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /continue/i })).toBeDisabled()

    fireEvent.change(address, { target: { value: 'host.docker.internal:9090' } })
    const cont = screen.getByRole('button', { name: /continue/i })
    await waitFor(() => expect(cont).not.toBeDisabled())
    fireEvent.click(cont)
    await screen.findByRole('button', { name: /copy all/i })
    expect(document.body.textContent).toContain('--set backendUrl=host.docker.internal:9090')
  })

  it('asks for an address on the own-cluster path when the backend has none to offer', async () => {
    defaults = { deploymentMode: 'external', agentNamespace: 'kubebolt-system' }
    wrap(<FirstClusterWizard onClose={() => {}} />)
    fireEvent.change(await screen.findByLabelText('Name your cluster'), { target: { value: 'prod-eu' } })
    expect(screen.getByLabelText('Where the agent reaches KubeBolt')).toHaveValue('')
    expect(screen.getByRole('button', { name: /continue/i })).toBeDisabled()
  })

  // An operator-configured ingest URL is authoritative: no address to confirm,
  // and the agent dials it over TLS.
  it('uses the configured ingest URL as is', async () => {
    defaults = { deploymentMode: 'in-cluster', agentIngestUrl: 'agent.example.com:443', agentNamespace: 'kubebolt-system' }
    issue.mockResolvedValue({ token: 't' })
    wrap(<FirstClusterWizard onClose={() => {}} />)
    await nameAndContinue()
    await screen.findByRole('button', { name: /copy all/i })
    expect(screen.queryByLabelText('Where the agent reaches KubeBolt')).not.toBeInTheDocument()
    const block = document.querySelector('pre')!.textContent!
    expect(block).toContain('--set backendUrl=agent.example.com:443')
    expect(block).toContain('--set tls.enabled=true')
  })

  it('offers kind, k3d and minikube on the laptop path', async () => {
    issue.mockResolvedValue({ token: 't' })
    wrap(<FirstClusterWizard initialWhere="local" onClose={() => {}} />)
    expect(await screen.findByDisplayValue('my-laptop')).toBeInTheDocument()
    const cont = screen.getByRole('button', { name: /continue/i })
    await waitFor(() => expect(cont).not.toBeDisabled())
    fireEvent.click(cont)

    expect(await screen.findByText('kind create cluster --name kubebolt-try')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', { name: 'k3d' }))
    expect(screen.getByText('k3d cluster create kubebolt-try')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', { name: 'minikube' }))
    expect(screen.getByText('minikube start -p kubebolt-try')).toBeInTheDocument()
  })

  it('keeps the kubeconfig import one click away', async () => {
    wrap(<FirstClusterWizard onClose={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: /import a kubeconfig/i }))
    expect(screen.getByText('kubeconfig import')).toBeInTheDocument()
  })

  it('ends the wait when a new cluster shows up and offers to open it', async () => {
    issue.mockResolvedValue({ token: 't' })
    clusters = [{ context: 'old', name: 'old' }]
    wrap(<FirstClusterWizard onClose={() => {}} />)
    await nameAndContinue()
    fireEvent.click(await screen.findByRole('button', { name: /i ran it/i }))
    expect(await screen.findByText('Waiting for prod-eu')).toBeInTheDocument()

    clusters = [{ context: 'old', name: 'old' }, { context: 'agent-prod', name: 'agent-prod', displayName: 'prod-eu' }]
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ['clusters'] })
    })
    expect(await screen.findByText('prod-eu is connected')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /open prod-eu/i }))
    expect(switchMutate).toHaveBeenCalledWith('agent-prod', expect.anything())
  })
})
