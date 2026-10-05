import { TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { BarList, KPI_COLOR, Legend, RingStat, SemiGauge, SplitBar } from '@/components/shared/kpi/MiniCharts'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'
import { formatMoney, formatPercent } from '@/utils/formatters'
import type { ClusterCost } from '@/hooks/useClusterCost'

// CostKpis — the scan row at the top of the Cost sub-tab, in the site's
// card anatomy (KpiCard) like the Capacity and Reliability rows, so the
// sub-tabs read as one family. Every number derives from OpenCost series
// already in VM (see useClusterCost); the strip summarizes the panels below,
// it introduces no new data.
//
// Four cards, left→right by decreasing headline weight: what you spend
// (and where it goes) · what's idle · how much of that the recommendations
// recover · how well you use what you pay for. The unit cost (per pod)
// rides in the run-rate's sentence instead of a fifth card.

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

// Hex for a Basis tooltip dot / chart that should TRACK a %-driven accent
// (Idle, Efficiency) instead of a fixed theme color — so the marker reads
// green when the metric is fine and amber/red only when it crosses the
// threshold.
function dotColor(accent: 'ok' | 'warn' | 'crit' | 'info' | 'default'): string {
  switch (accent) {
    case 'ok': return KPI_COLOR.ok
    case 'warn': return KPI_COLOR.warn
    case 'crit': return KPI_COLOR.err
    case 'info': return KPI_COLOR.info
    default: return '#8b93a7'
  }
}

const KPI_MIN_WIDTH = 280

export function CostKpis({ cost, savingsMonthly, recCount, ratesAvailable, preliminary, windowDays }: Props) {
  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()
  // Two thresholds, not one. At 31% idle a cluster is carrying normal burst
  // and HA headroom — amber, worth a look. At 90% it is paying for a cluster
  // it isn't running, and the card was reporting that in the same amber as
  // the merely-sloppy case.
  const idleAccent: 'crit' | 'warn' | 'default' =
    cost.idlePct == null ? 'default' : cost.idlePct >= 60 ? 'crit' : cost.idlePct >= 30 ? 'warn' : 'default'
  // A young P95 window makes idle/savings read optimistically — say so in the
  // caption so the headline number carries its own caveat.
  const prelimSpan =
    windowDays == null ? '' : windowDays < 1 ? `~${Math.max(1, Math.round(windowDays * 24))}h` : `~${windowDays.toFixed(1)}d`
  const money = (v: number) => formatMoney(v, { exact: v < 100_000 })
  const topNs = [...cost.byNamespace].filter((n) => n.monthly > 0).sort((a, b) => b.monthly - a.monthly)
  const recoverPct =
    ratesAvailable && savingsMonthly > 0 && cost.idleMonthly > 0
      ? Math.min(100, (savingsMonthly / cost.idleMonthly) * 100)
      : null
  let cpuUsed = 0
  let cpuAlloc = 0
  for (const n of cost.byNamespace) {
    cpuUsed += n.cpuUsed ?? 0
    cpuAlloc += n.cpuAllocated ?? 0
  }
  const eff = cost.efficiencyPct
  const effColor = dotColor(effAccent(eff))

  return (
    <div
      ref={gridRef}
      className={`grid gap-4 ${gridWidth ? '' : 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'}`}
      style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, KPI_MIN_WIDTH, 16, [4, 2, 1])}, minmax(0, 1fr))` } : undefined}
    >
      <KpiCard
        primary
        value={formatMoney(cost.monthly, { exact: cost.monthly < 100_000 })}
        unit="/mo"
        description={`${formatMoney(cost.hourly)}/h${cost.costPerPod != null ? ` · ${money(cost.costPerPod)} per pod` : ''}`}
        viz={
          topNs.length > 0 ? (
            <BarList
              rows={topNs.slice(0, 3).map((n) => ({ label: n.namespace, value: n.monthly, display: money(n.monthly) }))}
            />
          ) : undefined
        }
        caption={`run-rate, not month-to-date${cost.networkMonthly > 0 ? ` · network ${money(cost.networkMonthly)}` : ''}`}
        info={
          <>
            <TooltipHeader right="OpenCost">Monthly run-rate</TooltipHeader>
            <TooltipRow color="#22d68a" label="Basis" value="node_total_hourly_cost × 730" />
            <TooltipNote>
              The cluster's current hourly cost (OpenCost's authoritative per-node total)
              projected to a month — a run-rate, not month-to-date actuals. On cloud
              clusters this reflects real billing rates; on a local cluster it reflects
              the configured custom pricing. The bars are the namespaces it is
              attributed to; per pod is the run-rate spread over {cost.podCount ?? 'the'} running
              pods — a density signal, not any workload's bill.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        alert={idleAccent === 'crit' ? 'warn' : undefined}
        value={money(cost.idleMonthly)}
        unit="/mo idle"
        description={
          cost.idlePct != null ? `${formatPercent(cost.idlePct)} of spend nothing requested` : 'per month'
        }
        viz={
          cost.monthly > 0 ? (
            <SplitBar
              parts={[
                { value: Math.max(0, cost.allocatedMonthly), color: KPI_COLOR.ok },
                { value: Math.max(0, cost.idleMonthly), hatched: true },
              ]}
              left={`allocated ${money(cost.allocatedMonthly)}`}
              // The label takes the card's alert colour, so a card lit amber does
              // not carry a red figure inside it.
              right={<span style={{ color: idleAccent === 'default' ? undefined : KPI_COLOR.warn }}>idle {money(cost.idleMonthly)}</span>}
            />
          ) : undefined
        }
        caption="includes system overhead and HA headroom"
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
      />
      <KpiCard
        value={ratesAvailable && savingsMonthly > 0 ? formatMoney(savingsMonthly, { exact: true }) : '—'}
        unit="/mo savings"
        description={
          recCount > 0
            ? `rightsizing ${recCount} ${recCount === 1 ? 'workload' : 'workloads'}${
                recoverPct != null ? ` · ${recoverPct < 1 ? '<1' : Math.round(recoverPct)}% of idle` : ''
              }`
            : 'well sized — nothing to hand back'
        }
        viz={
          recoverPct != null ? (
            <SemiGauge
              percent={recoverPct}
              color={KPI_COLOR.ok}
              legend={
                <Legend
                  rows={[
                    { color: KPI_COLOR.ok, label: `${money(savingsMonthly)} recovered` },
                    { color: KPI_COLOR.muted, label: `${money(Math.max(0, cost.idleMonthly - savingsMonthly))} still idle` },
                  ]}
                />
              }
            />
          ) : undefined
        }
        caption={
          <>
            P95 over 7d
            {preliminary && <span className="text-status-warn"> · preliminary{prelimSpan ? ` (${prelimSpan}/7d)` : ''}</span>}
          </>
        }
        info={
          <>
            <TooltipHeader right="P95 over 7d">Rightsizing savings</TooltipHeader>
            <TooltipRow color="#22d68a" label="Basis" value="reclaimable × node rates" />
            <TooltipNote>
              Estimated monthly saving from applying the recommendations below — the
              reclaimable cores / GiB (each workload's P95 over 7 days plus headroom)
              priced at this cluster's CPU and RAM hourly rates. The gauge is that saving
              as a share of the idle spend: what requests alone can recover, before
              consolidating nodes.
            </TooltipNote>
          </>
        }
      />
      <KpiCard
        value={eff != null ? formatPercent(eff).replace('%', '') : '—'}
        unit="% efficient"
        description="of the CPU you requested is in use"
        viz={
          eff != null ? (
            <RingStat
              parts={[
                { value: Math.max(0, Math.min(100, eff)), color: effColor },
                { value: Math.max(0, 100 - Math.min(100, eff)), color: KPI_COLOR.track },
              ]}
              legend={
                cpuAlloc > 0 ? (
                  <Legend
                    rows={[
                      { color: effColor, label: `used ${cpuUsed.toFixed(1)} cores` },
                      { color: KPI_COLOR.muted, label: `requested ${cpuAlloc.toFixed(1)} cores` },
                    ]}
                  />
                ) : undefined
              }
            />
          ) : undefined
        }
        caption="CPU used ÷ allocated, all namespaces"
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
      />
    </div>
  )
}
