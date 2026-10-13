import { Link } from 'react-router-dom'
import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { BarList, KPI_COLOR, Legend, NodeRings, SemiGauge, UnitStrip } from '@/components/shared/kpi/MiniCharts'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'
import type { ResourceItem, ClusterOverview } from '@/types/kubernetes'
import { clockOrDay } from '@/utils/localTime'
import { formatAge } from '@/utils/formatters'

// NodesSummaryStrip is the scan layer above the node grid, in the site's
// card anatomy (KpiCard) like every other headline row: Nodes ready (one
// cell per node) · Bin-packing (requested CPU and memory) · Pods scheduled
// (share of pod capacity) · Under pressure (the busiest nodes' rings, with
// the consolidation observation in its caption). Every number is derived
// from data the page already has — node items (usage %, pod counts,
// cordon) + the cluster overview (requested vs allocatable). No new
// backend, no cost data. A card that lights names the node: the one not
// ready (and since when, from its Ready condition), the fullest of pods, the
// one under pressure. Bin-packing can't: requests per node are only in a
// node's detail, not in this list.
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

  const pressured = [...nodes]
    .filter((n) => num(n.memoryPercent) > MEM_PRESSURE_PCT)
    .sort((a, b) => num(b.memoryPercent) - num(a.memoryPercent))
  const underPressure = pressured.length
  // Not ready, oldest first (the first to go is usually the cause); then cordoned.
  const notReady = nodes
    .filter((n) => n.status !== 'Ready')
    .sort((a, b) => (Date.parse(readySince(a) ?? '') || 0) - (Date.parse(readySince(b) ?? '') || 0))
  const cordonedNodes = nodes.filter((n) => n.status === 'Ready' && isUnschedulable(n))
  const fullest = [...nodes]
    .filter((n) => num(n.podCapacity) > 0)
    .map((n) => ({ name: String(n.name ?? ''), pods: num(n.podCount), pct: (num(n.podCount) / num(n.podCapacity)) * 100 }))
    .sort((a, b) => b.pct - a.pct)

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
        // The sentence follows the colour: a NotReady node lit the card red
        // while it still said "all schedulable".
        description={
          notReady[0] ? (
            <>
              {total - ready} not ready · <NodeLink name={String(notReady[0].name)} />
              {readySince(notReady[0]) && <> since {clockOrDay(readySince(notReady[0]) as string)}</>}
            </>
          ) : cordonedNodes[0] ? (
            <>
              {cordoned} cordoned · <NodeLink name={String(cordonedNodes[0].name)} />
            </>
          ) : (
            'all schedulable'
          )
        }
        info={
          notReady.length + cordonedNodes.length > 0 ? (
            <>
              <TooltipHeader right="since">Not schedulable</TooltipHeader>
              <div className="space-y-1">
                {notReady.map((n) => {
                  const since = readySince(n)
                  return (
                    <TooltipRow
                      key={String(n.name)}
                      color={KPI_COLOR.err}
                      label={shortenNodeName(String(n.name))}
                      value={<span className="whitespace-nowrap">{since ? `not ready · ${clockOrDay(since)} · ${formatAge(since)}` : 'not ready'}</span>}
                    />
                  )
                })}
                {cordonedNodes.map((n) => (
                  <TooltipRow key={String(n.name)} color={KPI_COLOR.muted} label={shortenNodeName(String(n.name))} value="cordoned" />
                ))}
              </div>
            </>
          ) : undefined
        }
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
          podsPct >= 90 && fullest[0] ? (
            <>
              fullest · <NodeLink name={fullest[0].name} /> {Math.round(fullest[0].pct)}%
            </>
          ) : podNodes.length > 0 ? (
            podNodes.map((n) => `${n.name} ${n.pods}`).join(' · ') + (nodes.length > podNodes.length ? ` · +${nodes.length - podNodes.length}` : '')
          ) : undefined
        }
        info={
          <>
            <TooltipHeader>Pods scheduled</TooltipHeader>
            {podsPct >= 90 && fullest.length > 0 && (
              <div className="space-y-1">
                {fullest.slice(0, 3).map((n) => (
                  <TooltipRow
                    key={n.name}
                    color={n.pct >= 90 ? KPI_COLOR.warn : null}
                    label={shortenNodeName(n.name)}
                    value={<span className="whitespace-nowrap">{Math.round(n.pct)}% · {n.pods} pods</span>}
                  />
                ))}
              </div>
            )}
            <TooltipNote>
              Pods placed on the nodes against the kubelet's max-pods summed across them. The
              caption names the nodes carrying the most. Lights at 90 % of the cluster's pod capacity.
            </TooltipNote>
          </>
        }
      />

      <KpiCard
        alert={underPressure > 0 ? 'warn' : undefined}
        value={underPressure}
        unit="under pressure"
        description={
          pressured[0] ? (
            <>
              above {MEM_PRESSURE_PCT}% memory · <NodeLink name={String(pressured[0].name)} /> at {Math.round(num(pressured[0].memoryPercent))}%
            </>
          ) : (
            'no memory pressure'
          )
        }
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
            {pressured.length > 0 ? (
              <div className="space-y-1">
                {pressured.slice(0, 4).map((n) => (
                  <TooltipRow
                    key={String(n.name)}
                    color={KPI_COLOR.warn}
                    label={shortenNodeName(String(n.name))}
                    value={<span className="whitespace-nowrap">{Math.round(num(n.memoryPercent))}% mem · {Math.round(num(n.cpuPercent))}% cpu</span>}
                  />
                ))}
              </div>
            ) : (
              <TooltipRow color="#4c9aff" label="Rings" value="cpu inside · memory outside" />
            )}
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

// readySince — when the node's Ready condition last changed: for a node not
// ready, since when.
function readySince(n: ResourceItem): string | undefined {
  const conditions = (n as unknown as { conditions?: { type: string; lastTransitionTime?: string }[] }).conditions
  return conditions?.find((c) => c.type === 'Ready')?.lastTransitionTime || undefined
}

function NodeLink({ name }: { name: string }) {
  return (
    <Link
      to={`/nodes/_/${name}`}
      title={name}
      className="underline decoration-dotted underline-offset-2 hover:text-kb-text-primary transition-colors"
    >
      {shortenNodeName(name)}
    </Link>
  )
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
