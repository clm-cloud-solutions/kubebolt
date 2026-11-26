import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { ChevronDown, AlertTriangle, Pencil } from 'lucide-react'
import { api, ApiError } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { BarList, KPI_COLOR, SplitBar } from '@/components/shared/kpi/MiniCharts'
import { TD, TR_STATIC, Th } from '@/components/shared/table/Th'
import { PlansFeedbackLink } from './PlansBeta'
import { PlanExportModal } from './PlanExportModal'
import { StepValuesEditor } from './StepValuesEditor'
import { ConsolidationLink, ConsolidationPlanView } from './ConsolidationPlanView'
import { CreatedBy, FreesSplit, ObservedImpact, PlanHeader, Pill, RISK_COLOR, Stage, StageArrow, emphasize, shortDate, shortTime } from './planParts'
import { useAuth } from '@/contexts/AuthContext'
import type { KobiPlanView, PlanRisk, PlanStep } from '@/types/kobiPlans'
import {
  CHECK_OUTCOME,
  verifyWindowMs,
  RISK_LABEL,
  statusWords,
  changeLines,
  checkWords,
  excludedByReason,
  exclusionReasonWords,
  expiresIn,
  formatCpuMilli,
  formatMemBytes,
  formatPlanMoney,
  freedUnits,
  historyWords,
  isConsolidation,
  ownerNote,
  ownerWords,
  riskReasonWords,
  rollbackFor,
  applyWhere,
  scopeWords,
  sharedNamespace,
  workloadName,
} from '@/utils/kobiPlans'

// PlanDetailPage — one Kobi Plan, read-only (Kobi Plans plans-04). A global
// page: a plan is a stored record, so it answers when its cluster is down.
// Everything not built yet (export, verify, a pull request, Autopilot) is
// shown disabled with Soon. Every figure is the plan's own.

export function PlanDetailPage() {
  const { id = '' } = useParams()
  const { data: plan, isLoading, error, refetch } = useQuery({
    queryKey: ['kobi-plan', id],
    queryFn: () => api.getPlan(id),
    enabled: !!id,
    retry: false,
  })

  if (isLoading) return <LoadingSpinner />
  if (error || !plan) {
    const gone = error instanceof ApiError && error.status === 404
    return <ErrorState message={gone ? 'Plan not found' : 'The plan could not be read'} onRetry={gone ? undefined : () => refetch()} />
  }
  return isConsolidation(plan) ? <ConsolidationPlanView plan={plan} /> : <PlanView plan={plan} />
}

