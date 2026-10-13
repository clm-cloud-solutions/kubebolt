import { describe, expect, it } from 'vitest'
import { freshnessOf } from './freshness'

const now = 1_000_000

describe('freshnessOf', () => {
  it('is live through a normal cycle, past the interval itself', () => {
    expect(freshnessOf({ updatedAt: now - 8_000, intervalMs: 15_000, now })).toBe('live')
    expect(freshnessOf({ updatedAt: now - 16_000, intervalMs: 15_000, now })).toBe('live')
  })

  it('is delayed once a whole cycle is missed', () => {
    expect(freshnessOf({ updatedAt: now - 31_000, intervalMs: 15_000, now })).toBe('delayed')
  })

  it('is offline when the last poll failed, however recent the data', () => {
    expect(freshnessOf({ updatedAt: now - 1_000, intervalMs: 15_000, now, failed: true })).toBe('offline')
  })

  it('is connecting before the first answer', () => {
    expect(freshnessOf({ updatedAt: 0, intervalMs: 15_000, now })).toBe('connecting')
  })
})
