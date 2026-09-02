import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Activity, ChevronLeft, ChevronRight, FileWarning, ScrollText, ShieldAlert, ShieldCheck, Wrench } from 'lucide-react'
import { api } from '@/services/api'
import { StripCard } from '@/components/dashboard/StripCard'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { HoverTooltip, TooltipHeader, TooltipRow, TooltipNote } from '@/components/shared/Tooltip'
import { FindingDetailModal } from '@/components/security/FindingDetailModal'
import { SecuritySubTabs } from '@/components/security/SecuritySubTabs'
import { WorkloadFindingsModal } from '@/components/security/WorkloadFindingsModal'
import { SeverityDonut } from '@/components/security/SeverityDonut'
import {
  RuntimeEventModal,
  RUNTIME_PRIORITIES,
  falcoSentence,
  priorityPill,
  type RuntimeEvent,
} from '@/components/security/RuntimeEventModal'

// SecurityPage (E2 SEC-C, repainted to design/kubebolt-security-redesign.html).
//
// The organizing idea is the mockup's: normalized findings from four sources,
// with the ACTIONABLE list on top. Two panels of the mockup are deliberately
// NOT reproduced as drawn, because the data behind them does not exist:
//
//   - "CIS score 78%" and the per-section % breakdown. normalizeComplianceReport
//     keeps only failing controls (`if fails <= 0 { continue }`) and discards
//     totalPass, so there is no denominator. A percentage derived from failure
//     counts alone would be invented. We show the count of FAILING CONTROLS,
//     which is real. Emitting a compliance summary from the sweep is the scoped
//     follow-up that would make the score honest.
//   - "▲ 4% this week". Nothing persists historical snapshots.
//
// The severity bar renders only the bands actually ingested, with the coverage
// caveat spelled out: trivySeverities maps CRITICAL and HIGH only, so painting
// four equal bands would tell the operator "your fleet is 77% medium/low" when
// the truth is "medium/low CVEs are not collected".

// Derived from the client so the row shape has ONE definition. Duplicating it
// here is how a field silently stops being rendered after the API adds one.
type FindingRow = Awaited<ReturnType<typeof api.listFindings>>['findings'][number]

const SEV_ORDER = ['critical', 'high', 'medium', 'low'] as const

// Human name per lens, for copy that has to say WHICH lens is empty.
const LENS_LABEL: Record<string, string> = {
  vulnerability: 'Vulnerabilities',
  configuration: 'Configuration',
  rbac: 'Permissions',
  compliance: 'Compliance',
  runtime: 'Runtime',
}

const SEV_TEXT: Record<string, string> = {
  critical: 'text-status-error',
  high: 'text-status-warn',
  medium: 'text-status-info',
  low: 'text-kb-text-tertiary',
}
const SEV_BG: Record<string, string> = {
  critical: 'bg-status-error',
  high: 'bg-status-warn',
  medium: 'bg-status-info',
  low: 'bg-kb-text-tertiary',
}
const SEV_PILL: Record<string, string> = {
  critical: 'bg-status-error-dim text-status-error',
  high: 'bg-status-warn-dim text-status-warn',
  medium: 'bg-status-info/15 text-status-info',
  low: 'bg-kb-elevated text-kb-text-tertiary',
}

function ago(iso?: string): string {
  if (!iso) return '—'
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (secs < 60) return `${Math.floor(secs)}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`
  return `${Math.floor(secs / 86400)}d`
}

// KindMix says WHAT KIND of problem a workload has — the one thing severity
// cannot tell you. Five criticals could be packages to upgrade or a credential
// to rotate, and those are different mornings.
//
// It rides the second line of the workload cell rather than taking a column of
// its own: that line already answers "what is this", so the type lands where the
// eye is going anyway, and the table keeps every row on ONE line — the width we
// spent an afternoon winning back.
//
// Driven by a map, so a kind we have not shipped yet (Kyverno policy, infra
// assessment) appears on its own without a frontend change. Secrets get the
// accent because they are the row's reason for being at the top; everything else
// stays quiet so the emphasis means something.
const KIND_LABEL: Record<string, string> = {
  cve: 'CVE',
  exposed_secret: 'SECRET',
  misconfig: 'CONFIG',
  rbac_issue: 'RBAC',
  policy_violation: 'POLICY',
}

function KindMix({ kinds }: { kinds?: Record<string, number> }) {
  const entries = Object.entries(kinds ?? {})
    .filter(([, n]) => n > 0)
    // Secrets first, then by volume. Same priority as the row order, so the chip
    // strip and the list agree about what matters.
    .sort((a, b) => {
      if ((a[0] === 'exposed_secret') !== (b[0] === 'exposed_secret')) {
        return a[0] === 'exposed_secret' ? -1 : 1
      }
      return b[1] - a[1]
    })
  if (entries.length === 0) return null
  return (
    <>
      {entries.map(([kind, n]) => (
        <span
          key={kind}
          className={
            kind === 'exposed_secret' ? 'text-status-error' : 'text-kb-text-tertiary'
          }
        >
          {' · '}
          <span className="tabular-nums">{n}</span> {KIND_LABEL[kind] ?? kind.toUpperCase()}
        </span>
      ))}
    </>
  )
}

// One source's contribution. Deliberately NOT a card: the strip above already
// owns the card grammar, and a second row of cards read as a competing tier
// rather than as attribution. Thin row, mono label, number, done.
function SourceStat({
  name,
  value,
  accent,
  sub,
  live,
}: {
  name: string
  value: string | number
  accent?: string
  sub: string
  live?: boolean
}) {
  return (
    <div className="flex items-baseline gap-2 min-w-0">
      <span className={`text-base font-semibold tabular-nums ${accent ?? 'text-kb-text-primary'}`}>
        {value}
      </span>
      <span className="text-[10px] font-mono text-kb-text-secondary truncate">
        {name}
        {live && <span className="ml-1 inline-block w-1.5 h-1.5 rounded-full bg-status-ok align-middle" />}
      </span>
      <span className="text-[11px] font-mono text-kb-text-tertiary truncate">{sub}</span>
    </div>
  )
}

// The five lenses. Each is a different QUESTION with a different fix, so they
// are never counted together — see SecuritySubTabs for why one ranked list was
// the wrong shape.
export type SecurityGroup =
  | 'vulnerability'
  | 'configuration'
  | 'rbac'
  | 'compliance'
  | 'runtime'

