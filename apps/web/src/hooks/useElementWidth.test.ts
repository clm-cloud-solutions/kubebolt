import { describe, expect, it } from 'vitest'
import { columnsFor } from './useElementWidth'

describe('columnsFor', () => {
  it('keeps four KPI cards on a wide row', () => {
    expect(columnsFor(1700, 280, 12, [4, 2, 1])).toBe(4)
  })
  it('goes to 2 × 2 when Kobi docks and the row is ~1000px — never 3 + 1', () => {
    expect(columnsFor(1030, 280, 12, [4, 2, 1])).toBe(2)
  })
  it('stacks on a phone', () => {
    expect(columnsFor(360, 280, 12, [4, 2, 1])).toBe(1)
  })
})
