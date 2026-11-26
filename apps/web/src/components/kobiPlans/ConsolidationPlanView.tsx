import { useState } from 'react'
import { Link } from 'react-router-dom'
import { AlertTriangle, ArrowRight, ChevronDown, Link2 } from 'lucide-react'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { KPI_COLOR, SplitBar } from '@/components/shared/kpi/MiniCharts'
import { TD, TR_STATIC, Th } from '@/components/shared/table/Th'
import { useCopilot } from '@/contexts/CopilotContext'
import { KobiFace } from '@/components/kobi'
import { useOpenInCluster } from '@/hooks/useOpenInCluster'
import { usePlan } from '@/hooks/usePlan'
import { eeConsolidationPlans } from '@/ee/registry'
import { PlansFeedbackLink } from './PlansBeta'
import { PlanExportModal } from './PlanExportModal'
import { CreatedBy, ObservedImpact, PlanHeader, Pill, RISK_COLOR, SoonAction, Stage, StageArrow, shortDate, shortTime } from './planParts'
import type { KobiPlanView, PlanNodeLoad, PlanRisk, PlanStep } from '@/types/kobiPlans'
import {
  CHECK_OUTCOME,
  MINUS,
  RISK_LABEL,
  autoscalerWords,
  blockWords,
  checkWords,
  consolidationEvidenceOf,
  excludedByReason,
  exclusionReasonWords,
  expiresIn,
  formatPlanMoney,
  formatShare,
  freedUnits,
  historyWords,
  nodeCounts,
  ownerWords,
  pendingDependencies,
  planPath,
  poolBeforeOf,
  poolKindWords,
  poolLabel,
  removeNodesOf,
  riskReasonWords,
  statusWords,
  stepWaits,
  verifyWindowMs,
  windowWords,
} from '@/utils/kobiPlans'

// ConsolidationPlanView — a consolidation plan (plans-16): which nodes each
// pool could do without. Freed requests lower the bill only when a node goes,
// so its figure is the bill: the removed nodes' list price, less with
// commitments. The pool lives in the customer's infrastructure, so every
// step is exported, never applied here. Every figure is the plan's own.

const WAIT_COLOR = KPI_COLOR.warn

