import { useState } from 'react'
import { ChevronDown, ChevronUp } from 'lucide-react'
import { MetricChart } from '@/components/shared/MetricChart'
import { KPI_COLOR } from '@/components/shared/kpi/MiniCharts'
import { liveProcess } from '@/utils/promql'
import { AutoGrid } from './AutoGrid'
import { compact, FAILURE_PALETTE, failuresOrZero, inInterval, useHealthVector, type ChartBase, type Row } from './healthShared'

// The API's own health in Administration › System › Health (doc #67, O2): its
// HTTP surface, its background jobs and its calls to VictoriaMetrics. Every
// series is the operator's — none carries a tenant or a cluster — written by
// internal/opsmetrics. Counters are read per interval with increase_pure,
// gauges through liveProcess.

const human = (s: string) => s.replace(/_/g, ' ')

const CODE_COLOR: Record<string, string> = {
  '1xx': '#06b6d4',
  '2xx': '#22c55e',
  '3xx': '#94a3b8',
  '4xx': '#f59e0b',
  '5xx': '#ef4056',
  '503': '#a855f7',
}
// 503 is apart from 5xx: the API answers it when a cluster is unreachable or a
// feature is not configured — a state, not the API failing.
const codeLabel = (code: string) => (code === '503' ? '503 unavailable' : code)
// One colour per caller of VictoriaMetrics, the same in every chart: when the
// store goes down every call fails the same way, and what tells the bars
// apart is who was calling.
const VM_CALLER_COLOR: Record<string, string> = {
  dashboards: '#3b82f6',
  kobi: '#22c55e',
  selfwrite: '#ef4056',
  agent_ingest: '#06b6d4',
  remote_write: '#ec4899',
  series_cap: '#f97316',
  freshness: '#14b8a6',
}

const HTTP_USED = `${inInterval('kubebolt_http_requests_total')} > 0`
const VM_USED = `${inInterval('kubebolt_vm_requests_total')} > 0`

const quantileInInterval = (q: number, family: string, by = '') =>
  `histogram_quantile(${q}, ${inInterval(`${family}_bucket`, by ? `le, ${by}` : 'le')}) and on (${by}) (${inInterval(`${family}_count`, by)} > 0)`

interface SectionCharts {
  charts: ChartBase
  perInterval: string
  perPoint: string
}

// ─── HTTP ───────────────────────────────────────────────────────────────

export function HttpCharts({ charts, perInterval, perPoint }: SectionCharts) {
  return (
    <AutoGrid minCol={300} allowed={[4, 2, 1]} fallback="grid-cols-1 lg:grid-cols-2">
      <MetricChart
        {...charts}
        sparse
        title="Requests by status"
        unit="count"
        chartType="bar"
        query={`${inInterval('kubebolt_http_requests_total', 'code')} > 0`}
        seriesLabel={(l) => codeLabel(l.code ?? 'requests')}
        seriesColor={(l) => CODE_COLOR[l.code ?? '']}
        emptyMessage="No requests in this window."
        emptyHint="Every request the API answered, by status class. 503 stands apart: a cluster unreachable or a feature not configured. A WebSocket or a Kobi chat counts once, when it ends."
        footnote={`kubebolt_http_requests_total by (code) · ${perInterval}`}
      />
      <MetricChart
        {...charts}
        sparse
        title="Server errors by route group"
        unit="count"
        chartType="bar"
        query={failuresOrZero('kubebolt_http_requests_total{code="5xx"}', 'group', 'group', HTTP_USED)}
        seriesLabel={(l) => (l.group === 'none' ? 'none' : human(l.group ?? 'requests'))}
        seriesColor={(l) => (l.group === 'none' ? '#94a3b8' : undefined)}
        emptyMessage="No requests in this window."
        emptyHint="The 5xx the API answered, by the part of the API that answered them; 503 (unavailable) is not one of them. An interval with requests and no 5xx reads as 0."
        footnote={`kubebolt_http_requests_total{code="5xx"} by (group) · ${perInterval}`}
      />
      <MetricChart
        {...charts}
        sparse
        title="Response time"
        unit="seconds"
        chartType="line"
        queries={[
          { query: quantileInInterval(0.95, 'kubebolt_http_request_seconds'), prefix: 'p95', accent: '#f59e0b' },
          { query: quantileInInterval(0.5, 'kubebolt_http_request_seconds'), prefix: 'p50', accent: '#94a3b8' },
        ]}
        seriesLabel={(_l, prefix) => prefix ?? 'latency'}
        emptyMessage="No timed requests in this window."
        emptyHint="Every request but the streams (WebSocket, terminal, port-forward, Kobi chat, MCP), each interval on its own. The table below splits it by route group."
        footnote={`kubebolt_http_request_seconds · ${perPoint}`}
      />
      <MetricChart
        {...charts}
        title="Open right now"
        unit="count"
        chartType="line"
        queries={[
          { query: `sum(${liveProcess('kubebolt_http_requests_in_flight')})`, prefix: 'requests in flight', accent: '#3b82f6' },
          { query: `sum(${liveProcess('kubebolt_ws_clients')})`, prefix: 'live-update browsers', accent: '#22c55e' },
        ]}
        seriesLabel={(_l, prefix) => prefix ?? 'open'}
        emptyMessage="No API replica reported in this window."
        emptyHint="Sampled with each 30-second push, across replicas: requests being answered (streams excluded) and browsers on the live-update WebSocket."
        footnote="kubebolt_http_requests_in_flight · kubebolt_ws_clients"
      />
    </AutoGrid>
  )
}

