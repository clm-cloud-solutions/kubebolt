import { afterEach, describe, expect, it } from 'vitest'
import { getActionActivity, resetActionActivity, setActionActivity } from './actionActivity'

describe('actionActivity', () => {
  afterEach(() => resetActionActivity())

  it('reports executing over verifying, and clears per proposal', () => {
    expect(getActionActivity()).toBeUndefined()
    setActionActivity('p1', { state: 'verifying', detail: 'catalog-api' })
    expect(getActionActivity()).toEqual({ state: 'verifying', detail: 'catalog-api' })
    setActionActivity('p2', { state: 'executing', detail: 'payments' })
    expect(getActionActivity()).toEqual({ state: 'executing', detail: 'payments' })
    setActionActivity('p2', undefined)
    expect(getActionActivity()).toEqual({ state: 'verifying', detail: 'catalog-api' })
    setActionActivity('p1', undefined)
    expect(getActionActivity()).toBeUndefined()
  })
})
