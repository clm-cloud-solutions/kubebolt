import { useMemo, useState } from 'react'
import { Modal } from '@/components/shared/Modal'
import { useQuery } from '@tanstack/react-query'
import {
  BarChart3,
  Bot,
  CircleDollarSign,
  Clock,
  Database,
  Wrench,
  AlertTriangle,
  Scissors,
  Zap,
  RefreshCw,
} from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import type {
  CopilotSessionEnriched,
  CopilotUsageBucket,
  CopilotUsageSummary,
} from '@/types/copilotUsage'

type Range = '24h' | '7d' | '30d'

function fmtTokens(n: number): string {
  if (!Number.isFinite(n)) return '0'
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${Math.round(n / 1_000)}k`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function fmtUSD(n: number): string {
  if (n < 0.01) return `$${n.toFixed(4)}`
  if (n < 1) return `$${n.toFixed(3)}`
  if (n < 100) return `$${n.toFixed(2)}`
  return `$${Math.round(n).toLocaleString()}`
}

function fmtDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = s / 60
  if (m < 60) return `${m.toFixed(1)}m`
  return `${(m / 60).toFixed(1)}h`
}

function fmtRelative(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime()
  const s = Math.floor(diff / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

export function CopilotUsagePage() {
  const [range, setRange] = useState<Range>('7d')
  const [selected, setSelected] = useState<CopilotSessionEnriched | null>(null)

  const summaryQuery = useQuery({
    queryKey: ['copilot-usage-summary', range],
    queryFn: () => api.getCopilotUsageSummary(range),
    refetchInterval: 60_000,
  })

  const timeseriesQuery = useQuery({
    queryKey: ['copilot-usage-timeseries', range],
    queryFn: () => api.getCopilotUsageTimeseries(range),
    refetchInterval: 60_000,
  })

  const sessionsQuery = useQuery({
    queryKey: ['copilot-usage-sessions', range],
    queryFn: () => api.getCopilotUsageSessions(range, 200),
    refetchInterval: 60_000,
  })

  const summary = summaryQuery.data
  const timeseries = timeseriesQuery.data || []
  const sessions = sessionsQuery.data || []

  const isRefreshing =
    summaryQuery.isFetching || timeseriesQuery.isFetching || sessionsQuery.isFetching
  const handleRefresh = () => {
    summaryQuery.refetch()
    timeseriesQuery.refetch()
    sessionsQuery.refetch()
  }

  if (summaryQuery.isLoading || timeseriesQuery.isLoading) {
    return <LoadingSpinner />
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-5">
        <div className="flex items-center gap-3">
          <div className="w-9 h-9 rounded-lg bg-kb-accent-light flex items-center justify-center">
            <BarChart3 className="w-5 h-5 text-kb-accent" />
          </div>
          <div>
            <h1 className="text-lg font-semibold text-kb-text-primary">Copilot Usage</h1>
            <p className="text-xs text-kb-text-tertiary">
              Session analytics, token spend, tool activity. Stored locally for 30 days.
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={handleRefresh}
            disabled={isRefreshing}
            title="Refresh"
            className="inline-flex items-center justify-center w-7 h-7 rounded-md border border-kb-border bg-kb-card text-kb-text-secondary hover:border-kb-border-active hover:text-kb-text-primary disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            <RefreshCw className={`w-3.5 h-3.5 ${isRefreshing ? 'animate-spin' : ''}`} />
          </button>
          <RangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {summary && <SummaryTiles summary={summary} />}

      <div className="grid grid-cols-1 xl:grid-cols-2 gap-4 mt-5">
        <TokensChart buckets={timeseries} range={range} />
        <ToolsChart summary={summary} />
      </div>

      <SessionsTable
        sessions={sessions}
        onSelect={setSelected}
        isLoading={sessionsQuery.isLoading}
      />

      {selected && <SessionModal session={selected} onClose={() => setSelected(null)} />}
    </div>
  )
}

function RangeSelector({ value, onChange }: { value: Range; onChange: (v: Range) => void }) {
  const options: Range[] = ['24h', '7d', '30d']
  return (
    <div className="flex gap-1">
      {options.map((opt) => (
        <button
          key={opt}
          onClick={() => onChange(opt)}
          className={`px-2.5 py-1 rounded-md text-[10px] font-mono uppercase tracking-[0.06em] border transition-colors ${
            value === opt
              ? 'bg-kb-accent-light text-kb-accent border-kb-accent/30'
              : 'bg-kb-card text-kb-text-secondary border-kb-border hover:border-kb-border-active'
          }`}
        >
          {opt}
        </button>
      ))}
    </div>
  )
}

function SummaryTiles({ summary }: { summary: CopilotUsageSummary }) {
  const tiles = [
    {
      label: 'Sessions',
      value: summary.sessions.toLocaleString(),
      sub:
        summary.errorSessions > 0
          ? `${summary.errorSessions} error${summary.errorSessions === 1 ? '' : 's'}`
          : `${summary.avgRounds.toFixed(1)} avg rounds`,
      icon: Bot,
    },
    {
      label: 'Tokens billed',
      value: fmtTokens(summary.totalBilledTokens),
      sub: `${summary.cacheHitPct.toFixed(0)}% cached · ${fmtTokens(summary.cacheReadTokens)} read`,
      icon: Database,
    },
    {
      label: 'Estimated cost',
      value: fmtUSD(summary.estimatedUsd),
      sub: summary.estimatedUsd > 0 ? 'list pricing, approx' : 'no known pricing',
      icon: CircleDollarSign,
    },
    {
      label: 'Avg duration',
      value: fmtDuration(summary.avgDurationMs),
      sub: `${summary.compacts} compact${summary.compacts === 1 ? '' : 's'} fired`,
      icon: Clock,
    },
  ]
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
      {tiles.map(({ label, value, sub, icon: Icon }) => (
        <div key={label} className="bg-kb-card border border-kb-border rounded-[10px] p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
              {label}
            </span>
            <Icon className="w-4 h-4 text-kb-text-tertiary" />
          </div>
          <div className="text-xl font-semibold text-kb-text-primary">{value}</div>
          <div className="text-[10px] font-mono text-kb-text-tertiary mt-1">{sub}</div>
        </div>
      ))}
    </div>
  )
}

function TokensChart({ buckets, range }: { buckets: CopilotUsageBucket[]; range: Range }) {
  const max = useMemo(() => {
    return Math.max(
      1,
      ...buckets.map((b) => b.inputTokens + b.outputTokens + b.cacheReadTokens),
    )
  }, [buckets])
  const bucketWidth = 100 / Math.max(buckets.length, 1)

  // Hover state for the custom tooltip. Position is viewport-fixed
  // so the tooltip follows the cursor and escapes the card's clip
  // box — same pattern the cluster map hover tooltip uses.
  const [hover, setHover] = useState<{
    bucket: CopilotUsageBucket
    x: number
    y: number
  } | null>(null)

  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <h3 className="text-xs font-semibold text-kb-text-primary mb-3">Tokens over time</h3>
      <div className="relative h-40 flex items-end gap-0.5">
        {buckets.length === 0 ? (
          <div className="w-full h-full flex items-center justify-center text-xs text-kb-text-tertiary font-mono">
            no data
          </div>
        ) : (
          buckets.map((b, i) => {
            const total = b.inputTokens + b.outputTokens + b.cacheReadTokens
            const h = (total / max) * 100
            const cachedPct = total === 0 ? 0 : (b.cacheReadTokens / total) * 100
            const outputPct = total === 0 ? 0 : (b.outputTokens / total) * 100
            return (
              <div
                key={i}
                className="relative flex flex-col justify-end group"
                style={{ width: `${bucketWidth}%`, height: '100%' }}
                onMouseMove={(e) =>
                  setHover({ bucket: b, x: e.clientX, y: e.clientY })
                }
                onMouseLeave={() => setHover(null)}
              >
                <div
                  className="w-full transition-opacity group-hover:opacity-80"
                  style={{
                    height: `${h}%`,
                    background: `linear-gradient(to top, rgb(147, 197, 253) 0%, rgb(147, 197, 253) ${cachedPct}%, var(--kb-accent) ${cachedPct}%, var(--kb-accent) ${100 - outputPct}%, rgb(251, 191, 36) ${100 - outputPct}%, rgb(251, 191, 36) 100%)`,
                  }}
                />
              </div>
            )
          })
        )}
      </div>

      {/* Hover tooltip — same shape as MetricChart / cluster map so
          the styling reads as "tooltip" across every view. */}
      {hover && (
        <div
          style={{
            position: 'fixed',
            left: hover.x + 14,
            top: hover.y + 14,
            pointerEvents: 'none',
            zIndex: 1000,
          }}
          className="bg-kb-elevated/95 backdrop-blur border border-kb-border rounded-md px-3 py-2 text-[11px] shadow-xl min-w-[180px]"
        >
          <div className="text-kb-text-primary font-mono font-semibold text-[12px] tabular-nums mb-2 pb-1.5 border-b border-kb-border/60">
            {new Date(hover.bucket.time).toLocaleString()}
          </div>
          <div className="space-y-1">
            <TooltipBucketRow color="rgb(147, 197, 253)" label="Cache read" value={fmtTokens(hover.bucket.cacheReadTokens)} />
            <TooltipBucketRow color="var(--kb-accent)" label="Input (fresh)" value={fmtTokens(hover.bucket.inputTokens)} />
            <TooltipBucketRow color="rgb(251, 191, 36)" label="Output" value={fmtTokens(hover.bucket.outputTokens)} />
            <div className="pt-1 mt-1 border-t border-kb-border/60 flex items-center gap-2">
              <span className="w-2 h-2 rounded-full flex-shrink-0 bg-transparent" />
              <span className="text-kb-text-secondary">Sessions</span>
              <span className="ml-auto tabular-nums font-mono text-kb-text-primary">{hover.bucket.sessions}</span>
            </div>
          </div>
        </div>
      )}
      <div className="flex items-center gap-4 mt-3 text-[10px] font-mono text-kb-text-tertiary">
        <LegendSwatch color="rgb(147, 197, 253)" label="Cache read" />
        <LegendSwatch color="var(--kb-accent)" label="Input (fresh)" />
        <LegendSwatch color="rgb(251, 191, 36)" label="Output" />
        <span className="ml-auto">{range} · {buckets.length} buckets</span>
      </div>
    </div>
  )
}

// Single row inside the bucket-hover tooltip. Shape matches the
// MetricChart / cluster map tooltip conventions: colored dot, label,
// value flush right, tabular-nums for readable alignment.
function TooltipBucketRow({ color, label, value }: { color: string; label: string; value: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="w-2 h-2 rounded-full flex-shrink-0" style={{ background: color }} />
      <span className="text-kb-text-secondary">{label}</span>
      <span className="ml-auto tabular-nums font-mono text-kb-text-primary">{value}</span>
    </div>
  )
}

function LegendSwatch({ color, label }: { color: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className="w-2.5 h-2.5 rounded-sm" style={{ background: color }} />
      {label}
    </span>
  )
}

function ToolsChart({ summary }: { summary?: CopilotUsageSummary }) {
  if (!summary || summary.topTools.length === 0) {
    return (
      <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
        <h3 className="text-xs font-semibold text-kb-text-primary mb-3">Top tools</h3>
        <div className="h-40 flex items-center justify-center text-xs text-kb-text-tertiary font-mono">
          no tool calls yet
        </div>
      </div>
    )
  }
  const maxCalls = Math.max(...summary.topTools.map((t) => t.calls))
  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <h3 className="text-xs font-semibold text-kb-text-primary mb-3">Top tools</h3>
      <div className="space-y-1.5">
        {summary.topTools.map((t) => {
          const w = Math.max(4, (t.calls / maxCalls) * 100)
          const errorRate = t.calls > 0 ? (t.errors / t.calls) * 100 : 0
          return (
            <div key={t.name} className="flex items-center gap-2">
              <span className="text-[11px] font-mono text-kb-text-secondary w-40 truncate shrink-0">
                {t.name}
              </span>
              <div className="flex-1 h-5 bg-kb-bg rounded overflow-hidden relative">
                <div
                  className="h-full bg-kb-accent/60"
                  style={{ width: `${w}%` }}
                />
                <span className="absolute inset-0 flex items-center px-2 text-[10px] font-mono text-kb-text-primary">
                  {t.calls} calls · {fmtTokens(t.bytes / 4)} tokens
                  {errorRate > 0 && (
                    <span className="ml-auto text-status-warn">
                      {errorRate.toFixed(0)}% err
                    </span>
                  )}
                </span>
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function SessionsTable({
  sessions,
  onSelect,
  isLoading,
}: {
  sessions: CopilotSessionEnriched[]
  onSelect: (s: CopilotSessionEnriched) => void
  isLoading: boolean
}) {
  return (
    <div className="mt-5 bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
      <div className="px-4 py-3 border-b border-kb-border flex items-center justify-between">
        <h3 className="text-xs font-semibold text-kb-text-primary">Recent sessions</h3>
        <span className="text-[10px] font-mono text-kb-text-tertiary">{sessions.length} shown</span>
      </div>
      {isLoading ? (
        <div className="py-10"><LoadingSpinner /></div>
      ) : sessions.length === 0 ? (
        <div className="py-12 text-center text-xs text-kb-text-tertiary font-mono">no sessions yet</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-kb-border text-[10px] font-mono uppercase tracking-[0.06em] text-kb-text-tertiary">
                <th className="text-left px-4 py-2">When</th>
                <th className="text-left px-4 py-2">Model</th>
                <th className="text-left px-4 py-2">Trigger</th>
                <th className="text-right px-4 py-2">Rounds</th>
                <th className="text-right px-4 py-2">Billed</th>
                <th className="text-right px-4 py-2">Cached</th>
                <th className="text-right px-4 py-2">Tools</th>
                <th className="text-right px-4 py-2">Duration</th>
                <th className="text-right px-4 py-2">Cost</th>
              </tr>
            </thead>
            <tbody>
              {sessions.map((s) => {
                const billed = s.usage.inputTokens + s.usage.outputTokens
                const cached = s.usage.cacheReadTokens ?? 0
                return (
                  <tr
                    key={s.id}
                    onClick={() => onSelect(s)}
                    className="border-b border-kb-border hover:bg-kb-card-hover cursor-pointer"
                  >
                    <td className="px-4 py-2 text-kb-text-secondary font-mono">
                      {fmtRelative(s.timestamp)}
                    </td>
                    <td className="px-4 py-2 text-kb-text-secondary font-mono">
                      {s.provider}·{s.model || 'default'}
                    </td>
                    <td className="px-4 py-2">
                      <TriggerBadge trigger={s.trigger} />
                    </td>
                    <td className="px-4 py-2 text-right text-kb-text-secondary font-mono">
                      {s.rounds}
                    </td>
                    <td className="px-4 py-2 text-right text-kb-text-primary font-mono">
                      {fmtTokens(billed)}
                    </td>
                    <td className="px-4 py-2 text-right text-kb-text-tertiary font-mono">
                      {cached > 0 ? fmtTokens(cached) : '—'}
                    </td>
                    <td className="px-4 py-2 text-right text-kb-text-secondary font-mono">
                      {s.toolCalls}
                    </td>
                    <td className="px-4 py-2 text-right text-kb-text-secondary font-mono">
                      {fmtDuration(s.durationMs)}
                    </td>
                    <td className="px-4 py-2 text-right text-kb-accent font-mono">
                      {s.estimatedUsd > 0 ? fmtUSD(s.estimatedUsd) : '—'}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function TriggerBadge({ trigger }: { trigger: string }) {
  const color = {
    manual: 'bg-kb-elevated text-kb-text-secondary',
    insight: 'bg-status-warn-dim text-status-warn',
    not_ready_resource: 'bg-status-error-dim text-status-error',
    warning_event: 'bg-status-warn-dim text-status-warn',
  }[trigger] || 'bg-kb-elevated text-kb-text-secondary'
  return (
    <span className={`px-2 py-0.5 rounded text-[9px] font-mono uppercase tracking-wider ${color}`}>
      {trigger || 'manual'}
    </span>
  )
}

function SessionModal({
  session,
  onClose,
}: {
  session: CopilotSessionEnriched
  onClose: () => void
}) {
  const title = `${new Date(session.timestamp).toLocaleString()} · ${session.cluster}`

  return (
    <Modal badge="Copilot session" title={title} onClose={onClose} size="lg">
      <div className="flex-1 overflow-y-auto p-5 space-y-4">
          <div className="grid grid-cols-2 gap-3 text-xs">
            <KV label="Model" value={`${session.provider} · ${session.model || 'default'}`} />
            <KV label="Trigger" value={session.trigger || 'manual'} />
            <KV label="Rounds" value={String(session.rounds)} />
            <KV label="Duration" value={fmtDuration(session.durationMs)} />
            <KV label="Input" value={fmtTokens(session.usage.inputTokens)} />
            <KV label="Output" value={fmtTokens(session.usage.outputTokens)} />
            <KV label="Cache read" value={fmtTokens(session.usage.cacheReadTokens ?? 0)} />
            <KV label="Cost (est.)" value={fmtUSD(session.estimatedUsd)} />
          </div>

          {session.tools && Object.keys(session.tools).length > 0 && (
            <div>
              <div className="flex items-center gap-1.5 mb-2">
                <Wrench className="w-3.5 h-3.5 text-kb-accent" />
                <h4 className="text-xs font-semibold text-kb-text-primary">Tool calls</h4>
              </div>
              <div className="space-y-1">
                {Object.entries(session.tools).map(([name, t]) => (
                  <div
                    key={name}
                    className="flex items-center gap-2 text-[11px] font-mono"
                  >
                    <span className="flex-1 text-kb-text-secondary truncate">{name}</span>
                    <span className="text-kb-text-tertiary">{t.calls}×</span>
                    <span className="text-kb-text-tertiary">{fmtTokens(t.bytes / 4)} tok</span>
                    {t.errors > 0 && (
                      <span className="text-status-error">
                        <AlertTriangle className="w-3 h-3 inline" /> {t.errors}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}

          {session.compacts && session.compacts.length > 0 && (
            <div>
              <div className="flex items-center gap-1.5 mb-2">
                <Scissors className="w-3.5 h-3.5 text-kb-accent" />
                <h4 className="text-xs font-semibold text-kb-text-primary">Compactions</h4>
              </div>
              <div className="space-y-1">
                {session.compacts.map((c, i) => (
                  <div key={i} className="text-[11px] font-mono text-kb-text-secondary">
                    {c.turnsFolded} turns · {fmtTokens(c.tokensBefore)} →{' '}
                    {fmtTokens(c.tokensAfter)} · {c.model}
                  </div>
                ))}
              </div>
            </div>
          )}

          {session.fallback && (
            <div className="flex items-center gap-1.5 text-[11px] font-mono text-status-warn">
              <Zap className="w-3.5 h-3.5" />
              Fallback provider was used for this session
            </div>
          )}
      </div>
    </Modal>
  )
}

function KV({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
        {label}
      </div>
      <div className="text-kb-text-primary font-mono">{value}</div>
    </div>
  )
}
