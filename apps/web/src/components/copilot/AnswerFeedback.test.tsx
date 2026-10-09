import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import React from 'react'
import { AnswerFeedback } from './AnswerFeedback'
import { serializeMessages } from '@/services/copilot/chat'

// The last rating given is the one kept: the same icon again withdraws it, the
// other one changes it. A 👎 asks why before it is sent, so it is counted once.

const listConversationFeedback = vi.fn()
const rateCopilotAnswer = vi.fn()
vi.mock('@/services/api', async () => {
  const actual = await vi.importActual<typeof import('@/services/api')>('@/services/api')
  return {
    ...actual,
    api: {
      listConversationFeedback: (...a: unknown[]) => listConversationFeedback(...a),
      rateCopilotAnswer: (...a: unknown[]) => rateCopilotAnswer(...a),
    },
  }
})

function renderBar() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <AnswerFeedback conversationId="c1" turnId="t1" />
    </QueryClientProvider>,
  )
}

describe('AnswerFeedback', () => {
  beforeEach(() => {
    listConversationFeedback.mockReset()
    rateCopilotAnswer.mockReset()
    rateCopilotAnswer.mockResolvedValue({ feedback: null })
  })

  it('rates 👍 and withdraws it on a second click', async () => {
    listConversationFeedback.mockResolvedValue([])
    renderBar()
    await waitFor(() => expect(listConversationFeedback).toHaveBeenCalledWith('c1'))

    fireEvent.click(screen.getByTitle('Good answer'))
    await waitFor(() => expect(rateCopilotAnswer).toHaveBeenLastCalledWith({ conversationId: 'c1', turnId: 't1', rating: 'up' }))
    await waitFor(() => expect(screen.getByTitle('Remove rating')).toHaveAttribute('aria-pressed', 'true'))

    fireEvent.click(screen.getByTitle('Remove rating'))
    await waitFor(() => expect(rateCopilotAnswer).toHaveBeenLastCalledWith({ conversationId: 'c1', turnId: 't1', rating: '' }))
    await waitFor(() => expect(screen.getByTitle('Good answer')).toHaveAttribute('aria-pressed', 'false'))
  })

  it('asks why before sending a 👎, and sends nothing on cancel', async () => {
    listConversationFeedback.mockResolvedValue([])
    renderBar()
    await waitFor(() => expect(listConversationFeedback).toHaveBeenCalled())

    fireEvent.click(screen.getByTitle('Bad answer'))
    expect(rateCopilotAnswer).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(rateCopilotAnswer).not.toHaveBeenCalled()
    expect(screen.queryByText('What went wrong?')).toBeNull()

    fireEvent.click(screen.getByTitle('Bad answer'))
    fireEvent.click(screen.getByRole('radio', { name: 'Incomplete' }))
    fireEvent.change(screen.getByPlaceholderText('Tell us more (optional)'), {
      target: { value: '  it missed the evicted pods  ' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(rateCopilotAnswer).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(rateCopilotAnswer).toHaveBeenLastCalledWith({
      conversationId: 'c1',
      turnId: 't1',
      rating: 'down',
      reason: 'incomplete',
      comment: 'it missed the evicted pods',
    }))
    await waitFor(() => expect(screen.getByText('Incomplete')).toBeInTheDocument())
  })

  it('shows a rating given earlier, and a 👍 replaces it', async () => {
    listConversationFeedback.mockResolvedValue([
      { conversationId: 'c1', turnId: 't1', rating: 'down', reason: 'slow' },
      { conversationId: 'c1', turnId: 't2', rating: 'up' },
    ])
    renderBar()
    await waitFor(() => expect(screen.getByText('Too slow')).toBeInTheDocument())
    expect(screen.getByTitle('Good answer')).toHaveAttribute('aria-pressed', 'false')

    fireEvent.click(screen.getByTitle('Good answer'))
    await waitFor(() => expect(rateCopilotAnswer).toHaveBeenLastCalledWith({ conversationId: 'c1', turnId: 't1', rating: 'up' }))
    await waitFor(() => expect(screen.queryByText('Too slow')).toBeNull())
  })

  it('puts the rating back and says so when saving fails', async () => {
    listConversationFeedback.mockResolvedValue([])
    rateCopilotAnswer.mockRejectedValue(new Error('500'))
    renderBar()
    await waitFor(() => expect(listConversationFeedback).toHaveBeenCalled())
    fireEvent.click(screen.getByTitle('Good answer'))
    await waitFor(() => expect(screen.getByText("Couldn't save your rating")).toBeInTheDocument())
    expect(screen.getByTitle('Good answer')).toHaveAttribute('aria-pressed', 'false')
  })
})

describe('serializeMessages', () => {
  it('round-trips the answered turn id so earlier answers stay rateable', () => {
    const wire = serializeMessages([
      { id: 'a', role: 'assistant', content: 'OOMKilled.', timestamp: new Date(), turnId: 't1' },
    ])
    expect(wire[0].turnId).toBe('t1')
  })
})
