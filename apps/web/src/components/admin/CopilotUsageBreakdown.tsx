import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { api } from '@/services/api'
import type { BreakdownDimension, CopilotUsageSummary } from '@/types/copilotUsage'

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}
const fmtUsd = (n: number) => `$${n.toFixed(n >= 1 ? 2 : 4)}`

// ─── Reliability strip ───────────────────────────────────────────────
// Overall system-health tiles: error rate, fallback rate, max-rounds hits,
// and interactive-vs-total (aux records excluded from "interactive").

export function ReliabilityStrip({ summary }: { summary: CopilotUsageSummary }) {
  const tiles: Array<{ label: string; value: string; sub: string; warn: boolean }> = [
    {
      label: 'Error rate',
      value: `${summary.errorRate.toFixed(0)}%`,
      sub: `${summary.errorSessions}/${summary.sessions} sessions`,
      warn: summary.errorRate > 0,
    },
    {
      label: 'Fallback rate',
      value: `${summary.fallbackRate.toFixed(0)}%`,
      sub: `${summary.fallbackSessions} used fallback`,
      warn: summary.fallbackRate > 0,
    },
    {
      label: 'Max-rounds hit',
      value: `${summary.maxRoundsSessions}`,
      sub: 'capped before finishing',
      warn: summary.maxRoundsSessions > 0,
    },
    {
      label: 'Interactive',
      value: `${summary.interactiveSessions}`,
      sub: `of ${summary.sessions} records (excl. automatic)`,
      warn: false,
    },
  ]
  return (
    <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mt-3">
      {tiles.map((t) => (
        <div key={t.label} className="bg-kb-card border border-kb-border rounded-[10px] p-3">
          <div className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            {t.label}
          </div>
          <div
            className={`text-xl font-semibold tabular-nums mt-1 ${
              t.warn ? 'text-status-warn' : 'text-kb-text-primary'
            }`}
          >
            {t.value}
          </div>
          <div className="text-[10px] text-kb-text-tertiary mt-0.5 truncate">{t.sub}</div>
        </div>
      ))}
    </div>
  )
}

// ─── Breakdown section ───────────────────────────────────────────────

const DIMENSIONS: Array<{ id: BreakdownDimension; label: string }> = [
  { id: 'user', label: 'User' },
  { id: 'trigger', label: 'Trigger' },
]

const METRICS: Array<{
  id: string
  label: string
  get: (s: CopilotUsageSummary) => number
  fmt: (v: number) => string
}> = [
  { id: 'cost', label: 'Cost', get: (s) => s.estimatedUsd, fmt: fmtUsd },
  { id: 'sessions', label: 'Sessions', get: (s) => s.sessions, fmt: (v) => v.toLocaleString() },
  { id: 'tokens', label: 'Tokens', get: (s) => s.totalBilledTokens, fmt: fmtTokens },
]

export function BreakdownSection({ range }: { range: string }) {
  const [dim, setDim] = useState<BreakdownDimension>('user')
  const [metricId, setMetricId] = useState('cost')
  const metric = METRICS.find((m) => m.id === metricId) ?? METRICS[0]

  const { data, isLoading } = useQuery({
    queryKey: ['copilot-usage-breakdown', range, dim],
    queryFn: () => api.getCopilotUsageBreakdown(range, dim),
    refetchInterval: 60_000,
  })

  // Resolve user ids → display names for the per-user view (admin page, so the
  // users list is available). Falls back to the id for unknown/special keys.
  const usersQuery = useQuery({
    queryKey: ['users'],
    queryFn: api.listUsers,
    enabled: dim === 'user',
    staleTime: 5 * 60_000,
  })
  const userNames = useMemo(() => {
    const m = new Map<string, string>()
    for (const u of usersQuery.data ?? []) m.set(u.id, u.name || u.username || u.id)
    return m
  }, [usersQuery.data])
  const labelFor = (key: string) => (dim === 'user' ? userNames.get(key) ?? key : key)

  const groups = (data?.groups ?? [])
    .slice()
    .sort((a, b) => metric.get(b.summary) - metric.get(a.summary))
    .slice(0, 12)
  const max = Math.max(1, ...groups.map((g) => metric.get(g.summary)))

  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4 mt-4">
      <div className="flex items-center justify-between flex-wrap gap-2 mb-3">
        <div className="flex items-center gap-2">
          <h3 className="text-xs font-semibold text-kb-text-primary">Breakdown by</h3>
          <div className="flex gap-1">
            {DIMENSIONS.map((d) => (
              <button
                key={d.id}
                onClick={() => setDim(d.id)}
                className={`px-2.5 py-1 rounded-md text-[10px] font-mono uppercase tracking-[0.06em] border transition-colors ${
                  dim === d.id
                    ? 'bg-kb-accent-light text-kb-accent border-kb-accent/30'
                    : 'bg-kb-card text-kb-text-secondary border-kb-border hover:border-kb-border-active'
                }`}
              >
                {d.label}
              </button>
            ))}
          </div>
        </div>
        <div className="flex gap-1">
          {METRICS.map((m) => (
            <button
              key={m.id}
              onClick={() => setMetricId(m.id)}
              className={`px-2 py-0.5 rounded-md text-[10px] font-mono transition-colors ${
                metricId === m.id
                  ? 'bg-kb-elevated text-kb-text-primary'
                  : 'text-kb-text-tertiary hover:text-kb-text-secondary'
              }`}
            >
              {m.label}
            </button>
          ))}
        </div>
      </div>

      {isLoading ? (
        <div className="flex items-center justify-center py-10 text-kb-text-tertiary">
          <Loader2 className="w-5 h-5 animate-spin" />
        </div>
      ) : groups.length === 0 ? (
        <div className="py-10 text-center text-xs text-kb-text-tertiary font-mono">no data for this breakdown</div>
      ) : (
        <ul className="space-y-2">
          {groups.map((g) => {
            const v = metric.get(g.summary)
            const pct = Math.max(3, (v / max) * 100)
            const s = g.summary
            return (
              <li key={g.key} className="space-y-1">
                <div className="flex items-center gap-3">
                  <span className="w-40 shrink-0 truncate text-[11px] font-mono text-kb-text-secondary" title={g.key}>
                    {labelFor(g.key)}
                  </span>
                  <div className="flex-1 h-5 bg-kb-bg rounded overflow-hidden relative">
                    <div className="h-full bg-kb-accent/60 transition-all" style={{ width: `${pct}%` }} />
                    <span className="absolute inset-0 flex items-center px-2 text-[10px] font-mono text-kb-text-primary tabular-nums">
                      {metric.fmt(v)}
                    </span>
                  </div>
                </div>
                {(s.errorSessions > 0 || s.fallbackSessions > 0 || s.maxRoundsSessions > 0) && (
                  <div className="flex items-center gap-2 ml-[11.5rem] text-[10px] font-mono text-kb-text-tertiary">
                    {s.errorSessions > 0 && (
                      <span className="text-status-warn">{s.errorRate.toFixed(0)}% error</span>
                    )}
                    {s.fallbackSessions > 0 && <span className="text-status-warn">{s.fallbackSessions}× fallback</span>}
                    {s.maxRoundsSessions > 0 && (
                      <span className="text-status-warn">{s.maxRoundsSessions}× max-rounds</span>
                    )}
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
