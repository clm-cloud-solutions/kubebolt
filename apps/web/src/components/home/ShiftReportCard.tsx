import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { RotateCw, X } from 'lucide-react'
import { api, type InsightEpisode, type OperationalBurst, type ShiftReport } from '@/services/api'
import { LABELS as CAPABILITY_LABELS } from '@/ee/CapabilitiesSection'

// «While you were away» — Fase 3's shift report, restyled to the Home design
// (in-vivo 01-sep): it lives INSIDE the greeting card as its lower section,
// tells the window as a NARRATIVE (times, counts, the straggler linked to
// its episode), carries the degraded-capability banner with its remedy, the
// two suppression counters, and the coverage line. Dismissing collapses it
// to a «⟳ while you were away» chip in the greeting's chip row.
//
// The beacon ordering rule is unchanged: the report is read FIRST, the
// beacon fires after — marking first would collapse the window to seconds.

const DISMISS_KEY = 'kb-shift-report-dismissed'

export function fmtDur(seconds: number): string {
  const mins = Math.max(0, Math.round(seconds / 60))
  if (mins < 60) return `${mins}m`
  const h = Math.floor(mins / 60)
  if (h < 48) return `${h}h ${mins % 60}m`
  return `${Math.floor(h / 24)}d ${h % 24}h`
}

export function isQuietShift(r: ShiftReport): boolean {
  // Quiet = no EVENTS while away. Standing conditions don't break the quiet
  // — they were already known, and the Active list owns them.
  const e = r.episodes
  return (
    r.bursts.length === 0 &&
    e.opened === 0 &&
    e.autoRecovered === 0 &&
    e.remediated === 0 &&
    e.expired === 0 &&
    r.capabilityChanges === 0
  )
}

// ── narrative (pure, testable) ──────────────────────────────────────────────

export type Phrase = { t: string; tone?: 'time' | 'bad' | 'name' | 'ok' }

const KIND_OPENERS: Record<OperationalBurst['kind'], string> = {
  node_rotation: 'A node rotation',
  node_pressure: 'Node pressure',
  mass_rollout: 'A broad rollout',
  unknown_burst: 'A burst of findings',
}

function hhmm(iso: string, withDate = false): string {
  const d = new Date(iso)
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
  // Over a window longer than a day "05:30 PM" doesn't say which day.
  return withDate ? `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}, ${time}` : time
}

// spansDays — the report's window is long enough that a bare time is ambiguous.
export function spansDays(fromIso: string, toIso: string): boolean {
  return Date.parse(toIso) - Date.parse(fromIso) > 24 * 3600_000
}

// SHIFT_BURSTS_SHOWN — sentences the narrative writes out. Past it the report
// summarizes: a month away can hold fifty bursts, and fifty sentences in a row
// is a wall nobody reads (in-vivo 2026-10-05).
export const SHIFT_BURSTS_SHOWN = 3

// pickBursts — which bursts get a sentence when there are too many: the ones
// with workloads still down first (worst first), then the largest; told in
// the order they happened.
export function pickBursts(bursts: OperationalBurst[], max = SHIFT_BURSTS_SHOWN): OperationalBurst[] {
  if (bursts.length <= max) return bursts
  const ranked = [...bursts].sort(
    (a, b) =>
      b.blast.stillFiring - a.blast.stillFiring ||
      b.blast.affected - a.blast.affected ||
      Date.parse(b.windowFrom) - Date.parse(a.windowFrom),
  )
  return ranked.slice(0, max).sort((a, b) => Date.parse(a.windowFrom) - Date.parse(b.windowFrom))
}

const KIND_PLURALS: Record<OperationalBurst['kind'], [string, string]> = {
  node_rotation: ['node rotation', 'node rotations'],
  node_pressure: ['node-pressure burst', 'node-pressure bursts'],
  mass_rollout: ['broad rollout', 'broad rollouts'],
  unknown_burst: ['burst of findings', 'bursts of findings'],
}

