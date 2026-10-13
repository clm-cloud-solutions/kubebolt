import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { OutOfContactBanner } from './OutOfContactBanner'

describe('OutOfContactBanner', () => {
  it('dates the last known state and does not claim a cause', () => {
    const since = new Date(Date.now() - 14 * 60_000).toISOString()
    render(<OutOfContactBanner since={since} />)
    const banner = screen.getByRole('status')
    expect(banner).toHaveTextContent(/No contact with this cluster since .+ \(14m ago\)\./)
    expect(banner).toHaveTextContent('What you see is its last known state.')
    expect(banner).toHaveTextContent('cannot tell whether the agent, its node or the network went down')
    expect(banner).not.toHaveTextContent(/not the cluster being down|cluster is up/i)
  })

  it('still says it when the moment contact was lost is unknown', () => {
    render(<OutOfContactBanner />)
    expect(screen.getByRole('status')).toHaveTextContent(/^No contact with this cluster\. What you see/)
  })
})
