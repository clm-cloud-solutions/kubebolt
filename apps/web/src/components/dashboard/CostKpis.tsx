import { DollarSign, Zap } from 'lucide-react'
import { StripCard } from './StripCard'
import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { formatMoney, formatPercent } from '@/utils/formatters'
import type { ClusterCost } from '@/hooks/useClusterCost'

// CostKpis — the scan row at the top of the Cost sub-tab (design/
// kubebolt-cost-redesign.html). Same StripCard grammar as the
// Capacity strip so the two sub-tabs read consistently. Every number
// derives from OpenCost series already in VM (see useClusterCost);
// the strip summarizes the panels below, it introduces no new data.
//
// Five cards, left→right by decreasing headline weight: what you
// spend · what's wasted · what you could save · unit economics ·
// how well you use what you pay for.

interface Props {
  cost: ClusterCost
  // $/mo from applying the rightsizing recommendations, priced at the
  // cluster's node rates. null-ish when rates are unknown → the card
  // falls back to a cores/GiB-free "see recs" sub-line.
  savingsMonthly: number
  recCount: number
  ratesAvailable: boolean
  // From useRightSizing — the P95 window is young, so the savings / idle figures
  // are directional, not a mandate. Surfaced so the headline isn't taken as gospel.
  preliminary?: boolean
  windowDays?: number
}

// Efficiency thresholds — green when you use most of what you pay
// for, amber in the sloppy middle, red when the cluster is mostly
// idle spend. Matches the Overview efficiency band's framing.
function effAccent(pct: number | null): 'ok' | 'warn' | 'crit' | 'default' {
  if (pct == null) return 'default'
  if (pct >= 60) return 'ok'
  if (pct >= 35) return 'warn'
  return 'crit'
}

// Hex for a Basis tooltip dot that should TRACK a %-driven card accent (Idle,
// Efficiency) instead of a fixed theme color — so the marker reads green when
// the metric is fine and amber/red only when it actually crosses the threshold.
// Mirrors StripCard's SPARK_STROKE palette.
function dotColor(accent: 'ok' | 'warn' | 'crit' | 'info' | 'default'): string {
  switch (accent) {
    case 'ok': return '#22d68a'
    case 'warn': return '#f5a623'
    case 'crit': return '#ef4056'
    case 'info': return '#4c9aff'
    default: return '#8b93a7'
  }
}

export function CostKpis({ cost, savingsMonthly, recCount, ratesAvailable, preliminary, windowDays }: Props) {
  const idleAccent = cost.idlePct != null && cost.idlePct >= 30 ? 'warn' : 'default'
  // A young P95 window makes idle/savings read optimistically — say so in the
  // sub-line so the headline number carries its own caveat.
  const prelimSpan =
    windowDays == null ? '' : windowDays < 1 ? `~${Math.max(1, Math.round(windowDays * 24))}h` : `~${windowDays.toFixed(1)}d`
  const prelimHint = preliminary ? ` · preliminary (${prelimSpan}/7d)` : ''
  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-5 gap-3">
      <StripCard
        hero
        label="Monthly run-rate"
        icon={<DollarSign className="w-3 h-3" />}
        info={
          <>
            <TooltipHeader right="OpenCost">Monthly run-rate</TooltipHeader>
            <TooltipRow color="#22d68a" label="Basis" value="node_total_hourly_cost × 730" />
            <TooltipNote>
              The cluster's current hourly cost (OpenCost's authoritative per-node total)
              projected to a month — a run-rate, not month-to-date actuals. On cloud
              clusters this reflects real billing rates; on a local cluster it reflects
              the configured custom pricing.
            </TooltipNote>
          </>
        }
        value={formatMoney(cost.monthly, { exact: cost.monthly < 100_000 })}
        sub={`${formatMoney(cost.hourly)}/h${cost.podCount != null ? ` · ${cost.podCount} pods` : ''}`}
        subAccent="default"
      />
      <StripCard
        label="Idle"
        info={
          <>
            <TooltipHeader right="unallocated">Idle</TooltipHeader>
            <TooltipRow color={dotColor(idleAccent)} label="Basis" value="node total − Σ allocated" />
            <TooltipNote>
              Capacity you pay for that no workload requested — the gap between the node
              bill and what's attributed to running pods. It also absorbs node-level
              system overhead (kubelet, container runtime, kernel) and kube/system-reserved,
              which no namespace requests — so not all of it is reclaimable waste. And some
              is intentional: the burst headroom + HA margin a demand spike or a lost node
              needs. Reclaiming the real slack means right-sizing requests down or
              consolidating onto fewer nodes — a resilience-for-cost trade, not free money.
            </TooltipNote>
          </>
        }
        value={formatMoney(cost.idleMonthly, { exact: cost.idleMonthly < 100_000 })}
        valueAccent={idleAccent}
        sub={cost.idlePct != null ? `${formatPercent(cost.idlePct)} of spend` : 'per month'}
        subAccent={idleAccent}
      />
      <StripCard
        label="Rightsizing savings"
        icon={<Zap className="w-3 h-3" />}
        info={
          <>
            <TooltipHeader right="P95 over 7d">Rightsizing savings</TooltipHeader>
            <TooltipRow color="#22d68a" label="Basis" value="reclaimable × node rates" />
            <TooltipNote>
              Estimated monthly saving from applying the recommendations below — the
              reclaimable cores / GiB (each workload's P95 over 7 days plus headroom)
              priced at this cluster's CPU and RAM hourly rates.
            </TooltipNote>
          </>
        }
        value={ratesAvailable && savingsMonthly > 0 ? formatMoney(savingsMonthly, { exact: true }) : '—'}
        valueAccent={recCount > 0 ? 'ok' : 'default'}
        sub={
          recCount > 0
            ? `${recCount} ${recCount === 1 ? 'workload' : 'workloads'}${prelimHint}`
            : 'well sized'
        }
        subAccent={recCount > 0 ? 'ok' : 'default'}
      />
      <StripCard
        label="Cost / pod"
        value={cost.costPerPod != null ? formatMoney(cost.costPerPod, { exact: cost.costPerPod < 100_000 }) : '—'}
        sub={cost.podCount != null ? `${cost.podCount} running pods` : '/ month'}
        subAccent="default"
      />
      <StripCard
        label="Efficiency"
        info={
          <>
            <TooltipHeader right="CPU">Efficiency</TooltipHeader>
            <TooltipRow color={dotColor(effAccent(cost.efficiencyPct))} label="Basis" value="Σ used / Σ allocated cores" />
            <TooltipNote>
              How much of the CPU you've requested the cluster is actually using, blended
              across all namespaces. The same commitment signal as the Overview
              efficiency band — low means you're paying for headroom you don't touch.
            </TooltipNote>
          </>
        }
        value={cost.efficiencyPct != null ? formatPercent(cost.efficiencyPct) : '—'}
        valueAccent={effAccent(cost.efficiencyPct)}
        sub="CPU used / allocated"
        subAccent={effAccent(cost.efficiencyPct)}
      />
    </div>
  )
}
