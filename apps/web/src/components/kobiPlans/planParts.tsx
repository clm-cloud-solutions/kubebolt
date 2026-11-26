import { Link } from 'react-router-dom'
import { ArrowRight, ChevronRight, Upload } from 'lucide-react'
import { BarList, KPI_COLOR, SplitBar } from '@/components/shared/kpi/MiniCharts'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { BetaPill } from './PlansBeta'
import { DiscardPlanButton } from './DiscardPlanButton'
import { VerifyPlanButton } from './VerifyPlanButton'
import type { KobiPlanView, PlanRisk } from '@/types/kobiPlans'
import { RISK_LABEL, formatCpuMilli, formatMemBytes, formatPlanMoney, observedImpact, requestedBefore, statusWords } from '@/utils/kobiPlans'

// The parts every plan view shares, whatever its kind: the header with its
// actions, the outlined pills, the stages of a step.

export const RISK_COLOR: Record<PlanRisk, string> = { low: KPI_COLOR.ok, medium: KPI_COLOR.warn, high: KPI_COLOR.err }

export const shortDate = (iso: string) => new Date(iso).toLocaleDateString(undefined, { day: 'numeric', month: 'short' })
export const shortTime = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })

// PlanHeader: the breadcrumb, the title and its state chips with the actions
// beside them, and below at full width the context and the reminder that the
// steps are suggestions.
export function PlanHeader({ plan, context, onExport }: { plan: KobiPlanView; context: React.ReactNode; onExport: () => void }) {
  const status = plan.effectiveStatus
  const statusText = statusWords(status, plan.effectiveExpiredReason)
  const pending = plan.steps.filter((s) => s.status === 'pending').length
  return (
    <>
      <div className="flex items-center gap-1.5 text-[11px] font-mono text-kb-text-tertiary">
        <span>Kobi</span>
        <ChevronRight size={12} />
        <Link to="/plans" className="hover:text-kb-text-primary transition-colors">Plans</Link>
        <ChevronRight size={12} />
        <span className="text-kb-text-primary">{plan.clusterName || plan.clusterId}</span>
      </div>

      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="min-w-0 flex flex-col gap-2">
            <div className="flex items-center gap-2">
              <h1 className="text-xl font-semibold text-kb-text-primary tracking-tight">{plan.title}</h1>
              <BetaPill />
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Pill>{plan.kind}</Pill>
              <Pill>{statusText.toUpperCase()}</Pill>
              {plan.maxRisk && <Pill color={RISK_COLOR[plan.maxRisk]}>max {RISK_LABEL[plan.maxRisk]}</Pill>}
              <Pill>v{plan.version}</Pill>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {status === 'exported' && (
              <VerifyPlanButton
                planId={plan.id}
                className="inline-flex items-center gap-1.5 h-9 px-3.5 rounded-lg border border-kb-border text-[13px] font-medium text-kb-text-secondary hover:text-kb-text-primary hover:bg-kb-elevated disabled:opacity-60 transition-colors"
              />
            )}
            <button
              type="button"
              onClick={onExport}
              disabled={status === 'expired'}
              title={status === 'expired' ? `A ${statusText.toLowerCase()} plan is not exported: ask Kobi for a new one` : undefined}
              className="inline-flex items-center gap-1.5 h-9 px-3.5 rounded-lg bg-kb-accent text-white text-[13px] font-semibold hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed transition-opacity"
            >
              <Upload className="w-3.5 h-3.5" /> Export
            </button>
            <SoonAction label="Open a PR" />
            <SoonAction label="Run with Autopilot" />
            {/* A plan with every step applied is done, kept to measure its impact. */}
            {(status === 'draft' || status === 'exported') && pending > 0 && (
              <DiscardPlanButton
                planId={plan.id}
                title={plan.title}
                className="inline-flex items-center h-9 px-3.5 rounded-lg border border-kb-border text-[13px] font-medium text-kb-text-secondary hover:text-status-error hover:border-status-error/40 transition-colors"
              />
            )}
          </div>
        </div>
        <p className="text-[13px] leading-5 text-kb-text-secondary">{context}</p>
        {/* Suggestions, never instructions: the decision stays with the person
            (26-nov). */}
        <p className="text-[12px] leading-[17px] text-kb-text-tertiary">
          These are KubeBolt&apos;s suggestions: whether and how to apply each step is your decision. Measure each change before and after.
        </p>
      </div>
    </>
  )
}

// createdWords: «Created by Ana in Kobi's chat on Nov 26».
export function CreatedBy({ plan }: { plan: KobiPlanView }) {
  const author = plan.createdBy || plan.source.userName
  return (
    <>
      Created
      {author && (
        <>
          {' by '}
          <span className="font-medium text-kb-text-primary">{author}</span>
        </>
      )}{' '}
      in Kobi&apos;s chat on {shortDate(plan.createdAt)}
    </>
  )
}

