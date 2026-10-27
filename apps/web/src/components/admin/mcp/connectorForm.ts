import type {
  McpAuthMode,
  McpConnector,
  McpConnectorDraft,
  McpConnectorPut,
  McpFieldSchema,
  McpKobiMinRole,
} from '@/services/api'
import type { ClusterInfo } from '@/types/kubernetes'

// Form state and payload rules for the connector drawer, kept out of the
// component so the rules that matter — what "blank" means for a secret, what
// counts as a change, what a first save must carry — are tested on their own.

export interface ConnectorForm {
  mode: string
  // Non-secret values by field name, across every mode: switching modes and
  // back must not lose what was typed, and modes share names (region).
  fields: Record<string, string>
  // Newly typed secret values. Blank means KEEP the stored one — the form has
  // no way to say "clear", on purpose: removing credentials is Remove.
  secrets: Record<string, string>
  // Who may use the connector in Kobi. Not configuration: it is saved on its
  // own when nothing else changed, so it never resets the verification.
  kobiMinRole: McpKobiMinRole
}

// The dirty-set key of the role floor, next to the field names.
export const KOBI_ROLE_KEY = 'kobiMinRole'

export const DEFAULT_KOBI_ROLE: McpKobiMinRole = 'editor'

export function kobiRoleOf(c: McpConnector): McpKobiMinRole {
  return c.kobiMinRole === 'viewer' || c.kobiMinRole === 'admin' ? c.kobiMinRole : DEFAULT_KOBI_ROLE
}

// Where a prefilled value came from, so the field can say so.
export interface Prefill {
  field: string
  value: string
  source: string
}

export function authModes(c: McpConnector): McpAuthMode[] {
  return c.authModes ?? []
}

export function modeFor(c: McpConnector, id: string): McpAuthMode | undefined {
  return authModes(c).find((m) => m.id === id)
}

// Configured = saved at least once. Status alone does not say it: a connector
// saved but never enabled is still 'available'.
export function isConfigured(c: McpConnector): boolean {
  return c.mode !== ''
}

// Offered = this install lists at least one way of signing in. One it lists
// none for (Azure on a multi-tenant install, whose sidecar holds one identity
// per process) can be neither set up nor used here, whatever is stored for it:
// the backend never binds a mode it does not offer.
export function isOffered(c: McpConnector): boolean {
  return authModes(c).length > 0
}

// Locked by the plan: an upgrade is what would turn it on. A connector the
// install does not offer is not, whatever the plan says — upgrading would
// change nothing, so nothing may offer it.
export function isPlanLocked(c: McpConnector): boolean {
  if (!isOffered(c)) return false
  return !c.entitled || c.status === 'plan'
}

// The plan a locked connector points the org at. upgradeTo is the cheapest
// plan on sale today that unlocks it, and the one worth naming: it may sit
// above the catalog minimum when that one is not sold yet. Without it the
// minimum is still the plan to name, but nothing can be bought: buyable says
// which of Billing or a conversation with us the CTA leads to.
export function lockedPlan(c: McpConnector): { plan: string; buyable: boolean } | null {
  if (!isPlanLocked(c)) return null
  if (c.upgradeTo) return { plan: c.upgradeTo, buyable: true }
  return { plan: c.minPlan, buyable: false }
}

// Whether the stored configuration holds anything secret. The Azure sidecar
// and the modes that borrow the server's identity store none — their
// identity lives in the sidecar or on the machine — so the drawer talks about
// settings there, never about stored credentials.
export function storesSecrets(c: McpConnector): boolean {
  if (Object.values(c.config.secretsConfigured ?? {}).some(Boolean)) return true
  return (modeFor(c, c.mode)?.fields ?? []).some((f) => f.secret)
}

// A value KubeBolt creates (the AWS external ID) is shown, never edited or sent.
function editable(f: McpFieldSchema): boolean {
  return !f.generated
}

export function initialForm(c: McpConnector, prefill?: Prefill | null): ConnectorForm {
  const stored = c.config.fields ?? {}
  // A detected value only ever fills a connector that was never saved: on a
  // saved one it would slip into the next save as a change nobody made.
  const guess = isConfigured(c) ? null : prefill
  const modes = authModes(c)
  const mode = modes.some((m) => m.id === c.mode) ? c.mode : (modes[0]?.id ?? '')
  const fields: Record<string, string> = {}
  for (const m of modes) {
    for (const f of m.fields) {
      if (f.secret || f.name in fields) continue
      const saved = stored[f.name]
      if (saved !== undefined && saved !== '') fields[f.name] = saved
      else if (guess && guess.field === f.name) fields[f.name] = guess.value
      else fields[f.name] = saved ?? f.default ?? ''
    }
  }
  return { mode, fields, secrets: {}, kobiMinRole: kobiRoleOf(c) }
}

function modeFields(c: McpConnector, mode: string): McpFieldSchema[] {
  return modeFor(c, mode)?.fields ?? []
}

// Per-field dirtiness for the selected mode (plus 'mode' itself and the role
// floor). Fields of other modes are not sent, so they cannot make the form
// dirty; neither can a generated one, which the admin cannot edit.
export function dirtyFields(c: McpConnector, initial: ConnectorForm, form: ConnectorForm): Set<string> {
  const out = new Set<string>()
  if (form.mode !== initial.mode) out.add('mode')
  if (form.kobiMinRole !== initial.kobiMinRole) out.add(KOBI_ROLE_KEY)
  for (const f of modeFields(c, form.mode).filter(editable)) {
    if (f.secret) {
      if ((form.secrets[f.name] ?? '').trim() !== '') out.add(f.name)
    } else if ((form.fields[f.name] ?? '').trim() !== (initial.fields[f.name] ?? '').trim()) {
      out.add(f.name)
    }
  }
  return out
}

