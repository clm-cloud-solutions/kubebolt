import { describe, it, expect } from 'vitest'
import { RUNTIME_PRIORITIES, falcoSentence, priorityPill } from './RuntimeEventModal'

// The paragraph a responder reads first must be the sentence, not the field
// dump. Falco writes `HH:MM:SS.nanos: <Priority> <sentence> | k=v k=v…` and
// showing it whole buried twelve useful words under everything this dialog
// already renders structured — plus an <NA> for every field the rule left
// unfilled.
describe('falcoSentence', () => {
  it('keeps only the human half of a real Falco line', () => {
    const raw =
      '18:16:52.534172564: Warning Sensitive file opened for reading by non-trusted program | ' +
      'file=/etc/shadow gparent=<NA> ggparent=<NA> evt_type=openat user=root process=cat'
    expect(falcoSentence(raw)).toBe('Sensitive file opened for reading by non-trusted program')
  })

  it('drops the timestamp and the priority, which the header already shows', () => {
    expect(falcoSentence('09:01:02.000000001: Critical Terminal shell in container')).toBe(
      'Terminal shell in container',
    )
  })

  it('leaves a line alone when it has no field dump', () => {
    expect(falcoSentence('Unexpected outbound connection')).toBe('Unexpected outbound connection')
  })

  // A rule shape we have not seen must never render blank — falling back to the
  // raw line is always better than an empty paragraph on a security alert.
  it('falls back to the raw output rather than emptying it', () => {
    expect(falcoSentence(' | file=/etc/shadow')).toBe(' | file=/etc/shadow')
    expect(falcoSentence('')).toBe('')
  })

  // A one-word sentence must not be eaten by the priority-stripping heuristic.
  it('does not strip the only word there is', () => {
    expect(falcoSentence('Warning')).toBe('Warning')
  })
})

// The pill, the ring and the chips must never disagree about a priority. They
// used to: two colour maps existed and the shorter one had no `Notice`, so the
// same event came out blue in the ring and grey in the table — which reads as a
// disagreement about the data, not a style slip.
describe('priorityPill', () => {
  it('covers every level Falco can emit', () => {
    const syslog = [
      'Emergency', 'Alert', 'Critical', 'Error',
      'Warning', 'Notice', 'Informational', 'Debug',
    ]
    for (const p of syslog) {
      expect(RUNTIME_PRIORITIES.some((x) => x.key === p)).toBe(true)
    }
  })

  it('gives Notice a colour instead of falling through to neutral', () => {
    expect(priorityPill('Notice')).toContain('status-info')
    expect(priorityPill('Notice')).not.toBe(priorityPill('Debug'))
  })

  // An unknown level from a custom ruleset must degrade the same way everywhere,
  // which is the whole point of routing through one lookup.
  it('degrades an unknown level to neutral', () => {
    expect(priorityPill('Bizarre')).toBe('bg-kb-elevated text-kb-text-tertiary')
  })
})
