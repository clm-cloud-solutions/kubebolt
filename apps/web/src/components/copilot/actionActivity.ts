import { useSyncExternalStore } from 'react'

// What an approved action is doing right now, for Kobi's face. The proposal
// card knows it (its own `executing` status, then the rollout it watches) but
// the face lives in the header and on the floating head, which read
// useKobiPresence: this store carries it across.
//
// - executing: the approved action's request is in flight (amber: Kobi is
//   changing the infrastructure).
// - verifying: the change was accepted and Kobi watches it converge (the
//   rollout poll), until it completes, stalls or the card goes away.
//
// Executing wins when both are present: it is the one changing something.

export type ActionActivityState = 'executing' | 'verifying'

export interface ActionActivity {
  state: ActionActivityState
  /** What it is about: the target's name. */
  detail?: string
}

const active = new Map<string, ActionActivity>()
const listeners = new Set<() => void>()
let snapshot: ActionActivity | undefined

function recompute() {
  let next: ActionActivity | undefined
  for (const a of active.values()) {
    if (a.state === 'executing') {
      next = a
      break
    }
    next ??= a
  }
  if (next?.state === snapshot?.state && next?.detail === snapshot?.detail) return
  snapshot = next
  for (const l of listeners) l()
}

/** Sets (or, with undefined, clears) what the action behind a proposal is doing. */
export function setActionActivity(toolCallId: string, activity: ActionActivity | undefined) {
  if (activity) active.set(toolCallId, activity)
  else active.delete(toolCallId)
  recompute()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

export function getActionActivity(): ActionActivity | undefined {
  return snapshot
}

export function useActionActivity(): ActionActivity | undefined {
  return useSyncExternalStore(subscribe, getActionActivity, getActionActivity)
}

/** Tests only. */
export function resetActionActivity() {
  active.clear()
  snapshot = undefined
}
