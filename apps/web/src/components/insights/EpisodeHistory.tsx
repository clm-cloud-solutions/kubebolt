import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api, type InsightEpisode } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { EmptyState } from '@/components/shared/EmptyState'
import { History, ChevronLeft, ChevronRight } from 'lucide-react'

// EpisodeHistory — the Historial half of the Insights page (Fase 2, PR 2.3):
// episodes that OVERLAP the selected window, dead clusters included. This is
// the view the DIPRES morning-after needed: "what happened while I was away"
// as data (the narrative arrives with Fase 3's shift report).

const RANGES: { label: string; hours: number }[] = [
  { label: '24h', hours: 24 },
  { label: '7d', hours: 24 * 7 },
  { label: '30d', hours: 24 * 30 },
]

export function statusBadge(ep: InsightEpisode) {
  switch (ep.status) {
    case 'firing':
      // Blue, fixed for every active (Leafar's call, iterated in-vivo
      // 31-ago through red / gray / brand green / inverted / violet): calm,
      // "ongoing", and readable at the row's right edge. It shares the hue
      // with the info severity chip, but they no longer sit side by side —
      // severity leads the row on the left, state closes it on the right —
      // so each reads in its own context.
      return (
        <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-status-info-dim text-status-info text-[10px] font-mono font-semibold">
          <span className="w-1.5 h-1.5 rounded-full bg-status-info animate-pulse" aria-hidden />
          ACTIVE
        </span>
      )
    case 'resolved':
      return (
        <span className="px-2 py-0.5 rounded-full bg-status-ok-dim text-status-ok text-[10px] font-mono font-semibold">
          ✓ RESOLVED{ep.resolutionKind ? ` · ${ep.resolutionKind.toUpperCase()}` : ''}
        </span>
      )
    case 'expired':
      return <span className="px-2 py-0.5 rounded-full bg-kb-elevated text-kb-text-tertiary text-[10px] font-mono font-semibold" title="The condition stopped being verifiable — the cluster went silent. Not the same as resolved.">EXPIRED</span>
    default:
      return <span className="px-2 py-0.5 rounded-full bg-kb-elevated text-kb-text-tertiary text-[10px] font-mono font-semibold">{ep.status.toUpperCase()}</span>
  }
}

export function sevChip(sev: string) {
  const cls =
    sev === 'critical' ? 'bg-status-error-dim text-status-error'
    : sev === 'warning' ? 'bg-status-warn-dim text-status-warn'
    : 'bg-status-info-dim text-status-info'
  return <span className={`px-2 py-0.5 rounded-full text-[10px] font-mono font-semibold uppercase ${cls}`}>{sev}</span>
}

// advanceKnownTotal — progressive-count state machine: a short page pins the
// exact total; a full page reaching PAST a previously-pinned total means the
// data grew under us (refetch) and the figure is stale.
export function advanceKnownTotal(
  prev: number | null,
  itemsOnPage: number,
  page: number,
  pageSize: number,
): number | null {
  const end = (page - 1) * pageSize + itemsOnPage
  if (itemsOnPage < pageSize) return end
  return prev !== null && end > prev ? null : prev
}

// episodePagesLabel — the «1 / 2» half of the house pager. While the total
// is unknown a full page proves at least one more exists → «1 / 2+».
export function episodePagesLabel(page: number, pageSize: number, knownTotal: number | null): string {
  if (knownTotal !== null) return `${page} / ${Math.max(1, Math.ceil(knownTotal / pageSize))}`
  return `${page} / ${page + 1}+`
}

// episodeCountLabel — «51–100 of 131» once the edge was reached, «51–100 of
// 100+» while the total is still unknown.
export function episodeCountLabel(
  itemsOnPage: number,
  page: number,
  pageSize: number,
  knownTotal: number | null,
): string {
  if (itemsOnPage === 0) {
    return knownTotal !== null ? `${knownTotal.toLocaleString()} total` : `page ${page}`
  }
  const start = (page - 1) * pageSize + 1
  const end = (page - 1) * pageSize + itemsOnPage
  const range = `${start.toLocaleString()}–${end.toLocaleString()}`
  return knownTotal !== null
    ? `${range} of ${knownTotal.toLocaleString()}`
    : `${range} of ${end.toLocaleString()}+`
}

