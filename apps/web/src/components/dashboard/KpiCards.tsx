import { Link } from 'react-router-dom'
import { ArrowRight, ShieldOff } from 'lucide-react'
import type { ClusterOverview, HealthCheck } from '@/types/kubernetes'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'

type Accent = 'ok' | 'warn' | 'err' | 'restricted'

interface KpiCardsProps {
  overview: ClusterOverview
}

// KpiCards renders four headline KPIs aligned with the questions an
// operator opens the dashboard to answer:
//   1. Is this cluster healthy right now?
//   2. Are all my nodes participating?
//   3. Are my pods running?
//   4. Is anything actionable?
//
// Layout: label top-left, status pill or "view all →" link top-right,
// big numeric value with a subtle suffix, a one-line context caption,
// and an at-a-glance signal block at the bottom that USES the data we
// already have:
//   - Health / Nodes / Pods → thin progress bar (score%, ready/total).
//   - Insights              → severity breakdown chips (or muted text
//                             when the count is zero).
// We considered session-scoped sparklines but on a stable cluster they
// degenerate to a flat horizontal line that conveys nothing. Progress
// bars + severity chips read clearly even when the underlying value
// hasn't changed.
export function KpiCards({ overview }: KpiCardsProps) {
  const perms = overview.permissions
  const restricted = (key: string) => perms != null && perms[key] === false

  const health = overview.health
  const insights = health?.insights
  const insightsTotal = (insights?.critical ?? 0) + (insights?.warning ?? 0) + (insights?.info ?? 0)

  const nodesReady = overview.nodes?.ready ?? 0
  const nodesTotal = overview.nodes?.total ?? 0
  const podsReady = overview.pods?.ready ?? 0
  const podsTotal = overview.pods?.total ?? 0
  const nodesNotReady = overview.nodes?.notReady ?? 0
  const podsNotReady = overview.pods?.notReady ?? 0

  const healthAccent: Accent =
    health?.status === 'healthy' ? 'ok' : health?.status === 'warning' ? 'warn' : 'err'

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
      {/* Cluster Health — pill IS the headline state. Footer bar
          renders the score itself as a proportion of 100 in the
          accent color. The sub-line shows the single most actionable
          check, and the full check breakdown lives in a hover
          tooltip that matches the metric / cluster-map tooltip
          pattern (TooltipPanel + rows). */}
      <HealthCard health={health} accent={healthAccent} />

      <Kpi
        label="Nodes"
        accent={restricted('nodes') ? 'restricted' : nodesNotReady > 0 ? 'warn' : 'ok'}
        pill={restricted('nodes') ? null : { kind: 'link', text: 'view all', to: '/nodes' }}
        value={restricted('nodes') ? null : `${nodesReady}`}
        valueSuffix={restricted('nodes') ? undefined : `/ ${nodesTotal}`}
        sub={
          restricted('nodes')
            ? 'No access'
            : nodesNotReady > 0
              ? `${nodesNotReady} not ready`
              : 'all ready'
        }
        footer={
          !restricted('nodes') && nodesTotal > 0 ? (
            <ProgressBar
              percent={(nodesReady / nodesTotal) * 100}
              accent={nodesNotReady > 0 ? 'warn' : 'ok'}
            />
          ) : null
        }
      />

      <Kpi
        label="Pods"
        accent={restricted('pods') ? 'restricted' : podsNotReady > 0 ? 'warn' : 'ok'}
        pill={restricted('pods') ? null : { kind: 'link', text: 'view all', to: '/pods' }}
        value={restricted('pods') ? null : `${podsReady}`}
        valueSuffix={restricted('pods') ? undefined : `/ ${podsTotal}`}
        sub={
          restricted('pods')
            ? 'No access'
            : podsNotReady > 0
              ? `${podsNotReady} not running`
              : 'all running'
        }
        footer={
          !restricted('pods') && podsTotal > 0 ? (
            <ProgressBar
              percent={(podsReady / podsTotal) * 100}
              accent={podsNotReady > 0 ? 'warn' : 'ok'}
            />
          ) : null
        }
      />

      <Kpi
        label="Insights"
        accent={
          (insights?.critical ?? 0) > 0 ? 'err' : (insights?.warning ?? 0) > 0 ? 'warn' : 'ok'
        }
        pill={{ kind: 'link', text: 'view', to: '/insights' }}
        value={`${insightsTotal}`}
        sub={insightsTotal === 0 ? 'no issues detected' : undefined}
        footer={insightsTotal > 0 ? <SeverityChips insights={insights} /> : null}
      />
    </div>
  )
}

type Pill =
  // Static status pill — no navigation. Used for "Cluster Health" where
  // the pill IS the headline state, not a CTA.
  | { kind: 'status'; text: string; accent: Accent }
  // Link pill — small "view all →" affordance pointing at the related
  // resource page. Renders to the right of the label, replaces the
  // older standalone icon chip.
  | { kind: 'link'; text: string; to: string }

