import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

// Opened as the onboarding's «Advanced setup»: it reuses
// the token the simple flow issued instead of issuing a second one, says how
// long that token works unused, and has a way back.

vi.mock('@/contexts/AuthContext', () => ({ useAuth: () => ({ isSignupEnabled: false }) }))
vi.mock('@/services/onboarding', () => ({ reportOnboardingStep: () => {} }))

const issue = vi.fn()
vi.mock('@/services/api', async (importOriginal) => {
  const orig = await importOriginal<typeof import('@/services/api')>()
  return {
    ...orig,
    api: {
      listTeams: () => Promise.resolve([]),
      getAgentInstallDefaults: () => Promise.resolve({ deploymentMode: 'in-cluster', agentNamespace: 'kubebolt-system' }),
      getAgentAuthInfo: () => Promise.resolve({ enforcement: 'enforced', tenants: [{ id: 'org-1', name: 'acme' }] }),
      listClusters: () => Promise.resolve([]),
      issueAgentTokenAndMaterializeSecret: (b: unknown) => issue(b),
    },
  }
})

import { AddClusterWizard } from './AddClusterWizard'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const fromSimple = { token: 'kbi_fromsimple', tokenPrefix: 'kbi_from', tokenLabel: 'first-cluster prod', tenantId: 'org-1', unusedExpiresAt: '2026-11-25T08:00:00Z' }

beforeEach(() => issue.mockReset())

describe('AddClusterWizard opened from the onboarding', () => {
  it('shows the simple flow\'s token without issuing another', async () => {
    wrap(<AddClusterWizard onClose={() => {}} onBack={() => {}} initialToken={fromSimple} initialClusterName="prod" />)
    expect(await screen.findByText(/the token the simple setup issued/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /re-generate token/i })).toBeInTheDocument()
    expect(screen.getByText(/works until .* unless an agent uses it first/)).toBeInTheDocument()
    expect(issue).not.toHaveBeenCalled()
  })

  it('goes back to the simple setup', async () => {
    const onBack = vi.fn()
    wrap(<AddClusterWizard onClose={() => {}} onBack={onBack} initialToken={fromSimple} />)
    fireEvent.click(await screen.findByRole('button', { name: /back to the simple setup/i }))
    expect(onBack).toHaveBeenCalledTimes(1)
  })

  it('hands a re-generated token back', async () => {
    issue.mockResolvedValue({ ...fromSimple, token: 'kbi_new' })
    const onTokenIssued = vi.fn()
    wrap(<AddClusterWizard onClose={() => {}} onBack={() => {}} initialToken={fromSimple} onTokenIssued={onTokenIssued} />)
    const regen = await screen.findByRole('button', { name: /re-generate token/i })
    await waitFor(() => expect(regen).not.toBeDisabled())
    fireEvent.click(regen)
    await waitFor(() => expect(onTokenIssued).toHaveBeenCalledWith(expect.objectContaining({ token: 'kbi_new' })))
  })
})

describe('AddClusterWizard opened on its own', () => {
  it('has no way back and issues nothing until asked', async () => {
    wrap(<AddClusterWizard onClose={() => {}} />)
    expect(await screen.findByRole('button', { name: /^generate token$/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /back to the simple setup/i })).toBeNull()
    expect(issue).not.toHaveBeenCalled()
  })
})
