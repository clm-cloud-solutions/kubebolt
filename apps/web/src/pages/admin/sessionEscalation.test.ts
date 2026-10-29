import { describe, expect, it } from 'vitest'
import { escalationSentence, sessionModelLabel, shareRounds } from './sessionEscalation'
import type { SessionModelShare } from '@/types/copilotUsage'

const usage = { inputTokens: 1, outputTokens: 1 }
const share = (provider: string, model: string): SessionModelShare => ({ provider, model, usage, estimatedUsd: 0, credits: 0 })

// The lab session of 2026-10-28: five rounds on Haiku, then mcp__aws__call
// moved it to Sonnet for two more.
const lab = {
  provider: 'anthropic',
  model: 'claude-sonnet-5-5',
  rounds: 7,
  escalation: {
    provider: 'anthropic',
    model: 'claude-sonnet-5-5',
    fromProvider: 'anthropic',
    fromModel: 'claude-haiku-5-5',
    reason: 'mcp__aws__call',
    round: 5,
  },
}

describe('sessionModelLabel', () => {
  it('names both models of an escalated session', () => {
    expect(sessionModelLabel(lab)).toBe('anthropic · claude-haiku-5-5 → claude-sonnet-5-5')
    expect(
      sessionModelLabel({ ...lab, escalation: { ...lab.escalation, fromProvider: 'openai', fromModel: 'gpt-5-mini' } }),
    ).toBe('openai · gpt-5-mini → anthropic · claude-sonnet-5-5')
  })

  it('keeps the one model of a session that did not escalate, or does not say where it came from', () => {
    expect(sessionModelLabel({ provider: 'anthropic', model: 'claude-haiku-5-5', rounds: 3 })).toBe('anthropic · claude-haiku-5-5')
    expect(sessionModelLabel({ ...lab, escalation: { ...lab.escalation, fromModel: undefined } })).toBe(
      'anthropic · claude-sonnet-5-5',
    )
  })
})

describe('escalationSentence', () => {
  it('says when and why it moved', () => {
    expect(escalationSentence(lab.escalation)).toBe('Moved to claude-sonnet-5-5 after round 5 · mcp__aws__call')
    expect(escalationSentence({ ...lab.escalation, reason: 'history', round: 0 })).toBe(
      'Ran on claude-sonnet-5-5 from the start: an earlier turn had moved to it',
    )
    expect(escalationSentence({ ...lab.escalation, reason: 'handoff', round: 0 })).toBe(
      'Ran on claude-sonnet-5-5 from the start: it opened from a cluster switch',
    )
  })
})

describe('shareRounds', () => {
  it('gives the rounds before the move to the model it moved from, the rest to the escalation model', () => {
    expect(shareRounds(share('anthropic', 'claude-haiku-5-5'), lab)).toBe('rounds 1–5')
    expect(shareRounds(share('anthropic', 'claude-sonnet-5-5'), lab)).toBe('rounds 6–7')
    expect(shareRounds(share('anthropic', 'claude-sonnet-5-5'), { ...lab, rounds: 6 })).toBe('round 6')
  })

  it('gives every round to the escalation model when it ran from the start', () => {
    const fromStart = { ...lab, rounds: 3, escalation: { ...lab.escalation, reason: 'history', round: 0 } }
    expect(shareRounds(share('anthropic', 'claude-sonnet-5-5'), fromStart)).toBe('rounds 1–3')
  })
})
