import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { CopilotToggle, useHideCopilotLauncher } from './CopilotToggle'

// UI-1: the launcher floats bottom-right above everything, where a connector
// drawer keeps Save and Pause. Clicks on those landed on the launcher — and
// not only on its button: its invisible 180px tooltip was a pointer target
// too, on every page.

const copilot = vi.hoisted(() => ({
  value: {
    config: { enabled: true },
    isOpen: false,
    togglePanel: () => {},
    messages: [] as unknown[],
    isLoading: false,
    pendingToolCalls: [] as string[],
  },
}))

vi.mock('@/contexts/CopilotContext', () => ({
  useCopilot: () => copilot.value,
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

  it('says the state in a pill when Kobi is not watching, and only then', () => {
    const { rerender } = render(<CopilotToggle />)
    expect(screen.queryByText('Investigating')).not.toBeInTheDocument()

    copilot.value = {
      ...copilot.value,
      isLoading: true,
      pendingToolCalls: ['get_pod_logs'],
      messages: [
        { id: 'u1', role: 'user', content: 'why is it failing?', timestamp: new Date() },
        {
          id: 'a1',
          role: 'assistant',
          content: '',
          timestamp: new Date(),
          toolCalls: [{ id: 'c1', name: 'get_pod_logs', input: { namespace: 'shop', name: 'catalog-api-f958778f8-vg55l' } }],
        },
      ],
    }
    rerender(<CopilotToggle />)
    const button = screen.getByRole('button', { name: 'Open Kobi' })
    expect(button).toHaveAccessibleDescription(/^Investigating · Logs/)
  })
})