function PlanView({ plan }: { plan: KobiPlanView }) {
  const [exporting, setExporting] = useState(false)
  const { hasRole } = useAuth()
  const est = plan.impact.estimated
  const status = plan.effectiveStatus
  const statusText = statusWords(status, plan.effectiveExpiredReason)
  // A live plan's pending steps take a person's values (plans-06).
  const canEdit = (status === 'draft' || status === 'exported') && hasRole('editor')
  const expiry = status === 'draft' ? expiresIn(plan.expiresAt) : null
  const byRisk = (r: PlanRisk) => plan.steps.filter((s) => s.risk === r).length
  const groups = excludedByReason(plan.excluded)
  const pending = plan.steps.filter((s) => s.status === 'pending').length
  const unknownOwners = plan.steps.filter((s) => !s.target.owner || s.target.owner.kind === 'unknown').length
  // Said once, by the steps' heading, when every workload shares it.
  const shared = sharedNamespace(plan)
  // The same for the owner: one note when every step has the same Helm
  // release or GitOps app, instead of the same sentence on each step.
  const owners = new Set(plan.steps.map((s) => `${s.target.owner?.kind ?? 'unknown'}:${s.target.owner?.ref ?? ''}`))
  const commonOwner = owners.size === 1 && plan.steps.length > 1 ? plan.steps[0].target.owner : undefined
  const commonOwnerNote =
    commonOwner && (commonOwner.kind === 'helm' || commonOwner.kind === 'gitops') ? ownerNote(commonOwner) : null

  return (
    <div className="space-y-5">
      <PlanHeader
        plan={plan}
        onExport={() => setExporting(true)}
        context={
          <>
            <CreatedBy plan={plan} />
            {' · '}
            {scopeWords(plan)}
            {' · '}
            rightsizing engine, {historyWords(plan)}, computed on {shortDate(plan.engine.computedAt)} at {shortTime(plan.engine.computedAt)}
            {expiry && <> · expires {expiry} if not exported</>}
            {plan.exportedAt && <> · exported on {shortDate(plan.exportedAt)}</>}
            {status === 'expired' && <> · {statusText.toLowerCase()}</>}
          </>
        }
      />

      {plan.engine.lowConfidence && (
        <div className="flex items-start gap-2 rounded-xl border border-kb-border bg-kb-card px-4 py-3 text-[13px] text-kb-text-secondary">
          <AlertTriangle size={15} className="mt-0.5 shrink-0" style={{ color: KPI_COLOR.warn }} />
          Low confidence: the plan rests on under two days of history, so every step is high risk until there is more.
        </div>
      )}

      {/* Headline row: a figure, a short label, a chart and a caption, as on
          every page. */}
      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
        {est.money ? (
          <KpiCard
            primary
            value={formatPlanMoney(est.money.monthly, est.money.currency)}
            unit="/ month"
            description="capacity it frees, at list prices"
            viz={<FreesSplit plan={plan} />}
            caption={[freedUnits(est), 'a saving only once nodes or disks go'].filter(Boolean).join(' · ')}
          />
        ) : (
          <KpiCard
            primary
            value={est.cpuMilli > 0 ? formatCpuMilli(est.cpuMilli) : est.memBytes > 0 ? formatMemBytes(est.memBytes) : '—'}
            unit={est.cpuMilli > 0 ? 'CPU' : est.memBytes > 0 ? 'memory' : undefined}
            description={
              est.cpuMilli > 0 && est.memBytes > 0
                ? emphasize(`capacity it frees, plus ${formatMemBytes(est.memBytes)} memory`, `${formatMemBytes(est.memBytes)} memory`)
                : 'capacity it frees'
            }
            viz={<FreesSplit plan={plan} />}
            caption="no OpenCost prices · capacity only"
          />
        )}
        <ObservedImpact plan={plan} />
        <KpiCard
          value={plan.steps.length}
          unit={plan.steps.length === 1 ? 'step' : 'steps'}
          description={`${pending} pending`}
          viz={
            plan.steps.length > 0 ? (
              <SplitBar
                parts={(['low', 'medium', 'high'] as PlanRisk[]).map((r) => ({ value: byRisk(r), color: RISK_COLOR[r] }))}
                left={`${byRisk('low')} low · ${byRisk('medium')} medium`}
                right={`${byRisk('high')} high`}
              />
            ) : undefined
          }
          caption="by risk"
        />
        <KpiCard
          value={plan.excluded.length}
          unit="excluded"
          description={plan.excluded.length === 0 ? 'nothing left out' : 'left out, each with its reason'}
          viz={
            groups.length > 0 ? (
              <BarList rows={groups.map((g) => ({ label: exclusionReasonWords(g.reason), value: g.items.length }))} color={KPI_COLOR.muted} />
            ) : undefined
          }
          caption={groups.length > 0 ? 'by reason' : 'every flag is a step'}
        />
      </div>

      <ConsolidationLink plan={plan} />

      {/* Steps */}
      <section className="space-y-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 className="text-base font-semibold text-kb-text-primary">
            Steps, in the plan&apos;s order
            {shared && <span className="font-normal text-kb-text-secondary"> · namespace {shared}</span>}
          </h2>
          {commonOwnerNote && <span className="text-[12px] text-kb-text-tertiary">{commonOwnerNote}</span>}
          {unknownOwners > 0 && (
            <span className="text-[12px] text-kb-text-tertiary">
              KubeBolt could not tell who manages {unknownOwners === plan.steps.length ? 'these workloads' : `${unknownOwners} of them`}: check before applying.
            </span>
          )}
        </div>
        {plan.steps.length === 0 ? (
          <div className="bg-kb-card border border-kb-border rounded-xl p-5 text-[13px] text-kb-text-secondary">This plan has no steps.</div>
        ) : (
          plan.steps.map((s, i) => (
            <StepRow
              key={s.id}
              planId={plan.id}
              step={s}
              shared={shared}
              ownerSaid={!!commonOwnerNote}
              defaultOpen={i === 0}
              editable={canEdit && s.status === 'pending' && s.action === 'set_resources'}
            />
          ))
        )}
      </section>

      {/* Excluded */}
      {plan.excluded.length > 0 && (
        <section className="space-y-3">
          <h2 className="text-base font-semibold text-kb-text-primary">Excluded, with their reason</h2>
          <div className="kb-panel overflow-hidden">
            <div className="overflow-x-auto">
              <table className="w-full min-w-[640px] text-left">
                <thead>
                  <tr className="border-b border-kb-border">
                    <Th>Object</Th>
                    <Th>Reason</Th>
                    <Th>Detail</Th>
                  </tr>
                </thead>
                <tbody>
                  {plan.excluded.map((x, i) => (
                    <tr key={i} className={`${TR_STATIC} align-top`}>
                      <td className={`${TD} text-kb-text-primary whitespace-nowrap`}>{workloadName(x.target, shared)}</td>
                      <td className={`${TD} text-kb-text-primary whitespace-nowrap`}>{exclusionReasonWords(x.reason)}</td>
                      {/* Prose, so sans. The owner only when the detail has
                          not already named it (an operator's resource). */}
                      <td className="px-3 py-2.5 text-xs leading-5 text-kb-text-secondary">
                        {x.detail}
                        {x.target.owner &&
                          x.target.owner.kind !== 'unknown' &&
                          !(x.target.owner.ref && x.detail?.includes(x.target.owner.ref)) && (
                            <span className="text-kb-text-tertiary"> · {ownerWords(x.target.owner)}</span>
                          )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </section>
      )}

      {/* Notes and versions */}
      <section className="space-y-3">
        <h2 className="text-base font-semibold text-kb-text-primary">Notes and versions</h2>
        <ul className="space-y-1.5 text-[13px] text-kb-text-secondary">
          {[...plan.notes].reverse().map((n, i) => (
            <li key={i}>
              <span className="font-mono text-[12px] text-kb-text-tertiary">v{n.version} · {shortDate(n.at)}</span> {n.text}
            </li>
          ))}
          <li>
            <span className="font-mono text-[12px] text-kb-text-tertiary">v1 · {shortDate(plan.createdAt)}</span> Created from Kobi&apos;s chat, with {scopeWords(plan)} as its scope.
          </li>
        </ul>
      </section>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[12px] text-kb-text-tertiary">
          Kobi computes none of these figures: the values come from the rightsizing engine and the prices from OpenCost. Kobi orders the steps and explains them.
        </p>
        <PlansFeedbackLink />
      </div>
      {exporting && <PlanExportModal planId={plan.id} title={plan.title} onClose={() => setExporting(false)} />}
    </div>
  )
}

// StepRow: one line that says the whole step, and below it, when open, the
// three stages in the order they happen: apply (only while the workload is
// unchanged), verify for 24 h, and roll back if it fails. The first step
// opens by default, as on the canvas.
function StepRow({
  planId,
  step,
  shared,
  ownerSaid,
  defaultOpen,
  editable,
}: {
  planId: string
  step: PlanStep
  shared: string | null
  ownerSaid: boolean
  defaultOpen: boolean
  editable: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  const [editing, setEditing] = useState(false)
  const lines = changeLines(step)
  const containers = [...new Set(lines.map((l) => l.container))]
  const est = step.impact.estimated
  const units = freedUnits(est)
  const owned = step.target.owner?.kind === 'helm' || step.target.owner?.kind === 'gitops'
  const note = owned && !ownerSaid ? ownerNote(step.target.owner) : null
  const rollback = rollbackFor(step)
  const where = applyWhere(step.target.owner)
  const ev = step.evidence
  const pods = ev?.pods
  const kind = step.target.type.replace(/s$/, '')
  const risk = [RISK_LABEL[step.risk], ...step.riskReasons.map(riskReasonWords)].join(' · ')
  const result = step.verification.result

  return (
    <div className="rounded-xl border bg-kb-card" style={{ borderColor: owned ? `${KPI_COLOR.warn}59` : 'var(--kb-border)' }}>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="w-full flex flex-wrap items-center justify-between gap-3 px-5 py-3.5 text-left"
      >
        <div className="flex flex-wrap items-center gap-2.5 min-w-0">
          <span className="w-7 h-7 rounded-lg bg-kb-elevated text-[13px] font-mono text-kb-text-secondary flex items-center justify-center shrink-0">
            {step.order}
          </span>
          <span className="text-[15px] font-semibold text-kb-text-primary">{workloadName(step.target, shared)}</span>
          <Pill>{kind}</Pill>
          <Pill color={owned ? KPI_COLOR.warn : undefined}>owner: {ownerWords(step.target.owner)}</Pill>
          <Pill color={RISK_COLOR[step.risk]}>{risk}</Pill>
        </div>
        <div className="flex items-center gap-2.5">
          <span className="text-[13px] text-kb-text-secondary">
            {est.money ? `frees ${formatPlanMoney(est.money.monthly, est.money.currency)}/month at list prices` : units ? `frees ${units}` : 'frees nothing'}
          </span>
          <Pill>{step.status.toUpperCase()}</Pill>
          <ChevronDown size={16} className={`text-kb-text-tertiary transition-transform ${open ? 'rotate-180' : ''}`} />
        </div>
      </button>

      {!open && (
        <div className="px-5 pb-3.5 -mt-1.5 pl-[58px] text-[12px] font-mono text-kb-text-secondary">{lines.map((l) => l.text).join(' · ')}</div>
      )}
      {note && <div className="px-5 pb-3 -mt-1 pl-[58px] text-[13px] text-kb-text-secondary">{note}</div>}

      {open && (
        <div className="px-5 pb-4 flex flex-col gap-3">
          <div className="grid gap-2 items-stretch grid-cols-1 lg:grid-cols-[1fr_auto_1fr_auto_1fr]">
            <Stage n={1} title={`Apply${where ? ` ${where}` : ''} · ${containers.length === 1 ? `container ${containers[0]}` : 'containers'}`}>
              {editing ? (
                <StepValuesEditor planId={planId} step={step} onDone={() => setEditing(false)} />
              ) : (
                lines.map((l, i) => (
                  <div key={i} className="flex flex-wrap items-center gap-x-2 font-mono text-[13px] leading-5 text-kb-text-primary">
                    <span>
                      {containers.length > 1 && <span className="text-kb-text-tertiary">{l.container}: </span>}
                      {l.text}
                    </span>
                    {/* Inline, beside the values it edits: one edit for the step. */}
                    {editable && i === lines.length - 1 && (
                      <button
                        type="button"
                        onClick={() => setEditing(true)}
                        title="Set this step's values"
                        className="inline-flex items-center gap-1 px-1.5 -my-0.5 rounded font-sans text-[12px] font-medium text-kb-accent hover:bg-kb-accent-light transition-colors"
                      >
                        <Pencil className="w-3 h-3" aria-hidden /> Edit values
                      </button>
                    )}
                  </div>
                ))
              )}
              <div className="text-[12px] text-kb-text-tertiary">
                per pod{pods ? ` · ${pods} ${pods === 1 ? 'pod' : 'pods'}` : ''} · the rest does not change
              </div>
              {step.imposed && step.engineParams && (
                <div className="text-[12px] text-kb-text-secondary">
                  Values set by a person. The engine proposed{' '}
                  {changeLines({ params: step.engineParams, before: step.before })
                    .map((l) => l.text)
                    .join(', ')}
                  .
                </div>
              )}
              {ev?.qos && (
                <div className="text-[12px] text-kb-text-secondary">
                  The pod goes from {ev.qos.before} to {ev.qos.after}
                  {ev.qos.before === 'Guaranteed' && ': under memory pressure the kernel kills it much sooner'}.
                  {ev.qos.keepWith && (
                    <>
                      {' '}
                      Raising the request too ({[ev.qos.keepWith.cpu && `cpu ${ev.qos.keepWith.cpu}`, ev.qos.keepWith.memory && `memory ${ev.qos.keepWith.memory}`]
                        .filter(Boolean)
                        .join(', ')}
                      ) keeps it {ev.qos.before}.
                    </>
                  )}
                </div>
              )}
              {ev?.webhooks && ev.webhooks.length > 0 && (
                <div className="text-[12px] text-kb-text-secondary">
                  Serves {ev.webhooks.length === 1 ? 'an admission webhook' : `${ev.webhooks.length} admission webhooks`} that fail closed (
                  {Math.min(...ev.webhooks.map((w) => w.timeoutSeconds))} s timeout):{' '}
                  <span className="font-mono">{ev.webhooks.map((w) => w.name).join(', ')}</span>.
                </div>
              )}
              {ev && (ev.cpu || ev.mem) && (
                <div className="text-[12px] text-kb-text-tertiary">
                  from the P95 of the whole workload
                  {ev.cpu && <> · CPU {formatCpuMilli(ev.cpu.p95)}</>}
                  {ev.mem && <> · memory {formatMemBytes(ev.mem.p95)}</>}
                </div>
              )}
              <div className="mt-1 pt-2 border-t border-kb-border text-[12px] text-kb-text-tertiary">
                Only while the workload is unchanged:{' '}
                <span className="font-mono text-kb-text-secondary">
                  pod template {step.precondition.specHash.replace(/^sha256:/, '').slice(0, 12)}…
                  {step.precondition.generation ? ` · generation ${step.precondition.generation}` : ''}
                </span>
              </div>
            </Stage>
            <StageArrow />
            <Stage n={2} title={`Verify · ${step.verification.window}`}>
              <ul className="text-[13px] leading-5 text-kb-text-secondary">
                {step.verification.checks.map((c) => {
                  const outcome = result ? CHECK_OUTCOME[result.checks[c] ?? 'unknown'] : undefined
                  return (
                    <li key={c}>
                      <span className={outcome?.className ?? 'text-kb-text-tertiary'}>{outcome?.mark ?? '○'}</span> {checkWords(c)}
                      {outcome?.note && <span className="text-kb-text-tertiary"> · {outcome.note}</span>}
                    </li>
                  )
                })}
              </ul>
              {result && !result.passed && result.detail && (
                <div className="text-[12px] text-status-error">Failed: {result.detail}. Roll it back (3).</div>
              )}
              <div className="text-[12px] text-kb-text-tertiary">{verifyWhen(step)}</div>
            </Stage>
            <StageArrow />
            <Stage n={3} title="If it fails · roll back">
              {rollback.command && (
                <code className="font-mono text-[12px] leading-[18px] text-kb-text-primary whitespace-pre-wrap break-words">{rollback.command}</code>
              )}
              <div className={rollback.command ? 'text-[12px] text-kb-text-tertiary' : 'text-[13px] leading-5 text-kb-text-secondary'}>{rollback.text}</div>
            </Stage>
          </div>
          {step.provenance && <div className="text-[12px] text-kb-text-tertiary">Evidence: {step.provenance}</div>}
        </div>
      )}
    </div>
  )
}

// verifyWhen says where the step's checks stand: when they passed, when they
// last ran, when they will run, or that they start once it is applied.
function verifyWhen(step: PlanStep): string {
  const at = (iso: string) => `${shortDate(iso)} at ${shortTime(iso)}`
  if (step.status === 'verified' && step.verifiedAt) return `Held for ${step.verification.window}: verified on ${at(step.verifiedAt)}.`
  if (step.verification.result) return `Checked on ${at(step.verification.result.checkedAt)}.`
  if (step.status === 'applied' && step.appliedAt) {
    const due = new Date(new Date(step.appliedAt).getTime() + verifyWindowMs(step.verification.window)).toISOString()
    return `Applied on ${at(step.appliedAt)}; checked from ${at(due)}.`
  }
  if (step.status === 'expired') return 'The workload changed before the step was applied: nothing to check.'
  return `For ${step.verification.window} after the change is applied.`
}
