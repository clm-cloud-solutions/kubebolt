import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import type { ClusterOverview, HealthCheck, ResourceItem } from '@/types/kubernetes'
import { api } from '@/services/api'
import { TooltipHeader, TooltipNote, TooltipRow } from '@/components/shared/Tooltip'
import { KpiCard } from '@/components/shared/kpi/KpiCard'
import { KPI_COLOR, Legend, NodeRings, SemiGauge, Sparkline, StatusList } from '@/components/shared/kpi/MiniCharts'
import { useAgentInstalled } from '@/hooks/useAgentInstalled'
import { useInsights } from '@/hooks/useInsights'
import { useRefreshInterval } from '@/contexts/RefreshContext'
import { columnsFor, useElementWidth } from '@/hooks/useElementWidth'
import { shortenNodeName, withShortNames } from '@/components/resources/NodesSummaryStrip'
import { caseRows, NODE_RULES, openCases, POD_RULES, type CaseRow, type InsightCase } from '@/utils/insightCases'
import { clockOrDay } from '@/utils/localTime'
import { formatAge } from '@/utils/formatters'

// A KPI card needs ~300px for its figure, sentence and minichart.
const KPI_MIN_WIDTH = 300

interface KpiCardsProps {
  overview: ClusterOverview
}

// Pods over the last 24h, from the kubelet/cadvisor stream the agent ships —
// the same inner/outer collapse as the fleet rollup's pod count, so the line
// ends where the fleet number does. Agent-only: without samples the card
// falls back to the state breakdown instead of drawing nothing.
const PODS_TREND = 'count(count by (namespace, pod) (container_cpu_usage_seconds_total))'

