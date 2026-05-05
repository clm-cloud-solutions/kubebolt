import { useQuery } from '@tanstack/react-query'
import { ShieldOff } from 'lucide-react'
import { api } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { collapsePodToWorkload } from '@/utils/promql'
import type { PanelInquiryTriggerPayload } from '@/services/copilot/triggers'

// NetworkDrops surfaces L4 network flows that Hubble marked as
// dropped — in a Cilium cluster that's almost always a
// NetworkPolicy or CiliumNetworkPolicy blocking traffic, but it
// can also be connection refused, the destination pod being down,
// or host firewall rules.
//
// Distinct reliability signal from the HTTP panels above: those
// only see traffic that completed the TCP handshake. Drops
// happen BEFORE the application sees the request. A workload that
// "is fine" by HTTP error rate can still be unreachable from half
// the cluster because of a misconfigured policy. This panel is
// the early-warning channel for that class of failure.
//
// Sort by absolute drop rate (events/s), not percentage of
// flows: in a healthy cluster the denominator (forwarded flows)
// is huge, so drop percentages stay low even when a real issue
// exists. Absolute volume is the load-bearing signal.
//
// Empty state framed positively — "no drops" IS the all-clear,
// and the slim line confirming silence is more useful than
// hiding the panel (which would also break the 2-col pairing
// with TopLatencyWorkloads).

const TOP_N = 10
const REFRESH_MS = 30_000

interface DropRow {
  srcNamespace: string
  srcWorkload: string
  dstNamespace: string
  dstWorkload: string
  dropRate: number  // events/s
}

interface Props {
  rangeMinutes: number
}

export function NetworkDrops({ rangeMinutes }: Props) {
  const RATE_WINDOW = `${rangeMinutes}m`

  // Two-pass collapse: src_pod and dst_pod separately. Both go
  // into the `sum by` so aggregation lands on the workload pair.
  const dropQuery = `topk(${TOP_N}, sum by (src_namespace, src_workload, dst_namespace, dst_workload) (`
    + collapsePodToWorkload(
      collapsePodToWorkload(
        `rate(pod_flow_events_total{source="hubble", verdict="dropped"}[${RATE_WINDOW}])`,
        'src_pod',
        'src_workload',
      ),
      'dst_pod',
      'dst_workload',
    )
    + `))`

  const dropQ = useQuery({
    queryKey: ['reliability', 'network-drops', RATE_WINDOW],
    queryFn: () => api.queryMetrics({ query: dropQuery }),
    refetchInterval: REFRESH_MS,
    retry: false,
  })

  const isLoading = dropQ.isLoading
  const error = dropQ.error

  const rows: DropRow[] = (dropQ.data?.data?.result ?? [])
    .map((s) => ({
      srcNamespace: s.metric.src_namespace ?? '',
      srcWorkload: s.metric.src_workload ?? '',
      dstNamespace: s.metric.dst_namespace ?? '',
      dstWorkload: s.metric.dst_workload ?? '',
      dropRate: parseFloat(s.value?.[1] ?? '0'),
    }))
    .filter((r) => r.srcWorkload && r.dstWorkload && Number.isFinite(r.dropRate) && r.dropRate > 0)
    .sort((a, b) => b.dropRate - a.dropRate)

  const kobiRows = rows.map(buildKobiRow)

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-kb-text-secondary shrink-0">
            <ShieldOff className="w-4 h-4" />
          </span>
          <h4 className="text-sm font-semibold text-kb-text-primary truncate">
            Network Drops
          </h4>
          {rows.length > 0 && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'network_drops',
                rangeLabel: RATE_WINDOW,
                rows: kobiRows,
              }}
              variant="icon"
              label="Ask Kobi about dropped flows"
            />
          )}
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
          L4 verdict=dropped · top {TOP_N}
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
        <div className="text-[11px] text-kb-text-tertiary py-3">
          No dropped flows in the selected range. NetworkPolicies are passing —
          nothing's silently blocked.
        </div>
      )}

      {!isLoading && !error && rows.length > 0 && (
        <ul className="space-y-2">
          {rows.map((r) => (
            <li key={pairKey(r.srcNamespace, r.srcWorkload, r.dstNamespace, r.dstWorkload)}>
              <DropRowEl row={r} rangeLabel={RATE_WINDOW} />
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function DropRowEl({ row, rangeLabel }: { row: DropRow; rangeLabel: string }) {
  const kobiPayload: PanelInquiryTriggerPayload = {
    type: 'panel_inquiry',
    panel: 'network_drops',
    rangeLabel,
    rows: [buildKobiRow(row)],
  }
  return (
    <HoverTooltip body={<DropTooltip row={row} />}>
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
          <div className="flex items-center gap-2 mt-0.5">
            <span className="text-[10px] font-mono text-kb-text-tertiary truncate">
              {row.srcNamespace}
              {row.dstNamespace !== row.srcNamespace && ` → ${row.dstNamespace}`}
            </span>
          </div>
        </div>
        <AskCopilotButton
          payload={kobiPayload}
          variant="icon"
          label="Ask Kobi about this dropped flow"
          className="opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity shrink-0"
        />
        <span className="text-[11px] font-mono text-status-warn shrink-0 tabular-nums">
          {formatDropRate(row.dropRate)}
        </span>
      </div>
    </HoverTooltip>
  )
}

function DropTooltip({ row }: { row: DropRow }) {
  return (
    <>
      <TooltipHeader right="dropped">
        {row.srcWorkload} → {row.dstWorkload}
      </TooltipHeader>
      <div className="space-y-1">
        <TooltipRow color="#f59e0b" label="Drop rate" value={formatDropRate(row.dropRate)} />
        <TooltipRow color={null} label="Source ns" value={row.srcNamespace} />
        <TooltipRow color={null} label="Dest ns" value={row.dstNamespace} />
      </div>
    </>
  )
}

// ─── Helpers ────────────────────────────────────────────────────

function buildKobiRow(r: DropRow): Record<string, string | number> {
  return {
    src: `${r.srcNamespace}/${r.srcWorkload}`,
    dst: `${r.dstNamespace}/${r.dstWorkload}`,
    drop_events_per_sec: roundTo(r.dropRate, 3),
  }
}

function roundTo(v: number, decimals: number): number {
  const f = Math.pow(10, decimals)
  return Math.round(v * f) / f
}

// Drop rate display: events/s, but switch to events/min for very
// low rates so the user reads "12 events/min" instead of
// "0.20 events/s" — small numbers in the wrong unit feel
// dismissable, and dropped traffic is rarely "small enough to
// dismiss".
function formatDropRate(eventsPerSec: number): string {
  if (!Number.isFinite(eventsPerSec) || eventsPerSec === 0) return '0/s'
  if (eventsPerSec < 0.5) {
    const perMin = eventsPerSec * 60
    if (perMin < 10) return `${perMin.toFixed(1)}/min`
    return `${Math.round(perMin)}/min`
  }
  if (eventsPerSec < 10) return `${eventsPerSec.toFixed(1)}/s`
  return `${Math.round(eventsPerSec)}/s`
}

function pairKey(srcNs: string, srcWl: string, dstNs: string, dstWl: string): string {
  return `${srcNs}/${srcWl}->${dstNs}/${dstWl}`
}
