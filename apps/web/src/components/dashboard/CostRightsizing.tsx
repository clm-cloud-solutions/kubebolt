import { Link } from 'react-router-dom'
import { Scale, Info } from 'lucide-react'
import { HoverTooltip, TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { formatMoney, formatCPU, formatMemory } from '@/utils/formatters'
import { estimateMonthlySavings, type NodeRates } from '@/hooks/useClusterCost'
import type { Recommendation } from '@/hooks/useRightSizing'
import { PreliminaryBadge } from './PreliminaryBadge'

// CostRightsizing — the FinOps lens on the shared right-sizing engine
// (design/kubebolt-cost-redesign.html "Rightsizing recommendations").
// Same recommendations RightSizingPanel renders on Capacity, but
// framed by MONEY: each over-provisioned workload's reclaimable
// cores/GiB priced at the cluster's node rates, sorted by $/mo saved.
//
// Only over-provisioned findings appear here — near-limit and
// no-specs recs are a sizing concern with no dollar saving, so they
// stay on Capacity's panel. One engine, two lenses (§ design decision).
//
// The mockup's "Apply via PR" button is deliberately NOT rendered:
// there is no PR-generation backend yet, and a dead button reads as a
// broken promise. Each row instead links to the workload, where the
// operator acts today; automated PRs are a documented fast-follow.

interface Props {
  recs: Recommendation[]
  rates: NodeRates
  savingsMonthly: number
  // From useRightSizing — young P95 window ⇒ directional savings.
  preliminary?: boolean
  windowDays?: number
}

const KIND_TO_PATH: Record<string, string> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
}

interface Priced {
  rec: Recommendation
  savings: number
  cpuReclaimMilli: number
  memReclaimBytes: number
}

function priceRecs(recs: Recommendation[], rates: NodeRates): Priced[] {
  return recs
    .map((rec) => {
      const cpuReclaimMilli = rec.cpu.state === 'over' ? rec.cpu.request - rec.cpu.suggest : 0
      const memReclaimBytes = rec.mem.state === 'over' ? rec.mem.request - rec.mem.suggest : 0
      const savings = estimateMonthlySavings(cpuReclaimMilli, memReclaimBytes, rates)
      return { rec, savings, cpuReclaimMilli, memReclaimBytes }
    })
    .filter((p) => p.savings > 0)
    .sort((a, b) => b.savings - a.savings)
}

