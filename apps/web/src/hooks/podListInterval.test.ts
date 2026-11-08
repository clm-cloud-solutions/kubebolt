import { describe, it, expect } from 'vitest'
import { podListInterval } from './useResources'

describe('podListInterval', () => {
  it('the Pods tab keeps the page interval', () => {
    expect(podListInterval(5_000, 'table')).toBe(5_000)
    expect(podListInterval(30_000, 'table')).toBe(30_000)
  })

  it('Logs and Terminal pickers refresh at most once a minute', () => {
    expect(podListInterval(5_000, 'picker')).toBe(60_000)
    expect(podListInterval(30_000, 'picker')).toBe(60_000)
  })

  it('a page interval slower than a minute wins for pickers too', () => {
    expect(podListInterval(120_000, 'picker')).toBe(120_000)
  })
})
