import { useQuery } from '@tanstack/react-query'
import { Flame } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { collapsePodToWorkload } from '@/utils/promql'
import type { PanelInquiryTriggerPayload } from '@/services/copilot/triggers'

// ErrorHotspots — surfaces pod-to-pod (collapsed to workload-to-
// workload) HTTP flows with the highest absolute error rate (4xx +
// 5xx, in req/s). The Top Traffic panel sorts by raw load; this
// one sorts by *broken* load so a low-volume but consistently-
// failing flow doesn't get buried.
//
// Sort by absolute error req/s (not error rate %) on purpose: a
// dependency emitting 0.1 req/s of 5xx is interesting, but a flow
// at 100 req/s with a flat 1% error rate is interesting too — and
// shipping the latter to the top makes operators aware of error
// budget burn that's quietly running. Percentage tells the story
// of "how bad relative to load"; absolute tells "how much hurt is
// this flow causing".
//
// Sources: pod_flow_http_requests_total split by status_class. We
// query two cuts of the same metric: errors only (for sorting and
// the absolute number) and total (for the % we show in the tooltip).

const TOP_N = 10
const REFRESH_MS = 30_000

interface Props {
  rangeMinutes: number
}

interface HotspotRow {
  srcNamespace: string
  srcWorkload: string
  dstNamespace: string
  dstWorkload: string
  errorRate: number   // req/s of 4xx + 5xx
  totalRate: number   // req/s of all requests for the same pair
  // Per-status-class breakdown — only error classes; success
  // classes are aggregated into totalRate but not rendered.
  byStatus: { server_err: number; client_err: number }
}

