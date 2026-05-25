import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Activity, AlertCircle, CheckCircle2, Database, Gauge, KeyRound, Network, Power, Server, Timer } from 'lucide-react'
import { api, type AdminAgentEntry, type Tenant } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { MetricChart, METRIC_ACCENTS } from '@/components/shared/MetricChart'
import { RangeSelector, OVERVIEW_RANGE_OPTIONS } from '@/components/shared/RangeSelector'

// IngestActivityPage answers "what is my ingest doing right now?" —
// spec #09 V2 Item 5b. The companion piece to Item 5a's Prometheus
// integration card: 5a tells you the receiver is connected, this page
// tells you what it's actually receiving per tenant.
//
// Layout: one card per tenant. The default tenant always appears
// first (it's the single-tenant OSS case); future multi-tenant
// Enterprise renders the rest in alphabetical order under it.
//
// Inside each card:
//   - Header: tenant name + samples/sec current + active series / cap
//   - Sparkline (1h, 5m step) with two series: agent gRPC vs remote_write
//   - Stream + request status chips (1h roll-up)
//   - Heartbeat list: currently-connected agents for this tenant
//
// Auto-refresh every 30s — matches the cardinality tracker's poll
// cadence so the active-series gauge stays in step with backend state.

const POLL_INTERVAL_MS = 30_000

// Top-of-page header shows when the page last refreshed so operators
// can tell at a glance whether the numbers are stale (e.g., when the
// 30s poll fails or the tab is in the background).
function formatAge(unixMs: number): string {
  const secs = Math.max(0, Math.floor((Date.now() - unixMs) / 1000))
  if (secs < 5) return 'just now'
  if (secs < 60) return `${secs}s ago`
  return `${Math.floor(secs / 60)}m ago`
}

export function IngestActivityPage() {
  // Page-level range state — drives both the sparkline (via MetricChart's
  // controlledRangeMinutes) and the chip increase() windows. 60m default
  // matches the original "last 1h" framing operators saw before the
  // selector landed. RangeSelector lets them widen to 24h or narrow to 5m.
  const [rangeMinutes, setRangeMinutes] = useState(60)

  const { data: tenants, isLoading: tenantsLoading, error: tenantsError } = useQuery({
    queryKey: ['admin-tenants'],
    queryFn: api.listTenants,
    refetchInterval: POLL_INTERVAL_MS,
  })

  const { data: agents } = useQuery({
    queryKey: ['admin-agents'],
    queryFn: api.adminListAgents,
    refetchInterval: POLL_INTERVAL_MS,
  })

  // Default tenant first, the rest A-Z. The OSS install only has the
  // "default" tenant; this ordering is mostly future-proofing for the
  // multi-tenant Enterprise UX.
  const sortedTenants = useMemo(() => {
    if (!tenants) return []
    return [...tenants].sort((a, b) => {
      if (a.name === 'default') return -1
      if (b.name === 'default') return 1
      return a.name.localeCompare(b.name)
    })
  }, [tenants])

  if (tenantsLoading) return <LoadingSpinner />
  if (tenantsError) {
    return (
      <div className="rounded-lg border border-status-error-dim bg-status-error-dim/30 p-4 text-xs text-status-error">
        Failed to load tenants: {(tenantsError as Error).message}
      </div>
    )
  }

  return (
    <div className="pb-24 space-y-5">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <Activity className="w-5 h-5" />
            Ingest activity
          </h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5 max-w-2xl">
            Live view of what each tenant is ingesting via the two paths: kubebolt-agent gRPC
            channel and Prom remote_write receiver. Refreshes every 30s. Empty cards mean no
            activity in the selected window — see{' '}
            <a
              href="https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/prometheus.md"
              target="_blank"
              rel="noopener noreferrer"
              className="text-kb-accent underline"
            >
              Prometheus integration docs
            </a>{' '}
            if you expected activity and don't see it.
          </p>
        </div>
        <div className="flex items-center gap-3 shrink-0 mt-1">
          <RangeSelector value={rangeMinutes} onChange={setRangeMinutes} />
          <div className="text-[10px] font-mono text-kb-text-tertiary">auto-refresh 30s</div>
        </div>
      </header>

      {sortedTenants.length === 0 && (
        <div className="rounded-lg border border-kb-border bg-kb-card p-8 text-xs text-kb-text-tertiary text-center">
          No tenants configured. The backend should auto-seed a "default" tenant on first boot —
          if you're seeing this, check BoltDB persistence is enabled.
        </div>
      )}

      <div className="space-y-5">
        {sortedTenants.map((t) => (
          <TenantIngestCard
            key={t.id}
            tenant={t}
            agents={(agents ?? []).filter((a) => a.tenantId === t.id)}
            rangeMinutes={rangeMinutes}
          />
        ))}
      </div>
    </div>
  )
}

