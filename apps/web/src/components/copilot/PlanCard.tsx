import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, ArrowRight, Upload } from 'lucide-react'
import { api, ApiError } from '@/services/api'
import { PlanExportModal } from '@/components/kobiPlans/PlanExportModal'
import { DiscardPlanButton } from '@/components/kobiPlans/DiscardPlanButton'
import { VerifyPlanButton } from '@/components/kobiPlans/VerifyPlanButton'
import type { KobiPlanView, PlanRef, PlanRisk } from '@/types/kobiPlans'
import {
  MINUS,
  statusWords,
  expiresIn,
  formatPlanMoney,
  freedUnits,
  historyWords,
  isConsolidation,
  nodeCounts,
  pendingDependencies,
  planPath,
  scopeWords,
} from '@/utils/kobiPlans'

// The plan card in Kobi's chat (Kobi Plans plans-04). It reads the plan from
// GET /plans/{id}, so a conversation reopened days later shows today's state;
// the tool result only names the plan, in a field the model never reads.

interface Props {
  planRef: PlanRef
}

export function PlanCard({ planRef }: Props) {
  const { data: plan, isLoading, error } = useQuery({
    queryKey: ['kobi-plan', planRef.id],
    queryFn: () => api.getPlan(planRef.id),
    staleTime: 30_000,
    retry: (count, err) => !(err instanceof ApiError && (err.status === 404 || err.status === 403)) && count < 2,
  })

  if (isLoading) {
    return (
      <CardShell>
        <div className="px-3.5 py-3 text-[12px] text-kobi-text-secondary">Loading the plan…</div>
      </CardShell>
    )
  }
  if (error || !plan) {
    const gone = error instanceof ApiError && error.status === 404
    return (
      <CardShell>
        <div className="px-3.5 py-3 text-[12px] text-kobi-text-secondary">
          {gone ? 'This plan is no longer available.' : 'The plan could not be read right now.'}
        </div>
      </CardShell>
    )
  }
  const existing = planRef.outcome === 'existing_live_plan' ? 'one_per_cluster' : planRef.outcome === 'existing_same_scope' ? 'same_scope' : null
  return <PlanCardBody plan={plan} existing={existing} />
}

function CardShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="max-w-[95%] my-1 rounded-xl border border-kobi-border bg-kobi-card overflow-hidden">{children}</div>
  )
}

