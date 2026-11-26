import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ChevronRight } from 'lucide-react'
import { api } from '@/services/api'
import { KPI_COLOR } from '@/components/shared/kpi/MiniCharts'
import type { PlanBillPath, PlanBillPathCluster } from '@/types/kobiPlans'
import { MINUS, formatPlanMoney, freedUnits, planPath, poolLabel } from '@/utils/kobiPlans'

// BillPathPanel — the bill's path on the Plans list (plans-16): one cluster's
// bill today and what its live plans do to it. Right-sizing frees capacity
// the bill keeps paying for; consolidation removes the node then left over,
// and only that moves the bill. List prices from OpenCost. What KubeBolt does
// not plan yet (a node's shape, orphaned disks) is named, disabled, with no
// figure: nothing measures it yet.
//
// Right-sizing's column shows what the capacity it frees costs at list
// prices, hatched and marked «not in total»: that is its impact, but the nodes
// stay, so the path goes on at the bill's level (26-nov).
//
// Without a cluster it is the fleet's (26-nov): the same columns over the
// bills of every cluster with live plans, then each cluster's row, which opens
// its own path. A cluster without OpenCost prices is named, outside the sums.

const CHART_H = 220
const TOP_INSET = 12
const GRID_LINE = '1px dashed color-mix(in srgb, var(--kb-border) 60%, transparent)'
// Under this height a bar's figure goes below it.
const MIN_LABEL_H = 18
const CONNECTOR = '1px dashed color-mix(in srgb, var(--kb-text-tertiary) 55%, transparent)'

export function BillPathPanel({ clusterId, onPickCluster }: { clusterId?: string; onPickCluster?: (clusterId: string) => void }) {
  const { data } = useQuery({
    queryKey: ['kobi-plans', 'bill-path', clusterId ?? ''],
    queryFn: () => api.getPlanBillPath(clusterId),
    staleTime: 60_000,
  })
  if (!data?.bill || (data.rightsizing.length === 0 && data.consolidation.length === 0)) return null
  return <BillPathBody path={data} fleet={!clusterId} onPickCluster={onPickCluster} />
}

const amount = (v: number) => new Intl.NumberFormat(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(v)

// niceStep: 1, 2 or 5 times a power of ten, about a quarter of the range.
function niceStep(range: number): number {
  const raw = Math.max(range / 4, 1)
  const pow = 10 ** Math.floor(Math.log10(raw))
  const m = raw / pow
  return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10) * pow
}

