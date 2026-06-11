import { Link } from 'react-router-dom'
import { ArrowRight, ShieldOff } from 'lucide-react'
import type { ClusterOverview, HealthCheck } from '@/types/kubernetes'
import { HoverTooltip, TooltipHeader, TooltipRow } from '@/components/shared/Tooltip'
import { DonutGauge } from '@/components/shared/DonutGauge'
import { LegendRow } from '@/components/shared/LegendRow'

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
// then the same grammar the CPU/Memory usage cards use — anchor
// element on the left, breakdown LegendRows filling the right column:
//   - Health / Nodes / Pods → ring gauge (score%, ready/total) with
//     the value at the center; beside it the colored headline plus
//     dot-legend rows breaking the number down. The textual rows
//     carry the actionable signal (a 57/58 ring is visually
//     indistinguishable from 58/58 — same limit the old progress bar
//     had).
//   - Insights              → big numeric value + severity legend
//     rows. A count has no natural 0-100 scale, so no ring.
// We considered session-scoped sparklines but on a stable cluster they
// degenerate to a flat horizontal line that conveys nothing.
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
  // The connector buckets pods three ways: Ready (Running all-ready,
  // or Succeeded), NotReady (Failed phase only), and Warning —
  // everything in between (Pending, CrashLoopBackOff, partially
  // ready). Without surfacing Warning the card claims "all running"
  // while a pod is crash-looping, because that pod is neither Ready
  // nor NotReady.
  const podsDegraded = overview.pods?.warning ?? 0

  const healthAccent: Accent =
    health?.status === 'healthy' ? 'ok' : health?.status === 'warning' ? 'warn' : 'err'

  return (
    <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
      {/* Cluster Health — pill IS the headline state. The ring gauge
          renders the score as a proportion of 100 in the accent
          color. The sub-line shows the single most actionable
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
        gaugePercent={
          !restricted('nodes') && nodesTotal > 0 ? (nodesReady / nodesTotal) * 100 : undefined
        }
        rows={
          restricted('nodes')
            ? undefined
            : [
                { color: ACCENT_COLOR.ok, label: 'Ready', value: `${nodesReady}`, to: '/nodes?status=ready' },
                {
                  color: nodesNotReady > 0 ? ACCENT_COLOR.err : MUTED_DOT,
                  label: 'Not ready',
                  value: `${nodesNotReady}`,
                  // Zero-count rows don't link — landing on an empty
                  // filtered list is a dead end, not a shortcut.
                  to: nodesNotReady > 0 ? '/nodes?status=notready' : undefined,
                },
              ]
        }
      />

      <Kpi
        label="Pods"
        accent={
          restricted('pods')
            ? 'restricted'
            : podsNotReady > 0 || podsDegraded > 0
              ? 'warn'
              : 'ok'
        }
        pill={restricted('pods') ? null : { kind: 'link', text: 'view all', to: '/pods' }}
        value={restricted('pods') ? null : `${podsReady}`}
        valueSuffix={restricted('pods') ? undefined : `/ ${podsTotal}`}
        sub={
          restricted('pods')
            ? 'No access'
            : podsNotReady > 0
              ? `${podsNotReady} not running`
              : podsDegraded > 0
                ? `${podsDegraded} degraded`
                : 'all running'
        }
        gaugePercent={
          !restricted('pods') && podsTotal > 0 ? (podsReady / podsTotal) * 100 : undefined
        }
        rows={
          restricted('pods')
            ? undefined
            : [
                { color: ACCENT_COLOR.ok, label: 'Running', value: `${podsReady}`, to: '/pods?status=running' },
                {
                  // Warning bucket: Pending / CrashLoopBackOff /
                  // partially-ready. "degraded" is a backend
                  // pseudo-status (connector GetResources) because
                  // these pods carry heterogeneous status strings
                  // that no single exact match captures.
                  color: podsDegraded > 0 ? ACCENT_COLOR.warn : MUTED_DOT,
                  label: 'Degraded',
                  value: `${podsDegraded}`,
                  to: podsDegraded > 0 ? '/pods?status=degraded' : undefined,
                },
                {
                  color: podsNotReady > 0 ? ACCENT_COLOR.err : MUTED_DOT,
                  label: 'Not running',
                  value: `${podsNotReady}`,
                  // The card's not-running count is the Failed-phase
                  // bucket (see connector buildClusterOverview), so the
                  // deep link filters by that same status.
                  to: podsNotReady > 0 ? '/pods?status=failed' : undefined,
                },
              ]
        }
      />

      <Kpi
        label="Insights"
        accent={
          (insights?.critical ?? 0) > 0 ? 'err' : (insights?.warning ?? 0) > 0 ? 'warn' : 'ok'
        }
        pill={{ kind: 'link', text: 'view', to: '/insights' }}
        value={`${insightsTotal}`}
        valueSuffix="active"
        sub={insightsTotal === 0 ? 'no issues detected' : undefined}
        rows={insightsTotal > 0 ? severityRows(insights) : undefined}
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

// ACCENT_COLOR resolves an accent to the project's status hex
// constants (same values as utils/colors statusColorMap) for SVG
// strokes and legend dots, where Tailwind utility classes can't reach.
const ACCENT_COLOR: Record<Exclude<Accent, 'restricted'>, string> = {
  ok: '#22d68a',
  warn: '#f5a623',
  err: '#ef4056',
}
const INFO_COLOR = '#4c9aff'
// Muted dot for zero-count rows ("Not ready 0") — present so the card
// keeps its two-row rhythm, quiet so it doesn't read as a signal.
const MUTED_DOT = 'var(--kb-text-tertiary)'

interface KpiRow {
  color: string
  label: string
  value: string
  // Deep link into a pre-filtered list view (LegendRow renders the
  // row as a Link). Omit on zero-count rows — see call sites.
  to?: string
}

interface KpiProps {
  label: string
  accent: Accent
  pill: Pill | null
  value: string | null
  valueSuffix?: string
  sub?: string
  // gaugePercent switches the card body to the ring-gauge layout:
  // value + suffix centered inside a DonutGauge stroked in the accent
  // color. Omit (undefined) to keep the big-number layout — Insights
  // uses that, a count has no 0-100 scale to draw a ring against.
  gaugePercent?: number
  // rows render as dot-legend lines in the right column — the same
  // breakdown grammar the CPU/Memory usage cards use, so the whole
  // top half of the dashboard reads as one family.
  rows?: KpiRow[]
}

function Kpi({ label, accent, pill, value, valueSuffix, sub, gaugePercent, rows }: KpiProps) {
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
        <span className="text-sm font-semibold text-kb-text-primary truncate">
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
      ) : gaugePercent != null ? (
        // Same composition as the CPU/Memory usage cards: gauge
        // anchored left, breakdown column filling the rest. 116px
        // matches the Used donuts below, so the whole top half of the
        // dashboard shares one gauge scale instead of two competing
        // ones.
        <div className="flex items-center gap-6 py-1">
          <DonutGauge percent={gaugePercent} color={ACCENT_COLOR[accent]} size={116} strokeWidth={10}>
            <span className="text-3xl font-semibold text-kb-text-primary tabular-nums leading-none">
              {value}
            </span>
            {valueSuffix && (
              <span className="text-[11px] font-mono text-kb-text-tertiary tabular-nums mt-1">
                {valueSuffix}
              </span>
            )}
          </DonutGauge>
          <div className="flex-1 min-w-0">
            {sub && (
              <div className={`text-sm font-mono ${subColor} ${rows?.length ? 'mb-2.5' : ''}`}>
                {sub}
              </div>
            )}
            {rows && rows.length > 0 && (
              <div className="space-y-1.5">
                {rows.map((r) => (
                  <LegendRow key={r.label} color={r.color} label={r.label} value={r.value} labelWidth={88} to={r.to} />
                ))}
              </div>
            )}
          </div>
        </div>
      ) : (
        // Numeric layout (Insights) — value anchored left where the
        // gauge cards put their ring, severity legend filling the
        // right column, so the KPI row reads as one family.
        <div className="flex items-center gap-6 py-1 min-h-[124px]">
          <div className="flex flex-col items-center shrink-0">
            <span className="text-5xl font-semibold text-kb-text-primary tabular-nums leading-none">
              {value}
            </span>
            {valueSuffix && (
              <span className="text-[11px] font-mono text-kb-text-tertiary tabular-nums mt-1.5">
                {valueSuffix}
              </span>
            )}
          </div>
          <div className="flex-1 min-w-0">
            {sub && (
              <div className={`text-sm font-mono ${subColor} ${rows?.length ? 'mb-2.5' : ''}`}>
                {sub}
              </div>
            )}
            {rows && rows.length > 0 && (
              <div className="space-y-1.5">
                {rows.map((r) => (
                  <LegendRow key={r.label} color={r.color} label={r.label} value={r.value} labelWidth={88} to={r.to} />
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

// severityRows — severity breakdown as LegendRows, skipping
// zero-count buckets so the column stays readable when only one
// severity has hits (e.g. "Warning 12" alone instead of three rows
// padded with zeros).
function severityRows(insights?: {
  critical?: number
  warning?: number
  info?: number
}): KpiRow[] {
  const rows: KpiRow[] = []
  if ((insights?.critical ?? 0) > 0) {
    rows.push({ color: ACCENT_COLOR.err, label: 'Critical', value: `${insights!.critical}`, to: '/insights?severity=critical' })
  }
  if ((insights?.warning ?? 0) > 0) {
    rows.push({ color: ACCENT_COLOR.warn, label: 'Warning', value: `${insights!.warning}`, to: '/insights?severity=warning' })
  }
  if ((insights?.info ?? 0) > 0) {
    rows.push({ color: INFO_COLOR, label: 'Info', value: `${insights!.info}`, to: '/insights?severity=info' })
  }
  return rows
}

// healthRows — breakdown column for the Cluster health card: the
// component-check tally first, then the active-insight severities
// (the same buckets the score deduction comes from, so the rows
// visually audit the ring).
function healthRows(health?: ClusterOverview['health']): KpiRow[] {
  if (!health) return []
  const rows: KpiRow[] = []
  const checks = health.checks ?? []
  if (checks.length > 0) {
    const passing = checks.filter((c) => c.status === 'pass').length
    const failing = checks.some((c) => c.status === 'fail')
    const warning = checks.some((c) => c.status === 'warn')
    rows.push({
      color: failing ? ACCENT_COLOR.err : warning ? ACCENT_COLOR.warn : ACCENT_COLOR.ok,
      label: 'Checks',
      value: `${passing}/${checks.length} passing`,
    })
  }
  rows.push(...severityRows(health.insights))
  return rows
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
      gaugePercent={health?.score ?? undefined}
      rows={healthRows(health)}
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
