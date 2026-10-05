import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router-dom'
import {
  DollarSign,
  AlertTriangle,
  ChevronRight,
  Lock,
  Plus,
  ShieldAlert,
  ShieldCheck,
  Sparkles,
} from 'lucide-react'
import { KobiSigilIcon } from '@/components/kobi'
import { ShiftReportChip, ShiftReportSection, useShiftReport } from '@/components/home/ShiftReportCard'
import { api } from '@/services/api'
import { useFleetRollup } from '@/hooks/useFleetRollup'
import { usePlan, type PlanTier } from '@/hooks/usePlan'
import { HomeKpis } from '@/components/home/HomeKpis'
import { FleetBreakdown } from '@/components/home/FleetBreakdown'
import { useCopilot } from '@/contexts/CopilotContext'
import type { ConversationSummary } from '@/services/copilot/types'

// HomePage — the plan-aware landing (design/kubebolt-home-{free,team,…}.html).
//
// Until now the app dropped you straight into one cluster's overview, which
// answers "how is THIS cluster?" — a question you can only ask once you already
// know which cluster to look at. Home answers the one before it: "what needs me
// this morning, anywhere?"
//
// The plan model is a SUPERSET, not four different pages: every tier renders
// the same panels, and the ones your tier doesn't include are shown as a
// teaser under a veil rather than hidden. That is the whole point — a Free user
// must be able to SEE what Team buys, or the upgrade is invisible. It is also
// where "Upgrade to Team" lives, which is why Home is what makes the tier
// legible at all: Stripe can charge without it, but nothing on screen would
// explain what the charge was for.
//
// usePlan fails open (see its header): an ungated build — OSS, self-hosted —
// unlocks everything, so no veils are painted over features the operator
// already owns.
//
// OSS edition of the page: same anatomy and data, minus the panels whose
// backend is Enterprise-only — Autopilot (incidents / approvals), the plan
// headroom card and the team lens. The Enterprise build adds them back in
// place; nothing else on the page differs.

function money(v: number | null): string {
  if (v === null) return '—'
  return `$${Math.round(v).toLocaleString()}`
}

// KobiPanel — recent conversations plus a box that actually asks.
//
// The mockup drew an input and the nav doc said to degrade it to "pick a
// cluster", because the chat endpoint lives behind the connector guard. Both
// are right in their own frame; what settles it is that the box can send
// through the copilot context, which already carries the active cluster. So it
// asks, and the placeholder says where the question is going.
//
// Kobi is ONE agent wearing two faces — Copilot when you ask, Autopilot when it
// acts — so both panels here carry the Kobi sigil rather than stock glyphs.
// The chat face is the plain mark; Autopilot's has the dot in orbit. A
// lucide Sparkles/Bot pair said "generic AI feature" about the two things that
// are the product's whole argument.
function KobiPanel({ conversations }: { conversations: ConversationSummary[] }) {
  const { openPanel, sendMessage, resumeConversation } = useCopilot()
  const [draft, setDraft] = useState('')

  const submit = () => {
    const q = draft.trim()
    if (!q) return
    openPanel()
    void sendMessage(q)
    setDraft('')
  }

  return (
    <div className="bg-kb-card border border-kb-border rounded-xl p-4 flex flex-col">
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-sm font-semibold text-kb-text-primary flex items-center gap-2">
          <span className="w-5 h-5 rounded-md bg-kb-accent-light flex items-center justify-center text-kb-accent">
            <KobiSigilIcon className="w-3.5 h-3.5" />
          </span>
          Kobi
        </h2>
        <button
          type="button"
          onClick={openPanel}
          className="text-[11px] font-mono text-kb-accent hover:underline"
        >
          Open →
        </button>
      </div>

      <div className="flex-1 space-y-0.5">
        {conversations.length === 0 ? (
          <p className="text-[11px] text-kb-text-tertiary py-2">
            No conversations yet. Ask about a workload, a spike, or why something restarted.
          </p>
        ) : (
          conversations.map((c) => (
            <button
              key={c.id}
              type="button"
              // resumeConversation, not openPanel: the row NAMES a conversation,
              // so opening the panel on whatever session happened to be current
              // is a link that lies. The context already loads a past
              // conversation by id and opens the panel as one action.
              onClick={() => void resumeConversation(c.id)}
              title={c.preview || c.title || 'Open this conversation'}
              className="w-full text-left flex items-center gap-2.5 py-1.5 hover:bg-kb-card-hover rounded-md px-1 -mx-1 transition-colors group"
            >
              <span className="w-1.5 h-1.5 rounded-full bg-kb-accent/50 shrink-0" />
              <span className="min-w-0 flex-1 text-[12px] text-kb-text-secondary truncate group-hover:text-kb-text-primary transition-colors">
                {c.title || c.preview || 'Untitled'}
              </span>
              <span className="shrink-0 text-[10px] font-mono text-kb-text-tertiary">
                {ago(c.updatedAt)}
              </span>
            </button>
          ))
        )}
      </div>

      <div className="mt-3 flex items-center gap-2 bg-kb-elevated border border-kb-border rounded-lg px-3 py-2">
        <KobiSigilIcon className="w-3.5 h-3.5 text-kb-text-tertiary shrink-0" />
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && submit()}
          placeholder="Ask Kobi about this cluster…"
          className="flex-1 bg-transparent border-none outline-none text-xs text-kb-text-primary placeholder:text-kb-text-tertiary"
        />
        <button
          type="button"
          onClick={submit}
          className="text-[11px] font-mono text-kb-accent shrink-0 disabled:opacity-40"
          disabled={!draft.trim()}
        >
          ↵
        </button>
      </div>
    </div>
  )
}

