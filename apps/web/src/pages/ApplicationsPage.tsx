import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Package, AlertTriangle, Loader2, ChevronLeft, ChevronRight } from 'lucide-react'
import { api } from '@/services/api'
import { FilterBar } from '@/components/resources/FilterBar'
import type { HelmRelease } from '@/types/kubernetes'

const PAGE_SIZE = 50

// ApplicationsPage lists Helm releases (read-only, Sprint 4). Conforms to the
// shared resource-list design system (full width, bg-kb-card table,
// kb-card-hover rows) while keeping the icon+tinted title — a candidate to
// replicate to the other list pages (pending design approval).
export function ApplicationsPage() {
  // Scope the Helm-releases cache to the ACTIVE cluster: without the cluster in
  // the key, switching shows the PREVIOUS cluster's cached list while the live
  // (over-the-agent-proxy) refetch is in flight — stale-while-revalidate. Reuses
  // the already-cached ['clusters'] query, so this adds no extra request.
  const { data: clusters } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })
  const activeCluster = clusters?.find((c) => c.active)?.context ?? 'none'

  const { data, isLoading, error } = useQuery({
    queryKey: ['helm-releases', activeCluster],
    queryFn: () => api.listHelmReleases(),
    refetchInterval: 30_000,
  })

  const releases = data?.items ?? []

  const [namespace, setNamespace] = useState('')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)

  const namespaces = useMemo(
    () => Array.from(new Set(releases.map((r) => r.namespace).filter(Boolean))).sort(),
    [releases],
  )

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return releases.filter((r) => {
      if (namespace && r.namespace !== namespace) return false
      if (q && !`${r.name} ${r.chart}`.toLowerCase().includes(q)) return false
      return true
    })
  }, [releases, namespace, search])

  const total = filtered.length
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const pageClamped = Math.min(page, totalPages)
  const paged = filtered.slice((pageClamped - 1) * PAGE_SIZE, pageClamped * PAGE_SIZE)

  return (
    <div>
      <div className="flex items-center gap-2 mb-1">
        <Package className="w-5 h-5 text-kb-accent" />
        <h1 className="text-lg font-semibold text-kb-text-primary">Applications</h1>
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
        <>
          <FilterBar
            namespaces={namespaces}
            selectedNamespace={namespace}
            onNamespaceChange={(v) => {
              setNamespace(v)
              setPage(1)
            }}
            search={search}
            onSearchChange={(v) => {
              setSearch(v)
              setPage(1)
            }}
            total={total}
            resourceName="releases"
          />
          <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
            {total === 0 ? (
              <div className="text-sm text-kb-text-tertiary text-center py-12">
                {releases.length === 0
                  ? 'No Helm releases found in this cluster.'
                  : 'No releases match the current filter.'}
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
                  {paged.map((r) => (
                    <ReleaseRow key={`${r.namespace}/${r.name}`} release={r} />
                  ))}
                </tbody>
              </table>
            )}
          </div>
          {totalPages > 1 && (
            <div className="flex items-center justify-center gap-4 mt-3 px-1">
              <span className="text-[11px] font-mono text-kb-text-tertiary">
                {(pageClamped - 1) * PAGE_SIZE + 1}–{Math.min(pageClamped * PAGE_SIZE, total)} of {total}
              </span>
              <div className="flex items-center gap-1">
                <button
                  type="button"
                  title="Previous page"
                  onClick={() => setPage(Math.max(1, pageClamped - 1))}
                  disabled={pageClamped === 1}
                  className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                >
                  <ChevronLeft className="w-3.5 h-3.5" />
                </button>
                <span className="text-[11px] font-mono text-kb-text-secondary px-2">
                  {pageClamped} / {totalPages}
                </span>
                <button
                  type="button"
                  title="Next page"
                  onClick={() => setPage(Math.min(totalPages, pageClamped + 1))}
                  disabled={pageClamped === totalPages}
                  className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                >
                  <ChevronRight className="w-3.5 h-3.5" />
                </button>
              </div>
            </div>
          )}
        </>
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