// KpiCards — the four headline cards on the cluster Overview, in the site's
// card anatomy (KpiCard). Each answers one question with its own minichart:
//   1. Is this cluster healthy?       → score on a half ring, what took points off
//   2. Are all my nodes participating? → the busiest nodes, CPU and memory rings
//   3. Are my pods running?            → pod count over 24h (or the state list)
//   4. Is anything actionable?         → the open cases, worst and oldest first
//
// The card that lights names who, since when, and opens it in one click. The
// Insights card lights for open insights and lists them as cases; Nodes and
// Pods name their own case. Health gives the verdict (score and chip) and never
// lights: every check it scores is a card here (nodes, pods) or the page itself
// (an unreachable API has no Overview), so its glow only repeated another card.
//
// Kept from the previous cards: restricted resources hold their slot and say
// "No access"; Completed pods are out of the denominator (a batch of finished
// Jobs is not "pods missing"); every non-zero breakdown links to its filtered
// list; the health score's arithmetic is auditable on hover; and the columns
// follow the row's own width (Kobi's docked panel narrows it).
//
// An older note here rejected sparklines because a stable cluster draws a flat
// line. The pod line now spans 24h, not the session, and a flat series is
// labelled "steady at N" — which is the reading, not filler.
export function KpiCards({ overview }: KpiCardsProps) {
  const perms = overview.permissions
  const restricted = (key: string) => perms != null && perms[key] === false
  const { interval } = useRefreshInterval()
  const { installed } = useAgentInstalled()

  const health = overview.health
  const insights = health?.insights
  const insightsTotal = (insights?.critical ?? 0) + (insights?.warning ?? 0) + (insights?.info ?? 0)

  const nodesReady = overview.nodes?.ready ?? 0
  const nodesTotal = overview.nodes?.total ?? 0
  const nodesNotReady = overview.nodes?.notReady ?? 0
  const podsReady = overview.pods?.ready ?? 0
  const podsTotal = overview.pods?.total ?? 0
  const podsNotReady = overview.pods?.notReady ?? 0
  // Warning bucket: Pending / CrashLoopBackOff / partially ready — neither
  // Ready nor NotReady, so it must be shown or the card says "all running"
  // while a pod is crash-looping.
  const podsDegraded = overview.pods?.warning ?? 0
  // Completed Job pods: terminal success, out of the running denominator.
  const podsSucceeded = overview.pods?.succeeded ?? 0
  const podsActive = podsTotal - podsSucceeded
  const podsDenom = podsActive > 0 ? podsActive : podsTotal

  const nodesQ = useQuery({
    queryKey: ['kpi', 'nodes'],
    queryFn: () => api.getResources('nodes'),
    enabled: !restricted('nodes'),
    refetchInterval: interval,
  })
  const podsTrendQ = useQuery({
    queryKey: ['kpi', 'pods-trend'],
    queryFn: () => {
      const end = Math.floor(Date.now() / 1000)
      return api.queryMetricsRange({ query: PODS_TREND, start: end - 86400, end, step: '30m' })
    },
    enabled: installed && !restricted('pods'),
    refetchInterval: 5 * 60_000,
    staleTime: 60_000,
  })
  const insightsQ = useInsights()

  const [gridRef, gridWidth] = useElementWidth<HTMLDivElement>()

  // ── Health ────────────────────────────────────────────────────────────
  const healthColor =
    health?.status === 'healthy' ? KPI_COLOR.ok : health?.status === 'warning' ? KPI_COLOR.warn : KPI_COLOR.err
  // Compact on the card ("checks 4/4", "warnings − 10 pts"); the full sentence
  // is the caption and the hover breakdown.
  const healthLegend = healthRows(health).map((r) => ({
    color: r.color,
    label: `${r.label.toLowerCase()} ${r.value.replace(' passing', '')}`,
  }))

  // ── Nodes: the busiest few, by whichever of CPU / memory is higher ────
  const nodeStats = (nodesQ.data?.items ?? [])
    .map((n: ResourceItem) => ({ name: n.name, cpu: n.cpuPercent as number | undefined, mem: n.memoryPercent as number | undefined }))
    .filter((n): n is { name: string; cpu: number; mem: number } => typeof n.cpu === 'number' && typeof n.mem === 'number')
    .sort((a, b) => Math.max(b.cpu, b.mem) - Math.max(a.cpu, a.mem))
  const shownNodes = nodeStats.slice(0, 3)
  const moreNodes = nodeStats.length - shownNodes.length

  // ── Pods: 24h series ──────────────────────────────────────────────────
  const podSeries = (podsTrendQ.data?.data?.result?.[0]?.values ?? []).map(([, v]) => Math.round(Number(v)))

  // ── Insights as cases: who, which rule, since when ────────────────────
  const cases = openCases(insightsQ.data?.items)
  const nodeCases = cases.filter((c) => NODE_RULES.has(c.rule))
  const podCases = cases.filter((c) => POD_RULES.has(c.rule))
  // A node the API already reports NotReady before (or without) its insight:
  // named, without a time.
  const notReadyNodes = (nodesQ.data?.items ?? []).filter((n: ResourceItem) => n.status === 'NotReady').map((n) => n.name)
  const podTrouble = podsNotReady > 0 || podsDegraded > 0
  const insightsAlert = (insights?.critical ?? 0) > 0 ? 'crit' : (insights?.warning ?? 0) > 0 ? 'warn' : undefined
  const listed = caseRows(cases, 3)
  const hovered = caseRows(cases, 8)

  // The health sub-line opens the insights it summarizes.
  const healthSummary = summarizeHealth(health)
  const healthTo = (insights?.critical ?? 0) > 0 ? '/insights?severity=critical' : (insights?.warning ?? 0) > 0 ? '/insights?severity=warning' : null

  // A breakdown line where every non-zero part links to its filtered list —
  // landing on an empty filtered list is a dead end, so zeros stay plain.
  const parts = (items: { label: string; n: number; to: string; color?: string }[]) =>
    items.map((it, idx) => (
      <span key={it.label}>
        {idx > 0 && ' · '}
        {it.n > 0 ? (
          <Link to={it.to} className="hover:text-kb-text-primary transition-colors" style={it.color ? { color: it.color } : undefined}>
            {it.n} {it.label}
          </Link>
        ) : (
          `0 ${it.label}`
        )}
      </span>
    ))

  return (
    <div
      ref={gridRef}
      className={`grid gap-4 ${gridWidth ? '' : 'grid-cols-1 sm:grid-cols-2 xl:grid-cols-4'}`}
      style={gridWidth ? { gridTemplateColumns: `repeat(${columnsFor(gridWidth, KPI_MIN_WIDTH, 16, [4, 2, 1])}, minmax(0, 1fr))` } : undefined}
    >
      {/* The verdict: score and chip, never lit (see the header). */}
      <KpiCard
        primary
        status={health?.status ? HEALTH_STATUS[health.status] : undefined}
        value={health?.score != null ? health.score : '—'}
        unit={health?.score != null ? '/ 100' : undefined}
        description="cluster health"
        viz={health?.score != null ? <SemiGauge percent={health.score} color={healthColor} legend={<Legend rows={healthLegend} />} /> : undefined}
        caption={
          healthSummary && healthTo ? (
            <Link to={healthTo} className="hover:text-kb-text-primary transition-colors">
              {healthSummary}
            </Link>
          ) : (
            healthSummary
          )
        }
        info={healthTooltip(health)}
      />

      <KpiCard
        restricted={restricted('nodes')}
        alert={nodesNotReady > 0 ? 'crit' : undefined}
        link={{ text: 'view all', to: '/nodes' }}
        value={nodesReady}
        unit={`/ ${nodesTotal}`}
        description={
          nodesNotReady > 0 ? (
            <>
              {nodesNotReady} not ready
              {nodeCases[0] ? (
                <> · <Culprit c={nodeCases[0]} /></>
              ) : notReadyNodes[0] ? (
                <> · <Culprit c={{ name: notReadyNodes[0], to: `/nodes/_/${notReadyNodes[0]}` }} /></>
              ) : null}
            </>
          ) : (
            'nodes ready'
          )
        }
        info={
          nodesNotReady > 0 && (nodeCases.length > 0 || notReadyNodes.length > 0)
            ? nodeCases.length > 0
              ? casesTooltip('Not ready', nodeCases, (c) => c.name)
              : (
                <>
                  <TooltipHeader>Not ready</TooltipHeader>
                  <div className="space-y-1">
                    {notReadyNodes.map((n) => <TooltipRow key={n} color={KPI_COLOR.err} label={n} value="NotReady" />)}
                  </div>
                </>
              )
            : undefined
        }
        viz={
          shownNodes.length > 0 ? (
            <NodeRings nodes={withShortNames(shownNodes)} />
          ) : (
            <StatusList
              rows={[
                { color: KPI_COLOR.ok, label: 'Ready', value: nodesReady, to: '/nodes?status=ready' },
                {
                  color: nodesNotReady > 0 ? KPI_COLOR.err : KPI_COLOR.muted,
                  label: 'Not ready',
                  value: nodesNotReady,
                  to: nodesNotReady > 0 ? '/nodes?status=notready' : undefined,
                },
              ]}
            />
          )
        }
        caption={
          shownNodes.length > 0 ? (
            <>
              inner ring cpu · outer ring memory
              {moreNodes > 0 && (
                <>
                  {' · '}
                  <Link to="/nodes" className="hover:text-kb-text-primary transition-colors">+{moreNodes} more</Link>
                </>
              )}
            </>
          ) : undefined
        }
      />

      <KpiCard
        restricted={restricted('pods')}
        alert={podsNotReady > 0 ? 'crit' : podsDegraded > 0 ? 'warn' : undefined}
        link={{ text: 'view all', to: '/pods' }}
        value={podsReady}
        // "active" when Completed pods are left out of the denominator, so
        // 68 / 69 reconciles with the sidebar's 82 and the "13 completed" below.
        unit={podsSucceeded > 0 && podsActive > 0 ? `/ ${podsDenom} active` : `/ ${podsDenom}`}
        description={
          <>
            {podsNotReady > 0 ? `${podsNotReady} not running` : podsDegraded > 0 ? `${podsDegraded} degraded` : 'pods running'}
            {podTrouble && podCases[0] && (
              <> · <Culprit c={podCases[0]} /></>
            )}
          </>
        }
        info={
          podTrouble && podCases.length > 0
            ? casesTooltip('Pods not running well', podCases, (c) => `${c.name}${c.pods > 1 ? ` (${c.pods} pods)` : ''} · ${c.rule}`)
            : undefined
        }
        viz={
          podSeries.length >= 2 ? (
            <Sparkline values={podSeries} window="24h" />
          ) : (
            <StatusList
              rows={[
                { color: KPI_COLOR.ok, label: 'Running', value: podsReady, to: '/pods?status=running' },
                { color: podsDegraded > 0 ? KPI_COLOR.warn : KPI_COLOR.muted, label: 'Degraded', value: podsDegraded, to: podsDegraded > 0 ? '/pods?status=degraded' : undefined },
              ]}
            />
          )
        }
        caption={parts([
          { label: 'completed', n: podsSucceeded, to: '/pods?status=succeeded' },
          { label: 'degraded', n: podsDegraded, to: '/pods?status=degraded', color: KPI_COLOR.warn },
          { label: 'not running', n: podsNotReady, to: '/pods?status=failed', color: KPI_COLOR.err },
        ])}
      />

      <KpiCard
        alert={insightsAlert}
        link={{ text: 'view', to: '/insights' }}
        value={insightsTotal}
        unit={insightsTotal === 1 ? 'insight' : 'insights'}
        description={insightsTotal === 0 ? 'no issues detected' : 'open right now'}
        viz={
          listed.rows.length > 0 ? <StatusList rows={listed.rows.map(caseListRow)} /> : undefined
        }
        info={
          hovered.rows.length > 0 ? (
            <>
              <TooltipHeader right="since">Open insights</TooltipHeader>
              <div className="space-y-1">
                {hovered.rows.map((r) => {
                  const row = caseListRow(r)
                  return (
                    <TooltipRow key={row.label} color={row.color} label={row.label} value={<span className="whitespace-nowrap">{row.value}</span>} />
                  )
                })}
              </div>
              {hovered.more > 0 && <TooltipNote>+{hovered.more} more in Insights</TooltipNote>}
            </>
          ) : undefined
        }
        caption={parts([
          { label: 'critical', n: insights?.critical ?? 0, to: '/insights?severity=critical', color: KPI_COLOR.err },
          { label: 'warning', n: insights?.warning ?? 0, to: '/insights?severity=warning', color: KPI_COLOR.warn },
          { label: 'info', n: insights?.info ?? 0, to: '/insights?severity=info' },
        ])}
      />
    </div>
  )
}

