import { describe, it, expect } from 'vitest'
import { tokenStatusOf, canMutate, expiryOf } from './agentTokenStatus'
import type { IngestToken } from '@/services/api'

const baseToken: IngestToken = {
  id: 't1',
  prefix: 'kb_abcd',
  label: 'prod',
  createdAt: '2026-01-01T00:00:00Z',
  createdBy: 'admin',
  active: true, // server-derived; tokenStatusOf re-derives client-side from concrete fields
}

const NOW = new Date('2026-04-28T00:00:00Z').getTime()

describe('tokenStatusOf', () => {
  it('returns "active" for a fresh token without revoke or expiry', () => {
    expect(tokenStatusOf(baseToken, NOW)).toBe('active')
  })

  it('returns "active" for a token whose expiry is in the future', () => {
    const tok: IngestToken = { ...baseToken, expiresAt: '2026-12-31T00:00:00Z' }
    expect(tokenStatusOf(tok, NOW)).toBe('active')
  })

  it('returns "expired" once the expiry timestamp is in the past', () => {
    const tok: IngestToken = { ...baseToken, expiresAt: '2026-01-15T00:00:00Z' }
    expect(tokenStatusOf(tok, NOW)).toBe('expired')
  })

  it('returns "revoked" regardless of expiry when revokedAt is set', () => {
    const tok: IngestToken = {
      ...baseToken,
      revokedAt: '2026-02-01T00:00:00Z',
      expiresAt: '2026-12-31T00:00:00Z',
    }
    expect(tokenStatusOf(tok, NOW)).toBe('revoked')
  })

  it('treats revoked as a stronger signal than expired (both true)', () => {
    const tok: IngestToken = {
      ...baseToken,
      revokedAt: '2026-02-01T00:00:00Z',
      expiresAt: '2026-01-15T00:00:00Z',
    }
    expect(tokenStatusOf(tok, NOW)).toBe('revoked')
  })
})

describe('canMutate', () => {
  it('allows rotating / revoking active tokens', () => {
    expect(canMutate(baseToken, NOW)).toBe(true)
  })

  it('forbids mutating already-revoked tokens', () => {
    const tok: IngestToken = { ...baseToken, revokedAt: '2026-02-01T00:00:00Z' }
    expect(canMutate(tok, NOW)).toBe(false)
  })

  it('forbids mutating expired tokens', () => {
    const tok: IngestToken = { ...baseToken, expiresAt: '2026-01-15T00:00:00Z' }
    expect(canMutate(tok, NOW)).toBe(false)
  })
})

describe('wizard tokens with a use-by date', () => {
  const wizard: IngestToken = { ...baseToken, label: 'first-cluster sb 2026-04-20', unusedExpiresAt: '2026-04-27T00:00:00Z' }

  it('is expired once unused past its use-by date', () => {
    expect(tokenStatusOf(wizard, NOW)).toBe('expired')
    expect(canMutate(wizard, NOW)).toBe(false)
  })

  it('stays active before its use-by date', () => {
    expect(tokenStatusOf({ ...wizard, unusedExpiresAt: '2026-05-01T00:00:00Z' }, NOW)).toBe('active')
  })

  it('ignores the use-by date once an agent has used it', () => {
    expect(tokenStatusOf({ ...wizard, lastUsedAt: '2026-04-21T00:00:00Z' }, NOW)).toBe('active')
  })
})

describe('expiryOf', () => {
  it('reads a future hard expiry forward, not as "just now"', () => {
    expect(expiryOf({ ...baseToken, expiresAt: '2026-05-04T00:00:00Z' }, NOW).label).toBe('in 6d')
  })

  it('reads a past expiry backward', () => {
    expect(expiryOf({ ...baseToken, expiresAt: '2026-04-27T21:00:00Z' }, NOW).label).toBe('3h ago')
  })

  it('shows an unused token\'s use-by date as conditional', () => {
    const e = expiryOf({ ...baseToken, unusedExpiresAt: '2026-05-05T00:00:00Z' }, NOW)
    expect(e.label).toBe('if unused, in 7d')
    expect(e.title).toMatch(/unless an agent uses it first/)
  })

  it('rounds a window that has only just started', () => {
    expect(expiryOf({ ...baseToken, unusedExpiresAt: new Date(NOW + 7 * 86_400_000 - 120_000).toISOString() }, NOW).label).toBe('if unused, in 7d')
  })

  it('says never for a token with neither', () => {
    expect(expiryOf(baseToken, NOW).label).toBe('never')
  })
})