// burstSummaryPhrases — the lead sentence when the narrative is summarized:
// «47 bursts since Sep 5 — 45 recovered on their own, 2 with workloads still
// down (32 bursts of findings, 15 broad rollouts).»
export function burstSummaryPhrases(bursts: OperationalBurst[], windowFrom: string): Phrase[] {
  const down = bursts.filter((b) => b.blast.stillFiring > 0).length
  const since = new Date(windowFrom).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  const kinds = new Map<OperationalBurst['kind'], number>()
  for (const b of bursts) kinds.set(b.kind, (kinds.get(b.kind) ?? 0) + 1)
  const breakdown = [...kinds.entries()]
    .sort((a, b) => b[1] - a[1])
    .map(([k, n]) => `${n} ${KIND_PLURALS[k][n === 1 ? 0 : 1]}`)
    .join(', ')
  const out: Phrase[] = [
    { t: `${bursts.length} bursts`, tone: 'bad' },
    { t: ' since ' },
    { t: since, tone: 'time' },
    { t: ' — ' },
  ]
  if (down === 0) {
    out.push({ t: 'all recovered on their own', tone: 'ok' })
  } else {
    out.push(
      { t: `${bursts.length - down} recovered on their own, ` },
      { t: `${down} with workloads still down`, tone: 'bad' },
    )
  }
  out.push({ t: ` (${breakdown}).` })
  return out
}

// burstPhrases — the mock's sentence as typed segments: «A node rotation at
// 05:51 across gke-orquestador and gke-procesamiento hit 46 workloads.
// Everything recovered by 06:40» (or «N still down»).
export function burstPhrases(b: OperationalBurst, names: Record<string, string>, withDate = false): Phrase[] {
  const named = b.clusters.map((uid) => names[uid]).filter(Boolean)
  const where =
    named.length > 0
      ? named.join(' and ')
      : `${b.clusters.length} cluster${b.clusters.length === 1 ? '' : 's'}`
  const out: Phrase[] = [
    { t: KIND_OPENERS[b.kind] },
    { t: ' at ' },
    { t: hhmm(b.windowFrom, withDate), tone: 'time' },
    { t: ' across ' },
    { t: where, tone: 'name' },
    { t: ' hit ' },
    { t: `${b.blast.affected} workload${b.blast.affected === 1 ? '' : 's'}`, tone: 'bad' },
    { t: '. ' },
  ]
  if (b.blast.stillFiring === 0) {
    out.push({ t: 'Everything recovered by ' }, { t: hhmm(b.windowTo, withDate), tone: 'time' }, { t: '.' })
  } else {
    out.push(
      { t: `${b.blast.autoRecovered + b.blast.remediated} recovered` },
      { t: ' — ' },
      { t: `${b.blast.stillFiring} still down`, tone: 'bad' },
      { t: '.' },
    )
  }
  return out
}

// ── shared state (ONE hook call in HomePage feeds chip + section) ───────────

function readDismissed(): string {
  try {
    return localStorage.getItem(DISMISS_KEY) ?? ''
  } catch {
    return ''
  }
}
function writeDismissed(v: string) {
  try {
    localStorage.setItem(DISMISS_KEY, v)
  } catch {
    /* per-viewer convenience only */
  }
}

export function useShiftReport() {
  const { data, isSuccess, isError } = useQuery({
    queryKey: ['shift-report'],
    queryFn: api.getShiftReport,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    retry: false,
  })
  const marked = useRef(false)
  useEffect(() => {
    if ((isSuccess || isError) && !marked.current) {
      marked.current = true
      api.markDashboardSeen().catch(() => {})
    }
  }, [isSuccess, isError])

  const [dismissedKey, setDismissedKey] = useState(readDismissed)
  const dismissed = !!data && dismissedKey === data.windowFrom
  return {
    report: data ?? null,
    dismissed,
    dismiss: () => {
      if (data) {
        setDismissedKey(data.windowFrom)
        writeDismissed(data.windowFrom)
      }
    },
    reopen: () => {
      setDismissedKey('')
      writeDismissed('')
    },
  }
}

// ── the chip (greeting chip row, shown while dismissed) ─────────────────────

export function ShiftReportChip({
  report,
  dismissed,
  reopen,
}: {
  report: ShiftReport | null
  dismissed: boolean
  reopen: () => void
}) {
  if (!report || !dismissed) return null
  return (
    <button
      type="button"
      onClick={reopen}
      title="Reopen the shift report"
      className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full border border-kb-border bg-kb-card text-kb-text-secondary text-[10px] font-mono uppercase tracking-[0.08em] hover:text-kb-text-primary hover:border-kb-border-active transition-colors"
    >
      <RotateCw className="w-3 h-3" />
      while you were away
    </button>
  )
}

