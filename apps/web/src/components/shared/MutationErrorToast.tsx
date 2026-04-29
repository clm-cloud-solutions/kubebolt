import { useEffect } from 'react'
import { Link } from 'react-router-dom'
import { AlertTriangle, X, Settings } from 'lucide-react'
import { ApiError } from '@/services/api'

// MutationErrorToast renders a fixed-position banner explaining why
// a cluster-mutation action (restart / scale / delete / YAML apply)
// failed, with a 1-click fix when the cause is the agent being in
// reader-only mode. Replaces the bare `alert()` calls that used to
// surface raw apiserver text in a native browser modal.
//
// Two failure modes are handled specially:
//   - Agent SA forbidden (mode=metrics or reader, write attempt) →
//     guidance to open Configure for that agent and switch to
//     operator. Backend tags the 403 payload with
//     `agentRbacForbidden:true` for this discrimination.
//   - User role forbidden (Viewer trying to mutate) → "ask an Editor
//     or Admin". Same structure as the existing ActionProposalCard
//     translation.
//
// Anything else falls through to the raw error message — better
// than alert() because it's dismissable and stays in the page.

interface Props {
  // The error thrown by the mutation. ApiError is preferred (carries
  // status + payload); plain Error / string fall back to a generic
  // banner.
  error: unknown
  // What the user was trying to do, e.g. "Restart" / "Scale" /
  // "Delete". Surfaces in the banner heading.
  action: string
  onDismiss: () => void
}

export function MutationErrorToast({ error, action, onDismiss }: Props) {
  // Auto-dismiss every variant after the same 6s window — long
  // enough to read the body + CTA, short enough not to linger. The
  // X stays for explicit early dismiss; the CTA action also closes
  // the toast on click.
  const variant = classifyMutationError(error)
  useEffect(() => {
    const t = setTimeout(onDismiss, 6_000)
    return () => clearTimeout(t)
  }, [onDismiss])

  return (
    <div className="fixed bottom-6 right-6 z-[300] max-w-md w-[420px] bg-kb-card border border-status-error/40 rounded-lg shadow-2xl">
      <div className="flex items-start gap-3 p-4">
        <AlertTriangle className="w-5 h-5 text-status-error shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-semibold text-kb-text-primary">{variant.title || `${action} failed`}</div>
          <div className="text-xs text-kb-text-secondary mt-1 leading-relaxed">{variant.body}</div>
          {variant.cta && (
            <Link
              to={variant.cta.to}
              onClick={onDismiss}
              className="inline-flex items-center gap-1.5 mt-3 px-3 py-1.5 rounded-lg bg-kb-accent text-kb-on-accent text-xs font-medium hover:bg-kb-accent-hover transition-colors"
            >
              <Settings className="w-3.5 h-3.5" />
              {variant.cta.label}
            </Link>
          )}
          {variant.detail && (
            <details className="mt-2">
              <summary className="text-[10px] font-mono text-kb-text-tertiary cursor-pointer hover:text-kb-text-secondary">
                Server error
              </summary>
              <pre className="mt-1 text-[10px] font-mono text-kb-text-tertiary whitespace-pre-wrap break-all">
                {variant.detail}
              </pre>
            </details>
          )}
        </div>
        <button
          type="button"
          onClick={onDismiss}
          className="text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
          aria-label="Dismiss"
        >
          <X className="w-4 h-4" />
        </button>
      </div>
    </div>
  )
}

export interface MutationErrorVariant {
  kind: 'agent-rbac-forbidden' | 'user-role-forbidden' | 'generic'
  title?: string
  body: string
  cta?: { to: string; label: string }
  detail?: string
}

// classifyMutationError is exported so other surfaces (e.g. the
// delete-resource modal) can render the same translated body in
// their inline error banner without depending on the toast's
// fixed-position chrome.
export function classifyMutationError(error: unknown): MutationErrorVariant {
  if (error instanceof ApiError) {
    const payload = error.payload
    // Agent SA can't perform the verb — most common failure when an
    // operator installs the agent in reader mode and then tries to
    // mutate via the dashboard.
    if (error.status === 403 && payload?.agentRbacForbidden === true) {
      const verb = typeof payload.verb === 'string' ? payload.verb : 'modify'
      const resource = typeof payload.resource === 'string' ? payload.resource : 'this resource'
      return {
        kind: 'agent-rbac-forbidden',
        title: 'Agent is in read-only mode',
        body:
          `The agent's ServiceAccount can't ${verb} ${resource}. To enable write operations through the agent, ` +
          `open the KubeBolt Agent integration and switch the Permission tier to "Cluster-wide read + write".`,
        cta: { to: '/admin/integrations', label: 'Open Integrations' },
        detail: typeof error.message === 'string' ? error.message : undefined,
      }
    }
    if (error.status === 403) {
      return {
        kind: 'user-role-forbidden',
        title: 'Permission denied',
        body:
          'Your KubeBolt role does not allow this action. Ask an Editor or Admin to perform it, or have an Admin upgrade your role.',
        detail: typeof error.message === 'string' ? error.message : undefined,
      }
    }
  }
  const msg = error instanceof Error ? error.message : typeof error === 'string' ? error : 'Unknown error'
  return { kind: 'generic', body: msg }
}
