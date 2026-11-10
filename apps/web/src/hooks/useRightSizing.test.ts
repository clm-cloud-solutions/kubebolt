import { describe, expect, it } from 'vitest'
import { collectedLabel, rightSizingState } from './useRightSizing'

const base = { isLoading: false, error: null, insufficientHistory: false }

describe('rightSizingState', () => {
  it('says clean only when a read succeeded, history is enough and nothing breaks a rule', () => {
    expect(rightSizingState(base, 0)).toBe('clean')
  })

  it('never reads a failed request as clean', () => {
    expect(rightSizingState({ ...base, error: new Error('502') }, 0)).toBe('error')
  })

  it('never reads a cluster still collecting as clean', () => {
    expect(rightSizingState({ ...base, insufficientHistory: true }, 0)).toBe('collecting')
  })

  it('is loading before the first answer', () => {
    expect(rightSizingState({ ...base, isLoading: true }, 0)).toBe('loading')
  })

  it('keeps recommendations already on screen over a later failed refresh', () => {
    expect(rightSizingState({ ...base, error: new Error('502') }, 3)).toBe('recs')
  })
})

describe('collectedLabel', () => {
  it('counts hours towards the ~48 the rules need', () => {
    expect(collectedLabel(0.5)).toBe('12h of ~48h collected')
  })

  it('rounds a sliver of history up to one hour', () => {
    expect(collectedLabel(0.01)).toBe('1h of ~48h collected')
  })

  it('says so when there is no history at all', () => {
    expect(collectedLabel(undefined)).toBe('no usage history yet')
    expect(collectedLabel(0)).toBe('no usage history yet')
  })
})
