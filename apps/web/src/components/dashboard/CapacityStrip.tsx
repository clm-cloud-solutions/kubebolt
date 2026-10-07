import { Link } from 'react-router-dom'
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
import type { ClusterOverview } from '@/types/kubernetes'

// CapacityStrip — the scan layer above the Capacity charts, in the site's
// card anatomy (KpiCard), the same family as the Overview's row: Peak CPU
// (its curve over the range) · Peak Memory (how much of capacity the peak
// took) · Rightsizing opportunity (primary: the workloads that would hand the
// most back) · OOMKills (when, on the range's track). Every number derives
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

  // OOMKills inside the selected range, newest first for attribution.
  const cutoff = Date.now() / 1000 - rangeMinutes * 60
  const oomRows = (oomQ.data?.data?.result ?? [])
    .map((s) => ({
      pod: s.metric.pod ?? '',
      timestamp: parseFloat(s.value?.[1] ?? '0'),
    }))
    .filter((r) => r.pod && r.timestamp >= cutoff)
    .sort((a, b) => b.timestamp - a.timestamp)
  const oomNames = dedupe(oomRows.map((r) => shortenPodName(r.pod))).slice(0, 2)

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
        alert={cpuPct == null ? undefined : cpuPct >= 90 ? 'crit' : cpuPct >= 80 ? 'warn' : undefined}
        value={cpuPeak != null ? cpuPeak.toFixed(1) : '—'}
        unit={cpuCapacity > 0 ? `/ ${Math.round(cpuCapacity)} cores peak` : 'cores peak'}
        description={
          cpuPct != null
            ? `${Math.round(cpuPct)}% of capacity${cpuPct < 80 ? ' · headroom OK' : ''}`
            : 'no samples in range'
        }
        viz={
          cpuValues.length >= 2 ? (
            <Sparkline
              values={cpuValues}
              left={`${rangeLabel} · ${cpuLo.toFixed(1)}–${(cpuPeak ?? 0).toFixed(1)} cores`}
            />
          ) : undefined
        }
        caption="whole node — the OS and kubelet included"
        info={
          <>
            <TooltipHeader right="whole node">Peak CPU</TooltipHeader>
            <TooltipRow color="#22c55e" label="Scope" value="node total" />
            <TooltipNote>
              Highest CPU the <b>whole node</b> reached in range — from kubelet's node
              summary, so it includes the OS, kubelet, containerd and kernel on top of
              your pods. Overview's efficiency band counts <b>pods only</b> (Metrics
              Server), so its number is lower; the "pods" line on the chart below shows
              that subset here too.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        alert={memPct == null ? undefined : memPct >= 90 ? 'crit' : memPct >= 80 ? 'warn' : undefined}
        value={memPeakFig}
        unit={memCapacity > 0 ? `${memPeakUnit} / ${formatMemory(memCapacity)}` : memPeakUnit}
        description={
          memPct != null
            ? `${Math.round(memPct)}% of capacity at peak${memPct < 80 ? ' · headroom OK' : ''}`
            : 'no samples in range'
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
            <TooltipHeader right="whole node">Peak memory</TooltipHeader>
            <TooltipRow color="#3b82f6" label="Scope" value="node total" />
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
        caption={oomRows.length > 0 ? oomNames.join(' · ') : 'last exit out of memory, per container'}
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

// shortenPodName strips the ReplicaSet + pod hash suffixes so the
// attribution line reads "payments-api", not "payments-api-3b1c9-x7f2".
function shortenPodName(pod: string): string {
  return pod.replace(/-[a-z0-9]{6,12}-[a-z0-9]{5}$/, '').replace(/-[a-z0-9]{5}$/, '')
}

function dedupe(xs: string[]): string[] {
  return [...new Set(xs)]
}