const SEV_COLOR = { critical: KPI_COLOR.err, warning: KPI_COLOR.warn, info: KPI_COLOR.info } as const

// Culprit — who, as a link to its page, and since when when it is known.
function Culprit({ c }: { c: Pick<InsightCase, 'name' | 'to'> & Partial<Pick<InsightCase, 'since' | 'pods'>> }) {
  const name = c.pods && c.pods > 1 ? `${c.name} (${c.pods} pods)` : c.name
  return (
    <>
      {c.to ? (
        <Link to={c.to} className="underline decoration-dotted underline-offset-2 hover:text-kb-text-primary">
          {name}
        </Link>
      ) : (
        name
      )}
      {c.since && <> since {clockOrDay(c.since)}</>}
    </>
  )
}

// casesTooltip — every case behind a card, with when it started and for how long.
function casesTooltip(title: string, list: InsightCase[], label: (c: InsightCase) => string, max = 6) {
  return (
    <>
      <TooltipHeader right="since">{title}</TooltipHeader>
      <div className="space-y-1">
        {list.slice(0, max).map((c) => (
          <TooltipRow
            key={`${c.rule}|${c.resource}`}
            color={SEV_COLOR[c.severity]}
            label={label(c)}
            value={<span className="whitespace-nowrap">{`${clockOrDay(c.since)} · ${formatAge(c.since)}`}</span>}
          />
        ))}
      </div>
      {list.length > max && <TooltipNote>+{list.length - max} more in Insights</TooltipNote>}
    </>
  )
}

