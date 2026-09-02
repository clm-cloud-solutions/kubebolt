import { describe, it, expect } from 'vitest'
import { ApiError } from '@/services/api'
import { sessionIsGone } from './AuthContext'

describe('sessionIsGone', () => {
  // The regression. refreshUser runs on every tab focus, so treating these as a
  // logout bounced the operator to /login whenever anything hiccuped — most
  // visibly while a cluster was mid-reconnect and its requests were 503ing.
  it.each([503, 500, 502, 403, 404, 429])('keeps the session on %i', (status) => {
    expect(sessionIsGone(new ApiError(status, 'boom'))).toBe(false)
  })

  it('keeps the session when the network drops', () => {
    expect(sessionIsGone(new TypeError('Failed to fetch'))).toBe(false)
    expect(sessionIsGone(new Error('aborted'))).toBe(false)
  })

  it('keeps the session on a non-error rejection', () => {
    expect(sessionIsGone(undefined)).toBe(false)
    expect(sessionIsGone(null)).toBe(false)
    expect(sessionIsGone('401')).toBe(false)
  })

  // The other half, and it matters just as much: a real 401 must still log out.
  // Without this, the fix would keep a dead session alive and leave the user
  // inside an app where nothing works.
  it('ends the session on a definitive 401', () => {
    expect(sessionIsGone(new ApiError(401, 'unauthorized'))).toBe(true)
  })
})
