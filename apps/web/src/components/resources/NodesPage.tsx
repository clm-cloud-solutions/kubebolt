import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Activity } from 'lucide-react'
import { useResources } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { UsageBar } from './UsageBar'
import { NodeActionMenu } from './NodeActionMenu'
import { DrainModal } from './DrainModal'
import { AgentRequiredPlaceholder } from '@/components/shared/AgentRequiredPlaceholder'
import { MetricChart } from '@/components/shared/MetricChart'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'
import { api } from '@/services/api'
import { formatCPU, formatMemory } from '@/utils/formatters'
import {
  useNodeStress,
  classifyPSI,
  PSI_WARN,
  PSI_CRIT,
  type NodeStress,
} from '@/hooks/useNodeStress'
import type { ResourceItem } from '@/types/kubernetes'

function NodeCard({
  node,
  onDrain,
  stress,
}: {
  node: ResourceItem
  onDrain: (node: ResourceItem) => void
  stress?: NodeStress
}) {
  const cpuPercent = Number(node.cpuPercent ?? 0)
  const memPercent = Number(node.memoryPercent ?? 0)
  const cpuUsage = Number(node.cpuUsage ?? 0)
  const memUsage = Number(node.memoryUsage ?? 0)
  const cpuAlloc = Number(node.cpuAllocatable ?? 0)
  const memAlloc = Number(node.memoryAllocatable ?? 0)
  const podCount = Number(node.podCount ?? 0)
  const podCapacity = Number(node.podCapacity ?? 110)
  const kubeletVersion = (node.kubeletVersion as string) ?? ''
  const containerRuntime = (node.containerRuntime as string) ?? ''
  const hasMetrics = cpuUsage > 0 || memUsage > 0
  const unschedulable = (node as unknown as { unschedulable?: boolean }).unschedulable === true

  return (
    <Link to={`/nodes/_/${node.name}`} className="block bg-kb-card border border-kb-border rounded-[10px] p-4 hover:bg-kb-card-hover transition-colors">
      {/* Header */}
      <div className="flex items-center gap-2.5 mb-3">
        <div className={`w-2.5 h-2.5 rounded-full ${node.status === 'Ready' ? 'bg-status-ok' : 'bg-status-error'}`} />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-1.5">
            <div className="text-[13px] font-semibold text-kb-text-primary truncate">{node.name}</div>
            {unschedulable && (
              <span
                className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-status-warn-dim text-status-warn uppercase tracking-wide whitespace-nowrap"
                title="Node is cordoned — new pods will not be scheduled here"
              >
                SchedulingDisabled
              </span>
            )}
          </div>
          <div className="text-[10px] font-mono text-kb-text-tertiary">{(node.labels as Record<string, string>)?.['node.kubernetes.io/instance-type'] || ''}</div>
        </div>
        <NodeActionMenu node={node} onDrain={onDrain} />
      </div>

      {/* Bars */}
      <div className="space-y-2.5">
        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-kb-text-tertiary">CPU</span>
            <span className="text-[10px] font-mono text-kb-text-secondary">
              {hasMetrics ? `${Math.round(cpuPercent)}% · ${formatCPU(cpuUsage)}/${formatCPU(cpuAlloc)}` : `${formatCPU(cpuAlloc)} alloc`}
            </span>
          </div>
          <UsageBar percent={cpuPercent} height={6} />
        </div>

        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-kb-text-tertiary">Mem</span>
            <span className="text-[10px] font-mono text-kb-text-secondary">
              {hasMetrics ? `${Math.round(memPercent)}% · ${formatMemory(memUsage)}/${formatMemory(memAlloc)}` : `${formatMemory(memAlloc)} alloc`}
            </span>
          </div>
          <UsageBar percent={memPercent} height={6} />
        </div>

        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] text-kb-text-tertiary">Pods</span>
            <span className="text-[10px] font-mono text-kb-text-secondary">{podCount}/{podCapacity}</span>
          </div>
          <UsageBar percent={podCapacity > 0 ? (podCount / podCapacity) * 100 : 0} height={6} />
        </div>
      </div>

      {/* Load + PSI row — only renders when node-exporter is shipping
          (stress map populated). Load is always shown when present;
          the PSI badge is gated on the WARN threshold so quiet
          nodes stay quiet. The Activity icon next to the badge
          telegraphs "this is a stress signal" before the user
          parses the percentage. Tooltip carries the full per-axis
          breakdown so an operator hovering on a yellow badge can
          tell whether it's CPU, IO, or memory binding. */}
      {stress && (
        <NodeStressRow stress={stress} />
      )}

      {/* Footer */}
      <div className="mt-3 text-[10px] font-mono text-kb-text-tertiary">
        {kubeletVersion}{containerRuntime ? ` · ${containerRuntime}` : ''}
      </div>
    </Link>
  )
}

