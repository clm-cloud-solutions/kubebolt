import { describe, expect, it } from 'vitest'
import {
  burstPhrases,
  burstSummaryPhrases,
  fmtDur,
  isQuietShift,
  pickBursts,
  SHIFT_BURSTS_SHOWN,
  spansDays,
  type Phrase,
} from './ShiftReportCard'
import type { OperationalBurst, ShiftReport } from '@/services/api'

// The shift report narrates arithmetic over the clusterer's output — these
// pin the sentence builder against the Dipres shape and the quiet detection
// that keeps a calm return to one line.

function burst(over: Partial<OperationalBurst>): OperationalBurst {
  return {
    id: 'op-1',
    kind: 'unknown_burst',
    clusters: ['cl-a'],
    windowFrom: '2026-08-25T05:51:00Z',
    onsetTo: '2026-08-25T05:55:00Z',
    windowTo: '2026-08-25T06:40:00Z',
    seedIds: [],
    memberIds: ['a', 'b', 'c', 'd', 'e'],
    blast: {
      affected: 5,
      autoRecovered: 5,
      remediated: 0,
      stillFiring: 0,
      expired: 0,
      worstSeconds: 0,
      worstResource: '',
    },
    ...over,
  }
}

const text = (ps: Phrase[]) => ps.map((p) => p.t).join('')

describe('burstPhrases', () => {
  it('narrates the Dipres rotation with names, counts and recovery time', () => {
    const ps = burstPhrases(
      burst({
        kind: 'node_rotation',
        clusters: ['uid-a', 'uid-b'],
        blast: {
          affected: 46,
          autoRecovered: 45,
          remediated: 0,
          stillFiring: 1,
          expired: 0,
          worstSeconds: 9 * 3600,
          worstResource: 'Deploy/ns/the-one',
        },
      }),
      { 'uid-a': 'gke-orquestador', 'uid-b': 'gke-procesamiento' },
    )
    const s = text(ps)
    expect(s).toContain('A node rotation at ')
    expect(s).toContain('across gke-orquestador and gke-procesamiento')
    expect(s).toContain('hit 46 workloads')
    expect(s).toContain('1 still down')
    // The bad news carries the bad tone.
    expect(ps.find((p) => p.t === '46 workloads')?.tone).toBe('bad')
    expect(ps.find((p) => p.t === '1 still down')?.tone).toBe('bad')
  })

  it('a fully recovered burst says when everything came back', () => {
    const s = text(burstPhrases(burst({ kind: 'node_pressure' }), {}))
    expect(s).toContain('Node pressure at ')
    expect(s).toContain('Everything recovered by ')
    expect(s).not.toContain('still down')
  })

  it('falls back to a cluster count when names are unknown', () => {
    const s = text(burstPhrases(burst({ clusters: ['x', 'y'] }), {}))
    expect(s).toContain('across 2 clusters')
  })
})

// A month away held ~50 bursts and the narrative wrote 50 sentences in a row
// (in-vivo 2026-10-05). Past SHIFT_BURSTS_SHOWN it summarizes instead.
describe('summarizing many bursts', () => {
  const at = (day: number, over: Partial<OperationalBurst> = {}) =>
    burst({
      id: `op-${day}`,
      windowFrom: `2026-09-${String(day).padStart(2, '0')}T10:00:00Z`,
      windowTo: `2026-09-${String(day).padStart(2, '0')}T10:30:00Z`,
      ...over,
    })
  const blast = (affected: number, stillFiring = 0) => ({
    affected,
    autoRecovered: affected - stillFiring,
    remediated: 0,
    stillFiring,
    expired: 0,
    worstSeconds: 0,
    worstResource: '',
  })

  it('a few bursts are all told, untouched', () => {
    const few = [at(1), at(2), at(3)]
    expect(pickBursts(few)).toBe(few)
  })

  it('beyond the cap, still-down first, then the largest, told in order', () => {
    const many = [
      at(1, { blast: blast(5) }),
      at(2, { blast: blast(59) }),
      at(3, { blast: blast(4, 1) }), // still down: always told
      at(4, { blast: blast(43) }),
      at(5, { blast: blast(6) }),
    ]
    const picked = pickBursts(many)
    expect(picked).toHaveLength(SHIFT_BURSTS_SHOWN)
    expect(picked.map((b) => b.id)).toEqual(['op-2', 'op-3', 'op-4'])
  })

  it('the lead sentence counts, splits recovered from down, and breaks down by kind', () => {
    const many = [
      at(1, { kind: 'mass_rollout' }),
      at(2, { kind: 'mass_rollout' }),
      at(3, { kind: 'unknown_burst', blast: blast(4, 2) }),
      at(4, { kind: 'node_rotation' }),
    ]
    const ps = burstSummaryPhrases(many, '2026-09-01T00:00:00Z')
    const s = text(ps)
    expect(s).toMatch(/^4 bursts since /)
    expect(s).toContain('3 recovered on their own, 1 with workloads still down')
    expect(s).toContain('(2 broad rollouts, 1 burst of findings, 1 node rotation).')
    expect(ps.find((p) => p.t === '1 with workloads still down')?.tone).toBe('bad')
  })

  it('all recovered reads as good news', () => {
    const s = text(burstSummaryPhrases([at(1), at(2), at(3), at(4)], '2026-09-01T00:00:00Z'))
    expect(s).toContain('all recovered on their own')
  })

  it('a window longer than a day dates every time; a short one does not', () => {
    expect(spansDays('2026-09-05T13:52:00Z', '2026-10-05T13:52:00Z')).toBe(true)
    expect(spansDays('2026-10-05T01:00:00Z', '2026-10-05T13:00:00Z')).toBe(false)
    const dated = text(burstPhrases(at(12), {}, true))
    const bare = text(burstPhrases(at(12), {}))
    expect(dated.length).toBeGreaterThan(bare.length)
    expect(dated).toMatch(/at \S+ \d+, /) // «at Sep 12, 10:00 AM»
  })
})

describe('fmtDur', () => {
  it('formats like the episode views', () => {
    expect(fmtDur(300)).toBe('5m')
    expect(fmtDur(9 * 3600)).toBe('9h 0m')
    expect(fmtDur(50 * 3600)).toBe('2d 2h')
  })
})

describe('isQuietShift', () => {
  const base: ShiftReport = {
    windowFrom: '',
    windowTo: '',
    firstShift: false,
    truncated: false,
    bursts: [],
    episodes: { opened: 0, autoRecovered: 0, remediated: 0, expired: 0, stillFiring: 0, criticals: 0 },
    mutes: { createdInWindow: 0, activeNow: 0 },
    rulesOff: 0,
    capabilities: [],
    capabilityChanges: 0,
  }
  it('a calm window is quiet even with standing capability trouble', () => {
    expect(isQuietShift({ ...base, capabilities: [{ id: 'credits' } as never] })).toBe(true)
  })
  it('a resolution while away breaks the quiet even with zero opened', () => {
    expect(isQuietShift({ ...base, episodes: { ...base.episodes, autoRecovered: 2 } })).toBe(false)
  })
  it('one episode or one capability CHANGE breaks the quiet', () => {
    expect(isQuietShift({ ...base, episodes: { ...base.episodes, opened: 1 } })).toBe(false)
    expect(isQuietShift({ ...base, capabilityChanges: 2 })).toBe(false)
  })
})
