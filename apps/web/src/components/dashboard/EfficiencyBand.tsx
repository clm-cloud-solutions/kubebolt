import { Cpu, MemoryStick, Zap, AlertTriangle, ShieldOff, TrendingUp } from 'lucide-react'
import { Link } from 'react-router-dom'
import type { ResourceUsage as ResourceUsageType } from '@/types/kubernetes'
import { formatPercent, formatCPU, formatMemory } from '@/utils/formatters'
import { getUsageBarColor } from '@/utils/colors'
import { HoverTooltip, TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'

// EfficiencyBand is ResourceUsage potentiated from a side card to the
// Overview's hero (design/kubebolt-overview-cluster-redesign.html §7):
// same data (overview.cpu / overview.memory — Used, Requested, Limit,
// Allocatable straight from the connector), new framing. Instead of
// asking "how much am I consuming?" it asks "how much of what you
// RESERVE are you actually using?" — the requested-but-idle gap drawn
// as an explicit striped segment on the capacity axis, because that
// gap is the cluster's reclaimable money and no other panel makes it
// visible.
//
// Per-resource anatomy:
//   - efficiency score pill: used / requested (the % of the
//     reservation doing real work);
//   - stacked capacity axis: Used | Idle (requested − used, striped)
//     | free, with the Limits marker as a tick — same axis grammar
//     as the old bullet bar so returning users keep their bearings;
//   - callout: idle > IDLE_WARN_RATIO of the reservation → warning
//     with a Right-size CTA into Capacity; otherwise the "well
//     sized" confirmation.
//
// Footer: blended efficiency (plain average of the per-resource
// scores — usage-weighting would let a huge memory pool mask a
// wasteful CPU reservation) + the live right-sizing rec count from
// useRightSizing. The fleet/Home roll-up line from the mockup is
// deliberately omitted until a Home surface exists.
//
// Open design note (README §7): the idle segment uses warning amber;
// a neutral tone is under evaluation so sizing slack doesn't read as
// an alert. Kept amber here to match the approved mockup.

const IDLE_WARN_RATIO = 0.3

interface EfficiencyBandProps {
  cpu?: ResourceUsageType
  memory?: ResourceUsageType
  metricsAvailable?: boolean
  nodesRestricted?: boolean
  recsReady?: number
}

export function EfficiencyBand({
  cpu,
  memory,
  metricsAvailable = true,
  nodesRestricted,
  recsReady = 0,
}: EfficiencyBandProps) {
  const cpuEff = efficiencyOf(cpu, metricsAvailable)
  const memEff = efficiencyOf(memory, metricsAvailable)
  const scores = [cpuEff, memEff].filter((e): e is number => e != null)
  const blended = scores.length > 0 ? Math.round(scores.reduce((a, b) => a + b, 0) / scores.length) : null

  return (
    // Accent-washed band (mockup: linear-gradient(160deg, accent-dim,
    // card 55%) + glow border) — built from existing tokens via
    // color-mix so both themes derive their own wash; no new CSS vars.
    <div
      className="rounded-xl border p-4"
      style={{
        background: 'linear-gradient(160deg, var(--kb-accent-light), var(--kb-card) 55%)',
        borderColor: 'color-mix(in srgb, var(--kb-accent) 25%, transparent)',
      }}
    >
      <div className="flex items-center justify-between gap-3 mb-3">
        <div className="flex items-center gap-2">
          <span className="w-6 h-6 rounded-md bg-kb-accent-light flex items-center justify-center shrink-0">
            <Zap className="w-3.5 h-3.5 text-kb-accent" />
          </span>
          <h3 className="text-sm font-semibold text-kb-text-primary">Resource efficiency</h3>
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary">
          current state · pod usage vs requests
        </span>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
        <EfficiencyCard
          label="CPU"
          icon={<Cpu className="w-4 h-4" />}
          usage={cpu}
          formatFn={formatCPU}
          metricsAvailable={metricsAvailable}
          nodesRestricted={nodesRestricted}
          unitNoun="cores"
        />
        <EfficiencyCard
          label="Memory"
          icon={<MemoryStick className="w-4 h-4" />}
          usage={memory}
          formatFn={formatMemory}
          metricsAvailable={metricsAvailable}
          nodesRestricted={nodesRestricted}
          unitNoun="memory"
        />
      </div>

      {(blended != null || recsReady > 0) && (
        <div className="mt-3 pt-3 border-t border-kb-border flex items-center gap-2 text-xs text-kb-text-secondary flex-wrap">
          <TrendingUp className="w-3.5 h-3.5 text-kb-accent shrink-0" />
          {blended != null ? (
            <span>
              Cluster blended efficiency:{' '}
              <b className="text-kb-text-primary tabular-nums">{blended}%</b> of reserved
              resources in use
            </span>
          ) : (
            <span>Blended efficiency needs usage + requests data</span>
          )}
          {recsReady > 0 && (
            <Link
              to="/capacity"
              className="ml-auto text-[11px] font-mono text-kb-accent hover:opacity-80 transition-opacity"
            >
              {recsReady} rightsizing {recsReady === 1 ? 'rec' : 'recs'} ready →
            </Link>
          )}
        </div>
      )}
    </div>
  )
}

// efficiencyOf — used/requested as a 0-100 score, or null when either
// side is missing (no metrics, no requests declared, restricted).
function efficiencyOf(usage: ResourceUsageType | undefined, metricsAvailable: boolean): number | null {
  if (!metricsAvailable) return null
  const used = usage?.used ?? 0
  const requested = usage?.requested ?? 0
  if (used <= 0 || requested <= 0) return null
  return Math.min(100, Math.round((used / requested) * 100))
}

function EfficiencyCard({
  label,
  icon,
  usage,
  formatFn,
  metricsAvailable,
  nodesRestricted,
  unitNoun,
}: {
  label: string
  icon: React.ReactNode
  usage?: ResourceUsageType
  formatFn: (v: number) => string
  metricsAvailable: boolean
  nodesRestricted?: boolean
  unitNoun: string
}) {
  const used = usage?.used ?? 0
  const requested = usage?.requested ?? 0
  const limit = usage?.limit ?? 0
  const allocatable = usage?.allocatable ?? 0

  // No capacity axis without node access — everything below needs
  // allocatable as the denominator.
  if (nodesRestricted || allocatable <= 0) {
    return (
      <div className="rounded-[10px] border border-kb-border p-4" style={{ background: 'color-mix(in srgb, var(--kb-bg) 40%, var(--kb-card))' }}>
        <CardHeader label={label} icon={icon} score={null} />
        <div className="flex items-center gap-2 mt-3 text-kb-text-secondary">
          <ShieldOff className="w-3.5 h-3.5 text-status-warn shrink-0" />
          <span className="text-[11px] font-mono">No access to Nodes — capacity data unavailable</span>
        </div>
      </div>
    )
  }

  const hasUsage = metricsAvailable && used > 0
  const idle = Math.max(0, requested - used)
  const usedPct = Math.min(100, (used / allocatable) * 100)
  const idlePct = Math.max(0, Math.min(100 - usedPct, (idle / allocatable) * 100))
  const limitPct = limit > 0 ? Math.min(100, (limit / allocatable) * 100) : 0
  const overCommitted = limit > allocatable
  const score = efficiencyOf(usage, metricsAvailable)
  // Share of the reservation sitting idle — drives the callout tone.
  const idleRatio = requested > 0 && hasUsage ? idle / requested : 0

  return (
    <div className="rounded-[10px] border border-kb-border p-4" style={{ background: 'color-mix(in srgb, var(--kb-bg) 40%, var(--kb-card))' }}>
      <CardHeader label={label} icon={icon} score={score} />

      {/* Tooltip anchors on the axis block only — same rows the old
          ResourceUsage card taught (Used / Requests / Limits /
          Available / Capacity), so the deep numbers stay one hover
          away without cluttering the band. */}
      <HoverTooltip
        body={
          <>
            <TooltipHeader right={score != null ? `${score}% efficient` : undefined}>
              {label}
            </TooltipHeader>
            <div className="space-y-1">
              {hasUsage && (
                <TooltipRow
                  color={getUsageBarColor(usedPct)}
                  label="Used"
                  value={`${formatFn(used)} (${formatPercent(usedPct)} of capacity)`}
                />
              )}
              {requested > 0 && (
                <TooltipRow color="#f5a623" label="Idle (requested)" value={formatFn(idle)} />
              )}
              {requested > 0 && (
                <TooltipRow color="#4c9aff" label="Requests" value={formatFn(requested)} />
              )}
              {limit > 0 && (
                <TooltipRow color="#ef4056" label="Limits" value={formatFn(limit)} />
              )}
              <TooltipRow
                color={null}
                label="Available"
                value={formatFn(Math.max(0, allocatable - requested))}
              />
              <TooltipRow color={null} label="Capacity" value={formatFn(allocatable)} />
            </div>
            <div className="mt-2 pt-2 border-t border-kb-border">
              <TooltipNote>
                "Used" is the sum of your <b>pods</b> (Metrics Server) — it doesn't count
                the node's own OS / kubelet / kernel usage. Capacity's node-total figure
                runs higher for that reason; both are correct at their level.
              </TooltipNote>
            </div>
          </>
        }
      >
        <div className="mt-3">
          {/* Stacked capacity axis: Used | Idle (striped) | free, Limits tick */}
          <div className="relative">
            <div className="h-6 rounded-md overflow-hidden flex border border-kb-border" style={{ background: 'var(--kb-bar-track)' }}>
              {hasUsage && (
                // status-ok, not --kb-accent: the "Used" fill must match the
                // usage green the rings + workload-health bars use app-wide.
                // --kb-accent diverges in light mode (#16a34a vs #22d68a), so
                // the band's bar read as a different green there.
                <div
                  className="h-full transition-all duration-700 bg-status-ok"
                  style={{ width: `${usedPct}%` }}
                />
              )}
              {hasUsage && idlePct > 0 && (
                <div
                  className="h-full transition-all duration-700"
                  style={{
                    width: `${idlePct}%`,
                    background:
                      'repeating-linear-gradient(45deg, rgba(245,166,35,.45), rgba(245,166,35,.45) 5px, rgba(245,166,35,.2) 5px, rgba(245,166,35,.2) 10px)',
                  }}
                />
              )}
            </div>
            {limitPct > 0 && (
              <div
                className="absolute -top-0.5 -bottom-0.5 w-[2px] rounded-full bg-status-info"
                style={{ left: `${limitPct}%`, transform: 'translateX(-1px)' }}
              />
            )}
          </div>

          {/* Axis key */}
          <div className="flex flex-wrap gap-x-4 gap-y-1 mt-2 text-[10px] font-mono text-kb-text-secondary">
            <span className="flex items-center gap-1.5">
              <i className="w-2 h-2 rounded-[2px] bg-status-ok" />
              Used {hasUsage ? `${formatFn(used)} / ${formatFn(allocatable)}` : 'no data'}
            </span>
            {requested > 0 && (
              <span className="flex items-center gap-1.5">
                <i
                  className="w-2 h-2 rounded-[2px]"
                  style={{
                    background:
                      'repeating-linear-gradient(45deg, rgba(245,166,35,.55), rgba(245,166,35,.55) 2px, rgba(245,166,35,.25) 2px, rgba(245,166,35,.25) 4px)',
                  }}
                />
                Idle (requested) {hasUsage ? formatFn(idle) : '—'}
              </span>
            )}
            {limit > 0 && (
              <span className="flex items-center gap-1.5">
                <i className="w-2 h-2 rounded-[2px] bg-status-info" />
                Limits {formatFn(limit)}
                {overCommitted && <span className="text-status-warn">over capacity</span>}
              </span>
            )}
          </div>
        </div>
      </HoverTooltip>

      {/* Callout — the actionable line. Warning tone only when a
          meaningful share of the reservation idles; the threshold is
          a product constant, not derived. */}
      {hasUsage && requested > 0 ? (
        idleRatio > IDLE_WARN_RATIO ? (
          <div className="mt-3 flex items-center justify-between gap-2 rounded-lg border border-status-warn/30 bg-status-warn-dim px-3 py-2 text-xs">
            <span className="text-kb-text-secondary min-w-0">
              <b className="text-status-warn">{formatFn(idle)}</b> requested but idle —{' '}
              {Math.round(idleRatio * 100)}% of the {unitNoun} you reserve is unused
            </span>
            <Link
              to="/capacity"
              className="text-[10px] font-mono text-kb-accent shrink-0 hover:opacity-80 transition-opacity"
            >
              Right-size →
            </Link>
          </div>
        ) : (
          <div className="mt-3 flex items-center justify-between gap-2 rounded-lg bg-kb-accent-light px-3 py-2 text-xs">
            <span className="text-kb-text-secondary min-w-0">
              <b className="text-kb-accent">Well sized</b> —{' '}
              {Math.round((1 - idleRatio) * 100)}% of reserved {unitNoun} is in use
            </span>
            <Link
              to="/capacity"
              className="text-[10px] font-mono text-kb-accent shrink-0 hover:opacity-80 transition-opacity"
            >
              Details →
            </Link>
          </div>
        )
      ) : !metricsAvailable ? (
        <div className="mt-3 flex items-center gap-2 rounded-lg bg-status-warn-dim/50 px-3 py-2 text-status-warn">
          <AlertTriangle className="w-3 h-3 shrink-0" />
          <span className="text-[10px] font-mono">
            Metrics Server not detected — usage data unavailable
          </span>
        </div>
      ) : requested <= 0 ? (
        <div className="mt-3 rounded-lg bg-kb-elevated px-3 py-2 text-[10px] font-mono text-kb-text-tertiary">
          No {unitNoun} requests declared — efficiency can't be scored without a reservation
        </div>
      ) : null}
    </div>
  )
}

function CardHeader({
  label,
  icon,
  score,
}: {
  label: string
  icon: React.ReactNode
  score: number | null
}) {
  return (
    <div className="flex items-center justify-between gap-2">
      <span className="flex items-center gap-2 text-sm font-semibold text-kb-text-primary">
        <span className="text-kb-text-secondary">{icon}</span>
        {label}
      </span>
      {score != null && (
        <span
          className={`text-[10px] font-mono font-bold px-2 py-0.5 rounded-full ${
            score >= 70 ? 'bg-kb-accent-light text-kb-accent' : 'bg-status-warn-dim text-status-warn'
          }`}
        >
          {score}% efficient
        </span>
      )}
    </div>
  )
}