export function ConsolidationPlanView({ plan }: { plan: KobiPlanView }) {
  const [exporting, setExporting] = useState(false)
  const status = plan.effectiveStatus
  const statusText = statusWords(status, plan.effectiveExpiredReason)
  const expiry = status === 'draft' ? expiresIn(plan.expiresAt) : null
  const est = plan.impact.estimated
  const counts = nodeCounts(plan)
  const deps = plan.dependsOnPlans ?? []
  const waiting = pendingDependencies(plan)
  const planned = plan.steps.some((s) => consolidationEvidenceOf(s)?.scenario === 'planned')
  const pools = [...new Set([...plan.steps.map((s) => s.target.name), ...plan.excluded.map((x) => x.target.name)])]
  const byRisk = (r: PlanRisk) => plan.steps.filter((s) => s.risk === r).length
  const pending = plan.steps.filter((s) => s.status === 'pending').length
  const waits = plan.steps.filter((s) => stepWaits(plan, s)).length
  const bill = plan.clusterBill
  const onePool = plan.steps.length === 1 ? removeNodesOf(plan.steps[0]) : null
  const types = onePool ? (poolBeforeOf(plan.steps[0])?.instanceTypes ?? []) : []

  return (
    <div className="space-y-5">
      <PlanHeader
        plan={plan}
        onExport={() => setExporting(true)}
        context={
          <>
            <CreatedBy plan={plan} />
            {' · '}
            {pools.length === 0 ? 'every pool' : pools.map(poolLabel).join(' and ')}
            {' · '}
            consolidation engine, on {planned ? 'the requests the right-sizing plan leaves' : "today's requests"}, {historyWords(plan)}, computed on{' '}
            {shortDate(plan.engine.computedAt)} at {shortTime(plan.engine.computedAt)}
            {expiry && <> · expires {expiry} if not exported</>}
            {plan.exportedAt && <> · exported on {shortDate(plan.exportedAt)}</>}
            {status === 'expired' && <> · {statusText.toLowerCase()}</>}
          </>
        }
      />

      {deps.map((d) => (
        <div
          key={d.id}
          className="flex flex-wrap items-center justify-between gap-4 rounded-xl border px-5 py-3.5"
          style={{ borderColor: `${WAIT_COLOR}59`, background: `color-mix(in srgb, ${WAIT_COLOR} 6%, var(--kb-card))` }}
        >
          <div className="flex flex-1 min-w-[260px] items-start gap-3">
            <Link2 size={15} className="mt-0.5 shrink-0" style={{ color: WAIT_COLOR }} />
            <div className="min-w-0">
              <div className="text-[14px] font-semibold text-kb-text-primary">
                Depends on{' '}
                <Link to={planPath(d.id)} className="text-kb-accent hover:underline">
                  {d.title}
                </Link>
              </div>
              <div className="text-[13px] text-kb-text-secondary">
                {waiting.some((w) => w.id === d.id)
                  ? `The node is left over with the requests that plan leaves. ${d.stepCount - d.appliedCount} of its ${d.stepCount} steps are still to do; the pool step waits until those requests are down in the cluster.`
                  : 'The node is left over with the requests that plan leaves, and they are in the cluster now.'}
              </div>
            </div>
          </div>
          <div className="flex items-center gap-3 font-mono text-[12px]" style={{ color: WAIT_COLOR }}>
            <span className="w-28 h-1.5 rounded-full bg-kb-elevated overflow-hidden">
              <span className="block h-full rounded-full" style={{ width: `${d.stepCount ? (d.appliedCount / d.stepCount) * 100 : 0}%`, background: WAIT_COLOR }} />
            </span>
            {d.appliedCount} of {d.stepCount} applied
          </div>
        </div>
      ))}

      {plan.engine.lowConfidence && (
        <div className="flex items-start gap-2 rounded-xl border border-kb-border bg-kb-card px-4 py-3 text-[13px] text-kb-text-secondary">
          <AlertTriangle size={15} className="mt-0.5 shrink-0" style={{ color: KPI_COLOR.warn }} />
          Under a week of use: a weekly or monthly peak may not be in it, so every step is high risk until there is more.
        </div>
      )}

      <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
        {est.money ? (
          <KpiCard
            primary
            value={`${MINUS}${formatPlanMoney(est.money.monthly, est.money.currency)}`}
            unit="/ month"
            description="on the cluster bill, at list prices"
            caption={
              bill && bill.currency === est.money.currency
                ? `${formatPlanMoney(bill.monthly, bill.currency)} → ${formatPlanMoney(Math.round((bill.monthly - est.money.monthly) * 100) / 100, bill.currency)} · less with commitments`
                : 'less with Savings Plans or reservations'
            }
          />
        ) : (
          <KpiCard primary value={freedUnits(est) ?? '—'} description="capacity it removes" caption="no OpenCost prices · capacity only" />
        )}
        <KpiCard
          value={`${counts.before} → ${counts.after}`}
          unit="nodes"
          description={onePool ? [poolLabel(onePool.pool), ...types].join(' · ') : `${counts.pools.length} pools`}
          viz={counts.before > 0 ? <NodeBar before={counts.before} after={counts.after} /> : undefined}
          caption="room to lose one · one per zone"
        />
        <KpiCard
          value={plan.steps.length}
          unit={plan.steps.length === 1 ? 'step' : 'steps'}
          description={waits > 0 ? 'waits for right-sizing' : `${pending} pending`}
          viz={
            plan.steps.length > 1 ? (
              <SplitBar
                parts={(['low', 'medium', 'high'] as PlanRisk[]).map((r) => ({ value: byRisk(r), color: RISK_COLOR[r] }))}
                left={`${byRisk('low')} low · ${byRisk('medium')} medium`}
                right={`${byRisk('high')} high`}
              />
            ) : undefined
          }
          caption="by risk"
        />
        <ObservedImpact plan={plan} />
      </div>

      {plan.steps.map((s) => (
        <WhyLeftOver key={s.id} step={s} />
      ))}

      <section className="space-y-3">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <h2 className="text-base font-semibold text-kb-text-primary">
            Steps, in the plan&apos;s order
            {onePool && <span className="font-normal text-kb-text-secondary"> · {poolLabel(onePool.pool)}</span>}
          </h2>
          {plan.steps.some((s) => s.target.owner?.kind !== 'operator') && (
            <span className="text-[12px] text-kb-text-tertiary">
              Who sets a pool&apos;s size is not visible from the cluster: change it where it is declared too, or its next run brings the node back.
            </span>
          )}
        </div>
        {plan.steps.length === 0 ? (
          <div className="bg-kb-card border border-kb-border rounded-xl p-5 text-[13px] text-kb-text-secondary">No pool has a node to spare.</div>
        ) : (
          plan.steps.map((s, i) => <PoolStepRow key={s.id} plan={plan} step={s} defaultOpen={i === 0} />)
        )}
      </section>

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
                  {excludedByReason(plan.excluded).flatMap((g) =>
                    g.items.map((x, i) => (
                      <tr key={`${g.reason}-${i}`} className={`${TR_STATIC} align-top`}>
                        <td className={`${TD} text-kb-text-primary whitespace-nowrap`}>{poolLabel(x.target.name)}</td>
                        <td className={`${TD} text-kb-text-primary whitespace-nowrap`}>{exclusionReasonWords(x.reason)}</td>
                        <td className="px-3 py-2.5 text-xs leading-5 text-kb-text-secondary">{x.detail}</td>
                      </tr>
                    )),
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </section>
      )}

      <section className="space-y-3">
        <h2 className="text-base font-semibold text-kb-text-primary">Notes and versions</h2>
        <ul className="space-y-1.5 text-[13px] text-kb-text-secondary">
          {[...plan.notes].reverse().map((n, i) => (
            <li key={i}>
              <span className="font-mono text-[12px] text-kb-text-tertiary">v{n.version} · {shortDate(n.at)}</span> {n.text}
            </li>
          ))}
          <li>
            <span className="font-mono text-[12px] text-kb-text-tertiary">v1 · {shortDate(plan.createdAt)}</span> Created from Kobi&apos;s chat, for every pool of the cluster.
          </li>
        </ul>
      </section>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[12px] text-kb-text-tertiary">
          Kobi computes none of these figures: which nodes are left over comes from the consolidation engine and the prices from OpenCost. Kobi explains them.
        </p>
        <PlansFeedbackLink />
      </div>
      {exporting && <PlanExportModal planId={plan.id} title={plan.title} onClose={() => setExporting(false)} />}
    </div>
  )
}