function NodeStressRow({ stress }: { stress: NodeStress }) {
  const psi = classifyPSI(stress)
  const worstAxis = (() => {
    const m = Math.max(stress.psiCpu, stress.psiIo, stress.psiMemory)
    if (m === stress.psiCpu) return 'cpu'
    if (m === stress.psiIo) return 'io'
    return 'memory'
  })()
  const worstPct = Math.max(stress.psiCpu, stress.psiIo, stress.psiMemory) * 100
  const psiTooltip = (
    <>
      <TooltipHeader right={`${worstPct.toFixed(1)}% on ${worstAxis}`}>
        Pressure (PSI)
      </TooltipHeader>
      <div className="space-y-1">
        <TooltipRow
          color={psiColor(stress.psiCpu)}
          label="cpu"
          value={`${(stress.psiCpu * 100).toFixed(1)}%`}
        />
        <TooltipRow
          color={psiColor(stress.psiIo)}
          label="io"
          value={`${(stress.psiIo * 100).toFixed(1)}%`}
        />
        <TooltipRow
          color={psiColor(stress.psiMemory)}
          label="memory"
          value={`${(stress.psiMemory * 100).toFixed(1)}%`}
        />
      </div>
      <div className="mt-2 pt-2 border-t border-kb-border text-[10px] text-kb-text-tertiary leading-snug">
        Fraction of last 1m at least one task was waiting. {Math.round(PSI_WARN * 100)}% triggers watch, {Math.round(PSI_CRIT * 100)}% page.
      </div>
    </>
  )

  return (
    <div className="mt-3 flex items-center justify-between text-[10px] font-mono">
      <span className="text-kb-text-tertiary">
        Load <span className="text-kb-text-secondary tabular-nums">{stress.load1.toFixed(2)}</span>
        <span className="text-kb-text-tertiary/60"> · </span>
        <span className="text-kb-text-secondary tabular-nums">{stress.load5.toFixed(2)}</span>
        <span className="text-kb-text-tertiary/60"> · </span>
        <span className="text-kb-text-secondary tabular-nums">{stress.load15.toFixed(2)}</span>
      </span>
      {psi && (
        <HoverTooltip body={psiTooltip}>
          <span
            className={`flex items-center gap-1 px-1.5 py-0.5 rounded ${
              psi === 'crit'
                ? 'bg-status-error-dim text-status-error'
                : 'bg-status-warn-dim text-status-warn'
            }`}
            onClick={(e) => {
              // The card itself is wrapped in a <Link>, so any click
              // bubbles to navigation. The badge is purely informational
              // — keep clicks local so hovering for the tooltip doesn't
              // accidentally drill in.
              e.preventDefault()
              e.stopPropagation()
            }}
          >
            <Activity className="w-3 h-3" />
            <span className="text-[9px] font-semibold uppercase tracking-[0.04em]">
              PSI {worstPct.toFixed(0)}%
            </span>
          </span>
        </HoverTooltip>
      )}
    </div>
  )
}

function psiColor(v: number): string {
  if (v >= PSI_CRIT) return '#ef4056'
  if (v >= PSI_WARN) return '#f5a623'
  return '#555770'
}

