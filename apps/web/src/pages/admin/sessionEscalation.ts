import type { SessionEscalation, SessionModelShare } from '@/types/copilotUsage'

// How a Kobi session that moved to the escalation model reads in its detail:
// which model it ran on, why it moved, and which rounds each model answered.
// The cost of each model comes from the API (modelShares); this only labels it.

interface EscalatedSession {
  provider: string
  model: string
  rounds: number
  escalation?: SessionEscalation
}

// sessionModelLabel is the session's model: the model it moved from and the
// one it moved to when it escalated, else the one it ran on.
export function sessionModelLabel(s: EscalatedSession): string {
  const model = s.model || 'default'
  const from = s.escalation?.fromModel
  if (!from) return `${s.provider} · ${model}`
  const fromProvider = s.escalation?.fromProvider || s.provider
  if (fromProvider === s.provider) return `${s.provider} · ${from} → ${model}`
  return `${fromProvider} · ${from} → ${s.provider} · ${model}`
}

// escalationSentence says why the session ran on the escalation model.
export function escalationSentence(e: SessionEscalation): string {
  switch (e.reason) {
    case 'history':
      return `Ran on ${e.model} from the start: an earlier turn had moved to it`
    case 'handoff':
      return `Ran on ${e.model} from the start: it opened from a cluster switch`
  }
  if (e.round > 0) return `Moved to ${e.model} after round ${e.round} · ${e.reason}`
  return `Ran on ${e.model} from the start · ${e.reason}`
}

// shareRounds names the rounds a model answered: the model it moved from has
// the rounds up to the move, the escalation model the rest.
export function shareRounds(share: SessionModelShare, s: EscalatedSession): string {
  const e = s.escalation
  if (!e) return roundsLabel(1, s.rounds)
  const moved = Math.min(Math.max(e.round, 0), s.rounds)
  const isFrom = share.model === e.fromModel && share.provider === (e.fromProvider || s.provider)
  return isFrom ? roundsLabel(1, moved) : roundsLabel(moved + 1, s.rounds)
}

function roundsLabel(first: number, last: number): string {
  if (last <= first) return `round ${Math.max(first, 1)}`
  return `rounds ${first}–${last}`
}
