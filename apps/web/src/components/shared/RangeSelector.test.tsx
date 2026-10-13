import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { RangeSelector } from './RangeSelector'

function renderSelector(value: number) {
  const onChange = vi.fn()
  render(<RangeSelector value={value} onChange={onChange} />)
  return onChange
}

describe('RangeSelector', () => {
  it('names the range in force and lists every range, the days apart', async () => {
    renderSelector(15)
    fireEvent.click(screen.getByRole('button', { name: /Last 15 minutes/ }))
    expect(screen.getAllByRole('menuitemradio').map((i) => i.textContent)).toEqual([
      'Last 5 minutes',
      'Last 15 minutes',
      'Last hour',
      'Last 6 hours',
      'Last 24 hours',
      'Last 7 days',
      'Last 14 days',
      'Last 30 days',
    ])
    expect(screen.getByRole('menuitemradio', { name: 'Last 15 minutes' })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getAllByRole('separator')).toHaveLength(1)
  })

  it('applies a range and closes', () => {
    const onChange = renderSelector(15)
    fireEvent.click(screen.getByRole('button', { name: /Last 15 minutes/ }))
    fireEvent.click(screen.getByRole('menuitemradio', { name: 'Last 7 days' }))
    expect(onChange).toHaveBeenCalledWith(10080)
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('locks no range: a self-hosted install has no plan cap', () => {
    renderSelector(60)
    fireEvent.click(screen.getByRole('button', { name: /Last hour/ }))
    expect(screen.getByRole('menuitemradio', { name: 'Last 30 days' })).toBeEnabled()
    expect(screen.queryByText(/Your plan keeps/)).toBeNull()
  })
})
