import { describe, it, expect } from 'vitest'
import { formatMoney } from './formatters'

describe('formatMoney', () => {
  it('rounds whole dollars with thousands separators by default', () => {
    expect(formatMoney(1842)).toBe('$1,842')
    expect(formatMoney(0)).toBe('$0')
  })

  it('compacts large figures to k / M', () => {
    expect(formatMoney(12_400)).toBe('$12.4k')
    expect(formatMoney(1_500_000)).toBe('$1.5M')
  })

  it('keeps two decimals for sub-dollar values so they don’t collapse to $0', () => {
    // A $0.47/h node on a tiny cluster must not read as free.
    expect(formatMoney(0.475)).toBe('$0.47')
    expect(formatMoney(0.95)).toBe('$0.95')
  })

  it('exact mode keeps whole dollars past the compaction threshold', () => {
    expect(formatMoney(12_400, { exact: true })).toBe('$12,400')
  })

  it('carries the sign for negatives (savings chips render −$X)', () => {
    expect(formatMoney(-52)).toBe('-$52')
  })

  it('renders a dash for non-finite input', () => {
    expect(formatMoney(NaN)).toBe('—')
    expect(formatMoney(Infinity)).toBe('—')
  })
})
