import { describe, expect, it } from 'vitest'
import { burstPhrases, fmtDur, isQuietShift, type Phrase } from './ShiftReportCard'
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
