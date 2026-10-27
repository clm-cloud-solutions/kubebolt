import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Ban, Check, Copy, Info, Lock, Pause, Plug } from 'lucide-react'
import { CloudProviderIcon } from '@/components/shared/CloudProviderIcon'
import { HoverTooltip, TooltipHeader, TooltipNote } from '@/components/shared/Tooltip'
import { UpgradeLink } from '@/components/shared/UpgradeLink'
import { api, planLabel, type McpConnector, type McpConnectorStatus } from '@/services/api'
import { connectorDisplayName } from '@/utils/toolName'
import { isConfigured, isOffered, isPlanLocked, lockedPlan, modeFor, relativeTime } from './connectorForm'

// Small pieces shared by the connectors page and the drawer, so a status
// reads the same on the card and in the panel it opens.

export const MCP_CONNECTORS_KEY = ['admin', 'mcp-connectors'] as const

// The server's statuses, plus one it has no word for: 'unoffered', a
// connector this install offers no way of signing in to.
export type ConnectorDisplayStatus = McpConnectorStatus | 'unoffered'

// The one status vocabulary. 'plan' is not a failure — the connector exists,
// the plan does not include it — so it is neutral with a lock, never red; nor
// is 'unoffered', which no action on this install changes.
// Green text is the accent pair, not status-ok: the 400-tier status green
// reads at under 2:1 on the light theme, and this badge is the one word that
// says whether the connector works. The 'plan' label is a fallback: the badge
// names the plan itself ("Team plan") whenever it knows it.
const STATUS: Record<ConnectorDisplayStatus, { label: string; cls: string; Icon: typeof Check }> = {
  enabled: { label: 'Enabled', cls: 'bg-kb-accent-light text-kb-accent', Icon: Check },
  paused: { label: 'Paused', cls: 'bg-status-warn-dim text-status-warn', Icon: Pause },
  error: { label: 'Needs attention', cls: 'bg-status-error-dim text-status-error', Icon: AlertTriangle },
  available: { label: 'Available', cls: 'bg-kb-elevated text-kb-text-tertiary', Icon: Plug },
  plan: { label: 'Not in your plan', cls: 'bg-kb-elevated text-kb-text-secondary', Icon: Lock },
  unoffered: { label: 'Not offered', cls: 'bg-kb-elevated text-kb-text-tertiary', Icon: Ban },
}

// Paused wins over a stored error: pausing is the natural answer to a
// connector that keeps failing, and it has to read as paused (with Resume)
// even when the error that prompted it is still on record.
export function effectiveStatus(c: McpConnector): ConnectorDisplayStatus {
  if (isPlanLocked(c)) return 'plan'
  if (!isOffered(c)) return unofferedStatus(c)
  if (isConfigured(c) && c.paused) return 'paused'
  return STATUS[c.status] ? c.status : 'available'
}

// A connector this install does not offer, read from the stored switches.
// The server's status is no guide here: 'plan' for an org below the plan
// (no plan turns it on), 'available' for one saved and never switched on.
// Switched on, it needs attention — Kobi can't use it, and removing it is
// what clears that; paused, it says so; anything else is simply not offered.
function unofferedStatus(c: McpConnector): ConnectorDisplayStatus {
  if (!isConfigured(c)) return 'unoffered'
  if (c.paused) return 'paused'
  if (c.enabled) return 'error'
  return 'unoffered'
}

// The stored sign-in method, by name. The backend lists only the modes this
// install offers, so one saved before the install stopped offering it (the
// server's own identity, where it no longer lends it) has no label of its own.
export function storedModeLabel(c: McpConnector): string {
  return modeFor(c, c.mode)?.label ?? "A method this installation doesn't offer"
}

