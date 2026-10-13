import { useState, type ReactNode } from 'react'
import { RefreshCw } from 'lucide-react'
import { useIsFetching, useQuery, useQueryClient, type Query } from '@tanstack/react-query'
import { api } from '@/services/api'
import { barStep } from '@/components/shared/MetricChart'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { ScopedRefreshProvider, useRefreshInterval } from '@/contexts/RefreshContext'
import { OVERVIEW_RANGE_OPTIONS, RangeSelector } from '@/components/shared/RangeSelector'

// Shared by the Health tabs of Administration (AI › Health and System ›
// Health): the frame — range, refresh, freshness —, the query builders, the
// colours with a meaning and the query hooks. Every query reads KubeBolt's own
// series through /admin/metrics, which pins no cluster.

// The bar charts show what happened in each interval of the chart — one bar's
// interval, which widens with the range (barStep in MetricChart) — never a
// rate carried over a window: these events come in bursts, and nothing
// happening means nothing drawn. increase_pure over [1i]: the API's counters
// are born at zero and pushed every 30 s, so a new series' first sample
// already carries what happened — rate() never counts it and increase() drops
// it when it looks large. `> 0` leaves the idle intervals empty.
export const inInterval = (selector: string, by = '') => `sum${by ? ` by (${by})` : ''} (increase_pure(${selector}[1i]))`
export const KOBI_TURNS = inInterval('kubebolt_kobi_copilot_sessions_total')

// What went wrong per interval, plus a 0 (label="none") in the intervals where
// Kobi was used and nothing went wrong: «used, nothing failed» reads
// differently from «nobody used it», which stays empty. `failures` overrides
// the grouped expression when it is not a plain sum over one selector.
export function failuresOrZero(
  selector: string,
  by: string,
  label: string,
  used = `${KOBI_TURNS} > 0`,
  failures = inInterval(selector, by),
): string {
  return `(${failures} > 0) or (label_set(${used}, "${label}", "none") * 0 unless on() (${inInterval(selector)} > 0))`
}

// The interval one bar (or one point) covers, for the footnotes: "2m" → "per
// 2-minute interval".
export function intervalLabel(step: string): string {
  const m = /^(\d+)([smhd])$/.exec(step)
  if (!m) return 'per interval'
  const unit = { s: 'second', m: 'minute', h: 'hour', d: 'day' }[m[2] as 's' | 'm' | 'h' | 'd']
  return `per ${m[1]}-${unit} interval`
}

export type Row = { labels: Record<string, string>; value: number }

// Colours with a meaning: green for what went well, amber for what degraded,
// red for what failed, grey for what nobody needs to act on.
export const OUTCOME_COLOR: Record<string, string> = { done: '#22c55e', max_rounds: '#f59e0b', error: '#ef4056', canceled: '#94a3b8' }
// FAILURE_PALETTE: one colour per series in the charts that split failures
// two ways (source × result, caller × result): a colour per dimension would
// paint alike several sources failing the same way. No gray (that is «none»)
// and no green (that is ok or rescued).
export const FAILURE_PALETTE = ['#ef4056', '#f59e0b', '#a855f7', '#3b82f6', '#ec4899', '#f97316', '#06b6d4', '#eab308', '#14b8a6', '#8b5cf6']
export const TOKEN_COLOR: Record<string, string> = {
  input: '#3b82f6',
  output: '#22c55e',
  thinking: '#a855f7',
  cache_read: '#94a3b8',
  cache_write_5m: '#f59e0b',
  cache_write_1h: '#f97316',
}

const HEALTH_KEY = 'admin-health'

export function useHealthVector(key: string, query: string) {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: [HEALTH_KEY, key, query],
    queryFn: () => api.adminQueryMetrics({ query }),
    refetchInterval: interval,
    select: (r): Row[] =>
      (r?.data?.result ?? [])
        .map((x) => ({ labels: x.metric ?? {}, value: Number(x.value?.[1]) }))
        .filter((x) => Number.isFinite(x.value)),
  })
}

// The values of a range query's first series — the KPI sparklines.
export function useHealthSparkline(key: string, query: string, minutes = 360, step = '5m') {
  const { interval } = useRefreshInterval()
  return useQuery({
    queryKey: [HEALTH_KEY, 'spark', key, query, minutes, step],
    queryFn: () => {
      const end = Math.floor(Date.now() / 1000)
      return api.adminQueryMetricsRange({ query, start: end - minutes * 60, end, step })
    },
    refetchInterval: interval,
    select: (r): number[] => (r?.data?.result?.[0]?.values ?? []).map(([, v]) => Number(v)).filter(Number.isFinite),
  })
}

