import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { collapsePodToWorkload } from '@/utils/promql'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { BarList, EventTrack, KPI_COLOR, Sparkline, SplitBar } from '@/components/shared/kpi/MiniCharts'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'

// GoldenSignalsStrip — the scan layer above Reliability's panels, in the
// site's card anatomy (KpiCard), the same family as the Overview's row:
// Error rate 5xx (the traffic split by status class) · Latency (this window
// against the previous one) · Throughput (its curve over the range) · L4
// drops (where they land). Same Hubble series the detail panels consume;
// the strip only aggregates them cluster-wide.
//
// BASELINE SEMANTICS (decision 2026-07-16): every delta compares
// against the PREVIOUS WINDOW OF THE SAME LENGTH immediately before
// the selected range (PromQL `offset`), not "same time yesterday" —
// for ranges beyond 24h a same-window comparison is the natural
// read, and for short ranges it answers the operator's actual
// question: "is this getting worse right now?".
//
// LATENCY IS AVG, NOT P99: the agent ships latency as sum+count
// (no histogram buckets yet), so a true p99 is not computable. The
// card says "avg" — same honest-labeling rule TopLatencyWorkloads
// documents. When the agent grows buckets, flip this to
// histogram_quantile and relabel.

interface Props {
  rangeMinutes: number
}

const REQS = `pod_flow_http_requests_total{source="hubble"}`
const LAT_SUM = `pod_flow_http_latency_seconds_sum{source="hubble"}`
const LAT_COUNT = `pod_flow_http_latency_seconds_count{source="hubble"}`

// 5xx is ALWAYS red when meaningfully present — class identity, not
// severity tiers. Every other surface (error-rate chart, hotspots,
// status distributions) draws 5xx in red and 4xx in amber; an amber
// middle band here made the same signal read as two different
// colors across cards. Below the floor it's noise → ok green.
const ERR_MEANINGFUL_PCT = 0.1
// Throughput/latency shifts flagged beyond ±10% vs previous window.
const DELTA_NOTABLE = 0.1

const KPI_MIN_WIDTH = 280

// Sparkline sampling — ~24 points across the range, as CapacityStrip.
const SPARK_POINTS = 24

