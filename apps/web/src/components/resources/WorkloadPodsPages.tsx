import { useEffect, useMemo, useState } from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import type { ResourceItem } from '@/types/kubernetes'

// Rows per page in a workload's Pods tab, the same as the resource lists.
// Every row draws a restart sparkline that runs its own metrics query every
// minute, so a DaemonSet on hundreds of nodes ran hundreds of them; a page
// caps that at the rows on screen.
export const PODS_PAGE_SIZE = 50

const byName = new Intl.Collator(undefined, { numeric: true })

// usePodPages sorts a workload's pods by name and pages them. The API returns
// them in no stable order, so without the sort the rows, and the pages, would
// reshuffle on every refresh. Natural order keeps a StatefulSet's db-2 before
// db-10. When a refresh shrinks the list below the current page, the page
// moves back to the last one.
export function usePodPages(pods: ResourceItem[]) {
  const [page, setPage] = useState(1)
  const sorted = useMemo(
    () => [...pods].sort((a, b) => byName.compare(String(a.name ?? ''), String(b.name ?? ''))),
    [pods],
  )
  const total = sorted.length
  const totalPages = Math.max(1, Math.ceil(total / PODS_PAGE_SIZE))
  useEffect(() => {
    if (page > totalPages) setPage(totalPages)
  }, [page, totalPages])
  const current = Math.min(page, totalPages)
  const pageItems = sorted.slice((current - 1) * PODS_PAGE_SIZE, current * PODS_PAGE_SIZE)
  return { page: current, setPage, totalPages, total, pageItems }
}

// PodsPager is the resource lists' footer, shown only when there is more than
// one page.
export function PodsPager({ page, totalPages, total, setPage }: {
  page: number
  totalPages: number
  total: number
  setPage: (update: (p: number) => number) => void
}) {
  if (totalPages <= 1) return null
  return (
    <div className="flex items-center justify-center gap-4 mt-3 px-1">
      <span className="text-[11px] font-mono text-kb-text-tertiary">
        {(page - 1) * PODS_PAGE_SIZE + 1}–{Math.min(page * PODS_PAGE_SIZE, total)} of {total}
      </span>
      <div className="flex items-center gap-1">
        <button
          type="button"
          title="Previous page"
          onClick={() => setPage(p => Math.max(1, p - 1))}
          disabled={page === 1}
          className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
        >
          <ChevronLeft className="w-3.5 h-3.5" />
        </button>
        <span className="text-[11px] font-mono text-kb-text-secondary px-2">
          {page} / {totalPages}
        </span>
        <button
          type="button"
          title="Next page"
          onClick={() => setPage(p => Math.min(totalPages, p + 1))}
          disabled={page === totalPages}
          className="p-1 rounded border border-kb-border text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active disabled:opacity-30 disabled:cursor-not-allowed transition-colors"
        >
          <ChevronRight className="w-3.5 h-3.5" />
        </button>
      </div>
    </div>
  )
}
