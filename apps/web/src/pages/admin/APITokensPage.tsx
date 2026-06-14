// APITokensPage manages long-lived REST API tokens. Two clearly-separated
// kinds, because they have different security models and audiences:
//
//   • Service tokens (kbs_)  — for INTERNAL services (Autopilot, EE). Network-
//     bound: rejected when presented over the public edge. Preset to
//     editor + the Autopilot scopes.
//   • API tokens (kbk_)      — for the customer's own integrations / CI-CD.
//     Usable from anywhere; the operator picks role + scopes freely.
//
// Backend: apps/api/internal/auth/api_tokens_store.go + api_token_handlers.go.
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  KeyRound, Bot, Plus, Trash2, Copy, Check, AlertTriangle, ShieldCheck, Globe,
} from 'lucide-react'
import {
  api,
  type APIToken,
  type APITokenType,
  type IssuedAPIToken,
  type CreateAPITokenRequest,
} from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { Modal } from '@/components/shared/Modal'

// Scope presets offered when creating a customer API token. The operator
// can pick any combination; "Everything" collapses to the wildcard.
const SCOPE_OPTIONS: { value: string; label: string }[] = [
  { value: '/api/v1/cluster/overview', label: 'Cluster overview' },
  { value: '/api/v1/resources', label: 'Resources' },
  { value: '/api/v1/insights', label: 'Insights' },
  { value: '/api/v1/events', label: 'Events' },
  { value: '/api/v1/metrics', label: 'Metrics' },
]
const SCOPE_ALL = '*'
const ROLES = ['viewer', 'editor', 'admin']