// Proportions follow the approved canvas (Kobi Plans · v0 · tarjeta): a header
// and a footer bar, the title at 15px, the figure at 22px, and the rest as
// 12px prose; mono only for the plan's kind line and the small labels.
export function PlanCardBody({ plan, existing }: { plan: KobiPlanView; existing: 'one_per_cluster' | 'same_scope' | null }) {
  const [exporting, setExporting] = useState(false)
  const status = plan.effectiveStatus
  const est = plan.impact.estimated
  const units = freedUnits(est)
  const expiry = status === 'draft' ? expiresIn(plan.expiresAt) : null
  const excluded = plan.excluded.length
  const consolidation = isConsolidation(plan)
  const counts = nodeCounts(plan)
  const waiting = pendingDependencies(plan)

  return (
    <CardShell>
      <div className="flex items-center justify-between gap-2 px-3.5 py-2.5 border-b border-kobi-border">
        <span className="flex items-center gap-2">
          <span className="text-[11px] font-mono text-kobi-text-secondary">
            PLAN · {plan.kind.toUpperCase()} · v{plan.version}
          </span>
          <span
            title="Kobi Plans is in beta: it works, and we want your feedback to shape it."
            className="inline-flex items-center px-1.5 py-px rounded-full bg-kobi-accent-light text-kobi-accent text-[9px] font-mono font-semibold uppercase tracking-[0.08em]"
          >
            Beta
          </span>
        </span>
        <StatusBadge label={statusWords(status, plan.effectiveExpiredReason)} />
      </div>

      <div className="px-3.5 py-3.5 flex flex-col gap-3">
        <div className="flex flex-col gap-0.5">
          <span className="text-[15px] leading-5 font-semibold text-kobi-text break-words">{plan.title}</span>
          <span className="text-[12px] text-kobi-text-secondary">
            {[plan.clusterName, consolidation ? 'every pool' : scopeWords(plan), historyWords(plan)].filter(Boolean).join(' · ')}
          </span>
        </div>

        {existing && (
          <div className="text-[12px] leading-[17px] text-kobi-text-secondary">
            {existing === 'same_scope'
              ? 'A live plan already covers these workloads and still has steps to do. Nothing new was created.'
              : "Your organization's plan keeps one live plan per cluster, and this cluster already has this one. Nothing new was created."}
          </div>
        )}

        {/* What it frees: the plan's own figures, never recomputed here. The
            money is the list price of that capacity, not a saving: the bill
            falls only once nodes or disks go (26-nov). A consolidation plan's
            figure is the bill itself: the nodes it removes (plans-16). */}
        {consolidation ? (
          <div className="flex flex-col gap-0.5">
            <span className="text-[10px] font-mono tracking-[0.06em] text-kobi-text-secondary">ON THE CLUSTER BILL</span>
            <span className="text-[22px] leading-7 font-semibold text-kobi-text">
              {est.money ? `${MINUS}${formatPlanMoney(est.money.monthly, est.money.currency)}/month` : units ?? 'No node to spare'}
            </span>
            <span className="text-[12px] leading-[17px] text-kobi-text-secondary">
              {[
                counts.before > 0 && `${counts.before} → ${counts.after} nodes`,
                est.money ? 'list prices · less with commitments' : 'no OpenCost prices',
              ]
                .filter(Boolean)
                .join(' · ')}
            </span>
            {waiting.length > 0 && (
              <span className="text-[12px] leading-[17px] text-kobi-st-warn">
                Waits for {waiting[0].title}: the node is left over with the requests it leaves.
              </span>
            )}
          </div>
        ) : (
          <div className="flex flex-col gap-0.5">
            <span className="text-[10px] font-mono tracking-[0.06em] text-kobi-text-secondary">CAPACITY IT FREES</span>
            {est.money ? (
              <>
                <span className="text-[22px] leading-7 font-semibold text-kobi-text">
                  {formatPlanMoney(est.money.monthly, est.money.currency)}/month
                </span>
                <span className="text-[12px] leading-[17px] text-kobi-text-secondary">
                  {[units, 'list prices', 'a saving only once nodes go'].filter(Boolean).join(' · ')}
                </span>
              </>
            ) : (
              <>
                <span className="text-[22px] leading-7 font-semibold text-kobi-text">{units ?? 'No capacity freed'}</span>
                <span className="text-[12px] leading-[17px] text-kobi-text-secondary">Install OpenCost to see it in money.</span>
              </>
            )}
          </div>
        )}

        {plan.engine.lowConfidence && (
          <div className="flex items-start gap-1.5 text-[12px] leading-[17px] text-kobi-st-warn">
            <AlertTriangle className="w-3.5 h-3.5 mt-px shrink-0" />
            <span>
              {consolidation
                ? 'Under a week of use: a weekly or monthly peak may not be in it, so every step is high risk.'
                : 'Low confidence: under two days of history, so every step is high risk until there is more.'}
            </span>
          </div>
        )}

        <div className="flex flex-col gap-0.5">
          <span className="text-[12px] leading-[17px] text-kobi-text-secondary">
            {plan.steps.length} {plan.steps.length === 1 ? 'step' : 'steps'}
            {plan.maxRisk && (
              <>
                {' · max risk '}
                <span className={RISK_TEXT[plan.maxRisk]}>{plan.maxRisk}</span>
              </>
            )}
            {` · ${excluded} excluded`}
          </span>
          {expiry && <span className="text-[12px] text-kobi-text-tertiary">Expires {expiry} if not exported</span>}
          {status === 'expired' && <span className="text-[12px] text-kobi-text-tertiary">{statusWords(status, plan.effectiveExpiredReason)}</span>}
        </div>
      </div>

      {/* Actions. What is not built yet stays visible, disabled, with Soon. */}
      <div className="flex items-center gap-2 flex-wrap px-3.5 py-2.5 border-t border-kobi-border">
        <Link
          to={planPath(plan.id)}
          className="inline-flex items-center gap-1.5 h-[34px] px-3 rounded-lg text-white text-[13px] font-semibold bg-kobi-accent hover:bg-kobi-accent/90 transition-colors"
        >
          Open plan <ArrowRight className="w-3.5 h-3.5" />
        </Link>
        {existing ? (
          <>
            {/* A step's values are revised in the plan's view (Open plan). */}
            {(status === 'draft' || status === 'exported') && plan.steps.some((s) => s.status === 'pending') && (
              <DiscardPlanButton
                planId={plan.id}
                title={plan.title}
                className="inline-flex items-center h-[34px] px-3 rounded-lg border border-kobi-border text-[13px] font-semibold text-kobi-text hover:bg-kobi-elevated transition-colors"
              />
            )}
          </>
        ) : status === 'exported' ? (
          // Exported: the next step is checking it held (spec: the card's
          // action follows the state, Export or Verify).
          <VerifyPlanButton
            planId={plan.id}
            label="Verify"
            className="inline-flex items-center gap-1.5 h-[34px] px-3 rounded-lg border border-kobi-border text-[13px] font-semibold text-kobi-text hover:bg-kobi-elevated disabled:opacity-60 transition-colors"
          />
        ) : (
          <button
            type="button"
            onClick={() => setExporting(true)}
            disabled={status === 'expired'}
            className="inline-flex items-center gap-1.5 h-[34px] px-3 rounded-lg border border-kobi-border text-[13px] font-semibold text-kobi-text hover:bg-kobi-elevated disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            <Upload className="w-3.5 h-3.5" /> Export
          </button>
        )}
      </div>
      {exporting && <PlanExportModal planId={plan.id} title={plan.title} onClose={() => setExporting(false)} />}
    </CardShell>
  )
}

const RISK_TEXT: Record<PlanRisk, string> = {
  low: 'text-kobi-st-ok',
  medium: 'text-kobi-st-warn',
  high: 'text-kobi-st-error',
}

// The status reads as a neutral outlined pill: a draft is not a warning.
function StatusBadge({ label }: { label: string }) {
  return (
    <span className="px-2 py-0.5 rounded-full border border-kobi-border text-[10px] font-mono uppercase text-kobi-text-secondary">
      {label}
    </span>
  )
}