export function CostRightsizing({ recs, rates, savingsMonthly, preliminary, windowDays }: Props) {
  const priced = priceRecs(recs, rates)

  // Kobi payload — the priced recommendations the user sees, so the
  // LLM prioritizes by $ saved and risk. Reuses the shared right_sizing
  // panel prompt. Carries the ACTUAL request / P95 / suggested values
  // per resource (not just the reclaim delta) so the LLM never has to
  // infer a spec it wasn't given — the same gap that made it guess an
  // allocation on the cost breakdown.
  const kobiRows = priced.slice(0, 10).map(({ rec, savings }) => {
    const blob: Record<string, string | number> = {
      workload: `${rec.namespace}/${rec.name}`,
      kind: rec.kind,
      // Keep cents — rounding to whole dollars reported a real
      // ~$0.39/mo saving as "$0" and the LLM read it as "no saving".
      savings_usd_per_mo: Math.round(savings * 100) / 100,
    }
    if (rec.cpu.state === 'over') {
      if (rec.cpu.request > 0) blob.cpu_request_milli = Math.round(rec.cpu.request)
      if (rec.cpu.p95 > 0) blob.cpu_p95_milli = Math.round(rec.cpu.p95)
      if (rec.cpu.suggest > 0) blob.cpu_suggest_milli = Math.round(rec.cpu.suggest)
    }
    if (rec.mem.state === 'over') {
      if (rec.mem.request > 0) blob.mem_request_mib = Math.round(rec.mem.request / (1024 * 1024))
      if (rec.mem.p95 > 0) blob.mem_p95_mib = Math.round(rec.mem.p95 / (1024 * 1024))
      if (rec.mem.suggest > 0) blob.mem_suggest_mib = Math.round(rec.mem.suggest / (1024 * 1024))
    }
    return blob
  })

  return (
    <div className="rounded-[10px] border border-kb-border bg-kb-card p-4">
      <div className="flex items-center justify-between mb-3 gap-3">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-kb-text-secondary shrink-0">
            <Scale className="w-4 h-4" />
          </span>
          <h4 className="text-sm font-semibold text-kb-text-primary truncate">
            Rightsizing savings
          </h4>
          {preliminary && <PreliminaryBadge windowDays={windowDays} />}
          <HoverTooltip
            body={
              <>
                <TooltipHeader right="P95 over 7d">Rightsizing savings</TooltipHeader>
                <TooltipRow color="#22d68a" label="Basis" value="reclaimable × node rates" />
                <TooltipNote>
                  Estimated monthly saving from shrinking over-provisioned workloads to
                  their P95 usage over 7 days plus headroom, priced at this cluster's CPU
                  and RAM hourly rates. Only over-provisioned workloads appear here;
                  near-limit and no-specs recommendations — a sizing concern with no dollar
                  saving — live on the Capacity tab.
                </TooltipNote>
              </>
            }
          >
            <button
              type="button"
              aria-label="About rightsizing savings"
              className="shrink-0 text-kb-text-tertiary hover:text-kb-text-secondary transition-colors"
            >
              <Info className="w-3 h-3" />
            </button>
          </HoverTooltip>
          {priced.length > 0 && (
            <AskCopilotButton
              payload={{
                type: 'panel_inquiry',
                panel: 'right_sizing',
                rangeLabel: 'P95 over 7d',
                rows: kobiRows,
                truncatedFromTotal: priced.length,
              }}
              variant="icon"
              label="Ask Kobi about rightsizing savings"
            />
          )}
        </div>
        <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
          P95 over 7d
        </span>
      </div>

      {priced.length === 0 ? (
        <div className="py-8 text-center text-xs text-kb-text-tertiary">
          {rates.available
            ? 'No over-provisioned workloads — well sized'
            : 'Cost rates unavailable — savings can’t be priced'}
        </div>
      ) : (
        <>
          <ul className="space-y-1 max-h-[380px] overflow-y-auto pr-1">
            {priced.map(({ rec, savings, cpuReclaimMilli, memReclaimBytes }) => {
              const path = KIND_TO_PATH[rec.kind]
              const detail: string[] = []
              if (cpuReclaimMilli > 0) detail.push(`−${formatCPU(cpuReclaimMilli)}`)
              if (memReclaimBytes > 0) detail.push(`−${formatMemory(memReclaimBytes)}`)
              const inner = (
                <div className="flex items-center gap-3 py-1.5 min-w-0 flex-1">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-baseline gap-1.5 truncate">
                      <span className="text-xs text-kb-text-primary truncate">{rec.name}</span>
                      <span className="text-[10px] font-mono text-kb-text-tertiary truncate">
                        {rec.namespace}
                      </span>
                    </div>
                    <div className="text-[10px] font-mono text-kb-text-tertiary truncate mt-0.5">
                      {detail.join(' · ')} reclaimable
                    </div>
                  </div>
                  <span className="text-xs font-mono font-semibold text-kb-accent shrink-0 tabular-nums">
                    −{formatMoney(savings, { exact: true })}/mo
                  </span>
                </div>
              )
              return (
                <li key={`${rec.namespace}/${rec.kind}/${rec.name}`}>
                  <HoverTooltip
                    body={
                      <RowTooltip
                        rec={rec}
                        rates={rates}
                        savings={savings}
                        cpuReclaimMilli={cpuReclaimMilli}
                        memReclaimBytes={memReclaimBytes}
                      />
                    }
                  >
                    {path ? (
                      <Link
                        to={`/${path}/${encodeURIComponent(rec.namespace)}/${encodeURIComponent(rec.name)}`}
                        className="block px-2 rounded transition-colors hover:bg-kb-card-hover"
                      >
                        {inner}
                      </Link>
                    ) : (
                      <div className="px-2">{inner}</div>
                    )}
                  </HoverTooltip>
                </li>
              )
            })}
          </ul>
          {savingsMonthly > 0 && (
            <div className="mt-3 pt-3 border-t border-kb-border flex items-center justify-between">
              <span className="text-[11px] font-mono text-kb-text-tertiary">
                Total reclaimable
              </span>
              <span className="text-sm font-mono font-semibold text-kb-accent tabular-nums">
                −{formatMoney(savingsMonthly, { exact: true })}/mo
              </span>
            </div>
          )}
        </>
      )}
    </div>
  )
}

// RowTooltip — the audit trail behind a row's −$X/mo, mirroring
// Capacity's per-recommendation tooltip but through the cost lens:
// each over-provisioned resource's request → suggested size, its P95,
// and the dollars that resource specifically contributes to the
// saving. Makes the headline figure inspectable instead of a black box.
function RowTooltip({
  rec,
  rates,
  savings,
  cpuReclaimMilli,
  memReclaimBytes,
}: {
  rec: Recommendation
  rates: NodeRates
  savings: number
  cpuReclaimMilli: number
  memReclaimBytes: number
}) {
  const cpuSaving = estimateMonthlySavings(cpuReclaimMilli, 0, rates)
  const memSaving = estimateMonthlySavings(0, memReclaimBytes, rates)
  return (
    <>
      <TooltipHeader right={`−${formatMoney(savings, { exact: true })}/mo`}>
        {rec.name} · {rec.namespace}
      </TooltipHeader>
      {cpuReclaimMilli > 0 && (
        <>
          <TooltipRow
            color="#22d68a"
            label="CPU request"
            value={`${formatCPU(rec.cpu.request)} → ${formatCPU(rec.cpu.suggest)}`}
          />
          <TooltipRow
            color={null}
            label="CPU P95 · saving"
            value={`${formatCPU(rec.cpu.p95)} · −${formatMoney(cpuSaving, { exact: true })}/mo`}
          />
        </>
      )}
      {memReclaimBytes > 0 && (
        <>
          <TooltipRow
            color="#4c9aff"
            label="Mem request"
            value={`${formatMemory(rec.mem.request)} → ${formatMemory(rec.mem.suggest)}`}
          />
          <TooltipRow
            color={null}
            label="Mem P95 · saving"
            value={`${formatMemory(rec.mem.p95)} · −${formatMoney(memSaving, { exact: true })}/mo`}
          />
        </>
      )}
      <TooltipNote>
        Suggested size = each resource's P95 over 7d plus headroom. Applying hands back the
        reclaimable capacity, worth the $/mo at this cluster's node rates.
      </TooltipNote>
    </>
  )
}