interface KpiProps {
  label: string
  accent: Accent
  pill: Pill | null
  value: string | null
  valueSuffix?: string
  sub?: string
  footer?: React.ReactNode
}

function Kpi({ label, accent, pill, value, valueSuffix, sub, footer }: KpiProps) {
  const restricted = accent === 'restricted'
  const subColor =
    accent === 'err'
      ? 'text-status-error'
      : accent === 'warn'
        ? 'text-status-warn'
        : accent === 'restricted'
          ? 'text-kb-text-tertiary'
          : 'text-status-ok'

  return (
    <div
      className={`bg-kb-card border border-kb-border rounded-[10px] p-4 transition-colors hover:bg-kb-card-hover ${restricted ? 'opacity-60' : ''}`}
    >
      <div className="flex items-center justify-between gap-2 mb-3 min-h-[20px]">
        <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary truncate">
          {label}
        </span>
        {pill && <PillView pill={pill} />}
      </div>

      {restricted ? (
        <>
          <div className="flex items-center gap-1.5 mb-1">
            <ShieldOff className="w-4 h-4 text-status-warn" />
            <span className="text-sm font-medium text-kb-text-secondary">No access</span>
          </div>
          <div className="text-[10px] font-mono text-kb-text-tertiary">Insufficient permissions</div>
        </>
      ) : (
        <>
          <div className="flex items-baseline gap-1.5 mb-1">
            <span className="text-3xl font-semibold text-kb-text-primary tabular-nums leading-none">
              {value}
            </span>
            {valueSuffix && (
              <span className="text-[11px] font-mono text-kb-text-tertiary tabular-nums">
                {valueSuffix}
              </span>
            )}
          </div>
          {sub && <div className={`text-[11px] font-mono ${subColor}`}>{sub}</div>}
          {footer && <div className="mt-2.5">{footer}</div>}
        </>
      )}
    </div>
  )
}

// ProgressBar — thin horizontal fill used as the footer signal block.
// Inline `var(--kb-accent)` only triggers for the ok accent because
// it's the brand color; warn/err route through status-* utility
// classes that resolve to the project's hex constants. Avoids the
// Tailwind-opacity-on-hex-CSS-var pitfall that left the original
// Top Workloads bars invisible.
function ProgressBar({ percent, accent }: { percent: number; accent: Accent }) {
  const clamped = Math.max(0, Math.min(100, percent))
  const fill =
    accent === 'err'
      ? 'bg-status-error'
      : accent === 'warn'
        ? 'bg-status-warn'
        : 'bg-status-ok'
  return (
    <div className="h-1 rounded-full bg-kb-elevated overflow-hidden">
      <div className={`h-full rounded-full ${fill}`} style={{ width: `${clamped}%` }} />
    </div>
  )
}

// SeverityChips — replaces the generic "X warn · Y info" text line
// with a denser breakdown that color-codes each severity. Skips
// zero-count buckets so the chip strip stays readable when only one
// severity has hits (e.g. "12 warn" alone instead of "0 critical · 12
// warn · 0 info").
function SeverityChips({
  insights,
}: {
  insights?: { critical?: number; warning?: number; info?: number }
}) {
  const items: Array<{ count: number; label: string; color: string }> = []
  if ((insights?.critical ?? 0) > 0) {
    items.push({ count: insights!.critical!, label: 'critical', color: 'text-status-error' })
  }
  if ((insights?.warning ?? 0) > 0) {
    items.push({ count: insights!.warning!, label: 'warn', color: 'text-status-warn' })
  }
  if ((insights?.info ?? 0) > 0) {
    items.push({ count: insights!.info!, label: 'info', color: 'text-status-info' })
  }
  if (items.length === 0) return null
  return (
    <div className="flex items-center gap-2 text-[11px] font-mono">
      {items.map((it, i) => (
        <span key={it.label} className="flex items-center gap-1">
          <span className={it.color}>{it.count}</span>
          <span className="text-kb-text-tertiary">{it.label}</span>
          {i < items.length - 1 && <span className="text-kb-text-tertiary">·</span>}
        </span>
      ))}
    </div>
  )
}

// summarizeHealth picks the single most actionable line for the
// card's sub-line. Priority order:
//   1. Active critical insights (the "why is status critical when
//      checks all pass?" case the user couldn't reconcile)
//   2. Active warning insights (same shape)
//   3. First failing check (basic component is down)
//   4. First warning check
//   5. "X checks passing" fallback when everything's green
// Returns undefined when there's no health payload at all so the
// card omits the sub-line entirely instead of rendering "—".
function summarizeHealth(health?: ClusterOverview['health']): string | undefined {
  if (!health) return undefined
  const insights = health.insights
  if (insights && insights.critical > 0) {
    return `${insights.critical} critical ${insights.critical === 1 ? 'insight' : 'insights'}`
  }
  if (insights && insights.warning > 0) {
    return `${insights.warning} warning ${insights.warning === 1 ? 'insight' : 'insights'}`
  }
  const checks = health.checks ?? []
  if (checks.length === 0) return undefined
  const fail = checks.find((c) => c.status === 'fail')
  if (fail) return fail.message || `${fail.name} failing`
  const warn = checks.find((c) => c.status === 'warn')
  if (warn) return warn.message || `${warn.name} warning`
  return `${checks.length} ${checks.length === 1 ? 'check' : 'checks'} passing`
}

