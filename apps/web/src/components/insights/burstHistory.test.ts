import { describe, it, expect } from 'vitest'
import { resolveWindow, onsetSeconds, isStillOpen, type Preset } from './BurstHistory'
import type { OperationalBurst } from '@/services/api'

const NOW = new Date('2026-09-20T12:00:00Z')
const win = (qs: string) => resolveWindow(new URLSearchParams(qs), NOW)

describe('resolveWindow — the range lives in the URL, always', () => {
  it('defaults to the last 24h when the URL says nothing', () => {
    const w = win('')
    expect(w.preset).toBe<Preset>('24h')
    expect(w.to).toEqual(NOW)
    expect((NOW.getTime() - w.from.getTime()) / 3600_000).toBe(24)
  })

  it('honours the relative presets', () => {
    expect((NOW.getTime() - win('range=7d').from.getTime()) / 3600_000).toBe(24 * 7)
    expect((NOW.getTime() - win('range=30d').from.getTime()) / 3600_000).toBe(24 * 30)
  })

  it('addresses a whole day — the question the shift report cannot answer', () => {
    const w = win('day=2026-09-16')
    expect(w.day).toBe('2026-09-16')
    expect(w.preset).toBeNull()
    // Local midnight to local midnight: the operator means THEIR day.
    expect(w.from).toEqual(new Date(2026, 8, 16, 0, 0, 0, 0))
    expect(w.to).toEqual(new Date(2026, 8, 17, 0, 0, 0, 0))
  })

  it('lets an explicit from+to win, which is what a machine link passes', () => {
    const w = win('from=2026-09-16T03:00:00Z&to=2026-09-16T08:00:00Z&day=2026-01-01&range=30d')
    expect(w.from.toISOString()).toBe('2026-09-16T03:00:00.000Z')
    expect(w.to.toISOString()).toBe('2026-09-16T08:00:00.000Z')
    expect(w.day).toBeNull()
    expect(w.preset).toBeNull()
  })

  it('falls back to the default instead of rendering an empty range', () => {
    // A mangled link must still show something true rather than nothing.
    for (const qs of [
      'from=nonsense&to=alsononsense',
      'from=2026-09-16T08:00:00Z&to=2026-09-16T03:00:00Z', // inverted
      'from=2026-09-16T03:00:00Z', // half a window
      'day=16-09-2026',
      'day=not-a-day',
      'range=eternity',
    ]) {
      const w = win(qs)
      expect(w.preset, qs).toBe<Preset>('24h')
      expect(w.from.getTime(), qs).toBeLessThan(w.to.getTime())
    }
  })
})

function burst(over: Partial<OperationalBurst>): OperationalBurst {
  return {
    id: 'b1',
    kind: 'node_rotation',
    clusters: ['uid-a'],
    windowFrom: '2026-09-16T03:05:00Z',
    onsetTo: '2026-09-16T03:09:00Z',
    windowTo: '2026-11-24T10:00:00Z',
    seedIds: ['s'],
    memberIds: ['m1', 'm2'],
    blast: {
      affected: 48, autoRecovered: 48, remediated: 0,
      stillFiring: 0, expired: 0, worstSeconds: 640, worstResource: 'Deploy/ns/api',
    },
    ...over,
  }
}

describe('onsetSeconds — how long the breaking took, not how long it lingered', () => {
  it('measures windowFrom..onsetTo', () => {
    expect(onsetSeconds(burst({}))).toBe(4 * 60)
  })

  it('ignores windowTo, the number that showed a 4-minute rotation as 69 days', () => {
    const wide = burst({ windowTo: '2027-01-01T00:00:00Z' })
    expect(onsetSeconds(wide)).toBe(4 * 60)
  })

  it('reads zero when everything broke in the same instant', () => {
    expect(onsetSeconds(burst({ onsetTo: '2026-09-16T03:05:00Z' }))).toBe(0)
  })

  it('does not go negative or NaN on bad data', () => {
    expect(onsetSeconds(burst({ onsetTo: '2026-09-16T03:00:00Z' }))).toBe(0)
    expect(onsetSeconds(burst({ onsetTo: 'nope' }))).toBe(0)
    expect(onsetSeconds(burst({ windowFrom: 'nope' }))).toBe(0)
  })
})

describe('isStillOpen', () => {
  it('is open only while something is actually firing', () => {
    expect(isStillOpen(burst({}))).toBe(false)
    expect(isStillOpen(burst({ blast: { ...burst({}).blast, stillFiring: 3 } }))).toBe(true)
    // Expired is not firing: the cluster went silent, which is its own state.
    expect(isStillOpen(burst({ blast: { ...burst({}).blast, expired: 5 } }))).toBe(false)
  })
})
