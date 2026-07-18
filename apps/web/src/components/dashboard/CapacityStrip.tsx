import { useQuery } from '@tanstack/react-query'
import { Zap } from 'lucide-react'
import { api } from '@/services/api'
import { useRightSizing } from '@/hooks/useRightSizing'
import { formatCPU, formatMemory } from '@/utils/formatters'
import { StripCard } from './StripCard'
import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import type { ClusterOverview } from '@/types/kubernetes'

// CapacityStrip — the scan layer above the Capacity charts (design/
// kubebolt-capacity-redesign.html): Peak CPU · Peak Memory ·
// Rightsizing opportunity (hero) · OOMKills. Every number derives
// from the same sources as the panels below — the peaks come from
// the SAME series the trend charts plot, the rightsizing totals from
// the SAME hook the recommendations panel renders, the OOM count
// from the SAME KSM query RecentOOMKills lists. No new data sources.
//
// The Rightsizing card deliberately reports reclaimable cores/GiB,
// NOT $/mo: real currency needs per-node pricing, which arrives with
// the OpenCost integration (Cost slice). When that lands, this card
// is where the ≈$/mo figure appears.

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

export function CapacityStrip({ rangeMinutes, installed, overview }: Props) {
  const cpu = usePeakSeries('cpu', CPU_QUERY, rangeMinutes, installed)
  const mem = usePeakSeries('mem', MEM_QUERY, rangeMinutes, installed)
  const { totals, isLoading: recsLoading } = useRightSizing(installed, overview)

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

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
      <StripCard
        label={`Peak CPU (${rangeLabel})`}
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
        value={cpuPeak != null ? cpuPeak.toFixed(1) : '—'}
        valueSuffix={cpuCapacity > 0 ? `/ ${Math.round(cpuCapacity)}` : 'cores'}
        sub={
          cpuPct != null
            ? `${Math.round(cpuPct)}% of capacity${cpuPct < 80 ? ' · headroom OK' : ''}`
            : 'no samples in range'
        }
        subAccent={cpuPct != null && cpuPct >= 80 ? 'warn' : 'default'}
        spark={cpu.spark}
        sparkAccent="ok"
      />
      <StripCard
        label={`Peak memory (${rangeLabel})`}
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
        value={memPeak != null ? formatMemory(memPeak) : '—'}
        valueSuffix={memCapacity > 0 ? `/ ${formatMemory(memCapacity)}` : undefined}
        sub={
          memPct != null
            ? `${Math.round(memPct)}% of capacity${memPct < 80 ? ' · headroom OK' : ''}`
            : 'no samples in range'
        }
        subAccent={memPct != null && memPct >= 80 ? 'warn' : 'default'}
        spark={mem.spark}
        sparkAccent="info"
      />
      <StripCard
        hero
        label="Rightsizing opportunity"
        icon={<Zap className="w-3 h-3" />}
        info={
          <>
            <TooltipHeader right="P95 over 7d">Rightsizing opportunity</TooltipHeader>
            <TooltipRow color="#22d68a" label="Shows" value="reclaimable capacity" />
            <TooltipNote>
              Total CPU / memory you could hand back by applying the recommendations
              below — the sum of (request − suggested request) across over-provisioned
              workloads. Suggestions come from each workload's P95 usage over 7 days plus
              headroom. Reported as cores / GiB, not $/mo: currency needs per-node
              pricing, which arrives with the cost integration.
            </TooltipNote>
          </>
        }
        value={
          recsLoading
            ? '…'
            : totals.reclaimCpuMilli > 0
              ? formatCPU(totals.reclaimCpuMilli)
              : totals.reclaimMemBytes > 0
                ? formatMemory(totals.reclaimMemBytes)
                : '0'
        }
        valueAccent={totals.count > 0 ? 'ok' : 'default'}
        sub={
          totals.count > 0
            ? `${reclaimSummary(totals.reclaimCpuMilli, totals.reclaimMemBytes)} · ${totals.count} ${
                totals.count === 1 ? 'rec' : 'recs'
              } →`
            : recsLoading
              ? 'computing from 7d P95…'
              : 'no recommendations — well sized'
        }
        subAccent={totals.count > 0 ? 'ok' : 'default'}
      />
      <StripCard
        label={`OOMKills (${rangeLabel})`}
        value={`${oomRows.length}`}
        valueAccent={oomRows.length > 0 ? 'warn' : 'default'}
        sub={oomRows.length > 0 ? oomNames.join(' · ') : 'none in range'}
        subAccent={oomRows.length > 0 ? 'warn' : 'default'}
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

function reclaimSummary(cpuMilli: number, memBytes: number): string {
  const parts: string[] = []
  if (cpuMilli > 0) parts.push(formatCPU(cpuMilli))
  if (memBytes > 0) parts.push(formatMemory(memBytes))
  return `${parts.join(' + ')} reclaimable`
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