// ─── Per-tenant card ──────────────────────────────────────────────────

interface TenantIngestCardProps {
  tenant: Tenant
  agents: AdminAgentEntry[]
  // Selected window in minutes — drives the chip increase() windows
  // and the sparkline's controlled range. Headline samples/sec stays
  // at rate[5m] regardless because "current rate" is a smoothing
  // signal, not a range-dependent aggregation.
  rangeMinutes: number
}

function TenantIngestCard({ tenant, agents, rangeMinutes }: TenantIngestCardProps) {
  // Instant queries powering the chips + gauge. Each returns a single
  // number; we render the result inline. PromQL functions used:
  //   - sum(rate(...[5m])) for the headline (fixed 5m smoothing)
  //   - sum by (status) (increase(...[<window>])) for the chips,
  //     window comes from the RangeSelector
  //   - the activeSeries gauge is already a per-tenant value, no agg
  //     needed beyond the tenant label match.
  const tenantLabel = `tenant_id="${tenant.id}"`
  // PromQL duration string for the selected window — used in
  // increase() and the chip header copy. Pulled from the same
  // OVERVIEW_RANGE_OPTIONS table the RangeSelector uses, so the chip
  // header label always matches the chart's range chip.
  const rangeLabel =
    OVERVIEW_RANGE_OPTIONS.find((o) => o.minutes === rangeMinutes)?.label ?? `${rangeMinutes}m`

  // Headline samples/sec — sum of both paths.
  const { data: samplesPerSec } = useQuery({
    queryKey: ['ingest-activity', tenant.id, 'samples-per-sec'],
    queryFn: () =>
      api.adminQueryMetrics({
        query: `sum(rate(kubebolt_agent_grpc_samples_received_total{${tenantLabel}}[5m])) + sum(rate(kubebolt_prom_write_samples_accepted_total{${tenantLabel}}[5m]))`,
      }),
    refetchInterval: POLL_INTERVAL_MS,
  })

  // Active series — current cardinality count vs the per-tenant cap.
  // The cap comes from the tenant's effective limits (already exposed
  // by /admin/tenants/:id/limits — but for the V1 page, we fetch the
  // gauge directly and show the cap as a tooltip rather than a
  // hardcoded ceiling on the progress bar's max.
  const { data: activeSeries } = useQuery({
    queryKey: ['ingest-activity', tenant.id, 'active-series'],
    queryFn: () =>
      api.adminQueryMetrics({
        query: `kubebolt_prom_write_active_series{${tenantLabel}}`,
      }),
    refetchInterval: POLL_INTERVAL_MS,
  })

  // Per-tenant cap from the tenant-limits endpoint. Used to compute
  // the gauge's percentage. nil-tolerant: when the cap isn't fetched
  // yet, the gauge shows the raw count without a "/cap" suffix.
  const { data: limits } = useQuery({
    queryKey: ['admin-tenant-limits', tenant.id],
    queryFn: () => api.getTenantLimits(tenant.id),
    staleTime: 60_000, // limits change rarely; cache aggressively
  })

  // Stream lifecycle chips: connections / disconnects in the selected
  // window. increase() captures the count of events even when both go
  // to 0 (a quiet hour). status="auth_rejected" is RESERVED but not yet
  // wired backend-side — query returns 0 until that lands.
  const { data: streamStats } = useQuery({
    queryKey: ['ingest-activity', tenant.id, 'stream-stats', rangeLabel],
    queryFn: () =>
      api.adminQueryMetrics({
        query: `sum by (status) (increase(kubebolt_agent_grpc_streams_total{${tenantLabel}}[${rangeLabel}]))`,
      }),
    refetchInterval: POLL_INTERVAL_MS,
  })

  // Remote_write request chips: outcome distribution over the window.
  // The status label has 10 possible values; we group "everything not
  // accepted" into a single "rejected" bucket plus pull out the
  // operationally important ones (auth, rate_limit, cardinality).
  const { data: requestStats } = useQuery({
    queryKey: ['ingest-activity', tenant.id, 'request-stats', rangeLabel],
    queryFn: () =>
      api.adminQueryMetrics({
        query: `sum by (status) (increase(kubebolt_prom_write_requests_total{${tenantLabel}}[${rangeLabel}]))`,
      }),
    refetchInterval: POLL_INTERVAL_MS,
  })

  const samplesPerSecValue = parseFirstResultValue(samplesPerSec)
  const activeSeriesValue = parseFirstResultValue(activeSeries)
  const cap = limits?.effective.maxActiveSeries
  const seriesPercent =
    cap && cap > 0 && activeSeriesValue !== null
      ? Math.min(100, Math.round((activeSeriesValue / cap) * 100))
      : null

  // Empty-state detection: no samples/sec AND no agents AND no recent
  // activity in either status group. Show a quieter card with the docs
  // link rather than a sea of zero chips.
  const hasActivity =
    (samplesPerSecValue !== null && samplesPerSecValue > 0) ||
    agents.length > 0 ||
    hasNonZeroStats(streamStats) ||
    hasNonZeroStats(requestStats)

  return (
    <section className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
      {/* Header */}
      <header className="px-5 py-4 border-b border-kb-border flex items-start justify-between gap-4 flex-wrap">
        <div className="flex items-start gap-3 min-w-0">
          <div className="mt-0.5 shrink-0">
            <Server className="w-4 h-4 text-kb-accent" />
          </div>
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-kb-text-primary">
              {tenant.name}
              {tenant.name === 'default' && (
                <span className="ml-2 px-1.5 py-0.5 rounded-full text-[10px] font-mono uppercase tracking-wider bg-kb-elevated text-kb-text-tertiary">
                  Default
                </span>
              )}
            </h2>
            <p className="text-[11px] text-kb-text-tertiary mt-0.5 font-mono truncate" title={tenant.id}>
              {tenant.id}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-5 shrink-0">
          <HeadlineStat
            label="samples/sec"
            value={samplesPerSecValue !== null ? formatRate(samplesPerSecValue) : '—'}
            icon={<Activity className="w-3 h-3" />}
          />
          <HeadlineStat
            label={cap ? `active series / ${formatInt(cap)}` : 'active series'}
            value={activeSeriesValue !== null ? formatInt(activeSeriesValue) : '—'}
            icon={<Database className="w-3 h-3" />}
            extra={
              seriesPercent !== null ? (
                <span
                  className={
                    seriesPercent >= 90
                      ? 'ml-2 text-[10px] text-status-error font-mono'
                      : seriesPercent >= 70
                        ? 'ml-2 text-[10px] text-status-warn font-mono'
                        : 'ml-2 text-[10px] text-kb-text-tertiary font-mono'
                  }
                >
                  {seriesPercent}%
                </span>
              ) : null
            }
          />
        </div>
      </header>

      {/* Body */}
      {!hasActivity ? (
        <EmptyTenantState />
      ) : (
        <div className="px-5 py-4 space-y-5">
          {/* Sparkline — two series side by side */}
          <div>
            <MetricChart
              title={`Samples per second (last ${rangeLabel})`}
              icon={<Network className="w-4 h-4" />}
              unit="count"
              // Spec #09 V2 Item 5b — these are tenant-scoped backend
              // observability metrics that don't carry a cluster_id
              // label; route through the admin PromQL endpoint that
              // bypasses scopeQueryByCluster.
              bypassClusterScope
              queries={[
                {
                  query: `sum(rate(kubebolt_agent_grpc_samples_received_total{${tenantLabel}}[5m]))`,
                  prefix: 'agent (gRPC)',
                },
                {
                  query: `sum(rate(kubebolt_prom_write_samples_accepted_total{${tenantLabel}}[5m]))`,
                  prefix: 'remote_write',
                },
              ]}
              accents={METRIC_ACCENTS.networkRxTx}
              chartType="area"
              showStats={false}
              height={160}
              controlledRangeMinutes={rangeMinutes}
            />
          </div>

          {/* Chips row */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <StreamLifecycleChips stats={streamStats} rangeLabel={rangeLabel} />
            <RemoteWriteRequestChips stats={requestStats} rangeLabel={rangeLabel} />
          </div>

          {/* Heartbeat list */}
          <HeartbeatList agents={agents} />
        </div>
      )}
    </section>
  )
}

// ─── Sub-components ────────────────────────────────────────────────────

function HeadlineStat({
  label,
  value,
  icon,
  extra,
}: {
  label: string
  value: string
  icon: React.ReactNode
  extra?: React.ReactNode
}) {
  return (
    <div className="text-right">
      <div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary flex items-center gap-1 justify-end">
        {icon}
        {label}
      </div>
      <div className="text-base font-mono font-semibold text-kb-text-primary tabular-nums">
        {value}
        {extra}
      </div>
    </div>
  )
}

function EmptyTenantState() {
  return (
    <div className="px-5 py-8 text-center">
      <div className="text-xs text-kb-text-tertiary">
        No ingest activity in the last hour.
      </div>
      <div className="text-[11px] text-kb-text-tertiary mt-2 max-w-md mx-auto leading-relaxed">
        If this tenant should be active, give the backend a few seconds after startup to ship
        its first self-write to VM. See{' '}
        <a
          href="https://github.com/clm-cloud-solutions/kubebolt/blob/main/docs/integrations/prometheus.md"
          target="_blank"
          rel="noopener noreferrer"
          className="underline"
        >
          Prometheus integration docs
        </a>{' '}
        for the metric reference.
      </div>
    </div>
  )
}

function StreamLifecycleChips({
  stats,
  rangeLabel,
}: {
  stats: ReturnType<typeof Object>
  rangeLabel: string
}) {
  const byStatus = vectorByStatus(stats)
  const connected = byStatus['connected'] ?? 0
  const disconnected = byStatus['disconnected'] ?? 0
  const authRejected = byStatus['auth_rejected'] ?? 0
  return (
    <div className="rounded-lg border border-kb-border bg-kb-bg p-3">
      <div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary mb-2 flex items-center gap-1">
        <Power className="w-3 h-3" />
        gRPC stream events (last {rangeLabel})
      </div>
      <div className="flex flex-wrap gap-2">
        <Chip label="connected" count={connected} variant="ok" />
        <Chip label="disconnected" count={disconnected} variant="muted" />
        {authRejected > 0 && <Chip label="auth rejected" count={authRejected} variant="error" />}
      </div>
    </div>
  )
}

function RemoteWriteRequestChips({
  stats,
  rangeLabel,
}: {
  stats: ReturnType<typeof Object>
  rangeLabel: string
}) {
  const byStatus = vectorByStatus(stats)
  const accepted = byStatus['accepted'] ?? 0
  const authRejected = byStatus['auth'] ?? 0
  const rateLimited = byStatus['rate_limit'] ?? 0
  const cardinality = byStatus['cardinality'] ?? 0
  const malformed = byStatus['malformed'] ?? 0
  // Other rejection categories grouped under "other" so the chip row
  // doesn't sprawl. body_size / tenant_id_mismatch / tenant_id_missing
  // / injection_failed / upstream_error all fall here.
  const otherRejected =
    (byStatus['body_size'] ?? 0) +
    (byStatus['tenant_id_mismatch'] ?? 0) +
    (byStatus['tenant_id_missing'] ?? 0) +
    (byStatus['injection_failed'] ?? 0) +
    (byStatus['upstream_error'] ?? 0)
  return (
    <div className="rounded-lg border border-kb-border bg-kb-bg p-3">
      <div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary mb-2 flex items-center gap-1">
        <Network className="w-3 h-3" />
        remote_write requests (last {rangeLabel})
      </div>
      <div className="flex flex-wrap gap-2">
        <Chip label="accepted" count={accepted} variant="ok" />
        {authRejected > 0 && <Chip label="auth" count={authRejected} variant="error" />}
        {rateLimited > 0 && <Chip label="rate-limited" count={rateLimited} variant="warn" />}
        {cardinality > 0 && <Chip label="cardinality-capped" count={cardinality} variant="warn" />}
        {malformed > 0 && <Chip label="malformed" count={malformed} variant="warn" />}
        {otherRejected > 0 && <Chip label="other rejected" count={otherRejected} variant="muted" />}
      </div>
    </div>
  )
}

function Chip({
  label,
  count,
  variant,
}: {
  label: string
  count: number
  variant: 'ok' | 'warn' | 'error' | 'muted'
}) {
  const colorClass =
    variant === 'ok'
      ? 'bg-status-ok-dim border-status-ok-dim text-status-ok'
      : variant === 'warn'
        ? 'bg-status-warn-dim border-status-warn-dim text-status-warn'
        : variant === 'error'
          ? 'bg-status-error-dim border-status-error-dim text-status-error'
          : 'bg-kb-elevated border-kb-border text-kb-text-tertiary'
  return (
    <span
      className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md border text-[11px] font-mono ${colorClass}`}
    >
      <span className="font-semibold tabular-nums">{formatInt(count)}</span>
      <span>{label}</span>
    </span>
  )
}

function HeartbeatList({ agents }: { agents: AdminAgentEntry[] }) {
  if (agents.length === 0) {
    return (
      <div className="text-[11px] text-kb-text-tertiary italic">
        No gRPC agents connected for this tenant. Remote_write-only ingest (vmagent, external Prom)
        won't appear here.
      </div>
    )
  }
  // Sort by recency (newer connections first). Cap at 10 — beyond that
  // the table starts to crowd the card; a future enhancement could
  // expand into a modal.
  const sorted = [...agents].sort((a, b) => b.connectedAt - a.connectedAt).slice(0, 10)
  const nowMs = Date.now()
  return (
    <div>
      <div className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary mb-2 flex items-center gap-1">
        <Server className="w-3 h-3" />
        Connected agents ({agents.length})
      </div>
      <div className="border border-kb-border rounded-lg overflow-hidden">
        <table className="w-full text-[11px]">
          <thead className="bg-kb-elevated">
            <tr>
              <th className="px-3 py-1.5 text-left font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                Node
              </th>
              <th className="px-3 py-1.5 text-left font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                Agent ID
              </th>
              <th className="px-3 py-1.5 text-left font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                Auth
              </th>
              <th className="px-3 py-1.5 text-right font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                Connected
              </th>
            </tr>
          </thead>
          <tbody className="divide-y divide-kb-border">
            {sorted.map((a) => (
              <tr key={`${a.clusterId}/${a.agentId}`} className="hover:bg-kb-card-hover">
                <td className="px-3 py-1.5 font-mono text-kb-text-primary truncate max-w-[160px]" title={a.nodeName}>
                  {a.nodeName || '—'}
                </td>
                <td className="px-3 py-1.5 font-mono text-kb-text-tertiary truncate max-w-[140px]" title={a.agentId}>
                  {a.agentId.slice(0, 12)}…
                </td>
                <td className="px-3 py-1.5 font-mono text-kb-text-secondary text-[10px]">
                  {a.authMode || 'disabled'}
                </td>
                <td className="px-3 py-1.5 font-mono text-kb-text-secondary text-right tabular-nums">
                  {formatAge(a.connectedAt * 1000)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {agents.length > 10 && (
          <div className="px-3 py-1.5 border-t border-kb-border bg-kb-elevated text-[10px] font-mono text-kb-text-tertiary text-right">
            +{agents.length - 10} more
          </div>
        )}
      </div>
    </div>
  )
}

// ─── Helpers ──────────────────────────────────────────────────────────

// PromVectorResponse shape: { status, data: { resultType, result: [{metric, value: [ts, val]}] } }
// parseFirstResultValue returns the numeric value of the first entry, or null
// when the response is empty / errored — what the dashboard treats as "no data".
function parseFirstResultValue(resp: unknown): number | null {
  if (!resp || typeof resp !== 'object') return null
  const data = (resp as { data?: { result?: Array<{ value?: [number, string] }> } }).data
  const first = data?.result?.[0]
  if (!first?.value) return null
  const v = parseFloat(first.value[1])
  return Number.isFinite(v) ? v : null
}

// vectorByStatus reduces a vector response (one entry per status label
// value) into a status→count map. Used by the chip components.
function vectorByStatus(resp: unknown): Record<string, number> {
  const out: Record<string, number> = {}
  if (!resp || typeof resp !== 'object') return out
  const data = (resp as { data?: { result?: Array<{ metric?: Record<string, string>; value?: [number, string] }> } }).data
  for (const r of data?.result ?? []) {
    const status = r.metric?.status
    if (!status || !r.value) continue
    const v = parseFloat(r.value[1])
    if (Number.isFinite(v)) out[status] = v
  }
  return out
}

function hasNonZeroStats(resp: unknown): boolean {
  const map = vectorByStatus(resp)
  return Object.values(map).some((v) => v > 0)
}

function formatRate(v: number): string {
  if (v < 1) return v.toFixed(2)
  if (v < 100) return v.toFixed(1)
  return formatInt(v)
}

function formatInt(v: number): string {
  return new Intl.NumberFormat('en-US').format(Math.round(v))
}
