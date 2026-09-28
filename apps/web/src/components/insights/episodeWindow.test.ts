import { describe, it, expect } from 'vitest'
import { parsePinnedWindow, episodeWindowKey } from './EpisodeHistory'

// Regression, in vivo 20-sep: the History tab span forever and the API log
// filled with back-to-back GET /insights/episodes, milliseconds apart. The
// window's `since` had been put INTO the query key — and for a relative range
// `since` comes from Date.now(), so every render minted a new key, TanStack
// refetched, the refetch re-rendered, and the loop could never settle.
//
// The rule this pins: the key is built from the INPUTS (the pin, or the hour
// count), never from a timestamp derived at render time.
describe('episodeWindowKey — stable across renders or the tab loops', () => {
  it('returns the same key for a relative range no matter when it is called', () => {
    const first = episodeWindowKey(null, 24)
    // Two calls separated by real time: a key built from Date.now() would differ.
    const start = Date.now()
    while (Date.now() === start) { /* burn at least one millisecond */ }
    expect(episodeWindowKey(null, 24)).toBe(first)
  })

  it('keys different relative ranges apart', () => {
    expect(episodeWindowKey(null, 24)).not.toBe(episodeWindowKey(null, 24 * 7))
  })

  it('is stable for a pinned window and distinct from the relative ones', () => {
    const pin = { from: new Date('2026-09-16T03:00:00Z'), to: new Date('2026-09-16T08:00:00Z') }
    const k = episodeWindowKey(pin, 24)
    expect(episodeWindowKey({ from: new Date(pin.from), to: new Date(pin.to) }, 24)).toBe(k)
    expect(k).not.toBe(episodeWindowKey(null, 24))
    // The hour count must not leak into a pinned key — the pin fully decides it.
    expect(episodeWindowKey(pin, 720)).toBe(k)
  })
})

describe('parsePinnedWindow', () => {
  const parse = (qs: string) => parsePinnedWindow(new URLSearchParams(qs))

  it('takes a well-formed pair', () => {
    const w = parse('from=2026-09-16T03:00:00Z&to=2026-09-16T08:00:00Z')
    expect(w?.from.toISOString()).toBe('2026-09-16T03:00:00.000Z')
    expect(w?.to.toISOString()).toBe('2026-09-16T08:00:00.000Z')
  })

  it('refuses anything it cannot trust, so the view falls back instead of showing an empty range', () => {
    for (const qs of [
      '',
      'from=2026-09-16T03:00:00Z',                         // half a window
      'to=2026-09-16T08:00:00Z',
      'from=nonsense&to=alsononsense',
      'from=2026-09-16T08:00:00Z&to=2026-09-16T03:00:00Z', // inverted
      'from=2026-09-16T03:00:00Z&to=2026-09-16T03:00:00Z', // empty
    ]) {
      expect(parse(qs), qs).toBeNull()
    }
  })
})
