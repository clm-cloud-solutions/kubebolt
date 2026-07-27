import { Boxes } from 'lucide-react'
import { StripCard } from '@/components/dashboard/StripCard'
import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import type { ResourceItem, ClusterOverview } from '@/types/kubernetes'

// NodesSummaryStrip is the scan layer above the node grid (design/
// kubebolt-nodes-redesign.html): Nodes ready · Bin-packing · Pods
// scheduled · Under pressure · Consolidation. Every number is derived
// from data the page already has — node items (usage %, pod counts,
// cordon) + the cluster overview (requested vs allocatable). No new
// backend, no cost data.
//
// DELIBERATELY NO $/mo and NO "drainable" claim on the Consolidation
// card: a trustworthy "N nodes drainable" needs a scheduling-aware
// bin-packing simulation (taints/affinity/PDB/topology/local-PV), and
// the dollar figure needs node pricing (the OpenCost/Cost slice, not
// in EE yet). Until both exist, the card is an OBSERVATION — "the
// least-loaded node" — not a promise. Same discipline as the
// right-sizing "reclaimable" number: never over-promise a saving we
// can't verify.

interface Props {
  nodes: ResourceItem[]
  overview?: ClusterOverview
}

// A node counts as "under memory pressure" above this usage %.
const MEM_PRESSURE_PCT = 80
// Gap between requested CPU% and memory% beyond which one resource is
// clearly the binding constraint (the other is "stranded").
const BIND_MARGIN_PCT = 15

export function NodesSummaryStrip({ nodes, overview }: Props) {
  const total = nodes.length
  const ready = nodes.filter((n) => n.status === 'Ready').length
  const cordoned = nodes.filter((n) => isUnschedulable(n)).length

  const podsScheduled = nodes.reduce((a, n) => a + num(n.podCount), 0)
  const podsCapacity = nodes.reduce((a, n) => a + num(n.podCapacity), 0)
  const podsPct = podsCapacity > 0 ? (podsScheduled / podsCapacity) * 100 : 0

  const underPressure = nodes.filter((n) => num(n.memoryPercent) > MEM_PRESSURE_PCT).length

  // Cluster-wide bin-packing = requested (reserved by pod specs) vs
  // allocatable, from the overview. This is scheduler reservation, NOT
  // live usage (the node cards show usage) — it's what governs whether
  // pods fit and whether a node can be freed.
  const cpuReqPct = overview?.cpu?.percentRequested
  const memReqPct = overview?.memory?.percentRequested

  // Least-loaded node by memory usage (the binding resource in most
  // clusters) — surfaced as the consolidation observation, no claim.
  const idle = leastLoadedNode(nodes)

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-5 gap-3 mb-5">
      <StripCard
        label="Nodes ready"
        value={`${ready}`}
        valueSuffix={`/ ${total}`}
        valueAccent={ready === total ? 'ok' : 'warn'}
        sub={cordoned > 0 ? `${cordoned} cordoned` : 'all schedulable'}
        subAccent={cordoned > 0 ? 'warn' : 'ok'}
      />

      <BinPackingCard cpuReqPct={cpuReqPct} memReqPct={memReqPct} />

      <StripCard
        label="Pods scheduled"
        value={`${podsScheduled}`}
        valueSuffix={podsCapacity > 0 ? `/ ${podsCapacity}` : undefined}
        sub={podsCapacity > 0 ? `${Math.round(podsPct)}% of capacity` : 'pod capacity unknown'}
      />

      <StripCard
        label="Under pressure"
        value={`${underPressure}`}
        valueAccent={underPressure > 0 ? 'warn' : 'ok'}
        sub={underPressure > 0 ? `nodes > ${MEM_PRESSURE_PCT}% memory` : 'no memory pressure'}
        subAccent={underPressure > 0 ? 'warn' : 'ok'}
      />

      <StripCard
        hero
        label="Consolidation"
        icon={<Boxes className="w-3 h-3" />}
        info={
          <>
            <TooltipHeader right="observation">Consolidation</TooltipHeader>
            <TooltipRow color="#4c9aff" label="Shows" value="least-loaded node" />
            <TooltipNote>
              The node using the least memory right now — a candidate to review for
              consolidation. It is <b>not</b> a "drainable" verdict: confirming a node can be
              freed needs a scheduling-aware simulation (taints, affinity, PodDisruptionBudgets,
              local volumes), and the \$/mo saving needs node pricing — neither is available yet.
              Treat this as a starting point, not a promise.
            </TooltipNote>
          </>
        }
        value={idle ? idleShort(idle) : '—'}
        valueAccent={idle ? 'info' : 'default'}
        sub={
          idle
            ? `${Math.round(num(idle.memoryPercent))}% mem · ${num(idle.podCount)} pods · review to consolidate`
            : 'no clearly idle node'
        }
        subAccent="default"
      />
    </div>
  )
}

