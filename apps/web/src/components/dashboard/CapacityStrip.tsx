import { Link } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useRightSizing } from '@/hooks/useRightSizing'
import { useCostAvailable } from '@/hooks/useCostAvailable'
import { useNodeRates, estimateMonthlySavings } from '@/hooks/useClusterCost'
import { formatCPU, formatMemory, formatMoney } from '@/utils/formatters'
import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { BarList, EventTrack, KPI_COLOR, Legend, SemiGauge, Sparkline } from '@/components/shared/kpi/MiniCharts'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'
import type { ClusterOverview, ResourceItem } from '@/types/kubernetes'
import { AnchorLink, WorkloadLink } from '@/components/shared/kpi/CardLinks'
import { podWorkload } from '@/utils/insightCases'
import { clockOrDay } from '@/utils/localTime'
import { shortenNodeName } from '@/components/resources/NodesSummaryStrip'

// CapacityStrip — the scan layer above the Capacity charts, in the site's
// card anatomy (KpiCard), the same family as the Overview's row: Peak CPU
// (its curve over the range) · Peak Memory (how much of capacity the peak
// took) · Rightsizing opportunity (primary: the workloads that would hand the
// most back) · OOMKills (when, on the range's track). A card that lights names
// who: the node that peaked highest, the workloads that ran out of memory. Every number derives
// from the same sources as the panels below — the peaks come from
// the SAME series the trend charts plot, the rightsizing totals from
// the SAME hook the recommendations panel renders, the OOM count
// from the SAME KSM query RecentOOMKills lists. No new data sources.
//
// The Rightsizing card keeps reclaimable cores/GiB as its headline
// value (the sizing signal) and, once OpenCost cost rates are
// present, adds the ≈$/mo the reclaim is worth to its sub-line +
// links into the Cost tab (the savings lens). The $/mo comes from the
// SAME node rates the Cost dashboard prices with — one ruler across
// both tabs. Without OpenCost the card degrades to the cores/GiB-only
// line it always showed.

interface Props {
  rangeMinutes: number
  installed: boolean
  overview?: ClusterOverview
}

// Sparkline sampling: ~24 points across the range keeps the query
// cheap at any range while still drawing a recognizable shape.
const SPARK_POINTS = 24

// Same expressions the trend charts plot — peak = max of the series.
const CPU_QUERY = `sum(rate(node_cpu_usage_seconds_total[1m]))`
const MEM_QUERY = `sum(node_memory_working_set_bytes)`
// The same, per node: a lit card names the node that peaked highest against
// its own allocatable, since one node fills up long before the cluster does.
const CPU_BY_NODE = `sum by (node) (rate(node_cpu_usage_seconds_total[1m]))`
const MEM_BY_NODE = `sum by (node) (node_memory_working_set_bytes)`
// When a capacity card lights.
const WARN_PCT = 80
const CRIT_PCT = 90

// Same query RecentOOMKills renders — last-termination timestamps of
// containers whose last exit was an OOMKill. The strip counts the
// ones inside the selected range.
const OOM_QUERY = [
  'kube_pod_container_status_last_terminated_timestamp',
  '* on(uid, namespace, pod, container)',
  '(kube_pod_container_status_last_terminated_reason{reason="OOMKilled"} == 1)',
].join(' ')

const KPI_MIN_WIDTH = 280

// "9.3 Gi" → ["9.3", "Gi"]: the figure and its unit render apart.
function splitUnit(s: string): [string, string] {
  const i = s.lastIndexOf(' ')
  return i < 0 ? [s, ''] : [s.slice(0, i), s.slice(i + 1)]
}

