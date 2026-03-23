import { useState, useEffect } from 'react'

interface DataFreshnessIndicatorProps {
  dataUpdatedAt: number
  refreshInterval: number
  isFetching: boolean
}

export function DataFreshnessIndicator({
  dataUpdatedAt,
  refreshInterval,
  isFetching,
}: DataFreshnessIndicatorProps) {
  const [now, setNow] = useState(Date.now())

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  const elapsed = dataUpdatedAt ? Math.floor((now - dataUpdatedAt) / 1000) : 0
  const refreshSec = Math.round(refreshInterval / 1000)
  const ratio = elapsed / refreshSec

  // fresh (<50% of interval), aging (50-100%), stale (>100%)
  const statusColor =
    ratio < 0.5
      ? 'bg-emerald-400'
      : ratio < 1
        ? 'bg-amber-400'
        : 'bg-red-400'

  const label =
    elapsed < 5
      ? 'just now'
      : elapsed < 60
        ? `${elapsed}s ago`
        : `${Math.floor(elapsed / 60)}m ago`

  return (
    <div className="flex items-center gap-2 text-[10px] font-mono text-kb-text-tertiary">
      {isFetching ? (
        <span className="relative flex h-2 w-2">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-sky-400 opacity-75" />
          <span className="relative inline-flex rounded-full h-2 w-2 bg-sky-400" />
        </span>
      ) : (
        <span className="relative flex h-2 w-2">
          {ratio < 0.5 && (
            <span className={`animate-ping absolute inline-flex h-full w-full rounded-full ${statusColor} opacity-75`} />
          )}
          <span className={`relative inline-flex rounded-full h-2 w-2 ${statusColor}`} />
        </span>
      )}
      <span>
        {isFetching ? 'Updating…' : `Updated ${label}`}
      </span>
      <span className="text-[#3a3b4a]">·</span>
      <span>every {refreshSec}s</span>
    </div>
  )
}