export function SecurityPage({ group = 'vulnerability' }: { group?: SecurityGroup }) {
  // The clicked row, opening the drill-down. Kept as the whole record rather
  // than an id so the panel renders its header immediately and only the package
  // list waits on the cluster.
  const [selected, setSelected] = useState<FindingRow | null>(null)
  const [selectedWorkload, setSelectedWorkload] = useState<
    { kind?: string; namespace?: string; name: string; clusterId?: string } | null
  >(null)
  // Facets narrow the rows only — the summary behind every number on this page
  // is scope-wide, so the chips never erase themselves.
  const [severity, setSeverity] = useState('')
  const [kind, setKind] = useState('')
  const [cluster, setCluster] = useState('')
  // Page resets whenever the SCOPE changes — staying on page 7 after switching
  // tab or severity lands the operator on an empty list that looks like "no
  // findings" rather than "you are past the end".
  const [page, setPage] = useState(1)
  useEffect(() => setPage(1), [group, severity, kind, cluster])

  const { data: clusters = [] } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })

  const { data, isLoading, dataUpdatedAt, isFetching } = useQuery({
    queryKey: ['findings', group, severity, kind, cluster, page],
    queryFn: () =>
      api.listFindings({
        // Runtime has no findings at all (Falco writes to the event store), so
        // it scopes to compliance and simply never renders the table.
        group: group === 'runtime' ? 'compliance' : group,
        severity: severity || undefined,
        kind: kind || undefined,
        cluster: cluster || undefined,
        page,
      }),
    enabled: group !== 'runtime',
    refetchInterval: 60_000,
  })

  // The list is WORKLOADS now: 449 findings are 18 workloads, and a workload is
  // what actually gets fixed. The finding-level query stays for the KPI strip,
  // which still describes findings.
  const { data: wl } = useQuery({
    queryKey: ['finding-workloads', group, severity, kind, cluster, page],
    queryFn: () =>
      api.listFindingWorkloads({
        group: group === 'runtime' ? 'compliance' : group,
        severity: severity || undefined,
        kind: kind || undefined,
        cluster: cluster || undefined,
        page,
      }),
    refetchInterval: 60_000,
    enabled: group !== 'runtime',
  })

  // Attribution is a PAGE question, not a lens one: "which of my scanners are
  // contributing" does not change because the reader switched tab. It is drawn
  // ABOVE the sub-tabs, which promises exactly that — and it was computed from
  // the group-scoped summary, so it said "Kyverno 0" on every tab but
  // Configuration while Kyverno was installed and reporting 31 findings. A zero
  // that means "not in this lens" is indistinguishable from "not installed",
  // which is the one distinction this row exists to make.
  //
  // pageSize 1 because only the summary is wanted; the rows are already on
  // screen from the scoped query.
  const { data: fleetWide } = useQuery({
    queryKey: ['findings-by-source', cluster],
    queryFn: () => api.listFindings({ cluster: cluster || undefined, pageSize: 1 }),
    refetchInterval: 60_000,
  })

  const { data: rt } = useQuery({
    queryKey: ['runtime-events', cluster],
    queryFn: () => api.listRuntimeEvents({ cluster: cluster || undefined, since: '24h' }),
    refetchInterval: 30_000,
  })

  const findings = data?.findings ?? []
  const bySeverity = data?.bySeverity ?? {}
  const byKind = data?.byKind ?? {}
  const scopeTotal = data?.scopeTotal ?? 0
  const fixable = data?.activeWithRemediation ?? 0
  const rollups = data?.rollups ?? 0
  const affected = data?.affectedResources ?? 0
  const topResource = data?.topResource ?? ''
  const topCount = data?.topResourceCount ?? 0
  // Only the vulnerability lens carries images — see the table header.
  const showImage = group === 'vulnerability'
  // What the rows ARE. The aggregation is the same in every lens — collapse
  // findings onto the thing that gets edited — but under Permissions that thing
  // is a Role, and calling it a workload would misname every row on the page.
  const unit = group === 'rbac' ? 'role' : 'workload'
  const Unit = group === 'rbac' ? 'Role' : 'Workload'
  // A workload row carries the kube-system UID; the operator knows the cluster
  // by its name. The selector's list is already in hand, so the lookup is free.
  const clusterName = (uid?: string) => {
    if (!uid) return ''
    const c = clusters.find((x) => x.clusterId === uid)
    return c?.displayName || c?.name?.replace(/^agent:/, '').slice(0, 8) || uid.slice(0, 8)
  }
  const showCluster = !cluster && clusters.length > 1
  const workloads = wl?.workloads ?? []
  const topImages = wl?.topImages ?? []
  const topChecks = wl?.topChecks ?? []
  const benchmarks = wl?.benchmarks ?? []
  const workloadTotal = wl?.total ?? 0
  // The pager must describe the LIST, and the list is whatever the active tab
  // renders — controls under compliance, workloads everywhere else. Both totals
  // are taken from the SAME response that produced those rows, which is the
  // invariant that was broken:
  //
  // compliance read `scopeTotal`, and scopeTotal is deliberately
  // facet-independent — it is the denominator the KPI strip describes, so the
  // chips never erase their own numbers. Using it here meant filtering to
  // "Critical 3" still announced "1–25 of 55 controls" and offered a second page
  // that could not exist. `total` is the facet-filtered count; it is the one the
  // pager wants.
  //
  // pageSize likewise comes from its own response instead of always from the
  // workload one. The two constants happen to be 25 today, so the bug was
  // invisible — and would have appeared as off-by-a-page the day either moved.
  const listTotal = group === 'compliance' ? data?.total ?? 0 : workloadTotal
  const pageSize = group === 'compliance' ? data?.pageSize ?? 25 : wl?.pageSize ?? 25
  const pages = Math.max(1, Math.ceil(listTotal / pageSize))
  const events = rt?.events ?? []

  // Scoped counts drive the CHIPS (they filter the current lens); the page-wide
  // ones drive the attribution row above the tabs.
  const policyViolations = byKind.policy_violation ?? 0
  // Scoped on purpose: the CIS panel below lists the CONTROLS of the compliance
  // lens, so its count must match the rows beside it.
  const cisFailing = byKind.misconfig ?? 0
  const allKinds = fleetWide?.byKind ?? {}
  const cvesAll = allKinds.cve ?? 0
  const cisFailingAll = allKinds.misconfig ?? 0
  const policyAll = allKinds.policy_violation ?? 0
  // Credentials baked into an image. Kept apart from the CVE count on purpose:
  // one of these outranks a page of highs, and averaged into 449 it disappears.
  const exposedSecrets = byKind.exposed_secret ?? 0
  const newest = events[0]

  // How many of the four normalized sources are actually contributing. A
  // fleet with zero findings and a fleet with zero SCANNERS look identical
  // in the numbers, and only one of them is good news.
  const sourcesReporting = [cvesAll, cisFailingAll, policyAll, events.length].filter(
    (n) => n > 0,
  ).length

  // Only bands with findings are drawn — see the file header.
  const bands = SEV_ORDER.map((s) => ({ sev: s, n: bySeverity[s] ?? 0 })).filter((b) => b.n > 0)
  const bandTotal = bands.reduce((a, b) => a + b.n, 0)

  // Filter chips. The first cut drew them as 10px outlines in tertiary text on
  // the card background — they read as captions, not controls, and on a light
  // theme they nearly vanished. Three changes, each fixing a specific failure:
  //
  //   - a FILLED resting state (kb-elevated), so the eye registers a thing it
  //     can press rather than a label sitting on the card;
  //   - 11px and secondary text, because 10px tertiary on white is below the
  //     contrast where a number like "455" stays legible at a glance;
  //   - a SOLID accent when active. The old 12%-tint-plus-30%-border said
  //     "slightly emphasised"; a filter that is ON is a mode the page is in, and
  //     that deserves to be unmistakable.
  //
  // The shape itself lives in chipButton so the Runtime lens presses the SAME
  // control and not a lookalike that drifts from this one.
  const chip = chipButton

  return (
    <div className="space-y-5">
      {selected && (
        <FindingDetailModal finding={selected} onClose={() => setSelected(null)} />
      )}
      {selectedWorkload && group !== 'runtime' && (
        <WorkloadFindingsModal
          workload={selectedWorkload}
          // The guard above already narrowed runtime out.
          group={group}
          // The ROW's cluster, not the selector's: viewing "all clusters" the
          // selector is empty, and the drill-down would then pull this
          // workload's namesakes from every other cluster too.
          cluster={selectedWorkload.clusterId || cluster}
          onClose={() => setSelectedWorkload(null)}
        />
      )}
      {/* Header in the app's own grammar (same shape as Fleet / Capacity /
          Reliability): title row on the left, controls right, and a LIVE
          summary line underneath. The old subtitle described the sources in
          prose — true, but identical on every render, so it told the operator
          nothing about the state they are looking at. */}
      <div>
        <div className="flex items-start justify-between gap-4">
          <h1 className="text-lg font-semibold text-kb-text-primary flex items-center gap-2">
            <ShieldAlert className="w-4 h-4 text-kb-accent" />
            Security &amp; Compliance
          </h1>
          <div className="flex items-center gap-2">
            {/* This page is FLEET-WIDE: it reads every cluster the caller may
                see, regardless of which one the Topbar has active. That is a
                genuine trap — the Topbar naming one cluster while the page
                answers for several reads as if they agreed.

                With several clusters the selector states the scope by existing.
                With ONE it used to render nothing at all, so a user whose team
                owns a single cluster had no evidence of whose data they were
                looking at — and after the team-scoping fix that is exactly the
                user most likely to wonder. A static chip is not decoration
                there: it is the only thing on screen naming the scope. */}
            {clusters.length > 1 ? (
              <select
                value={cluster}
                onChange={(e) => setCluster(e.target.value)}
                className="bg-kb-elevated border border-kb-border rounded-lg px-2.5 py-1.5 text-[11px] font-mono text-kb-text-secondary"
              >
                <option value="">All clusters</option>
                {clusters.map((c) => (
                  <option key={c.context} value={c.clusterId || c.context}>
                    {c.displayName || c.name}
                  </option>
                ))}
              </select>
            ) : clusters.length === 1 ? (
              <span className="bg-kb-elevated border border-kb-border rounded-lg px-2.5 py-1.5 text-[11px] font-mono text-kb-text-secondary">
                {clusters[0].displayName || clusters[0].name}
              </span>
            ) : null}
            <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
          </div>
        </div>
        <p className="text-xs text-kb-text-tertiary mt-0.5">
          {/* Names the scope in words. "455 findings" says nothing about WHOSE,
              and this page does not follow the Topbar's active cluster. */}
          {scopeTotal} finding{scopeTotal === 1 ? '' : 's'} across{' '}
          {cluster
            ? clusterName(cluster) || 'the selected cluster'
            : clusters.length === 1
              ? clusters[0].displayName || clusters[0].name
              : `${clusters.length} clusters`}{' '}
          · {sourcesReporting} of 4 sources reporting
          {(data?.newLast24h ?? 0) > 0 ? ` · ${data?.newLast24h} new in 24h` : ' · none new in 24h'}
        </p>
      </div>

      {/* StripCard, not a private KPI component. It is the app's documented
          card grammar for summary strips, already used by Capacity, Reliability
          and Fleet — a second implementation is exactly what makes a page read
          as "not one of ours". Its accents replace the hand-passed tone classes.

          Tooltips matter more here than on other strips: "23 critical" means
          different things depending on WHICH scanner produced it, and the page
          normalizes four of them. */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <StripCard
          label="Critical"
          value={bySeverity.critical ?? 0}
          valueAccent={(bySeverity.critical ?? 0) > 0 ? 'crit' : 'default'}
          sub={
            (data?.newLast24h ?? 0) > 0 ? `▲ ${data?.newLast24h} new in 24h` : 'none new in 24h'
          }
          subAccent={(data?.newLast24h ?? 0) > 0 ? 'crit' : 'default'}
          info={
            <>
              <TooltipHeader>Critical findings</TooltipHeader>
              <TooltipRow label="Sources" value="Trivy · Kyverno · CIS" />
              <TooltipNote>
                Severity as each scanner reports it, normalized to one scale. Runtime threats are
                counted separately — they are events, not standing findings.
              </TooltipNote>
            </>
          }
        />
        {/* Blast radius, not another severity tally. "394 high" says nothing
            about how bad the situation is; "18 workloads affected, and one of
            them carries 47 of the findings" says both how wide the problem is
            and where to start. */}
        <StripCard
          label={`${Unit}s affected`}
          value={affected}
          valueAccent={affected > 0 ? 'warn' : 'default'}
          sub={
            topResource
              ? `worst: ${topResource.split('/').pop()} (${topCount})`
              : `${bySeverity.high ?? 0} high · ${bySeverity.critical ?? 0} critical`
          }
          info={
            <>
              <TooltipHeader>Distinct {unit}s carrying a finding</TooltipHeader>
              <TooltipNote>
                A count of findings answers "how many", never "how bad". Ten findings across ten
                workloads is a different morning than ten on one image everybody runs.
              </TooltipNote>
            </>
          }
        />
        <StripCard
          label="Fixable"
          value={fixable}
          valueAccent={fixable > 0 ? 'info' : 'default'}
          sub={
            scopeTotal > 0
              ? `${Math.round((fixable / scopeTotal) * 100)}% of ${scopeTotal} have a published fix`
              : 'nothing to fix'
          }
          info={
            <>
              <TooltipHeader>Findings with a known remedy</TooltipHeader>
              <TooltipNote>
                The scanner shipped a concrete remediation with these — a version to bump, a field
                to set. The rest need a judgement call, so this is where to start.
              </TooltipNote>
            </>
          }
        />
        <StripCard
          label="Runtime threats"
          value={events.length}
          valueAccent={events.length > 0 ? 'crit' : 'default'}
          sub={newest ? `latest ${ago(newest.at)} ago` : 'none in 24h'}
          subAccent={events.length > 0 ? 'crit' : 'default'}
          info={
            <>
              <TooltipHeader>Runtime threats</TooltipHeader>
              <TooltipRow label="Source" value="Falco" />
              <TooltipNote>
                Events from the last 24h, not standing findings — this number falls on its own as
                events age out.
              </TooltipNote>
            </>
          }
        />
      </div>

      {/* Attribution, not a second KPI tier. Fed by byKind, because bySource can
          never exceed two keys: CIS ships under source="trivy", and Falco writes
          to the event store rather than emitting findings at all.

          These used to be four cards repeating three numbers the strip above
          already showed — nine cards for seven figures, and no way to tell
          whether "CIS controls 3" and "CIS · compliance 3" were the same metric.
          The strip above now carries severity and actionability; this carries
          where it came from. */}
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-2.5 bg-kb-elevated border border-kb-border rounded-lg">
        <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
          By source
        </span>
        <SourceStat
          name="Trivy"
          value={cvesAll}
          accent={cvesAll > 0 ? 'text-status-error' : undefined}
          sub="CVEs"
        />
        <SourceStat
          name="CIS"
          value={cisFailingAll}
          accent={cisFailingAll > 0 ? 'text-status-warn' : undefined}
          sub="controls failing"
        />
        <SourceStat
          name="Kyverno"
          value={policyAll}
          accent={policyAll > 0 ? 'text-status-warn' : undefined}
          sub="violations"
        />
        <SourceStat
          name="Falco"
          value={events.length}
          accent={events.length > 0 ? 'text-status-error' : undefined}
          sub="threats (24h)"
          live={events.length > 0}
        />
      </div>

      <SecuritySubTabs />

      {group === 'runtime' ? (
        <RuntimeOnly cluster={cluster} />
      ) : (
      // Full width. The side column held one hint and a panel that now lives in
      // its own tab, so two thirds of the screen carried a paragraph while the
      // list it was explaining scrolled in a third of the width.
      <div className="grid grid-cols-1 gap-4">
        {/* Findings that need action — the core */}
        <div className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
          <div className="px-4 py-3 border-b border-kb-border flex items-center justify-between gap-3 flex-wrap">
            <h2 className="text-sm font-semibold text-kb-text-primary">Findings that need action</h2>
            <div className="flex gap-1.5 flex-wrap">
              {/* EVERY chip carries its count, and NO chip appears at zero.
                  Two rules, one idea: a filter should say how much it will show
                  before you press it, and pressing one must never land on an
                  empty table.

                  Half of them had no number, which made the two that did look
                  like a different kind of control. And the counts were already in
                  hand — the summary behind the donut — so the omission bought
                  nothing.

                  The zero rule also retires a chip that could never work: Policy
                  filters policy_violation, and the Vulnerabilities lens is CVEs
                  and secrets by definition, so it had no possible match in this
                  tab regardless of what Kyverno reports. It now shows up only
                  where it can act. */}
              {(bySeverity.critical ?? 0) > 0 &&
                chip(`Critical ${bySeverity.critical}`, severity === 'critical', () => {
                  setSeverity(severity === 'critical' ? '' : 'critical')
                  setKind('')
                })}
              {(bySeverity.high ?? 0) > 0 &&
                chip(`High ${bySeverity.high}`, severity === 'high', () => {
                  setSeverity(severity === 'high' ? '' : 'high')
                  setKind('')
                })}
              {(bySeverity.medium ?? 0) > 0 &&
                chip(`Medium ${bySeverity.medium}`, severity === 'medium', () => {
                  setSeverity(severity === 'medium' ? '' : 'medium')
                  setKind('')
                })}
              {policyViolations > 0 &&
                chip(`Policy ${policyViolations}`, kind === 'policy_violation', () => {
                  setKind(kind === 'policy_violation' ? '' : 'policy_violation')
                  setSeverity('')
                })}
              {exposedSecrets > 0 &&
                chip(`Secrets ${exposedSecrets}`, kind === 'exposed_secret', () => {
                  setKind(kind === 'exposed_secret' ? '' : 'exposed_secret')
                  setSeverity('')
                })}
              {chip(`All ${scopeTotal}`, !severity && !kind, () => {
                setSeverity('')
                setKind('')
              })}
            </div>
          </div>

          {bandTotal > 0 && (
            <div className="px-4 pt-3">
              {/* A ring rather than a bar-plus-legend. A flat bar shows the split
                  but the reader still assembles the proportion from a row of
                  numbers underneath; the donut hands it over, with the total in
                  the middle because that is the figure people read first and
                  then decompose. */}
              <div className="flex flex-col lg:flex-row lg:items-start gap-6">
                <SeverityDonut counts={bySeverity} />
                {/* Where the leverage is. One bad image explains several
                    workloads, so rebuilding it clears all of them at once —
                    something a per-workload list structurally cannot show.
                    Hidden when the lens has no image data (a config-audit check
                    is about the manifest, not the artifact) rather than rendered
                    as an empty panel. */}
                {benchmarks.length > 0 && (
                  <div className="min-w-0 flex-1 max-w-2xl">
                    {/* Compliance broken down by STANDARD. Fifty-five failing
                        controls in one pile treats CIS, the NSA guide and the
                        two Pod Security profiles as interchangeable — they are
                        not, and an operator is usually held to one of them
                        specifically.
                        
                        Not a "top N": there are four, and hiding any would
                        answer the question wrong. */}
                    <div className="rounded-lg border border-kb-border overflow-hidden">
                      <table className="w-full table-fixed text-left">
                        <thead className="bg-kb-elevated">
                          <tr className="text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary">
                            <th className="px-3 py-1.5 font-medium">Failing controls by benchmark</th>
                            <th className="px-1.5 py-1.5 font-medium text-right w-9">
                              <span className="inline-flex w-4 h-4 items-center justify-center rounded bg-status-error-dim text-status-error">
                                C
                              </span>
                            </th>
                            <th className="px-1.5 py-1.5 font-medium text-right w-9">
                              <span className="inline-flex w-4 h-4 items-center justify-center rounded bg-status-warn-dim text-status-warn">
                                H
                              </span>
                            </th>
                            <th className="px-1.5 py-1.5 font-medium text-right w-9">
                              <span className="inline-flex w-4 h-4 items-center justify-center rounded bg-status-info/15 text-status-info">
                                M
                              </span>
                            </th>
                            <th className="px-3 py-1.5 font-medium text-right w-16">Failing</th>
                          </tr>
                        </thead>
                        <tbody>
                          {benchmarks.map((b) => (
                            <tr key={b.name} className="border-t border-kb-border">
                              <td className="px-3 py-1.5">
                                <HoverTooltip
                                  maxWidth={340}
                                  body={
                                    <>
                                      <TooltipHeader>{b.name}</TooltipHeader>
                                      <TooltipRow label="Failing controls" value={String(b.failing)} />
                                      {b.rollups > 0 && (
                                        <TooltipNote>
                                          {b.rollups} of these are aggregates of workload checks also
                                          listed under Configuration, so they are shown but not
                                          counted in the totals above.
                                        </TooltipNote>
                                      )}
                                    </>
                                  }
                                >
                                  <div className="flex items-baseline gap-2 min-w-0">
                                    <span className="text-[11px] text-kb-text-secondary truncate">
                                      {b.name}
                                    </span>
                                    {b.rollups > 0 && (
                                      <span className="shrink-0 text-[10px] font-mono text-kb-text-tertiary">
                                        {b.rollups} rollup
                                      </span>
                                    )}
                                  </div>
                                </HoverTooltip>
                              </td>
                              <td className="px-1.5 py-1.5 text-right text-[11px] font-mono tabular-nums text-status-error">
                                {b.critical || '\u2014'}
                              </td>
                              <td className="px-1.5 py-1.5 text-right text-[11px] font-mono tabular-nums text-status-warn">
                                {b.high || '\u2014'}
                              </td>
                              <td className="px-1.5 py-1.5 text-right text-[11px] font-mono tabular-nums text-status-info">
                                {b.medium || '\u2014'}
                              </td>
                              <td className="px-3 py-1.5 text-right text-[11px] font-mono tabular-nums text-kb-text-primary">
                                {b.failing}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                    <p className="mt-2 text-[11px] text-kb-text-tertiary">
                      No percentage: Trivy reports only the controls that FAIL, so there is no
                      denominator to score against.
                    </p>
                  </div>
                )}
                {topChecks.length > 0 && (
                  <div className="min-w-0 flex-1 max-w-2xl">
                    {/* The configuration counterpart of the images table. There
                        is no artifact to rebuild here, so the leverage is the
                        CHECK: "Runs as root user" failing on 44 workloads is one
                        manifest pattern to change, not 44 separate problems.
                        
                        Ranked by WORKLOADS rather than findings, which is the
                        opposite of images — for a check, the number of places it
                        must be edited IS the work; for an image, the pile of
                        CVEs is what a single rebuild clears. */}
                    <div className="rounded-lg border border-kb-border overflow-hidden">
                      <table className="w-full table-fixed text-left">
                        <thead className="bg-kb-elevated">
                          <tr className="text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary">
                            <th className="px-3 py-1.5 font-medium">Top 5 checks by reach</th>
                            <th className="px-1.5 py-1.5 font-medium text-right w-9">
                              <span className="inline-flex w-4 h-4 items-center justify-center rounded bg-status-warn-dim text-status-warn">
                                H
                              </span>
                            </th>
                            <th className="px-1.5 py-1.5 font-medium text-right w-9">
                              <span className="inline-flex w-4 h-4 items-center justify-center rounded bg-status-info/15 text-status-info">
                                M
                              </span>
                            </th>
                            <th className="px-3 py-1.5 font-medium text-right w-20">Workloads</th>
                          </tr>
                        </thead>
                        <tbody>
                          {topChecks.map((chk) => {
                            const at = chk.title.indexOf(':')
                            const id = at > 0 ? chk.title.slice(0, at) : ''
                            const name = at > 0 ? chk.title.slice(at + 1).trim() : chk.title
                            return (
                              <tr key={chk.title} className="border-t border-kb-border">
                                <td className="px-3 py-1.5">
                                  <HoverTooltip
                                    maxWidth={360}
                                    body={
                                      <>
                                        <TooltipHeader>{name}</TooltipHeader>
                                        {id && <TooltipRow label="Check" value={id} />}
                                        {(chk.clusters ?? []).length > 0 && (
                                          <TooltipRow
                                            label={(chk.clusters ?? []).length === 1 ? 'Cluster' : 'Clusters'}
                                            value={(chk.clusters ?? []).map(clusterName).join(', ')}
                                          />
                                        )}
                                        <TooltipRow
                                          label="Fails on"
                                          value={`${chk.workloads} workload${chk.workloads === 1 ? '' : 's'}`}
                                        />
                                        {(chk.workloadNames ?? []).length > 0 && (
                                          <TooltipNote>
                                            <div className="pl-3 space-y-0.5">
                                              {(chk.workloadNames ?? []).map((n) => (
                                                <div key={n} className="font-mono truncate">
                                                  {n}
                                                </div>
                                              ))}
                                              {chk.workloads > (chk.workloadNames ?? []).length && (
                                                <div className="text-kb-text-tertiary">
                                                  +{chk.workloads - (chk.workloadNames ?? []).length} more
                                                </div>
                                              )}
                                            </div>
                                          </TooltipNote>
                                        )}
                                      </>
                                    }
                                  >
                                    <div className="flex items-baseline gap-2 min-w-0">
                                      <span className="text-[11px] text-kb-text-secondary truncate">
                                        {name}
                                      </span>
                                      {id && (
                                        <span className="shrink-0 text-[10px] font-mono text-kb-text-tertiary">
                                          {id}
                                        </span>
                                      )}
                                    </div>
                                  </HoverTooltip>
                                </td>
                                <td className="px-1.5 py-1.5 text-right text-[11px] font-mono tabular-nums text-status-warn">
                                  {chk.high || '\u2014'}
                                </td>
                                <td className="px-1.5 py-1.5 text-right text-[11px] font-mono tabular-nums text-status-info">
                                  {chk.medium || '\u2014'}
                                </td>
                                <td className="px-3 py-1.5 text-right text-[11px] font-mono tabular-nums text-kb-text-primary">
                                  {chk.workloads}
                                </td>
                              </tr>
                            )
                          })}
                        </tbody>
                      </table>
                    </div>
                    <p className="mt-2 text-[11px] text-kb-text-tertiary">
                      One manifest pattern each — fixing it clears every workload listed.
                    </p>
                  </div>
                )}
                {topImages.length > 0 && (
                  <div className="min-w-0 flex-1 max-w-2xl">
                    {/* Severity headers as small COLOURED LETTERS: instant to
                        read, and the width they save keeps every row on ONE
                        line. Headers are right-aligned to match their figures —
                        a left-aligned header over right-aligned numbers reads as
                        two different columns.
                        
                        WHERE the image runs lives in a tooltip rather than more
                        columns: workload names and clusters would double the
                        width for context that is only wanted on the one row
                        being considered.
                        
                        No FIXES column. It came from the reference dashboard and
                        was removed — Trivy publishes a remedy for every CVE it
                        reports (435 of 435 here), so it would reprint the total
                        on every row. */}
                    <div className="rounded-lg border border-kb-border overflow-hidden">
                      {/* table-fixed, not the browser default: the numeric columns
                          declare their widths and the first one takes whatever is
                          left. Under auto layout a cell grows to fit its content no
                          matter what `truncate` says on the element inside it, so a
                          long value pushed C/H/Total past the rounded border and
                          overflow-hidden clipped Total off the panel entirely.
                          Observed with a 40-character commit-SHA image tag. The
                          three sibling tables on this page have the same shape and
                          only escaped it by happening to hold short rows, so they
                          carry the same class. */}
                      <table className="w-full table-fixed text-left">
                        <thead className="bg-kb-elevated">
                          <tr className="text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary">
                            <th className="px-3 py-1.5 font-medium">
                              Top 5 images by findings
                            </th>
                            <th className="px-1.5 py-1.5 font-medium text-right w-9">
                              <span className="inline-flex w-4 h-4 items-center justify-center rounded bg-status-error-dim text-status-error">
                                C
                              </span>
                            </th>
                            <th className="px-1.5 py-1.5 font-medium text-right w-9">
                              <span className="inline-flex w-4 h-4 items-center justify-center rounded bg-status-warn-dim text-status-warn">
                                H
                              </span>
                            </th>
                            <th className="px-3 py-1.5 font-medium text-right w-14">Total</th>
                          </tr>
                        </thead>
                        <tbody>
                          {topImages.map((img) => {
                            const at = img.image.lastIndexOf(':')
                            const repo = at > 0 ? img.image.slice(0, at) : img.image
                            const tag = at > 0 ? img.image.slice(at + 1) : ''
                            // A tag that is a bare commit SHA says nothing at a
                            // glance, and being both unshrinkable and the widest
                            // thing in the row it was taking the space the
                            // repository needed — the one part the reader is
                            // actually scanning. Cut to the customary short form;
                            // the full value stays one hover away in the tooltip.
                            //
                            // 32 and not 13, which would have been enough for the
                            // case at hand: a date-stamped tag like 20260811123045
                            // is also valid hex, and shortening one would hide the
                            // seconds that tell two builds apart. At 32 only a full
                            // SHA-1 (40) or SHA-256 (64) qualifies.
                            const shownTag = /^[0-9a-f]{32,}$/i.test(tag) ? `${tag.slice(0, 12)}…` : tag
                            return (
                              <tr key={img.image} className="border-t border-kb-border">
                                <td className="px-3 py-1.5">
                                  <HoverTooltip
                                    maxWidth={360}
                                    body={
                                      <>
                                        <TooltipHeader>{repo}</TooltipHeader>
                                        {tag && <TooltipRow label="Tag" value={tag} />}
                                        {(img.clusters ?? []).length > 0 && (
                                          <TooltipRow
                                            label={
                                              (img.clusters ?? []).length === 1 ? 'Cluster' : 'Clusters'
                                            }
                                            value={(img.clusters ?? []).map(clusterName).join(', ')}
                                          />
                                        )}
                                        <TooltipRow
                                          label="Runs in"
                                          value={`${img.workloads} workload${img.workloads === 1 ? '' : 's'}`}
                                        />
                                        {(img.workloadNames ?? []).length > 0 && (
                                          <TooltipNote>
                                            {/* Indented and one per line: these
                                                are the DETAIL of the "Runs in"
                                                row above, and run together as
                                                prose they read as another
                                                label/value pair rather than as
                                                its expansion. */}
                                            <div className="pl-3 space-y-0.5">
                                              {(img.workloadNames ?? []).map((n) => (
                                                <div key={n} className="font-mono truncate">
                                                  {n}
                                                </div>
                                              ))}
                                              {img.workloads > (img.workloadNames ?? []).length && (
                                                <div className="text-kb-text-tertiary">
                                                  +{img.workloads - (img.workloadNames ?? []).length} more
                                                </div>
                                              )}
                                            </div>
                                          </TooltipNote>
                                        )}
                                      </>
                                    }
                                  >
                                    <div className="flex items-baseline gap-2 min-w-0">
                                      <code className="text-[11px] font-mono text-kb-text-secondary truncate">
                                        {repo}
                                      </code>
                                      <span className="shrink-0 text-[10px] font-mono text-kb-text-tertiary">
                                        {shownTag && `${shownTag} · `}
                                        {img.workloads} wl
                                      </span>
                                    </div>
                                  </HoverTooltip>
                                </td>
                                <td className="px-1.5 py-1.5 text-right text-[11px] font-mono tabular-nums text-status-error">
                                  {img.critical || '\u2014'}
                                </td>
                                <td className="px-1.5 py-1.5 text-right text-[11px] font-mono tabular-nums text-status-warn">
                                  {img.high || '\u2014'}
                                </td>
                                <td className="px-3 py-1.5 text-right text-[11px] font-mono tabular-nums text-kb-text-primary">
                                  {img.findings}
                                </td>
                              </tr>
                            )
                          })}
                        </tbody>
                      </table>
                    </div>
                    <p className="mt-2 text-[11px] text-kb-text-tertiary">
                      Rebuilding one of these closes every row that runs it.
                    </p>
                  </div>
                )}
              </div>
              <p className="text-[11px] text-kb-text-tertiary mt-1.5">
                Only CRITICAL and HIGH CVEs are ingested — medium and low bands come from policy and
                compliance checks, not from image scanning. Exposed secrets are the exception and are
                ingested at every severity: there are few, and a low-rated credential is still a
                credential.
                {rollups > 0 && (
                  <>
                    {' '}
                    {rollups} compliance {rollups === 1 ? 'control is' : 'controls are'} shown but not
                    counted: their totals are the sum of workload checks already listed on their own.
                  </>
                )}
              </p>
            </div>
          )}

          <table className="w-full text-left mt-3">
            {/* Compliance is CONTROL-shaped, not workload-shaped: a benchmark
                control describes the cluster and names no resource, so the
                workload table renders empty for it — every one of its findings
                lands in `unassigned`. Two shapes, one table element. */}
            {group === 'compliance' ? (
              <>
                <thead>
                  <tr className="text-left text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary border-b border-kb-border">
                    <th className="px-4 py-2.5 font-medium">Control</th>
                    <th className="px-4 py-2.5 font-medium">Benchmark</th>
                    <th className="px-4 py-2.5 font-medium">Severity</th>
                    <th className="px-4 py-2.5 font-medium text-right">Open for</th>
                    <th className="px-4 py-2.5" />
                  </tr>
                </thead>
                <tbody>
                  {(data?.findings ?? []).map((f) => (
                    <tr
                      key={f.fingerprint}
                      onClick={() => setSelected(f)}
                      className="border-b border-kb-border last:border-0 hover:bg-kb-card-hover transition-colors cursor-pointer"
                    >
                      <td className="px-4 py-3">
                        <div className="text-xs text-kb-text-primary truncate max-w-[28rem]" title={f.title}>
                          {f.title}
                        </div>
                      </td>
                      <td className="px-4 py-3 text-[11px] font-mono text-kb-text-tertiary">
                        {f.cisControl ? `control ${f.cisControl}` : '—'}
                        {f.rollup && (
                          <span
                            className="ml-2 px-1.5 py-0.5 rounded text-[10px] uppercase bg-kb-elevated text-kb-text-tertiary"
                            title="Aggregate of findings also listed under Configuration — not counted in the totals"
                          >
                            rollup
                          </span>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <span
                          className={`px-1.5 py-0.5 rounded text-[10px] font-mono font-bold uppercase ${
                            SEV_PILL[f.severity] ?? SEV_PILL.low
                          }`}
                        >
                          {f.severity}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-right text-xs font-mono text-kb-text-tertiary tabular-nums">
                        {ago(f.firstSeen)}
                      </td>
                      <td className="px-4 py-3 text-right">
                        <ChevronRight className="w-4 h-4 text-kb-text-tertiary inline" />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </>
            ) : (
              <>
                <thead>
                  <tr className="text-left text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary border-b border-kb-border">
                    <th className="px-4 py-2.5 font-medium">{Unit}</th>
                    {/* Only when looking at ALL clusters. With one selected the
                        column would repeat the same value on every row, which is
                        noise dressed as information. */}
                    {showCluster && <th className="px-4 py-2.5 font-medium">Cluster</th>}
                    {/* Only the vulnerability lens has images: Trivy stamps them
                        on CVE reports, and a config-audit check is about the
                        manifest, not the artifact. An always-empty column is
                        worse than no column — it reads as missing data. */}
                    {showImage && <th className="px-4 py-2.5 font-medium">Image</th>}
                    <th className="px-4 py-2.5 font-medium">Severity</th>
                    <th className="px-4 py-2.5 font-medium text-right">Fixable</th>
                    <th className="px-4 py-2.5 font-medium text-right">Open for</th>
                    <th className="px-4 py-2.5" />
                  </tr>
                </thead>
                <tbody>
                  {workloads.map((w) => (
                    <tr
                      key={`${w.namespace}/${w.kind}/${w.name}`}
                      onClick={() => setSelectedWorkload(w)}
                      className="border-b border-kb-border last:border-0 hover:bg-kb-card-hover transition-colors cursor-pointer"
                    >
                      <td className="px-4 py-3">
                        <div className="text-xs text-kb-text-primary truncate max-w-[16rem]" title={w.name}>
                          {w.name}
                        </div>
                        <div className="text-[11px] font-mono text-kb-text-tertiary truncate max-w-[22rem]">
                          {w.kind}
                          {w.namespace ? ` · ${w.namespace}` : ''}
                          <KindMix kinds={w.kinds} />
                        </div>
                      </td>
                      {showCluster && (
                        <td className="px-4 py-3">
                          <span
                            className="text-[11px] font-mono text-kb-text-tertiary truncate"
                            title={w.clusterId}
                          >
                            {clusterName(w.clusterId)}
                          </span>
                        </td>
                      )}
                      {showImage && (
                        <td className="px-4 py-3">
                          {w.image ? (
                            <div
                              className="text-[11px] font-mono text-kb-text-secondary truncate max-w-[18rem]"
                              title={w.image}
                            >
                              {w.image}
                              {(w.images ?? 1) > 1 && (
                                <span className="text-kb-text-tertiary"> +{(w.images ?? 1) - 1}</span>
                              )}
                            </div>
                          ) : (
                            <span className="text-[11px] font-mono text-kb-text-tertiary">—</span>
                          )}
                        </td>
                      )}
                      <td className="px-4 py-3">
                        <SeverityBar w={w} />
                      </td>
                      <td className="px-4 py-3 text-right">
                        <span className="text-xs font-mono text-kb-text-secondary tabular-nums">
                          {w.fixable}/{w.total}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-right">
                        <span className="text-xs font-mono text-kb-text-tertiary tabular-nums">
                          {ago(w.oldestSeen)}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-right">
                        <ChevronRight className="w-4 h-4 text-kb-text-tertiary inline" />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </>
            )}
          </table>

          {/* An empty table used to render as a header row over nothing, which
              reads as a page that failed to load rather than as good news. Only
              Runtime had a message; the other four lenses had none.

              The copy separates the two cases, because they are opposite:
              nothing found in this lens (the scanner ran and there is nothing to
              fix) versus nothing found anywhere (probably no scanner installed).
              Saying "no findings" when the truth is "nothing is looking" is the
              same failure the source strip had, and it is the one that matters —
              an empty security page is only reassuring if something was watching. */}
          {!isLoading && listTotal === 0 && (
            <div className="px-4 py-10 text-center">
              <ShieldCheck className="w-7 h-7 text-status-ok mx-auto mb-2" />
              <div className="text-xs text-kb-text-primary">
                {severity || kind
                  ? 'Nothing matches this filter'
                  : scopeTotal === 0 && sourcesReporting === 0
                    ? 'No security data yet'
                    : `Nothing to fix under ${LENS_LABEL[group] ?? 'this lens'}`}
              </div>
              <div className="mt-1.5 text-[11px] text-kb-text-tertiary max-w-md mx-auto leading-relaxed">
                {severity || kind ? (
                  'Clear the filter above to see the rest.'
                ) : sourcesReporting === 0 ? (
                  <>
                    No scanner is reporting. Install Trivy, Kyverno or Falco on this cluster — an
                    empty page is only good news once something is looking.
                  </>
                ) : (
                  <>Other lenses may still have findings — check the tabs above.</>
                )}
              </div>
            </div>
          )}

          {/* A pager, not a truncation notice. The list used to cut at 200 and
              say so — honest, but it left the rest unreachable, and for a
              security list that means findings nobody can look at. */}
          {pages > 1 && (
            <div className="px-4 py-2 border-t border-kb-border flex items-center justify-between gap-3">
              <span className="text-[11px] font-mono text-kb-text-tertiary">
                {(page - 1) * pageSize + 1}–{Math.min(page * pageSize, listTotal)} of {listTotal} {group === 'compliance' ? 'controls' : `${unit}s`}
              </span>
              <div className="flex items-center gap-1">
                <PagerButton disabled={page <= 1} onClick={() => setPage(page - 1)}>
                  <ChevronLeft className="w-3.5 h-3.5" />
                </PagerButton>
                <span className="px-2 text-[11px] font-mono text-kb-text-tertiary tabular-nums">
                  {page} / {pages}
                </span>
                <PagerButton disabled={page >= pages} onClick={() => setPage(page + 1)}>
                  <ChevronRight className="w-3.5 h-3.5" />
                </PagerButton>
              </div>
            </div>
          )}
        </div>

        {/* Side column. The CIS panel now lives ONLY in its own tab: on the
            vulnerabilities view it answered a different question in the corner
            of the screen, which is the habit this split exists to break. The
            runtime feed moved out entirely — it is its own lens. */}
        <div className="space-y-4">
          {group !== 'compliance' && (
            <p className="text-[11px] text-kb-text-tertiary">
              {group === 'vulnerability' &&
                'Image vulnerabilities are fixed by rebuilding and rolling out the image. Workload settings live under Configuration; benchmark posture under Compliance.'}
              {group === 'configuration' &&
                'Workload settings are fixed by editing the manifest. Image CVEs live under Vulnerabilities; benchmark posture under Compliance.'}
              {/* Says what is NOT here as well as what is. Kubernetes\u2019 own
                  bootstrap roles are excluded on purpose \u2014 the control plane
                  reconciles any edit to them, so reporting them would fill the
                  page with advice that cannot be followed. Leaving that unsaid
                  makes the omission look like missing data. */}
              {group === 'rbac' &&
                'Over-permissive roles are fixed by narrowing the Role or ClusterRole. Kubernetes\u2019 built-in roles are not listed: the control plane restores them on edit, and they are identical on every cluster.'}
            </p>
          )}
          {group === 'compliance' && (
          <div className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
            <div className="px-4 py-3 border-b border-kb-border flex items-center gap-2">
              <ScrollText className="w-3.5 h-3.5 text-kb-text-tertiary" />
              <h2 className="text-sm font-semibold text-kb-text-primary">CIS compliance</h2>
            </div>
            {cisFailing === 0 ? (
              <div className="px-4 py-8 text-center">
                <ShieldCheck className="w-7 h-7 text-status-ok mx-auto mb-2" />
                <div className="text-[11px] text-kb-text-tertiary">
                  No failing controls — or no ClusterComplianceReport yet.
                </div>
              </div>
            ) : (
              <div className="divide-y divide-kb-border max-h-72 overflow-y-auto">
                {findings
                  .filter((f) => f.cisControl)
                  .slice(0, 12)
                  .map((f) => (
                    <div key={f.fingerprint} className="px-4 py-2 flex items-start gap-2">
                      <span className="font-mono text-[10px] text-kb-accent shrink-0">{f.cisControl}</span>
                      <span className="text-[11px] text-kb-text-secondary truncate" title={f.title}>
                        {f.title}
                      </span>
                    </div>
                  ))}
                <div className="px-4 py-2 text-[11px] font-mono text-kb-text-tertiary">
                  {cisFailing} controls failing. A pass/fail score needs the compliance summary the
                  sweep does not persist yet.
                </div>
              </div>
            )}
          </div>

          )}
        </div>
      </div>
      )}
    </div>
  )
}

// RuntimeOnly is the Falco lens. It is a separate TAB rather than a side panel
// because it answers a different question in a different tense: the other three
// describe state ("what is wrong"), this one describes events ("what just
// happened"). Ranking a runtime alert against a CVE is meaningless.
// The ring keeps Falco's vocabulary rather than being remapped onto the CVE
// scale, and DERIVES its colours from the one palette so a band can never
// disagree with the pill beside it — which is exactly what happened when the two
// were written out separately and one of them forgot Notice.
const RUNTIME_BANDS = RUNTIME_PRIORITIES.map((p) => ({
  key: p.key,
  stroke: p.stroke,
  swatch: p.swatch,
  label: p.key,
}))

// The feed is fetched in one generous page and sliced here: the events endpoint
// takes a limit but no offset, and adding one would mean an offset through both
// the Bolt and Postgres stores for a stream that is already bounded to 24h.
// When the cap is reached the list says so rather than pretending it is all.
const RUNTIME_FETCH_LIMIT = 500
const RUNTIME_PAGE_SIZE = 25

function RuntimeOnly({ cluster }: { cluster: string }) {
  const [selected, setSelected] = useState<RuntimeEvent | null>(null)
  const [priority, setPriority] = useState('')
  const [page, setPage] = useState(1)
  useEffect(() => setPage(1), [priority, cluster])

  // ONE query, filtered in the browser. The priority facet used to be a second
  // round-trip, which meant the chips' own counts came from a different response
  // than the rows — two sources for one number is how they drift.
  const { data } = useQuery({
    queryKey: ['runtime-events', cluster],
    queryFn: () =>
      api.listRuntimeEvents({
        cluster: cluster || undefined,
        since: '24h',
        limit: RUNTIME_FETCH_LIMIT,
      }),
    refetchInterval: 30_000,
  })
  const { data: clusters = [] } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })

  const all = data?.events ?? []
  const events = priority ? all.filter((e) => e.priority === priority) : all
  const capped = all.length >= RUNTIME_FETCH_LIMIT

  const byPriority = all.reduce<Record<string, number>>((acc, e) => {
    acc[e.priority] = (acc[e.priority] ?? 0) + 1
    return acc
  }, {})

  // Which rules are firing, and how widely. The counterpart of "top checks by
  // reach" on the other tabs: one rule accounting for most of the feed is the
  // difference between an incident and a noisy rule.
  const ruleAgg = new Map<string, { hits: number; where: Set<string> }>()
  for (const e of events) {
    const agg = ruleAgg.get(e.ruleName) ?? { hits: 0, where: new Set<string>() }
    agg.hits++
    agg.where.add(e.podName ? `${e.namespace}/${e.podName}` : e.fields?.hostname || '—')
    ruleAgg.set(e.ruleName, agg)
  }
  const topRules = [...ruleAgg.entries()]
    .map(([rule, a]) => ({ rule, hits: a.hits, where: a.where.size }))
    .sort((a, b) => b.hits - a.hits || a.rule.localeCompare(b.rule))
    .slice(0, 5)

  const pages = Math.max(1, Math.ceil(events.length / RUNTIME_PAGE_SIZE))
  const rows = events.slice((page - 1) * RUNTIME_PAGE_SIZE, page * RUNTIME_PAGE_SIZE)

  const clusterName = (uid?: string) => {
    if (!uid) return ''
    const c = clusters.find((x) => x.clusterId === uid)
    return c?.displayName || c?.name?.replace(/^agent:/, '') || uid.slice(0, 8)
  }
  // Same rule as the other lenses: with one cluster the column would repeat one
  // value down every row, which is noise wearing the shape of information.
  const showCluster = !cluster && clusters.length > 1

  return (
    <div className="grid grid-cols-1 gap-4">
      {selected && (
        <RuntimeEventModal
          event={selected}
          clusterName={clusterName(selected.clusterId)}
          onClose={() => setSelected(null)}
        />
      )}
      <div className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
        <div className="px-4 py-3 border-b border-kb-border flex items-center gap-2 flex-wrap">
          <Activity className="w-3.5 h-3.5 text-kb-text-tertiary" />
          <h2 className="text-sm font-semibold text-kb-text-primary">Runtime threats</h2>
          <span className="text-[11px] font-mono text-kb-text-tertiary">in the last 24h</span>
          <div className="ml-auto flex gap-1.5 flex-wrap">
            {RUNTIME_BANDS.map((b) =>
              (byPriority[b.key] ?? 0) > 0
                ? chipButton(`${b.key} ${byPriority[b.key]}`, priority === b.key, () =>
                    setPriority(priority === b.key ? '' : b.key),
                  )
                : null,
            )}
            {all.length > 0 &&
              chipButton(`All ${all.length}`, !priority, () => setPriority(''))}
          </div>
        </div>

        {all.length === 0 ? (
          // An empty runtime feed is ambiguous in a way no other lens is: it
          // reads identically whether nothing happened or nothing is connected,
          // and only one of those is good news. The old copy admitted the
          // ambiguity and left the operator there; this one names the three
          // things that actually go wrong, in the order they go wrong.
          <div className="px-4 py-10 text-center">
            <ShieldCheck className="w-7 h-7 text-status-ok mx-auto mb-2" />
            <div className="text-[11px] text-kb-text-tertiary">
              No runtime events in the last 24h.
            </div>
            <div className="mt-2 text-[11px] text-kb-text-tertiary max-w-xl mx-auto leading-relaxed">
              If Falco is installed and this stays empty, check in this order: its ingest token must
              be <span className="text-kb-text-secondary">scoped to a cluster</span> (an unscoped one
              is refused), the webhook header key is{' '}
              <span className="font-mono text-kb-text-secondary">customHeaders</span> — the chart
              documents it lowercase and reads it camelCase, which fails silently — and{' '}
              <span className="font-mono text-kb-text-secondary">POST OK (202)</span> should appear in
              the falcosidekick log.
            </div>
          </div>
        ) : (
          <>
            {/* Same summary grammar as the other lenses: the shape on the left,
                where the leverage is on the right. */}
            <div className="px-4 pt-3">
              <div className="flex flex-col lg:flex-row lg:items-start gap-6">
                <SeverityDonut counts={byPriority} bands={RUNTIME_BANDS} unit="events" />
                {topRules.length > 0 && (
                  <div className="min-w-0 flex-1 max-w-2xl">
                    <div className="rounded-lg border border-kb-border overflow-hidden">
                      <table className="w-full table-fixed text-left">
                        <thead className="bg-kb-elevated">
                          <tr className="text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary">
                            <th className="px-3 py-1.5 font-medium">Top 5 rules by hits</th>
                            <th className="px-3 py-1.5 font-medium text-right w-24">Where</th>
                            <th className="px-3 py-1.5 font-medium text-right w-16">Hits</th>
                          </tr>
                        </thead>
                        <tbody>
                          {topRules.map((r) => (
                            <tr key={r.rule} className="border-t border-kb-border">
                              <td className="px-3 py-1.5">
                                <span
                                  className="text-[11px] text-kb-text-secondary truncate block"
                                  title={r.rule}
                                >
                                  {r.rule}
                                </span>
                              </td>
                              <td className="px-3 py-1.5 text-right text-[11px] font-mono tabular-nums text-kb-text-tertiary">
                                {r.where}
                              </td>
                              <td className="px-3 py-1.5 text-right text-[11px] font-mono tabular-nums text-kb-text-primary">
                                {r.hits}
                              </td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                    </div>
                    <p className="mt-2 text-[11px] text-kb-text-tertiary">
                      One rule carrying most of the feed is worth reading before the rows are.
                    </p>
                  </div>
                )}
              </div>
              <p className="text-[11px] text-kb-text-tertiary mt-1.5">
                Events age out of the feed after 24h rather than being resolved.
                {capped && ` Showing the most recent ${RUNTIME_FETCH_LIMIT} — older ones are not on this page.`}
              </p>
            </div>

            {/* A TABLE, like every other lens. It was a stack of text, so the
                only way to learn anything about a row was to open it — while
                the columns the other tabs use were sitting unused in the event.

                What each column earns its width for:
                  Threat  the rule, and under it the COMMAND — which is what
                          tells two hits of the same rule apart (three reads of
                          /etc/shadow by cat, grep and awk are three incidents).
                  Target  the object the rule fired ON. It lived only in the
                          popup, and it is half of what the alert says.
                  Where   pod, or the NODE when the rule is host-scoped — an
                          event without a pod is not missing data.
                  User    root vs a service account changes the reading. */}
            <table className="w-full text-left mt-3">
              <thead>
                <tr className="text-left text-[10px] font-mono uppercase tracking-wide text-kb-text-tertiary border-b border-kb-border">
                  <th className="px-4 py-2.5 font-medium">Threat</th>
                  <th className="px-4 py-2.5 font-medium">Target</th>
                  <th className="px-4 py-2.5 font-medium">Where</th>
                  {showCluster && <th className="px-4 py-2.5 font-medium">Cluster</th>}
                  <th className="px-4 py-2.5 font-medium">User</th>
                  <th className="px-4 py-2.5 font-medium text-right">When</th>
                  <th className="px-4 py-2.5" />
                </tr>
              </thead>
              <tbody>
                {rows.map((e, i) => (
                  <tr
                    key={e.id || `${e.at}-${i}`}
                    onClick={() => setSelected(e)}
                    className="border-b border-kb-border last:border-0 hover:bg-kb-card-hover transition-colors cursor-pointer"
                  >
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2 min-w-0">
                        <span
                          className={`shrink-0 px-1.5 py-0.5 rounded text-[9px] font-mono uppercase ${
                            priorityPill(e.priority)
                          }`}
                        >
                          {e.priority}
                        </span>
                        <span
                          className="text-xs text-kb-text-primary truncate max-w-[18rem]"
                          title={e.ruleName}
                        >
                          {e.ruleName}
                        </span>
                      </div>
                      <div
                        className="mt-0.5 text-[11px] font-mono text-kb-text-secondary truncate max-w-[24rem]"
                        title={e.fields?.['proc.cmdline'] || falcoSentence(e.detectedBehavior)}
                      >
                        {e.fields?.['proc.cmdline'] || falcoSentence(e.detectedBehavior)}
                      </div>
                    </td>
                    <td className="px-4 py-3">
                      <span
                        className="text-[11px] font-mono text-kb-text-secondary truncate block max-w-[14rem]"
                        title={e.fields?.['fd.name'] || ''}
                      >
                        {e.fields?.['fd.name'] || '—'}
                      </span>
                      {e.fields?.['evt.type'] && (
                        <span className="text-[10px] font-mono text-kb-text-tertiary">
                          {e.fields['evt.type']}
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      <div className="text-[11px] font-mono text-kb-text-secondary truncate max-w-[14rem]">
                        {e.podName || `node ${e.fields?.hostname || '—'}`}
                      </div>
                      <div className="text-[10px] font-mono text-kb-text-tertiary truncate max-w-[14rem]">
                        {e.podName ? e.namespace : e.fields?.['container.name'] || ''}
                      </div>
                    </td>
                    {showCluster && (
                      <td className="px-4 py-3">
                        <span
                          className="text-[11px] font-mono text-kb-text-tertiary truncate"
                          title={e.clusterId}
                        >
                          {clusterName(e.clusterId)}
                        </span>
                      </td>
                    )}
                    <td className="px-4 py-3">
                      <span className="text-[11px] font-mono text-kb-text-tertiary">
                        {e.fields?.['user.name'] || '—'}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-right">
                      <span className="text-xs font-mono text-kb-text-tertiary tabular-nums">
                        {ago(e.at)}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-right">
                      <ChevronRight className="w-4 h-4 text-kb-text-tertiary inline" />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>

            {pages > 1 && (
              <div className="px-4 py-2 border-t border-kb-border flex items-center justify-between gap-3">
                <span className="text-[11px] font-mono text-kb-text-tertiary">
                  {(page - 1) * RUNTIME_PAGE_SIZE + 1}–
                  {Math.min(page * RUNTIME_PAGE_SIZE, events.length)} of {events.length} events
                </span>
                <div className="flex items-center gap-1">
                  <PagerButton disabled={page <= 1} onClick={() => setPage(page - 1)}>
                    <ChevronLeft className="w-3.5 h-3.5" />
                  </PagerButton>
                  <span className="px-2 text-[11px] font-mono text-kb-text-tertiary tabular-nums">
                    {page} / {pages}
                  </span>
                  <PagerButton disabled={page >= pages} onClick={() => setPage(page + 1)}>
                    <ChevronRight className="w-3.5 h-3.5" />
                  </PagerButton>
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}

// chipButton is the filter-chip shape, lifted out of SecurityPage's closure so
// the Runtime lens uses the SAME control rather than a lookalike.
function chipButton(label: string, active: boolean, onClick: () => void) {
  return (
    <button
      key={label}
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`px-2.5 py-1 rounded-full text-[11px] font-mono border transition-colors ${
        active
          ? 'bg-kb-accent text-white border-kb-accent'
          : 'bg-kb-elevated border-kb-border text-kb-text-secondary hover:border-kb-border-active hover:text-kb-text-primary'
      }`}
    >
      {label}
    </button>
  )
}

function PagerButton({
  disabled,
  onClick,
  children,
}: {
  disabled: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className="p-1 rounded border border-kb-border text-kb-text-tertiary hover:text-kb-text-primary
                 disabled:opacity-40 disabled:hover:text-kb-text-tertiary transition-colors"
    >
      {children}
    </button>
  )
}

// SevCount is a severity chip carrying its own count. Omitted entirely at zero:
// a row reading "0 0 3 0" makes the eye do arithmetic before it can see
// anything.
// SeverityBar shows the SHAPE of a workload's problem before its size. Four
// separate chips still made the eye read four numbers and compare them; a
// stacked bar answers "how bad, proportionally" in one glance, with the counts
// beside it when the detail is wanted. Bands at zero draw nothing rather than a
// "0", so nothing competes for attention that has nothing to say.
function SeverityBar({
  w,
}: {
  w: { critical: number; high: number; medium: number; low: number; total: number }
}) {
  const bands = [
    { n: w.critical, bg: 'bg-status-error', text: 'text-status-error', label: 'critical' },
    { n: w.high, bg: 'bg-status-warn', text: 'text-status-warn', label: 'high' },
    { n: w.medium, bg: 'bg-status-info', text: 'text-status-info', label: 'medium' },
    { n: w.low, bg: 'bg-kb-text-tertiary', text: 'text-kb-text-tertiary', label: 'low' },
  ].filter((b) => b.n > 0)
  const total = w.total || 1

  return (
    <div className="flex items-center gap-2.5 min-w-[11rem]">
      <div className="flex h-1.5 w-24 shrink-0 overflow-hidden rounded-full bg-kb-elevated">
        {bands.map((b) => (
          <div
            key={b.label}
            className={b.bg}
            style={{ width: `${(b.n / total) * 100}%` }}
            title={`${b.n} ${b.label}`}
          />
        ))}
      </div>
      <div className="flex items-baseline gap-2">
        {bands.map((b) => (
          <span key={b.label} className={`text-xs font-mono tabular-nums ${b.text}`} title={b.label}>
            {b.n}
          </span>
        ))}
      </div>
    </div>
  )
}
