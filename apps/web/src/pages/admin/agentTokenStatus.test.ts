import { describe, it, expect } from 'vitest'
import { tokenStatusOf, canMutate } from './agentTokenStatus'
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
