import { describe, it, expect, beforeEach } from 'vitest'
import { render, renderHook } from '@testing-library/react'
import { CountUp } from './CountUp'
import { resetEnterAnimations, useEnterMode } from '@/utils/enterOnce'

// The page marks itself as entered when a figure first shows a real number,
// not when the card mounts with a placeholder (the rows mount before data).
function Row({ value, mode = 'play' }: { value: string | number; mode?: 'play' | 'skip' }) {
  return (
    <main data-kb-enter={mode} data-kb-enter-page="/capacity">
      <CountUp value={value} />
    </main>
  )
}
const modeOf = (page: string) => renderHook(() => useEnterMode(page)).result.current

describe('CountUp', () => {
  beforeEach(() => resetEnterAnimations())

  it('shows the value as is (the test environment does not animate)', () => {
    const { container } = render(<Row value="$743" />)
    expect(container.textContent).toBe('$743')
  })

  it('does not mark the page while the figure is a placeholder', () => {
    render(<Row value="—" />)
    expect(modeOf('/capacity')).toBe('play')
  })

  it('does not mark the page on a zero', () => {
    render(<Row value={0} />)
    expect(modeOf('/capacity')).toBe('play')
  })

  it('marks the page once the real number arrives', () => {
    const { rerender } = render(<Row value="—" />)
    rerender(<Row value="9.5" />)
    expect(modeOf('/capacity')).toBe('skip')
  })

  it('does not mark anything on a skipped visit', () => {
    render(<Row value={42} mode="skip" />)
    expect(modeOf('/capacity')).toBe('play')
  })
})
