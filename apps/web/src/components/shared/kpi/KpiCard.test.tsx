import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
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