// ── capability remedies (the mock's amber banner action) ────────────────────

const CAP_ACTIONS: Record<string, { label: string; to: string }> = {
  autopilot: { label: 'Enable →', to: '/admin/ai?tab=autopilot' },
  notifications: { label: 'Configure →', to: '/admin/system?tab=notifications' },
  credits: { label: 'Add credits →', to: '/admin/billing' },
  ingest_caps: { label: 'Review plan →', to: '/admin/billing' },
  active_series: { label: 'Review plan →', to: '/admin/billing' },
}

function toneSpan(p: Phrase, i: number): ReactNode {
  const cls =
    p.tone === 'time'
      ? 'font-mono text-status-warn font-semibold'
      : p.tone === 'bad'
        ? 'font-mono text-status-error font-semibold'
        : p.tone === 'name'
          ? 'font-semibold text-kb-text-primary'
          : p.tone === 'ok'
            ? 'font-mono text-status-ok font-semibold'
            : ''
  return cls ? (
    <span key={i} className={cls}>
      {p.t}
    </span>
  ) : (
    <span key={i}>{p.t}</span>
  )
}

// ── the section (inside the greeting card) ──────────────────────────────────
//
// The site's «While you were away»: a timeline of the window with one dot per
// episode that opened in it, then the few episodes worth a row — still open
// first (they carry «Review →»), then what resolved, then what expired — and
// the counted summary for everything else. The narrative, the capability
// banners, the suppression counters and the coverage line stay as they were.

const SEV_COLOR: Record<string, string> = { critical: '#ef4056', warning: '#f5a623', info: '#4c9aff' }
const OK = '#22d68a'

function shortResource(resource: string): string {
  const parts = resource.split('/')
  return parts[parts.length - 1] || resource
}

function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

// A time of day alone reads as "today": anything older carries its date.
function when(iso: string): string {
  const d = new Date(iso)
  if (d.toDateString() === new Date().toDateString()) return clock(iso)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

const SEV_RANK: Record<string, number> = { critical: 0, warning: 1, info: 2 }

// Which episodes earn one of the three rows. The rows are a sample of the
// window, not its top three: three resolved rows on a morning with eight
// still open would say "all handled". So the first row goes to the worst
// thing still open (opened in the window before carried over from before
// it), the second to the worst thing that resolved, and the third to the
// best of what is left — each pick worst severity first, then most recent.
function pickRows(episodes: InsightEpisode[], from: number, n: number): InsightEpisode[] {
  const sev = (e: InsightEpisode) => SEV_RANK[e.maxSeverity] ?? 3
  const rank = (e: InsightEpisode) => {
    const group =
      e.status === 'firing' ? (Date.parse(e.firstSeen) >= from ? 0 : 2) : e.status === 'resolved' ? 1 : 3
    return group * 10 + sev(e)
  }
  const sorted = [...episodes].sort(
    (a, b) => rank(a) - rank(b) || Date.parse(b.lastSeen) - Date.parse(a.lastSeen),
  )
  const picked: InsightEpisode[] = []
  for (const want of ['firing', 'resolved'] as const) {
    const hit = sorted.find((e) => e.status === want)
    if (hit && picked.length < n) picked.push(hit)
  }
  for (const e of sorted) {
    if (picked.length >= n) break
    if (!picked.includes(e)) picked.push(e)
  }
  // Shown open first — the row that asks for something leads.
  const shown = (e: InsightEpisode) => (e.status === 'firing' ? 0 : e.status === 'resolved' ? 10 : 20) + sev(e)
  return picked.sort((a, b) => shown(a) - shown(b))
}

function EpisodeRow({ e, showCluster }: { e: InsightEpisode; showCluster: boolean }) {
  const firing = e.status === 'firing'
  const resolved = e.status === 'resolved'
  const color = firing ? (SEV_COLOR[e.maxSeverity] ?? SEV_COLOR.warning) : resolved ? OK : 'var(--kb-text-tertiary)'
  const start = Date.parse(e.firstSeen)
  const end = resolved && e.resolvedAt ? Date.parse(e.resolvedAt) : firing ? Date.now() : Date.parse(e.lastSeen)
  const dur = fmtDur(Math.max(0, Math.round((end - start) / 1000)))
  const where = showCluster && e.clusterName ? ` · ${e.clusterName.replace(/ \(via agent\)$/, '')}` : ''
  const sub = firing
    ? `firing since ${when(e.firstSeen)} · ${dur}${where}`
    : resolved
      ? `resolved ${when(e.resolvedAt ?? e.lastSeen)} · after ${dur}${where}`
      : `expired · no signal since ${when(e.lastSeen)}${where}`
  return (
    <div
      className="flex items-center gap-3 rounded-xl px-3.5 py-2.5 min-w-0"
      style={{ background: 'var(--kb-surface)', boxShadow: firing ? `inset 0 0 0 1px color-mix(in srgb, ${color} 22%, transparent)` : undefined }}
    >
      <span
        className="w-7 h-7 rounded-full flex items-center justify-center shrink-0 text-[12px] font-semibold"
        style={{ color, background: `color-mix(in srgb, ${color} 14%, transparent)` }}
        aria-hidden
      >
        {firing ? '!' : resolved ? '✓' : '↺'}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] text-kb-text-primary truncate">
          <span className="font-semibold">{shortResource(e.resource)}</span>
          <span className="text-kb-text-secondary"> · {e.title || e.ruleId}</span>
        </div>
        <div className="text-[11px] font-mono text-kb-text-tertiary truncate">{sub}</div>
      </div>
      {firing ? (
        <Link
          to={`/insights/episodes/${e.id}?from=active`}
          className="shrink-0 px-3 py-1 rounded-full text-[11px] font-mono transition-colors hover:opacity-80"
          style={{ color, boxShadow: `inset 0 0 0 1px color-mix(in srgb, ${color} 55%, transparent)` }}
        >
          Review →
        </Link>
      ) : (
        <Link
          to={`/insights/episodes/${e.id}`}
          className="shrink-0 text-[11px] font-mono hover:opacity-80"
          style={{ color }}
        >
          {resolved ? e.resolutionKind || 'resolved' : e.status}
        </Link>
      )}
    </div>
  )
}

