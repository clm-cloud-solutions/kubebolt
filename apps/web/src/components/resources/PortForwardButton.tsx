import { useState, useEffect } from 'react'
import { ExternalLink, X, Loader2, Cable } from 'lucide-react'
import { api } from '@/services/api'

interface PortForwardButtonProps {
  namespace: string
  pod: string
  container?: string
  remotePort: number
  disabled?: boolean
}

interface ActiveForward {
  id: string
  url: string
}

export function PortForwardButton({ namespace, pod, container, remotePort, disabled }: PortForwardButtonProps) {
  const [forward, setForward] = useState<ActiveForward | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Check if there's already an active forward for this pod:port on mount
  useEffect(() => {
    api.listPortForwards().then(forwards => {
      const existing = forwards.find(
        f => f.pod === pod && f.namespace === namespace && f.remotePort === remotePort && f.status === 'active'
      )
      if (existing) {
        // Link through the backend's /pf/{id} reverse proxy (same origin as
        // the dashboard) so the HTTP service is reachable even when the
        // backend is remote — not the direct host:port, which only works
        // when backend and browser share a machine.
        setForward({ id: existing.id, url: `/pf/${existing.id}/` })
      }
    }).catch(() => {})
  }, [namespace, pod, remotePort])

  async function startForward() {
    setLoading(true)
    setError(null)
    try {
      const result = await api.createPortForward({ namespace, pod, container, remotePort })
      setForward({ id: result.id, url: `/pf/${result.id}/` })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start port-forward')
    } finally {
      setLoading(false)
    }
  }

  async function stopForward() {
    if (!forward) return
    try {
      await api.deletePortForward(forward.id)
    } catch {}
    setForward(null)
  }

  if (loading) {
    return (
      <span className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-kb-elevated text-[10px] font-mono text-kb-text-tertiary">
        <Loader2 className="w-3 h-3 animate-spin" />
        {remotePort}
      </span>
    )
  }

  if (forward) {
    return (
      <span className="inline-flex items-center gap-1">
        <a
          href={forward.url}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-status-ok-dim text-[10px] font-mono text-status-ok border border-status-ok/20 hover:bg-status-ok/20 transition-colors"
        >
          <ExternalLink className="w-3 h-3" />
          {remotePort}
        </a>
        <button
          onClick={stopForward}
          className="inline-flex items-center p-0.5 rounded text-kb-text-tertiary hover:text-status-error hover:bg-status-error-dim transition-colors"
          title="Stop port-forward"
        >
          <X className="w-3 h-3" />
        </button>
      </span>
    )
  }

  return (
    <span className="inline-flex flex-col items-start">
      <button
        onClick={startForward}
        disabled={disabled}
        className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-kb-elevated text-[10px] font-mono text-kb-text-secondary border border-kb-border hover:border-kb-border-active hover:text-kb-text-primary transition-colors disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:border-kb-border disabled:hover:text-kb-text-secondary"
        title={disabled ? 'Editor role required' : `Forward port ${remotePort}`}
      >
        <ExternalLink className="w-3 h-3" />
        {remotePort}
      </button>
      {error && (
        <span className="text-[9px] font-mono text-status-error mt-0.5">{error}</span>
      )}
    </span>
  )
}

export function PortForwardNote() {
  return (
    <div className="inline-flex items-center gap-1.5 px-1.5 py-1 rounded border border-kb-border bg-kb-elevated text-kb-text-tertiary text-[9px] font-mono">
      <Cable className="w-3 h-3 shrink-0" />
      <span>Opens the forwarded HTTP(S) service inside the dashboard — works with remote backends. Raw TCP (e.g. databases) isn't supported here yet.</span>
    </div>
  )
}