const CHECK_DOT_COLOR: Record<HealthCheck['status'], string> = {
  pass: '#22d68a',
  warn: '#f5a623',
  fail: '#ef4056',
}

// HealthCard wraps the standard Kpi shell with a hover tooltip that
// expands the score's component breakdown. Each row is one
// HealthCheck — same data model the connector emits — rendered with
// the shared tooltip primitives so the visual matches the metric /
// cluster-map tooltips users already learned.
function HealthCard({
  health,
  accent,
}: {
  health: ClusterOverview['health']
  accent: Accent
}) {
  const checks = health?.checks ?? []
  const insights = health?.insights
  const hasInsightDeduction =
    !!insights && (insights.critical > 0 || insights.warning > 0)
  const tooltipBody =
    checks.length > 0 || hasInsightDeduction ? (
      <>
        <TooltipHeader right={health?.status?.toUpperCase()}>
          {health?.score != null ? `${health.score} / 100` : 'Cluster health'}
        </TooltipHeader>
        <div className="space-y-1">
          {checks.map((c) => (
            <TooltipRow
              key={c.name}
              color={CHECK_DOT_COLOR[c.status]}
              label={c.name}
              value={c.message}
            />
          ))}
          {/* Insights line spells out the score deduction the
              connector applied, so "100 - 5 = 95" is auditable from
              the tooltip rather than implicit. Only renders when
              there's something to report (criticals or warnings),
              keeping the tooltip terse on healthy clusters. */}
          {insights && insights.critical > 0 && (
            <TooltipRow
              color={CHECK_DOT_COLOR.fail}
              label="insights"
              value={`${insights.critical} critical · −${Math.min(insights.critical * 5, 25)} pts`}
            />
          )}
          {insights && insights.warning > 0 && insights.critical === 0 && (
            <TooltipRow
              color={CHECK_DOT_COLOR.warn}
              label="insights"
              value={`${insights.warning} warning · −${Math.min(insights.warning * 2, 10)} pts`}
            />
          )}
        </div>
      </>
    ) : null

  const card = (
    <Kpi
      label="Cluster health"
      accent={accent}
      pill={
        health
          ? { kind: 'status', text: health.status.toUpperCase(), accent }
          : { kind: 'status', text: 'UNKNOWN', accent: 'restricted' }
      }
      value={health?.score != null ? `${health.score}` : '—'}
      valueSuffix={health?.score != null ? '/ 100' : undefined}
      sub={summarizeHealth(health)}
      footer={
        health?.score != null ? (
          <ProgressBar percent={health.score} accent={accent} />
        ) : null
      }
    />
  )

  if (!tooltipBody) return card
  return <HoverTooltip body={tooltipBody}>{card}</HoverTooltip>
}

function PillView({ pill }: { pill: Pill }) {
  if (pill.kind === 'status') {
    const dotColor =
      pill.accent === 'err'
        ? 'bg-status-error'
        : pill.accent === 'warn'
          ? 'bg-status-warn'
          : pill.accent === 'restricted'
            ? 'bg-kb-text-tertiary'
            : 'bg-status-ok'
    const textColor =
      pill.accent === 'err'
        ? 'text-status-error'
        : pill.accent === 'warn'
          ? 'text-status-warn'
          : pill.accent === 'restricted'
            ? 'text-kb-text-tertiary'
            : 'text-status-ok'
    const bgColor =
      pill.accent === 'err'
        ? 'bg-status-error-dim'
        : pill.accent === 'warn'
          ? 'bg-status-warn-dim'
          : pill.accent === 'restricted'
            ? 'bg-kb-elevated'
            : 'bg-status-ok-dim'
    return (
      <span
        className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full ${bgColor} ${textColor} text-[9px] font-mono uppercase tracking-[0.08em] shrink-0`}
      >
        <span className={`w-1.5 h-1.5 rounded-full ${dotColor}`} />
        {pill.text}
      </span>
    )
  }
  // link kind — minimal styling, the arrow makes it scannable as a CTA
  // without the heavy weight of a button.
  return (
    <Link
      to={pill.to}
      className="inline-flex items-center gap-1 text-[10px] font-mono text-kb-text-tertiary hover:text-kb-text-primary transition-colors shrink-0"
    >
      {pill.text}
      <ArrowRight className="w-2.5 h-2.5" />
    </Link>
  )
}