// The window as a track, one dot per moment something opened. Episodes that
// open together (a node rotation opens a dozen) share a dot, which grows with
// the count and takes the worst state among them.
function dotState(e: InsightEpisode): number {
  if (e.status === 'firing') return e.maxSeverity === 'critical' ? 0 : 1
  return e.status === 'resolved' ? 2 : 3
}
const DOT_COLOR = [SEV_COLOR.critical, SEV_COLOR.warning, OK, 'var(--kb-text-tertiary)']

function Timeline({ episodes, from, to }: { episodes: InsightEpisode[]; from: string; to: string }) {
  const t0 = Date.parse(from)
  const t1 = Date.parse(to)
  const span = t1 - t0
  if (!(span > 0)) return null
  const buckets = new Map<number, { n: number; state: number }>()
  for (const e of episodes) {
    const t = Date.parse(e.firstSeen)
    if (t < t0) continue
    const k = Math.round(((t - t0) / span) * 120)
    const cur = buckets.get(k)
    buckets.set(k, { n: (cur?.n ?? 0) + 1, state: Math.min(cur?.state ?? 3, dotState(e)) })
  }
  if (buckets.size === 0) return null
  return (
    <div className="mt-1 mb-3">
      <div className="relative h-4">
        <div className="absolute inset-x-0 top-1/2 h-[3px] -translate-y-1/2 rounded-full" style={{ background: 'var(--kb-bar-track)' }} />
        {[...buckets.entries()].map(([k, d]) => {
          const size = Math.min(14, 7 + Math.log2(d.n) * 2)
          return (
            <span
              key={k}
              title={`${d.n} opened`}
              className="absolute top-1/2 rounded-full -translate-x-1/2 -translate-y-1/2"
              style={{ left: `${(k / 120) * 100}%`, width: size, height: size, background: DOT_COLOR[d.state], opacity: d.state < 2 ? 1 : 0.8 }}
            />
          )
        })}
      </div>
      <div className="mt-0.5 flex justify-between text-[10px] font-mono text-kb-text-tertiary">
        <span>{fmtDur(Math.round(span / 1000)).replace(/ 0m$/, '')} ago</span>
        <span>now</span>
      </div>
    </div>
  )
}

