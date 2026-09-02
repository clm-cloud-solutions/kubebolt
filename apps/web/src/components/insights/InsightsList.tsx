import { useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { useInsights } from '@/hooks/useInsights'
import { api } from '@/services/api'
import { InsightCard } from './InsightCard'
import { buildMuteIndex, partitionByMutes } from './mutes'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { EmptyState } from '@/components/shared/EmptyState'
import { Lightbulb, BellOff, ChevronLeft, ChevronRight } from 'lucide-react'
import { EpisodeHistory } from './EpisodeHistory'
import { ResourceTypeIcon, resourceTypeDescription } from '@/utils/resourceIcons'

type SeverityFilter = '' | 'critical' | 'warning' | 'info'

// Insights can pile up on a large or unhealthy cluster — paginate the list
// client-side so it never becomes an endless scroll. The fetch already
// returns the (bounded, severity-sorted) active set, so we slice locally.
// Default 10 (cards are tall, so a small page keeps it scannable); the
// operator can raise it via the Per-page selector (persisted in localStorage).
const PAGE_SIZE_OPTIONS = [10, 25, 50, 100]
const DEFAULT_PAGE_SIZE = 10
const PAGE_SIZE_KEY = 'kb-insights-page-size'

export function InsightsList() {
  // Severity filter lives in the URL so dashboard deep links
  // (/insights?severity=warning from the KPI legend rows) land
  // pre-filtered and the view is bookmarkable. Unknown values fall
  // back to '' (All) instead of silently filtering by garbage.
  const [searchParams, setSearchParams] = useSearchParams()
  const rawSeverity = searchParams.get('severity') ?? ''
  const severity: SeverityFilter =
    rawSeverity === 'critical' || rawSeverity === 'warning' || rawSeverity === 'info'
      ? rawSeverity
      : ''
  // Fase 2: the same page serves Active and History — a segment, not a new
  // menu item (v2.1 §6). History lives in the URL for deep links.
  const view = searchParams.get('view') === 'history' ? 'history' : 'active'
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState<number>(() => {
    const stored = Number(localStorage.getItem(PAGE_SIZE_KEY))
    return PAGE_SIZE_OPTIONS.includes(stored) ? stored : DEFAULT_PAGE_SIZE
  })
  // includeMuted: this page builds the «N muted» counter + reveal, so it
  // needs the full set — every OTHER consumer gets the server-filtered view.
  const { data, isLoading, error, refetch } = useInsights(
    severity ? { severity, includeMuted: 'true' } : { includeMuted: 'true' },
  )

  // #54 overlay: the current cluster's mutes. A 503 (no store) or any error
  // degrades to "no mutes" — the overlay must never break the list.
  const [showMuted, setShowMuted] = useState(false)
  const { data: muteData } = useQuery({
    queryKey: ['insight-mutes'],
    queryFn: () => api.getInsightMutes(),
    refetchInterval: 60_000,
    retry: false,
  })

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const muteIndex = buildMuteIndex(muteData?.mutes ?? [])
  const { visible, hidden } = partitionByMutes(data?.items || [], muteIndex)
  // The default view hides muted (non-pierced) insights; the header counter
  // reveals them in place, marked and dimmed, never pretending they resolved.
  const rows = showMuted ? [...visible, ...hidden] : visible
  // Pantalla 4 (#44): what the environment profile is hiding, counted per
  // rule — the silence is a visible, reversible decision, never an omission.
  const hiddenProfile = Object.entries(data?.hiddenByProfile ?? {}).sort((a, b) => b[1] - a[1])
  const hiddenProfileTotal = hiddenProfile.reduce((n, [, c]) => n + c, 0)
  const profileName = data?.profile || 'environment'
  // Rendered in BOTH branches — with zero visible insights the banner is
  // MORE important, not less (in-vivo 01-sep: an empty list said «everything
  // looks healthy» while 16 findings sat hidden by the profile).
  const profileBanner = hiddenProfileTotal > 0 && (
    <div className="mb-3 flex items-center gap-2.5 flex-wrap px-3.5 py-2.5 rounded-lg bg-status-info-dim border border-status-info/25 text-[13px] text-kb-text-secondary">
      <span>
        <b className="text-kb-text-primary">
          {hiddenProfileTotal.toLocaleString()} finding{hiddenProfileTotal === 1 ? '' : 's'} hidden
        </b>{' '}
        by the <b className="text-kb-text-primary">{profileName}</b> profile —{' '}
        {hiddenProfile.map(([rule, n], i) => (
          <span key={rule}>
            {i > 0 && ', '}
            {n} <code className="font-mono text-[0.85em]">{rule}</code>
          </span>
        ))}
        .
      </span>
      <span className="flex-1" />
      {/* The link speaks the banner's own hue (in-vivo 01-sep: the
          brand green clashed on the info-blue ground). */}
      <Link
        to="/admin/insights?tab=silenced"
        className="text-status-info font-semibold text-[12px] hover:underline shrink-0"
      >
        View silenced →
      </Link>
    </div>
  )
  const totalPages = Math.max(1, Math.ceil(rows.length / pageSize))
  // Clamp on render so a refetch that shrinks the list (insights resolving)
  // can't leave us stranded past the last page.
  const currentPage = Math.min(page, totalPages)
  const start = (currentPage - 1) * pageSize
  const pageItems = rows.slice(start, start + pageSize)

  // Changing the severity filter resets to page 1.
  function selectSeverity(v: SeverityFilter) {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (v) next.set('severity', v)
        else next.delete('severity')
        return next
      },
      { replace: true },
    )
    setPage(1)
  }

  // Per-page change: persist + jump back to page 1.
  function changePageSize(n: number) {
    setPageSize(n)
    setPage(1)
    localStorage.setItem(PAGE_SIZE_KEY, String(n))
  }

  const filters: { label: string; value: SeverityFilter }[] = [
    { label: 'All', value: '' },
    { label: 'Critical', value: 'critical' },
    { label: 'Warning', value: 'warning' },
    { label: 'Info', value: 'info' },
  ]

  return (
    <div>
      <div className="mb-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <ResourceTypeIcon type="insights" />
            <h1 className="text-lg font-semibold text-kb-text-primary">Insights</h1>
            <div className="ml-2 flex rounded-lg border border-kb-border overflow-hidden text-[11px] font-mono">
              {(['active', 'history'] as const).map((v) => (
                <button
                  key={v}
                  onClick={() =>
                    setSearchParams((prev) => {
                      const next = new URLSearchParams(prev)
                      if (v === 'history') next.set('view', 'history')
                      else next.delete('view')
                      return next
                    }, { replace: true })
                  }
                  className={`px-3 py-1 ${view === v ? 'bg-kb-elevated text-kb-text-primary font-semibold' : 'text-kb-text-secondary hover:text-kb-text-primary'}`}
                >
                  {v === 'active' ? 'Active' : 'History'}
                </button>
              ))}
            </div>
          </div>
          <div className="flex items-center gap-3">
            {(view === 'history' || rows.length > PAGE_SIZE_OPTIONS[0]) && (
              <label className="flex items-center gap-1.5 text-[11px] font-mono text-kb-text-tertiary">
                Per page
                <select
                  value={pageSize}
                  onChange={(e) => changePageSize(Number(e.target.value))}
                  className="bg-kb-card border border-kb-border rounded px-1.5 py-0.5 text-kb-text-secondary text-[11px] font-mono focus:outline-none focus:border-kb-border-active"
                >
                  {PAGE_SIZE_OPTIONS.map((n) => (
                    <option key={n} value={n}>{n}</option>
                  ))}
                </select>
              </label>
            )}
            {/* #54 counter: the silence overlay is always COUNTED, never
                invisible. Toggles revealing the muted rows in place. */}
            {view === 'active' && (hidden.length > 0 || showMuted) && (
              <button
                type="button"
                onClick={() => { setShowMuted((s) => !s); setPage(1) }}
                className={`inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[10px] font-mono border transition-colors ${
                  showMuted
                    ? 'bg-kb-elevated text-kb-text-primary border-kb-border-active'
                    : 'bg-kb-card text-kb-text-secondary border-kb-border hover:border-kb-border-active'
                }`}
                title={showMuted ? 'Hide the silenced insights again' : 'Show the silenced insights in place'}
              >
                <BellOff className="w-3 h-3" />
                {hidden.length} muted
              </button>
            )}
            <div className="flex gap-1">
              {filters.map((f) => (
                <button
                  key={f.value}
                  onClick={() => selectSeverity(f.value)}
                  className={`px-2.5 py-1 rounded-md text-[10px] font-mono uppercase tracking-[0.06em] border transition-colors ${
                    severity === f.value
                      ? 'bg-status-info-dim text-status-info border-status-info/20'
                      : 'bg-kb-card text-kb-text-secondary border-kb-border hover:border-kb-border-active'
                  }`}
                >
                  {f.label}
                </button>
              ))}
            </div>
          </div>
        </div>
        <p className="text-xs text-kb-text-secondary mt-1">{resourceTypeDescription('insights')}</p>
        {view === 'active' && (
          <p className="text-[11px] text-kb-text-tertiary mt-0.5">
            Showing conditions firing <span className="font-medium text-kb-text-secondary">right now</span> — everything
            that resolved or expired lives in{' '}
            <button
              onClick={() =>
                setSearchParams((prev) => {
                  const next = new URLSearchParams(prev)
                  next.set('view', 'history')
                  return next
                }, { replace: true })
              }
              className="underline underline-offset-2 hover:text-kb-text-primary"
            >
              History
            </button>
            .
          </p>
        )}
      </div>

      {view === 'history' ? (
        <EpisodeHistory severity={severity} pageSize={pageSize} />
      ) : rows.length === 0 ? (
        <>
          {profileBanner}
          {hidden.length > 0 ? (
            <EmptyState
              icon={<BellOff className="w-10 h-10" />}
              title="All remaining insights are silenced"
              message={`${hidden.length} silenced — use the counter above to see them`}
            />
          ) : (
            <EmptyState
              icon={<Lightbulb className="w-10 h-10" />}
              title="No active insights"
              // «Everything looks healthy» would be a lie the banner above
              // contradicts — the honest empty copy names the boundary.
              message={
                hiddenProfileTotal > 0
                  ? `Nothing firing outside the ${profileName} profile`
                  : 'Everything looks healthy'
              }
            />
          )}
        </>
      ) : (
        <>
          {profileBanner}
          <div className="space-y-3">
            {pageItems.map((row) => (
              <InsightCard key={row.insight.id} insight={row.insight} mute={row.mute} pierced={row.pierced} />
            ))}
          </div>
          {rows.length > PAGE_SIZE_OPTIONS[0] && (
            <div className="flex items-center justify-center gap-4 mt-4 px-1">
              <span className="text-[11px] font-mono text-kb-text-tertiary">
                {start + 1}–{Math.min(start + pageSize, rows.length)} of {rows.length}
              </span>
              <div className="flex items-center gap-1">
                <button
                  type="button"
                  title="Previous page"
                  onClick={() => setPage(p => Math.max(1, p - 1))}
                  disabled={currentPage === 1}
                  className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
                >
                  <ChevronLeft className="w-3.5 h-3.5" />
                </button>
                <span className="text-[11px] font-mono text-kb-text-secondary px-2">
                  {currentPage} / {totalPages}
                </span>
                <button
                  type="button"
                  title="Next page"
                  onClick={() => setPage(p => Math.min(totalPages, p + 1))}
                  disabled={currentPage === totalPages}
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
