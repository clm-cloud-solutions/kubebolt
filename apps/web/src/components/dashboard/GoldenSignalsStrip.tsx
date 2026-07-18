import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { StripCard, type StripAccent } from './StripCard'
import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { collapsePodToWorkload } from '@/utils/promql'

// GoldenSignalsStrip — the scan layer above Reliability's panels
// (design/kubebolt-reliability-redesign.html): Error rate 5xx ·
// Latency · Throughput · L4 drops. Same Hubble series the detail
// panels consume; the strip only aggregates them cluster-wide.
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

export function GoldenSignalsStrip({ rangeMinutes }: Props) {
  const w = `${rangeMinutes}m`

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
      const [errNow, errPrev, rpsNow, rpsPrev, latNow, latPrev, drops, hot] = await Promise.all([
        scalar(errExpr('')),
        scalar(errExpr(` offset ${w}`)),
        scalar(rpsExpr('')),
        scalar(rpsExpr(` offset ${w}`)),
        scalar(latExpr('')),
        scalar(latExpr(` offset ${w}`)),
        scalar(dropsExpr),
        labeled(hotExpr, 'workload'),
      ])
      return { errNow, errPrev, rpsNow, rpsPrev, latNow, latPrev, drops, hot }
    },
    refetchInterval: 30_000,
    retry: false,
  })

  const d = q.data
  const errAccent: StripAccent =
    d?.errNow == null ? 'default' : d.errNow >= ERR_MEANINGFUL_PCT ? 'crit' : 'ok'
  const latDelta = relDelta(d?.latNow, d?.latPrev)
  const rpsDelta = relDelta(d?.rpsNow, d?.rpsPrev)
  // Empty increase() result = no dropped-flow series in range. The
  // strip only renders when Hubble is shipping, so "no series" IS
  // zero drops, not missing data — showing "—" here read as broken.
  const drops = d ? Math.round(d.drops ?? 0) : null
  const rangeLabel = formatRange(rangeMinutes)

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
      <StripCard
        label="Error rate · 5xx"
        info={
          <>
            <TooltipHeader>Error rate · 5xx</TooltipHeader>
            <TooltipRow color="#ef4056" label="What" value="server errors" />
            <TooltipNote>
              Share of HTTP requests returning 5xx (server-side failures) across all
              Hubble-observed L7 traffic. 4xx (caller errors) are tracked separately in
              the chart below. The delta compares the previous window of the same length.
            </TooltipNote>
          </>
        }
        value={d?.errNow != null ? `${formatPct(d.errNow)}%` : '—'}
        valueAccent={errAccent}
        sub={
          d?.errPrev != null && relDelta(d?.errNow, d?.errPrev) != null
            ? `${deltaArrow(relDelta(d?.errNow, d?.errPrev))} vs ${formatPct(d.errPrev)}% previous window`
            : `over the last ${rangeLabel}`
        }
        subAccent={errAccent === 'default' ? 'default' : errAccent}
      />
      <StripCard
        label="Latency · avg"
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
        value={d?.latNow != null ? Math.round(d.latNow) : '—'}
        valueSuffix="ms"
        valueAccent={latDelta != null && latDelta > DELTA_NOTABLE ? 'warn' : 'default'}
        sub={latencySub(latDelta, d?.hot)}
        subAccent={latDelta != null && latDelta > DELTA_NOTABLE ? 'warn' : 'default'}
      />
      <StripCard
        label="Throughput"
        value={d?.rpsNow != null ? formatRps(d.rpsNow) : '—'}
        valueSuffix="rps"
        sub={
          rpsDelta == null
            ? `over the last ${rangeLabel}`
            : Math.abs(rpsDelta) <= DELTA_NOTABLE
              ? 'steady vs previous window'
              : `${deltaArrow(rpsDelta)} ${Math.round(Math.abs(rpsDelta) * 100)}% vs previous window`
        }
        subAccent={rpsDelta != null && Math.abs(rpsDelta) <= DELTA_NOTABLE ? 'ok' : 'default'}
      />
      <StripCard
        label="L4 drops"
        info={
          <>
            <TooltipHeader>L4 drops</TooltipHeader>
            <TooltipRow color="#f5a623" label="Source" value="Hubble verdict=dropped" />
            <TooltipNote>
              Count of L4 flows Cilium DROPPED in this range — most are NetworkPolicy
              denials, but connection-refused and host-firewall blocks land here too.
              This is the early-warning channel the HTTP panels miss: dropped traffic
              never reaches the application layer to become a 4xx/5xx.
            </TooltipNote>
          </>
        }
        value={drops != null ? `${drops}` : '—'}
        valueAccent={drops != null && drops > 0 ? 'warn' : 'default'}
        sub={drops != null && drops > 0 ? 'NetworkPolicy / refused' : 'no drops in range'}
        subAccent={drops != null && drops > 0 ? 'warn' : 'ok'}
      />
    </div>
  )
}

function withClass(metric: string, statusClass: string): string {
  return metric.replace('}', `, status_class="${statusClass}"}`)
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

function latencySub(
  delta: number | null,
  hot: { name: string; value: number } | null | undefined,
): string {
  const parts: string[] = []
  if (delta != null && Math.abs(delta) > DELTA_NOTABLE) {
    parts.push(`${deltaArrow(delta)} ${Math.round(Math.abs(delta) * 100)}% vs previous window`)
  } else if (delta != null) {
    parts.push('steady vs previous window')
  }
  if (hot) parts.push(`${hot.name} hottest`)
  return parts.length > 0 ? parts.join(' · ') : 'cluster-wide average'
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

function formatRps(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`
  if (v >= 10) return `${Math.round(v)}`
  return v.toFixed(1)
}
