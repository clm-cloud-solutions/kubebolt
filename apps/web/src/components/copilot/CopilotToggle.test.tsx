import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { CopilotToggle, useHideCopilotLauncher } from './CopilotToggle'

// UI-1: the launcher floats bottom-right above everything, where a connector
// drawer keeps Save and Pause. Clicks on those landed on the launcher — and
// not only on its button: its invisible 180px tooltip was a pointer target
// too, on every page.

vi.mock('@/contexts/CopilotContext', () => ({
  useCopilot: () => ({ config: { enabled: true }, isOpen: false, togglePanel: () => {} }),
}))

function Panel() {
  useHideCopilotLauncher()
  return <div>panel</div>
}

describe('CopilotToggle', () => {
  it('lets clicks through everywhere but the round button', () => {
    render(<CopilotToggle />)
    const button = screen.getByRole('button', { name: 'Open Kobi' })
    const box = button.closest('.fixed') as HTMLElement
    expect(box.className).toContain('pointer-events-none')
    expect(button.parentElement?.className).toContain('pointer-events-auto')
  })

  it('steps aside while a panel asks it to, and comes back when the last one closes', () => {
    const { rerender } = render(
      <>
        <CopilotToggle />
        <Panel />
        <Panel />
      </>,
    )
    expect(screen.queryByRole('button', { name: 'Open Kobi' })).not.toBeInTheDocument()
    rerender(
      <>
        <CopilotToggle />
        <Panel />
      </>,
    )
    expect(screen.queryByRole('button', { name: 'Open Kobi' })).not.toBeInTheDocument()
    rerender(<CopilotToggle />)
    expect(screen.getByRole('button', { name: 'Open Kobi' })).toBeInTheDocument()
  })
})