export function BillPathBody({ path, fleet = false, onPickCluster }: { path: PlanBillPath; fleet?: boolean; onPickCluster?: (clusterId: string) => void }) {
  const bill = path.bill!
  const clusters = fleet ? (path.clusters ?? []) : []
  const unpriced = fleet ? (path.unpriced ?? []) : []
  const cutting = clusters.filter((c) => c.off > 0).length
  const cur = bill.currency
  const off = path.consolidation.reduce((sum, c) => sum + (c.monthly && c.monthly.currency === cur ? c.monthly.monthly : 0), 0)
  const reachable = path.reachable?.monthly ?? bill.monthly - off
  const nodes = path.consolidation.reduce((n, c) => n + c.pools.reduce((m, p) => m + (p.nodesBefore - p.nodesAfter), 0), 0)
  const freed = freedUnits({
    cpuMilli: path.rightsizing.reduce((n, r) => n + r.estimated.cpuMilli, 0),
    memBytes: path.rightsizing.reduce((n, r) => n + r.estimated.memBytes, 0),
    money: null,
  })
  const pools = path.consolidation.flatMap((c) => c.pools)
  // What the right-sizing plans free, at list prices, in the bill's currency.
  const idle = path.rightsizing.reduce((sum, r) => sum + (r.estimated.money && r.estimated.money.currency === cur ? r.estimated.money.monthly : 0), 0)
  const rsSteps = path.rightsizing.reduce((n, r) => n + r.stepCount, 0)
  const rsApplied = path.rightsizing.reduce((n, r) => n + r.appliedCount, 0)

  // The axis starts below the lowest bar so a change of a tenth reads; the
  // caption says where it starts.
  const floor = Math.min(reachable, bill.monthly - idle)
  const step = niceStep(Math.max(bill.monthly - floor, bill.monthly * 0.1))
  const lo = Math.max(0, Math.floor((floor - step * 1.5) / step) * step)
  const hi = Math.ceil((bill.monthly + step * 0.25) / step) * step
  // Inset from the top so the highest tick's label is not cut.
  const y = (v: number) => TOP_INSET + ((hi - v) / (hi - lo)) * (CHART_H - TOP_INSET)
  const ticks: number[] = []
  for (let t = lo; t <= hi + 1e-9; t += step) ticks.push(t)

  // Across clusters a column opens its plan only when it has one.
  const rs = !fleet || path.rightsizing.length === 1 ? path.rightsizing[0] : undefined
  const cons = !fleet || path.consolidation.length === 1 ? path.consolidation[0] : undefined
  const columns: Column[] = [
    { label: 'Bill today', sub: 'nodes, disks, load balancers and fee', bar: { from: lo, to: bill.monthly, kind: 'bill' }, value: amount(bill.monthly) },
    {
      label: 'Right-sizing',
      tag: idle > 0 ? 'not in total' : undefined,
      sub: rightsizingWords(path.rightsizing.length, freed, rsApplied, rsSteps),
      to: rs && planPath(rs.id),
      bar: idle > 0 ? { from: bill.monthly - idle, to: bill.monthly, kind: 'idle' } : { from: bill.monthly, to: bill.monthly, kind: 'flat' },
      // No price, no figure: a «0.00» would read as no impact.
      value: idle > 0 || path.rightsizing.length === 0 ? amount(idle) : undefined,
    },
    {
      label: 'Consolidation',
      sub:
        pools.length === 0
          ? 'no live plan'
          : cutting > 1
            ? `${nodes} ${nodes === 1 ? 'node' : 'nodes'} less in ${cutting} clusters`
            : pools.length === 1
            ? `${poolLabel(pools[0].pool)}, ${pools[0].nodesBefore} → ${pools[0].nodesAfter} nodes`
            : `${pools.length} pools, ${nodes} ${nodes === 1 ? 'node' : 'nodes'} less`,
      to: cons && planPath(cons.id),
      bar: off > 0 ? { from: reachable, to: bill.monthly, kind: 'cut' } : { from: bill.monthly, to: bill.monthly, kind: 'flat' },
      value: off > 0 ? `${MINUS}${amount(off)}` : amount(0),
    },
    { label: 'Reachable today', sub: 'with what can be planned now', bar: { from: lo, to: reachable, kind: 'reach' }, value: amount(reachable) },
    { label: 'Node shape', sub: 'another family or size', soon: true, level: reachable },
    { label: 'Orphans', sub: 'unused disks and load balancers', soon: true, level: reachable },
  ]
  const pct = bill.monthly > 0 ? (off / bill.monthly) * 100 : 0

  return (
    <section className="kb-panel px-5 py-5 flex flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 max-w-3xl flex flex-col gap-1">
          <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            The bill&apos;s path · {fleet ? `${clusters.length} ${clusters.length === 1 ? 'cluster' : 'clusters'} with live plans` : path.clusterName || path.clusterId}
          </div>
          <h2 className="text-[17px] font-semibold text-kb-text-primary">
            {off > 0
              ? `From ${formatPlanMoney(bill.monthly, cur)} to ${formatPlanMoney(reachable, cur)} a month, by removing ${nodes === 1 ? 'one node' : `${nodes} nodes`}${
                  cutting > 1 ? ` in ${cutting} clusters` : ''
                }`
              : `${formatPlanMoney(bill.monthly, cur)} a month today${fleet ? ` across ${clusters.length === 1 ? 'one cluster' : `${clusters.length} clusters`}` : ''}: no node is planned to go yet`}
          </h2>
          <p className="text-[13px] leading-5 text-kb-text-secondary">
            Right-sizing frees capacity, but the bill moves only when consolidation removes the node left over. OpenCost list prices: with Savings Plans or
            reservations the real saving can be smaller.
          </p>
        </div>
        {off > 0 && (
          <div className="text-right">
            <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">Reachable today</div>
            <div className="text-[26px] font-semibold text-kb-text-primary">
              {formatPlanMoney(reachable, cur)} <span className="text-[14px] font-normal text-kb-text-secondary">/ month</span>
            </div>
            <div className="text-[13px] text-status-ok">
              {MINUS}
              {formatPlanMoney(off, cur)} · {MINUS}
              {pct.toLocaleString(undefined, { maximumFractionDigits: 1 })}%
            </div>
          </div>
        )}
      </div>

      <div className="overflow-x-auto">
        <div className="min-w-[640px] grid grid-cols-[48px_1fr]">
          {/* Axis */}
          <div className="relative" style={{ height: CHART_H }}>
            {ticks.map((t) => (
              <span key={t} className="absolute right-2 -translate-y-1/2 text-[10px] font-mono text-kb-text-tertiary" style={{ top: y(t) }}>
                {Math.round(t).toLocaleString()}
              </span>
            ))}
          </div>
          <div className="relative border-l border-b border-kb-border" style={{ height: CHART_H }}>
            {ticks.map((t) => (
              <span key={t} className="absolute left-0 right-0" style={{ top: y(t), borderTop: GRID_LINE }} />
            ))}
            <div className="absolute inset-0 grid grid-cols-6">
              {columns.map((c, i) => (
                <BarCell key={c.label} column={c} y={y} next={columns[i + 1]} />
              ))}
            </div>
          </div>
          <span />
          <div className="grid grid-cols-6 pt-2.5">
            {columns.map((c) => (
              <div key={c.label} className="px-2 text-center flex flex-col gap-0.5">
                <div className="flex flex-wrap items-center justify-center gap-1.5 text-[13px] font-semibold">
                  {c.to ? (
                    <Link to={c.to} className="text-kb-accent hover:underline">
                      {c.label}
                    </Link>
                  ) : (
                    <span className={c.soon ? 'text-kb-text-tertiary' : 'text-kb-text-primary'}>{c.label}</span>
                  )}
                  {c.soon && <span className="text-[9px] font-mono font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded-full bg-kb-accent-light text-kb-accent">Soon</span>}
                  {c.tag && (
                    <span className="text-[9px] font-mono font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded-full border border-kb-border bg-kb-elevated text-kb-text-tertiary whitespace-nowrap">
                      {c.tag}
                    </span>
                  )}
                </div>
                <div className="text-[12px] leading-4 text-kb-text-tertiary">{c.sub}</div>
              </div>
            ))}
          </div>
        </div>
      </div>

      {fleet && clusters.length > 0 && <FleetRows clusters={clusters} currency={cur} onPick={onPickCluster} />}
      {unpriced.length > 0 && (
        <p className="text-[12px] text-kb-text-tertiary">
          {listWords(unpriced.map((u) => u.clusterName || u.clusterId))} {unpriced.length === 1 ? 'has' : 'have'} live plans but no OpenCost prices: not in these
          figures.
        </p>
      )}

      <div className="flex flex-wrap items-center justify-between gap-2 pt-3 border-t border-kb-border text-[12px] text-kb-text-tertiary">
        <span>
          Axis from {formatPlanMoney(lo, cur)}.{' '}
          {idle > 0 && 'Right-sizing shows what the capacity it frees costs at list prices: the nodes stay, so it is not in the total until consolidation removes one. '}
          Cross-zone network is not in it: nothing measures it yet.{' '}
          {fleet ? 'Each cluster opens its own path' : 'Each name opens its plan'}; the measured bill comes 7 days after a plan is applied.
        </span>
        <span className="font-mono">list prices · OpenCost · {cur.toUpperCase()}</span>
      </div>
    </section>
  )
}

