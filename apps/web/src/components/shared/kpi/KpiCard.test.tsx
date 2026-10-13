import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { KpiCard } from './KpiCard'

describe('KpiCard verdict', () => {
  it('draws the green bevel while the verdict says all is well', () => {
    const { container } = render(<KpiCard primary value={98} status={{ label: 'Healthy', tone: 'ok' }} />)
    expect(container.firstElementChild).toHaveClass('kb-kpi-primary')
  })

  it('draws a grey bevel, never the green one, when no verdict can be given', () => {
    const { container } = render(<KpiCard primary value="—" status={{ label: 'No contact', tone: 'unknown' }} />)
    const card = container.firstElementChild
    expect(card).toHaveClass('kb-kpi-verdict', 'kb-kpi-verdict-unknown')
    expect(card).not.toHaveClass('kb-kpi-primary')
    expect(screen.getByText('No contact')).toBeInTheDocument()
  })
})

describe('KpiCard figure row', () => {
  it('leaves the unit the whole width when the card has no corner link', () => {
    render(<KpiCard value={83} unit="workloads affected" />)
    expect(screen.getByText('workloads affected').parentElement).not.toHaveClass('pr-16')
  })

  it('keeps clear of the corner link when there is one', () => {
    render(
      <MemoryRouter>
        <KpiCard value={2} unit="/ 2" link={{ to: '/nodes', text: 'view all' }} />
      </MemoryRouter>,
    )
    expect(screen.getByText('/ 2').parentElement).toHaveClass('pr-16')
  })
})