export function ErrorHotspots({ rangeMinutes }: Props) {
  // Rate window is the user-selected range so the hot-spot list and
  // the trend chart above agree on "what window are we looking at".
  // queryKey carries the window so changing the selector
  // invalidates and re-fetches.
  const RATE_WINDOW = `${rangeMinutes}m`
  // For source-pod collapse we use a different output label name so
  // both src_workload and dst_workload survive the `sum by` below.
  const errorByPair =
    `sum by (src_namespace, src_workload, dst_namespace, dst_workload, status_class) (`
    + collapsePodToWorkload(
      collapsePodToWorkload(
        `rate(pod_flow_http_requests_total{source="hubble", status_class=~"server_err|client_err"}[${RATE_WINDOW}])`,
        'src_pod',
        'src_workload',
      ),
      'dst_pod',
      'dst_workload',
    )
    + `)`

  const totalByPair =
    `sum by (src_namespace, src_workload, dst_namespace, dst_workload) (`
    + collapsePodToWorkload(
      collapsePodToWorkload(
        `rate(pod_flow_http_requests_total{source="hubble"}[${RATE_WINDOW}])`,
        'src_pod',
        'src_workload',
      ),
      'dst_pod',
      'dst_workload',
    )
    + `)`

  const errQ = useQuery({
    queryKey: ['reliability', 'hotspots', 'errors', RATE_WINDOW],
    queryFn: () => api.queryMetrics({ query: errorByPair }),
    refetchInterval: REFRESH_MS,
    retry: false,
  })
  const totalQ = useQuery({
    queryKey: ['reliability', 'hotspots', 'total', RATE_WINDOW],
    queryFn: () => api.queryMetrics({ query: totalByPair }),
    refetchInterval: REFRESH_MS,
    retry: false,
  })

  const isLoading = errQ.isLoading
  const error = errQ.error

  // Aggregate the (status_class-broken) error rows into
  // per-pair totals + per-class breakdowns. Same sample iteration
  // populates both, so we only walk the result vector once.
  const pairMap = new Map<string, HotspotRow>()
  for (const s of errQ.data?.data?.result ?? []) {
    const srcNs = s.metric.src_namespace ?? ''
    const srcWl = s.metric.src_workload ?? ''
    const dstNs = s.metric.dst_namespace ?? ''
    const dstWl = s.metric.dst_workload ?? ''
    if (!srcWl || !dstWl) continue
    const k = pairKey(srcNs, srcWl, dstNs, dstWl)
    const v = parseFloat(s.value?.[1] ?? '0')
    if (!Number.isFinite(v) || v <= 0) continue
    const cls = s.metric.status_class === 'client_err' ? 'client_err' : 'server_err'
    let row = pairMap.get(k)
    if (!row) {
      row = {
        srcNamespace: srcNs,
        srcWorkload: srcWl,
        dstNamespace: dstNs,
        dstWorkload: dstWl,
        errorRate: 0,
        totalRate: 0,
        byStatus: { server_err: 0, client_err: 0 },
      }
      pairMap.set(k, row)
    }
    row.errorRate += v
    row.byStatus[cls] += v
  }
  // Join total rate into each pair so the tooltip can show the %
  // alongside absolute err req/s. Total query may include pairs
  // with zero errors (which the loop above never created); skip
  // those — we only render hot-spots, not all flows.
  for (const s of totalQ.data?.data?.result ?? []) {
    const k = pairKey(
      s.metric.src_namespace ?? '',
      s.metric.src_workload ?? '',
      s.metric.dst_namespace ?? '',
      s.metric.dst_workload ?? '',
    )
    const row = pairMap.get(k)
    if (!row) continue
    const v = parseFloat(s.value?.[1] ?? '0')
    if (Number.isFinite(v)) row.totalRate = v
  }

  const rows = Array.from(pairMap.values())
    .sort((a, b) => b.errorRate - a.errorRate)
    .slice(0, TOP_N)

  // Kobi panel-level payload — same blob shape used per-row, just
  // multi-row for the panel button.
  const kobiRows = rows.map(buildKobiRow)

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-kb-text-secondary shrink-0">
            <Flame className="w-4 h-4" />
          </span>
          <h4 className="text-sm font-semibold text-kb-text-primary truncate">
            Error Hot-spots
          </h4>
          {rows.length > 0 && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'error_hotspots',
                rangeLabel: RATE_WINDOW,
                rows: kobiRows,
              }}
              variant="icon"
              label="Ask Kobi about error hot-spots"
            />
          )}
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
          4xx + 5xx · top {TOP_N}
        </span>
      </div>

      {isLoading && (
        <div className="py-6">
          <LoadingSpinner size="sm" />
        </div>
      )}

      {error && !isLoading && (
        <div className="text-[11px] text-status-warn font-mono py-3">
          Query failed — VictoriaMetrics unreachable or Hubble metrics not yet shipped.
        </div>
      )}

      {!isLoading && !error && rows.length === 0 && (
        // No errors in window IS the all-clear. Show it explicitly
        // — silence here would read as "did the query break?", and
        // the slim affirming line is more useful than hiding the
        // panel entirely (which would also break the 2-col layout
        // pairing with Top Workloads · Traffic).
        <div className="text-[11px] text-kb-text-tertiary py-3">
          No HTTP errors in the selected range. Cluster is clean.
        </div>
      )}

      {!isLoading && !error && rows.length > 0 && (
        <ul className="space-y-2">
          {rows.map((r) => (
            <li key={pairKey(r.srcNamespace, r.srcWorkload, r.dstNamespace, r.dstWorkload)}>
              <HotspotRowEl row={r} rangeLabel={RATE_WINDOW} />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function HotspotRowEl({ row, rangeLabel }: { row: HotspotRow; rangeLabel: string }) {
  const errPct = row.totalRate > 0 ? (row.errorRate / row.totalRate) * 100 : NaN
  // Per-row Kobi payload — single-row variant of panel_inquiry, so
  // buildTriggerPrompt picks the singular phrasing and the LLM
  // focuses on this one flow rather than summarizing the list.
  const kobiPayload: PanelInquiryTriggerPayload = {
    type: 'panel_inquiry',
    panel: 'error_hotspots',
    rangeLabel,
    rows: [buildKobiRow(row)],
  }
  return (
    <HoverTooltip body={<HotspotTooltip row={row} errPct={errPct} />}>
      <div className="group flex items-center gap-1 px-2 rounded transition-colors hover:bg-kb-card-hover focus-within:bg-kb-card-hover">
        <div className="min-w-0 flex-1 py-1.5">
          <div className="flex items-baseline gap-1.5 truncate">
            <span className="text-xs text-kb-text-primary truncate">
              {row.srcWorkload}
            </span>
            <span className="text-[10px] font-mono text-kb-text-tertiary">→</span>
            <span className="text-xs text-kb-text-primary truncate">
              {row.dstWorkload}
            </span>
          </div>
          <div className="flex items-center gap-1.5 mt-0.5">
            {row.byStatus.server_err > 0 && (
              <StatusChip color="#ef4056" label="5xx" value={row.byStatus.server_err} />
            )}
            {row.byStatus.client_err > 0 && (
              <StatusChip color="#f59e0b" label="4xx" value={row.byStatus.client_err} />
            )}
          </div>
        </div>
        <AskCopilotButton
          payload={kobiPayload}
          variant="icon"
          label="Ask Kobi about this hot-spot"
          className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity shrink-0"
        />
        <span className="text-[11px] font-mono text-status-error shrink-0 tabular-nums">
          {formatRate(row.errorRate)}
        </span>
      </div>
    </HoverTooltip>
  )
}

function HotspotTooltip({ row, errPct }: { row: HotspotRow; errPct: number }) {
  return (
    <>
      <TooltipHeader right={`${row.dstNamespace}`}>
        {row.srcWorkload} → {row.dstWorkload}
      </TooltipHeader>
      <div className="space-y-1">
        <TooltipRow color="#ef4056" label="Errors" value={formatRate(row.errorRate)} />
        <TooltipRow color="#94a3b8" label="Total requests" value={formatRate(row.totalRate)} />
        <TooltipRow
          color={null}
          label="Error rate"
          value={Number.isFinite(errPct) ? `${errPct.toFixed(errPct < 1 ? 2 : 1)}%` : '—'}
        />
        {row.byStatus.server_err > 0 && (
          <TooltipRow color="#ef4056" label="5xx" value={formatRate(row.byStatus.server_err)} />
        )}
        {row.byStatus.client_err > 0 && (
          <TooltipRow color="#f59e0b" label="4xx" value={formatRate(row.byStatus.client_err)} />
        )}
        <TooltipRow
          color={null}
          label="Source ns"
          value={row.srcNamespace}
        />
      </div>
    </>
  )
}

function StatusChip({ color, label, value }: { color: string; label: string; value: number }) {
  return (
    <span
      className="text-[9px] font-mono uppercase tracking-[0.06em] px-1.5 py-0.5 rounded tabular-nums"
      style={{ background: `${color}20`, color }}
    >
      {label} {formatRate(value)}
    </span>
  )
}

function formatRate(reqPerSec: number): string {
  if (!Number.isFinite(reqPerSec)) return '—'
  if (reqPerSec === 0) return '0 req/s'
  if (reqPerSec < 1) return `${reqPerSec.toFixed(2)} req/s`
  if (reqPerSec < 10) return `${reqPerSec.toFixed(1)} req/s`
  return `${Math.round(reqPerSec)} req/s`
}

function pairKey(srcNs: string, srcWl: string, dstNs: string, dstWl: string): string {
  return `${srcNs}/${srcWl}->${dstNs}/${dstWl}`
}

// Kobi blob for one hot-spot. Carries the full source/destination
// identity plus the absolute and relative error metrics — enough
// for the LLM to talk about whether 4xx (caller's fault) or 5xx
// (receiver's fault) dominates without re-querying.
function buildKobiRow(r: HotspotRow): Record<string, string | number> {
  const errPct = r.totalRate > 0 ? (r.errorRate / r.totalRate) * 100 : NaN
  const blob: Record<string, string | number> = {
    src: `${r.srcNamespace}/${r.srcWorkload}`,
    dst: `${r.dstNamespace}/${r.dstWorkload}`,
    error_rate_per_sec: roundTo(r.errorRate, 2),
    total_rate_per_sec: roundTo(r.totalRate, 2),
  }
  if (Number.isFinite(errPct)) blob.error_rate_pct = roundTo(errPct, 2)
  if (r.byStatus.client_err > 0) blob.rate_4xx = roundTo(r.byStatus.client_err, 2)
  if (r.byStatus.server_err > 0) blob.rate_5xx = roundTo(r.byStatus.server_err, 2)
  return blob
}

function roundTo(v: number, decimals: number): number {
  const f = Math.pow(10, decimals)
  return Math.round(v * f) / f
}