// caseListRow — one row of the Insights card: a case opens its object; a rule
// folded from several cases opens the list at that severity.
function caseListRow(r: CaseRow) {
  if (r.kind === 'case') {
    const c = r.case
    return {
      color: SEV_COLOR[c.severity],
      label: `${c.name} · ${c.rule}`,
      value: clockOrDay(c.since),
      to: c.to ?? `/insights?severity=${c.severity}`,
    }
  }
  return {
    color: SEV_COLOR[r.severity],
    label: `${r.rule} ×${r.count}`,
    value: clockOrDay(r.since),
    to: `/insights?severity=${r.severity}`,
  }
}

const HEALTH_STATUS = {
  healthy: { label: 'Healthy', tone: 'ok' },
  warning: { label: 'Warning', tone: 'warn' },
  critical: { label: 'Critical', tone: 'crit' },
} as const

// ACCENT_COLOR resolves a state to the project's status hex constants (same
// values as utils/colors statusColorMap) for SVG strokes and legend dots.
const ACCENT_COLOR = {
  ok: KPI_COLOR.ok,
  warn: KPI_COLOR.warn,
  err: KPI_COLOR.err,
}

interface KpiRow {
  color: string
  label: string
  value: string
}

// scoreDeductions mirrors the connector's insight penalty (GetHealth):
// −5 per critical capped at −25, −2 per warning capped at −10, and BOTH
// apply. Kept in one place so the card's rows and its tooltip can't drift
// from each other or from the backend.
function scoreDeductions(insights?: {
  critical?: number
  warning?: number
}): { label: string; points: number; color: string }[] {
  const out: { label: string; points: number; color: string }[] = []
  const critical = insights?.critical ?? 0
  const warning = insights?.warning ?? 0
  if (critical > 0) {
    out.push({
      label: critical === 1 ? 'Critical' : 'Criticals',
      points: Math.min(critical * 5, 25),
      color: ACCENT_COLOR.err,
    })
  }
  if (warning > 0) {
    out.push({
      label: warning === 1 ? 'Warning' : 'Warnings',
      points: Math.min(warning * 2, 10),
      color: ACCENT_COLOR.warn,
    })
  }
  return out
}

