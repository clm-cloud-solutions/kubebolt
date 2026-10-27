import { describe, it, expect } from 'vitest'
import { HUBBLE_STALE_AFTER_S, isHubbleStale, lastSampleTime } from './useHubbleAvailable'

// value[0] is the evaluation time; value[1] is what tlast_over_time
// returns, the timestamp of the newest sample in the range.
function resp(value: string | null) {
  return {
    status: 'success',
    data: {
      resultType: 'vector',
      result: value === null ? [] : [{ metric: {}, value: [1793050000, value] }],
    },
  }
}

describe('lastSampleTime', () => {
  it('reads the newest sample time', () => {
    expect(lastSampleTime(resp('1793047378'))).toBe(1793047378)
  })

  it('is null when the range holds no sample', () => {
    expect(lastSampleTime(resp(null))).toBeNull()
    expect(lastSampleTime(undefined)).toBeNull()
  })

  it('is null on a value that is not a time', () => {
    expect(lastSampleTime(resp('NaN'))).toBeNull()
    expect(lastSampleTime(resp('0'))).toBeNull()
  })
})

describe('isHubbleStale', () => {
  const now = 1793050000

  it('is fresh within the window', () => {
    expect(isHubbleStale(now - 30, now)).toBe(false)
    expect(isHubbleStale(now - HUBBLE_STALE_AFTER_S, now)).toBe(false)
  })

  it('is stale past the window', () => {
    expect(isHubbleStale(now - HUBBLE_STALE_AFTER_S - 1, now)).toBe(true)
  })

  // No sample in the range is "never seen", a different state with its
  // own placeholder: it is not stale.
  it('is not stale without a sample', () => {
    expect(isHubbleStale(null, now)).toBe(false)
  })
})
