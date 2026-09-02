import { Link } from 'react-router-dom'
import { AlertOctagon } from 'lucide-react'
import { useCapabilities } from '@/hooks/useCapabilities'

// IngestTruncatedBanner — the point-of-use consequence of #50's worst case:
// under the HARD regime (Free), an org over its caps has data silently
// truncated at ingest, which changes what every panel on this page can even
// see. The state lives in Billing; the consequence lives HERE, so this banner
// does too — permanent until resolved, with the numbers, the concrete
// consequence, and the remedy one click away. Renders nothing when the org is
// within caps, on paid soft-cap plans (overage bills, nothing is dropped), or
// on installs without the capability registry.
export function IngestTruncatedBanner() {
  const { byId } = useCapabilities()
  const ingest = byId['ingest_caps']
  const series = byId['active_series']
  const truncated = ingest?.status === 'degraded'
  const capping = series?.status === 'degraded'
  if (!truncated && !capping) return null

  const active = truncated ? ingest : series
  const d = (active?.detail ?? {}) as Record<string, unknown>
  const n = (v: unknown) => (typeof v === 'number' ? v.toLocaleString() : String(v ?? '—'))

  const title = truncated ? 'Ingestion truncated by plan limits' : 'Metric series cap engaged'
  const figures = truncated
    ? `${n(d.nodes)} nodes / ${n(d.maxNodes)} allowed · ${n(d.pods)} pods / ${n(d.maxPods)}`
    : `${n(d.count)} active series against a ${n(d.max)} cap`
  const consequence = truncated
    ? 'What happens beyond the caps is not recorded and cannot be reconstructed later.'
    : 'New metric series are dropped at ingest — charts for new pods and workloads stay empty or incomplete until the cap is raised.'

  const since = active?.since ? new Date(active.since) : null
  const sinceLabel = since && !isNaN(since.getTime()) ? since.toLocaleString() : null

  return (
    <div className="rounded-lg border border-status-error/30 bg-status-error-dim/25 px-4 py-3 flex items-center gap-3">
      <AlertOctagon className="w-4 h-4 text-status-error shrink-0" />
      <div className="min-w-0">
        <div className="text-[12px]">
          <span className="font-medium text-status-error">{title}</span>
          <span className="text-kb-text-primary"> — {figures}.</span>
        </div>
        <div className="text-[11px] text-kb-text-secondary mt-0.5">
          {consequence}
          {sinceLabel && <span className="opacity-75"> · since {sinceLabel}</span>}
        </div>
      </div>
      <span className="flex-1" />
      <Link
        to="/admin/billing"
        className="shrink-0 px-2.5 py-1 rounded-md border border-status-error/40 text-[11px] font-medium text-status-error hover:bg-status-error-dim/40 transition-colors"
      >
        Review plan →
      </Link>
    </div>
  )
}
