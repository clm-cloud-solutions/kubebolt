// Pure helpers for AgentTokensPage. Extracted into a sibling module so
// they can be unit-tested without React Testing Library (the project
// uses bare Vitest).
import type { IngestToken } from '@/services/api'

export type TokenStatus = 'active' | 'revoked' | 'expired'

export function tokenStatusOf(token: IngestToken, nowMs: number = Date.now()): TokenStatus {
  if (token.revokedAt) return 'revoked'
  if (token.expiresAt && new Date(token.expiresAt).getTime() < nowMs) return 'expired'
  // A wizard's token that nothing used before its use-by date (the server
  // sends unusedExpiresAt only while the token is unused).
  if (token.unusedExpiresAt && !token.lastUsedAt && new Date(token.unusedExpiresAt).getTime() < nowMs) return 'expired'
  return 'active'
}

// formatWhen renders a date relative to now in both directions: "in 6d",
// "3h ago". The page's formatRelative only knew the past, so a future expiry
// read "just now".
export function formatWhen(dateStr: string, nowMs: number = Date.now()): string {
  const d = new Date(dateStr)
  const diff = d.getTime() - nowMs
  const future = diff > 0
  const mins = Math.floor(Math.abs(diff) / 60000)
  // The past counts whole units elapsed ("6d ago"); the future rounds, so a
  // seven-day window issued a minute ago reads "in 7d", not "in 6d".
  const unit = future ? Math.round : Math.floor
  let span: string
  if (mins < 1) return future ? 'in under a minute' : 'just now'
  if (mins < 60) span = `${mins}m`
  else if (mins < 60 * 24) span = `${unit(mins / 60)}h`
  else if (mins < 60 * 24 * 30) span = `${unit(mins / (60 * 24))}d`
  else return d.toLocaleDateString()
  return future ? `in ${span}` : `${span} ago`
}

// expiryOf is the Expires column: the hard expiry when there is one, else the
// use-by date of a token nothing has used yet, else "never".
export function expiryOf(token: IngestToken, nowMs: number = Date.now()): { label: string; title: string } {
  if (token.expiresAt) {
    return { label: formatWhen(token.expiresAt, nowMs), title: new Date(token.expiresAt).toLocaleString() }
  }
  if (token.unusedExpiresAt && !token.lastUsedAt) {
    return {
      label: `if unused, ${formatWhen(token.unusedExpiresAt, nowMs)}`,
      title: `Stops working on ${new Date(token.unusedExpiresAt).toLocaleString()} unless an agent uses it first. Once used, it does not expire.`,
    }
  }
  return { label: 'never', title: '' }
}

// Both Active and Expired tokens are immutable from the UI side: only
// Active tokens can be rotated or revoked. Pinning that here so the
// page and its tests stay aligned.
export function canMutate(token: IngestToken, nowMs: number = Date.now()): boolean {
  return tokenStatusOf(token, nowMs) === 'active'
}