const INCIDENT_DOT: Record<string, string> = {
  critical: 'bg-status-error',
  warning: 'bg-status-warn',
  info: 'bg-status-info',
}

// ago renders a compact age. Home shows several kinds of timestamp and they must
// read the same way, so it lives here rather than in each panel.
function ago(iso?: string): string {
  if (!iso) return '—'
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (secs < 60) return `${Math.floor(secs)}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`
  return `${Math.floor(secs / 86400)}d`
}

type AttentionItem = {
  sev: 'crit' | 'warn'
  text: string
  hint: string
  where: string
  to: string
}

// AttentionPanel — the answer to "what needs me?", which is the whole reason
// Home exists as something other than Fleet.
//
// The empty state is deliberately a statement and not a blank: "nothing needs
// you" is the single most valuable thing this page can say, and rendering
// nothing would read as a page that failed to load.
function AttentionPanel({ items, tone }: { items: AttentionItem[]; tone: HomeTone }) {
  const tint = TONE_TINT[tone]
  return (
    // The panel keeps the app's ordinary card frame in every state. Colour
    // enters ONLY through the small icon chip and the per-row rails — the rows
    // are the thing you act on, so that is where the eye should be pulled, not
    // to a red-rimmed box that makes the whole page look on fire.
    <div className="border border-kb-border rounded-xl overflow-hidden bg-kb-card">
      <div className="px-4 py-3 border-b border-kb-border flex items-center justify-between">
        <h2 className="text-sm font-semibold text-kb-text-primary flex items-center gap-2">
          <span
            className="w-5 h-5 rounded-md flex items-center justify-center"
            // `tint` in both states — an empty list already resolves the tone to
            // 'ok', so the branch only ever picked a SECOND green, and the
            // brand accent it picked disagreed in light mode with the
            // status-ok shield rendered directly below it in the same panel.
            style={{
              color: tint,
              background: `color-mix(in srgb, ${tint} 12%, transparent)`,
            }}
          >
            {items.length > 0 ? (
              <AlertTriangle className="w-3 h-3" />
            ) : (
              <ShieldCheck className="w-3 h-3" />
            )}
          </span>
          Needs your attention
        </h2>
        {items.length > 0 && (
          <span className="text-[11px] font-mono text-kb-text-tertiary">
            {items.length} {items.length === 1 ? 'thing' : 'things'} · worst first
          </span>
        )}
      </div>
      {items.length === 0 ? (
        <div className="px-4 py-8 text-center">
          <ShieldCheck className="w-7 h-7 text-status-ok mx-auto mb-2" />
          <div className="text-xs text-kb-text-primary">Nothing needs you right now</div>
          <div className="mt-1 text-[11px] text-kb-text-tertiary">
            Every cluster is reporting and no critical findings are open.
          </div>
        </div>
      ) : (
        <div className="divide-y divide-kb-border">
          {items.map((it, i) => (
            <Link
              key={`${it.to}-${i}`}
              to={it.to}
              // Per-row severity rail: the list is sorted worst-first, and the
              // rail lets the eye confirm that without reading a single word.
              className={`relative flex items-center gap-3 px-4 py-2.5 pl-5 hover:bg-kb-card-hover transition-colors border-l-2 ${
                it.sev === 'crit' ? 'border-status-error' : 'border-status-warn'
              }`}
            >
              <span
                className={`w-1.5 h-1.5 rounded-full shrink-0 ${
                  it.sev === 'crit' ? 'bg-status-error' : 'bg-status-warn'
                }`}
              />
              <span className="min-w-0 flex-1 text-xs text-kb-text-primary truncate">
                {it.text}{' '}
                <span className="text-[11px] font-mono text-kb-text-tertiary">— {it.hint}</span>
              </span>
              <span className="shrink-0 text-[11px] font-mono text-kb-text-tertiary">{it.where}</span>
              <ChevronRight className="w-3.5 h-3.5 shrink-0 text-kb-text-tertiary" />
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}

function Panel({
  title,
  icon,
  cta,
  ctaTo,
  badge,
  children,
  className = '',
}: {
  title: string
  icon: React.ReactNode
  cta?: string
  ctaTo?: string
  // Small pill after the title. Used for "Limited time" — a capability that is
  // open to a tier it doesn't belong to yet. Worded that way and never "Free":
  // free reads permanent, and taking back what a user believes is theirs turns
  // a plan change into a grievance.
  badge?: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={`bg-kb-card border border-kb-border rounded-xl p-4 ${className}`}>
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-sm font-semibold text-kb-text-primary flex items-center gap-2">
          <span className="w-5 h-5 rounded-md bg-kb-accent-light flex items-center justify-center text-kb-accent">
            {icon}
          </span>
          {title}
          {badge && (
            <span className="inline-flex items-center px-2 py-0.5 rounded-full bg-kb-accent-light text-kb-accent text-[9px] font-mono font-semibold uppercase tracking-[0.08em] shrink-0">
              {badge}
            </span>
          )}
        </h2>
        {cta && ctaTo && (
          <Link to={ctaTo} className="text-[11px] font-mono text-kb-accent hover:underline">
            {cta} →
          </Link>
        )}
      </div>
      {children}
    </div>
  )
}

// LockedPanel renders the real panel content BLURRED under a veil — the teaser
// pattern from the mockups. Showing a plausible shape beats an empty box: the
// user learns what the feature looks like, not just that it exists.
function LockedPanel({
  title,
  requires,
  blurb,
  teaser,
  className = '',
}: {
  title: string
  requires: string
  blurb: string
  teaser: React.ReactNode
  className?: string
}) {
  const navigate = useNavigate()
  return (
    <div
      className={`relative bg-kb-card border border-kb-border rounded-xl p-4 overflow-hidden ${className}`}
    >
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-sm font-semibold text-kb-text-primary">{title}</h2>
      </div>
      <div className="blur-[3px] select-none pointer-events-none opacity-60" aria-hidden="true">
        {teaser}
      </div>
      <div className="absolute inset-0 flex flex-col items-center justify-center text-center px-6 bg-kb-card/70">
        <Lock className="w-5 h-5 text-kb-accent mb-2" />
        <div className="text-sm font-semibold text-kb-text-primary">{blurb}</div>
        <button
          type="button"
          // `/account`, not `/account/plan`. The latter is the BACKEND endpoint;
          // as a frontend route it does not exist, so both upgrade CTAs on this
          // page landed on a blank screen (N-3). The one place a user is most
          // decided to pay is the worst place to send them nowhere.
          onClick={() => navigate('/account')}
          className="mt-3 px-3 py-1.5 rounded-lg bg-kb-accent text-white text-[11px] font-mono hover:opacity-90 transition-opacity"
        >
          Upgrade to {requires} →
        </button>
      </div>
    </div>
  )
}

function SecurityTile({ n, label, tone }: { n: number; label: string; tone: string }) {
  return (
    <div className="bg-kb-elevated border border-kb-border rounded-lg py-2.5 text-center">
      <div className={`text-lg font-semibold tabular-nums ${tone || 'text-kb-text-primary'}`}>{n}</div>
      <div className="text-[9px] font-mono uppercase tracking-wider text-kb-text-tertiary mt-0.5">
        {label}
      </div>
    </div>
  )
}

function TeaserRow({ left, right }: { left: string; right: string }) {
  return (
    <div className="flex items-center justify-between py-1.5 border-b border-kb-border last:border-0 text-[11px]">
      <span className="text-kb-text-secondary">{left}</span>
      <span className="font-mono text-kb-text-tertiary">{right}</span>
    </div>
  )
}

// greeting / today read the BROWSER's clock, so they follow whoever is looking
// rather than the server's timezone.
//
// Two things were wrong. The night band didn't exist — anything before noon was
// "Good morning", so someone on a 3am page tripped over a cheerful sunrise
// greeting; 23:00–04:59 now reads as the late shift it is. And both were
// computed once at mount: a tab left open across noon kept wishing you good
// morning all afternoon, and across midnight it printed yesterday's date. The
// minute ticker below re-renders them.
function greeting(now: Date): string {
  const h = now.getHours()
  if (h >= 23 || h < 5) return 'Working late'
  if (h < 12) return 'Good morning'
  if (h < 19) return 'Good afternoon'
  return 'Good evening'
}

// displayFirstName — el nombre de pila que va en el saludo.
//
// Sale de partir el identificador de acceso, que es un email o un usuario, así
// que llega en minúscula: "Good morning, leafar". No hay campo de nombre real
// en el perfil (`AuthUser` sólo trae `username`), de modo que capitalizarlo es
// lo mejor disponible mientras no exista.
//
// Sólo la PRIMERA letra. Nada de `toLowerCase()` en el resto: quien se registre
// como `JMartinez` o `McCarthy` no debe quedar convertido en `Jmartinez` ni
// `Mccarthy` por una decisión de estilo del saludo.
//
// Recorre por puntos de código y no por `charAt`, que parte los caracteres
// fuera del plano básico por la mitad y deja medio símbolo en pantalla.
//
// Los separadores son los de siempre más `_`, que ningún nombre de pila lleva.
// El guion NO se añade a propósito: partiría `jean-luc` en "Jean", y Jean-Luc o
// Anne-Marie son un solo nombre. Capitalizar no es excusa para cambiar de paso
// cómo se recorta.
function displayFirstName(username?: string): string {
  const first = (username ?? '').split(/[@.\s_]/)[0]
  if (!first) return ''
  const [head, ...rest] = [...first]
  return head.toUpperCase() + rest.join('')
}

function today(now: Date): string {
  return now.toLocaleDateString(undefined, {
    weekday: 'long',
    day: 'numeric',
    month: 'long',
  })
}

// useNow — a clock that ticks once a minute. Aligned to the next whole minute
// so the flip happens ON the hour boundary rather than up to 59s after it.
function useNow(): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    let interval: ReturnType<typeof setInterval> | undefined
    const align = setTimeout(
      () => {
        setNow(new Date())
        interval = setInterval(() => setNow(new Date()), 60_000)
      },
      (60 - new Date().getSeconds()) * 1000,
    )
    return () => {
      clearTimeout(align)
      if (interval) clearInterval(interval)
    }
  }, [])
  return now
}

// ─── The greeting card ──────────────────────────────────────────────────────
//
// The same surface as the KPI row under it (`.kb-panel`, the site's card), not
// a zone washed in the fleet's state. The state used to tint the whole block —
// glow, surface, border, hairline, tile and eyebrow — and with one critical
// open the landing page opened as a red room. It now lives in the verdict pill
// and in the rows of «While you were away», where it points at something.

type HomeTone = 'ok' | 'warn' | 'crit'

// The status palette, not the brand accent: `ok` in particular cannot come
// from --kb-accent, which resolves to a darker green in light mode and made
// Home's "all clear" disagree with the same state everywhere else.
const TONE_TINT: Record<HomeTone, string> = {
  ok: '#22d68a',
  warn: '#f5a623',
  crit: '#ef4056',
}

// The verdict pill. Only the critical one pulses: an animation that plays on
// a healthy fleet is decoration, and once it's decoration nobody reads it on
// the morning it isn't.
function VerdictPill({ tone, count }: { tone: HomeTone; count: number }) {
  const label =
    tone === 'ok' ? 'All clear' : `${count} ${count === 1 ? 'thing needs you' : 'things need you'}`
  const tint = TONE_TINT[tone]
  return (
    // Dot + dim fill + no border — the same status-pill grammar KpiCards uses
    // (bg-status-*-dim is 12% alpha, which is what the color-mix reproduces for
    // a tint that has to be picked at runtime). The border this carried was the
    // giveaway that it wasn't one of the app's pills.
    <span
      className="inline-flex items-center gap-2 rounded-full px-2.5 py-1 text-[10px] font-mono uppercase tracking-[0.08em]"
      style={{ color: tint, background: `color-mix(in srgb, ${tint} 12%, transparent)` }}
    >
      <span className="w-1.5 h-1.5 rounded-full" style={{ background: tint }} />
      {label}
    </span>
  )
}


export function HomePage() {
  const plan = usePlan()
  const { data: allClusters = [] } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters })
  const { data: me } = useQuery({ queryKey: ['me'], queryFn: api.getMe, retry: false })

  // ONE hook call feeds both halves of the shift report (the chip in the
  // greeting's chip row and the section inside the card) so dismissal stays
  // in sync. The presence beacon fires inside the hook AFTER the report is
  // read — marking first would collapse the window to seconds.
  const shift = useShiftReport()

  const paid = plan.atLeast('team' as PlanTier)

  // What Free actually includes, which is NOT "everything behind one veil".
  //
  //   · Pods / nodes — Free. The agreed Free mockup puts the fleet pod count in
  //     its KPI row; the number needs an agent, not a subscription.
  //   · Vulnerabilities + exposed secrets — Free, by the 2026-08-05 pricing
  //     decision (annotation ⑤ of the mockup): "recortar lentes o severidades
  //     en seguridad es vender ceguera".
  //   · Cost (OpenCost) — Free FOR NOW, an acquisition hook like Autopilot.
  //
  // This slice shipped gating all of it, so a Free org's Home printed an
  // em-dash for pods and hid a security posture it is entitled to see. Worse,
  // `critical` still rendered 83 because the findings queryKey is shared with
  // the Security page: visit Security first and the cache fills the card the
  // page claims is locked. Nothing on the server gates any of this — the fence
  // was UI-only, and in the wrong place.
  //
  // LIMITED_TIME is the seam, and it is a CONSTANT rather than an inlined
  // `true` on purpose — mockup annotation ⑥ on Autopilot says it in as many
  // words: build the thing behind the gate from the first commit, so the day it
  // moves is "config, not rebuild". Flip these to `paid` and the veils, the
  // locked card and the teaser all come back on their own. The chips are worded
  // "limited time", never "free": free reads permanent, and taking back
  // something a user believes is theirs is how a plan change becomes a
  // grievance.
  const LIMITED_TIME_IN_FREE = true
  const canSeeSecurity = true
  const canSeeCost = paid || LIMITED_TIME_IN_FREE
  // Only worth saying on the tier that isn't paying for it.
  const costIsPromo = !paid && canSeeCost

  // OSS is single-tenant: no team lens, the fleet is every cluster the backend
  // lists — the same set Fleet and the switcher show.
  const clusters = allClusters
  const visibleIds = clusters.map((c) => c.clusterId).filter((id): id is string => !!id)

  // Totals must cover the SAME clusters as the page says it is describing. The
  // roll-up is org-scoped server-side, so the ids go down and the sums are
  // folded from the per-cluster rows.
  // La retención del plan fija el horizonte del delta de coste: con 15 días
  // guardados no se puede comparar contra hace 30. Ver deltaWindowDays.
  const rollup = useFleetRollup(clusters.length > 0, visibleIds, plan.retentionDays)

  const { data: findings } = useQuery({
    queryKey: ['findings', '', '', ''],
    queryFn: () => api.listFindings(),
    enabled: canSeeSecurity,
    retry: false,
  })

  const healthy = clusters.filter((c) => c.status === 'connected' || c.agentConnected).length
  const attention = clusters.length - healthy

  // Findings fold the same way. The lens lives in localStorage and never travels
  // to the server, and a summary cannot be recomputed from one page of rows —
  // so the API ships a per-cluster tally and the narrowing happens here. With
  // no lens active the fold covers every cluster and equals the org total.
  const perCluster = findings?.bySeverityCluster
  const foldSeverity = (sev: string): number => {
    if (!perCluster) return findings?.bySeverity?.[sev] ?? 0
    return visibleIds.reduce((n, id) => n + (perCluster[id]?.[sev] ?? 0), 0)
  }
  const critical = foldSeverity('critical')
  const high = foldSeverity('high')
  // Configuration / compliance is the Team half of the security pillar, and
  // rides the same limited-time opening as the rest. The locked tile below
  // stays wired so flipping the flag restores it without a rebuild.
  const cisFailing =
    paid || LIMITED_TIME_IN_FREE ? (findings?.byKind?.misconfig ?? 0) : null

  // Sólo lo accionable, ordenado por gravedad. Una fila que no pide nada del
  // lector no pertenece aquí — para "qué tengo" está Fleet.
  const attention_items: AttentionItem[] = []
  for (const c of clusters) {
    const name = c.displayName || c.name
    if (c.status === 'error') {
      attention_items.push({
        sev: 'crit',
        text: `${name} is unreachable`,
        hint: 'no metrics or findings are arriving from it',
        where: name,
        to: '/fleet',
      })
    } else if (!(c.status === 'connected' || c.agentConnected) && (c.lastSeen || c.source === 'agent-proxy')) {
      // …y sólo si LLEGÓ a estar conectado. Un kubeconfig de trabajo trae
      // docenas de contextos a los que uno simplemente tiene acceso; contarlos
      // aquí llenaba la lista de «no agent connected» —doce filas en la flota
      // real del operador— y ahogaba las dos que pedían algo.
      // `lastSeen` viene de la fila de membresía durable, así que existe
      // exactamente cuando algún agente contactó alguna vez: es la diferencia
      // entre «se cayó» e «inventario».
      attention_items.push({
        sev: 'warn',
        text: `${name} has no agent connected`,
        hint: 'connect one to get metrics and security findings',
        where: name,
        to: '/fleet',
      })
      // No longer gated on the plan: the roll-up now runs for every tier, so a
      // Free org gets told its agent is up but silent — which is exactly the
      // kind of thing a landing page exists to catch.
    } else if (c.clusterId && rollup.byCluster[c.clusterId]?.pods == null) {
      attention_items.push({
        sev: 'warn',
        text: `${name} is not reporting metrics`,
        hint: 'the agent is up but no samples are arriving',
        where: name,
        to: '/fleet',
      })
    }
  }
  if (critical > 0) {
    attention_items.push({
      sev: 'crit',
      text: `${critical} critical ${critical === 1 ? 'finding' : 'findings'}`,
      hint: high > 0 ? `and ${high} high` : 'across the fleet',
      where: 'Security',
      to: '/security',
    })
  }
  attention_items.sort((a, b) => (a.sev === b.sev ? 0 : a.sev === 'crit' ? -1 : 1))

  // Kobi and Autopilot are the two things this product does that a dashboard
  // does not, and Home showed neither. Both degrade quietly: a missing history
  // renders an invitation, and an Autopilot that is not deployed renders
  // nothing at all rather than an error the reader cannot act on.
  const { data: conversations = [] } = useQuery({
    queryKey: ['home-conversations'],
    queryFn: () => api.listConversations({ limit: 3 }),
    retry: false,
    staleTime: 60_000,
  })
  // Estado REAL de cada cluster. Misma queryKey que Fleet —una petición para
  // las dos— y mismo origen que la salud del Overview.
  const { data: insightSummary } = useQuery({
    queryKey: ['insights-summary'],
    queryFn: api.getInsightsSummary,
    refetchInterval: 60_000,
    retry: false,
  })

  const firstName = displayFirstName(me?.username)
  const now = useNow()
  // The page's tone: the worst thing on it. Nothing pending → calm.
  const tone: HomeTone = attention_items.some((i) => i.sev === 'crit')
    ? 'crit'
    : attention_items.length > 0
      ? 'warn'
      : 'ok'

  return (
    // No page padding of its own — <main> already applies p-5 for every route,
    // and the extra p-6 here made Home sit visibly further from the sidebar
    // than the cluster tabs it sends you to. Gaps match the dashboards' gap-3
    // for the same reason: the landing page cannot be the one that measures
    // differently.
    <div className="space-y-4">
      <header className="kb-panel overflow-hidden">
        <div className="px-5 pt-4 pb-3 flex items-start justify-between gap-4 flex-wrap">
          <div className="min-w-0">
            <div className="text-[10.5px] font-mono uppercase tracking-[0.16em] text-kb-text-tertiary">
              {today(now)}
            </div>
            {/* DM Sans, not `font-display`: the display face is reserved for
                Kobi's brand titles, and Home is not a Kobi surface. */}
            <h1 className="mt-1.5 text-[26px] font-semibold text-kb-text-primary leading-none tracking-[-0.02em]">
              {greeting(now)}
              {firstName ? `, ${firstName}` : ''}
            </h1>
            <p className="text-[11px] font-mono text-kb-text-tertiary mt-2">
              {clusters.length} {clusters.length === 1 ? 'cluster' : 'clusters'} ·{' '}
              {attention === 0 ? 'all reporting' : `${attention} need attention`}
            </p>
          </div>
          <div className="flex items-center gap-2 flex-wrap justify-end">
            {/* Dismissed report → the «⟳ while you were away» chip lives HERE,
                in the greeting's own chip row (the design's placement). */}
            <ShiftReportChip report={shift.report} dismissed={shift.dismissed} reopen={shift.reopen} />
            <VerdictPill tone={tone} count={attention_items.length} />
            {plan.label && (
              <Link
                to="/account"
                className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full bg-kb-accent-light text-kb-accent text-[10px] font-mono uppercase tracking-[0.08em] hover:opacity-80 transition-opacity"
              >
                <Sparkles className="w-3 h-3" />
                {plan.label}
              </Link>
            )}
            {/* Obligatorio, no decorativo: una org recién creada aterriza AQUÍ con
                cero clusters, y sin este botón la primera pantalla del producto no
                tiene salida. Fleet lo lleva también; los dos son puertas válidas. */}
            <Link
              to="/fleet"
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-full bg-kb-accent text-white text-[11px] font-mono font-semibold hover:bg-kb-accent/90 transition-colors"
            >
              <Plus className="w-3 h-3" />
              Connect cluster
            </Link>
          </div>
        </div>

        {/* El parte de guardia (Fase 3): la sección inferior de ESTA misma
            caja — el saludo se expande con «mientras no estabas» (diseño
            Home), no con una tarjeta suelta debajo. */}
        {shift.report && !shift.dismissed && <div className="mx-5 border-t border-kb-border" aria-hidden />}
        <ShiftReportSection
          report={shift.report}
          dismissed={shift.dismissed}
          dismiss={shift.dismiss}
          visibleIds={visibleIds}
        />
      </header>

      {/* The headline row in the site's card anatomy (HomeKpis → KpiCard), the
          same family as the cluster Overview's row. Colour lives in each card's
          minichart — which clusters need you, how much spend is idle, the
          critical share — and a card lights up only when its number asks for
          someone. */}
      <HomeKpis
        clusters={clusters}
        visibleIds={visibleIds}
        rollup={rollup}
        critical={critical}
        high={high}
        canSeeCost={canSeeCost}
        costIsPromo={costIsPromo}
      />

      {/* LA página. Home responde "¿qué necesita algo de mí?"; Fleet responde
          "¿qué tengo?" — y hasta ahora Home repetía las tarjetas de cluster de
          Fleet, que es lo que hacía que las dos pantallas se sintieran la misma
          (D16 del doc de navegación).

          Una lista priorizada además ESCALA: con 2 clusters las tarjetas caben,
          con 10 la página deja de aterrizar y se vuelve scroll. Y cada fila
          enlaza a un destino DISTINTO — el cluster, Security, el plan — que es
          lo que la convierte en un aterrizaje y no en un resumen. */}
      {/* La capacidad que aparece al subir a Team: qué clusters están peor y de
          quién son. Doble puerta a propósito.
          Por PLAN, porque es lo que el plan de pago compra —clusters ilimitados
          y equipos—; y por ESCALA, porque con un solo cluster el desglose sería
          la misma frase de la cabecera ocupando quince veces más sitio. Las dos
          condiciones son verdad a la vez en Free, así que allí no se pinta;
          pero un usuario Team con un cluster tampoco merece una lista de uno. */}
      {paid && clusters.length > 1 && (
        <FleetBreakdown
          clusters={clusters}
          byCluster={rollup.byCluster}
          bySeverityCluster={perCluster}
          insightsByCluster={insightSummary?.bySeverityCluster}
          teams={[]}
        />
      )}

      <AttentionPanel items={attention_items} tone={tone} />

      {/* Kobi alone on this row: Autopilot, its second face, is Enterprise-only
          and the EE build renders its panel beside this one. */}
      <KobiPanel conversations={conversations} />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
        {canSeeCost ? (
          <>
            <Panel
              title="Cost"
              icon={<DollarSign className="w-3 h-3" />}
              cta="Cost dashboard"
              ctaTo="/cost"
              badge={costIsPromo ? 'Limited time' : undefined}
            >
              {rollup.costAvailable ? (
                <div className="space-y-1.5">
                  {clusters
                    .filter((c) => c.clusterId && rollup.byCluster[c.clusterId]?.costMonthly != null)
                    .slice(0, 4)
                    .map((c) => {
                      const r = rollup.byCluster[c.clusterId!]
                      const pct =
                        rollup.fleetSpendMonthly && r.costMonthly
                          ? (r.costMonthly / rollup.fleetSpendMonthly) * 100
                          : 0
                      return (
                        <div key={c.context}>
                          <div className="flex items-center justify-between text-[11px]">
                            <span className="text-kb-text-secondary truncate">
                              {c.displayName || c.name}
                            </span>
                            <span className="font-mono tabular-nums text-kb-text-primary">
                              {money(r.costMonthly)}
                            </span>
                          </div>
                          <div className="h-1.5 bg-kb-elevated rounded-full mt-1 overflow-hidden">
                            {/* status-ok, NOT --kb-accent. The two greens agree in
                                dark mode and diverge in light (#22d68a vs the
                                accent's #009a54), so this bar read as a darker,
                                different green than the Cost breakdown's bars
                                sitting one click away. Same trap EfficiencyBand
                                documents: brand accent is for chrome — buttons,
                                links, chips — and any fill that represents DATA
                                takes the status palette so every bar in the
                                product is the same colour in both themes. */}
                            <div className="h-full bg-status-ok rounded-full" style={{ width: `${pct}%` }} />
                          </div>
                        </div>
                      )
                    })}
                </div>
              ) : (
                <p className="text-[11px] text-kb-text-tertiary py-4 text-center">
                  No cost data yet — install OpenCost on a cluster to see fleet spend.
                </p>
              )}
            </Panel>
          </>
        ) : (
          <LockedPanel
            title="Cost"
            requires="Team"
            blurb="See what your fleet costs"
            // Capabilities, not results. The veil used to read "data/postgres —
            // $842/mo · 12 critical CVEs", numbers invented on the two surfaces
            // where inventing one costs the most: a prospect who upgrades and
            // finds different figures learned that this product makes them up.
            teaser={
              <div>
                <TeaserRow left="Spend per cluster and namespace" right="OpenCost" />
                <TeaserRow left="Idle capacity you are paying for" right="run-rate" />
                <TeaserRow left="Rightsizing priced at your node rates" right="$/mo" />
              </div>
            }
          />
        )}

        {/* Security is NOT behind the veil. Vulnerabilities and exposed
            secrets ship in Free by the 2026-08-05 pricing decision — cutting
            lenses or severities in security is selling blindness. Team adds
            the Configuration and Compliance lenses, which is why the third
            tile is the only part of this panel that locks. */}
        <Panel
          title="Security"
          icon={<ShieldAlert className="w-3 h-3" />}
          cta="Security"
          ctaTo="/security"
          // No "limited time" chip on this panel, unlike Cost. Vulnerabilities
          // and exposed secrets are Free PERMANENTLY (pricing 2026-08-05); only
          // the Configuration tile inside is on loan. Badging the whole panel
          // would tell a Free user their CVE feed is about to be taken away,
          // which is the opposite of the decision.
        >
          <div className="grid grid-cols-3 gap-2">
            <SecurityTile n={critical} label="Critical" tone={critical > 0 ? 'text-status-error' : ''} />
            <SecurityTile n={high} label="High" tone={high > 0 ? 'text-status-warn' : ''} />
            {cisFailing == null ? (
              <Link
                to="/account"
                className="bg-kb-elevated border border-dashed border-kb-border rounded-lg py-2.5 text-center hover:border-kb-accent/40 transition-colors"
              >
                <Lock className="w-4 h-4 text-kb-text-tertiary mx-auto" />
                <div className="text-[9px] font-mono uppercase tracking-wider text-kb-text-tertiary mt-1">
                  Config · Team
                </div>
              </Link>
            ) : (
              <SecurityTile
                n={cisFailing}
                label="CIS failing"
                tone={cisFailing > 0 ? 'text-status-warn' : ''}
              />
            )}
          </div>
          {(findings?.newLast24h ?? 0) > 0 && (
            <p className="text-[10px] font-mono text-status-warn mt-2.5">
              ▲ {findings?.newLast24h} new in the last 24h
            </p>
          )}
        </Panel>

        {/* Business tier teaser — the next rung, shown to everyone below it and
            hidden once they own it.
            It used to require `paid`, which was fine while Free had a veil of
            its own. Now that Cost and Security are open on loan, a Free user
            would see no locked panel anywhere — and this page's stated job is
            to make the ladder legible. One rung above whatever you are on. */}
        {!plan.atLeast('business' as PlanTier) && (
          <LockedPanel
            className="lg:col-span-2"
            title="Lifecycle & FinOps"
            requires="Business"
            blurb="Let KubeBolt act on the savings it finds"
            // Same rule as the Cost veil: name the capability, never a saving.
            // "saves $340/mo" on a cluster nobody has measured is a quote, and
            // the first bill that disagrees with it is the last one they read.
            teaser={
              <div>
                <TeaserRow left="Apply rightsizing recommendations" right="one click" />
                <TeaserRow left="Scale environments on a schedule" right="off-hours" />
                <TeaserRow left="Reclaim orphaned volumes and load balancers" right="automatic" />
              </div>
            }
          />
        )}
      </div>
    </div>
  )
}