export function ShiftReportSection({
  report,
  dismissed,
  dismiss,
  visibleIds,
}: {
  report: ShiftReport | null
  dismissed: boolean
  dismiss: () => void
  // The team lens: episode rows fold over the same clusters as the page.
  visibleIds?: string[]
}) {
  const quiet = report ? isQuietShift(report) : true
  const episodesQ = useQuery({
    queryKey: ['shift-report', 'episodes', report?.windowFrom, report?.windowTo],
    queryFn: () => api.getInsightEpisodes({ since: report!.windowFrom, until: report!.windowTo, cluster: 'all', limit: 300 }),
    enabled: !!report && !dismissed && !quiet,
    staleTime: Infinity,
    retry: false,
  })
  if (!report || dismissed) return null
  const e = report.episodes
  const names = report.clusterNames ?? {}
  const windowLabel = report.firstShift
    ? 'your first shift — the last 24h'
    : `since your last visit · ${when(report.windowFrom)} → now`

  const ingestTrouble = report.capabilities.some(
    (c) => (c.id === 'ingest_caps' || c.id === 'active_series') && c.status === 'degraded',
  )
  const bannerCaps = report.capabilities.filter((c) => c.id in CAP_ACTIONS && c.status !== 'near')

  const from = Date.parse(report.windowFrom)
  const lens = visibleIds ? new Set(visibleIds) : null
  const episodes = (episodesQ.data?.episodes ?? [])
    .filter((ep) => ep.status !== 'superseded')
    .filter((ep) => !lens || lens.has(ep.clusterId))
  const rows = pickRows(episodes, from, 3)
  const multiCluster = new Set(episodes.map((ep) => ep.clusterId)).size > 1
  const more = episodes.length - rows.length
  const withDates = spansDays(report.windowFrom, report.windowTo)
  const shownBursts = pickBursts(report.bursts)
  const summarized = shownBursts.length < report.bursts.length

  return (
    <div className="relative px-5 pt-3 pb-4">
      <div className="flex items-baseline gap-3 flex-wrap mb-2">
        <h2 className="text-[10.5px] font-mono uppercase tracking-[0.16em] text-kb-text-secondary">While you were away</h2>
        <span className="text-[10.5px] font-mono text-kb-text-tertiary">{windowLabel}</span>
        <span className="flex-1" />
        <button
          type="button"
          onClick={dismiss}
          className="inline-flex items-center gap-1 text-[10.5px] font-mono text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
          title="Close — a chip stays above to bring it back"
        >
          close <X className="w-3 h-3" />
        </button>
      </div>

      {!quiet && <Timeline episodes={episodes} from={report.windowFrom} to={report.windowTo} />}

      {quiet ? (
        <p className="text-[12.5px] text-kb-text-secondary">
          <span className="font-medium text-status-ok">All quiet</span> — nothing opened, resolved
          or expired, and no capability changed.
        </p>
      ) : (
        <div className="space-y-2.5">
          {/* Narrative: one sentence per burst, the straggler linked. Past
              SHIFT_BURSTS_SHOWN it summarizes and links to the burst view. */}
          {report.bursts.length > 0 && (
            <p className="text-[13px] text-kb-text-secondary leading-relaxed max-w-[90ch]">
              {summarized && (
                <>
                  {burstSummaryPhrases(report.bursts, report.windowFrom).map(toneSpan)}{' '}
                  {report.bursts.some((b) => b.blast.stillFiring > 0) ? 'The worst:' : 'The largest:'}{' '}
                </>
              )}
              {shownBursts.map((b, bi) => (
                <span key={b.id}>
                  {bi > 0 && ' '}
                  {burstPhrases(b, names, withDates).map(toneSpan)}
                </span>
              ))}
              {summarized && (
                <>
                  {' '}
                  <Link
                    to={`/insights?view=bursts&from=${encodeURIComponent(report.windowFrom)}&to=${encodeURIComponent(report.windowTo)}`}
                    className="font-mono text-kb-accent underline underline-offset-2 hover:opacity-80 whitespace-nowrap"
                  >
                    See all {report.bursts.length} bursts →
                  </Link>
                </>
              )}
              {report.worst && report.worst.status === 'firing' && report.worst.seconds > 0 && (
                <>
                  {' '}
                  The longest,{' '}
                  <Link
                    to={`/insights/episodes/${report.worst.id}?from=active`}
                    className="font-mono text-kb-accent underline underline-offset-2 hover:opacity-80"
                  >
                    {report.worst.resource}
                  </Link>
                  , has been down{' '}
                  <span className="font-mono text-status-error font-semibold">
                    {fmtDur(report.worst.seconds)}
                  </span>
                  .
                </>
              )}
            </p>
          )}

          {rows.length > 0 && (
            <div className="space-y-1.5">
              {rows.map((ep) => (
                <EpisodeRow key={ep.id} e={ep} showCluster={multiCluster} />
              ))}
            </div>
          )}

          {/* Counted summary — everything the rows above do not show. */}
          <p className="text-[12px] font-mono">
            <span className="text-status-ok">
              {e.autoRecovered.toLocaleString()} resolved on their own
            </span>
            <span className="text-kb-text-secondary">
              {' · '}
              {e.opened.toLocaleString()} new
              {e.stillFiring > 0 && (
                <>
                  {' · '}
                  <span className="text-status-error">{e.stillFiring} still open</span>
                </>
              )}
              {e.criticals > 0 && (
                <>
                  {' · '}
                  <span className="text-status-error">{e.criticals} critical</span>
                </>
              )}
              {e.remediated > 0 && <> · {e.remediated.toLocaleString()} remediated</>}
              {e.expired > 0 && <> · {e.expired.toLocaleString()} expired</>}
              {more > 0 && <> · +{more} not shown</>}
            </span>
          </p>

          {/* Degraded capabilities as the mock's actionable amber banner. */}
          {bannerCaps.map((c) => (
            <div
              key={c.id}
              className="flex items-center gap-2.5 flex-wrap px-3 py-2 rounded-lg bg-status-warn-dim border border-status-warn/25 text-[12.5px]"
            >
              <span className="text-[9px] font-mono font-semibold uppercase tracking-[0.14em] text-status-warn">
                {CAPABILITY_LABELS[c.id] ?? c.id}
              </span>
              <span className="text-kb-text-secondary">
                {c.id === 'autopilot' ? (
                  <>
                    <b className="text-kb-text-primary">Did not intervene:</b> {c.reason || 'not running'}. It
                    detected, but nobody investigated or remediated.
                  </>
                ) : (
                  <>{c.reason}</>
                )}
              </span>
              <span className="flex-1" />
              <Link
                to={CAP_ACTIONS[c.id].to}
                className="shrink-0 px-2.5 py-1 rounded-md bg-status-warn text-white text-[11px] font-mono font-semibold hover:opacity-90 transition-opacity"
              >
                {CAP_ACTIONS[c.id].label}
              </Link>
            </div>
          ))}

          {/* Suppression counters — both layers, always countable. */}
          {(report.mutes.createdInWindow > 0 || report.rulesOff > 0) && (
            <p className="text-[11.5px] font-mono text-kb-text-tertiary">
              {report.mutes.createdInWindow > 0 && (
                <>
                  {report.mutes.createdInWindow} finding{report.mutes.createdInWindow === 1 ? '' : 's'} silenced in
                  this window ·{' '}
                  <Link to="/admin/insights?tab=silenced" className="text-status-info hover:underline">
                    view
                  </Link>
                </>
              )}
              {report.mutes.createdInWindow > 0 && report.rulesOff > 0 && <>&nbsp;&nbsp;&nbsp;</>}
              {report.rulesOff > 0 && (
                <>
                  {report.rulesOff} rule{report.rulesOff === 1 ? '' : 's'} off by policy ·{' '}
                  <Link to="/admin/insights?tab=silenced" className="text-status-info hover:underline">
                    view
                  </Link>
                </>
              )}
            </p>
          )}
        </div>
      )}

      {/* Coverage line — the report says what it can and cannot see. */}
      <div className="mt-3 pt-2.5 border-t border-dashed border-kb-border flex items-center gap-2 flex-wrap text-[11px] font-mono text-kb-text-tertiary">
        <span>
          {report.truncated
            ? 'coverage: clipped to the last 30 days — older activity may have aged out of your retention'
            : report.firstShift
              ? 'coverage: first shift — the last 24h'
              : 'coverage: full window since your last visit'}
          {ingestTrouble && (
            <>
              {' · '}
              <span className="text-status-warn font-semibold">ingest truncated by plan limits</span>
            </>
          )}
        </span>
        <span className="flex-1" />
        <Link
          to="/insights?view=history"
          className="text-kb-text-secondary underline underline-offset-2 hover:text-kb-text-primary shrink-0"
        >
          open full history →
        </Link>
      </div>
    </div>
  )
}
