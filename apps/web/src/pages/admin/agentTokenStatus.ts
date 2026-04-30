// Pure helpers for AgentTokensPage. Extracted into a sibling module so
// they can be unit-tested without React Testing Library (the project
// uses bare Vitest).
import type { IngestToken } from '@/services/api'

export type TokenStatus = 'active' | 'revoked' | 'expired'

export function tokenStatusOf(token: IngestToken, nowMs: number = Date.now()): TokenStatus {
  if (token.revokedAt) return 'revoked'
  if (token.expiresAt && new Date(token.expiresAt).getTime() < nowMs) return 'expired'
  return 'active'
}

// Both Active and Expired tokens are immutable from the UI side: only
// Active tokens can be rotated or revoked. Pinning that here so the
// page and its tests stay aligned.
export function canMutate(token: IngestToken, nowMs: number = Date.now()): boolean {
  return tokenStatusOf(token, nowMs) === 'active'
}
