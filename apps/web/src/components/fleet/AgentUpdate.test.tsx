import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { AgentUpdateBadge, AgentUpdateModal, AgentUpdateTag } from './AgentUpdate'
import type { AgentUpdate } from '@/types/kubernetes'

const command =
  'helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \\\n' +
  '  --namespace kubebolt-system --version 1.4.9 --reset-then-reuse-values'
const recommended: AgentUpdate = {
  version: '1.4.7',
  latest: '1.4.9',
  level: 'recommended',
  reasons: ['It stores each pod network twice.', 'It never forgets a flow key.'],
  upgrade: command,
}
const available: AgentUpdate = { version: '1.4.8', latest: '1.4.9', level: 'available', upgrade: command }
// Agents before 1.4.4 all report 1.0.0: the server sends no version, only
// what is known.
const unreported: AgentUpdate = { before: '1.4.4', latest: '1.4.9', level: 'recommended', reasons: ['r1', 'r2'], upgrade: command }

// debt-24: KubeBolt says the agent is out of date and how to upgrade it; it
// never upgrades it.
describe('AgentUpdate', () => {
  it('shows nothing for a current agent, a neutral chip when a release exists, amber when it fixes something', () => {
    const { rerender, container } = render(<AgentUpdateBadge update={{ version: '1.4.9', latest: '1.4.9', level: 'current' }} onOpen={() => {}} />)
    expect(container.textContent).toBe('')
    rerender(<AgentUpdateBadge update={available} onOpen={() => {}} />)
    expect(screen.getByRole('button').textContent).toContain('Agent 1.4.8 · update available')
    rerender(<AgentUpdateBadge update={recommended} onOpen={() => {}} />)
    expect(screen.getByRole('button').textContent).toContain('Agent 1.4.7 · update recommended')
  })

  it('never names 1.0.0 for an agent that does not report its version', () => {
    render(<AgentUpdateBadge update={unreported} onOpen={() => {}} />)
    expect(screen.getByRole('button').textContent).toContain('Agent <1.4.4 · update recommended')
  })

  it('Fleet shows a tag only when an upgrade fixes something', () => {
    const { container, rerender } = render(<AgentUpdateTag update={available} />)
    expect(container.textContent).toBe('')
    rerender(<AgentUpdateTag update={unreported} />)
    expect(container.textContent).toContain('Agent <1.4.4')
  })

  it('the dialog gives the reasons and the server’s Helm command to copy', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    render(<AgentUpdateModal clusterName="dipres-prod" update={recommended} onClose={() => {}} />)
    expect(screen.getByText('dipres-prod')).toBeTruthy()
    expect(screen.getByText('Agent 1.4.7 → 1.4.9')).toBeTruthy()
    expect(screen.getByText('It stores each pod network twice.')).toBeTruthy()
    expect(screen.getByText('It never forgets a flow key.')).toBeTruthy()
    expect(screen.getByText(/KubeBolt never upgrades the agent itself/)).toBeTruthy()
    // Seen live: kubectl pointed at another cluster.
    expect(screen.getByText(/runs against kubectl's current context/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(command))
    expect(await screen.findByText('Copied')).toBeTruthy()
    expect(screen.getByRole('link', { name: /What changed in 1.4.9/ }).getAttribute('href')).toBe(
      'https://github.com/clm-cloud-solutions/kubebolt/releases/tag/agent-v1.4.9',
    )
  })

  it('the dialog explains why the version is unknown', () => {
    render(<AgentUpdateModal clusterName="kind-lab" update={unreported} onClose={() => {}} />)
    expect(screen.getByText('Agent before 1.4.4 → 1.4.9')).toBeTruthy()
    expect(screen.getByText(/every agent before 1.4.4 sends 1.0.0/)).toBeTruthy()
  })
})
