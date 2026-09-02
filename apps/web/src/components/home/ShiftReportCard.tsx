import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { RotateCw, X } from 'lucide-react'
import { api, type OperationalBurst, type ShiftReport } from '@/services/api'
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

function hhmm(iso: string): string {
  return new Date(iso).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

// burstPhrases — the mock's sentence as typed segments: «A node rotation at
// 05:51 across gke-orquestador and gke-procesamiento hit 46 workloads.
// Everything recovered by 06:40» (or «N still down»).
export function burstPhrases(b: OperationalBurst, names: Record<string, string>): Phrase[] {
  const named = b.clusters.map((uid) => names[uid]).filter(Boolean)
  const where =
    named.length > 0
      ? named.join(' and ')
      : `${b.clusters.length} cluster${b.clusters.length === 1 ? '' : 's'}`
  const out: Phrase[] = [
    { t: KIND_OPENERS[b.kind] },
    { t: ' at ' },
    { t: hhmm(b.windowFrom), tone: 'time' },
    { t: ' across ' },
    { t: where, tone: 'name' },
    { t: ' hit ' },
    { t: `${b.blast.affected} workload${b.blast.affected === 1 ? '' : 's'}`, tone: 'bad' },
    { t: '. ' },
  ]
  if (b.blast.stillFiring === 0) {
    out.push({ t: 'Everything recovered by ' }, { t: hhmm(b.windowTo), tone: 'time' }, { t: '.' })
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

export function ShiftReportSection({
  report,
  dismissed,
  dismiss,
}: {
  report: ShiftReport | null
  dismissed: boolean
  dismiss: () => void
}) {
  if (!report || dismissed) return null
  const quiet = isQuietShift(report)
  const e = report.episodes
  const names = report.clusterNames ?? {}
  const windowLabel = report.firstShift
    ? 'your first shift — the last 24h'
    : `${new Date(report.windowFrom).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })} → ${new Date(report.windowTo).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })} · since your last visit`

  const ingestTrouble = report.capabilities.some(
    (c) => (c.id === 'ingest_caps' || c.id === 'active_series') && c.status === 'degraded',
  )
  const bannerCaps = report.capabilities.filter((c) => c.id in CAP_ACTIONS && c.status !== 'near')

  return (
    <div className="relative bg-kb-card border-t border-kb-border px-5 py-4">
      {/* Header row */}
      <div className="flex items-baseline gap-3 flex-wrap mb-2">
        <h2 className="text-[15px] font-bold text-kb-text-primary">While you were away</h2>
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

      {quiet ? (
        <p className="text-[12.5px] text-kb-text-secondary">
          <span className="font-medium text-status-ok">All quiet</span> — nothing opened, resolved
          or expired, and no capability changed.
        </p>
      ) : (
        <div className="space-y-2.5">
          {/* Narrative: one sentence per burst, the straggler linked. */}
          {report.bursts.length > 0 && (
            <p className="text-[13px] text-kb-text-secondary leading-relaxed max-w-[72ch]">
              {report.bursts.map((b, bi) => (
                <span key={b.id}>
                  {bi > 0 && ' '}
                  {burstPhrases(b, names).map(toneSpan)}
                </span>
              ))}
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

          {/* Counted summary — the mock's «169 se resolvieron solos» line. */}
          <p className="text-[13px] font-mono">
            <span className="text-status-ok font-semibold">
              {e.autoRecovered.toLocaleString()} finding{e.autoRecovered === 1 ? '' : 's'} resolved on their own.
            </span>{' '}
            <span className="text-kb-text-secondary">
              {e.opened.toLocaleString()} new
              {e.stillFiring > 0 && (
                <>
                  , <span className="text-status-error font-semibold">{e.stillFiring} still open</span>
                </>
              )}
              {e.criticals > 0 && (
                <>
                  , <span className="text-status-error font-semibold">{e.criticals} critical</span>
                </>
              )}
              {e.remediated > 0 && <>, {e.remediated.toLocaleString()} remediated</>}
              {e.expired > 0 && <>, {e.expired.toLocaleString()} expired</>}.
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
