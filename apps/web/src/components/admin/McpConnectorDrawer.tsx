import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  Ban,
  Check,
  ChevronDown,
  ExternalLink,
  Info,
  Loader2,
  Lock,
  Pause,
  Play,
  ShieldCheck,
  Trash2,
  X,
} from 'lucide-react'
import {
  api,
  ApiError,
  isRequiresUpgrade,
  planLabel,
  requiresUpgradePlan,
  type McpAuthMode,
  type McpConnector,
  type McpConnectorPut,
  type McpKobiMinRole,
} from '@/services/api'
import { useEscapeClose } from '@/hooks/useEscapeClose'
import { ConfirmDialog } from '@/pages/admin/settings/ConfirmDialog'
import { MutationErrorToast } from '@/components/shared/MutationErrorToast'
import { useHideCopilotLauncher } from '@/components/copilot/CopilotToggle'
import { UnsavedChip } from '@/pages/admin/settings/SettingsField'
import { connectorDisplayName } from '@/utils/toolName'
import {
  KOBI_ROLE_KEY,
  authModes,
  buildDraft,
  buildPut,
  configDirty,
  detectRegion,
  dirtyFields,
  initialForm,
  isConfigured,
  isOffered,
  isPlanLocked,
  missingRequired,
  modeFor,
  settingsReach,
  storesSecrets,
  type ConnectorForm,
} from './mcp/connectorForm'
import {
  MCP_CONNECTORS_KEY,
  ConnectorStatusBadge,
  ConnectorTile,
  PlanGateLink,
  PlanUpgradeCta,
  SectionLabel,
  UPGRADE_CTA,
  categoryLabel,
  effectiveStatus,
  planInvitation,
  safeContactUrl,
  safeDocsUrl,
  storedModeLabel,
  useUpgradeContactUrl,
  verifiedText,
} from './mcp/ConnectorBits'
import { ConnectorFieldInput, type SecretState } from './mcp/ConnectorFieldInput'
import { SetupGuide } from './mcp/SetupGuide'
import { describeTestError, describeTestResult, planGateUpgradeTo, type TestOutcome } from './mcp/testOutcome'

// McpConnectorDrawer — set up, check and manage one MCP connector.
//
// Reading order follows the decision an admin makes: what it is and whether it
// works (status), what it will let Kobi read and what never (families + the
// Never list) and who may use it in Kobi, how to prepare the cloud side (setup
// guide), then the credentials — "settings" where the mode stores nothing
// secret. On a connector that is already working the credentials fold behind
// "Change credentials" and the guide behind a disclosure, so the panel opens
// on the answer to "is it OK?" rather than on a form.
//
// Actions live in a sticky footer with one vocabulary: Test connection, Save
// and enable (first time) / Save changes, Pause / Resume. Enabling is not a
// toggle in the body — a toggle next to Save left it unclear which of the two
// turned the connector on.

// The backend rate-limits tests (1 per 5 s per org); matching it client-side
// keeps the button from inviting a 429.
const TEST_COOLDOWN_MS = 5_000

// Who may use the connector in Kobi. The default first: it is what a
// connector that was never touched does.
const KOBI_ROLES: { value: McpKobiMinRole; label: string }[] = [
  { value: 'editor', label: 'Editors and admins (default)' },
  { value: 'viewer', label: 'Everyone' },
  { value: 'admin', label: 'Admins only' },
]

// Inputs taken out of the tab order (the hidden file picker) are excluded:
// wrapping focus onto one would land nowhere.
const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]):not([tabindex="-1"]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])'

// An inline text action ("Change settings", "Review settings"). Neutral text
// with an underline, not the accent: the accent green reads at 3.65:1 on the
// light theme's white card at 11px, under AA.
const TEXT_ACTION =
  'text-[11px] font-medium text-kb-text-primary underline decoration-kb-border underline-offset-2 hover:decoration-current'

type FooterNote =
  | { kind: 'test'; outcome: TestOutcome }
  | { kind: 'saved'; text: string }
  // A 409 requires_upgrade: the plan it names (upgradeTo, else requiredPlan)
  // and the one on sale that clears it ('' when nothing on sale does).
  | { kind: 'plan'; plan: string | null; upgradeTo: string }
  | { kind: 'error'; text: string }

interface Props {
  connector: McpConnector
  // Where "Contact us" leads when nothing on sale unlocks a locked connector
  // (useUpgradeContactUrl); '' or absent draws no such action.
  contactUrl?: string
  onClose: () => void
}