// Whether a dirty set touches the configuration — anything but the role floor.
export function configDirty(dirty: Set<string>): boolean {
  for (const k of dirty) if (k !== KOBI_ROLE_KEY) return true
  return false
}

// A secret counts as present when one is typed now, or when a readable one
// is stored for the mode being saved. After a mode switch the stored secrets
// belong to the other mode as far as the form can tell, so they do not count.
export function secretPresent(c: McpConnector, form: ConnectorForm, name: string): boolean {
  if ((form.secrets[name] ?? '').trim() !== '') return true
  if (form.mode !== c.mode) return false
  const configured = !!c.config.secretsConfigured?.[name]
  const unreadable = (c.config.secretsUnreadable ?? []).includes(name)
  return configured && !unreadable
}

export function missingRequired(c: McpConnector, form: ConnectorForm): McpFieldSchema[] {
  return modeFields(c, form.mode).filter((f) => {
    // A generated value is KubeBolt's to fill, not something to ask for.
    if (!f.required || f.generated) return false
    if (f.secret) return !secretPresent(c, form, f.name)
    return (form.fields[f.name] ?? '').trim() === ''
  })
}

// The config part of a save or a test: the chosen mode, every non-secret
// field of that mode (the whole set, so a stored value of another shape
// cannot survive by accident), and only the secrets actually typed. A blank
// secret is left out — absent means keep — and never sent as "", which the
// API reads as "clear".
function configPayload(c: McpConnector, form: ConnectorForm): Required<Pick<McpConnectorPut, 'mode' | 'fields'>> & Pick<McpConnectorPut, 'secrets'> {
  const fields: Record<string, string> = {}
  const secrets: Record<string, string> = {}
  for (const f of modeFields(c, form.mode).filter(editable)) {
    if (f.secret) {
      const v = (form.secrets[f.name] ?? '').trim()
      if (v !== '') secrets[f.name] = v
    } else {
      fields[f.name] = (form.fields[f.name] ?? '').trim()
    }
  }
  const out: Required<Pick<McpConnectorPut, 'mode' | 'fields'>> & Pick<McpConnectorPut, 'secrets'> = {
    mode: form.mode,
    fields,
  }
  if (Object.keys(secrets).length > 0) out.secrets = secrets
  return out
}

// A first save turns the connector on: the admin clicked "Save and enable".
// Later saves change only what changed; pausing is its own action. A change
// of the role floor alone goes out alone: resending an unchanged config
// would be a save of it all the same.
export function buildPut(c: McpConnector, form: ConnectorForm, opts: { enable: boolean }): McpConnectorPut {
  const sendConfig = opts.enable || !isConfigured(c) || configDirty(dirtyFields(c, initialForm(c), form))
  const body: McpConnectorPut = sendConfig ? configPayload(c, form) : {}
  if (opts.enable) body.enabled = true
  if (form.kobiMinRole !== kobiRoleOf(c)) body.kobiMinRole = form.kobiMinRole
  return body
}

// What the Test button sends. Nothing — test what is stored, which also
// refreshes "Verified …" — when the form holds no change to a saved config
// (the role floor is not config); otherwise the draft, merged over the
// stored config server-side and never persisted.
export function buildDraft(c: McpConnector, configChanged: boolean, form: ConnectorForm): McpConnectorDraft | undefined {
  if (isConfigured(c) && !configChanged) return undefined
  return configPayload(c, form)
}

// The region of a cluster already running on this provider, to prefill the
// connector's region with. The active cluster wins; a guess from a cluster on
// another provider would be worse than an empty field.
export function detectRegion(connectorId: string, clusters: ClusterInfo[] | null | undefined): Prefill | null {
  if (!clusters) return null
  const onProvider = clusters.filter((cl) => providerOf(cl.cloudProvider) === connectorId && !!cl.region)
  if (onProvider.length === 0) return null
  const pick = onProvider.find((cl) => cl.active) ?? onProvider[0]
  return { field: 'region', value: pick.region!, source: pick.displayName || pick.name }
}

function providerOf(raw?: string): string {
  const p = (raw ?? '').trim().toLowerCase()
  if (!p) return ''
  if (p === 'aws' || p.includes('amazon') || p === 'eks') return 'aws'
  if (p === 'gcp' || p.includes('google') || p === 'gke' || p === 'gce') return 'gcp'
  if (p.includes('azure') || p === 'aks' || p.includes('microsoft')) return 'azure'
  return p
}

// "just now", "5 min ago", "3 h ago", "2 days ago". Empty for a bad date.
export function relativeTime(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return ''
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return ''
  const s = Math.max(0, Math.floor((now - then) / 1000))
  if (s < 60) return 'just now'
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} min ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} h ago`
  const d = Math.floor(h / 24)
  return `${d} day${d === 1 ? '' : 's'} ago`
}

// What changing the settings can point KubeBolt at, named from the mode's own
// fields: an AWS region, an Azure subscription, a Google Cloud project.
const SETTING_NOUNS: Record<string, string> = {
  subscriptionId: 'subscription',
  projectId: 'project',
  region: 'region',
  roleArn: 'role',
}

export function settingsReach(mode: McpAuthMode | undefined): string {
  const nouns = [
    ...new Set(
      (mode?.fields ?? [])
        .filter((f) => !f.secret && !f.generated && !f.advanced)
        .map((f) => SETTING_NOUNS[f.name])
        .filter((n): n is string => !!n),
    ),
  ]
  if (nouns.length === 0) return ''
  const list = nouns.length === 1 ? nouns[0] : `${nouns.slice(0, -1).join(', ')} or ${nouns[nouns.length - 1]}`
  return ` Change the settings to point KubeBolt at another ${list}.`
}
