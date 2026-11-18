import { describe, expect, it } from 'vitest'
import { collectedLabel, historyNeededLabel, historySpanLabel, rightSizingState } from './useRightSizing'

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
  it('counts hours towards the 2 days the rules need', () => {
    expect(collectedLabel(0.5)).toBe('12h of 2 days collected')
  })

  it('rounds a sliver of history up to one hour', () => {
    expect(collectedLabel(0.01)).toBe('1h of 2 days collected')
  })

  it('says so when there is no history at all', () => {
    expect(collectedLabel(undefined)).toBe('no usage history yet')
    expect(collectedLabel(0)).toBe('no usage history yet')
  })
})

describe('a platform that lowered the minimum history', () => {
  it('counts towards the minimum it set', () => {
    expect(collectedLabel(20 / 24, 36)).toBe('20h of 1 day 12h collected')
    expect(collectedLabel(30 / 24, 72)).toBe('1 day 6h of 3 days collected')
  })
  it('says what is needed in days, hours under a day', () => {
    expect(historyNeededLabel()).toBe('2 days')
    expect(historyNeededLabel(24)).toBe('1 day')
    expect(historyNeededLabel(36)).toBe('1 day 12h')
    expect(historyNeededLabel(12)).toBe('12h')
  })
  it('names a P95 under two days exactly, whole days above', () => {
    expect(historySpanLabel(44 / 24)).toBe('1 day 20h')
    expect(historySpanLabel(10 / 24)).toBe('10h')
    expect(historySpanLabel(3)).toBe('3d')
    expect(historySpanLabel(undefined)).toBe('7d')
  })
})
