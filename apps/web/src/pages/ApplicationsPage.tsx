import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Package, AlertTriangle, Loader2 } from 'lucide-react'
import { api } from '@/services/api'
import type { HelmRelease } from '@/types/kubernetes'

// ApplicationsPage lists Helm releases (read-only, Sprint 4). Conforms to the
// shared resource-list design system (full width, bg-kb-card table,
// kb-card-hover rows) while keeping the icon+tinted title — a candidate to
// replicate to the other list pages (pending design approval).
export function ApplicationsPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ['helm-releases'],
    queryFn: () => api.listHelmReleases(),
    refetchInterval: 30_000,
  })

  const releases = data?.items ?? []

  return (
    <div>
      <div className="flex items-center gap-2 mb-1">
        <Package className="w-5 h-5 text-kb-accent" />
        <h1 className="text-lg font-semibold text-kb-text-primary">Applications</h1>
        {!isLoading && !error && (
          <span className="text-[10px] font-mono px-2.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary">
            {releases.length} total
          </span>
        )}
      </div>
      <p className="text-xs text-kb-text-secondary mb-4">
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
          <span>
            Could not load Helm releases. The connected ServiceAccount may not be permitted to read
            release Secrets.
          </span>
        </div>
      )}

      {!isLoading && !error && (
        <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
          {releases.length === 0 ? (
            <div className="text-sm text-kb-text-tertiary text-center py-12">
              No Helm releases found in this cluster.
            </div>
          ) : (
            <table className="w-full">
              <thead>
                <tr className="border-b border-kb-border">
                  {['Name', 'Namespace', 'Chart', 'Version', 'Rev', 'Status', 'Updated'].map((h) => (
                    <th
                      key={h}
                      className="px-3 py-2.5 text-left text-[10px] font-mono font-medium uppercase tracking-[0.08em] text-kb-text-secondary"
                    >
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {releases.map((r) => (
                  <ReleaseRow key={`${r.namespace}/${r.name}`} release={r} />
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  )
}

function ReleaseRow({ release: r }: { release: HelmRelease }) {
  return (
    <tr className="border-b border-kb-border last:border-0 hover:bg-kb-card-hover transition-colors">
      <td className="px-3 py-2.5 text-xs">
        <Link
          to={`/applications/${encodeURIComponent(r.namespace)}/${encodeURIComponent(r.name)}`}
          className="text-status-info hover:underline font-medium"
        >
          {r.name}
        </Link>
      </td>
      <td className="px-3 py-2.5 text-xs font-mono text-kb-text-secondary">{r.namespace}</td>
      <td className="px-3 py-2.5 text-xs text-kb-text-secondary">{r.chart}</td>
      <td className="px-3 py-2.5 text-xs font-mono text-kb-text-secondary">
        {r.chartVersion}
        {r.appVersion ? <span className="text-kb-text-tertiary"> · app {r.appVersion}</span> : null}
      </td>
      <td className="px-3 py-2.5 text-xs font-mono text-kb-text-secondary">{r.revision}</td>
      <td className="px-3 py-2.5 text-xs">
        <HelmStatusBadge status={r.status} />
      </td>
      <td className="px-3 py-2.5 text-xs text-kb-text-tertiary">
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
        : s.startsWith('pending')
          ? 'text-status-warn bg-status-warn-dim'
          : 'text-kb-text-secondary bg-kb-elevated'
  return (
    <span className={`text-[10px] font-mono uppercase tracking-wider px-1.5 py-0.5 rounded ${cls}`}>
      {status}
    </span>
  )
}