// ObservedImpact: the plan's observed-impact card, the memory it freed set
// apart in its sentence.
export function ObservedImpact({ plan }: { plan: KobiPlanView }) {
  const { highlight, description, compare, bills, progress, ...card } = observedImpact(plan)
  let viz: React.ReactNode
  if (compare) {
    const fmt = compare.dim === 'cpu' ? formatCpuMilli : formatMemBytes
    viz = (
      <BarList
        rows={[
          { label: <Label>estimated</Label>, value: compare.estimated, display: fmt(compare.estimated) },
          { label: <Label>measured</Label>, value: compare.measured, display: fmt(compare.measured) },
        ]}
      />
    )
  } else if (bills) {
    viz = (
      <BarList
        color={KPI_COLOR.muted}
        rows={[
          { label: <Label>before</Label>, value: bills.before, display: formatPlanMoney(bills.before, bills.currency) },
          { label: <Label>after</Label>, value: bills.after, display: formatPlanMoney(bills.after, bills.currency) },
        ]}
      />
    )
  } else if (progress) {
    viz = (
      <SplitBar
        parts={[
          { value: progress.day, color: KPI_COLOR.ok },
          { value: progress.of - progress.day, color: 'transparent' },
        ]}
        left="applied"
        right={`day ${progress.of}`}
      />
    )
  }
  return <KpiCard {...card} description={emphasize(description, highlight)} viz={viz} />
}

// Label: one width for a comparison's labels, so its bars start together.
function Label({ children }: { children: React.ReactNode }) {
  return <span className="inline-block w-[66px]">{children}</span>
}

// FreesSplit: the «capacity it frees» chart. What the plan's workloads
// request today, split into what stays and what the plan frees, in the
// card's own unit (CPU first).
export function FreesSplit({ plan }: { plan: KobiPlanView }) {
  const est = plan.impact.estimated
  const dim = est.cpuMilli > 0 ? 'cpu' : est.memBytes > 0 ? 'mem' : null
  if (!dim) return null
  const freed = dim === 'cpu' ? est.cpuMilli : est.memBytes
  const before = requestedBefore(plan, dim)
  if (before === null || before < freed || before <= 0) return null
  const fmt = dim === 'cpu' ? formatCpuMilli : formatMemBytes
  return (
    <SplitBar
      parts={[
        { value: before - freed, color: KPI_COLOR.muted },
        { value: freed, color: KPI_COLOR.ok },
      ]}
      left={`${fmt(before - freed)} stays`}
      right={`${fmt(freed)} freed of ${fmt(before)}`}
    />
  )
}

// emphasize sets apart, in a card's sentence, the part that is a figure of its
// own: the memory a plan frees, beside the CPU the card shows large.
export function emphasize(text: string, part?: string): React.ReactNode {
  const i = part ? text.indexOf(part) : -1
  if (!part || i < 0) return text
  return (
    <>
      {text.slice(0, i)}
      <span className="font-semibold">{part}</span>
      {text.slice(i + part.length)}
    </>
  )
}

export function Stage({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0 rounded-lg border border-kb-border bg-kb-bg px-3.5 py-3 flex flex-col gap-1.5">
      <div className="flex items-center gap-2 text-[10px] font-mono uppercase tracking-[0.06em] text-kb-text-tertiary">
        <span className="w-4 h-4 rounded-full bg-kb-elevated text-kb-text-secondary flex items-center justify-center text-[10px]">{n}</span>
        {title}
      </div>
      {children}
    </div>
  )
}

export function StageArrow() {
  return (
    <div className="hidden lg:flex items-center justify-center text-kb-text-tertiary" aria-hidden>
      <ArrowRight size={14} />
    </div>
  )
}

// Pill: the canvas's outlined chip; a colour tints the text and the border.
export function Pill({ children, color }: { children: React.ReactNode; color?: string }) {
  return (
    <span
      className="px-2 py-0.5 rounded-full border text-[11px] font-mono"
      style={color ? { color, borderColor: `${color}73` } : { color: 'var(--kb-text-secondary)', borderColor: 'var(--kb-border)' }}
    >
      {children}
    </span>
  )
}

export function SoonAction({ label, small }: { label: string; small?: boolean }) {
  return (
    <span
      title={`${label} — coming soon`}
      aria-disabled="true"
      className={`inline-flex items-center gap-1.5 ${small ? 'h-8 px-3 text-[12px]' : 'h-9 px-3.5 text-[13px]'} rounded-lg border border-kb-border font-medium text-kb-text-tertiary cursor-not-allowed`}
    >
      {label}
      <span className="text-[9px] font-mono font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded-full bg-kb-accent-light text-kb-accent">Soon</span>
    </span>
  )
}
