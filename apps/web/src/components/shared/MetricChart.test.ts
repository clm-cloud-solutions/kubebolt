import { describe, expect, it } from 'vitest'
import { barStep, stepSeconds } from './MetricChart'

describe('stepSeconds', () => {
  it('reads the range steps as seconds', () => {
    expect(stepSeconds('15s')).toBe(15)
    expect(stepSeconds('2m')).toBe(120)
    expect(stepSeconds('1h')).toBe(3600)
    expect(stepSeconds('1d')).toBe(86400)
    expect(stepSeconds('')).toBe(0)
    expect(stepSeconds('5x')).toBe(0)
  })
})

describe('barStep', () => {
  it('widens the bar interval with the range, never under the 30 s push', () => {
    expect(barStep(5, '15s')).toBe('30s')
    expect(barStep(60, '30s')).toBe('2m')
    expect(barStep(360, '2m')).toBe('15m')
    expect(barStep(1440, '10m')).toBe('1h')
    expect(barStep(10080, '1h')).toBe('6h')
    expect(barStep(43200, '6h')).toBe('1d')
    expect(barStep(999, '5m')).toBe('5m')
  })
})
