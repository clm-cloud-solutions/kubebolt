import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { BarList, KPI_COLOR, Legend, NodeRings, SemiGauge, UnitStrip } from '@/components/shared/kpi/MiniCharts'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'
import type { ResourceItem, ClusterOverview } from '@/types/kubernetes'

// NodesSummaryStrip is the scan layer above the node grid, in the site's
// card anatomy (KpiCard) like every other headline row: Nodes ready (one
// cell per node) · Bin-packing (requested CPU and memory) · Pods scheduled
// (share of pod capacity) · Under pressure (the busiest nodes' rings, with
// the consolidation observation in its caption). Every number is derived
// from data the page already has — node items (usage %, pod counts,
// cordon) + the cluster overview (requested vs allocatable). No new
// backend, no cost data.
//
// DELIBERATELY NO $/mo and NO "drainable" claim in the consolidation
// line: a trustworthy "N nodes drainable" needs a scheduling-aware
// bin-packing simulation (taints/affinity/PDB/topology/local-PV), and
// the dollar figure needs node pricing (the OpenCost/Cost slice, not
// in EE yet). Until both exist, the line is an OBSERVATION — "the
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
  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()
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
  const reqKnown = cpuReqPct != null && memReqPct != null
  const cpuReq = clampPct(cpuReqPct)
  const memReq = clampPct(memReqPct)
  const binding = reqKnown ? bindingInsight(cpuReq, memReq) : null

  // Least-loaded node by memory usage (the binding resource in most
  // clusters) — surfaced as the consolidation observation, no claim.
  const idle = leastLoadedNode(nodes)

  // One cell per node: not ready, then cordoned, then ready.
  const cells = [...nodes]
    .map((n) => ({ n, rank: n.status !== 'Ready' ? 0 : isUnschedulable(n) ? 1 : 2 }))
    .sort((a, b) => a.rank - b.rank)
    .map(({ n, rank }) => ({
      color: rank === 0 ? KPI_COLOR.err : KPI_COLOR.ok,
      hatched: rank === 1,
      title: `${n.name} — ${rank === 0 ? 'not ready' : rank === 1 ? 'cordoned' : 'ready'}`,
    }))

  // The busiest nodes, as the Overview's node card draws them.
  const busiest = withShortNames(
    [...nodes]
      .map((n) => ({ name: String(n.name ?? ''), cpu: num(n.cpuPercent), mem: num(n.memoryPercent) }))
      .sort((a, b) => Math.max(b.cpu, b.mem) - Math.max(a.cpu, a.mem))
      .slice(0, 2),
  )

  const podNodes = withShortNames(
    [...nodes]
      .sort((a, b) => num(b.podCount) - num(a.podCount))
      .slice(0, 2)
      .map((n) => ({ name: String(n.name ?? ''), pods: num(n.podCount) })),
    14,
  )
  // Reserved capacity: green with plenty of headroom, amber past 70 %, red
  // past 90 % — the scheduler starts struggling to place pods as it fills.
  const reqColor = (p: number) => (p >= 90 ? KPI_COLOR.err : p >= 70 ? KPI_COLOR.warn : KPI_COLOR.ok)

  return (
    <div
      ref={gridRef}
      className={`grid gap-4 mb-5 ${gridWidth ? '' : 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'}`}
      style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, 280, 16, [4, 2, 1])}, minmax(0, 1fr))` } : undefined}
    >
      <KpiCard
        primary
        alert={ready < total ? 'crit' : cordoned > 0 ? 'warn' : undefined}
        value={ready}
        unit={`/ ${total} ready`}
        description={cordoned > 0 ? `${cordoned} cordoned` : 'all schedulable'}
        viz={
          total > 0 ? (
            <UnitStrip
              cells={cells}
              legend={
                <div className="flex flex-wrap gap-x-3 gap-y-1">
                  <Legend rows={[{ color: KPI_COLOR.ok, label: `${ready - cordoned} schedulable` }]} />
                  {cordoned > 0 && <Legend rows={[{ color: KPI_COLOR.muted, label: `${cordoned} cordoned` }]} />}
                  {total - ready > 0 && <Legend rows={[{ color: KPI_COLOR.err, label: `${total - ready} not ready` }]} />}
                </div>
              }
            />
          ) : undefined
        }
        caption="one cell per node"
      />

      <KpiCard
        alert={!reqKnown ? undefined : Math.max(cpuReq, memReq) >= 90 ? 'crit' : Math.max(cpuReq, memReq) >= 70 ? 'warn' : undefined}
        value={reqKnown ? Math.round(Math.max(cpuReq, memReq)) : '—'}
        unit="% requested"
        description={binding ? binding.text : 'requested vs allocatable unavailable'}
        viz={
          reqKnown ? (
            <BarList
              max={100}
              color={reqColor(Math.max(cpuReq, memReq))}
              rows={[
                { label: 'CPU', value: cpuReq, display: `${Math.round(cpuReq)}%` },
                { label: 'Memory', value: memReq, display: `${Math.round(memReq)}%` },
              ]}
            />
          ) : undefined
        }
        caption="what pod specs reserve, not live usage"
        info={
          <>
            <TooltipHeader right="scheduler view">Bin-packing</TooltipHeader>
            <TooltipNote>
              CPU and memory reserved by pod requests against what the nodes can allocate — what
              the scheduler sees when it places a pod, not what the pods use. When one resource
              runs far ahead of the other, the lagging one is stranded: paid for, but it cannot
              be scheduled against.
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        alert={podsPct >= 90 ? 'warn' : undefined}
        value={podsScheduled}
        unit={podsCapacity > 0 ? `/ ${podsCapacity} pods` : 'pods'}
        description={podsCapacity > 0 ? `${Math.round(podsPct)}% of pod capacity` : 'pod capacity unknown'}
        viz={
          podsCapacity > 0 ? (
            <SemiGauge
              percent={podsPct}
              color={podsPct >= 90 ? KPI_COLOR.warn : KPI_COLOR.ok}
              legend={
                <Legend
                  rows={[
                    { color: podsPct >= 90 ? KPI_COLOR.warn : KPI_COLOR.ok, label: `${podsScheduled} scheduled` },
                    { color: KPI_COLOR.muted, label: `${Math.max(0, podsCapacity - podsScheduled)} free` },
                  ]}
                />
              }
            />
          ) : undefined
        }
        caption={
          podNodes.length > 0
            ? podNodes.map((n) => `${n.name} ${n.pods}`).join(' · ') + (nodes.length > podNodes.length ? ` · +${nodes.length - podNodes.length}` : '')
            : undefined
        }
        info={
          <>
            <TooltipHeader>Pods scheduled</TooltipHeader>
            <TooltipNote>
              Pods placed on the nodes against the kubelet's max-pods summed across them. The
              caption names the nodes carrying the most.
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        alert={underPressure > 0 ? 'warn' : undefined}
        value={underPressure}
        unit="under pressure"
        description={underPressure > 0 ? `nodes above ${MEM_PRESSURE_PCT}% memory` : 'no memory pressure'}
        viz={busiest.length > 0 ? <NodeRings nodes={busiest} /> : undefined}
        caption={
          idle ? (
            <span title={String(idle.name ?? '')}>
              least loaded · {idleShort(idle)} {Math.round(num(idle.memoryPercent))}% mem — review to consolidate
            </span>
          ) : (
            'no clearly idle node to consolidate'
          )
        }
        info={
          <>
            <TooltipHeader right="observation">Pressure &amp; consolidation</TooltipHeader>
            <TooltipRow color="#4c9aff" label="Rings" value="cpu inside · memory outside" />
            <TooltipNote>
              The busiest nodes right now, and — when one stands well below the rest — the node
              using the least memory, a candidate to review for consolidation. It is <b>not</b> a
              "drainable" verdict: confirming a node can be freed needs a scheduling-aware
              simulation (taints, affinity, PodDisruptionBudgets, local volumes), and the $/mo
              saving needs node pricing. Treat it as a starting point, not a promise.
            </TooltipNote>
          </>
        }
      />
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

// Managed node pools bury the identity at DIFFERENT ends depending on the cloud,
// so a one-sided trim is right for exactly one of them.
//
//   AWS   ip-10-0-1-23.ec2.internal          identity in the TAIL ("ip-" is noise)
//   AKS   aks-tmpdefault-30800577-vmss000000 identity in the HEAD (pool name)
//   GKE   gke-<cluster>-<pool>-<hash>-<sfx>  identity in the HEAD
//
// This used to keep the last 20 characters, which is correct for AWS and cuts
// exactly the wrong end everywhere else: an AKS node rendered as
// "…-30800577-vmss000000", where the visible half is what every node in the pool
// shares and "aks-tmpdefault" — the only distinguishing part — was gone.
//
// So elide the MIDDLE, on a segment boundary. The generated hash lives there and
// nobody reads it, while both surviving halves say something: the pool and the
// instance ordinal.
export function shortenNodeName(name: string, max = 26): string {
  const short = String(name ?? '')
    .replace(/\.ec2\.internal$/, '')
    .replace(/\.compute\.internal$/, '')
  if (short.length <= max) return short

  const parts = short.split('-')
  if (parts.length >= 4) {
    const collapsed = `${parts[0]}-${parts[1]}…${parts[parts.length - 1]}`
    if (collapsed.length <= max) return collapsed
  }
  // No usable segments (or still too long): fall back to a character split that
  // favours the head, since that is where the identity sits on every cloud but
  // AWS — and AWS names are short enough to never reach this branch.
  const head = Math.max(1, Math.ceil((max - 1) * 0.6))
  return `${short.slice(0, head)}…${short.slice(-(max - 1 - head))}`
}

function idleShort(n: ResourceItem): string {
  return shortenNodeName(String(n.name ?? ''))
}

// withShortNames drops the prefix every shown node shares (on a "-" boundary:
// "kubebolt-dev-control-plane" / "kubebolt-dev-worker" → "control-plane" /
// "worker") — it is the cluster's name, already in the page header — then
// elides what is still long the way shortenNodeName does.
export function withShortNames<T extends { name: string }>(nodes: T[], max = 20): T[] {
  if (nodes.length < 2) return nodes.map((n) => ({ ...n, name: shortenNodeName(n.name, max) }))
  let prefix = nodes[0].name
  for (const n of nodes) while (!n.name.startsWith(prefix)) prefix = prefix.slice(0, -1)
  const cut = prefix.lastIndexOf('-') + 1
  return nodes.map((n) => ({ ...n, name: shortenNodeName(n.name.slice(cut) || n.name, max) }))
}