// BinPackingCard — the one card StripCard can't express (dual mini-bar).
// Same visual tokens so it sits in the strip cohesively.
function BinPackingCard({
  cpuReqPct,
  memReqPct,
}: {
  cpuReqPct?: number
  memReqPct?: number
}) {
  const cpu = clampPct(cpuReqPct)
  const mem = clampPct(memReqPct)
  const known = cpuReqPct != null && memReqPct != null
  const insight = known ? bindingInsight(cpu, mem) : null

  return (
    <div className="relative rounded-[10px] border border-kb-border bg-kb-card p-4 min-w-0">
      <div className="text-[10px] font-mono uppercase tracking-[0.09em] text-kb-text-tertiary mb-2.5">
        Bin-packing
      </div>
      {known ? (
        <>
          <div className="space-y-1.5">
            <MiniBar label="CPU" pct={cpu} />
            <MiniBar label="Mem" pct={mem} />
          </div>
          {insight && (
            <div className={`text-[11px] font-mono mt-2 ${insight.accent}`}>{insight.text}</div>
          )}
        </>
      ) : (
        <div className="text-[11px] font-mono text-kb-text-tertiary py-2">
          requested vs allocatable unavailable
        </div>
      )}
    </div>
  )
}

function MiniBar({ label, pct }: { label: string; pct: number }) {
  // Reserved capacity bar: green when there's plenty of headroom,
  // amber past 70%, red past 90% — the scheduler starts struggling to
  // place pods as the reservation fills.
  const color = pct >= 90 ? 'bg-status-error' : pct >= 70 ? 'bg-status-warn' : 'bg-status-ok'
  return (
    <div className="flex items-center gap-2 text-[10px] font-mono">
      <span className="w-8 text-kb-text-tertiary shrink-0">{label}</span>
      <span className="flex-1 h-1.5 rounded-full overflow-hidden" style={{ background: 'var(--kb-bar-track)' }}>
        <span className={`block h-full rounded-full ${color}`} style={{ width: `${pct}%` }} />
      </span>
      <span className="w-9 text-right text-kb-text-primary tabular-nums shrink-0">{Math.round(pct)}%</span>
    </div>
  )
}

// ─── helpers ─────────────────────────────────────────────────────

function num(v: unknown): number {
  const n = Number(v)
  return Number.isFinite(n) ? n : 0
}

function clampPct(v?: number): number {
  if (v == null || !Number.isFinite(v)) return 0
  return Math.max(0, Math.min(100, v))
}

function isUnschedulable(n: ResourceItem): boolean {
  return (n as unknown as { unschedulable?: boolean }).unschedulable === true
}

// bindingInsight — which resource is the scheduling bottleneck. When
// one is reserved far more than the other, the low one is "stranded"
// (capacity you paid for but can't schedule against, because the other
// resource runs out first).
function bindingInsight(cpu: number, mem: number): { text: string; accent: string } | null {
  if (mem - cpu >= BIND_MARGIN_PCT) {
    return { text: 'CPU stranded · memory-bound', accent: 'text-status-warn' }
  }
  if (cpu - mem >= BIND_MARGIN_PCT) {
    return { text: 'memory stranded · CPU-bound', accent: 'text-status-warn' }
  }
  return { text: 'balanced', accent: 'text-kb-text-tertiary' }
}

// leastLoadedNode — the schedulable node with the lowest memory usage,
// only when it's meaningfully below the fleet (else there's no clear
// consolidation candidate). Returns undefined when nothing stands out.
function leastLoadedNode(nodes: ResourceItem[]): ResourceItem | undefined {
  const schedulable = nodes.filter((n) => !isUnschedulable(n) && n.status === 'Ready')
  if (schedulable.length < 3) return undefined // too small to consolidate meaningfully
  const sorted = [...schedulable].sort((a, b) => num(a.memoryPercent) - num(b.memoryPercent))
  const lowest = sorted[0]
  const median = num(sorted[Math.floor(sorted.length / 2)].memoryPercent)
  // Only surface it if the lowest is clearly under the median (a real
  // outlier), not just marginally the smallest.
  if (num(lowest.memoryPercent) < median - 20) return lowest
  return undefined
}

function idleShort(n: ResourceItem): string {
  // Trim the AWS-style FQDN to the recognizable segment.
  const name = String(n.name ?? '')
  const short = name.replace(/\.ec2\.internal$/, '').replace(/\.compute\.internal$/, '')
  return short.length > 22 ? `…${short.slice(-20)}` : short
}