export function CapacityStrip({ rangeMinutes, installed, overview }: Props) {
  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()
  const cpu = usePeakSeries('cpu', CPU_QUERY, rangeMinutes, installed)
  const mem = usePeakSeries('mem', MEM_QUERY, rangeMinutes, installed)
  const cpuNodePeaks = useNodePeaks('cpu', CPU_BY_NODE, rangeMinutes, installed)
  const memNodePeaks = useNodePeaks('mem', MEM_BY_NODE, rangeMinutes, installed)
  const { recs, totals, isLoading: recsLoading, windowDays, preliminary } = useRightSizing(installed, overview)

  // Money layer: when OpenCost cost rates are available, price the
  // reclaimable capacity into $/mo — the same node rates and formula
  // the Cost dashboard uses. Absent OpenCost, savings is 0 and the
  // card shows its cores/GiB-only sub-line.
  const { available: costAvailable } = useCostAvailable()
  const rates = useNodeRates(costAvailable)
  const savingsMonthly = estimateMonthlySavings(
    totals.reclaimCpuMilli,
    totals.reclaimMemBytes,
    rates,
  )
  const showMoney = costAvailable && rates.available && savingsMonthly > 0

  const nodesQ = useQuery({
    // Same key as the Overview's node card: one request for both.
    queryKey: ['kpi', 'nodes'],
    queryFn: () => api.getResources('nodes'),
    enabled: installed,
    retry: false,
  })
  const oomQ = useQuery({
    // Same queryKey as RecentOOMKills so both consumers share one
    // in-flight request + cache entry.
    queryKey: ['recent-oom-kills'],
    queryFn: () => api.queryMetrics({ query: OOM_QUERY }),
    refetchInterval: 30_000,
    enabled: installed,
    retry: false,
  })

  if (!installed) return null

  const cpuCapacity = (overview?.cpu?.allocatable ?? 0) / 1000 // millicores → cores
  const memCapacity = overview?.memory?.allocatable ?? 0
  const cpuPeak = cpu.peak
  const memPeak = mem.peak
  const cpuPct = cpuCapacity > 0 && cpuPeak != null ? (cpuPeak / cpuCapacity) * 100 : null
  const memPct = memCapacity > 0 && memPeak != null ? (memPeak / memCapacity) * 100 : null
  const tier = (pct: number | null) => (pct == null ? undefined : pct >= CRIT_PCT ? 'crit' : pct >= WARN_PCT ? 'warn' : undefined)
  const cpuAlert = tier(cpuPct)
  const memAlert = tier(memPct)
  const nodes = nodesQ.data?.items ?? []
  const cpuNodes = nodePeaks(cpuNodePeaks, nodes, (n) => num(n.cpuAllocatable) / 1000)
  const memNodes = nodePeaks(memNodePeaks, nodes, (n) => num(n.memoryAllocatable))

  // OOMKills inside the selected range, newest first for attribution.
  const cutoff = Date.now() / 1000 - rangeMinutes * 60
  const oomRows = (oomQ.data?.data?.result ?? [])
    .map((s) => ({
      namespace: s.metric.namespace ?? '',
      pod: s.metric.pod ?? '',
      container: s.metric.container ?? '',
      timestamp: parseFloat(s.value?.[1] ?? '0'),
    }))
    .filter((r) => r.pod && r.timestamp >= cutoff)
    .sort((a, b) => b.timestamp - a.timestamp)
  // The workloads that ran out of memory, each once, newest first.
  const oomWorkloads = dedupeBy(
    oomRows.map((r) => ({ namespace: r.namespace, name: podWorkload(r.pod) })),
    (w) => `${w.namespace}/${w.name}`,
  ).slice(0, 2)

  const rangeLabel = formatRange(rangeMinutes)
  const now = Date.now() / 1000

  // The workloads that would hand back the most, on the axis the headline
  // uses (CPU when there is CPU to reclaim, else memory).
  const byCpu = totals.reclaimCpuMilli > 0
  const reclaimRows = recs
    .map((r) => {
      const f = byCpu ? r.cpu : r.mem
      return { name: r.name, value: f.state === 'over' ? Math.max(0, f.request - f.suggest) : 0 }
    })
    .filter((r) => r.value > 0)
    .sort((a, b) => b.value - a.value)
  const reclaimHeadline = byCpu
    ? splitUnit(formatCPU(totals.reclaimCpuMilli))
    : totals.reclaimMemBytes > 0
      ? splitUnit(formatMemory(totals.reclaimMemBytes))
      : null
  const cpuValues = cpu.spark
  const cpuLo = cpuValues.length ? Math.min(...cpuValues) : 0
  const memNow = mem.spark.length ? mem.spark[mem.spark.length - 1] : null
  const [memPeakFig, memPeakUnit] = memPeak != null ? splitUnit(formatMemory(memPeak)) : ['—', '']

  return (
    <div
      ref={gridRef}
      className={`grid gap-4 ${gridWidth ? '' : 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'}`}
      style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, KPI_MIN_WIDTH, 16, [4, 2, 1])}, minmax(0, 1fr))` } : undefined}
    >
      <KpiCard
        alert={cpuAlert}
        value={cpuPeak != null ? cpuPeak.toFixed(1) : '—'}
        unit={cpuCapacity > 0 ? `/ ${Math.round(cpuCapacity)} cores peak` : 'cores peak'}
        description={
          cpuPct != null ? (
            <>
              {Math.round(cpuPct)}% of capacity
              {cpuAlert && cpuNodes[0] ? <> · busiest <NodeLink node={cpuNodes[0].node} /> at {Math.round(cpuNodes[0].pct)}%</> : cpuPct < WARN_PCT ? ' · headroom OK' : ''}
            </>
          ) : (
            'no samples in range'
          )
        }
        viz={
          cpuValues.length >= 2 ? (
            <Sparkline
              values={cpuValues}
              left={`${rangeLabel} · ${cpuLo.toFixed(1)}–${(cpuPeak ?? 0).toFixed(1)} cores`}
            />
          ) : undefined
        }
        caption={
          cpuAlert ? (
            <>
              whole node · <AnchorLink id="top-workloads-cpu">Top workloads · CPU</AnchorLink>
            </>
          ) : (
            'whole node — the OS and kubelet included'
          )
        }
        info={
          <>
            <TooltipHeader right="whole node">{cpuAlert && cpuNodes.length > 0 ? 'Peak per node' : 'Peak CPU'}</TooltipHeader>
            {cpuAlert && cpuNodes.length > 0 ? (
              <NodeRows rows={cpuNodes} color={cpuAlert === 'crit' ? KPI_COLOR.err : KPI_COLOR.warn} fmt={(v) => `${v.toFixed(1)} cores`} />
            ) : (
              <TooltipRow color="#22c55e" label="Scope" value="node total" />
            )}
            <TooltipNote>
              Highest CPU the <b>whole node</b> reached in range — from kubelet's node
              summary, so it includes the OS, kubelet, containerd and kernel on top of
              your pods. Overview's efficiency band counts <b>pods only</b> (Metrics
              Server), so its number is lower; the "pods" line on the chart below shows
              that subset here too. Lights at {WARN_PCT}% of the cluster; a single node can
              be full sooner.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        alert={memAlert}
        value={memPeakFig}
        unit={memCapacity > 0 ? `${memPeakUnit} / ${formatMemory(memCapacity)}` : memPeakUnit}
        description={
          memPct != null ? (
            <>
              {Math.round(memPct)}% of capacity at peak
              {memAlert && memNodes[0] ? <> · busiest <NodeLink node={memNodes[0].node} /> at {Math.round(memNodes[0].pct)}%</> : memPct < WARN_PCT ? ' · headroom OK' : ''}
            </>
          ) : (
            'no samples in range'
          )
        }
        viz={
          memPct != null ? (
            <SemiGauge
              percent={memPct}
              color={memPct >= 80 ? KPI_COLOR.warn : KPI_COLOR.info}
              legend={
                <Legend
                  rows={[
                    { color: memPct >= 80 ? KPI_COLOR.warn : KPI_COLOR.info, label: `peak ${formatMemory(memPeak ?? 0)}` },
                    ...(memNow != null ? [{ color: KPI_COLOR.muted, label: `now ${formatMemory(memNow)}` }] : []),
                  ]}
                />
              }
            />
          ) : undefined
        }
        caption={`whole node · last ${rangeLabel}`}
        info={
          <>
            <TooltipHeader right="whole node">{memAlert && memNodes.length > 0 ? 'Peak per node' : 'Peak memory'}</TooltipHeader>
            {memAlert && memNodes.length > 0 ? (
              <NodeRows rows={memNodes} color={memAlert === 'crit' ? KPI_COLOR.err : KPI_COLOR.warn} fmt={(v) => formatMemory(v)} />
            ) : (
              <TooltipRow color="#3b82f6" label="Scope" value="node total" />
            )}
            <TooltipNote>
              Highest working set the <b>whole node</b> reached in range — includes the
              OS, kubelet, containerd and kernel memory (active page cache) beyond your
              pods, so it runs well above Overview's <b>pod-only</b> figure. Both are
              correct; they measure different things. The "pods" line on the chart below
              shows the workload subset.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        primary
        value={recsLoading ? '…' : reclaimHeadline ? reclaimHeadline[0] : '0'}
        unit={byCpu ? (reclaimHeadline?.[1] === 'cores' ? 'cores' : 'cpu') : reclaimHeadline ? `${reclaimHeadline[1]} memory` : undefined}
        description={
          totals.count > 0
            ? `reclaimable${showMoney ? ` · ≈ ${formatMoney(savingsMonthly, { exact: true })}/mo` : ''} · ${totals.count} ${totals.count === 1 ? 'rec' : 'recs'}`
            : recsLoading
              ? 'computing from 7d P95…'
              : 'well sized — nothing to hand back'
        }
        viz={
          reclaimRows.length > 0 ? (
            <BarList
              color={KPI_COLOR.ok}
              rows={reclaimRows.slice(0, 3).map((r) => ({
                label: r.name,
                value: r.value,
                display: byCpu ? formatCPU(r.value) : formatMemory(r.value),
              }))}
            />
          ) : undefined
        }
        caption={
          <>
            {`P95 over ${windowDays != null ? `${Math.max(1, Math.round(windowDays))}d` : '7d'}`}
            {preliminary && <span className="text-status-warn"> · preliminary</span>}
            {totals.count > 0 && (
              <>
                {' · '}
                <Link to="/cost" className="hover:text-kb-text-primary transition-colors">
                  open Cost →
                </Link>
              </>
            )}
          </>
        }
        info={
          <>
            <TooltipHeader right="P95 over 7d">Rightsizing opportunity</TooltipHeader>
            <TooltipRow color="#22d68a" label="Shows" value="reclaimable capacity" />
            <TooltipNote>
              Total CPU / memory you could hand back by applying the recommendations
              below — the sum of (request − suggested request) across over-provisioned
              workloads. Suggestions come from each workload's P95 usage over 7 days plus
              headroom. When OpenCost cost rates are present, the sentence prices this
              into ≈$/mo; otherwise it's reported as cores / GiB.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        alert={oomRows.length > 0 ? 'warn' : undefined}
        value={oomRows.length}
        unit={oomRows.length === 1 ? 'OOMKill' : 'OOMKills'}
        description={oomRows.length > 0 ? `in the last ${rangeLabel}` : `none in the last ${rangeLabel}`}
        viz={
          <EventTrack
            events={oomRows.map((r) => r.timestamp)}
            from={now - rangeMinutes * 60}
            to={now}
            left={`${rangeLabel} ago`}
          />
        }
        caption={
          oomWorkloads.length > 0 ? (
            <>
              {oomWorkloads.map((w, i) => (
                <span key={`${w.namespace}/${w.name}`}>
                  {i > 0 && ' · '}
                  {w.namespace ? <WorkloadLink namespace={w.namespace} name={w.name} /> : w.name}
                </span>
              ))}
              {' · '}
              <AnchorLink id="oom-kills">all</AnchorLink>
            </>
          ) : (
            'last exit out of memory, per container'
          )
        }
        info={
          oomRows.length > 0 ? (
            <>
              <TooltipHeader right={`last ${rangeLabel}`}>Out of memory</TooltipHeader>
              <div className="space-y-1">
                {oomRows.slice(0, 6).map((r) => (
                  <TooltipRow
                    key={`${r.pod}/${r.container}/${r.timestamp}`}
                    color={KPI_COLOR.warn}
                    label={`${podWorkload(r.pod)} / ${r.container}`}
                    value={<span className="whitespace-nowrap">{clockOrDay(r.timestamp * 1000)}</span>}
                  />
                ))}
              </div>
              {oomRows.length > 6 && <TooltipNote>+{oomRows.length - 6} more below</TooltipNote>}
              <TooltipNote>Each name opens its workload; its Monitor tab shows memory against the limit.</TooltipNote>
            </>
          ) : undefined
        }
      />
    </div>
  )
}

// usePeakSeries — one coarse range query per resource serving both
// the peak number and the sparkline; peak computed client-side so we
// don't pay a second (max_over_time) round-trip.
function usePeakSeries(
  key: string,
  query: string,
  rangeMinutes: number,
  enabled: boolean,
): { peak: number | null; spark: number[] } {
  const step = Math.max(15, Math.round((rangeMinutes * 60) / SPARK_POINTS))
  const q = useQuery({
    queryKey: ['capacity-strip', key, rangeMinutes],
    queryFn: () => {
      const end = Math.floor(Date.now() / 1000)
      return api.queryMetricsRange({
        query,
        start: end - rangeMinutes * 60,
        end,
        step: `${step}s`,
      })
    },
    refetchInterval: 30_000,
    enabled,
    retry: false,
  })
  const values = (q.data?.data?.result?.[0]?.values ?? [])
    .map((p: [number, string]) => parseFloat(p[1]))
    .filter((v: number) => Number.isFinite(v))
  return {
    peak: values.length > 0 ? Math.max(...values) : null,
    spark: values,
  }
}

function formatRange(minutes: number): string {
  if (minutes < 60) return `${minutes}m`
  if (minutes < 1440) return `${Math.round(minutes / 60)}h`
  return `${Math.round(minutes / 1440)}d`
}

function dedupeBy<T>(xs: T[], key: (x: T) => string): T[] {
  const seen = new Set<string>()
  return xs.filter((x) => {
    const k = key(x)
    if (seen.has(k)) return false
    seen.add(k)
    return true
  })
}

const num = (v: unknown) => (typeof v === 'number' ? v : Number(v) || 0)

// useNodePeaks — each node's highest value over the range (same step as the
// sparkline), only queried while there are nodes to name.
function useNodePeaks(key: string, query: string, rangeMinutes: number, enabled: boolean): Map<string, number> {
  const step = Math.max(15, Math.round((rangeMinutes * 60) / SPARK_POINTS))
  const q = useQuery({
    queryKey: ['capacity-strip', key, 'by-node', rangeMinutes],
    queryFn: () => api.queryMetrics({ query: `max_over_time((${query})[${rangeMinutes}m:${step}s])` }),
    refetchInterval: 30_000,
    enabled,
    retry: false,
  })
  const out = new Map<string, number>()
  for (const s of q.data?.data?.result ?? []) {
    const v = parseFloat(s.value?.[1] ?? '')
    if (s.metric?.node && Number.isFinite(v)) out.set(s.metric.node, v)
  }
  return out
}

// nodePeaks — the nodes by how full their peak got against their own
// allocatable, fullest first.
function nodePeaks(peaks: Map<string, number>, nodes: ResourceItem[], allocatable: (n: ResourceItem) => number) {
  return nodes
    .map((n) => {
      const peak = peaks.get(n.name)
      const cap = allocatable(n)
      return peak != null && cap > 0 ? { node: n.name, peak, pct: (peak / cap) * 100 } : null
    })
    .filter((r): r is { node: string; peak: number; pct: number } => r != null)
    .sort((a, b) => b.pct - a.pct)
}

function NodeLink({ node }: { node: string }) {
  return (
    <Link to={`/nodes/_/${node}`} className="underline decoration-dotted underline-offset-2 hover:text-kb-text-primary transition-colors" title={node}>
      {shortenNodeName(node)}
    </Link>
  )
}

function NodeRows({ rows, color, fmt }: { rows: { node: string; peak: number; pct: number }[]; color: string; fmt: (v: number) => ReactNode }) {
  return (
    <div className="space-y-1">
      {rows.slice(0, 3).map((r, i) => (
        <TooltipRow
          key={r.node}
          color={i === 0 ? color : null}
          label={shortenNodeName(r.node)}
          value={<span className="whitespace-nowrap">{Math.round(r.pct)}% · {fmt(r.peak)}</span>}
        />
      ))}
    </div>
  )
}