const byLabel = (rows: Row[] | undefined, label: string) =>
  new Map((rows ?? []).map((r) => [r.labels[label] ?? '', r.value]))

const pct = (part: number, whole: number) => (whole > 0 && part > 0 ? ` · ${((part / whole) * 100).toFixed(part / whole < 0.01 ? 2 : 1)}%` : '')

// The rows each table shows before it is expanded: the ten busiest route
// groups; the six jobs that need a look first.
const TOP_GROUPS = 10
const TOP_JOBS = 6

// RouteGroupsTable — the whole range per route group: what it served, how much
// failed, and how fast. The charts above show when; this shows where. The ten
// busiest groups show first; the rest open on demand.
export function RouteGroupsTable({ rangeMinutes }: { rangeMinutes: number }) {
  const [expanded, setExpanded] = useState(false)
  const w = `${rangeMinutes}m`
  const total = useHealthVector(`http-groups-${w}`, `sum by (group) (increase_pure(kubebolt_http_requests_total[${w}]))`)
  const c4 = useHealthVector(`http-4xx-${w}`, `sum by (group) (increase_pure(kubebolt_http_requests_total{code="4xx"}[${w}]))`)
  const c5 = useHealthVector(`http-5xx-${w}`, `sum by (group) (increase_pure(kubebolt_http_requests_total{code="5xx"}[${w}]))`)
  const p95 = useHealthVector(
    `http-p95-${w}`,
    `histogram_quantile(0.95, sum by (group, le) (increase_pure(kubebolt_http_request_seconds_bucket[${w}])))`,
  )
  const e4 = byLabel(c4.data, 'group')
  const e5 = byLabel(c5.data, 'group')
  const lat = byLabel(p95.data, 'group')
  const rows = (total.data ?? [])
    .filter((r) => r.value >= 0.5)
    .map((r) => ({ group: r.labels.group ?? '?', requests: r.value }))
    .sort((a, b) => b.requests - a.requests)

  if (rows.length === 0) {
    return <p className="text-xs text-kb-text-tertiary px-1">No requests in this window.</p>
  }
  const hidden = rows.length - TOP_GROUPS
  const shown = expanded || hidden <= 0 ? rows : rows.slice(0, TOP_GROUPS)
  return (
    <div className="rounded-lg border border-kb-border bg-kb-bg overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-[11px] uppercase tracking-wider text-kb-text-tertiary border-b border-kb-border">
            <th className="px-4 py-2 font-medium">Route group</th>
            <th className="px-3 py-2 font-medium text-right">Requests</th>
            <th className="px-3 py-2 font-medium text-right">4xx</th>
            <th className="px-3 py-2 font-medium text-right" title="503 — a cluster or a feature unavailable — is not counted">5xx</th>
            <th className="px-4 py-2 font-medium text-right" title="Streams are not timed">p95</th>
          </tr>
        </thead>
        <tbody>
          {shown.map((r) => {
            const n4 = Math.round(e4.get(r.group) ?? 0)
            const n5 = Math.round(e5.get(r.group) ?? 0)
            const p = lat.get(r.group)
            return (
              <tr key={r.group} className="border-b border-kb-border last:border-0">
                <td className="px-4 py-2 text-kb-text-primary">{human(r.group)}</td>
                <td className="px-3 py-2 text-right tabular-nums text-kb-text-secondary">{compact(Math.round(r.requests))}</td>
                <td className={`px-3 py-2 text-right tabular-nums ${n4 > 0 ? 'text-status-warn' : 'text-kb-text-tertiary'}`}>
                  {n4 > 0 ? `${compact(n4)}${pct(n4, r.requests)}` : '—'}
                </td>
                <td className={`px-3 py-2 text-right tabular-nums ${n5 > 0 ? 'text-status-error' : 'text-kb-text-tertiary'}`}>
                  {n5 > 0 ? `${compact(n5)}${pct(n5, r.requests)}` : '—'}
                </td>
                <td className="px-4 py-2 text-right tabular-nums text-kb-text-secondary">
                  {p !== undefined && Number.isFinite(p) ? seconds(p) : '—'}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      <ExpandRows hidden={hidden} top={TOP_GROUPS} expanded={expanded} onToggle={() => setExpanded((v) => !v)} noun={['route group', 'route groups']} />
    </div>
  )
}

// ExpandRows is the footer of a table that shows its first rows and folds the
// rest: «Show N more …» to open it, «Show the top N only» to fold it again.
function ExpandRows({ hidden, top, expanded, onToggle, noun }: {
  hidden: number
  top: number
  expanded: boolean
  onToggle: () => void
  noun: [string, string]
}) {
  if (hidden <= 0) return null
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-expanded={expanded}
      className="w-full flex items-center justify-center gap-1.5 px-4 py-2 border-t border-kb-border text-[11px] text-kb-text-tertiary hover:text-kb-text-primary hover:bg-kb-card-hover transition-colors"
    >
      {expanded ? (
        <>
          <ChevronUp className="w-3.5 h-3.5" />
          Show the top {top} only
        </>
      ) : (
        <>
          <ChevronDown className="w-3.5 h-3.5" />
          Show {hidden} more {hidden === 1 ? noun[0] : noun[1]}
        </>
      )}
    </button>
  )
}

function seconds(s: number): string {
  if (s < 1) return `${Math.round(s * 1000)} ms`
  return `${s.toFixed(s < 10 ? 2 : 1)} s`
}

// ─── Background jobs ────────────────────────────────────────────────────

const JOBS: Record<string, { label: string; what: string }> = {
  selfwrite: { label: 'Metrics push', what: "pushes the API's own series to VictoriaMetrics" },
  external_push: { label: 'External push', what: "sends the API's health series to the external watcher" },
  retention: { label: 'Retention', what: 'prunes history past its retention window' },
  findings_sweep: { label: 'Security findings sweep', what: 'reads the scanners in every connected cluster' },
  insight_expiry: { label: 'Insight expiry', what: 'closes firing insights nobody has seen for a while' },
  agent_registry_prune: { label: 'Agent registry prune', what: 'forgets agents disconnected past the horizon' },
  series_cap: { label: 'Active-series cap', what: 'refreshes the series count for the ingest cap' },
}

type JobStatus = { label: string; color: string; title: string; rank: number }

// jobStatus — late means no success for three intervals (two minutes at
// least, so a 30-second job is not late for one slow push).
export function duration(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${s}s`
}

export function jobStatus(ageSec: number | null, intervalSec: number, errors: number): JobStatus {
  const lateAfter = Math.max(3 * intervalSec, 120)
  const late = ageSec === null || ageSec > lateAfter
  if (late && errors > 0) return { label: 'Failing', color: KPI_COLOR.err, title: 'No success for three intervals, and runs are failing.', rank: 0 }
  if (late) return { label: 'Late', color: KPI_COLOR.err, title: 'No success for three intervals and no failed run either: the job has stopped running.', rank: 1 }
  if (errors > 0) return { label: 'Errors', color: KPI_COLOR.warn, title: 'Some runs in this window failed; the last success is recent.', rank: 2 }
  return { label: 'On time', color: KPI_COLOR.ok, title: 'Succeeded within its interval.', rank: 3 }
}

// JobsTable — every background job and whether it still succeeds on time. The
// ones in trouble sort first, so the six shown before expanding are never the
// healthy ones while a broken job waits folded below.
export function JobsTable({ rangeMinutes }: { rangeMinutes: number }) {
  const [expanded, setExpanded] = useState(false)
  const w = `${rangeMinutes}m`
  const intervals = useHealthVector('jobs-interval', 'max by (name) (max_over_time(kubebolt_job_interval_seconds[24h]))')
  const success = useHealthVector('jobs-success', 'max by (name) (max_over_time(kubebolt_job_last_success_timestamp_seconds[24h]))')
  // When each job was first declared in the window: one that has not succeeded
  // yet is measured from there, not shown late the moment the API starts.
  const declared = useHealthVector('jobs-declared', 'min by (name) (tfirst_over_time(kubebolt_job_interval_seconds[24h]))')
  const durations = useHealthVector('jobs-duration', `max by (name) (${liveProcess('kubebolt_job_last_duration_seconds')})`)
  const oks = useHealthVector(`jobs-ok-${w}`, `sum by (name) (increase_pure(kubebolt_job_runs_total{result="ok"}[${w}]))`)
  const errs = useHealthVector(`jobs-err-${w}`, `sum by (name) (increase_pure(kubebolt_job_runs_total{result="error"}[${w}]))`)
  // The job's label is "name": the self-push owns "job" (job="kubebolt-api").
  const last = byLabel(success.data, 'name')
  const since = byLabel(declared.data, 'name')
  const dur = byLabel(durations.data, 'name')
  const ok = byLabel(oks.data, 'name')
  const err = byLabel(errs.data, 'name')
  const now = Date.now() / 1000
  const rows = (intervals.data ?? [])
    .filter((r) => r.labels.name) // a series without the label predates it
    .map((r) => {
      const job = r.labels.name
      const ts = last.get(job)
      const age = ts !== undefined && ts > 0 ? now - ts : null
      const declaredAt = since.get(job)
      const waited = age ?? (declaredAt !== undefined ? now - declaredAt : null)
      const errors = Math.round(err.get(job) ?? 0)
      return { job, interval: r.value, age, errors, runs: Math.round(ok.get(job) ?? 0) + errors, dur: dur.get(job), status: jobStatus(waited, r.value, errors) }
    })
    .sort((a, b) => a.status.rank - b.status.rank || (JOBS[a.job]?.label ?? a.job).localeCompare(JOBS[b.job]?.label ?? b.job))

  if (rows.length === 0) {
    return <p className="text-xs text-kb-text-tertiary px-1">No background job reported in the last 24 hours.</p>
  }
  const hidden = rows.length - TOP_JOBS
  const shown = expanded || hidden <= 0 ? rows : rows.slice(0, TOP_JOBS)
  return (
    <div className="rounded-lg border border-kb-border bg-kb-bg overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-[11px] uppercase tracking-wider text-kb-text-tertiary border-b border-kb-border">
            <th className="px-4 py-2 font-medium">Job</th>
            <th className="px-3 py-2 font-medium">Status</th>
            <th className="px-3 py-2 font-medium text-right">Last success</th>
            <th className="px-3 py-2 font-medium text-right">Every</th>
            <th className="px-3 py-2 font-medium text-right" title="With several replicas, the slowest one's last run">Last run took</th>
            <th className="px-4 py-2 font-medium text-right">Runs · failed</th>
          </tr>
        </thead>
        <tbody>
          {shown.map((r) => (
            <tr key={r.job} className="border-b border-kb-border last:border-0">
              <td className="px-4 py-2">
                <div className="text-kb-text-primary">{JOBS[r.job]?.label ?? human(r.job)}</div>
                <div className="text-[11px] text-kb-text-tertiary">{JOBS[r.job]?.what ?? r.job}</div>
              </td>
              <td className="px-3 py-2">
                <span className="inline-flex items-center gap-1.5 text-[11px]" title={r.status.title}>
                  <span className="w-2 h-2 rounded-full shrink-0" style={{ background: r.status.color }} />
                  <span className="text-kb-text-secondary">{r.status.label}</span>
                </span>
              </td>
              <td className="px-3 py-2 text-right tabular-nums text-kb-text-secondary">
                {r.age !== null ? `${duration(r.age)} ago` : r.runs > 0 ? 'over 24h' : 'not yet'}
              </td>
              <td className="px-3 py-2 text-right tabular-nums text-kb-text-tertiary">{duration(r.interval)}</td>
              <td className="px-3 py-2 text-right tabular-nums text-kb-text-secondary">
                {r.dur !== undefined && Number.isFinite(r.dur) ? seconds(r.dur) : '—'}
              </td>
              <td className="px-4 py-2 text-right tabular-nums">
                <span className="text-kb-text-secondary">{compact(r.runs)}</span>
                <span className={r.errors > 0 ? 'text-status-error' : 'text-kb-text-tertiary'}> · {r.errors}</span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <ExpandRows hidden={hidden} top={TOP_JOBS} expanded={expanded} onToggle={() => setExpanded((v) => !v)} noun={['job', 'jobs']} />
    </div>
  )
}

// ─── Calls to VictoriaMetrics ───────────────────────────────────────────

export function VMCallsCharts({ charts, perInterval, perPoint }: SectionCharts) {
  return (
    <>
      <MetricChart
        {...charts}
        sparse
        title="Calls from the API"
        unit="count"
        chartType="bar"
        query={`${inInterval('kubebolt_vm_requests_total', 'caller')} > 0`}
        seriesLabel={(l) => human(l.caller ?? 'calls')}
        seriesColor={(l) => VM_CALLER_COLOR[l.caller ?? '']}
        emptyMessage="No calls to VictoriaMetrics in this window."
        emptyHint="Who asks: dashboards, Kobi, the metrics push, agent ingest, remote write, the series cap…"
        footnote={`kubebolt_vm_requests_total by (caller) · ${perInterval}`}
      />
      <MetricChart
        {...charts}
        sparse
        title="Failed calls"
        unit="count"
        chartType="bar"
        query={failuresOrZero('kubebolt_vm_requests_total{result!="ok"}', 'caller, result', 'result', VM_USED)}
        seriesLabel={(l) => (l.result === 'none' ? 'none' : `${human(l.caller ?? '')} · ${human(l.result ?? '')}`)}
        accents={FAILURE_PALETTE}
        seriesColor={(l) => (l.result === 'none' ? '#94a3b8' : undefined)}
        emptyMessage="No calls to VictoriaMetrics in this window."
        emptyHint="Timeouts, errors and 4xx/5xx answers, one colour per caller and result. An interval with calls and no failures reads as 0."
        footnote={`kubebolt_vm_requests_total{result!="ok"} by (caller, result) · ${perInterval}`}
      />
      <MetricChart
        {...charts}
        sparse
        title="Response time, p95"
        unit="seconds"
        chartType="line"
        accents={['#3b82f6', '#a855f7', '#22c55e', '#f59e0b', '#06b6d4']}
        query={quantileInInterval(0.95, 'kubebolt_vm_request_seconds', 'op')}
        seriesLabel={(l) => human(l.op ?? 'calls')}
        emptyMessage="No calls to VictoriaMetrics in this window."
        emptyHint="Until the response headers, by operation, each interval on its own."
        footnote={`kubebolt_vm_request_seconds by (op) · ${perPoint}`}
      />
    </>
  )
}