export function NodesPage() {
  const { data, isLoading, error, refetch, dataUpdatedAt, isFetching } = useResources('nodes')
  // Stress data is fetched once at the page level so 3 VM queries
  // (load + PSI waiting) run for the whole list, not N. Each card
  // reads its own slice from the map. Returns an empty map when
  // node-exporter isn't shipping — cards gracefully drop the
  // load/PSI row.
  const { stress } = useNodeStress()
  // Drain modal lives at the page level rather than per-card so a
  // single instance can render even when the operator opens it from
  // any node card. Keeps state from leaking into NodeCard re-renders.
  const [drainTarget, setDrainTarget] = useState<ResourceItem | null>(null)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const nodes = data?.items || []

  return (
    <div>
      <div className="flex items-center gap-3 mb-4">
        <h1 className="text-lg font-semibold text-kb-text-primary">Nodes</h1>
        <span className="text-[10px] font-mono px-2.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary">
          {nodes.length} total
        </span>
        <div className="ml-auto">
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
      </div>
      <div className="grid grid-cols-3 gap-3 mb-5">
        {nodes.map((node) => (
          <NodeCard
            key={node.name}
            node={node}
            onDrain={setDrainTarget}
            stress={stress[String(node.name)]}
          />
        ))}
      </div>
      <NodeFleetCharts />
      {drainTarget && (
        <DrainModal node={drainTarget} onClose={() => setDrainTarget(null)} />
      )}
    </div>
  )
}

// NodeFleetCharts renders per-node disk and network activity in two
// multi-series charts. The agent emits node_fs_used_bytes and
// node_network_{receive,transmit}_bytes_total with a `node` label,
// which MetricChart auto-picks up as one series per node without
// any extra config. When the agent is absent we fall back to the
// install prompt — same information, no misleading empty charts.
function NodeFleetCharts() {
  const { data: agent, isLoading } = useQuery({
    queryKey: ['integration', 'agent'],
    queryFn: () => api.getIntegration('agent'),
    refetchInterval: 10_000,
    staleTime: 5_000,
  })

  if (isLoading) return null

  const installed = agent && (agent.status === 'installed' || agent.status === 'degraded')
  if (!installed) {
    return (
      <AgentRequiredPlaceholder
        title="Disk I/O & Network per node"
        description="Detailed node metrics require the KubeBolt Agent. Install it from Administration → Integrations to unlock per-node disk and network charts on this page."
      />
    )
  }

  // Network chart mirrors the Overview's "RX up / TX down" layout
  // but scoped per-node. Using two separate queries (rather than
  // adding inside the rate()) avoids a PromQL vector-match failure
  // when RX and TX series carry different auxiliary labels: the
  // outer sum by (node) collapses each side to one series per node
  // first, after which the halves can share one chart cleanly with
  // TX negated below the axis.
  //
  // No accents override: with N nodes × 2 directions we'd end up
  // with all RX lines in one color and all TX in another, losing
  // the per-node distinction. The default palette picks a unique
  // hue per series ("RX worker", "TX worker", "RX control-plane",
  // "TX control-plane"), and the series label keeps the direction
  // visible in the tooltip and legend.
  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <MetricChart
        title="Filesystem used per node"
        unit="bytes"
        query="node_fs_used_bytes"
        chartType="area"
      />
      <MetricChart
        title="Network activity per node (RX up / TX down)"
        unit="bytes/s"
        queries={[
          // device filter excludes virtual interfaces (cilium_*, lxc*,
          // veth*, cali*, flannel*, gre*, tunl*, lo) that double/triple-count
          // the same packet as it traverses the CNI overlay. eth0/ens5/eno1
          // is the physical NIC — what actually crosses the node boundary.
          // Without this filter the dashboard inflated 6-8× on kind/yagan
          // (verified against CloudWatch ENI metrics and per-device topk).
          // Keep in sync with apps/api/internal/copilot/workload_metrics.go's
          // nodeNetworkDeviceFilter constant.
          { query: 'sum by (node) (rate(node_network_receive_bytes_total{device=~"eth.*|ens.*|en[a-z].*"}[1m]))', prefix: 'RX' },
          { query: 'sum by (node) (rate(node_network_transmit_bytes_total{device=~"eth.*|ens.*|en[a-z].*"}[1m]))', prefix: 'TX', negate: true },
        ]}
        // Multi-node clusters produce one series per (RX, node) and
        // one per (TX, node). The default seriesLabel collapses by
        // prefix only, so two RX series would both label as "RX" and
        // the chart's collision resolver appends " (2)" — confusing
        // (legend reads "RX" + "RX (2)" with no node hint). Include
        // the node name explicitly so each line identifies its node.
        seriesLabel={(labels, prefix) => {
          const node = labels.node || 'node'
          return prefix ? `${prefix} ${node}` : node
        }}
        chartType="area"
      />
    </div>
  )
}
