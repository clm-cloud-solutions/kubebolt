import { describe, expect, it } from 'vitest'
import { jobStatus } from './ApiHealthSections'

// A job is late after three intervals without a success (two minutes at
// least, so the 30-second metrics push is not late for one slow push).
describe('jobStatus', () => {
  it('is on time inside three intervals', () => {
    expect(jobStatus(150, 60, 0).label).toBe('On time')
    expect(jobStatus(100, 30, 0).label).toBe('On time') // 2-minute floor
  })
  it('measures a job that has not succeeded yet from when it was declared', () => {
    // The caller passes the time since declaration: an hourly job is on time
    // for its first three hours.
    expect(jobStatus(600, 3600, 0).label).toBe('On time')
  })
  it('is late after three intervals with no failed run — the job stopped', () => {
    expect(jobStatus(3 * 3600 + 1, 3600, 0).label).toBe('Late')
    expect(jobStatus(null, 600, 0).label).toBe('Late')
  })
  it('is failing when late and runs fail', () => {
    expect(jobStatus(400, 60, 3).label).toBe('Failing')
  })
  it('flags errors even when the last success is recent', () => {
    expect(jobStatus(20, 60, 1).label).toBe('Errors')
  })
})