export function ConnectorStatusBadge({ connector }: { connector: McpConnector }) {
  const s = STATUS[effectiveStatus(connector)]
  const plan = lockedPlan(connector)?.plan
  return (
    <span
      className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-mono font-semibold uppercase tracking-wider shrink-0 ${s.cls}`}
    >
      <s.Icon className="w-3 h-3" aria-hidden />
      {plan ? `${planLabel(plan)} plan` : s.label}
    </span>
  )
}

export interface PlanInvitation {
  // The plan it names, and the one on sale ('' when nothing on sale unlocks it).
  plan: string
  upgradeTo: string
  // Where "Contact us" leads when nothing is on sale: a safe address, or ''
  // when there is none — and then the invitation never says "Contact us".
  contactUrl: string
  // "Included from the Team plan": what the badge says, in words.
  headline: string
  // What the upgrade gets the org, in the connector's own name. '' when there
  // is nothing to buy and nowhere to write: the headline alone says which
  // plan has it ("Available on the Business plan").
  body: string
}

// What a plan-locked connector says on its card and in its drawer, so both
// read the same: the plan, then what upgrading lets Kobi do. Null where no
// upgrade turns it on (it is not locked, or this install does not offer it).
// contactUrl is the install's contact address (useUpgradeContactUrl): the
// words offer a conversation only when the CTA can link to one.
export function planInvitation(c: McpConnector, contactUrl?: string): PlanInvitation | null {
  const lock = lockedPlan(c)
  if (!lock) return null
  const gets = `let Kobi read your ${connectorDisplayName(c.id)} resources while it investigates.`
  const upgradeTo = lock.buyable ? lock.plan : ''
  if (upgradeTo) {
    return {
      plan: lock.plan,
      upgradeTo,
      contactUrl: '',
      headline: `Included from the ${planLabel(lock.plan)} plan`,
      body: `Upgrade to ${gets}`,
    }
  }
  const contact = safeContactUrl(contactUrl) ?? ''
  return {
    plan: lock.plan,
    upgradeTo,
    contactUrl: contact,
    headline: lock.plan ? `Available on the ${planLabel(lock.plan)} plan` : 'Not included in your plan',
    body: contact ? `Contact us to ${gets}` : '',
  }
}

// Only an https page or a mail address: the contact target is server config,
// and an href is not the place to find out it was something else — nor to
// send an org's upgrade conversation over plain http.
export function safeContactUrl(url: string | undefined): string | null {
  return url && /^(https:\/\/|mailto:)/i.test(url) ? url : null
}

// The address an org is sent to when nothing on sale unlocks a connector:
// KUBEBOLT_UPGRADE_URL, which the usage endpoint already carries for the
// credits card. Asked only when a card needs it; '' when there is none.
export function useUpgradeContactUrl(needed: boolean): string {
  const { data } = useQuery({
    queryKey: ['account', 'usage'],
    queryFn: api.getAccountUsage,
    enabled: needed,
    staleTime: 5 * 60_000,
    // No address is an answer, not a failure: the card draws no link.
    retry: false,
  })
  return safeContactUrl(data?.credits?.upgradeUrl) ?? ''
}

// The upgrade CTA's look, on the card and in the drawer footer. Text on the
// accent is white in light (the app's primary-button pair) and the page
// background in dark, where white on the bright accent reads at 1.76:1 and
// kb-bg at 11.2:1.
export const UPGRADE_CTA =
  'inline-flex items-center px-3.5 py-1.5 rounded-lg bg-kb-accent text-white dark:text-kb-bg text-xs font-semibold hover:opacity-90 transition-opacity'

// The one rule for a plan gate's action, wherever it is drawn (the locked
// card and drawer, the drawer's 409 note, a test refused by the plan):
// "Upgrade to <plan>" into Billing only when a plan on sale unlocks it,
// otherwise "Contact us" at the install's address, otherwise nothing — never
// a Billing link to a plan that cannot be bought there.
export function PlanGateLink({
  upgradeTo,
  contactUrl,
  className,
}: {
  upgradeTo?: string | null
  contactUrl?: string
  className?: string
}) {
  if (upgradeTo) return <UpgradeLink url="" plan={upgradeTo} className={className} />
  const url = safeContactUrl(contactUrl)
  return url ? <UpgradeLink url={url} label="Contact us" className={className} /> : null
}

// The one action a locked connector offers, on the card and in the drawer:
// "Upgrade to <plan>" into Billing, or "Contact us" when nothing is on sale.
// Nothing at all when there is nowhere to send the org — no dead button.
export function PlanUpgradeCta({
  connector,
  contactUrl,
  className,
}: {
  connector: McpConnector
  contactUrl?: string
  className?: string
}) {
  const invite = planInvitation(connector, contactUrl)
  if (!invite) return null
  return <PlanGateLink upgradeTo={invite.upgradeTo} contactUrl={invite.contactUrl} className={className} />
}

// Brand tile. The official glyphs come from CloudProviderIcon; a connector
// without one (an observability vendor added later) gets the plug.
export function ConnectorTile({ id, size = 'md' }: { id: string; size?: 'md' | 'sm' }) {
  const box = size === 'md' ? 'w-10 h-10' : 'w-9 h-9'
  const known = id === 'aws' || id === 'gcp' || id === 'azure'
  return (
    <div className={`${box} rounded-lg bg-kb-elevated border border-kb-border flex items-center justify-center shrink-0`}>
      {known ? (
        <CloudProviderIcon provider={id} className="w-5 h-5" />
      ) : (
        <Plug className="w-5 h-5 text-kb-text-tertiary" aria-hidden />
      )}
    </div>
  )
}

// Only web links leave the product: a docs URL is catalog data, and an href
// is not the place to find out it was something else.
export function safeDocsUrl(url: string | undefined): string | null {
  return url && /^https?:\/\//i.test(url) ? url : null
}

export function categoryLabel(category: string): string {
  if (category === 'cloud') return 'Cloud provider'
  if (category === 'observability') return 'Observability'
  return category ? category.charAt(0).toUpperCase() + category.slice(1) : ''
}

// The one-line state under a card's description, and the status block's
// headline in the drawer.
export function statusSentence(c: McpConnector): { tone: 'ok' | 'warn' | 'error' | 'muted'; text: string } | null {
  // Not offered here: the card says so in so many words. The description
  // says why, but it is clamped to three lines and the reason comes last.
  if (!isOffered(c)) {
    if (!isConfigured(c)) return { tone: 'muted', text: 'Not available on this installation' }
    return {
      tone: effectiveStatus(c) === 'error' ? 'error' : 'muted',
      text: "Saved, but it can't be used on this installation",
    }
  }
  switch (effectiveStatus(c)) {
    // Not a state line: the card draws planInvitation in its place.
    case 'plan':
      return null
    case 'error':
      return { tone: 'error', text: c.lastError || 'The last call failed. Open it to see what to fix.' }
    case 'paused':
      return { tone: 'warn', text: "Paused — Kobi won't use it until you resume it." }
    case 'enabled':
      // A pass that found something to fix (root credentials) stays a
      // warning for as long as that verification stands.
      if (c.verifiedAt && c.verifiedWarning) return { tone: 'warn', text: c.verifiedWarning }
      return c.verifiedAt
        ? { tone: 'ok', text: verifiedText(c) }
        : { tone: 'ok', text: 'Enabled · not verified yet' }
    default:
      return isConfigured(c) ? { tone: 'muted', text: 'Saved, not enabled yet' } : null
  }
}

// "Verified · 5 tools · 7 min ago". On a connector that has failed since,
// the same facts read as history: "Last verified · …".
export function verifiedText(c: McpConnector, opts: { past?: boolean } = {}): string {
  const n = c.verifiedTools ?? 0
  const when = relativeTime(c.verifiedAt)
  return [opts.past ? 'Last verified' : 'Verified', `${n} tool${n === 1 ? '' : 's'}`, when].filter(Boolean).join(' · ')
}

// The chips under a card, each one naming what it is: read-only always, then,
// once saved, how KubeBolt signs in and where it reads. The catalog's tags
// (EKS, AKS…) are not repeated here: bare, they read as one more setting,
// and the card's Reads line already says what the connector covers.
export function connectorChips(c: McpConnector): string[] {
  const chips = ['Read-only']
  if (isConfigured(c)) {
    const mode = modeFor(c, c.mode)
    if (mode) chips.push(`Sign-in: ${mode.label}`)
    const f = c.config.fields ?? {}
    if (f.region) chips.push(`Region: ${f.region}`)
    else if (f.location) chips.push(`Location: ${f.location}`)
    else if (f.projectId) chips.push(`Project: ${f.projectId}`)
    else if (f.subscriptionId) chips.push(`Subscription: ${shortId(f.subscriptionId)}`)
  }
  return chips
}

// A subscription id is a GUID: its first block is enough to tell two apart on
// a card, and the drawer shows it whole.
function shortId(id: string): string {
  return id.length > 13 ? `${id.slice(0, 8)}…` : id
}

const REACH_LINE =
  'inline-flex items-center gap-1.5 text-left text-[11px] text-kb-text-tertiary focus-visible:text-kb-text-primary transition-colors'

export interface ReachArea {
  label: string
  // Kobi reads it and Autopilot does not.
  kobiOnly: boolean
}

// What a connector reads, as one line a card can carry: how many areas, and
// how much of the provider in its own unit (AWS services, Azure commands,
// Google Cloud tools). The areas open on hover or keyboard focus, with the
// ones Autopilot does not use marked; a click opens the drawer, which has
// each area's detail. A connector with no drawer (coming soon) gets the line
// without the click.
export function ConnectorReach({
  areas,
  reach,
  upcoming = false,
  onOpen,
}: {
  areas: ReachArea[]
  reach?: McpConnector['reach']
  upcoming?: boolean
  onOpen?: () => void
}) {
  if (areas.length === 0) return null
  const count = `${areas.length} ${areas.length === 1 ? 'area' : 'areas'}`
  const body = (
    <>
      <TooltipHeader right={count}>{upcoming ? 'What Kobi will read' : 'What Kobi reads'}</TooltipHeader>
      <ul className="space-y-1">
        {areas.map((a) => (
          <li key={a.label} className="flex items-baseline justify-between gap-3">
            <span className="text-kb-text-primary">{a.label}</span>
            {a.kobiOnly && <span className="text-[10px] text-kb-text-tertiary shrink-0">Kobi only</span>}
          </li>
        ))}
      </ul>
      {reach && (
        <div className="mt-2 pt-2 border-t border-kb-border">
          <TooltipNote>
            Through {reach.kobi} {reach.unit}, read-only.
            {reach.autopilot > 0 && ` Autopilot uses ${reach.autopilot} of them.`}
          </TooltipNote>
        </div>
      )}
    </>
  )
  const line = (
    <>
      <span>
        <span className="text-kb-text-secondary font-medium">{upcoming ? 'Will read' : 'Reads'}</span> {count}
        {reach && ` · ${reach.kobi} ${reach.unit}`}
      </span>
      <Info className="w-3 h-3 shrink-0" aria-hidden />
      <span className="sr-only">: {areas.map((a) => a.label).join(', ')}</span>
    </>
  )
  return (
    // The tooltip's trigger box takes its parent's width: kept to the line
    // itself so hovering beside it does not open the list.
    <div className="self-start max-w-full">
      <HoverTooltip body={body} minWidth={240} maxWidth={340}>
        {onOpen ? (
          <button type="button" onClick={onOpen} className={`${REACH_LINE} hover:text-kb-text-primary`}>
            {line}
          </button>
        ) : (
          // Focusable so the keyboard opens the list too; nothing to click.
          <span tabIndex={0} className={REACH_LINE}>
            {line}
          </span>
        )}
      </HoverTooltip>
    </div>
  )
}

// Copy with a short confirmation on the button itself — no toast for this.
// `name` is what a screen reader hears: with several on one panel, "Copy"
// alone does not say which one this is.
export function CopyButton({ value, label = 'Copy', name }: { value: string; label?: string; name?: string }) {
  const [copied, setCopied] = useState(false)
  const onClick = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), 1400)
    } catch {
      // Clipboard blocked (insecure context, unfocused frame): the text is
      // still selectable by hand.
    }
  }
  return (
    <button
      type="button"
      onClick={onClick}
      className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] font-mono text-kb-text-tertiary hover:text-kb-text-primary hover:bg-kb-card-hover transition-colors"
      aria-label={copied ? 'Copied' : (name ?? label)}
    >
      {copied ? <Check className="w-3 h-3 text-kb-accent" aria-hidden /> : <Copy className="w-3 h-3" aria-hidden />}
      {copied ? 'Copied' : label}
    </button>
  )
}

export function SectionLabel({ children, id }: { children: React.ReactNode; id?: string }) {
  return (
    <h3 id={id} className="text-[10px] font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider mb-2">
      {children}
    </h3>
  )
}