export function GoldenSignalsStrip({ rangeMinutes }: Props) {
  const w = `${rangeMinutes}m`
  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()

  // One batched fetch: 7 instant queries (3 signals × now/previous +
  // hottest-latency attribution). Instant queries are cheap; the
  // panels below fire far heavier range queries.
  const q = useQuery({
    queryKey: ['reliability', 'golden-signals', rangeMinutes],
    queryFn: async () => {
      const errExpr = (offset: string) =>
        `100 * sum(rate(${withClass(REQS, 'server_err')}[${w}]${offset})) / clamp_min(sum(rate(${REQS}[${w}]${offset})), 1e-9)`
      const rpsExpr = (offset: string) => `sum(rate(${REQS}[${w}]${offset}))`
      const latExpr = (offset: string) =>
        `1000 * sum(rate(${LAT_SUM}[${w}]${offset})) / clamp_min(sum(rate(${LAT_COUNT}[${w}]${offset})), 1e-9)`
      const dropsExpr = `sum(increase(pod_flow_events_total{source="hubble", verdict="dropped"}[${w}]))`
      // Hottest workload by avg latency — attribution for the card's
      // sub-line. Same collapse transform the latency panel uses so
      // both name the same workload. The collapse wraps the rate()
      // (label_replace over an instant vector — a range selector
      // can't apply to a function result), and topk ranks the
      // QUOTIENT so the winner is highest-latency, not highest-traffic.
      const hotExpr = [
        `topk(1,`,
        `  (1000 * sum by (workload) (${collapsePodToWorkload(`rate(${LAT_SUM}[${w}])`)}))`,
        `  / on(workload)`,
        `  clamp_min(sum by (workload) (${collapsePodToWorkload(`rate(${LAT_COUNT}[${w}])`)}), 1e-9)`,
        `)`,
      ].join(' ')
      // The traffic by status class — the error card's split bar.
      const classExpr = `sum by (status_class) (rate(${REQS}[${w}]))`
      // Where the drops land — the same destination collapse NetworkDrops
      // uses, so both name the same workloads.
      const dropDstExpr = `topk(3, sum by (destination_workload) (${collapsePodToWorkload(
        `increase(pod_flow_events_total{source="hubble", verdict="dropped"}[${w}])`,
        'destination_pod',
        'destination_workload',
      )}))`
      const [errNow, errPrev, rpsNow, rpsPrev, latNow, latPrev, drops, hot, classes, dropDst] = await Promise.all([
        scalar(errExpr('')),
        scalar(errExpr(` offset ${w}`)),
        scalar(rpsExpr('')),
        scalar(rpsExpr(` offset ${w}`)),
        scalar(latExpr('')),
        scalar(latExpr(` offset ${w}`)),
        scalar(dropsExpr),
        labeled(hotExpr, 'workload'),
        labeledAll(classExpr, 'status_class'),
        labeledAll(dropDstExpr, 'destination_workload'),
      ])
      return { errNow, errPrev, rpsNow, rpsPrev, latNow, latPrev, drops, hot, classes, dropDst }
    },
    refetchInterval: 30_000,
    retry: false,
  })

  // Throughput over the range — the one signal whose shape matters more
  // than its window-over-window delta.
  const step = Math.max(15, Math.round((rangeMinutes * 60) / SPARK_POINTS))
  const rpsQ = useQuery({
    queryKey: ['reliability', 'golden-rps-trend', rangeMinutes],
    queryFn: () => {
      const end = Math.floor(Date.now() / 1000)
      return api.queryMetricsRange({
        query: `sum(rate(${REQS}[${Math.max(60, step)}s]))`,
        start: end - rangeMinutes * 60,
        end,
        step: `${step}s`,
      })
    },
    refetchInterval: 30_000,
    retry: false,
  })
  const rpsSeries = (rpsQ.data?.data?.result?.[0]?.values ?? [])
    .map((p: [number, string]) => parseFloat(p[1]))
    .filter((v: number) => Number.isFinite(v))

  const d = q.data
  const errBad = d?.errNow != null && d.errNow >= ERR_MEANINGFUL_PCT
  const latDelta = relDelta(d?.latNow, d?.latPrev)
  const rpsDelta = relDelta(d?.rpsNow, d?.rpsPrev)
  const errDelta = relDelta(d?.errNow, d?.errPrev)
  // Empty increase() result = no dropped-flow series in range. The
  // strip only renders when Hubble is shipping, so "no series" IS
  // zero drops, not missing data — showing "—" here read as broken.
  const drops = d ? Math.round(d.drops ?? 0) : null
  const rangeLabel = formatRange(rangeMinutes)
  const now = Date.now() / 1000

  // Status classes folded into the three the bar shows.
  const cls = { ok: 0, client: 0, server: 0 }
  for (const c of d?.classes ?? []) {
    if (c.name === 'server_err') cls.server += c.value
    else if (c.name === 'client_err') cls.client += c.value
    else cls.ok += c.value
  }
  const clsTotal = cls.ok + cls.client + cls.server
  const pct = (v: number) => (clsTotal > 0 ? (v / clsTotal) * 100 : 0)
  // No 5xx series at all makes the ratio query come back empty; with traffic
  // flowing that is a 0 %, not an unknown — the class split already says so.
  const errNow = d?.errNow ?? (clsTotal > 0 ? pct(cls.server) : null)
  const fmtShare = (v: number) => (v === 0 ? '0' : formatPct(v))
  const dropRows = (d?.dropDst ?? []).filter((r) => r.value >= 0.5)

  return (
    <div
      ref={gridRef}
      className={`grid gap-4 ${gridWidth ? '' : 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'}`}
      style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, KPI_MIN_WIDTH, 16, [4, 2, 1])}, minmax(0, 1fr))` } : undefined}
    >
      <KpiCard
        primary
        alert={errBad ? 'crit' : undefined}
        value={errNow != null ? fmtShare(errNow) : '—'}
        unit="% 5xx"
        description={
          errDelta != null && d?.errPrev != null
            ? `${deltaArrow(errDelta)} vs ${formatPct(d.errPrev)}% the window before`
            : errBad
              ? `server errors over the last ${rangeLabel}`
              : `no meaningful server errors · ${rangeLabel}`
        }
        viz={
          clsTotal > 0 ? (
            <SplitBar
              parts={[
                { value: cls.ok, color: KPI_COLOR.ok },
                { value: cls.client, color: KPI_COLOR.warn },
                { value: cls.server, color: KPI_COLOR.err },
              ]}
              left={`ok ${fmtShare(pct(cls.ok))}%`}
              right={
                <>
                  <span style={{ color: cls.client > 0 ? KPI_COLOR.warn : undefined }}>4xx {fmtShare(pct(cls.client))}%</span>
                  {' · '}
                  <span style={{ color: errBad ? KPI_COLOR.err : undefined }}>5xx {fmtShare(pct(cls.server))}%</span>
                </>
              }
            />
          ) : undefined
        }
        caption="Hubble L7 · every observed request"
        info={
          <>
            <TooltipHeader>Error rate · 5xx</TooltipHeader>
            <TooltipRow color="#ef4056" label="What" value="server errors" />
            <TooltipNote>
              Share of HTTP requests returning 5xx (server-side failures) across all
              Hubble-observed L7 traffic. The bar splits the same traffic into ok, 4xx
              (caller errors) and 5xx. The delta compares the previous window of the same
              length.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        alert={latDelta != null && latDelta > DELTA_NOTABLE ? 'warn' : undefined}
        value={d?.latNow != null ? formatMs(d.latNow) : '—'}
        unit="ms avg"
        description={latencySentence(latDelta)}
        viz={
          d?.latNow != null && d?.latPrev != null ? (
            <BarList
              color={latDelta != null && latDelta > DELTA_NOTABLE ? KPI_COLOR.warn : KPI_COLOR.ok}
              rows={[
                { label: `last ${rangeLabel}`, value: d.latNow, display: `${formatMs(d.latNow)} ms` },
                { label: 'window before', value: d.latPrev, display: `${formatMs(d.latPrev)} ms` },
              ]}
            />
          ) : undefined
        }
        caption={d?.hot ? `hottest · ${d.hot.name} ${Math.round(d.hot.value)} ms` : 'cluster-wide mean, not a p99'}
        info={
          <>
            <TooltipHeader right="not p99">Latency · avg</TooltipHeader>
            <TooltipRow color="#f5a623" label="Metric" value="mean, cluster-wide" />
            <TooltipNote>
              Average HTTP response time across all L7 flows — the sum of request
              durations over the count. This is a MEAN, not a p99: the agent ships
              latency as sum + count without histogram buckets, so a true percentile
              isn't computable yet. A consistently high average still points at a real
              problem; a single outlier can pull it up.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        value={d?.rpsNow != null ? formatRps(d.rpsNow) : '—'}
        unit="req/s"
        description={
          rpsDelta == null
            ? `over the last ${rangeLabel}`
            : Math.abs(rpsDelta) <= DELTA_NOTABLE
              ? 'steady vs the window before'
              : `${deltaArrow(rpsDelta)} ${Math.round(Math.abs(rpsDelta) * 100)}% vs the window before`
        }
        viz={
          rpsSeries.length >= 2 ? (
            <Sparkline
              fromZero
              values={rpsSeries}
              left={`${rangeLabel} · ${formatRps(Math.min(...rpsSeries))}–${formatRps(Math.max(...rpsSeries))} req/s`}
            />
          ) : undefined
        }
        caption="every request Hubble saw, all workloads"
      />
      <KpiCard
        alert={drops != null && drops > 0 ? 'warn' : undefined}
        value={drops ?? '—'}
        unit={drops === 1 ? 'L4 drop' : 'L4 drops'}
        description={drops != null && drops > 0 ? `in the last ${rangeLabel}` : `none in the last ${rangeLabel}`}
        viz={
          dropRows.length > 0 ? (
            <BarList
              color={KPI_COLOR.warn}
              rows={dropRows.map((r) => ({ label: `→ ${r.name}`, value: r.value, display: Math.round(r.value) }))}
            />
          ) : (
            <EventTrack events={[]} from={now - rangeMinutes * 60} to={now} left={`${rangeLabel} ago`} />
          )
        }
        caption={drops != null && drops > 0 ? 'NetworkPolicy denials or connection refused' : 'Cilium verdict=dropped'}
        info={
          <>
            <TooltipHeader>L4 drops</TooltipHeader>
            <TooltipRow color="#f5a623" label="Source" value="Hubble verdict=dropped" />
            <TooltipNote>
              Count of L4 flows Cilium DROPPED in this range — most are NetworkPolicy
              denials, but connection-refused and host-firewall blocks land here too.
              This is the early-warning channel the HTTP panels miss: dropped traffic
              never reaches the application layer to become a 4xx/5xx. The bars name the
              workloads the drops were headed for.
            </TooltipNote>
          </>
        }
      />
    </div>
  )
}

function withClass(metric: string, statusClass: string): string {
  // Insert the status_class matcher before the selector's CLOSING brace.
  // Uses lastIndexOf + slice rather than String.replace('}', …): replace
  // targets the first '}' (fragile if the selector ever grows a nested brace)
  // and trips CodeQL's incomplete-sanitization rule; this targets the closing
  // brace deterministically and no-ops on a brace-less metric.
  const close = metric.lastIndexOf('}')
  if (close === -1) return metric
  return `${metric.slice(0, close)}, status_class="${statusClass}"${metric.slice(close)}`
}

// scalar — run an instant query and take the single-series value.
async function scalar(query: string): Promise<number | null> {
  const res = await api.queryMetrics({ query })
  const v = parseFloat(res?.data?.result?.[0]?.value?.[1] ?? '')
  return Number.isFinite(v) ? v : null
}

// labeled — instant query returning the top series' label + value.
async function labeled(
  query: string,
  label: string,
): Promise<{ name: string; value: number } | null> {
  const res = await api.queryMetrics({ query })
  const s = res?.data?.result?.[0]
  if (!s) return null
  const v = parseFloat(s.value?.[1] ?? '')
  const name = s.metric?.[label]
  if (!name || !Number.isFinite(v)) return null
  return { name, value: v }
}

// labeledAll — instant query returning every series' label + value.
async function labeledAll(query: string, label: string): Promise<{ name: string; value: number }[]> {
  const res = await api.queryMetrics({ query })
  return (res?.data?.result ?? [])
    .map((s) => ({ name: s.metric?.[label] ?? '', value: parseFloat(s.value?.[1] ?? '') }))
    .filter((r) => r.name && Number.isFinite(r.value))
    .sort((a, b) => b.value - a.value)
}

// relDelta — (now − prev) / prev, null when either side is missing or
// the previous window isn't a usable baseline. Two guards beyond the
// divide-by-zero: (a) prev negligible relative to now (< 2%) and (b)
// resulting delta beyond ±500%. Both mean "the previous window barely
// had signal" — e.g. a 30d range whose offset window predates the
// agent install — and printing "▲ 6730%" reads as a bug, not a trend.
function relDelta(now?: number | null, prev?: number | null): number | null {
  if (now == null || prev == null || prev <= 0) return null
  if (now > 0 && prev < now * 0.02) return null
  const delta = (now - prev) / prev
  if (Math.abs(delta) > 5) return null
  return delta
}

function deltaArrow(delta: number | null): string {
  if (delta == null) return ''
  return delta > 0 ? '▲' : delta < 0 ? '▼' : '·'
}

function latencySentence(delta: number | null): string {
  if (delta != null && Math.abs(delta) > DELTA_NOTABLE) {
    return `${deltaArrow(delta)} ${Math.round(Math.abs(delta) * 100)}% vs the window before`
  }
  if (delta != null) return 'steady vs the window before'
  return 'cluster-wide average'
}

function formatRange(minutes: number): string {
  if (minutes < 60) return `${minutes}m`
  if (minutes < 1440) return `${Math.round(minutes / 60)}h`
  return `${Math.round(minutes / 1440)}d`
}

function formatPct(v: number): string {
  if (v >= 10) return v.toFixed(0)
  if (v >= 1) return v.toFixed(1)
  return v.toFixed(2)
}

// Under 10 ms a whole number hides the movement the delta reports
// ("▲ 14%" over two bars both reading "5 ms").
function formatMs(v: number): string {
  return v < 10 ? v.toFixed(1) : `${Math.round(v)}`
}

function formatRps(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`
  if (v >= 10) return `${Math.round(v)}`
  return v.toFixed(1)
}
