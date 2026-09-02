import { describe, expect, it } from 'vitest'
import { advanceKnownTotal, episodeCountLabel, episodePagesLabel } from './EpisodeHistory'

// The History pager counts progressively (in-vivo ask 31-ago): no COUNT(*)
// upfront — the total is discovered the moment a short page arrives.

describe('advanceKnownTotal', () => {
  it('keeps the total unknown across full pages', () => {
    expect(advanceKnownTotal(null, 50, 1, 50)).toBeNull()
    expect(advanceKnownTotal(null, 50, 2, 50)).toBeNull()
  })

  it('pins the exact total on a short page', () => {
    // page 3 of 50 with 31 rows → 131 total.
    expect(advanceKnownTotal(null, 31, 3, 50)).toBe(131)
  })

  it('pins the total on an empty page past the edge', () => {
    // page 3 empty → everything fit in the first two pages.
    expect(advanceKnownTotal(null, 0, 3, 50)).toBe(100)
  })

  it('keeps a discovered total while paging back through full pages', () => {
    expect(advanceKnownTotal(131, 50, 2, 50)).toBe(131)
    expect(advanceKnownTotal(131, 50, 1, 50)).toBe(131)
  })

  it('forgets a stale total when a full page reaches past it (data grew)', () => {
    // total pinned at 80, but page 2 now comes back full to row 100.
    expect(advanceKnownTotal(80, 50, 2, 50)).toBeNull()
    // exactly AT the pinned total is consistent, not growth — keep it.
    expect(advanceKnownTotal(100, 50, 2, 50)).toBe(100)
  })

  it('re-pins when the edge moves', () => {
    expect(advanceKnownTotal(131, 40, 3, 50)).toBe(140)
  })
})

describe('episodeCountLabel', () => {
  it('shows an open floor while the total is unknown', () => {
    expect(episodeCountLabel(50, 1, 50, null)).toBe('1–50 of 50+')
    expect(episodeCountLabel(50, 2, 50, null)).toBe('51–100 of 100+')
  })

  it('shows the exact total once discovered', () => {
    expect(episodeCountLabel(31, 3, 50, 131)).toBe('101–131 of 131')
    expect(episodeCountLabel(50, 1, 50, 131)).toBe('1–50 of 131')
  })

  it('handles the empty page past the edge', () => {
    expect(episodeCountLabel(0, 3, 50, 100)).toBe('100 total')
    expect(episodeCountLabel(0, 3, 50, null)).toBe('page 3')
  })

  it('uses thousands separators like the rest of the UI', () => {
    // Locale-independent: build the expectation with the same formatter.
    const [start, end, total] = [1451, 1474, 1474].map((n) => n.toLocaleString())
    expect(episodeCountLabel(24, 30, 50, 1474)).toBe(`${start}–${end} of ${total}`)
  })
})

describe('episodePagesLabel', () => {
  it('shows an open page count while the total is unknown', () => {
    // A full page proves at least one more page exists.
    expect(episodePagesLabel(1, 50, null)).toBe('1 / 2+')
    expect(episodePagesLabel(3, 50, null)).toBe('3 / 4+')
  })

  it('shows exact pages once the total is discovered', () => {
    expect(episodePagesLabel(2, 50, 131)).toBe('2 / 3')
    expect(episodePagesLabel(1, 50, 31)).toBe('1 / 1')
    // Empty window discovered as 0 total still renders a page 1.
    expect(episodePagesLabel(1, 50, 0)).toBe('1 / 1')
  })
})
