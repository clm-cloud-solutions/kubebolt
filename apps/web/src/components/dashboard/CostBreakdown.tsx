import { useState } from 'react'
import { Link } from 'react-router-dom'
import { HoverTooltip, TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { formatMoney, formatPercent, formatCPU, formatMemory } from '@/utils/formatters'
import type { ClusterCost } from '@/hooks/useClusterCost'

// CostBreakdown — where the spend goes, one lens at a time (design/
// kubebolt-cost-redesign.html "By namespace / By workload / By node").
// Bars are proportional to the biggest row so the distribution reads
// at a glance; the namespace lens also carries the commitment-
// efficiency % (used/allocated) so "expensive AND wasteful" jumps
// out — that's the row worth right-sizing first. Node lens tags spot
// capacity. Hovering a bar opens a per-resource tooltip (CPU + memory
// used/allocated), the same grammar Capacity's tooltips use. All
// figures come from useClusterCost's attribution — no new queries.

type Lens = 'namespace' | 'workload' | 'node'

interface Props {
  cost: ClusterCost
}

// Efficiency badge color — mirrors CostKpis.effAccent so a namespace
// reads the same way here as in the KPI strip.
function effClass(pct: number | null): string {
  if (pct == null) return 'text-kb-text-tertiary'
  if (pct >= 60) return 'text-status-ok'
  if (pct >= 35) return 'text-status-warn'
  return 'text-status-error'
}

interface Row {
  key: string
  name: string
  sub?: string
  monthly: number
  effPct?: number | null
  spot?: boolean
  // Per-resource detail for the bar tooltip (namespace lens only).
  // CPU in cores, memory in bytes; null when no samples.
  cpuUsed?: number | null
  cpuAllocated?: number | null
  memUsed?: number | null
  memAllocated?: number | null
}

function rowsFor(cost: ClusterCost, lens: Lens): Row[] {
  if (lens === 'workload') {
    return cost.byWorkload.slice(0, 8).map((w) => ({
      key: `${w.namespace}/${w.workload}`,
      name: w.workload,
      sub: w.namespace,
      monthly: w.monthly,
    }))
  }
  if (lens === 'node') {
    return cost.byNode.slice(0, 8).map((n) => ({
      key: n.node,
      name: n.node,
      monthly: n.monthly,
      spot: n.isSpot,
    }))
  }
  return cost.byNamespace.slice(0, 8).map((n) => ({
    key: n.namespace,
    name: n.namespace,
    monthly: n.monthly,
    effPct: n.efficiencyPct,
    cpuUsed: n.cpuUsed,
    cpuAllocated: n.cpuAllocated,
    memUsed: n.memUsed,
    memAllocated: n.memAllocated,
  }))
}

// BarTooltip — explains what the bar represents for each resource,
// mirroring Capacity's per-resource tooltips (CPU / Memory rows with
// used vs allocated). Adapts per lens: namespaces get the full
// resource breakdown; nodes and workloads get their cost basis.
function BarTooltip({ row }: { row: Row }) {
  const isNs = row.cpuUsed !== undefined
  return (
    <>
      <TooltipHeader right={`${formatMoney(row.monthly, { exact: true })}/mo`}>
        {row.name}
      </TooltipHeader>
      {isNs ? (
        <>
          <TooltipRow
            color="#22d68a"
            label="CPU used / alloc"
            value={`${formatCPU((row.cpuUsed ?? 0) * 1000)} / ${formatCPU((row.cpuAllocated ?? 0) * 1000)}`}
          />
          <TooltipRow
            color="#4c9aff"
            label="Mem used / alloc"
            value={`${formatMemory(row.memUsed ?? 0)} / ${formatMemory(row.memAllocated ?? 0)}`}
          />
          <TooltipNote>
            Bar length = share of the top namespace's spend. Efficiency (the % at right)
            is CPU used / allocated — low means you're paying for headroom that sits idle,
            so it's a right-sizing target.
          </TooltipNote>
        </>
      ) : row.spot !== undefined ? (
        <TooltipNote>
          The node's full hourly cost (CPU + RAM + GPU) × 730.{' '}
          {row.spot ? 'Spot capacity — discounted but pre-emptible.' : 'On-demand capacity.'}
        </TooltipNote>
      ) : (
        <TooltipNote>
          The workload's attributed spend — its pods' CPU + memory allocation priced at
          the nodes' hourly rates, summed across replicas.
        </TooltipNote>
      )}
    </>
  )
}

export function CostBreakdown({ cost }: Props) {
  const [lens, setLens] = useState<Lens>('namespace')
  const rows = rowsFor(cost, lens)
  const max = rows.reduce((m, r) => Math.max(m, r.monthly), 0)

  // Kobi payload — the rows the user sees at click-time, so the LLM
  // speaks about exactly this lens's breakdown. Cost + (for namespaces)
  // efficiency AND the ABSOLUTE used/allocated cores + memory go in the
  // blob: without the absolute numbers the LLM has been observed to
  // infer allocation backwards from the efficiency % (e.g. "3% eff →
  // ~1000m allocated") and state the guess as fact. Handing it the real
  // milli/MiB figures removes the gap. truncatedFromTotal tells Kobi
  // the rest are tool-reachable.
  const kobiRows = rows.map((r) => {
    const blob: Record<string, string | number> = {
      name: r.sub ? `${r.name} · ${r.sub}` : r.name,
      cost_usd_per_mo: Math.round(r.monthly),
    }
    if (r.effPct != null) blob.cpu_efficiency_pct = Math.round(r.effPct)
    if (r.cpuUsed != null) blob.cpu_used_milli = Math.round(r.cpuUsed * 1000)
    if (r.cpuAllocated != null) blob.cpu_allocated_milli = Math.round(r.cpuAllocated * 1000)
    if (r.memUsed != null) blob.mem_used_mib = Math.round(r.memUsed / (1024 * 1024))
    if (r.memAllocated != null) blob.mem_allocated_mib = Math.round(r.memAllocated / (1024 * 1024))
    if (r.spot) blob.spot = 'true'
    return blob
  })
  const totalCount =
    lens === 'namespace'
      ? cost.byNamespace.length
      : lens === 'workload'
        ? cost.byWorkload.length
        : cost.byNode.length

  return (
    <div className="rounded-[10px] border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-sm font-semibold text-kb-text-primary">Cost breakdown</span>
          {rows.length > 0 && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'cost_breakdown',
                rows: kobiRows,
                truncatedFromTotal: totalCount,
              }}
              variant="icon"
              label="Ask Kobi about cost breakdown"
            />
          )}
        </div>
        <div className="flex items-center gap-1 text-[11px] font-mono">
          {(['namespace', 'workload', 'node'] as const).map((l) => (
            <button
              key={l}
              type="button"
              onClick={() => setLens(l)}
              className={`px-2 py-0.5 rounded-md transition-colors ${
                lens === l
                  ? 'bg-kb-accent-light text-kb-accent'
                  : 'text-kb-text-tertiary hover:text-kb-text-secondary'
              }`}
            >
              {l === 'namespace' ? 'By namespace' : l === 'workload' ? 'By workload' : 'By node'}
            </button>
          ))}
        </div>
      </div>

      {rows.length === 0 ? (
        <div className="py-8 text-center text-xs text-kb-text-tertiary">
          No attributable spend yet
        </div>
      ) : (
        <>
          {/* Column labels — fixed-width, right-aligned so every row's
              cost and efficiency line up in clean columns. */}
          <div className="flex items-center gap-3 mb-2 text-[9px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            <span className="flex-1 min-w-0">{lens}</span>
            <span className="w-20 text-right shrink-0">Cost / mo</span>
            {lens === 'namespace' && <span className="w-12 text-right shrink-0">Eff</span>}
          </div>
          <div className="space-y-2.5">
            {rows.map((r) => {
              const pct = max > 0 ? (r.monthly / max) * 100 : 0
              const warn = r.effPct != null && r.effPct < 35
              return (
                <div key={r.key}>
                  <div className="flex items-center gap-3 mb-1">
                    <span className="text-xs text-kb-text-secondary truncate min-w-0 flex-1">
                      <span className="text-kb-text-primary">{r.name}</span>
                      {r.sub && <span className="text-kb-text-tertiary"> · {r.sub}</span>}
                      {r.spot && (
                        <span className="ml-1.5 px-1 py-0.5 rounded bg-status-info-dim text-status-info text-[9px] font-mono uppercase tracking-[0.06em]">
                          spot
                        </span>
                      )}
                    </span>
                    <span className="font-mono tabular-nums text-xs text-kb-text-primary shrink-0 w-20 text-right">
                      {formatMoney(r.monthly, { exact: r.monthly < 100_000 })}
                    </span>
                    {r.effPct !== undefined && (
                      <span
                        className={`font-mono tabular-nums text-[11px] w-12 text-right shrink-0 ${effClass(r.effPct)}`}
                      >
                        {r.effPct != null ? formatPercent(r.effPct) : '—'}
                      </span>
                    )}
                  </div>
                  <HoverTooltip body={<BarTooltip row={r} />}>
                    <div
                      className="h-1.5 rounded-full overflow-hidden cursor-help"
                      style={{ background: 'var(--kb-bar-track)' }}
                    >
                      <div
                        className={`h-full rounded-full ${warn ? 'bg-status-warn' : 'bg-status-ok'}`}
                        style={{ width: `${Math.max(2, pct)}%` }}
                      />
                    </div>
                  </HoverTooltip>
                </div>
              )
            })}
          </div>
        </>
      )}

      {/* Idle callout — the reclaimable-now line the mockup carries.
          Idle is compute you pay for that nothing requested; the fix
          lives on Capacity (right-size requests) and Nodes
          (consolidate onto fewer nodes). */}
      {cost.idleMonthly > 0 && (
        <div
          className="mt-3 flex items-center justify-between gap-2 rounded-lg border bg-kb-accent-light px-3 py-2"
          style={{ borderColor: 'color-mix(in srgb, var(--kb-accent) 22%, transparent)' }}
        >
          <span className="text-xs text-kb-text-secondary">
            <span className="text-kb-accent font-semibold">
              {formatMoney(cost.idleMonthly, { exact: cost.idleMonthly < 100_000 })}/mo
            </span>{' '}
            idle · unallocated capacity
          </span>
          <Link
            to="/capacity"
            className="text-[11px] font-mono text-kb-accent hover:underline shrink-0"
          >
            Right-size →
          </Link>
        </div>
      )}
    </div>
  )
}