// NodeBar: one segment per node, the ones that stay filled and the ones that
// go outlined.
function NodeBar({ before, after }: { before: number; after: number }) {
  const shown = Math.min(before, 24)
  const kept = Math.round((after / before) * shown)
  return (
    <div className="flex gap-1 w-full" role="img" aria-label={`${after} of ${before} nodes stay`}>
      {Array.from({ length: shown }, (_, i) => (
        <span
          key={i}
          className="h-1.5 flex-1 rounded-full"
          style={i < kept ? { background: KPI_COLOR.ok } : { border: '1px dashed var(--kb-text-tertiary)' }}
        />
      ))}
    </div>
  )
}

// WhyLeftOver: each node of the pool, its load before and after, and its
// list price; the node that goes, last and marked.
function WhyLeftOver({ step }: { step: PlanStep }) {
  const params = removeNodesOf(step)
  const ev = consolidationEvidenceOf(step)
  if (!params || !ev || step.status === 'expired') return null
  const gone = params.nodes.map((n) => n.name)
  const rows: PlanNodeLoad[] = [...ev.loads].sort((a, b) => Number(!!a.removed) - Number(!!b.removed) || a.node.localeCompare(b.node))
  const ceiling = Math.round(ev.ceiling * 100)
  const stay = params.nodesAfter
  const currency = step.impact.estimated.money?.currency
  const price = (m?: number) => (m === undefined || !currency ? '—' : `${formatPlanMoney(m, currency)}/mo`)
  const subject = gone.length === 1 ? `one node of ${poolLabel(params.pool)} is` : `${gone.length} nodes of ${poolLabel(params.pool)} are`

  return (
    <section className="space-y-3">
      <h2 className="text-base font-semibold text-kb-text-primary">Why {subject} left over</h2>
      <p className="text-[13px] leading-5 text-kb-text-secondary">
        {ev.scenario === 'planned' ? 'With the requests right-sizing leaves' : "With today's requests"}, the pods of {gone.join(' and ')} fit on the other{' '}
        {stay}. Each stays under {ceiling}% of its allocatable, and if one fails the other {Math.max(stay - 1, 1)} take its pods. It counts the larger of
        request and use, and keeps each pod in its volume&apos;s zone.
        {ev.scenario === 'planned' && (ev.removableToday === 0 ? " With today's requests, none is left over." : ` With today's requests, ${ev.removableToday} would be.`)}
      </p>
      {ev.blocked && ev.blocked.length > 0 && (
        <ul className="text-[12px] leading-5 text-kb-text-tertiary">
          {ev.blocked.map((b) => (
            <li key={b.node}>Kept: {blockWords(b)}.</li>
          ))}
        </ul>
      )}
      <div className="kb-panel overflow-hidden">
        <div className="overflow-x-auto">
          <table className="w-full min-w-[640px] text-left">
            <thead>
              <tr className="border-b border-kb-border">
                <Th>Node</Th>
                <Th>Zone</Th>
                <Th right>{ev.scenario === 'planned' ? 'After right-sizing' : 'Load today'} · CPU / mem</Th>
                <Th right>After</Th>
                <Th right>List price</Th>
              </tr>
            </thead>
            <tbody>
              {rows.map((l) => (
                <tr
                  key={l.node}
                  className={TR_STATIC}
                  style={l.removed ? { background: `color-mix(in srgb, ${KPI_COLOR.ok} 8%, transparent)` } : undefined}
                >
                  <td className={`${TD} font-mono text-kb-text-primary whitespace-nowrap`}>{l.node}</td>
                  <td className={`${TD} font-mono text-kb-text-secondary whitespace-nowrap`}>{l.zone ?? '—'}</td>
                  <td className={`${TD} font-mono text-right text-kb-text-secondary whitespace-nowrap`}>
                    {formatShare(l.cpuBefore)} · {formatShare(l.memBefore)}
                  </td>
                  <td className={`${TD} font-mono text-right whitespace-nowrap ${l.removed ? 'text-status-ok' : ''}`}>
                    {l.removed ? <span className="font-sans font-medium">leaves the pool</span> : `${formatShare(l.cpuAfter)} · ${formatShare(l.memAfter)}`}
                  </td>
                  <td className={`${TD} font-mono text-right whitespace-nowrap ${l.removed ? 'text-status-ok' : 'text-kb-text-secondary'}`}>
                    {l.removed ? `${MINUS}${price(l.monthly)}` : price(l.monthly)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  )
}

// PoolStepRow: one pool's step, and below it the three stages: apply where
// the pool is declared, verify for a week, grow it back if it fails.
function PoolStepRow({ plan, step, defaultOpen }: { plan: KobiPlanView; step: PlanStep; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  const params = removeNodesOf(step)
  const before = poolBeforeOf(step)
  const ev = consolidationEvidenceOf(step)
  if (!params) return null
  const money = step.impact.estimated.money
  const waits = stepWaits(plan, step)
  const reasons = step.riskReasons.filter((r) => r !== 'depends_on_plan')
  const risk = [RISK_LABEL[step.risk], ...reasons.map(riskReasonWords)].join(' · ')
  const result = step.verification.result
  const nodes = params.nodes.map((n) => n.name)
  // Removed one at a time: what is already gone, while the step is to do.
  const gone = new Set(step.nodesGone ?? [])
  const emptying = new Set(step.nodesCordoned ?? [])
  const inProgress = step.status === 'pending' && (gone.size > 0 || emptying.size > 0)

  return (
    <div className="rounded-xl border border-kb-border bg-kb-card">
      <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className="w-full flex flex-wrap items-center justify-between gap-3 px-5 py-3.5 text-left">
        <div className="flex flex-wrap items-center gap-2.5 min-w-0">
          <span className="w-7 h-7 rounded-lg bg-kb-elevated text-[13px] font-mono text-kb-text-secondary flex items-center justify-center shrink-0">{step.order}</span>
          <span className="text-[15px] font-semibold text-kb-text-primary">{poolLabel(params.pool)}</span>
          <Pill>{poolKindWords(params.poolSource)}</Pill>
          <Pill>owner: {ownerWords(step.target.owner)}</Pill>
          <Pill color={RISK_COLOR[step.risk]}>{risk}</Pill>
        </div>
        <div className="flex items-center gap-2.5">
          <span className={`text-[13px] ${money ? 'text-status-ok' : 'text-kb-text-secondary'}`}>
            {money ? `${MINUS}${formatPlanMoney(money.monthly, money.currency)}/month on the bill` : `removes ${freedUnits(step.impact.estimated) ?? 'nothing'}`}
          </span>
          {waits ? (
            <Pill color={WAIT_COLOR}>WAITS FOR RIGHT-SIZING</Pill>
          ) : inProgress ? (
            <Pill color={KPI_COLOR.info}>
              IN PROGRESS · {gone.size} OF {nodes.length}
            </Pill>
          ) : (
            <Pill>{step.status.toUpperCase()}</Pill>
          )}
          <ChevronDown size={16} className={`text-kb-text-tertiary transition-transform ${open ? 'rotate-180' : ''}`} />
        </div>
      </button>

      {!open && (
        <div className="px-5 pb-3.5 -mt-1.5 pl-[58px] text-[12px] font-mono text-kb-text-secondary">
          nodes {params.nodesBefore} → {params.nodesAfter} · remove{' '}
          {nodes.map((n) => (gone.has(n) ? `${n} (gone)` : emptying.has(n) ? `${n} (being emptied)` : n)).join(', ')}
        </div>
      )}

      {open && (
        <div className="px-5 pb-4 flex flex-col gap-3">
          <div className="grid gap-2 items-stretch grid-cols-1 lg:grid-cols-[1fr_auto_1fr_auto_1fr]">
            <Stage n={1} title={`Apply where it is declared · ${poolKindWords(params.poolSource)} ${params.pool}`}>
              <div className="font-mono text-[13px] leading-5 text-kb-text-primary">
                nodes {params.nodesBefore} → {params.nodesAfter}
              </div>
              {params.nodes.map((n) => (
                <div key={n.name} className="font-mono text-[13px] leading-5 text-kb-text-primary">
                  <span className={gone.has(n.name) ? 'line-through text-kb-text-tertiary' : ''}>remove {n.name}</span>
                  {n.zone && <span className="text-kb-text-tertiary"> · {n.zone}</span>}
                  {gone.has(n.name) && <span className="ml-2 font-sans text-[12px] text-status-ok">✓ gone</span>}
                  {emptying.has(n.name) && <span className="ml-2 font-sans text-[12px] text-status-info">◐ being emptied</span>}
                </div>
              ))}
              <div className="text-[12px] text-kb-text-tertiary">Exported, never applied: the pool lives in your infrastructure.</div>
              <div className="mt-1 pt-2 border-t border-kb-border flex flex-col gap-2">
                <div className="text-[12px] leading-[17px] text-kb-text-secondary">
                  First, empty {nodes.length === 1 ? nodes[0] : 'each node'} with cordon and drain{nodes.length > 1 ? ', one at a time' : ''}. Drain respects
                  PodDisruptionBudgets; the export has the commands.
                </div>
                <div>
                  <SoonAction label="Cordon and drain" small />
                </div>
                <div className="text-[12px] leading-[17px] text-kb-text-tertiary">{autoscalerWords(params.autoscaler)}</div>
              </div>
            </Stage>
            <StageArrow />
            <Stage n={2} title={`Verify · ${windowWords(step.verification.window)}`}>
              <ul className="text-[13px] leading-5 text-kb-text-secondary">
                {step.verification.checks.map((c) => {
                  const outcome = result ? CHECK_OUTCOME[result.checks[c] ?? 'unknown'] : undefined
                  return (
                    <li key={c}>
                      <span className={outcome?.className ?? 'text-kb-text-tertiary'}>{outcome?.mark ?? '○'}</span>{' '}
                      {c === 'pool_held' ? `the pool stays at ${params.nodesAfter} nodes` : checkWords(c)}
                    </li>
                  )
                })}
                <li>
                  <span className="text-kb-text-tertiary">○</span> the cluster bill, before and after
                </li>
              </ul>
              {result && !result.passed && result.detail && <div className="text-[12px] text-status-error">Failed: {result.detail}. Grow the pool back (3).</div>}
              <div className="text-[12px] text-kb-text-tertiary">{poolVerifyWhen(step)}</div>
            </Stage>
            <StageArrow />
            <Stage n={3} title="If it fails · grow it back">
              <div className="font-mono text-[13px] leading-5 text-kb-text-primary">
                nodes {params.nodesAfter} → {params.nodesBefore}
              </div>
              <div className="text-[12px] leading-[17px] text-kb-text-tertiary">
                Where the pool is declared. While the node still exists, uncordon it and it takes pods again.
              </div>
            </Stage>
          </div>
          {before && (
            <div className="text-[12px] text-kb-text-tertiary">
              Only while the pool is unchanged: {before.nodes} nodes{before.instanceTypes.length > 0 ? `, ${before.instanceTypes.join(', ')}` : ''}.
            </div>
          )}
          {ev && (
            <div className="text-[12px] text-kb-text-tertiary">
              Evidence: consolidation · {ev.scenario === 'planned' ? "the right-sizing plan's requests" : "today's requests"} · {Math.round(ev.ceiling * 100)}%
              ceiling · room to lose a node
              {before && before.zones.length > 0 ? ` · ${before.zones.length} ${before.zones.length === 1 ? 'zone' : 'zones'}` : ''}
              {ev.windowDays ? ` · ${ev.windowDays.toLocaleString(undefined, { maximumFractionDigits: 1 })} days of use` : ''}
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function poolVerifyWhen(step: PlanStep): string {
  const at = (iso: string) => `${shortDate(iso)} at ${shortTime(iso)}`
  if (step.status === 'verified' && step.verifiedAt) return `Held for ${windowWords(step.verification.window)}: verified on ${at(step.verifiedAt)}.`
  if (step.verification.result) return `Checked on ${at(step.verification.result.checkedAt)}.`
  if (step.status === 'applied' && step.appliedAt) {
    const due = new Date(new Date(step.appliedAt).getTime() + verifyWindowMs(step.verification.window)).toISOString()
    return `The pool reached its new size on ${at(step.appliedAt)}; checked from ${at(due)}.`
  }
  if (step.status === 'expired') return 'The pool changed before the step was applied, or grew back after it: nothing to check.'
  const params = removeNodesOf(step)
  const gone = step.nodesGone?.length ?? 0
  const emptying = step.nodesCordoned?.length ?? 0
  if (params && (gone > 0 || emptying > 0) && step.status === 'pending') {
    const left = params.nodes.length - gone
    return `${gone} of ${params.nodes.length} nodes gone; ${left} more to go${emptying > 0 ? `, ${emptying} being emptied` : ''}. The week of checks starts once the pool is at ${params.nodesAfter} nodes.`
  }
  return `For ${windowWords(step.verification.window)} after the node is gone.`
}

// ConsolidationLink: on a right-sizing plan, where what it frees meets the
// bill. With a consolidation plan that counts on it, that plan and its
// figure; without one, Kobi can be asked for it (the bill falls only when
// nodes go).
export function ConsolidationLink({ plan }: { plan: KobiPlanView }) {
  const { config, askInCluster, openPanel, activeClusterContext } = useCopilot()
  const { targetFor, switchTo } = useOpenInCluster()
  // Consolidation plans are for paid plans (26-nov); an unresolved plan
  // (self-hosted) has them.
  const { isFree } = usePlan()
  if (plan.kind !== 'rightsizing' || plan.effectiveStatus === 'expired') return null
  const est = plan.impact.estimated
  if (est.cpuMilli <= 0 && est.memBytes <= 0) return null
  const next = plan.leadsTo?.[0]

  if (next) {
    const money = next.estimated.money
    const left = plan.steps.filter((s) => s.status === 'pending').length
    const applied = plan.steps.filter((s) => s.status === 'applied' || s.status === 'verified').length
    return (
      <div className="flex flex-wrap items-center justify-between gap-4 rounded-xl border border-kb-border bg-kb-card px-5 py-4">
        <div className="flex flex-1 min-w-[260px] items-start gap-3">
          <Link2 size={15} className="mt-0.5 shrink-0 text-kb-accent" />
          <div className="min-w-0">
            <div className="text-[14px] font-semibold text-kb-text-primary">What this plan frees lowers the bill once a node goes</div>
            <div className="text-[13px] text-kb-text-secondary">
              With these requests a node is left over. {next.title} removes it
              {money ? `: ${MINUS}${formatPlanMoney(money.monthly, money.currency)}/month on the bill, at list prices` : ''}.
              {left > 0 && ` It waits for ${left} ${left === 1 ? 'step' : 'steps'} of this plan${applied > 0 ? `; ${applied} ${applied === 1 ? 'is' : 'are'} already applied` : ''}.`}
            </div>
          </div>
        </div>
        <Link
          to={planPath(next.id)}
          className="shrink-0 inline-flex items-center gap-1.5 h-9 px-3.5 rounded-lg border border-kb-border text-[13px] font-medium text-kb-text-primary hover:bg-kb-elevated transition-colors"
        >
          Open the consolidation <ArrowRight className="w-3.5 h-3.5" />
        </Link>
      </div>
    )
  }

  // Kobi is reached by its own button, and only when it is configured. Where
  // consolidation is not offered (the community edition, or Free) the card
  // still says why the bill does not move, and where it comes with.
  const offered = eeConsolidationPlans && !isFree
  if (!config?.enabled && offered) return null
  const ask = async () => {
    const question = `Make a consolidation plan for this cluster: which nodes are left over with the requests the right-sizing plan «${plan.title}» leaves.`
    const target = targetFor(plan.clusterId)
    if (target) {
      if (!(await switchTo(target.context))) return
      openPanel()
      askInCluster(target.context, question)
      return
    }
    openPanel()
    if (activeClusterContext) askInCluster(activeClusterContext, question)
  }
  return (
    <div className="flex flex-wrap items-center justify-between gap-4 rounded-xl border border-kb-border bg-kb-card px-5 py-4">
      <div className="flex flex-1 min-w-[260px] items-start gap-3">
        <Link2 size={15} className="mt-0.5 shrink-0 text-kb-text-tertiary" />
        <div className="min-w-0">
          <div className="text-[14px] font-semibold text-kb-text-primary">The capacity it frees sits idle on the same nodes</div>
          <div className="text-[13px] text-kb-text-secondary">
            The bill falls only when nodes go. KubeBolt works out whether one is left over, keeping room to lose one.
          </div>
        </div>
      </div>
      {!eeConsolidationPlans ? (
        <span className="shrink-0 text-[12px] text-kb-text-tertiary">Consolidation plans come with KubeBolt Cloud and Enterprise.</span>
      ) : isFree ? (
        <span className="shrink-0 text-[12px] text-kb-text-tertiary">Consolidation plans come with the paid plans.</span>
      ) : (
        <button
          type="button"
          onClick={ask}
          className="shrink-0 inline-flex items-center gap-1.5 h-9 px-3.5 rounded-lg bg-kb-accent text-white text-[13px] font-semibold hover:opacity-90 transition-opacity"
        >
          <KobiFace size={16} still />
          Ask Kobi for a consolidation plan
        </button>
      )}
    </div>
  )
}