export function McpConnectorDrawer({ connector: c, contactUrl, onClose }: Props) {
  const qc = useQueryClient()
  const uid = useId()
  const idPrefix = `mcp-${c.id}-${uid.replace(/[^a-zA-Z0-9_-]/g, '')}`
  const titleId = `${idPrefix}-title`
  const name = connectorDisplayName(c.id) || c.name
  // The footer's primary action sits where the Kobi launcher floats.
  useHideCopilotLauncher()

  // Never true where the install offers no way of signing in: there the
  // plan is beside the point, and the drawer says why instead (below).
  const locked = isPlanLocked(c)
  // What the card says about the lock, word for word: set exactly when locked.
  const invite = planInvitation(c, contactUrl)
  const configured = isConfigured(c)
  const status = effectiveStatus(c)
  // Never turned on (or saved but never enabled): the primary action is
  // "Save and enable". Once it has been on, saving only saves.
  const firstSave = !c.enabled
  const modes = authModes(c)
  // No way of signing in offered on this install (Azure on a multi-tenant
  // one): nothing to set up, save or test, and whatever was stored before it
  // stopped offering one can only be looked at and removed.
  const offered = isOffered(c)
  // Credentials or only settings: the Azure sidecar and the modes that borrow
  // the server's identity store nothing secret, and the copy must not say so.
  const secretsStored = storesSecrets(c)
  const settingsWord = secretsStored ? 'credentials' : 'settings'

  // A region already known from a cluster on the same provider prefills the
  // form of a connector that was never saved.
  const { data: clusters } = useQuery({ queryKey: ['clusters'], queryFn: api.listClusters, staleTime: 60_000 })
  const prefill = useMemo(() => detectRegion(c.id, clusters), [c.id, clusters])

  const [initial, setInitial] = useState<ConnectorForm>(() => initialForm(c, prefill))
  const [form, setForm] = useState<ConnectorForm>(initial)
  const dirtySet = useMemo(() => dirtyFields(c, initial, form), [c, initial, form])
  const dirty = dirtySet.size > 0
  // The role floor is not configuration: changing it alone neither makes
  // Test send a draft nor asks for the credentials again.
  const cfgDirty = configDirty(dirtySet)

  // Follow the server (the list refetches every 30 s, and a save seeds it)
  // for as long as the admin has nothing unsaved. With edits in flight the
  // form is theirs until they save or discard.
  useEffect(() => {
    if (dirty) return
    const next = initialForm(c, prefill)
    setInitial(next)
    setForm(next)
  }, [c, prefill, dirty])

  const mode = modeFor(c, form.mode)
  const fields = mode?.fields ?? []
  const basicFields = fields.filter((f) => !f.advanced)
  const advancedFields = fields.filter((f) => f.advanced)
  const missing = missingRequired(c, form)
  const unreadable = c.config.secretsUnreadable ?? []

  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [showForm, setShowForm] = useState(() => !configured || c.status === 'error' || unreadable.length > 0)
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [showGuide, setShowGuide] = useState(!configured)
  const [showTech, setShowTech] = useState(false)
  const [note, setNote] = useState<FooterNote | null>(null)
  // A plan gate in the footer (a 409 on save, a test the plan refused) with
  // nothing on sale leads to the contact address. It can arrive while the list
  // still says entitled — the plan dropped after the drawer opened — so the
  // page asked for no address; the drawer asks for it then.
  const gateNeedsContact = !!note && isPlanGateNote(note) && !planGateNoteUpgradeTo(note)
  const ownContact = useUpgradeContactUrl(gateNeedsContact && !safeContactUrl(contactUrl))
  const gateContact = safeContactUrl(contactUrl) ?? ownContact
  const [toast, setToast] = useState<{ action: string; error: unknown } | null>(null)
  // Stable: MutationErrorToast restarts its 6 s timer whenever this changes.
  const dismissToast = useCallback(() => setToast(null), [])
  const [confirmDiscard, setConfirmDiscard] = useState(false)
  const [confirmRemove, setConfirmRemove] = useState(false)

  // ── Close, Esc, focus ────────────────────────────────────────────────
  const requestClose = useCallback(() => {
    // With a confirmation open, Esc belongs to it (its Modal closes itself).
    if (confirmDiscard || confirmRemove) return
    if (dirty) {
      setConfirmDiscard(true)
      return
    }
    onClose()
  }, [confirmDiscard, confirmRemove, dirty, onClose])
  useEscapeClose(requestClose)

  const panelRef = useRef<HTMLDivElement>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  useEffect(() => {
    // Focus the title rather than the first input: on a connector being set
    // up, the first input sits below the setup guide, and jumping there would
    // skip what the admin has to read first. Focus goes back where it came
    // from on close.
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    headingRef.current?.focus()
    return () => {
      if (previous && document.contains(previous)) previous.focus()
    }
  }, [])

  const onPanelKeyDown = (e: React.KeyboardEvent) => {
    if (e.key !== 'Tab' || !panelRef.current) return
    const nodes = Array.from(panelRef.current.querySelectorAll<HTMLElement>(FOCUSABLE))
    if (nodes.length === 0) return
    const first = nodes[0]
    const last = nodes[nodes.length - 1]
    const active = document.activeElement
    if (e.shiftKey && (active === first || active === headingRef.current)) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && active === last) {
      e.preventDefault()
      first.focus()
    }
  }

  // ── Editing ──────────────────────────────────────────────────────────
  const setValue = (fieldName: string, value: string, secret: boolean) => {
    setForm((f) =>
      secret
        ? { ...f, secrets: { ...f.secrets, [fieldName]: value } }
        : { ...f, fields: { ...f.fields, [fieldName]: value } },
    )
    setFieldErrors((errs) => {
      if (!(fieldName in errs)) return errs
      const next = { ...errs }
      delete next[fieldName]
      return next
    })
    // A test or save result describes the form as it was.
    setNote((n) => (n && n.kind !== 'plan' ? null : n))
  }

  const setMode = (id: string) => {
    setForm((f) => ({ ...f, mode: id }))
    setFieldErrors({})
    setNote((n) => (n && n.kind !== 'plan' ? null : n))
  }

  const setKobiRole = (role: McpKobiMinRole) => {
    setForm((f) => ({ ...f, kobiMinRole: role }))
    setNote((n) => (n && n.kind !== 'plan' ? null : n))
  }

  // Point a 400 at its field: open the form (and Advanced if that is where
  // the field lives) and move focus there. A field the form does not show
  // becomes a footer message instead.
  const markField = (fieldName: string, message: string) => {
    const f = fields.find((x) => x.name === fieldName)
    if (!f) {
      setNote({ kind: 'error', text: message })
      return
    }
    setFieldErrors((errs) => ({ ...errs, [fieldName]: message }))
    setShowForm(true)
    if (f.advanced) setShowAdvanced(true)
    setTimeout(() => document.getElementById(`${idPrefix}-${fieldName}`)?.focus(), 0)
  }

  // "Review credentials" on a failing connector: the form sits below the
  // catalog of what it reads, so take the admin there and into the first field.
  const connectionRef = useRef<HTMLElement>(null)
  const reviewCredentials = () => {
    setShowForm(true)
    setTimeout(() => {
      connectionRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })
      connectionRef.current?.querySelector<HTMLElement>('input:not([type="radio"]):not([type="file"]), select')?.focus({ preventScroll: true })
    }, 0)
  }

  const applyView = (view: McpConnector) => {
    qc.setQueryData<{ connectors: McpConnector[] | null }>(MCP_CONNECTORS_KEY, (old) =>
      old ? { connectors: (old.connectors ?? []).map((x) => (x.id === view.id ? view : x)) } : old,
    )
  }

  const onMutationError = (action: string) => (err: unknown) => {
    if (err instanceof ApiError && err.status === 400 && typeof err.payload?.field === 'string') {
      markField(err.payload.field, err.message)
      return
    }
    if (isRequiresUpgrade(err)) {
      setNote({ kind: 'plan', plan: requiresUpgradePlan(err), upgradeTo: planGateUpgradeTo(err) })
      return
    }
    setToast({ action, error: err })
  }

  // ── Save / pause / remove ────────────────────────────────────────────
  const save = useMutation({
    mutationFn: (body: McpConnectorPut) => api.putMcpConnector(c.id, body),
    onSuccess: (view, body) => {
      if (view) {
        applyView(view)
        const next = initialForm(view, prefill)
        setInitial(next)
        setForm(next)
      } else {
        qc.invalidateQueries({ queryKey: MCP_CONNECTORS_KEY })
      }
      setFieldErrors({})
      setShowForm(false)
      setNote({
        kind: 'saved',
        text: body.enabled ? `${name} is on. Kobi can use it from its next message.` : 'Changes saved.',
      })
    },
    onError: onMutationError('Save'),
  })

  const pause = useMutation({
    mutationFn: (paused: boolean) => api.putMcpConnector(c.id, { paused }),
    onSuccess: (view, paused) => {
      if (view) applyView(view)
      else qc.invalidateQueries({ queryKey: MCP_CONNECTORS_KEY })
      setNote({
        kind: 'saved',
        text: paused ? "Paused. Kobi stops using it right away." : 'Resumed. Kobi can use it again.',
      })
    },
    onError: (err, paused) => onMutationError(paused ? 'Pause' : 'Resume')(err),
  })

  const remove = useMutation({
    mutationFn: () => api.deleteMcpConnector(c.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: MCP_CONNECTORS_KEY })
      setConfirmRemove(false)
      onClose()
    },
    onError: (err) => {
      setConfirmRemove(false)
      setToast({ action: 'Remove', error: err })
    },
  })

  const busy = save.isPending || pause.isPending || remove.isPending

  // Save and Test say "fill this in first" the same way: on the field, with
  // focus there — never through a disabled button whose reason only a
  // tooltip knows.
  const blockOnMissing = (): boolean => {
    if (missing.length === 0) return false
    markField(missing[0].name, `${missing[0].label} is required.`)
    return true
  }

  const onSave = () => {
    // A change of who may use it goes out alone and needs no credentials.
    const sendsConfig = firstSave || !configured || cfgDirty
    if (sendsConfig && blockOnMissing()) return
    save.mutate(buildPut(c, form, { enable: firstSave }))
  }

  // ── Test ─────────────────────────────────────────────────────────────
  const [testing, setTesting] = useState(false)
  const [cooldownUntil, setCooldownUntil] = useState(0)
  const [clock, setClock] = useState(() => Date.now())
  useEffect(() => {
    if (cooldownUntil <= Date.now()) return
    const t = window.setInterval(() => {
      const now = Date.now()
      setClock(now)
      if (now >= cooldownUntil) window.clearInterval(t)
    }, 250)
    return () => window.clearInterval(t)
  }, [cooldownUntil])
  const coolingFor = Math.max(0, Math.ceil((cooldownUntil - clock) / 1000))

  const coolDown = (ms: number) => {
    const now = Date.now()
    setClock(now)
    setCooldownUntil(now + ms)
  }

  const runTest = async () => {
    if (blockOnMissing()) return
    coolDown(TEST_COOLDOWN_MS)
    setTesting(true)
    setNote(null)
    const draft = buildDraft(c, cfgDirty, form)
    try {
      const result = await api.testMcpConnector(c.id, draft)
      setNote({ kind: 'test', outcome: describeTestResult(result) })
      // Only a test of the stored config moves "Verified …".
      if (!draft) qc.invalidateQueries({ queryKey: MCP_CONNECTORS_KEY })
    } catch (err) {
      const outcome = describeTestError(err)
      if (outcome.retryAfterSeconds) coolDown(outcome.retryAfterSeconds * 1000)
      if (outcome.field && fields.some((f) => f.name === outcome.field)) markField(outcome.field, outcome.title)
      setNote({ kind: 'test', outcome })
    } finally {
      setTesting(false)
    }
  }

  // ── Render ───────────────────────────────────────────────────────────
  const secretStateOf = (fieldName: string): SecretState => {
    if (form.mode !== c.mode) return 'none'
    if (unreadable.includes(fieldName)) return 'unreadable'
    return c.config.secretsConfigured?.[fieldName] ? 'set' : 'none'
  }

  const renderField = (f: (typeof fields)[number]) => (
    <ConnectorFieldInput
      key={f.name}
      field={f}
      idPrefix={idPrefix}
      value={f.secret ? (form.secrets[f.name] ?? '') : (form.fields[f.name] ?? '')}
      onChange={(v) => setValue(f.name, v, f.secret)}
      dirty={dirtySet.has(f.name)}
      error={fieldErrors[f.name]}
      secretState={f.secret ? secretStateOf(f.name) : undefined}
      prefillSource={
        !configured && prefill && prefill.field === f.name && form.fields[f.name] === prefill.value
          ? prefill.source
          : undefined
      }
      disabled={locked}
    />
  )

  const families = c.families ?? []
  const never = c.never ?? []
  const steps = c.setupGuide?.steps ?? []
  const kobiTools = c.tools?.kobi ?? []
  const autopilotTools = c.tools?.autopilot ?? []
  const anyAutopilot = families.some((f) => f.autopilot)
  const storedMode = modeFor(c, c.mode)
  // A value KubeBolt generated for the org (the AWS external ID) outlives a
  // remove: set up again, the connector shows the same one, so the role whose
  // trust policy names it keeps working. Only the stored mode's counts.
  const keptOnRemove = storedMode?.fields.find((f) => f.generated && c.config.fields?.[f.name])
  // Kobi has no access to lose on an install that does not offer the
  // connector: removing it only deletes what was stored.
  const removeCopy = offered
    ? [
        secretsStored
          ? 'Kobi loses access immediately and the stored credentials are deleted.'
          : 'Kobi loses access immediately and its settings are deleted.',
        keptOnRemove
          ? `The ${keptOnRemove.label} stays the same, so the role that trusts it keeps working when you set the connector up again.`
          : 'You can set it up again later.',
      ].join(' ')
    : `${secretsStored ? 'Its stored credentials are deleted.' : 'Its settings are deleted.'} Kobi doesn't use it on this installation, so nothing changes for Kobi.`
  // A verification that no longer describes what Kobi can do reads as
  // history: after a failure, and on an install that does not offer the
  // connector, where no test can renew it.
  const verifiedPast = status === 'error' || !offered
  // A passing test that found something to fix stays in view, in the warn
  // tone, for as long as that verification stands.
  const verifiedWarning = c.verifiedAt && !verifiedPast ? c.verifiedWarning : undefined

  return (
    // Below the Topbar (h-[52px]) so the cluster switcher and search stay
    // reachable, like the Integrations panel.
    <div className="fixed left-0 right-0 bottom-0 top-[52px] z-[150] flex items-stretch justify-end pointer-events-none">
      <div
        className="absolute inset-0 bg-black/30 backdrop-blur-sm pointer-events-auto"
        onClick={requestClose}
        aria-hidden
      />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={onPanelKeyDown}
        className="relative w-full max-w-lg bg-kb-card border-l border-kb-border flex flex-col pointer-events-auto shadow-2xl"
      >
        {/* Header */}
        <div className="flex items-start justify-between gap-3 px-5 py-4 border-b border-kb-border shrink-0">
          <div className="flex items-center gap-3 min-w-0">
            <ConnectorTile id={c.id} size="sm" />
            <div className="min-w-0">
              <div className="flex items-center gap-2 flex-wrap">
                <h2
                  id={titleId}
                  ref={headingRef}
                  tabIndex={-1}
                  className="text-sm font-semibold text-kb-text-primary truncate focus:outline-none"
                >
                  {c.name}
                </h2>
                <ConnectorStatusBadge connector={c} />
              </div>
              <p className="text-[11px] text-kb-text-tertiary mt-0.5">
                {categoryLabel(c.category)} · Read-only
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={requestClose}
            className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors shrink-0"
            aria-label={`Close ${c.name} settings`}
          >
            <X className="w-4 h-4" aria-hidden />
          </button>
        </div>

        {/* Body */}
        <div className="flex-1 overflow-y-auto p-5 space-y-6">
          {/* The action itself is the footer's, which stays in view: one
              "Upgrade to <plan>" per panel, as on the card. */}
          {invite && (
            <div className="flex items-start gap-2.5 rounded-xl border border-kb-border border-l-2 border-l-kb-accent bg-kb-bg p-4">
              <Lock className="w-4 h-4 text-kb-accent shrink-0 mt-0.5" aria-hidden />
              <div className="text-xs text-kb-text-secondary leading-relaxed space-y-1">
                <div className="font-semibold text-kb-text-primary">{invite.headline}</div>
                <p>{[invite.body, 'Until then you can read what it does and how it is set up.'].filter(Boolean).join(' ')}</p>
              </div>
            </div>
          )}

          {unreadable.length > 0 && (
            <div className="flex items-start gap-2.5 rounded-xl border border-kb-border bg-status-warn-dim p-4 text-xs text-kobi-st-warn">
              <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5 text-status-warn" aria-hidden />
              <div className="leading-relaxed">
                <div className="font-semibold mb-0.5">A stored credential can&apos;t be read</div>
                <div>
                  KubeBolt&apos;s encryption key changed since it was saved. Enter{' '}
                  {unreadable
                    .map((n) => fields.find((f) => f.name === n)?.label ?? storedMode?.fields.find((f) => f.name === n)?.label ?? n)
                    .join(', ')}{' '}
                  again and save to restore access.
                </div>
              </div>
            </div>
          )}

          <div className="space-y-2">
            <p className="text-xs text-kb-text-secondary leading-relaxed">{c.description}</p>
            {safeDocsUrl(c.docsURL) && (
              <a
                href={safeDocsUrl(c.docsURL)!}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 text-[11px] text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
              >
                <ExternalLink className="w-3 h-3" aria-hidden />
                Provider documentation
              </a>
            )}
          </div>

          {/* Status — only once there is something to report on. */}
          {configured && (
            <section aria-label="Status">
              <div className="rounded-xl border border-kb-border bg-kb-bg p-4 space-y-3">
                <StatusHeadline
                  connector={c}
                  planHeadline={invite?.headline}
                  settingsWord={settingsWord}
                  onReview={!locked && offered ? reviewCredentials : undefined}
                />
                {/* Where no test can be run, "run a test" is no advice. */}
                {(c.verifiedAt || offered) && (
                  <div className="space-y-1">
                    <div className="flex items-center gap-1.5 text-[11px] text-kb-text-secondary">
                      {verifiedWarning ? (
                        <AlertTriangle className="w-3.5 h-3.5 shrink-0 text-status-warn" aria-hidden />
                      ) : (
                        <ShieldCheck
                          className={`w-3.5 h-3.5 shrink-0 ${c.verifiedAt && !verifiedPast ? 'text-kb-accent' : 'text-kb-text-tertiary'}`}
                          aria-hidden
                        />
                      )}
                      {c.verifiedAt
                        ? verifiedText(c, { past: verifiedPast })
                        : `Not verified yet — run a test to check the ${settingsWord}.`}
                    </div>
                    {verifiedWarning && (
                      <p className="pl-5 text-[11px] text-kobi-st-warn leading-relaxed">{verifiedWarning}</p>
                    )}
                  </div>
                )}
                <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-[11px] pt-2 border-t border-kb-border">
                  <dt className="text-kb-text-tertiary">Signs in with</dt>
                  <dd className="text-kb-text-primary">{storedModeLabel(c)}</dd>
                  {c.verifiedIdentity && <SummaryRow label="Verified as" value={c.verifiedIdentity} mono />}
                  {(storedMode?.fields ?? []).map((f) => {
                    if (f.secret) {
                      const state = unreadable.includes(f.name)
                        ? "Can't be read"
                        : c.config.secretsConfigured?.[f.name]
                          ? 'Set'
                          : 'Not set'
                      return (
                        <SummaryRow key={f.name} label={f.label} value={state} warn={state !== 'Set' && f.required} />
                      )
                    }
                    const v = c.config.fields?.[f.name]
                    return v ? <SummaryRow key={f.name} label={f.label} value={v} mono /> : null
                  })}
                </dl>
              </div>
            </section>
          )}

          {/* What it reaches, in plain words. */}
          <div className="grid grid-cols-3 gap-3">
            <Fact label="Runs from" value="KubeBolt server" />
            <Fact label="Reaches" value={`${name} management APIs`} />
            <Fact label="Access" value="Read-only" />
          </div>

          {/* What Kobi can read + Never */}
          {(families.length > 0 || never.length > 0) && (
            <section aria-labelledby={`${idPrefix}-reads`}>
              <SectionLabel id={`${idPrefix}-reads`}>What Kobi can read</SectionLabel>
              {anyAutopilot && (
                <p className="text-[11px] text-kb-text-tertiary mb-2 leading-relaxed">
                  Kobi reads these while it answers you. Lines marked Autopilot are also read during Autopilot&apos;s
                  incident runs.
                </p>
              )}
              {families.length > 0 && (
                <ul className="rounded-xl border border-kb-border divide-y divide-kb-border">
                  {families.map((f) => (
                    <li key={f.label} className="flex items-start justify-between gap-3 px-3 py-2.5">
                      <div className="min-w-0">
                        <div className="text-xs font-medium text-kb-text-primary">{f.label}</div>
                        {f.detail && (
                          <div className="text-[11px] text-kb-text-tertiary leading-relaxed mt-0.5">{f.detail}</div>
                        )}
                      </div>
                      <div className="flex items-center gap-1 shrink-0">
                        {f.kobi && <ConsumerMark>Kobi</ConsumerMark>}
                        {f.autopilot && <ConsumerMark muted>Autopilot</ConsumerMark>}
                      </div>
                    </li>
                  ))}
                </ul>
              )}
              {never.length > 0 && (
                <div className="mt-3">
                  <div className="text-[11px] font-semibold text-kb-text-primary mb-1.5">Never</div>
                  <ul className="space-y-1">
                    {never.map((n) => (
                      <li key={n} className="flex items-start gap-1.5 text-[11px] text-kb-text-secondary leading-relaxed">
                        <Ban className="w-3 h-3 text-status-error shrink-0 mt-0.5" aria-hidden />
                        {n}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {(kobiTools.length > 0 || autopilotTools.length > 0) && (
                <div className="mt-3">
                  <Disclosure open={showTech} onToggle={() => setShowTech((v) => !v)} controls={`${idPrefix}-tech`}>
                    Technical details
                  </Disclosure>
                  {showTech && (
                    <div id={`${idPrefix}-tech`} className="mt-2 space-y-2">
                      <ToolList title="Tools Kobi sees" tools={kobiTools} />
                      <ToolList title="Tools Autopilot sees" tools={autopilotTools} />
                    </div>
                  )}
                </div>
              )}
            </section>
          )}

          {/* Who may use it in Kobi. Part of the form like everything else in
              the body — Save applies it — but not configuration: saved on its
              own it keeps the verification. */}
          {!locked && offered && (
            <section aria-labelledby={`${idPrefix}-who`}>
              <div className="flex items-center gap-2">
                <SectionLabel id={`${idPrefix}-who`}>Who can use it in Kobi</SectionLabel>
                {dirtySet.has(KOBI_ROLE_KEY) && (
                  <span className="mb-2">
                    <UnsavedChip />
                  </span>
                )}
              </div>
              <select
                id={`${idPrefix}-kobi-role`}
                aria-labelledby={`${idPrefix}-who`}
                aria-describedby={`${idPrefix}-who-help`}
                value={form.kobiMinRole}
                onChange={(e) => setKobiRole(e.target.value as McpKobiMinRole)}
                className="w-full px-2.5 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
              >
                {KOBI_ROLES.map((r) => (
                  <option key={r.value} value={r.value}>
                    {r.label}
                  </option>
                ))}
              </select>
              <p id={`${idPrefix}-who-help`} className="mt-1.5 text-[11px] text-kb-text-tertiary leading-relaxed">
                Anyone else still uses Kobi, without this connector. It applies in every team
                {autopilotTools.length > 0 ? ", and Autopilot's incident runs are not affected." : '.'}
              </p>
            </section>
          )}

          {/* Setup guide — or, where no way of signing in is offered, why
              there is nothing to prepare. A guide to a connector that can't
              be connected would walk the admin through work for nothing, and
              that holds whatever the plan says: no upgrade makes it
              available (isPlanLocked), so no Upgrade is offered either. */}
          {!offered ? (
            <section aria-labelledby={`${idPrefix}-guide`}>
              <SectionLabel id={`${idPrefix}-guide`}>Setup</SectionLabel>
              <div className="flex items-start gap-2.5 rounded-xl border border-kb-border bg-kb-elevated p-4">
                <Info className="w-4 h-4 text-kb-text-secondary shrink-0 mt-0.5" aria-hidden />
                <div className="text-xs text-kb-text-secondary leading-relaxed space-y-1">
                  <div className="font-semibold text-kb-text-primary">
                    This connector can&apos;t be set up on this installation.
                  </div>
                  <p>
                    It signs in with an identity that belongs to the server KubeBolt runs on, which only a
                    self-hosted, single-tenant installation may lend to Kobi. There is nothing to prepare on your side
                    here.
                  </p>
                </div>
              </div>
            </section>
          ) : (
            <section aria-labelledby={`${idPrefix}-guide`}>
              {configured ? (
                <>
                  <Disclosure open={showGuide} onToggle={() => setShowGuide((v) => !v)} controls={`${idPrefix}-guide-body`}>
                    <span id={`${idPrefix}-guide`}>Setup guide</span>
                  </Disclosure>
                  {showGuide && (
                    <div id={`${idPrefix}-guide-body`} className="mt-3">
                      <SetupGuide steps={steps} />
                    </div>
                  )}
                </>
              ) : (
                <>
                  <SectionLabel id={`${idPrefix}-guide`}>Before you connect</SectionLabel>
                  <SetupGuide steps={steps} />
                </>
              )}
            </section>
          )}

          {/* Connection */}
          {!locked && offered && (
            <section ref={connectionRef} aria-labelledby={`${idPrefix}-conn`} className="space-y-3 scroll-mt-4">
              <div className="flex items-center justify-between gap-2">
                <SectionLabel id={`${idPrefix}-conn`}>Connection</SectionLabel>
                {configured && !showForm && (
                  <button
                    type="button"
                    onClick={() => setShowForm(true)}
                    className={`${TEXT_ACTION} mb-2`}
                  >
                    Change {settingsWord}
                  </button>
                )}
              </div>

              {configured && !showForm ? (
                <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
                  {secretsStored
                    ? 'Credentials are stored encrypted and never shown again. Change them to rotate a key or point KubeBolt at another role or project.'
                    : identityHome(storedMode)}
                </p>
              ) : (
                <div className="space-y-4">
                  {modes.length > 1 ? (
                    <div role="radiogroup" aria-label="How KubeBolt signs in" className="grid gap-2">
                      {modes.map((m) => {
                        const on = form.mode === m.id
                        return (
                          <label
                            key={m.id}
                            className={`flex items-start gap-2.5 p-3 rounded-lg border cursor-pointer transition-colors ${
                              on ? 'border-kb-accent bg-kb-accent-light' : 'border-kb-border hover:bg-kb-card-hover'
                            }`}
                          >
                            <input
                              type="radio"
                              name={`${idPrefix}-mode`}
                              value={m.id}
                              checked={on}
                              onChange={() => setMode(m.id)}
                              className="mt-0.5 accent-kb-accent"
                            />
                            <span className="min-w-0">
                              <span className="flex items-center gap-2 flex-wrap text-xs font-medium text-kb-text-primary">
                                {m.label}
                                {dirtySet.has('mode') && on && <UnsavedChip />}
                              </span>
                              {m.description && (
                                <span className="block text-[11px] text-kb-text-secondary leading-relaxed mt-0.5">
                                  {m.description}
                                </span>
                              )}
                            </span>
                          </label>
                        )
                      })}
                    </div>
                  ) : (
                    mode && (
                      <div>
                        <div className="text-xs font-medium text-kb-text-primary">{mode.label}</div>
                        {mode.description && (
                          <p className="text-[11px] text-kb-text-secondary leading-relaxed mt-0.5">{mode.description}</p>
                        )}
                      </div>
                    )
                  )}

                  {mode?.selfHostedOnly && (
                    <div className="flex items-start gap-2 rounded-lg border border-kb-border bg-kb-elevated p-3 text-[11px] text-kb-text-secondary leading-relaxed">
                      <Info className="w-3.5 h-3.5 text-status-info shrink-0 mt-0.5" aria-hidden />
                      <span>
                        KubeBolt uses the identity of the server it runs on. Kobi can still only make the read-only
                        calls listed above, but the identity itself isn&apos;t narrowed for it — give it read-only
                        permissions.
                      </span>
                    </div>
                  )}

                  {basicFields.map(renderField)}

                  {advancedFields.length > 0 && (
                    <div>
                      <Disclosure
                        open={showAdvanced}
                        onToggle={() => setShowAdvanced((v) => !v)}
                        controls={`${idPrefix}-advanced`}
                      >
                        Advanced
                      </Disclosure>
                      {showAdvanced && (
                        <div id={`${idPrefix}-advanced`} className="mt-3 space-y-4">
                          {advancedFields.map(renderField)}
                        </div>
                      )}
                    </div>
                  )}

                  {configured && showForm && !dirty && c.status !== 'error' && unreadable.length === 0 && (
                    <button
                      type="button"
                      onClick={() => setShowForm(false)}
                      className="text-[11px] text-kb-text-tertiary hover:text-kb-text-primary"
                    >
                      Hide {settingsWord}
                    </button>
                  )}
                </div>
              )}
            </section>
          )}

          {/* Danger zone — also on a locked connector, so credentials left
              behind by a plan change can still be deleted. */}
          {configured && (
            <section aria-labelledby={`${idPrefix}-danger`} className="pt-4 border-t border-kb-border">
              <SectionLabel id={`${idPrefix}-danger`}>Danger zone</SectionLabel>
              <div className="flex items-center justify-between gap-3">
                <p className="text-[11px] text-kb-text-secondary leading-relaxed">
                  {secretsStored
                    ? 'Remove the connector and delete its stored credentials.'
                    : 'Remove the connector and its settings.'}
                </p>
                <button
                  type="button"
                  onClick={() => setConfirmRemove(true)}
                  disabled={busy}
                  className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-kb-border text-status-error text-xs font-medium hover:bg-status-error-dim transition-colors shrink-0 disabled:opacity-50"
                >
                  <Trash2 className="w-3.5 h-3.5" aria-hidden />
                  Remove connector
                </button>
              </div>
            </section>
          )}
        </div>

        {/* Footer. Where the install offers no way to sign in and there is
            no plan to point at, it has nothing to hold but a note. */}
        {(locked || offered || note) && (
          <div className="shrink-0 border-t border-kb-border bg-kb-card px-5 py-3 space-y-2.5">
            {/* A plan gate's note gives way to the invitation once the list
                turns the connector locked: the footer offers one upgrade
                action, not the note's and the invitation's side by side. */}
            <div aria-live="polite">
              {note && !(invite && isPlanGateNote(note)) && <FooterNoteView note={note} contactUrl={gateContact} />}
            </div>
            {invite ? (
              <div className="flex items-center justify-between gap-3">
                <span className="inline-flex items-center gap-1.5 text-[11px] text-kb-text-secondary">
                  <Lock className="w-3.5 h-3.5 shrink-0" aria-hidden />
                  {invite.headline}
                </span>
                <PlanUpgradeCta connector={c} contactUrl={contactUrl} className={`${UPGRADE_CTA} shrink-0`} />
              </div>
            ) : offered ? (
              <div className="flex items-center justify-between gap-2">
                {/* No way of signing in offered here (Azure on a multi-tenant
                    install): nothing to test, as there is nothing to save. */}
                {offered ? (
                  <button
                    type="button"
                    onClick={runTest}
                    disabled={testing || coolingFor > 0 || busy}
                    title={cfgDirty ? 'Tests the values in the form without saving them' : undefined}
                    className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-kb-border bg-kb-elevated text-kb-text-primary text-xs hover:bg-kb-card-hover transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    {testing ? (
                      <>
                        <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden />
                        Testing…
                      </>
                    ) : coolingFor > 0 ? (
                      `Test again in ${coolingFor}s`
                    ) : (
                      <>
                        <ShieldCheck className="w-3.5 h-3.5" aria-hidden />
                        Test connection
                      </>
                    )}
                  </button>
                ) : (
                  <span />
                )}
                <div className="flex items-center gap-2">
                  {/* Driven by the stored switches, not by status: a paused
                      connector that still carries an error is paused. Neither
                      shows where the install does not offer the connector: the
                      gateway never binds a mode it does not offer, so Kobi
                      stays off it whatever the switches say, and pausing or
                      resuming would change nothing it does. */}
                  {offered && configured && c.enabled && !c.paused && (
                    <button
                      type="button"
                      onClick={() => pause.mutate(true)}
                      disabled={busy}
                      className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-kb-border bg-kb-elevated text-kb-text-primary text-xs hover:bg-kb-card-hover transition-colors disabled:opacity-50"
                    >
                      {pause.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden /> : <Pause className="w-3.5 h-3.5" aria-hidden />}
                      Pause
                    </button>
                  )}
                  {offered && configured && c.paused && (
                    <button
                      type="button"
                      onClick={() => pause.mutate(false)}
                      disabled={busy}
                      className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border border-kb-border bg-kb-elevated text-kb-text-primary text-xs hover:bg-kb-card-hover transition-colors disabled:opacity-50"
                    >
                      {pause.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden /> : <Play className="w-3.5 h-3.5" aria-hidden />}
                      Resume
                    </button>
                  )}
                  {/* No auth mode offered here (Azure on a multi-tenant install):
                      there is nothing to save, so no disabled Save to puzzle over. */}
                  {offered && (firstSave || dirty || showForm) && (
                    <button
                      type="button"
                      onClick={onSave}
                      disabled={busy || (!firstSave && !dirty)}
                      title={!firstSave && !dirty ? 'Nothing to save yet' : undefined}
                      className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-lg bg-kb-accent text-white text-xs font-semibold hover:opacity-90 transition-opacity disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {save.isPending && <Loader2 className="w-3.5 h-3.5 animate-spin" aria-hidden />}
                      {save.isPending ? 'Saving…' : firstSave ? 'Save and enable' : 'Save changes'}
                    </button>
                  )}
                </div>
              </div>
            ) : null}
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmDiscard}
        badge="Unsaved"
        title="Discard changes?"
        description={`Your edits to the ${c.name} connector haven't been saved.`}
        confirmLabel="Discard"
        variant="danger"
        onConfirm={() => {
          setConfirmDiscard(false)
          onClose()
        }}
        onCancel={() => setConfirmDiscard(false)}
      />
      <ConfirmDialog
        open={confirmRemove}
        badge="Remove"
        title={`Remove the ${c.name} connector?`}
        description={removeCopy}
        confirmLabel="Remove connector"
        variant="danger"
        busy={remove.isPending}
        onConfirm={() => remove.mutate()}
        onCancel={() => setConfirmRemove(false)}
      />
      {/* pointer-events is inherited, and the wrapper is pointer-events-none
          (only the scrim and the panel take clicks): the toast has to opt
          back in or its dismiss button would not take a click. */}
      {toast && (
        <div className="pointer-events-auto">
          <MutationErrorToast error={toast.error} action={toast.action} onDismiss={dismissToast} />
        </div>
      )}
    </div>
  )
}

// Where the identity lives when KubeBolt stores no secret for the connector:
// in the MCP server running next to it (the Azure sidecar), or on the machine
// KubeBolt runs on. That is also where a key is rotated.
function identityHome(mode: McpAuthMode | undefined): string {
  const rest = settingsReach(mode)
  if (mode?.id === 'sidecar') {
    return 'Nothing secret is stored here: the identity lives in the MCP server that runs next to KubeBolt, and its keys are rotated there.' + rest
  }
  if (mode?.selfHostedOnly) {
    return 'Nothing secret is stored here: KubeBolt signs in with the identity of the server it runs on, and its keys are rotated there.' + rest
  }
  return 'Nothing secret is stored here.' + rest
}

function StatusHeadline({
  connector: c,
  planHeadline,
  settingsWord,
  onReview,
}: {
  connector: McpConnector
  // The invitation's headline ("Included from the Team plan"): a connector
  // set up before the plan dropped says the same words as the panel above it.
  planHeadline?: string
  settingsWord: 'credentials' | 'settings'
  onReview?: () => void
}) {
  const status = effectiveStatus(c)
  const offered = isOffered(c)
  const view = !offered
    ? {
        // Saved before this install stopped offering it (the ambient
        // override turned off, or a move to multi-tenant). The dot keeps the
        // stored switches' colour; the words say what they amount to here.
        dot: status === 'error' ? 'bg-status-error' : status === 'paused' ? 'bg-status-warn' : 'bg-kb-text-tertiary',
        title: "Can't be used on this installation",
        sub:
          status === 'paused'
            ? `It was paused before this installation stopped offering it. Kobi doesn't use it here either way; its ${settingsWord} stay saved until you remove it.`
            : `Kobi doesn't use it here, so there is nothing to pause or resume. Its ${settingsWord} stay saved until you remove it.`,
      }
    : status === 'enabled'
      ? { dot: 'bg-kb-accent', title: 'Enabled', sub: 'Kobi uses it when an investigation leads outside the cluster.' }
      : status === 'paused'
        ? {
            dot: 'bg-status-warn',
            title: 'Paused',
            // The error that prompted the pause is still on record: say so,
            // or Resume would look like it fixes it.
            sub: c.lastError
              ? `Kobi won't use it until you resume it. The last call failed: ${c.lastError}`
              : "Kobi won't use it until you resume it.",
          }
        : status === 'error'
          ? {
              dot: 'bg-status-error',
              title: 'Needs attention',
              sub: c.lastError || 'The last call failed. Test the connection to see why.',
            }
          : status === 'plan'
            ? {
                dot: 'bg-kb-text-tertiary',
                // It read "Not included in your plan" under a panel and a
                // footer that both say "Included from the Team plan".
                title: planHeadline || 'Not included in your plan',
                sub: `Kobi can't use it until then; its ${settingsWord} are kept.`,
              }
            : { dot: 'bg-kb-text-tertiary', title: 'Saved, not enabled', sub: 'Save and enable it to let Kobi use it.' }
  return (
    <div className="flex items-start gap-2.5">
      <span className={`w-2 h-2 rounded-full mt-1.5 shrink-0 ${view.dot}`} aria-hidden />
      <div className="min-w-0">
        <div className="text-xs font-semibold text-kb-text-primary">{view.title}</div>
        <div
          className={`text-[11px] leading-relaxed mt-0.5 ${status === 'error' && offered ? 'text-status-error' : 'text-kb-text-secondary'}`}
        >
          {view.sub}
        </div>
        {status === 'error' && onReview && (
          <button type="button" onClick={onReview} className={`${TEXT_ACTION} mt-1.5`}>
            Review {settingsWord}
          </button>
        )}
      </div>
    </div>
  )
}

function SummaryRow({ label, value, mono, warn }: { label: string; value: string; mono?: boolean; warn?: boolean }) {
  return (
    <>
      <dt className="text-kb-text-tertiary">{label}</dt>
      <dd
        className={`min-w-0 truncate ${mono ? 'font-mono' : ''} ${warn ? 'text-kobi-st-warn' : 'text-kb-text-primary'}`}
        title={value}
      >
        {value}
      </dd>
    </>
  )
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1">{label}</div>
      <div className="text-[11px] text-kb-text-primary leading-snug">{value}</div>
    </div>
  )
}

function ConsumerMark({ children, muted }: { children: React.ReactNode; muted?: boolean }) {
  return (
    <span
      className={`px-1.5 py-0.5 rounded text-[10px] font-mono ${
        muted ? 'bg-kb-elevated text-kb-text-secondary' : 'bg-kb-accent-light text-kb-accent'
      }`}
    >
      {children}
    </span>
  )
}

function ToolList({ title, tools }: { title: string; tools: string[] }) {
  if (tools.length === 0) return null
  return (
    <div>
      <div className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1">{title}</div>
      <div className="flex flex-wrap gap-1">
        {tools.map((t) => (
          <span
            key={t}
            className="px-2 py-0.5 rounded-full bg-kb-elevated border border-kb-border text-[10px] font-mono text-kb-text-secondary"
          >
            {t}
          </span>
        ))}
      </div>
    </div>
  )
}

function Disclosure({
  open,
  onToggle,
  controls,
  children,
}: {
  open: boolean
  onToggle: () => void
  controls: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-expanded={open}
      aria-controls={controls}
      className="inline-flex items-center gap-1 text-[11px] font-medium text-kb-text-secondary hover:text-kb-text-primary transition-colors"
    >
      <ChevronDown className={`w-3.5 h-3.5 transition-transform ${open ? '' : '-rotate-90'}`} aria-hidden />
      {children}
    </button>
  )
}

// A plan gate in the footer: the 409 note, or a test the plan refused.
function isPlanGateNote(note: FooterNote): boolean {
  return note.kind === 'plan' || (note.kind === 'test' && !!note.outcome.planGate)
}

function planGateNoteUpgradeTo(note: FooterNote): string {
  if (note.kind === 'plan') return note.upgradeTo
  if (note.kind === 'test') return note.outcome.upgradePlan ?? ''
  return ''
}

// On the light theme's white card neither green reads as 11px text (status-ok
// is below 2:1, the accent 3.65:1), nor does the status amber (about 2:1). So
// the icon carries the tone and the text does not: a success line is neutral
// next to its green check, and warn text takes the per-theme amber.
//
// A plan gate's action follows PlanUpgradeCta's rule (PlanGateLink): Billing
// only for a plan on sale, else the contact address, else nothing.
function FooterNoteView({ note, contactUrl }: { note: FooterNote; contactUrl: string }) {
  if (note.kind === 'saved') {
    return (
      <div className="flex items-start gap-1.5 text-[11px] text-kb-text-primary">
        <Check className="w-3.5 h-3.5 shrink-0 mt-px text-kb-accent" aria-hidden />
        <span>{note.text}</span>
      </div>
    )
  }
  if (note.kind === 'plan') {
    return (
      <div className="flex items-start gap-1.5 text-[11px] text-kb-text-secondary">
        <Lock className="w-3.5 h-3.5 shrink-0 mt-px" aria-hidden />
        <span>
          {note.plan ? `Turning it on needs the ${planLabel(note.plan)} plan. ` : "Your plan doesn't include this connector. "}
          <PlanGateLink upgradeTo={note.upgradeTo} contactUrl={contactUrl} className={TEXT_ACTION} />
        </span>
      </div>
    )
  }
  if (note.kind === 'error') {
    return (
      <div className="flex items-start gap-1.5 text-[11px] text-status-error">
        <AlertTriangle className="w-3.5 h-3.5 shrink-0 mt-px" aria-hidden />
        <span>{note.text}</span>
      </div>
    )
  }
  const o = note.outcome
  const iconTone =
    o.tone === 'ok' ? 'text-kb-accent' : o.tone === 'warn' ? 'text-status-warn' : 'text-status-error'
  const textTone =
    o.tone === 'ok' ? 'text-kb-text-primary' : o.tone === 'warn' ? 'text-kobi-st-warn' : 'text-status-error'
  const Icon = o.tone === 'ok' ? Check : AlertTriangle
  return (
    <div className="flex items-start gap-1.5 text-[11px] min-w-0">
      <Icon className={`w-3.5 h-3.5 shrink-0 mt-px ${iconTone}`} aria-hidden />
      <div className="min-w-0 space-y-0.5">
        <div className={`font-medium ${textTone}`}>{o.title}</div>
        {o.warning && <div className="text-kobi-st-warn">{o.warning}</div>}
        {o.detail && <div className="text-kb-text-secondary break-all">{o.detail}</div>}
        {o.hint && <div className="text-kb-text-secondary">{o.hint}</div>}
        {o.planGate && <PlanGateLink upgradeTo={o.upgradePlan} contactUrl={contactUrl} className={TEXT_ACTION} />}
        {o.raw && (
          <details>
            <summary className="text-[10px] font-mono text-kb-text-tertiary cursor-pointer hover:text-kb-text-secondary">
              Details from the provider
            </summary>
            <pre className="mt-1 text-[10px] font-mono text-kb-text-tertiary whitespace-pre-wrap break-all max-h-32 overflow-auto">
              {o.raw}
            </pre>
          </details>
        )}
      </div>
    </div>
  )
}

