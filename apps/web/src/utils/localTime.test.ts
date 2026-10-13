import { describe, expect, it } from 'vitest'
import {
  clockOrDate,
  clockOrDay,
  formatLocalDateTime,
  localizeInlineTimestamps,
  localizeTimelineTimestamps,
  parseZonedTimestamp,
} from './localTime'

// Real stamps from inc_pTuIXlFjff and inc_9D5YnJJzZ0 — three zones, one instant each.
const SAME_INSTANT = [
  '2026-09-23T19:01:24Z',
  '2026-09-23T21:01:24+02:00',
  '2026-09-23 21:01:24 (+02:00)',
  'Wed, 23 Sep 2026 21:01:24 +0200',
  '2026-09-23T21:01:24+0200',
]

describe('parseZonedTimestamp', () => {
  it('reads every zoned form the reports contain as the same instant', () => {
    const ms = SAME_INSTANT.map((s) => parseZonedTimestamp(s)?.getTime())
    for (const m of ms) expect(m).toBe(Date.parse('2026-09-23T19:01:24Z'))
  })
  it('refuses what cannot be placed on a clock without inventing a part', () => {
    for (const s of ['2026-08-08', '19:00:44 UTC', 'Over the 11m before investigation', '95%', '']) {
      expect(parseZonedTimestamp(s)).toBeNull()
    }
  })
})

describe('localizeTimelineTimestamps', () => {
  // Shaped on inc_oFTpuWYFdE: one response day, then workload history reaching
  // back months — and across a year boundary, to exercise the year rule.
  const md = [
    '### Incident response',
    '- **2026-09-23T19:10:25Z** — detected',
    '- **2026-09-23T19:10:45Z** — triage completed',
    '- **2026-09-23T21:11:15+02:00** — approval received',
    '### Relevant workload history',
    '- **2026-05-05T17:24:20+02:00** — Deployment created',
    '- **2025-12-01T10:00:00Z** — something older',
    '- **2026-08-08** — Revision 202 created',
    '- investigator confirmed with **95%** confidence',
  ].join('\n')
  const out = localizeTimelineTimestamps(md)
  const stamp = (i: number) => /\*\*([^*]+)\*\*/.exec(out.split('\n')[i])?.[1] ?? ''

  it('names the date once per day, then only the time', () => {
    expect(stamp(1)).toContain(',') // date, time
    expect(stamp(2)).not.toContain(',') // same day → time only
    expect(stamp(3)).not.toContain(',') // same instant family, other offset → still same local day
  })
  it('converts both clocks into one zone — same local time for the same instant', () => {
    const t = new Date('2026-09-23T19:11:15Z').toLocaleTimeString(undefined, {
      hour: '2-digit', minute: '2-digit', second: '2-digit',
    })
    expect(stamp(3)).toBe(t)
  })
  it('shows the zone once, not on every line', () => {
    const zoned = [1, 2, 3].filter((i) => /[A-Z]{3}|GMT|UTC|[+-]\d/.test(stamp(i).replace(/[AP]M/, '')))
    expect(zoned).toEqual([1])
  })
  it('restarts the date at each section and adds the year only when it differs', () => {
    expect(stamp(5)).toContain(',') // new section → date shown
    expect(stamp(5)).not.toContain('2026')
    expect(stamp(6)).toContain('2025')
  })
  it('leaves dates on their own and non-time bold untouched', () => {
    expect(out).toContain('**2026-08-08**')
    expect(out).toContain('**95%**')
  })
})

describe('formatLocalDateTime', () => {
  it('names the zone and survives empty or bad input', () => {
    expect(formatLocalDateTime('2026-09-23T19:01:24Z')).toMatch(/[A-Z]{2,}|GMT|UTC|[+-]\d/)
    expect(formatLocalDateTime(null)).toBe('—')
    expect(formatLocalDateTime('nope')).toBe('nope')
  })
})

describe('localizeInlineTimestamps', () => {
  it('converts a zoned stamp written as inline code, anywhere in the text', () => {
    const md = 'exit code `255` at `2026-09-22T12:04:28+02:00`, restart count `5`'
    const out = localizeInlineTimestamps(md)
    expect(out).toContain(formatLocalDateTime('2026-09-22T10:04:28Z'))
    expect(out).not.toContain('2026-09-22T12:04:28+02:00')
    // non-timestamps stay code
    expect(out).toContain('`255`')
    expect(out).toContain('`5`')
  })
  it('leaves a code span that is not a zoned stamp untouched', () => {
    expect(localizeInlineTimestamps('`nginx:1.27-alpine` and `2026-08-08`')).toBe(
      '`nginx:1.27-alpine` and `2026-08-08`',
    )
  })
})

describe('clockOrDate', () => {
  // Local-time constructors: the assertions hold in any zone the suite runs in.
  const now = new Date(2026, 9, 12, 12, 30)

  it('gives the time of day alone for today', () => {
    const out = clockOrDate(new Date(2026, 9, 12, 11, 51), now)
    expect(out).toMatch(/11:51/)
    expect(out).not.toMatch(/Oct/)
  })

  it('carries the date for an earlier day', () => {
    expect(clockOrDate(new Date(2026, 9, 11, 23, 10), now)).toMatch(/Oct 11/)
  })

  it('says nothing it cannot read', () => {
    expect(clockOrDate('not a date', now)).toBe('—')
  })
})

describe('clockOrDay', () => {
  const now = new Date(2026, 9, 12, 12, 30)
  it('gives the time of day for today and the date alone before', () => {
    expect(clockOrDay(new Date(2026, 9, 12, 11, 51), now)).toMatch(/11:51/)
    const earlier = clockOrDay(new Date(2026, 7, 19, 10, 45), now)
    expect(earlier).toMatch(/Aug 19/)
    expect(earlier).not.toMatch(/10:45/)
  })
})
