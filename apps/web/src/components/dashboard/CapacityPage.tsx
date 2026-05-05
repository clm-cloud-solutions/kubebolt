import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Cpu, MemoryStick, Network, HardDrive } from 'lucide-react'
import { useClusterOverview } from '@/hooks/useClusterOverview'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { MetricChart, METRIC_ACCENTS } from '@/components/shared/MetricChart'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { AgentRequiredPlaceholder } from '@/components/shared/AgentRequiredPlaceholder'
import { RangeSelector } from '@/components/shared/RangeSelector'
import { api } from '@/services/api'
import type { EventMarker } from '@/components/shared/MetricChart'
import { DashboardSubTabs } from './DashboardSubTabs'
import { OverviewHeader } from './OverviewHeader'
import { TopWorkloadsCpu } from './TopWorkloadsCpu'
import { DeploysList } from './DeploysList'
import { RightSizingPanel } from './RightSizingPanel'

// CapacityPage answers "how is the cluster consuming, and is it
// sized right for what it's actually doing?" — the investigative
// counterpart to Overview's at-a-glance scan. Time-series charts +
// deploy markers, recent deploys table, top consumers, and right-
// sizing recommendations all live here.
//
// All queries (agent integration status, deploys list) hoist to
// this level rather than living inside AgentTrendsBlock. That used
// to cause "Recent Deploys disappears while agent query loads"
// flicker because every panel was nested inside AgentTrendsBlock's
// return tree and got hidden together. Now the trends block is
// self-contained (renders charts or its placeholder), and the rest
// of the page renders independently.
//
// Range default is 15m to match the per-resource Monitor tabs
// elsewhere in the app — one mental model across views.
export function CapacityPage() {
  const { data: overview, isLoading, error, refetch, dataUpdatedAt, isFetching } = useClusterOverview()
  const [rangeMinutes, setRangeMinutes] = useState(15)
  // Deploy markers visible by default — they're the differentiating
  // feature of this tab. Off-state lets the user read raw curves
  // when investigating something whose root cause isn't a rollout.
  const [showDeploys, setShowDeploys] = useState(true)

  const { data: agent, isLoading: agentLoading } = useQuery({
    queryKey: ['integration', 'agent'],
    queryFn: () => api.getIntegration('agent'),
    refetchInterval: 10_000,
    staleTime: 5_000,
  })
  const installed = !!agent && (agent.status === 'installed' || agent.status === 'degraded')

  // Deploys feed BOTH the chart markers and the standalone Recent
  // Deploys panel. One query, two consumers — the panel decides its
  // own visibility (returns null on empty) and the markers respect
  // the showDeploys toggle.
  const { data: deploys } = useQuery({
    queryKey: ['deploys', rangeMinutes],
    queryFn: () => api.getDeploys({ windowMinutes: rangeMinutes }),
    refetchInterval: 30_000,
    staleTime: 15_000,
    retry: false,
  })
  const eventMarkers: EventMarker[] = showDeploys
    ? (deploys ?? []).map((d) => ({
        // Backend emits RFC3339; chart axis is unix seconds.
        // Date.parse returns ms; divide once here so the prop
        // contract is "seconds" throughout.
        timestamp: Math.floor(Date.parse(d.deployedAt) / 1000),
        label: `${d.name} deploy`,
      }))
    : []
  const deployCount = deploys?.length ?? 0

  if (isLoading) return <LoadingSpinner />
  if (error || !overview) return <ErrorState message={error?.message} onRetry={() => refetch()} />

  return (
    <div className="space-y-5">
      <div className="flex items-start justify-between gap-4 flex-wrap">
        <OverviewHeader overview={overview} />
        <div className="flex items-center gap-3 mt-1">
          <RangeSelector value={rangeMinutes} onChange={setRangeMinutes} />
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
      </div>

      <DashboardSubTabs />

      <AgentTrendsBlock
        rangeMinutes={rangeMinutes}
        agentInstalled={installed}
        agentLoading={agentLoading}
        eventMarkers={eventMarkers}
        showDeploys={showDeploys}
        onToggleDeploys={() => setShowDeploys((v) => !v)}
        deployCount={deployCount}
      />

      <DeploysList deploys={deploys ?? []} windowMinutes={rangeMinutes} />

      {/* Workload analytics — paired side-by-side because both
          answer "per-workload" questions and their list rows are
          short enough that full width was mostly empty space. The
          grid collapses to 1-column on narrow viewports. Default
          stretch alignment keeps both cards the same height; the
          shorter card carries some empty space at the bottom but
          the row reads as a balanced pair instead of a stair-step. */}
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
        <TopWorkloadsCpu installed={installed} overview={overview} />
        <RightSizingPanel installed={installed} overview={overview} />
      </div>
    </div>
  )
}

