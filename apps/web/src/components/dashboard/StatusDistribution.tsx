// Shared status_class visualization primitives.
//
// Top Workloads · Traffic was the first panel to break HTTP rates
// down by status_class (2xx / 3xx / 4xx / 5xx). Top Workloads ·
// Latency picked up the same lens for "is this slow workload also
// erroring, or just slow?". Rather than duplicate the bar + chip
// + parsing logic, both panels now read from this module.
//
// Single dist query — shared queryKey prefix lets both panels
// dedupe their fetch via TanStack Query's cache. See
// useWorkloadStatusDist below.

import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { collapsePodToWorkload } from '@/utils/promql'
import { TooltipRow } from '@/components/shared/Tooltip'

// ─── Types ──────────────────────────────────────────────────────

export interface StatusDistribution {
  success: number   // 2xx rate
  redirect: number  // 3xx rate
  clientErr: number // 4xx rate
  serverErr: number // 5xx rate
  unknown: number   // anything outside the four classes (incl. 1xx, code=0)
}

export const EMPTY_DIST: StatusDistribution = {
  success: 0,
  redirect: 0,
  clientErr: 0,
  serverErr: 0,
  unknown: 0,
}

// Colors carried forward from TopWorkloadsTraffic. 2xx green,
// 3xx slate (informational, not concerning), 4xx amber (caller
// fault), 5xx red (server fault), unknown dim slate.
export const STATUS_COLORS = {
  success: '#22c55e',
  redirect: '#94a3b8',
  clientErr: '#f59e0b',
  serverErr: '#ef4056',
  unknown: '#64748b',
} as const

// ─── Shared query / cache hook ──────────────────────────────────

// Both Traffic and Latency panels read this. Same queryKey ⇒
// TanStack Query dedupes the fetch — only one HTTP round-trip per
// (rangeMinutes) regardless of how many panels are mounted.
export function useWorkloadStatusDist(rangeMinutes: number) {
  const RATE_WINDOW = `${rangeMinutes}m`
  const query = `sum by (dst_namespace, workload, status_class) (${collapsePodToWorkload(
    `rate(pod_flow_http_requests_total{source="hubble"}[${RATE_WINDOW}])`,
  )})`
  return useQuery({
    queryKey: ['reliability', 'workload-status-dist', RATE_WINDOW],
    queryFn: () => api.queryMetrics({ query }),
    refetchInterval: 30_000,
    retry: false,
  })
}

// ─── Index builder ──────────────────────────────────────────────

export function buildDistIndex(
  result: Array<{ metric: Record<string, string>; value?: [number, string] }> | undefined,
): Map<string, StatusDistribution> {
  const map = new Map<string, StatusDistribution>()
  if (!result) return map
  for (const s of result) {
    const ns = s.metric.dst_namespace
    const wl = s.metric.workload
    if (!ns || !wl) continue
    const v = parseFloat(s.value?.[1] ?? '0')
    if (!Number.isFinite(v) || v <= 0) continue
    const k = `${ns}/${wl}`
    let cur = map.get(k)
    if (!cur) {
      cur = { ...EMPTY_DIST }
      map.set(k, cur)
    }
    // Agent emits: info (1xx), ok (2xx), redir (3xx), client_err
    // (4xx), server_err (5xx), unknown (code 0 / unparseable).
    // We collapse 1xx into the "other" bucket — vanishingly rare
    // (WebSocket upgrades, Expect: 100-continue) and not worth
    // its own chip.
    switch (s.metric.status_class) {
      case 'ok':
        cur.success += v
        break
      case 'redir':
        cur.redirect += v
        break
      case 'client_err':
        cur.clientErr += v
        break
      case 'server_err':
        cur.serverErr += v
        break
      default:
        cur.unknown += v
    }
  }
  return map
}

export function distTotal(d: StatusDistribution): number {
  return d.success + d.redirect + d.clientErr + d.serverErr + d.unknown
}

export function distErrorRate(d: StatusDistribution): number {
  const total = distTotal(d)
  if (total === 0) return NaN
  return ((d.clientErr + d.serverErr) / total) * 100
}

// ─── Visual primitives ──────────────────────────────────────────

