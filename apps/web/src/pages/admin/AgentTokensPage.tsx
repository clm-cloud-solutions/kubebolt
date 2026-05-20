// AgentTokensPage operates on the auto-seeded "default" tenant only.
//
// ENTERPRISE-CANDIDATE (multi-tenant management):
// The backend exposes List/Create/Update/Delete tenants (see
// auth/tenant_handlers.go) but the OSS frontend deliberately surfaces
// only the default tenant. A multi-tenant management UI — tenant
// selector, billing, plans, per-tenant dashboards — lands in the
// Enterprise edition when SaaS hospedado launches.
import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Plus, RefreshCw, Trash2, Copy, Check, AlertTriangle, ChevronDown } from 'lucide-react'
import { api, type IngestToken, type IssuedToken } from '@/services/api'
import type { ClusterInfo } from '@/types/kubernetes'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { Modal } from '@/components/shared/Modal'
import { tokenStatusOf, canMutate } from './agentTokenStatus'

const DEFAULT_TENANT_NAME = 'default'

function formatRelative(dateStr?: string) {
  if (!dateStr) return '-'
  const d = new Date(dateStr)
  const diffMs = Date.now() - d.getTime()
  const mins = Math.floor(diffMs / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return d.toLocaleDateString()
}

function formatAbsolute(dateStr?: string) {
  if (!dateStr) return '-'
  return new Date(dateStr).toLocaleString()
}

function StatusBadge({ token }: { token: IngestToken }) {
  const status = tokenStatusOf(token)
  switch (status) {
    case 'revoked':
      return <span className="px-2 py-0.5 rounded-full text-[10px] font-mono font-medium uppercase tracking-wider bg-status-error-dim text-status-error">Revoked</span>
    case 'expired':
      return <span className="px-2 py-0.5 rounded-full text-[10px] font-mono font-medium uppercase tracking-wider bg-status-warn-dim text-status-warn">Expired</span>
    default:
      return <span className="px-2 py-0.5 rounded-full text-[10px] font-mono font-medium uppercase tracking-wider bg-status-ok-dim text-status-ok">Active</span>
  }
}

// ─── New-token reveal modal ───────────────────────────────────────────
//
// The plaintext is the only time the API returns the secret. Closing
// the modal drops it from React state and there is no API to retrieve
// it again. The user must copy it before closing.

interface RevealTokenModalProps {
  issued: IssuedToken
  title: string
  onClose: () => void
}

function RevealTokenModal({ issued, title, onClose }: RevealTokenModalProps) {
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
            This is the only time you'll see the full token. Store it in your
            agent's Secret manager <strong>before closing this dialog</strong>.
          </div>
        </div>

        <div className="space-y-1">
          <label className="text-[11px] font-medium text-kb-text-secondary">Token</label>
          <div className="flex gap-2">
            <code
              data-testid="token-plaintext"
              className="flex-1 px-3 py-2 text-xs font-mono bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary break-all select-all"
            >
              {issued.token}
            </code>
            <button
              onClick={handleCopy}
              className="px-3 py-2 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 transition-colors flex items-center gap-1.5"
              title="Copy to clipboard"
            >
              {copied ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
              {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
        </div>

        <div className="flex items-start gap-2">
          <input
            id="ack-stored"
            type="checkbox"
            checked={acknowledged}
            onChange={e => setAcknowledged(e.target.checked)}
            className="mt-0.5"
          />
          <label htmlFor="ack-stored" className="text-xs text-kb-text-secondary cursor-pointer">
            I have stored this token securely.
          </label>
        </div>

        <div className="flex justify-end pt-1">
          <button
            onClick={onClose}
            disabled={!acknowledged}
            className="px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            Close
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ─── Cluster scope dropdown ───────────────────────────────────────────

// shortClusterName extracts a human-readable short label from a
// ClusterInfo. Order of preference:
//
//   1. operator-set DisplayName when present (the operator already
//      decided this is the most readable form).
//   2. last path segment of EKS-style ARNs
//      ("arn:aws:eks:us-east-1:123:cluster/sucal-eks-uat" → "sucal-eks-uat").
//   3. last path segment of GKE-style names
//      ("gke_my-project_us-central1-a_my-cluster" → "my-cluster").
//   4. agent-proxy display
//      ("agent:5368e0d2-..." → "via agent"; the cluster name itself
//      comes from the Hello message, exposed as DisplayName when set).
//   5. fallback: the context name itself.
//
// Defensive against empty inputs — never returns "" so a dropdown
// row always has visible text.
function shortClusterName(c: ClusterInfo): string {
  if (c.displayName) return c.displayName
  const ctx = c.context
  if (ctx.startsWith('arn:aws:eks:')) {
    const slash = ctx.lastIndexOf('/')
    if (slash >= 0 && slash < ctx.length - 1) return ctx.slice(slash + 1)
  }
  if (ctx.startsWith('gke_')) {
    const underscore = ctx.lastIndexOf('_')
    if (underscore >= 0 && underscore < ctx.length - 1) return ctx.slice(underscore + 1)
  }
  if (ctx.startsWith('agent:')) return 'via agent'
  return ctx
}

interface ClusterScopeDropdownProps {
  clusters: ClusterInfo[]
  value: string
  onChange: (clusterId: string) => void
}

// ClusterScopeDropdown renders a button + popup list. Each row
// shows the short cluster name (primary text) plus the full context
// name on a second line (muted) so the operator can verify they're
// picking the right one. "Any cluster (advanced)" is a trailing
// option separated by a thin rule.
function ClusterScopeDropdown({ clusters, value, onChange }: ClusterScopeDropdownProps) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  // Click-outside + Escape to close. Mirror of the topbar cluster
  // switcher's interaction model so the operator's muscle memory
  // carries over.
  useEffect(() => {
    if (!open) return
    function onDocClick(e: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDocClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const selected = clusters.find((c) => c.clusterId === value)

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full flex items-center justify-between gap-2 px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary hover:bg-kb-card-hover focus:outline-none focus:border-kb-accent transition-colors"
      >
        <div className="flex items-baseline gap-2 min-w-0">
          {selected ? (
            <>
              <span className="truncate">{shortClusterName(selected)}</span>
              {selected.active && (
                <span className="text-[10px] uppercase tracking-wider text-kb-text-tertiary shrink-0">
                  active
                </span>
              )}
            </>
          ) : (
            <span className="text-kb-text-secondary">Any cluster (advanced)</span>
          )}
        </div>
        <ChevronDown className={`w-3.5 h-3.5 text-kb-text-tertiary shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div className="absolute left-0 right-0 mt-1 z-10 bg-kb-card border border-kb-border rounded-lg shadow-lg max-h-72 overflow-auto">
          {clusters.map((c) => {
            const isSel = c.clusterId === value
            return (
              <button
                key={c.clusterId}
                type="button"
                onClick={() => {
                  onChange(c.clusterId ?? '')
                  setOpen(false)
                }}
                className={`w-full text-left px-3 py-2 hover:bg-kb-card-hover transition-colors ${isSel ? 'bg-kb-card-hover' : ''}`}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm text-kb-text-primary truncate">
                    {shortClusterName(c)}
                  </span>
                  {c.active && (
                    <span className="text-[10px] uppercase tracking-wider text-kb-text-tertiary shrink-0">
                      active
                    </span>
                  )}
                </div>
                <div className="text-[10px] font-mono text-kb-text-tertiary truncate mt-0.5">
                  {c.context}
                </div>
              </button>
            )
          })}
          {/* "Any cluster" lives at the bottom, separated by a thin
              rule. It's the escape hatch for the agent-install
              flow, not the recommended default — keep it visually
              de-emphasized so operators don't reach for it
              accidentally. */}
          <div className="border-t border-kb-border">
            <button
              type="button"
              onClick={() => {
                onChange('')
                setOpen(false)
              }}
              className={`w-full text-left px-3 py-2 hover:bg-kb-card-hover transition-colors ${value === '' ? 'bg-kb-card-hover' : ''}`}
            >
              <div className="text-sm text-kb-text-secondary">
                Any cluster <span className="text-[10px] uppercase tracking-wider text-kb-text-tertiary ml-1">advanced</span>
              </div>
              <div className="text-[10px] text-kb-text-tertiary mt-0.5">
                No cluster binding — for tokens reused across clusters or agent-install
              </div>
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// ─── Issue token modal ────────────────────────────────────────────────

interface IssueTokenModalProps {
  tenantID: string
  onClose: () => void
  onIssued: (issued: IssuedToken) => void
}

function IssueTokenModal({ tenantID, onClose, onIssued }: IssueTokenModalProps) {
  const [label, setLabel] = useState('')
  const [ttlDays, setTtlDays] = useState<number | ''>('')
  // Cluster scope. Empty string = "Any cluster" (legacy unscoped
  // behaviour, also useful for agent-install tokens where the
  // agent itself carries cluster_id via the Hello message — no
  // per-token binding needed). Populated UUIDs come from clusters
  // whose ID is already known — currently agent-proxy contexts
  // that registered via Hello.
  const [clusterId, setClusterId] = useState<string>('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  // Load the known clusters so the operator can pick a scope.
  // Tolerant of the empty case (no clusters or all without UID) —
  // the dropdown simply renders "Any cluster" only.
  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: () => api.listClusters(),
    staleTime: 60_000,
  })
  const scopedClusters = (clusters ?? []).filter((c) => !!c.clusterId)
  // Default the picker to the active cluster (if scoped) so the
  // common case "I'm wiring up Prom in the cluster I'm looking at"
  // works out of the box. The operator can still pick "Any cluster"
  // for the agent-install flow where the agent carries the
  // cluster_id at runtime.
  const activeScoped = scopedClusters.find((c) => c.active)
  useEffect(() => {
    if (clusterId === '' && activeScoped?.clusterId) {
      setClusterId(activeScoped.clusterId)
    }
    // Run only on initial active-cluster resolution. Operator
    // changes to the dropdown thereafter must stick.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeScoped?.clusterId])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try {
      const ttlSeconds = ttlDays === '' ? undefined : Number(ttlDays) * 86_400
      const issued = await api.issueAgentToken(
        tenantID,
        label,
        ttlSeconds,
        clusterId || undefined,
      )
      onIssued(issued)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to issue token')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Modal badge="New token" title="Issue agent token" onClose={onClose} size="sm">
      <form onSubmit={handleSubmit} className="p-5 space-y-3">
        {error && (
          <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>
        )}

        <div className="space-y-1">
          <label className="text-[11px] font-medium text-kb-text-secondary">Label</label>
          <input
            value={label}
            onChange={e => setLabel(e.target.value)}
            required
            placeholder="prod-east, staging, ..."
            autoFocus
            className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors"
          />
          <p className="text-[10px] text-kb-text-tertiary">
            Human-readable name. The token plaintext is independent of this.
          </p>
        </div>

        {/* Cluster scope — visible only when at least one cluster
            has a known UID. For installs where every cluster is
            direct-kubeconfig (no agent-proxy registration yet) we
            silently fall back to "Any cluster" without showing a
            useless dropdown.

            Recommended path: pick a cluster. For Prometheus
            remote_write tokens, this is the only way KubeBolt can
            attribute samples correctly in multi-cluster installs.
            Agent-install tokens are the exception — the agent
            carries cluster_id in its Hello message, so "Any cluster"
            still works there. The default selection is the active
            cluster to make the common case one click.

            Custom dropdown rather than native <select> — option
            labels need two lines (short name + full context muted)
            because EKS / GKE / managed contexts have long ARN-like
            names that are unreadable on a single line, and the
            kubeconfig context is the canonical identifier so we
            can't just hide it. */}
        {scopedClusters.length > 0 && (
          <div className="space-y-1">
            <label className="text-[11px] font-medium text-kb-text-secondary">
              Cluster scope
              <span className="ml-1.5 text-[9px] uppercase tracking-wider text-kb-text-tertiary font-normal">
                recommended
              </span>
            </label>
            <ClusterScopeDropdown
              clusters={scopedClusters}
              value={clusterId}
              onChange={setClusterId}
            />
            <p className="text-[10px] text-kb-text-tertiary">
              For <span className="text-kb-text-secondary">Prometheus remote_write</span>{' '}
              tokens, pick the cluster the Prom instance monitors so
              KubeBolt can attribute samples correctly. For{' '}
              <span className="text-kb-text-secondary">agent-install</span>{' '}
              tokens, "Any cluster" is fine — the agent stamps its
              own cluster_id at runtime.
            </p>
          </div>
        )}

        <div className="space-y-1">
          <label className="text-[11px] font-medium text-kb-text-secondary">Expires after (days)</label>
          <input
            type="number"
            min={1}
            value={ttlDays}
            onChange={e => setTtlDays(e.target.value === '' ? '' : Number(e.target.value))}
            placeholder="never"
            className="w-full px-3 py-1.5 text-sm bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-kb-accent transition-colors"
          />
          <p className="text-[10px] text-kb-text-tertiary">
            Leave blank for no expiration. You can revoke any token manually.
          </p>
        </div>

        <div className="flex justify-end gap-2 pt-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors">Cancel</button>
          <button type="submit" disabled={submitting || !label} className="px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-50 transition-colors">
            {submitting ? 'Issuing...' : 'Issue token'}
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ─── Confirm rotate / revoke ──────────────────────────────────────────

interface ConfirmRotateProps {
  tenantID: string
  token: IngestToken
  onClose: () => void
  onRotated: (issued: IssuedToken) => void
}

function ConfirmRotateModal({ tenantID, token, onClose, onRotated }: ConfirmRotateProps) {
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleConfirm() {
    setError(null)
    setSubmitting(true)
    try {
      const issued = await api.rotateAgentToken(tenantID, token.id)
      onRotated(issued)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to rotate token')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Modal badge="Rotate token" title={token.label} onClose={onClose} size="sm">
      <div className="p-5 space-y-3">
        <p className="text-xs text-kb-text-tertiary">
          A new plaintext will be generated and shown once. The old token is
          revoked immediately — agents using it will fail to connect within
          the cache TTL (≤5 minutes).
        </p>
        {error && <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>}
        <div className="flex justify-end gap-2 pt-1">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors">Cancel</button>
          <button onClick={handleConfirm} disabled={submitting} className="px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-50 transition-colors">
            {submitting ? 'Rotating...' : 'Rotate'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

interface ConfirmRevokeProps {
  tenantID: string
  token: IngestToken
  onClose: () => void
  onRevoked: () => void
}

function ConfirmRevokeModal({ tenantID, token, onClose, onRevoked }: ConfirmRevokeProps) {
  const [confirmText, setConfirmText] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function handleConfirm() {
    setError(null)
    setSubmitting(true)
    try {
      await api.revokeAgentToken(tenantID, token.id)
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
          Agents currently using this token will fail to authenticate within the
          cache TTL (≤5 minutes). This cannot be undone.
        </p>
        {error && <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{error}</div>}
        <input
          value={confirmText}
          onChange={e => setConfirmText(e.target.value)}
          placeholder={token.label}
          autoFocus
          className="w-full px-3 py-1.5 text-sm font-mono bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary placeholder-kb-text-tertiary focus:outline-none focus:border-status-error transition-colors"
        />
        <div className="flex justify-end gap-2 pt-1">
          <button type="button" onClick={onClose} className="px-3 py-1.5 text-xs text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors">Cancel</button>
          <button
            onClick={handleConfirm}
            disabled={confirmText !== token.label || submitting}
            className="px-3 py-1.5 text-xs font-medium text-white bg-status-error rounded-lg hover:bg-status-error/90 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            {submitting ? 'Revoking...' : 'Revoke token'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────

export function AgentTokensPage() {
  const queryClient = useQueryClient()
  const [issuing, setIssuing] = useState(false)
  const [rotating, setRotating] = useState<IngestToken | null>(null)
  const [revoking, setRevoking] = useState<IngestToken | null>(null)
  const [revealedToken, setRevealedToken] = useState<{ issued: IssuedToken; title: string } | null>(null)

  const { data: tenants, isLoading: tenantsLoading, error: tenantsError } = useQuery({
    queryKey: ['admin-tenants'],
    queryFn: api.listTenants,
  })
  const defaultTenant = tenants?.find(t => t.name === DEFAULT_TENANT_NAME)

  const { data: tokens, isLoading: tokensLoading, error: tokensError } = useQuery({
    queryKey: ['admin-tokens', defaultTenant?.id],
    queryFn: () => api.listAgentTokens(defaultTenant!.id),
    enabled: !!defaultTenant,
  })

  function invalidate() {
    queryClient.invalidateQueries({ queryKey: ['admin-tokens', defaultTenant?.id] })
    queryClient.invalidateQueries({ queryKey: ['admin-tenants'] })
  }

  if (tenantsLoading || tokensLoading) {
    return <div className="flex items-center justify-center h-64"><LoadingSpinner size="lg" /></div>
  }
  if (tenantsError) {
    return <ErrorState message={tenantsError instanceof Error ? tenantsError.message : 'Failed to load tenants'} onRetry={invalidate} />
  }
  if (!defaultTenant) {
    return <ErrorState message='Default tenant not found. The backend should auto-seed it on first boot.' onRetry={invalidate} />
  }
  if (tokensError) {
    return <ErrorState message={tokensError instanceof Error ? tokensError.message : 'Failed to load tokens'} onRetry={invalidate} />
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <KeyRound className="w-5 h-5" />
            Agent ingest tokens
          </h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5">
            Long-lived bearer tokens used by kubebolt-agent to authenticate to this backend.
            Issued tokens are returned in plaintext exactly once — store them in the agent's Secret immediately.
          </p>
        </div>
        <button
          onClick={() => setIssuing(true)}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 transition-colors"
        >
          <Plus className="w-3.5 h-3.5" />
          Issue token
        </button>
      </div>

      <div className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
        <table className="w-full">
          <thead>
            <tr className="border-b border-kb-border">
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Label</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Prefix</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Status</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Created</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Last used</th>
              <th className="px-4 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Expires</th>
              <th className="px-4 py-2.5 text-right text-[10px] font-mono font-medium uppercase tracking-wider text-kb-text-tertiary">Actions</th>
            </tr>
          </thead>
          <tbody>
            {tokens?.map(tok => (
              <tr key={tok.id} className="border-b border-kb-border last:border-b-0 hover:bg-kb-card-hover transition-colors">
                <td className="px-4 py-2.5 text-xs font-medium text-kb-text-primary">{tok.label}</td>
                <td className="px-4 py-2.5 text-xs font-mono text-kb-text-secondary">{tok.prefix}…</td>
                <td className="px-4 py-2.5"><StatusBadge token={tok} /></td>
                <td className="px-4 py-2.5 text-xs text-kb-text-tertiary font-mono" title={formatAbsolute(tok.createdAt)}>{formatRelative(tok.createdAt)}</td>
                <td className="px-4 py-2.5 text-xs text-kb-text-tertiary font-mono" title={formatAbsolute(tok.lastUsedAt)}>{formatRelative(tok.lastUsedAt)}</td>
                <td className="px-4 py-2.5 text-xs text-kb-text-tertiary font-mono" title={tok.expiresAt ? formatAbsolute(tok.expiresAt) : ''}>{tok.expiresAt ? formatRelative(tok.expiresAt) : 'never'}</td>
                <td className="px-4 py-2.5">
                  <div className="flex items-center justify-end gap-1">
                    <button
                      onClick={() => setRotating(tok)}
                      disabled={!canMutate(tok)}
                      className="p-1.5 rounded-md text-kb-text-tertiary hover:text-kb-text-primary hover:bg-kb-elevated disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                      title="Rotate token"
                    >
                      <RefreshCw className="w-3.5 h-3.5" />
                    </button>
                    <button
                      onClick={() => setRevoking(tok)}
                      disabled={!canMutate(tok)}
                      className="p-1.5 rounded-md text-kb-text-tertiary hover:text-status-error hover:bg-status-error-dim disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                      title="Revoke token"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                    </button>
                  </div>
                </td>
              </tr>
            ))}
            {tokens?.length === 0 && (
              <tr>
                <td colSpan={7} className="px-4 py-8 text-center text-xs text-kb-text-tertiary">
                  No tokens issued yet. Click "Issue token" to create one for your agent fleet.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>

      {issuing && (
        <IssueTokenModal
          tenantID={defaultTenant.id}
          onClose={() => setIssuing(false)}
          onIssued={(issued) => {
            setIssuing(false)
            setRevealedToken({ issued, title: `New token: ${issued.info.label}` })
            invalidate()
          }}
        />
      )}
      {rotating && (
        <ConfirmRotateModal
          tenantID={defaultTenant.id}
          token={rotating}
          onClose={() => setRotating(null)}
          onRotated={(issued) => {
            setRotating(null)
            setRevealedToken({ issued, title: `Rotated: ${issued.info.label}` })
            invalidate()
          }}
        />
      )}
      {revoking && (
        <ConfirmRevokeModal
          tenantID={defaultTenant.id}
          token={revoking}
          onClose={() => setRevoking(null)}
          onRevoked={invalidate}
        />
      )}
      {revealedToken && (
        <RevealTokenModal
          issued={revealedToken.issued}
          title={revealedToken.title}
          onClose={() => setRevealedToken(null)}
        />
      )}
    </div>
  )
}
