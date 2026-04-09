import { useState, useEffect, useRef } from 'react'
import { ChevronDown } from 'lucide-react'
import { useRefreshInterval } from '@/contexts/RefreshContext'

const INTERVAL_OPTIONS = [
  { value: 5_000, label: '5s' },
  { value: 10_000, label: '10s' },
  { value: 15_000, label: '15s' },
  { value: 30_000, label: '30s' },
  { value: 60_000, label: '1m' },
  { value: 120_000, label: '2m' },
] as const

interface DataFreshnessIndicatorProps {
  dataUpdatedAt: number
  refreshInterval?: number // optional — falls back to global context
  isFetching: boolean
}

export function DataFreshnessIndicator({
  dataUpdatedAt,
  refreshInterval,
  isFetching,
}: DataFreshnessIndicatorProps) {
  const [now, setNow] = useState(Date.now())
  const [open, setOpen] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)
  const { interval: globalInterval, setInterval: setGlobalInterval } = useRefreshInterval()

  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    if (open) document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  const activeInterval = refreshInterval ?? globalInterval
  const elapsed = dataUpdatedAt ? Math.floor((now - dataUpdatedAt) / 1000) : 0
  const refreshSec = Math.round(activeInterval / 1000)
  const ratio = elapsed / refreshSec

  const statusColor =
    ratio < 0.5
      ? 'bg-emerald-400'
      : ratio < 1
        ? 'bg-amber-400'
        : 'bg-red-400'

  const label =
    elapsed < 2
      ? 'just now'
      : elapsed < 60
        ? `${elapsed}s ago`
        : `${Math.floor(elapsed / 60)}m ago`

  const activeLabel = INTERVAL_OPTIONS.find(o => o.value === activeInterval)?.label ?? `${refreshSec}s`

  return (
    <div className="flex items-center gap-2 text-[10px] font-mono text-kb-text-tertiary">
      {isFetching ? (
        <span className="relative flex h-2 w-2">
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-kb-accent opacity-75" />
          <span className="relative inline-flex rounded-full h-2 w-2 bg-kb-accent" />
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
        {isFetching ? 'Updating...' : `Updated ${label}`}
      </span>
      <span className="text-kb-text-tertiary/50">·</span>
      <div className="relative" ref={dropdownRef}>
        <button
          onClick={() => setOpen(!open)}
          className="flex items-center gap-0.5 hover:text-kb-text-secondary transition-colors"
        >
          every {activeLabel}
          <ChevronDown className={`w-2.5 h-2.5 transition-transform ${open ? 'rotate-180' : ''}`} />
        </button>
        {open && (
          <div className="absolute right-0 top-full mt-1 bg-kb-card border border-kb-border rounded-md shadow-lg z-50 py-0.5 min-w-[80px]">
            {INTERVAL_OPTIONS.map((opt) => (
              <button
                key={opt.value}
                onClick={() => {
                  setGlobalInterval(opt.value as Parameters<typeof setGlobalInterval>[0])
                  setOpen(false)
                }}
                className={`w-full text-left px-3 py-1.5 text-[10px] font-mono transition-colors ${
                  activeInterval === opt.value
                    ? 'text-status-info bg-status-info-dim'
                    : 'text-kb-text-secondary hover:bg-kb-card-hover'
                }`}
              >
                {opt.label}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