// AgentTrendsBlock renders the 2×2 chart grid (or the
// agent-required placeholder when the agent isn't installed). Pure
// presentation — all state and queries live in CapacityPage so the
// other panels on the page are independent of this block's
// loading/empty states.
function AgentTrendsBlock({
  rangeMinutes,
  agentInstalled,
  agentLoading,
  eventMarkers,
  showDeploys,
  onToggleDeploys,
  deployCount,
}: {
  rangeMinutes: number
  agentInstalled: boolean
  agentLoading: boolean
  eventMarkers: EventMarker[]
  showDeploys: boolean
  onToggleDeploys: () => void
  deployCount: number
}) {
  if (agentLoading) return null

  if (!agentInstalled) {
    return (
      <div className="space-y-2 pt-2">
        <div className="flex items-baseline justify-between">
          <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
            Cluster trends
          </div>
          <div className="text-[10px] text-kb-text-tertiary">
            agent unlocks history
          </div>
        </div>
        <AgentRequiredPlaceholder
          title="Capacity trends require the KubeBolt Agent"
          description="Time-series charts here come from VictoriaMetrics samples shipped by the agent. Install the agent to populate this view; live commitment bars on Overview keep working from the Metrics Server in the meantime."
          hideWhileLoading
        />
      </div>
    )
  }

  return (
    <div className="space-y-3 pt-2">
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div className="text-[11px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
          Cluster trends
        </div>
        <div className="flex items-center gap-3">
          {/* Deploy markers toggle — pill with the same triangle
              glyph the chart draws, so the button visually matches
              what it controls. The count next to the label tells
              the user there's something to toggle even when the
              triangles are too small/sparse to spot at a glance. */}
          <button
            type="button"
            onClick={onToggleDeploys}
            title={showDeploys ? 'Hide deploy markers' : 'Show deploy markers'}
            className={`flex items-center gap-1.5 px-2 py-1 rounded border text-[10px] font-mono transition-colors ${
              showDeploys
                ? 'border-kb-border bg-kb-elevated/40 text-kb-text-primary hover:border-kb-border-active'
                : 'border-kb-border text-kb-text-tertiary opacity-60 hover:opacity-100'
            }`}
          >
            <span
              className="inline-block"
              style={{
                width: 0,
                height: 0,
                borderLeft: '4px solid transparent',
                borderRight: '4px solid transparent',
                borderTop: `5px solid ${showDeploys ? '#94a3b8' : 'var(--kb-text-tertiary)'}`,
              }}
            />
            <span>Deploys</span>
            {deployCount > 0 && (
              <span className="text-kb-text-tertiary tabular-nums">
                {deployCount}
              </span>
            )}
          </button>
          <span className="text-[10px] text-kb-text-tertiary">
            actual usage over selected range
          </span>
        </div>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <MetricChart
          title="CPU Usage"
          icon={<Cpu className="w-4 h-4" />}
          unit="cores"
          query={`sum(node_cpu_usage_cores)`}
          seriesLabel={() => 'cluster total'}
          accents={METRIC_ACCENTS.cpu}
          chartType="area"
          showStats={false}
          height={180}
          controlledRangeMinutes={rangeMinutes}
          eventMarkers={eventMarkers}
        />
        <MetricChart
          title="Memory Working Set"
          icon={<MemoryStick className="w-4 h-4" />}
          unit="bytes"
          query={`sum(node_memory_working_set_bytes)`}
          seriesLabel={() => 'cluster total'}
          accents={METRIC_ACCENTS.memory}
          chartType="area"
          showStats={false}
          height={180}
          controlledRangeMinutes={rangeMinutes}
          eventMarkers={eventMarkers}
        />
        <MetricChart
          title="Network Activity"
          icon={<Network className="w-4 h-4" />}
          unit="bytes/s"
          queries={[
            { query: `sum(rate(node_network_receive_bytes_total[1m]))`, prefix: 'RX' },
            { query: `sum(rate(node_network_transmit_bytes_total[1m]))`, prefix: 'TX', negate: true },
          ]}
          seriesLabel={(_labels, prefix) => prefix ?? 'total'}
          accents={METRIC_ACCENTS.networkRxTx}
          chartType="area"
          showStats={false}
          height={180}
          controlledRangeMinutes={rangeMinutes}
          eventMarkers={eventMarkers}
        />
        <MetricChart
          title="Filesystem Used"
          icon={<HardDrive className="w-4 h-4" />}
          unit="bytes"
          query={`sum(node_fs_used_bytes)`}
          seriesLabel={() => 'cluster total'}
          accents={METRIC_ACCENTS.filesystem}
          chartType="area"
          showStats={false}
          height={180}
          controlledRangeMinutes={rangeMinutes}
          eventMarkers={eventMarkers}
        />
      </div>
    </div>
  )
}