interface Column {
  label: string
  // tag: a chip beside the label (right-sizing's «not in total»).
  tag?: string
  sub: string
  to?: string
  bar?: { from: number; to: number; kind: 'bill' | 'flat' | 'cut' | 'reach' | 'idle' }
  value?: string
  soon?: boolean
  level?: number
}

const BAR_STYLE: Record<string, React.CSSProperties> = {
  bill: { background: 'color-mix(in srgb, var(--kb-text-tertiary) 45%, transparent)' },
  cut: { background: `color-mix(in srgb, ${KPI_COLOR.ok} 18%, transparent)`, border: `1px solid ${KPI_COLOR.ok}` },
  reach: { background: `color-mix(in srgb, ${KPI_COLOR.ok} 40%, transparent)`, border: `1px solid ${KPI_COLOR.ok}` },
  // Hatched: a price, not a step down the bill.
  idle: {
    background: 'repeating-linear-gradient(135deg, color-mix(in srgb, var(--kb-text-tertiary) 22%, transparent) 0 5px, transparent 5px 10px)',
    border: '1px dashed color-mix(in srgb, var(--kb-text-tertiary) 70%, transparent)',
  },
}

function BarCell({ column: c, y, next }: { column: Column; y: (v: number) => number; next?: Column }) {
  if (c.soon) {
    // No figure: only the level it would start from.
    return (
      <div className="relative">
        <span className="absolute left-0 right-0" style={{ top: y(c.level ?? 0), borderTop: CONNECTOR }} />
      </div>
    )
  }
  const b = c.bar!
  const top = y(Math.max(b.from, b.to))
  const height = Math.max(y(Math.min(b.from, b.to)) - top, b.kind === 'flat' ? 0 : 2)
  const end = b.kind === 'cut' ? Math.min(b.from, b.to) : Math.max(b.from, b.to)
  // A change's figure sits in its bar, or under it when the bar is too thin.
  const delta = b.kind === 'cut' || b.kind === 'idle'
  const inside = height >= MIN_LABEL_H
  const deltaLabel = delta && (
    <span className={`text-[12px] font-mono ${b.kind === 'cut' ? 'text-status-ok' : `px-1 rounded-sm text-kb-text-secondary ${inside ? 'bg-kb-card' : ''}`}`}>{c.value}</span>
  )
  return (
    <div className="relative">
      {b.kind === 'flat' ? (
        <span className="absolute left-[12%] right-[12%] h-[3px] rounded-full" style={{ top: top - 1, background: 'color-mix(in srgb, var(--kb-text-tertiary) 60%, transparent)' }} />
      ) : (
        <span className="absolute left-[12%] right-[12%] rounded-sm flex items-center justify-center" style={{ top, height, ...BAR_STYLE[b.kind] }}>
          {inside && deltaLabel}
        </span>
      )}
      {delta && !inside && (
        <span className="absolute left-0 right-0 text-center" style={{ top: top + height + 3 }}>
          {deltaLabel}
        </span>
      )}
      {!delta && (
        <span className="absolute left-0 right-0 text-center text-[12px] font-mono text-kb-text-primary" style={{ top: top - 20 }}>
          {c.value}
        </span>
      )}
      {/* The connector to the next bar, at the level this one ends. */}
      {next && !next.soon && <span className="absolute left-[88%] -right-[12%]" style={{ top: y(end), borderTop: CONNECTOR }} />}
    </div>
  )
}

