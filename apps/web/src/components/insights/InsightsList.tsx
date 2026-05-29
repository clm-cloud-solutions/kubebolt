import { useState } from 'react'
import { useInsights } from '@/hooks/useInsights'
import { InsightCard } from './InsightCard'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { EmptyState } from '@/components/shared/EmptyState'
import { Lightbulb, ChevronLeft, ChevronRight } from 'lucide-react'
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
  const [severity, setSeverity] = useState<SeverityFilter>('')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState<number>(() => {
    const stored = Number(localStorage.getItem(PAGE_SIZE_KEY))
    return PAGE_SIZE_OPTIONS.includes(stored) ? stored : DEFAULT_PAGE_SIZE
  })
  const { data, isLoading, error, refetch } = useInsights(severity ? { severity } : undefined)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const insights = data?.items || []
  const totalPages = Math.max(1, Math.ceil(insights.length / pageSize))
  // Clamp on render so a refetch that shrinks the list (insights resolving)
  // can't leave us stranded past the last page.
  const currentPage = Math.min(page, totalPages)
  const start = (currentPage - 1) * pageSize
  const pageItems = insights.slice(start, start + pageSize)

  // Changing the severity filter resets to page 1.
  function selectSeverity(v: SeverityFilter) {
    setSeverity(v)
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
          </div>
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
        <p className="text-xs text-kb-text-secondary mt-1">{resourceTypeDescription('insights')}</p>
      </div>

      {insights.length === 0 ? (
        <EmptyState
          icon={<Lightbulb className="w-10 h-10" />}
          title="No insights"
          message="Everything looks healthy"
        />
      ) : (
        <>
          <div className="space-y-3">
            {pageItems.map((insight) => (
              <InsightCard key={insight.id} insight={insight} />
            ))}
          </div>
          {insights.length > PAGE_SIZE_OPTIONS[0] && (
            <div className="flex items-center justify-center gap-4 mt-4 px-1">
              <span className="text-[11px] font-mono text-kb-text-tertiary">
                {start + 1}–{Math.min(start + pageSize, insights.length)} of {insights.length}
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
            </div>
          )}
        </>
      )}
    </div>
  )
}
