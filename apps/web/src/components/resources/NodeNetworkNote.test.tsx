import { describe, it, expect } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { NodeNetworkNote, NODE_NETWORK_NOTE_MAX_LINKS } from './NodeNetworkNote'

// Twelve AKS-style names: long enough that shortenNodeName elides the middle.
const nodes = Array.from({ length: 12 }, (_, i) => `aks-nodepool1-12345678-vmss${String(i).padStart(6, '0')}`)

function renderNote(nodeNames: string[], scope: 'pod' | 'workload' = 'workload') {
  return render(
    <MemoryRouter>
      <NodeNetworkNote title="Network traffic" scope={scope} nodeNames={nodeNames} />
    </MemoryRouter>,
  )
}

const nodeLinks = () => screen.queryAllByRole('link').filter((a) => a.getAttribute('href')?.startsWith('/nodes/_/'))

describe('NodeNetworkNote', () => {
  it('links the first nodes of a DaemonSet on twelve and folds the rest into "and N more"', () => {
    renderNote(nodes)
    expect(nodeLinks()).toHaveLength(NODE_NETWORK_NOTE_MAX_LINKS)
    expect(screen.getByRole('button', { name: '6 more' })).toBeTruthy()
  })

  it('"and N more" shows every node in the same note', () => {
    renderNote(nodes)
    fireEvent.click(screen.getByRole('button', { name: '6 more' }))
    expect(nodeLinks()).toHaveLength(12)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('shows a shortened name but links the full one, kept in the tooltip', () => {
    renderNote(nodes.slice(0, 1))
    const link = nodeLinks()[0]
    expect(link.getAttribute('href')).toBe('/nodes/_/aks-nodepool1-12345678-vmss000000?tab=monitor')
    expect(link.getAttribute('title')).toBe('aks-nodepool1-12345678-vmss000000')
    expect(link.textContent).not.toBe('aks-nodepool1-12345678-vmss000000')
    expect(link.textContent).toContain('vmss000000')
  })

  it('joins the last two with "and" when nothing is folded', () => {
    renderNote(['node-a', 'node-b', 'node-c'])
    expect(nodeLinks()).toHaveLength(3)
    expect(screen.getByText(/See it on/).textContent).toContain('node-a, node-b and node-c.')
  })

  it('without pods says to look at each node', () => {
    renderNote([])
    expect(nodeLinks()).toHaveLength(0)
    expect(screen.getByText(/each node's Monitor tab/)).toBeTruthy()
  })

  it('a pod names its one node', () => {
    renderNote(['kind-worker'], 'pod')
    expect(screen.getByText(/This pod runs with hostNetwork/)).toBeTruthy()
    expect(nodeLinks()).toHaveLength(1)
  })
})