export function sum(rows: Row[] | undefined): number {
  return (rows ?? []).reduce((a, r) => a + r.value, 0)
}

export function first(rows: Row[] | undefined): number | null {
  return rows && rows.length > 0 ? rows[0].value : null
}

export function compact(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${(n / 1000).toFixed(1)}k`
  return Math.round(n).toLocaleString('en-US')
}

export function usd(n: number): string {
  return n >= 100 ? `$${Math.round(n).toLocaleString()}` : `$${n.toFixed(2)}`
}

/** The props every chart of a Health tab shares (range, refresh, look). */
export interface ChartBase {
  bypassClusterScope: true
  controlledRangeMinutes: number
  height: number
  recessed: true
  refetchMs: number
  showStats: false
}

export interface HealthFrameProps {
  charts: ChartBase
  rangeMinutes: number
  // What one bar covers at this range, and one point of a line, for the
  // footnotes.
  perInterval: string
  perPoint: string
}

const RANGE_KEY = 'kb-admin-health-range'

function storedRange(): number {
  try {
    const v = Number(localStorage.getItem(RANGE_KEY))
    if (OVERVIEW_RANGE_OPTIONS.some((o) => o.minutes === v)) return v
  } catch {
    // Storage unavailable: fall back to the default range.
  }
  return 360
}

function storeRange(minutes: number) {
  try {
    localStorage.setItem(RANGE_KEY, String(minutes))
  } catch {
    // Storage unavailable: the range still applies for this visit.
  }
}

function isHealthQuery(q: Query): boolean {
  return q.queryKey[0] === HEALTH_KEY || q.queryKey[0] === 'metrics-range'
}

// HealthFrame is a Health tab's chrome: its own refresh interval — 30 s unless
// the admin picks another here; the API pushes its series every 30 s, so a
// faster default only re-reads the same points, and a choice made on the
// cluster pages does not carry over —, the time of the newest answer, a manual
// refresh and the range every chart follows.
export function HealthFrame({ intro, children }: { intro: ReactNode; children: (p: HealthFrameProps) => ReactNode }) {
  return (
    <ScopedRefreshProvider storageKey="kb-admin-health-refresh" defaultInterval={30_000}>
      <Frame intro={intro}>{children}</Frame>
    </ScopedRefreshProvider>
  )
}

function Frame({ intro, children }: { intro: ReactNode; children: (p: HealthFrameProps) => ReactNode }) {
  const [rangeMinutes, setRangeState] = useState(storedRange)
  const setRangeMinutes = (minutes: number) => {
    setRangeState(minutes)
    storeRange(minutes)
  }
  const { interval } = useRefreshInterval()
  const queryClient = useQueryClient()
  const fetching = useIsFetching({ predicate: isHealthQuery }) > 0
  const updatedAt = Math.max(
    0,
    ...queryClient
      .getQueryCache()
      .findAll({ predicate: isHealthQuery })
      .map((q) => q.state.dataUpdatedAt),
  )
  const refresh = () => queryClient.refetchQueries({ predicate: isHealthQuery, type: 'active' })

  // No stats column: in a three-up grid it took half the card. The hover
  // tooltip carries the values, and multi-series charts get their inline legend.
  const charts: ChartBase = { bypassClusterScope: true, controlledRangeMinutes: rangeMinutes, height: 180, recessed: true, refetchMs: interval, showStats: false }
  const lineStep = OVERVIEW_RANGE_OPTIONS.find((o) => o.minutes === rangeMinutes)?.step ?? ''
  const perInterval = intervalLabel(barStep(rangeMinutes, lineStep))
  const perPoint = intervalLabel(lineStep)

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <p className="text-sm text-kb-text-secondary max-w-[70ch]">{intro}</p>
        <div className="flex items-center gap-2">
          <DataFreshnessIndicator dataUpdatedAt={updatedAt} isFetching={fetching} />
          <button
            type="button"
            onClick={refresh}
            disabled={fetching}
            title="Refresh now"
            aria-label="Refresh now"
            className="inline-flex items-center justify-center w-7 h-7 rounded-md border border-kb-border bg-kb-card text-kb-text-secondary hover:border-kb-border-active hover:text-kb-text-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${fetching ? 'animate-spin' : ''}`} />
          </button>
          <RangeSelector value={rangeMinutes} onChange={setRangeMinutes} />
        </div>
      </div>
      {children({ charts, rangeMinutes, perInterval, perPoint })}
    </div>
  )
}
