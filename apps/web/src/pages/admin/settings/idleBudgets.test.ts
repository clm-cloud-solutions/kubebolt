import { describe, expect, it } from 'vitest'
import { effectiveIdleBudget, idleBudgetsRaised } from './idleBudgets'

describe('idle budgets', () => {
  it('raises a budget below the request timeout plus headroom', () => {
    expect(effectiveIdleBudget(25, 30)).toBe(35)
    expect(effectiveIdleBudget(60, 30)).toBe(60)
    expect(effectiveIdleBudget(60, 90)).toBe(95)
  })

  it('keeps a disabled watchdog disabled', () => {
    expect(effectiveIdleBudget(0, 30)).toBe(0)
  })

  it('lists only the budgets the API would raise', () => {
    expect(
      idleBudgetsRaised({ connectTimeoutSeconds: '25', stuckTimeoutSeconds: '45', requestTimeoutSeconds: '30' }),
    ).toEqual([{ name: 'Connect deadline', configured: 25, effective: 35 }])
    expect(
      idleBudgetsRaised({ connectTimeoutSeconds: '60', stuckTimeoutSeconds: '0', requestTimeoutSeconds: '30' }),
    ).toEqual([])
    expect(
      idleBudgetsRaised({ connectTimeoutSeconds: '60', stuckTimeoutSeconds: '45', requestTimeoutSeconds: '' }),
    ).toEqual([])
  })
})
