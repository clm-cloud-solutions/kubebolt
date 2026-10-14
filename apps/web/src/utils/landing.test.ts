import { describe, expect, it } from 'vitest'
import { landingAfterAuth, POST_AUTH_LANDING } from './routes'

describe('landingAfterAuth', () => {
  it('returns to the deep link that asked for the sign-in, query included', () => {
    expect(landingAfterAuth('/admin/insights?tab=rules')).toBe('/admin/insights?tab=rules')
    expect(landingAfterAuth('/pods')).toBe('/pods')
  })

  it('never lands on a sign-in page, with or without a query', () => {
    expect(landingAfterAuth('/login')).toBe(POST_AUTH_LANDING)
    expect(landingAfterAuth('/login?verified=ok')).toBe(POST_AUTH_LANDING)
    expect(landingAfterAuth('/reset-password?token=abc')).toBe(POST_AUTH_LANDING)
  })

  it('rejects anything that is not a path on this app', () => {
    expect(landingAfterAuth(undefined)).toBe(POST_AUTH_LANDING)
    expect(landingAfterAuth('https://evil.example')).toBe(POST_AUTH_LANDING)
    expect(landingAfterAuth('//evil.example')).toBe(POST_AUTH_LANDING)
  })
})