// healthRows — breakdown column for the Cluster health card. These rows
// AUDIT the ring: the component-check tally, then what each severity took
// off the score, so "94" is explained on the card instead of only inside a
// hover.
//
// They deliberately do NOT restate the severity counts. The Insights card
// sits immediately to the right showing exactly "Warning 3 · Info 15" — two
// of the four hero cards were printing the same two lines, which spent half
// the row on one fact and made the strip read as filler.
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
  for (const d of scoreDeductions(health.insights)) {
    rows.push({ color: d.color, label: d.label, value: `− ${d.points} pts` })
  }
  return rows
}

// summarizeHealth picks the card's sub-line so it MATCHES the overall status.
//   - status healthy → a positive summary ("All/N of M checks passing"); a lone
//     non-critical check stays in the breakdown, not the headline.
//   - status not healthy → the most actionable problem, in priority order:
//       1. active critical insights · 2. active warning insights ·
//       3. first failing check · 4. first warning check · 5. checks-passing.
// Returns undefined when there's no health payload at all so the
// card omits the sub-line entirely instead of rendering "—".
function summarizeHealth(health?: ClusterOverview['health']): string | undefined {
  if (!health) return undefined
  const checks = health.checks ?? []
  // When the overall health is good, the headline MATCHES the score: a single
  // non-critical issue (e.g. metrics server unavailable) belongs in the
  // breakdown below, not as the headline — showing "Metrics server not
  // available" on a 90/100 HEALTHY cluster reads as a contradiction. Only
  // surface the worst problem when the STATUS itself reflects one.
  if (health.status === 'healthy') {
    if (checks.length === 0) return 'All systems operational'
    const passing = checks.filter((c) => c.status === 'pass').length
    return passing === checks.length
      ? `All ${checks.length} checks passing`
      : `${passing} of ${checks.length} checks passing`
  }
  const insights = health.insights
  if (insights && insights.critical > 0) {
    return `${insights.critical} critical ${insights.critical === 1 ? 'insight' : 'insights'}`
  }
  if (insights && insights.warning > 0) {
    return `${insights.warning} warning ${insights.warning === 1 ? 'insight' : 'insights'}`
  }
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

// healthTooltip — the score's component breakdown on hover: one row per
// HealthCheck the connector emits, then what each insight severity took off,
// so "100 − 10 = 90" is auditable rather than implicit. Null when there is
// nothing to break down.
function healthTooltip(health?: ClusterOverview['health']) {
  const checks = health?.checks ?? []
  const insights = health?.insights
  const hasInsightDeduction = !!insights && (insights.critical > 0 || insights.warning > 0)
  if (checks.length === 0 && !hasInsightDeduction) return undefined
  return (
    <>
      <TooltipHeader right={health?.status?.toUpperCase()}>
        {health?.score != null ? `${health.score} / 100` : 'Cluster health'}
      </TooltipHeader>
      <div className="space-y-1">
        {checks.map((c) => (
          <TooltipRow key={c.name} color={CHECK_DOT_COLOR[c.status]} label={c.name} value={c.message} />
        ))}
        {scoreDeductions(insights).map((d) => (
          <TooltipRow key={d.label} color={d.color} label={d.label.toLowerCase()} value={`− ${d.points} pts`} />
        ))}
      </div>
    </>
  )
}