// FleetRows: each cluster's bill, the part its plans take off lit, on one
// scale, so the rows compare.
function FleetRows({ clusters, currency, onPick }: { clusters: PlanBillPathCluster[]; currency: string; onPick?: (clusterId: string) => void }) {
  const max = Math.max(...clusters.map((c) => c.bill.monthly), 1)
  return (
    <div className="flex flex-col gap-1">
      <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">By cluster</div>
      <ul className="flex flex-col">
        {clusters.map((c) => {
          const name = c.clusterName || c.clusterId
          return (
            <li key={c.clusterId}>
              <button
                type="button"
                onClick={() => onPick?.(c.clusterId)}
                aria-label={`Open the bill's path of ${name}`}
                className="w-full grid grid-cols-[minmax(0,1fr)_auto_auto] sm:grid-cols-[minmax(0,10rem)_minmax(0,1fr)_minmax(0,12rem)_minmax(0,9rem)_auto] items-center gap-x-4 gap-y-1 px-2 py-2 rounded-md text-left hover:bg-kb-elevated transition-colors"
              >
                <span className="truncate text-[13px] font-semibold text-kb-text-primary">{name}</span>
                <ClusterBar bill={c.bill.monthly} off={c.off} max={max} />
                <span className="hidden sm:inline text-[12px] font-mono text-kb-text-secondary whitespace-nowrap">
                  {amount(c.bill.monthly)} → {amount(c.reachable.monthly)}
                </span>
                <span className={`text-[12px] whitespace-nowrap ${c.off > 0 ? 'text-status-ok' : 'text-kb-text-tertiary'}`}>
                  {c.off > 0 ? `${MINUS}${formatPlanMoney(c.off, currency)} · ${c.nodes === 1 ? 'one node' : `${c.nodes} nodes`}` : 'no node planned'}
                </span>
                <ChevronRight className="w-4 h-4 text-kb-text-tertiary" aria-hidden />
              </button>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

// ClusterBar: the bill as a bar on the fleet's scale; what stays is grey, what
// the plans take off is lit, like the chart's columns.
function ClusterBar({ bill, off, max }: { bill: number; off: number; max: number }) {
  const stays = Math.max(bill - off, 0)
  return (
    <span className="hidden sm:flex h-2.5 w-full items-stretch" aria-hidden>
      <span className={off > 0 ? 'rounded-l-sm' : 'rounded-sm'} style={{ width: `${(stays / max) * 100}%`, ...BAR_STYLE.bill }} />
      {off > 0 && <span className="rounded-r-sm" style={{ width: `${(off / max) * 100}%`, ...BAR_STYLE.cut }} />}
    </span>
  )
}

function listWords(names: string[]): string {
  return names.length <= 1 ? (names[0] ?? '') : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`
}

// rightsizingWords: what the live right-sizing plans free, and how far they
// are applied.
function rightsizingWords(plans: number, freed: string | null, applied: number, steps: number): string {
  if (plans === 0) return 'no live plan'
  const what = freed ?? 'capacity'
  if (steps > 0 && applied >= steps) return `freed ${what}, left idle`
  return `frees ${what}, left idle${applied > 0 ? ` · ${applied} of ${steps} steps applied` : ''}`
}
