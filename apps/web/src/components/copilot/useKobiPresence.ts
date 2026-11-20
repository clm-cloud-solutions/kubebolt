import { useMemo } from 'react'
import { useCopilot } from '@/contexts/CopilotContext'
import { parseActionProposal, type CopilotMessage } from '@/services/copilot/types'
import type { KobiFaceState } from '@/components/kobi'
import { toolTitle } from './toolTitles'

// Kobi's live state, for the two places that show it: the floating head and
// the panel header. Everywhere else the face is still, or Watching (brand
// guidelines, «Where the face lives in the product»).

export interface KobiPresence {
  state: KobiFaceState
  /** What the state is called: "Investigating", "Awaiting your approval". */
  label: string
  /** What it is about, when known: "Logs · catalog-api". */
  detail?: string
}

export const PRESENCE_LABEL: Record<KobiFaceState, string> = {
  watching: 'Watching',
  investigating: 'Investigating',
  awaiting: 'Awaiting your approval',
  executing: 'Executing',
  verifying: 'Verifying',
  nocause: 'No cause found',
  unavailable: 'Unavailable',
}

// A pending proposal is one Kobi emitted as an action_proposal tool result
// that the operator has not executed or dismissed yet: Kobi is waiting for a
// decision.
function hasPendingProposal(messages: CopilotMessage[]): boolean {
  for (const m of messages) {
    if (m.role !== 'user' || !m.toolResults) continue
    for (const tr of m.toolResults) {
      const proposal = parseActionProposal(tr.content)
      if (proposal && proposal.executionStatus === undefined) return true
    }
  }
  return false
}

// The call the turn in flight is on: the latest call of the current turn
// still without a result, else its latest call. The turn starts after the
// operator's last prompt (a user message that carries no tool results).
function currentCall(messages: CopilotMessage[]) {
  const answered = new Set<string>()
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]
    if (m.role === 'user' && !m.toolResults?.length) break
    for (const tr of m.toolResults ?? []) answered.add(tr.toolCallId)
    const calls = m.toolCalls ?? []
    for (let j = calls.length - 1; j >= 0; j--) {
      if (!answered.has(calls[j].id)) return calls[j]
    }
    if (calls.length) return calls[calls.length - 1]
  }
  return undefined
}

// The plan's limits, said in words over the face the brand guidelines give
// them («The Face: Sentinel», the state table):
// - A question waiting for a free slot (Kobi already answers as many as the
//   plan allows) is Watching: Kobi is available, nothing runs for it yet and
//   it needs nothing from the person. Not Awaiting: that is a person's
//   approval, and its blue means «Kobi needs you».
// - The month's AI credits spent is Unavailable (gray, eyes closed): Kobi is
//   off for the organization until they come back.
export const WAITING_FOR_SLOT = 'Waiting for a free slot'
export const OUT_OF_CREDITS = 'Out of AI credits'

// lastTurnOutOfCredits: the latest question ended refused for credits.
function lastTurnOutOfCredits(messages: CopilotMessage[]): boolean {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]
    if (m.failure) return m.failure.kind === 'credits'
    if (m.role === 'user' && m.content && !m.toolResults?.length) return false
  }
  return false
}

// Precedence: a question waiting for a free slot is not being worked on yet;
// then investigating (the model is generating or tools are in flight) wins
// over awaiting, because active work is the more current signal: the
// operator may have sent a follow-up before deciding on the card.
export function kobiPresenceOf(
  messages: CopilotMessage[],
  isLoading: boolean,
  pendingToolCalls: string[],
  waitingForSlot = false,
): KobiPresence {
  if (isLoading && waitingForSlot) return { state: 'watching', label: WAITING_FOR_SLOT }
  if (isLoading || pendingToolCalls.length > 0) {
    const call = currentCall(messages)
    const t = call ? toolTitle(call.name, call.input ?? {}) : undefined
    return {
      state: 'investigating',
      label: PRESENCE_LABEL.investigating,
      detail: t ? (t.chip ? `${t.title} · ${t.chip}` : t.title) : undefined,
    }
  }
  if (lastTurnOutOfCredits(messages)) return { state: 'unavailable', label: OUT_OF_CREDITS }
  if (hasPendingProposal(messages)) return { state: 'awaiting', label: PRESENCE_LABEL.awaiting }
  return { state: 'watching', label: PRESENCE_LABEL.watching }
}

export function useKobiPresence(): KobiPresence {
  const { messages, isLoading, pendingToolCalls, turnWaitUntil } = useCopilot()
  const waiting = turnWaitUntil != null
  return useMemo(
    () => kobiPresenceOf(messages, isLoading, pendingToolCalls, waiting),
    [messages, isLoading, pendingToolCalls, waiting],
  )
}