export function episodeDuration(ep: InsightEpisode): string {
  const start = new Date(ep.firstSeen).getTime()
  const end = ep.resolvedAt ? new Date(ep.resolvedAt).getTime() : new Date(ep.lastSeen).getTime()
  const mins = Math.max(0, Math.round((end - start) / 60000))
  if (mins < 60) return `${mins}m`
  const h = Math.floor(mins / 60)
  if (h < 48) return `${h}h ${mins % 60}m`
  return `${Math.floor(h / 24)}d ${h % 24}h`
}

interface EpisodeHistoryProps {
  // The header's severity pills and Per-page selector govern BOTH views
  // (in-vivo find 31-ago: they were decorative in History). Severity filters
  // on the episode's MAX severity — an episode that escalated to critical
  // belongs under Critical even if it opened as warning.
  severity: '' | 'critical' | 'warning' | 'info'
  pageSize: number
  // The Admin/Insights hub embeds this org-wide; the Insights page keeps the
  // cluster default.
  defaultScope?: 'cluster' | 'all'
}

export function EpisodeHistory({ severity, pageSize, defaultScope = 'cluster' }: EpisodeHistoryProps) {
  const [hours, setHours] = useState(24)
  // Scope: los episodios expired viven casi siempre en clusters MUERTOS que
  // ya no puedes seleccionar en el topbar (hallazgo in-vivo 31-ago: 131
  // expired, cero visibles) — «All clusters» es la única puerta hacia ellos.
  const [scope, setScope] = useState<'cluster' | 'all'>(defaultScope)
  const [status, setStatus] = useState('')
  const [page, setPage] = useState(1)
  // Progressive total (in-vivo find 31-ago: «page N» alone gives no sense of
  // volume). The server never COUNT(*)s — the total is DISCOVERED the moment
  // a short page arrives; until then the floor reads «100+». Mirrors the
  // Active view's «1–10 of 43» shape without paying for the count upfront.
  const [knownTotal, setKnownTotal] = useState<number | null>(null)
  useEffect(() => {
    setPage(1)
    setKnownTotal(null)
  }, [severity, hours, pageSize, scope, status])
  const since = new Date(Date.now() - hours * 3600_000).toISOString()
  // Todo server-side (hallazgo in-vivo: con fetch de 200 ordenado por
  // last_seen, los expired viejos quedaban ESTRUCTURALMENTE enterrados bajo
  // cientos de resolved recientes — «Any state» nunca los enseñaba).
  const { data, isLoading, error } = useQuery({
    queryKey: ['insight-episodes', hours, scope, status, severity, pageSize, page],
    queryFn: () =>
      api.getInsightEpisodes({
        since,
        status: status || undefined,
        severity: severity || undefined,
        cluster: scope === 'all' ? 'all' : undefined,
        limit: pageSize,
        page,
      }),
    refetchInterval: 60_000,
    retry: false,
  })

  const pageItems = data?.episodes ?? []
  const hasNext = pageItems.length === pageSize

  useEffect(() => {
    if (!data) return
    const n = data.episodes?.length ?? 0
    setKnownTotal((t) => advanceKnownTotal(t, n, page, pageSize))
  }, [data, page, pageSize])

  const countLabel = episodeCountLabel(pageItems.length, page, pageSize, knownTotal)

  // The pager renders OUTSIDE the list so an empty page>1 (you walked past
  // the edge) still offers the way back — before, the EmptyState said «go
  // back one» while hiding the only button that could. Same shape as the
  // Active view's pager: «1–50 of 81   ‹ 1 / 2 ›».
  const pager =
    pageItems.length > 0 || page > 1 ? (
      <div className="flex items-center justify-center gap-4 pt-2">
        <span className="text-[11px] font-mono text-kb-text-tertiary">{countLabel}</span>
        <div className="flex items-center gap-1">
          <button
            type="button"
            title="Previous page"
            onClick={() => setPage((p) => Math.max(1, p - 1))}
            disabled={page === 1}
            className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
          >
            <ChevronLeft className="w-3.5 h-3.5" />
          </button>
          <span className="text-[11px] font-mono text-kb-text-secondary px-2">
            {episodePagesLabel(page, pageSize, knownTotal)}
          </span>
          <button
            type="button"
            title="Next page"
            onClick={() => setPage((p) => p + 1)}
            disabled={!hasNext}
            className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
          >
            <ChevronRight className="w-3.5 h-3.5" />
          </button>
        </div>
      </div>
    ) : null

  return (
    <div>
      <div className="flex items-center gap-2 mb-3">
        <div className="flex gap-1">
          {RANGES.map((r) => (
            <button
              key={r.hours}
              onClick={() => setHours(r.hours)}
              className={`px-2.5 py-1 rounded-md text-[10px] font-mono border transition-colors ${
                hours === r.hours
                  ? 'bg-status-info-dim text-status-info border-status-info/20'
                  : 'bg-kb-card text-kb-text-secondary border-kb-border hover:border-kb-border-active'
              }`}
            >
              {r.label}
            </button>
          ))}
        </div>
        <div className="flex rounded-md border border-kb-border overflow-hidden text-[10px] font-mono">
          {(['cluster', 'all'] as const).map((v) => (
            <button
              key={v}
              onClick={() => setScope(v)}
              className={`px-2.5 py-1 ${scope === v ? 'bg-kb-elevated text-kb-text-primary font-semibold' : 'text-kb-text-secondary hover:text-kb-text-primary'}`}
            >
              {v === 'cluster' ? 'This cluster' : 'All clusters'}
            </button>
          ))}
        </div>
        <select
          value={status}
          onChange={(e) => setStatus(e.target.value)}
          className="bg-kb-card border border-kb-border rounded px-1.5 py-1 text-kb-text-secondary text-[10px] font-mono focus:outline-none focus:border-kb-border-active"
        >
          <option value="">Any state</option>
          <option value="firing">Firing</option>
          <option value="resolved">Resolved</option>
          <option value="expired">Expired</option>
        </select>
        <span className="text-[11px] text-kb-text-tertiary">
          episodes overlapping the window{scope === 'all' && ' — dead clusters included'}
        </span>
      </div>

      {isLoading ? (
        <LoadingSpinner />
      ) : error ? (
        <EmptyState icon={<History className="w-10 h-10" />} title="History unavailable" message="Episode history needs the KubeBolt database (Enterprise/Cloud)." />
      ) : pageItems.length === 0 ? (
        page > 1 ? (
          <div className="space-y-2">
            <EmptyState icon={<History className="w-10 h-10" />} title="End of the history" message="No more episodes on this page — go back one." />
            {pager}
          </div>
        ) : (
          <EmptyState icon={<History className="w-10 h-10" />} title="Nothing in this window" message="No insight episodes match the selected range and filters." />
        )
      ) : (
        <div className="space-y-2">
          {pageItems.map((ep) => (
            <Link
              key={ep.id}
              to={`/insights/episodes/${ep.id}?from=history`}
              className="block bg-kb-card border border-kb-border rounded-lg px-4 py-2.5 hover:border-kb-border-active transition-colors"
            >
              <div className="flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 flex-wrap">
                    {/* Severity alone leads the row, exactly like the Active
                        view — the two badges TOGETHER were the problem
                        (in-vivo 31-ago): side by side they compete for the
                        same glance. State lives on the right, with the
                        outcome metadata that is its natural family. */}
                    {sevChip(ep.maxSeverity || ep.severity)}
                    <span className="text-[13px] font-medium text-kb-text-primary min-w-0 truncate">
                      {ep.title || ep.ruleId}
                    </span>
                  </div>
                  <div className="mt-1 flex items-center gap-2 text-[11px] font-mono text-kb-text-secondary min-w-0">
                    <span className="truncate">{ep.resource}</span>
                    <span className="px-1.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary text-[10px] shrink-0">{ep.ruleId}</span>
                    {scope === 'all' && (
                      <span className="px-1.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary text-[10px] shrink-0" title={ep.clusterId}>
                        {ep.clusterName || `cluster ${ep.clusterId.slice(0, 8)}…`}
                      </span>
                    )}
                  </div>
                </div>
                <div className="shrink-0 text-right">
                  <div className="flex items-center justify-end gap-2">
                    {statusBadge(ep)}
                    <span className="text-[13px] font-mono font-semibold text-kb-text-primary">
                      {episodeDuration(ep)}
                      {ep.flapCount > 0 && <span className="text-status-warn text-[11px]"> · ×{ep.flapCount}</span>}
                    </span>
                  </div>
                  <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">
                    {new Date(ep.firstSeen).toLocaleString()}
                    {ep.resolvedAt && <> → {new Date(ep.resolvedAt).toLocaleTimeString()}</>}
                  </div>
                </div>
              </div>
            </Link>
          ))}
          {pager}
        </div>
      )}
    </div>
  )
}
