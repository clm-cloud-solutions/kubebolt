import { describe, expect, it } from 'vitest'
import { barStep, queryForStep, stepSeconds, windowSteps } from './MetricChart'
import { gaugeMax1m, peakPerStep } from '@/utils/promql'

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

describe('windowSteps', () => {
  it('takes the steps of the shortest range that covers a window', () => {
    // 12 days of a month: the 14d line step, the 14d bar.
    expect(windowSteps(12 * 1440)).toEqual({ line: '2h', bar: '12h' })
    // A 30-day month is the 30d range exactly.
    expect(windowSteps(30 * 1440)).toEqual({ line: '6h', bar: '1d' })
  })

  it('goes coarser past the longest range', () => {
    // A 31-day month, a 92-day custom range.
    expect(windowSteps(31 * 1440)).toEqual({ line: '12h', bar: '1d' })
    expect(windowSteps(92 * 1440)).toEqual({ line: '12h', bar: '1d' })
  })
})

describe('queryForStep', () => {
  const spec = {
    query: 'sum(container_memory_working_set_bytes{pod="p"})',
    peak: peakPerStep(`sum(${gaugeMax1m('container_memory_working_set_bytes{pod="p"}')})`),
  }

  it('sends the plain query while a step spans a minute or less', () => {
    for (const step of ['15s', '30s', '1m']) {
      expect(queryForStep(spec, step)).toBe(spec.query)
    }
  })

  it('sends the peak of each step once a step spans more than a minute', () => {
    expect(queryForStep(spec, '1h')).toBe(
      'max_over_time((sum(max_over_time(container_memory_working_set_bytes{pod="p"}[1m])))[1h:1m])',
    )
    expect(queryForStep(spec, '2m')).toBe(
      'max_over_time((sum(max_over_time(container_memory_working_set_bytes{pod="p"}[1m])))[2m:1m])',
    )
  })

  it('leaves a spec without a peak variant alone at every step', () => {
    const plain = { query: 'sum(rate(container_network_receive_bytes_total[1m]))' }
    expect(queryForStep(plain, '15s')).toBe(plain.query)
    expect(queryForStep(plain, '6h')).toBe(plain.query)
  })
})