// Stacked horizontal bar of status_class proportions. Track is a
// muted background so the row stays visually consistent when no
// data exists yet (loading state, or workload with zero traffic).
export function StatusDistBar({ dist }: { dist: StatusDistribution }) {
  const total = distTotal(dist)
  if (total <= 0) {
    return (
      <div
        className="flex-1 h-[6px] rounded-full"
        style={{ background: 'var(--kb-bar-track)' }}
        aria-hidden
      />
    )
  }
  const segments: Array<{ color: string; pct: number }> = [
    { color: STATUS_COLORS.success, pct: (dist.success / total) * 100 },
    { color: STATUS_COLORS.redirect, pct: (dist.redirect / total) * 100 },
    { color: STATUS_COLORS.clientErr, pct: (dist.clientErr / total) * 100 },
    { color: STATUS_COLORS.serverErr, pct: (dist.serverErr / total) * 100 },
    { color: STATUS_COLORS.unknown, pct: (dist.unknown / total) * 100 },
  ].filter((s) => s.pct > 0)
  return (
    <div
      className="flex-1 h-[6px] rounded-full overflow-hidden flex"
      style={{ background: 'var(--kb-bar-track)' }}
      aria-hidden
    >
      {segments.map((s, i) => (
        <span
          key={i}
          style={{ width: `${s.pct}%`, background: s.color }}
          className="block h-full"
        />
      ))}
    </div>
  )
}

// Compact chips with the rate per status class. Skips zero
// classes — absent chip means truly zero, not "we forgot to ask
// about that class". Wraps onto multiple lines on narrow
// viewports rather than truncating.
export function ClassRates({ dist }: { dist: StatusDistribution }) {
  const classes: Array<{ key: string; color: string; label: string; value: number }> = []
  if (dist.success > 0) classes.push({ key: 'success', color: STATUS_COLORS.success, label: '2xx', value: dist.success })
  if (dist.redirect > 0) classes.push({ key: 'redirect', color: STATUS_COLORS.redirect, label: '3xx', value: dist.redirect })
  if (dist.clientErr > 0) classes.push({ key: 'clientErr', color: STATUS_COLORS.clientErr, label: '4xx', value: dist.clientErr })
  if (dist.serverErr > 0) classes.push({ key: 'serverErr', color: STATUS_COLORS.serverErr, label: '5xx', value: dist.serverErr })
  if (dist.unknown > 0) classes.push({ key: 'unknown', color: STATUS_COLORS.unknown, label: 'other', value: dist.unknown })
  if (classes.length === 0) return null
  return (
    <div className="flex items-center gap-1 mt-1 flex-wrap">
      {classes.map((c) => (
        <span
          key={c.key}
          className="text-[9px] font-mono tabular-nums px-1.5 py-0.5 rounded"
          style={{ background: `${c.color}20`, color: c.color }}
        >
          {c.label}&nbsp;{formatRateCompact(c.value)}
        </span>
      ))}
    </div>
  )
}

// Tooltip rows for each non-zero class. Used by both panels'
// hover tooltips so the breakdown reads identically across them.
export function ClassTooltipRows({ dist }: { dist: StatusDistribution }) {
  if (distTotal(dist) <= 0) return null
  return (
    <>
      {dist.success > 0 && (
        <TooltipRow color={STATUS_COLORS.success} label="2xx" value={formatRate(dist.success)} />
      )}
      {dist.redirect > 0 && (
        <TooltipRow color={STATUS_COLORS.redirect} label="3xx" value={formatRate(dist.redirect)} />
      )}
      {dist.clientErr > 0 && (
        <TooltipRow color={STATUS_COLORS.clientErr} label="4xx" value={formatRate(dist.clientErr)} />
      )}
      {dist.serverErr > 0 && (
        <TooltipRow color={STATUS_COLORS.serverErr} label="5xx" value={formatRate(dist.serverErr)} />
      )}
      {dist.unknown > 0 && (
        <TooltipRow color={STATUS_COLORS.unknown} label="other" value={formatRate(dist.unknown)} />
      )}
    </>
  )
}

// ─── Formatting ─────────────────────────────────────────────────

// Compact format for the in-row chips. Drops "/s" since the row
// already establishes the unit elsewhere (panel title or trailing
// rate label).
function formatRateCompact(reqPerSec: number): string {
  if (!Number.isFinite(reqPerSec) || reqPerSec === 0) return '0'
  if (reqPerSec < 0.1) return reqPerSec.toFixed(2)
  if (reqPerSec < 1) return reqPerSec.toFixed(2)
  if (reqPerSec < 10) return reqPerSec.toFixed(1)
  return Math.round(reqPerSec).toString()
}

// Verbose format for tooltips — keeps the unit explicit since
// tooltip rows read alone, not in the row's context.
function formatRate(reqPerSec: number): string {
  if (!Number.isFinite(reqPerSec)) return '—'
  if (reqPerSec === 0) return '0 req/s'
  if (reqPerSec < 1) return `${reqPerSec.toFixed(2)} req/s`
  if (reqPerSec < 10) return `${reqPerSec.toFixed(1)} req/s`
  return `${Math.round(reqPerSec)} req/s`
}