function formatRelative(dateStr?: string) {
  if (!dateStr) return '-'
  const d = new Date(dateStr)
  const mins = Math.floor((Date.now() - d.getTime()) / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return d.toLocaleDateString()
}
function formatAbsolute(dateStr?: string) {
  return dateStr ? new Date(dateStr).toLocaleString() : '-'
}

type TokenStatus = 'active' | 'revoked' | 'expired'
function statusOf(t: APIToken): TokenStatus {
  if (t.revokedAt) return 'revoked'
  if (t.expiresAt && new Date(t.expiresAt).getTime() < Date.now()) return 'expired'
  return 'active'
}

function StatusBadge({ token }: { token: APIToken }) {
  const base = 'px-2 py-0.5 rounded-full text-[10px] font-mono font-medium uppercase tracking-wider'
  switch (statusOf(token)) {
    case 'revoked':
      return <span className={`${base} bg-status-error-dim text-status-error`}>Revoked</span>
    case 'expired':
      return <span className={`${base} bg-status-warn-dim text-status-warn`}>Expired</span>
    default:
      return <span className={`${base} bg-status-ok-dim text-status-ok`}>Active</span>
  }
}

function ScopeChips({ scopes }: { scopes?: string[] }) {
  if (!scopes || scopes.length === 0) return <span className="text-xs text-kb-text-tertiary">none</span>
  if (scopes.includes(SCOPE_ALL)) {
    return <span className="px-1.5 py-0.5 rounded text-[10px] font-mono bg-kb-elevated text-kb-text-secondary">all paths</span>
  }
  const shown = scopes.slice(0, 2)
  return (
    <div className="flex flex-wrap items-center gap-1" title={scopes.join('\n')}>
      {shown.map(s => (
        <span key={s} className="px-1.5 py-0.5 rounded text-[10px] font-mono bg-kb-elevated text-kb-text-secondary">
          {s.replace(/^\/api\/v1\//, '')}
        </span>
      ))}
      {scopes.length > shown.length && (
        <span className="text-[10px] text-kb-text-tertiary">+{scopes.length - shown.length}</span>
      )}
    </div>
  )
}

// ─── Reveal-once modal ────────────────────────────────────────────────
function RevealAPITokenModal({ issued, title, onClose }: { issued: IssuedAPIToken; title: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const [acknowledged, setAcknowledged] = useState(false)
  async function handleCopy() {
    await navigator.clipboard.writeText(issued.token)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  return (
    <Modal badge="Token issued" title={title} onClose={onClose} size="sm">
      <div className="p-5 space-y-4">
        <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-warn-dim text-status-warn">
          <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
          <div className="text-xs">
            This is the only time you'll see the full token. Copy it now and
            store it securely <strong>before closing this dialog</strong>.
          </div>
        </div>
        <div className="space-y-1">
          <label className="text-[11px] font-medium text-kb-text-secondary">Token</label>
          <div className="flex gap-2">
            <code data-testid="apitoken-plaintext" className="flex-1 px-3 py-2 text-xs font-mono bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary break-all select-all">
              {issued.token}
            </code>
            <button onClick={handleCopy} title="Copy to clipboard" className="px-3 py-2 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 transition-colors flex items-center gap-1.5">
              {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
              {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
        </div>
        <div className="flex items-start gap-2">
          <input id="ack-apitoken" type="checkbox" checked={acknowledged} onChange={e => setAcknowledged(e.target.checked)} className="mt-0.5" />
          <label htmlFor="ack-apitoken" className="text-xs text-kb-text-secondary cursor-pointer">I have stored this token securely.</label>
        </div>
        <div className="flex justify-end pt-1">
          <button onClick={onClose} disabled={!acknowledged} className="px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors">Close</button>
        </div>
      </div>
    </Modal>
  )
}

// ─── Create modal (adapts to token type) ──────────────────────────────
function CreateAPITokenModal({ tokenType, onClose, onIssued }: { tokenType: APITokenType; onClose: () => void; onIssued: (i: IssuedAPIToken) => void }) {
  const isService = tokenType === 'service'
  const [label, setLabel] = useState('')
  const [ttlDays, setTtlDays] = useState<number | ''>('')
  const [role, setRole] = useState('viewer')
  const [scopeSet, setScopeSet] = useState<Set<string>>(new Set(['/api/v1/resources', '/api/v1/insights']))
  const [allScopes, setAllScopes] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  function toggleScope(v: string) {
    setScopeSet(prev => {
      const next = new Set(prev)
      next.has(v) ? next.delete(v) : next.add(v)
      return next
    })
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const body: CreateAPITokenRequest = { label, type: tokenType }
      if (ttlDays !== '') body.ttlHours = Number(ttlDays) * 24
      if (!isService) {
        body.role = role
        body.scopes = allScopes ? [SCOPE_ALL] : Array.from(scopeSet)
      }
      // Service tokens omit role/scopes → backend applies editor + Autopilot defaults.
      const issued = await api.createAPIToken(body)
      onIssued(issued)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to create token')
    } finally {
      setSubmitting(false)
    }
  }

  const submitDisabled = submitting || !label || (!isService && !allScopes && scopeSet.size === 0)

  return (
    <Modal badge={isService ? 'New service token' : 'New API token'} title={isService ? 'Create service token' : 'Create API token'} onClose={onClose} size="sm">
      <form onSubmit={handleSubmit} className="p-5 space-y-3">
        {error && <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>}

        {/* Per-type security note */}
        {isService ? (
          <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-kb-elevated text-kb-text-secondary text-xs">
            <ShieldCheck className="w-4 h-4 mt-0.5 shrink-0" />
            <span>Internal use only (e.g. Autopilot). Rejected when presented from outside your network. Preset to <strong>editor</strong> with the Autopilot scopes.</span>
          </div>
        ) : (
          <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-kb-elevated text-kb-text-secondary text-xs">
            <Globe className="w-4 h-4 mt-0.5 shrink-0" />
            <span>For CI/CD and external integrations. Usable from anywhere — scope it to the minimum your integration needs.</span>
          </div>
        )}

        <div className="space-y-1">
          <label className="text-[11px] font-medium text-kb-text-secondary">Label</label>
          <input value={label} onChange={e => setLabel(e.target.value)} required autoFocus
            placeholder={isService ? 'autopilot' : 'github-actions, deploy-bot, ...'}
            className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors" />
        </div>

        {!isService && (
          <>
            <div className="space-y-1">
              <label className="text-[11px] font-medium text-kb-text-secondary">Role</label>
              <select value={role} onChange={e => setRole(e.target.value)}
                className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary focus:outline-none focus:border-kb-accent transition-colors">
                {ROLES.map(r => <option key={r} value={r}>{r}</option>)}
              </select>
            </div>
            <div className="space-y-1.5">
              <label className="text-[11px] font-medium text-kb-text-secondary">Scopes</label>
              <label className="flex items-center gap-2 text-xs text-kb-text-secondary cursor-pointer">
                <input type="checkbox" checked={allScopes} onChange={e => setAllScopes(e.target.checked)} />
                Everything (all authenticated paths)
              </label>
              {!allScopes && (
                <div className="grid grid-cols-2 gap-1.5 pt-0.5">
                  {SCOPE_OPTIONS.map(o => (
                    <label key={o.value} className="flex items-center gap-2 text-xs text-kb-text-secondary cursor-pointer">
                      <input type="checkbox" checked={scopeSet.has(o.value)} onChange={() => toggleScope(o.value)} />
                      {o.label}
                    </label>
                  ))}
                </div>
              )}
            </div>
          </>
        )}

        <div className="space-y-1">
          <label className="text-[11px] font-medium text-kb-text-secondary">Expires after (days)</label>
          <input type="number" min={1} value={ttlDays}
            onChange={e => setTtlDays(e.target.value === '' ? '' : Number(e.target.value))}
            placeholder="never"
            className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors" />
          <p className="text-[10px] text-kb-text-tertiary">Leave blank for no expiration. You can revoke any token manually.</p>
        </div>

        <div className="flex justify-end gap-2 pt-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors">Cancel</button>
          <button type="submit" disabled={submitDisabled} className="px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-50 transition-colors">
            {submitting ? 'Creating...' : 'Create token'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ─── Confirm revoke ───────────────────────────────────────────────────
function ConfirmRevokeAPIModal({ token, onClose, onRevoked }: { token: APIToken; onClose: () => void; onRevoked: () => void }) {
  const [confirmText, setConfirmText] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleConfirm() {
    setError(null)
    setSubmitting(true)
    try {
      await api.revokeAPIToken(token.id)
      onRevoked()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to revoke token')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Modal badge="Revoke token" title={token.label} onClose={onClose} size="sm">
      <div className="p-5 space-y-3">
        <p className="text-xs text-kb-text-tertiary">
          Type <strong className="text-kb-text-primary font-mono">{token.label}</strong> to confirm.
          Any service or integration using this token stops working immediately. This cannot be undone.
        </p>
        {error && <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>}
        <input value={confirmText} onChange={e => setConfirmText(e.target.value)} placeholder={token.label} autoFocus
          className="w-full px-3 py-1.5 text-sm font-mono bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-status-error transition-colors" />
        <div className="flex justify-end gap-2 pt-1">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors">Cancel</button>
          <button onClick={handleConfirm} disabled={confirmText !== token.label || submitting}
            className="px-3 py-1.5 text-xs font-medium text-white bg-status-error rounded-lg hover:bg-status-error/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors">
            {submitting ? 'Revoking...' : 'Revoke token'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ─── Token table (shared by both sections) ────────────────────────────
function TokenTable({ tokens, emptyHint, onRevoke }: { tokens: APIToken[]; emptyHint: string; onRevoke: (t: APIToken) => void }) {
  const th = 'px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary'
  return (
    <div className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
      <table className="w-full">
        <thead>
          <tr className="border-b border-kb-border">
            <th className={th}>Label</th>
            <th className={th}>Prefix</th>
            <th className={th}>Role</th>
            <th className={th}>Scopes</th>
            <th className={th}>Status</th>
            <th className={th}>Last used</th>
            <th className={th}>Expires</th>
            <th className={`${th} text-right`}>Actions</th>
          </tr>
        </thead>
        <tbody>
          {tokens.map(tok => {
            const revoked = statusOf(tok) === 'revoked'
            return (
              <tr key={tok.id} className="border-b border-kb-border last:border-b-0 hover:bg-kb-card-hover transition-colors">
                <td className="px-4 py-2.5 text-xs font-medium text-kb-text-primary">{tok.label}</td>
                <td className="px-4 py-2.5 text-xs font-mono text-kb-text-secondary">{tok.prefix}…</td>
                <td className="px-4 py-2.5 text-xs text-kb-text-secondary">{tok.role}</td>
                <td className="px-4 py-2.5"><ScopeChips scopes={tok.scopes} /></td>
                <td className="px-4 py-2.5"><StatusBadge token={tok} /></td>
                <td className="px-4 py-2.5 text-xs text-kb-text-tertiary font-mono" title={formatAbsolute(tok.lastUsedAt)}>{formatRelative(tok.lastUsedAt)}</td>
                <td className="px-4 py-2.5 text-xs text-kb-text-tertiary font-mono" title={tok.expiresAt ? formatAbsolute(tok.expiresAt) : ''}>{tok.expiresAt ? formatRelative(tok.expiresAt) : 'never'}</td>
                <td className="px-4 py-2.5">
                  <div className="flex items-center justify-end">
                    <button onClick={() => onRevoke(tok)} disabled={revoked}
                      className="p-1.5 rounded-md text-kb-text-tertiary hover:text-status-error hover:bg-status-error-dim disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                      title="Revoke token">
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                </td>
              </tr>
            )
          })}
          {tokens.length === 0 && (
            <tr><td colSpan={8} className="px-4 py-8 text-center text-xs text-kb-text-tertiary">{emptyHint}</td></tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────
export function APITokensPage() {
  const queryClient = useQueryClient()
  const [creating, setCreating] = useState<APITokenType | null>(null)
  const [revoking, setRevoking] = useState<APIToken | null>(null)
  const [revealed, setRevealed] = useState<{ issued: IssuedAPIToken; title: string } | null>(null)

  const { data: tokens, isLoading, error } = useQuery({
    queryKey: ['admin-api-tokens'],
    queryFn: api.listAPITokens,
  })
  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['admin-api-tokens'] })

  if (isLoading) {
    return <div className="flex items-center justify-center h-64"><LoadingSpinner size="lg" /></div>
  }
  if (error) {
    return <ErrorState message={error instanceof Error ? error.message : 'Failed to load tokens'} onRetry={invalidate} />
  }

  const all = tokens ?? []
  const serviceTokens = all.filter(t => t.type === 'service')
  const apiTokens = all.filter(t => t.type === 'apikey')

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
          <KeyRound className="w-5 h-5" />
          API &amp; service tokens
        </h1>
        <p className="text-xs text-kb-text-tertiary mt-0.5">
          Long-lived bearer tokens for non-interactive callers. The plaintext is shown once at creation — store it immediately.
        </p>
      </div>

      {/* ── Service tokens ── */}
      <section>
        <div className="flex items-center justify-between mb-3">
          <div>
            <h2 className="text-sm font-semibold text-kb-text-primary flex items-center gap-2">
              <ShieldCheck className="w-4 h-4 text-kb-text-secondary" />
              Service tokens
              <span className="px-1.5 py-0.5 rounded text-[10px] font-mono uppercase tracking-wider bg-kb-elevated text-kb-text-secondary">internal</span>
            </h2>
            <p className="text-xs text-kb-text-tertiary mt-0.5">
              For internal services like Autopilot. <strong>Rejected over the public edge</strong> — usable only from inside your network. Prefix <code className="font-mono">kbs_</code>.
            </p>
          </div>
          <button onClick={() => setCreating('service')} className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 transition-colors shrink-0">
            <Bot className="w-3.5 h-3.5" />
            New service token
          </button>
        </div>
        <TokenTable tokens={serviceTokens} onRevoke={setRevoking}
          emptyHint='No service tokens yet. Create one for Autopilot or another internal service.' />
      </section>

      {/* ── API tokens ── */}
      <section>
        <div className="flex items-center justify-between mb-3">
          <div>
            <h2 className="text-sm font-semibold text-kb-text-primary flex items-center gap-2">
              <Globe className="w-4 h-4 text-kb-text-secondary" />
              API tokens
              <span className="px-1.5 py-0.5 rounded text-[10px] font-mono uppercase tracking-wider bg-kb-elevated text-kb-text-secondary">integrations</span>
            </h2>
            <p className="text-xs text-kb-text-tertiary mt-0.5">
              For your own integrations and CI/CD pipelines. Usable from anywhere; scope them as you wish. Prefix <code className="font-mono">kbk_</code>.
            </p>
          </div>
          <button onClick={() => setCreating('apikey')} className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 transition-colors shrink-0">
            <Plus className="w-3.5 h-3.5" />
            New API token
          </button>
        </div>
        <TokenTable tokens={apiTokens} onRevoke={setRevoking}
          emptyHint='No API tokens yet. Create one for CI/CD or an external integration.' />
      </section>

      {creating && (
        <CreateAPITokenModal
          tokenType={creating}
          onClose={() => setCreating(null)}
          onIssued={(issued) => {
            setCreating(null)
            setRevealed({ issued, title: `New token: ${issued.apiToken.label}` })
            invalidate()
          }}
        />
      )}
      {revoking && (
        <ConfirmRevokeAPIModal token={revoking} onClose={() => setRevoking(null)} onRevoked={invalidate} />
      )}
      {revealed && (
        <RevealAPITokenModal issued={revealed.issued} title={revealed.title} onClose={() => setRevealed(null)} />
      )}
    </div>
  )
}
