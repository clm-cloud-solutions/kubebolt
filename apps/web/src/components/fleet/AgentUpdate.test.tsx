import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { AgentUpdateBadge, AgentUpdateModal, AgentUpdateTag, agentUpgradeCommand } from './AgentUpdate'
import type { AgentUpdate } from '@/types/kubernetes'

const recommended: AgentUpdate = {
  version: '1.3.1',
  latest: '1.4.9',
  level: 'recommended',
  reasons: ['It stores each pod network twice.', 'It never forgets a flow key.'],
}
const available: AgentUpdate = { version: '1.4.8', latest: '1.4.9', level: 'available' }

// debt-24: KubeBolt says the agent is out of date and how to upgrade it; it
// never upgrades it.
describe('AgentUpdate', () => {
  it('shows nothing for a current agent, a neutral chip when a release exists, amber when it fixes something', () => {
    const { rerender, container } = render(<AgentUpdateBadge update={{ version: '1.4.9', latest: '1.4.9', level: 'current' }} onOpen={() => {}} />)
    expect(container.textContent).toBe('')
    rerender(<AgentUpdateBadge update={available} onOpen={() => {}} />)
    expect(screen.getByRole('button').textContent).toContain('Agent 1.4.8 · update available')
    rerender(<AgentUpdateBadge update={recommended} onOpen={() => {}} />)
    expect(screen.getByRole('button').textContent).toContain('Agent 1.3.1 · update recommended')
  })

  it('Fleet shows a tag only when an upgrade fixes something', () => {
    const { container, rerender } = render(<AgentUpdateTag update={available} />)
    expect(container.textContent).toBe('')
    rerender(<AgentUpdateTag update={recommended} />)
    expect(container.textContent).toContain('Agent 1.3.1')
  })

  it('the dialog gives the reasons and the Helm command to copy', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    render(<AgentUpdateModal clusterName="dipres-prod" update={recommended} onClose={() => {}} />)
    expect(screen.getByText('dipres-prod: agent 1.3.1 → 1.4.9')).toBeTruthy()
    expect(screen.getByText('It stores each pod network twice.')).toBeTruthy()
    expect(screen.getByText('It never forgets a flow key.')).toBeTruthy()
    expect(screen.getByText(/KubeBolt never upgrades the agent itself/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(agentUpgradeCommand('1.4.9')))
    expect(await screen.findByText('Copied')).toBeTruthy()
    expect(screen.getByRole('link', { name: /What changed in 1.4.9/ }).getAttribute('href')).toBe(
      'https://github.com/clm-cloud-solutions/kubebolt/releases/tag/agent-v1.4.9',
    )
  })

  it('the command upgrades the release the install wizard creates, keeping the settings', () => {
    const cmd = agentUpgradeCommand('1.4.9')
    expect(cmd).toContain('helm upgrade kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent')
    expect(cmd).toContain('--namespace kubebolt-system --version 1.4.9 --reset-then-reuse-values')
  })
})
