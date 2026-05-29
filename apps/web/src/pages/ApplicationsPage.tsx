import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Package, AlertTriangle, Loader2 } from 'lucide-react'
import { api } from '@/services/api'
import type { HelmRelease } from '@/types/kubernetes'

// ApplicationsPage lists Helm releases (read-only, Sprint 4). Decoded from
// Helm's storage Secrets server-side; write actions + App Center are deferred
// (see internal/helm-applications-post-1.14.md).
export function ApplicationsPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['helm-releases'],
    queryFn: () => api.listHelmReleases(),
    refetchInterval: 30_000,
  })

  const releases = data?.items ?? []

  return (
    <div className="p-6 max-w-6xl mx-auto">
      <div className="flex items-center gap-2 mb-1">
        <Package className="w-5 h-5 text-kb-accent" />
        <h1 className="text-lg font-semibold text-kb-text-primary">Applications</h1>
      </div>
      <p className="text-xs text-kb-text-secondary mb-5">
        Helm releases installed in this cluster. Read-only view.
      </p>

      {isLoading && (
        <div className="flex items-center gap-2 text-sm text-kb-text-secondary">
          <Loader2 className="w-4 h-4 animate-spin" /> Loading releases…
        </div>
      )}

      {error && (
        <div className="flex items-start gap-2 text-sm text-status-error">
          <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
          <span>Could not load Helm releases. The connected ServiceAccount may not be permitted to read release Secrets.</span>
        </div>
      )}

      {!isLoading && !error && releases.length === 0 && (
        <div className="text-sm text-kb-text-secondary border border-kb-border rounded-lg px-4 py-8 text-center">
          No Helm releases found in this cluster.
        </div>
      )}

      {releases.length > 0 && (
        <div className="border border-kb-border rounded-lg overflow-hidden">
          <table className="w-full text-sm">
            <thead className="bg-kb-elevated text-kb-text-secondary text-[11px] uppercase tracking-wider">
              <tr>
                <th className="text-left font-medium px-3 py-2">Name</th>
                <th className="text-left font-medium px-3 py-2">Namespace</th>
                <th className="text-left font-medium px-3 py-2">Chart</th>
                <th className="text-left font-medium px-3 py-2">Version</th>
                <th className="text-left font-medium px-3 py-2">Rev</th>
                <th className="text-left font-medium px-3 py-2">Status</th>
                <th className="text-left font-medium px-3 py-2">Updated</th>
              </tr>
            </thead>
            <tbody>
              {releases.map((r) => (
                <ReleaseRow key={`${r.namespace}/${r.name}`} release={r} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function ReleaseRow({ release: r }: { release: HelmRelease }) {
  return (
    <tr className="border-t border-kb-border hover:bg-kb-elevated/50 transition-colors">
      <td className="px-3 py-2">
        <Link
          to={`/applications/${encodeURIComponent(r.namespace)}/${encodeURIComponent(r.name)}`}
          className="text-kb-accent hover:underline font-medium"
        >
          {r.name}
        </Link>
      </td>
      <td className="px-3 py-2 text-kb-text-secondary font-mono text-xs">{r.namespace}</td>
      <td className="px-3 py-2 text-kb-text-secondary">{r.chart}</td>
      <td className="px-3 py-2 text-kb-text-secondary font-mono text-xs">
        {r.chartVersion}
        {r.appVersion ? <span className="text-kb-text-tertiary"> · app {r.appVersion}</span> : null}
      </td>
      <td className="px-3 py-2 text-kb-text-secondary font-mono text-xs">{r.revision}</td>
      <td className="px-3 py-2">
        <HelmStatusBadge status={r.status} />
      </td>
      <td className="px-3 py-2 text-kb-text-tertiary text-xs">
        {r.updated ? new Date(r.updated).toLocaleString() : '—'}
      </td>
    </tr>
  )
}

export function HelmStatusBadge({ status }: { status: string }) {
  const s = status.toLowerCase()
  const cls =
    s === 'deployed'
      ? 'text-status-ok bg-status-ok-dim'
      : s === 'failed'
        ? 'text-status-error bg-status-error-dim'
        : s === 'pending-install' || s === 'pending-upgrade' || s === 'pending-rollback'
          ? 'text-status-warn bg-status-warn-dim'
          : 'text-kb-text-secondary bg-kb-elevated'
  return (
    <span className={`text-[10px] font-mono uppercase tracking-wider px-1.5 py-0.5 rounded ${cls}`}>
      {status}
    </span>
  )
}
