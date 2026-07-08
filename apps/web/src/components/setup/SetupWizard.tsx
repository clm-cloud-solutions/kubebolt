import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Bell,
  Bot,
  Check,
  ChevronDown,
  Copy,
  Eye,
  EyeOff,
  KeyRound,
  Loader2,
  Mail,
  Server,
  Sparkles,
} from 'lucide-react'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import {
  CUSTOM_MODEL_VALUE,
  findModelOption,
  groupModels,
  MODELS_BY_PROVIDER,
  type ProviderID,
} from '@/pages/admin/settings/modelCatalog'

// SetupWizard — optional onboarding overlay. The install path already
// established the only mandatory piece of state (admin password —
// either auto-generated and printed to logs or set via env). Every
// step here is a "nice to have for a complete experience" knob, NOT
// a gate. The wizard surfaces them in one guided pass so a new
// operator doesn't have to discover each tab on their own; anyone can
// dismiss with "Skip wizard" at any point and reach the same forms via
// the Settings tabs later.
//
// All persistence is via existing PUT endpoints — the wizard doesn't
// invent new state, just walks the operator through the same forms.
// Marking complete is a single POST /admin/setup/complete at the end
// (or on skip), so it doesn't re-appear on next login.
//
// Deliberately NOT Esc / outside-click dismissable — the explicit
// "Skip wizard" button is the only way out, which makes the operator
// pause and decide rather than fat-fingering it closed.

type StepIdx = 0 | 1 | 2 | 3

const STEPS = [
  { idx: 0 as StepIdx, label: 'Password', icon: KeyRound, required: false },
  { idx: 1 as StepIdx, label: 'AI Copilot', icon: Bot, required: false },
  { idx: 2 as StepIdx, label: 'Agent', icon: Server, required: false },
  { idx: 3 as StepIdx, label: 'Notifications', icon: Bell, required: false },
]

export function SetupWizard({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient()
  const [step, setStep] = useState<StepIdx>(0)
  const [passwordSet, setPasswordSet] = useState(false)
  const [copilotConfigured, setCopilotConfigured] = useState(false)
  const [notificationsConfigured, setNotificationsConfigured] = useState(false)

  const completeMutation = useMutation({
    mutationFn: () => api.completeSetup(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['setup-status'] })
      onDone()
    },
  })

  function goNext() {
    if (step < 3) {
      setStep((s) => (s + 1) as StepIdx)
    } else {
      completeMutation.mutate()
    }
  }

  function goBack() {
    if (step > 0) setStep((s) => (s - 1) as StepIdx)
  }

  // Every step is optional — "Next" is always enabled. Operators who
  // want to actually save a step's value click that step's inline
  // "Change password" / "Save key" / etc button; "Next" just advances
  // the wizard without acting as a form-submit shortcut.
  const canAdvance = true
  const isLast = step === 3

  return (
    <div className="fixed inset-0 z-[99999] flex items-center justify-center">
      <div className="absolute inset-0 bg-black/80 backdrop-blur-md" />
      <div className="relative w-[90vw] max-w-2xl bg-kb-card border border-kb-border rounded-2xl shadow-2xl flex flex-col max-h-[90vh] overflow-hidden">
        <Header step={step} />

        <main className="flex-1 overflow-y-auto px-6 py-5">
          {/* key={step} re-mounts on each navigation so the CSS
              animation retriggers cleanly. Cheap relative to the
              network calls the steps issue. */}
          <div key={step} className="animate-wizard-step">
            {step === 0 && (
              <StepPassword onDone={() => setPasswordSet(true)} alreadyDone={passwordSet} />
            )}
            {step === 1 && (
              <StepCopilot
                onDone={() => setCopilotConfigured(true)}
                alreadyDone={copilotConfigured}
              />
            )}
            {step === 2 && <StepAgent />}
            {step === 3 && (
              <StepNotifications
                onDone={() => setNotificationsConfigured(true)}
                alreadyDone={notificationsConfigured}
              />
            )}
          </div>
        </main>

        <Footer
          step={step}
          canAdvance={canAdvance}
          isLast={isLast}
          finishing={completeMutation.isPending}
          onBack={goBack}
          onNext={goNext}
          onSkipWizard={() => completeMutation.mutate()}
        />
      </div>
    </div>
  )
}

function Header({ step }: { step: StepIdx }) {
  return (
    <header className="border-b border-kb-border px-6 py-4 shrink-0">
      <div className="flex items-center gap-2 mb-1">
        <Sparkles className="w-4 h-4 text-kb-accent" />
        <h1 className="text-sm font-semibold text-kb-text-primary">Finish setting up KubeBolt</h1>
      </div>
      <p className="text-[11px] text-kb-text-tertiary mb-3 leading-snug">
        Every step is optional — your install is already usable. Skip any time.
      </p>
      <ol className="flex items-center gap-2">
        {STEPS.map((s, i) => {
          const active = s.idx === step
          const done = s.idx < step
          return (
            <li key={s.idx} className="flex items-center gap-2 min-w-0">
              <div
                // key={done} forces a remount on the done→pending boundary
                // so the chip-pop animation retriggers as each step ticks
                // green — adds a small reward beat to advancing.
                key={`${done}`}
                className={`flex items-center gap-1.5 px-2 py-1 rounded-md text-[10px] font-mono uppercase tracking-wider transition-colors ${
                  active
                    ? 'bg-kb-accent-light text-kb-accent'
                    : done
                    ? 'bg-status-ok-dim text-status-ok animate-wizard-chip-pop'
                    : 'bg-kb-elevated text-kb-text-tertiary'
                }`}
              >
                {done ? <Check className="w-3 h-3" /> : <s.icon className="w-3 h-3" />}
                <span>{s.label}</span>
              </div>
              {i < STEPS.length - 1 && (
                <div
                  className={`h-px transition-all duration-300 ${
                    done ? 'w-6 bg-status-ok' : 'w-4 bg-kb-border'
                  }`}
                />
              )}
            </li>
          )
        })}
      </ol>
    </header>
  )
}

function Footer({
  step,
  canAdvance,
  isLast,
  finishing,
  onBack,
  onNext,
  onSkipWizard,
}: {
  step: StepIdx
  canAdvance: boolean
  isLast: boolean
  finishing: boolean
  onBack: () => void
  onNext: () => void
  onSkipWizard: () => void
}) {
  return (
    <footer className="border-t border-kb-border px-6 py-3 flex items-center justify-between gap-3 shrink-0">
      <button
        type="button"
        onClick={onBack}
        disabled={step === 0}
        className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <ArrowLeft className="w-3.5 h-3.5" />
        Back
      </button>
      <div className="flex items-center gap-2">
        {/* Skip wizard entirely — available on every step. Every knob
            in here is optional (the install already established the
            admin credential), so the operator can dismiss at any time.
            Marks setup complete without filling in the rest. */}
        <button
          type="button"
          onClick={onSkipWizard}
          disabled={finishing}
          className="px-3 py-1.5 rounded-md text-xs text-kb-text-tertiary hover:text-kb-text-secondary"
        >
          Skip wizard
        </button>
        <button
          type="button"
          onClick={onNext}
          disabled={!canAdvance || finishing}
          className="flex items-center gap-1.5 px-4 py-1.5 rounded-md text-xs font-medium bg-kb-accent text-kb-bg disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {finishing ? (
            <>
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
              Finishing…
            </>
          ) : isLast ? (
            <>
              <Check className="w-3.5 h-3.5" />
              Finish
            </>
          ) : (
            <>
              Next
              <ArrowRight className="w-3.5 h-3.5" />
            </>
          )}
        </button>
      </div>
    </footer>
  )
}

// ─── Step 1: change admin password ───────────────────────────────────

function StepPassword({ onDone, alreadyDone }: { onDone: () => void; alreadyDone: boolean }) {
  const { user } = useAuth()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [reveal, setReveal] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const mutation = useMutation({
    mutationFn: () => api.changePassword(current, next),
    onSuccess: () => {
      setError(null)
      onDone()
    },
    onError: (e: Error) => setError(e.message),
  })

  function handleSave(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    if (next.length < 8) {
      setError('New password must be at least 8 characters.')
      return
    }
    if (next !== confirm) {
      setError('Confirmation does not match the new password.')
      return
    }
    if (next === current) {
      setError('New password must differ from the current one.')
      return
    }
    mutation.mutate()
  }

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-base font-semibold text-kb-text-primary mb-1">Admin password (optional)</h2>
        <p className="text-xs text-kb-text-secondary leading-relaxed">
          Your install already set an admin password — either via{' '}
          <code className="font-mono text-kb-accent">KUBEBOLT_ADMIN_PASSWORD</code> or
          auto-generated and printed to the API logs on first boot. Change it here if you'd
          prefer something only you know, or skip — you can always change it later from the
          account menu.
        </p>
        {user?.username && (
          <p className="text-[11px] text-kb-text-tertiary mt-1 font-mono">User: {user.username}</p>
        )}
      </div>

      {alreadyDone ? (
        <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">
          <Check className="w-4 h-4 mt-0.5 shrink-0" />
          <div>Password changed. Continue to the next step when ready.</div>
        </div>
      ) : (
        <form onSubmit={handleSave} className="space-y-3">
          <SecretInput
            label="Current password"
            value={current}
            onChange={setCurrent}
            reveal={reveal}
            onToggleReveal={() => setReveal((v) => !v)}
            placeholder="The auto-generated one from the boot logs"
            autoFocus
          />
          <SecretInput
            label="New password"
            value={next}
            onChange={setNext}
            reveal={reveal}
            onToggleReveal={() => setReveal((v) => !v)}
            placeholder="At least 8 characters"
          />
          <SecretInput
            label="Confirm new password"
            value={confirm}
            onChange={setConfirm}
            reveal={reveal}
            onToggleReveal={() => setReveal((v) => !v)}
          />

          {error && (
            <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
              <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
              <div>{error}</div>
            </div>
          )}

          <button
            type="submit"
            disabled={mutation.isPending || !current || !next || !confirm}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-kb-accent text-kb-bg disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {mutation.isPending ? (
              <>
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
                Updating…
              </>
            ) : (
              <>
                <KeyRound className="w-3.5 h-3.5" />
                Change password
              </>
            )}
          </button>
        </form>
      )}
    </div>
  )
}

// ─── Step 2: AI Copilot (optional) ────────────────────────────────────
//
// Covers the PRIMARY provider only — fallback is power-user territory
// and lives in Settings → AI Copilot for operators who care. Loads the
// current effective Copilot config so an env-driven baseline doesn't
// get accidentally wiped by submitting partial overrides.

function StepCopilot({ onDone, alreadyDone }: { onDone: () => void; alreadyDone: boolean }) {
  const queryClient = useQueryClient()
  const { data: existing } = useQuery({
    queryKey: ['admin', 'settings', 'copilot'],
    queryFn: api.getSettingsCopilot,
  })
  const eff = existing?.effective

  const [provider, setProvider] = useState<ProviderID>('anthropic')
  const [model, setModel] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [maxTokens, setMaxTokens] = useState('4096')
  const [showToolCalls, setShowToolCalls] = useState(true)
  const [autoCompact, setAutoCompact] = useState(true)
  const [reveal, setReveal] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [seeded, setSeeded] = useState(false)

  // Seed once from the effective config so the operator sees the
  // boot baseline (KUBEBOLT_AI_*) instead of empty defaults. After
  // seed they own the form state.
  useEffect(() => {
    if (seeded || !eff) return
    setProvider((eff.provider as ProviderID) || 'anthropic')
    setModel(eff.model || '')
    setBaseURL(eff.baseURL || '')
    setMaxTokens(String(eff.maxTokens || 4096))
    setShowToolCalls(eff.showToolCalls)
    setAutoCompact(eff.autoCompact)
    setSeeded(true)
  }, [eff, seeded])

  const mutation = useMutation({
    mutationFn: () => {
      const primaryPatch: Record<string, string> = { provider }
      if (model) primaryPatch.model = model
      if (baseURL) primaryPatch.baseURL = baseURL
      const mt = parseInt(maxTokens, 10)
      return api.putSettingsCopilot({
        patch: {
          primary: primaryPatch,
          maxTokens: !isNaN(mt) && mt > 0 ? mt : undefined,
          showToolCalls,
          autoCompact,
        },
        // Send key only if the operator actually typed one — empty
        // means "keep whatever's there" (env baseline or prior save).
        plaintextAPIKey: apiKey.trim() ? apiKey : undefined,
      })
    },
    onSuccess: (newData) => {
      setError(null)
      // Seed the Settings → AI Copilot tab's cache so the change is
      // visible there immediately after the wizard saves. Also bust
      // the public /copilot/config cache so the chat panel's pill
      // flips to the new provider/model without a reload.
      queryClient.setQueryData(['admin', 'settings', 'copilot'], newData)
      queryClient.invalidateQueries({ queryKey: ['copilot-config'] })
      onDone()
    },
    onError: (e: Error) => setError(e.message),
  })

  function handleSave(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    // Key is required ONLY when no key is already configured. If the
    // operator's env or a prior save already set one, the wizard can
    // safely save model/maxTokens/etc without forcing a re-paste.
    const hasExistingKey = Boolean(eff?.apiKeyMasked)
    if (!hasExistingKey && apiKey.trim().length < 8) {
      setError('Paste your provider API key (typically 30+ characters) to enable Kobi.')
      return
    }
    mutation.mutate()
  }

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-base font-semibold text-kb-text-primary mb-1">Connect AI Copilot (Kobi)</h2>
        <p className="text-xs text-kb-text-secondary leading-relaxed">
          KubeBolt's chat assistant uses YOUR LLM provider — bring your own key. Skip if you want to set it up later; the UI works fully without Kobi.
        </p>
      </div>

      {alreadyDone ? (
        <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">
          <Check className="w-4 h-4 mt-0.5 shrink-0" />
          <div>Saved. Tune fallback model, auto-compact thresholds and other advanced knobs from Settings → AI Copilot.</div>
        </div>
      ) : (
        <form onSubmit={handleSave} className="space-y-3">
          <Field label="Provider">
            <select
              value={provider}
              onChange={(e) => {
                const nextProvider = e.target.value as ProviderID
                // Switching provider invalidates the current model
                // selection if it doesn't exist in the new catalog.
                const modelCarryOver = findModelOption(nextProvider, model) ? model : ''
                setProvider(nextProvider)
                setModel(modelCarryOver)
              }}
              className="w-full max-w-md px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            >
              <option value="anthropic">Anthropic (Claude)</option>
              <option value="openai">OpenAI + compatible (GPT, Grok, Qwen, DeepSeek, Llama, Mistral)</option>
            </select>
          </Field>

          <Field label="Model">
            <WizardModelPicker provider={provider} value={model} onChange={setModel} />
          </Field>

          <Field
            label="API key"
            helper={
              eff?.apiKeyMasked
                ? `Currently set: ${eff.apiKeyMasked}. Leave blank to keep, or paste to replace.`
                : 'Required to enable Kobi.'
            }
          >
            <SecretInputInline
              value={apiKey}
              onChange={setApiKey}
              reveal={reveal}
              onToggleReveal={() => setReveal((v) => !v)}
              placeholder={
                eff?.apiKeyMasked
                  ? '••••••••'
                  : provider === 'anthropic'
                  ? 'sk-ant-...'
                  : 'sk-... or xai-...'
              }
            />
          </Field>

          <Field
            label="Base URL (optional)"
            helper="Override the provider endpoint — needed for OpenAI-compatible gateways like xAI Grok (api.x.ai/v1), DeepSeek, Groq, Together."
          >
            <input
              type="text"
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder={provider === 'anthropic' ? 'https://api.anthropic.com (default)' : 'https://api.openai.com/v1 (default)'}
              className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
            />
          </Field>

          <Field label="Max tokens" helper="Cap on output tokens per provider call. Bump if Kobi truncates long answers.">
            <input
              type="number"
              min={1}
              value={maxTokens}
              onChange={(e) => setMaxTokens(e.target.value)}
              className="w-32 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            />
          </Field>

          <label className="flex items-start gap-2 text-xs text-kb-text-secondary cursor-pointer">
            <input
              type="checkbox"
              checked={showToolCalls}
              onChange={(e) => setShowToolCalls(e.target.checked)}
              className="accent-kb-accent mt-0.5"
            />
            <div>
              <div className="text-kb-text-primary">Show tool call cards in chat</div>
              <div className="text-kb-text-tertiary">Persistent cards for each tool Kobi invokes. Off: only the final assistant text remains visible.</div>
            </div>
          </label>

          <label className="flex items-start gap-2 text-xs text-kb-text-secondary cursor-pointer">
            <input
              type="checkbox"
              checked={autoCompact}
              onChange={(e) => setAutoCompact(e.target.checked)}
              className="accent-kb-accent mt-0.5"
            />
            <div>
              <div className="text-kb-text-primary">Auto-compact long sessions</div>
              <div className="text-kb-text-tertiary">Summarize older turns when the session approaches the context window. Off: long sessions hit the budget and fail.</div>
            </div>
          </label>

          <p className="text-[11px] text-kb-text-tertiary">
            Fallback model + auto-compact thresholds are tunable from Settings → AI Copilot after you finish the wizard.
          </p>

          {error && (
            <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
              <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
              <div>{error}</div>
            </div>
          )}

          <button
            type="submit"
            disabled={mutation.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-kb-accent text-kb-bg disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {mutation.isPending ? (
              <>
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
                Saving…
              </>
            ) : (
              <>
                <Bot className="w-3.5 h-3.5" />
                Save settings
              </>
            )}
          </button>
        </form>
      )}
    </div>
  )
}

// WizardModelPicker is a slimmed-down variant of the Settings tab's
// ModelPicker — same provider catalog + optgroups + "Custom…" escape
// hatch, no model description popover. Operators wanting the full
// picker with trade-off blurbs head to Settings → AI Copilot post-
// wizard.
function WizardModelPicker({
  provider,
  value,
  onChange,
}: {
  provider: ProviderID
  value: string
  onChange: (model: string) => void
}) {
  const groups = groupModels(MODELS_BY_PROVIDER[provider] ?? [])
  const matched = findModelOption(provider, value)
  const isCustom = value !== '' && !matched
  const selectValue = isCustom ? CUSTOM_MODEL_VALUE : value

  return (
    <div className="space-y-1.5">
      <select
        className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
        value={selectValue}
        onChange={(e) => {
          const next = e.target.value
          if (next === CUSTOM_MODEL_VALUE) {
            if (!isCustom) onChange('')
            return
          }
          onChange(next)
        }}
      >
        <option value="" disabled>Select a model…</option>
        {groups.map((g) => (
          <optgroup key={g.group} label={g.group}>
            {g.items.map((opt) => (
              <option key={opt.id} value={opt.id}>{opt.label}</option>
            ))}
          </optgroup>
        ))}
        <option value={CUSTOM_MODEL_VALUE}>Custom…</option>
      </select>
      {isCustom && (
        <input
          type="text"
          placeholder="e.g. my-custom-model-v2"
          autoFocus
          className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
    </div>
  )
}

// ─── Step 3: Agent install snippet ────────────────────────────────────

function StepAgent() {
  const [copiedKey, setCopiedKey] = useState<string | null>(null)

  // Spec #09 V2 — when the backend's gRPC channel runs `enforced` (or
  // `permissive`), the agent needs an ingest token Secret mounted into
  // its pod. Pull the current channel auth mode + tenant list so the
  // wizard can offer one-click token issuance, and tailor the helm
  // command with the Secret reference.
  const { data: channel } = useQuery({
    queryKey: ['admin', 'settings', 'ingest-channel'],
    queryFn: api.getSettingsIngestChannel,
    // Mid-wizard the channel mode shouldn't change under us, but
    // refetch on focus catches the "operator opened another tab,
    // flipped to enforced, came back here" case.
    staleTime: 30_000,
  })
  const { data: authInfo } = useQuery({
    queryKey: ['agent-auth-info'],
    queryFn: () => api.getAgentAuthInfo(),
    staleTime: 30_000,
  })

  const channelAuthMode = channel?.effective.agentAuthMode ?? 'disabled'
  const needsToken = channelAuthMode === 'enforced' || channelAuthMode === 'permissive'

  const [issuedSecret, setIssuedSecret] = useState<{ secretName: string; namespace: string; tokenPrefix: string } | null>(null)
  const issueToken = useMutation({
    mutationFn: () => {
      const firstActive = authInfo?.tenants?.find((t) => !t.disabled)
      const tenantId = firstActive?.id || ''
      if (!tenantId) {
        throw new Error('No active tenant available — issue tokens from Admin → Agent Tokens')
      }
      return api.issueAgentTokenAndMaterializeSecret({
        tenantId,
        materialize: true, // first-run install into a backend-reachable cluster — create the Secret in one click
        namespace: 'kubebolt',
        secretName: 'kubebolt-agent-token',
        label: `wizard ${new Date().toISOString().slice(0, 10)}`,
      })
    },
    onSuccess: (resp) => {
      // Backend already wrote the plaintext into the K8s Secret —
      // it never round-trips back to the UI. The helm chart's
      // projected volume reads the Secret on agent pod start. We
      // only need name + namespace + the tokenPrefix to display the
      // operator-facing identity ("you issued token kbat_a1b2…").
      setIssuedSecret({
        secretName: resp.secretName ?? '',
        namespace: resp.namespace ?? '',
        tokenPrefix: resp.tokenPrefix,
      })
    },
  })

  // Helm command tailored to the auth posture. Without a token: bare
  // install. With a token (after issueToken success): add auth.mode +
  // auth.ingestTokenSecret so the Helm chart wires the Secret into the
  // agent DaemonSet's projected volume.
  const helmCmd = (() => {
    const base = `helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent \\
  --namespace kubebolt --create-namespace`
    if (issuedSecret) {
      return `${base} \\
  --set auth.mode=ingest-token \\
  --set auth.ingestTokenSecret=${issuedSecret.secretName}`
    }
    return base
  })()

  function copy(key: string, text: string) {
    navigator.clipboard.writeText(text)
    setCopiedKey(key)
    setTimeout(() => setCopiedKey(null), 1500)
  }

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-base font-semibold text-kb-text-primary mb-1">Install the agent</h2>
        <p className="text-xs text-kb-text-secondary leading-relaxed">
          The agent is a DaemonSet that ships kubelet metrics + Cilium flow events from each node into KubeBolt. The UI works without it but you'll miss network telemetry and per-node breakdowns.
        </p>
      </div>

      {/* Auth posture banner — only renders when the channel is in
          enforced/permissive. Tells the operator they need a token
          BEFORE they paste the helm command, and offers the one-click
          issue button so they don't have to leave the wizard. */}
      {needsToken && !issuedSecret && (
        <div className="rounded-md border border-status-info-dim bg-status-info-dim/30 p-3 text-xs space-y-2">
          <div className="flex items-start gap-2 text-status-info">
            <KeyRound className="w-4 h-4 mt-0.5 shrink-0" />
            <div>
              <div className="font-semibold">
                Channel auth: <code className="font-mono">{channelAuthMode}</code>
              </div>
              <div className="text-kb-text-secondary mt-0.5 leading-relaxed">
                The backend is configured to require credentials. Generate an ingest token now
                — the wizard will materialize a Kubernetes Secret in the{' '}
                <code className="font-mono">kubebolt</code> namespace and add the right flags to
                the helm command below.
              </div>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => issueToken.mutate()}
              disabled={issueToken.isPending}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-kb-accent text-white hover:opacity-90 disabled:opacity-40"
            >
              {issueToken.isPending ? (
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
              ) : (
                <KeyRound className="w-3.5 h-3.5" />
              )}
              Generate token + create Secret
            </button>
            {issueToken.isError && (
              <span className="text-[11px] text-status-error">
                {(issueToken.error as Error).message}
              </span>
            )}
          </div>
        </div>
      )}

      {/* Post-issuance card. The plaintext token NEVER round-trips
          to the UI — the backend wrote it straight into a K8s Secret
          which the agent pod will read via projected volume. We only
          surface the secret name (for the helm flag) and the
          tokenPrefix so the operator can later identify the token in
          Admin → Agent Tokens if they need to revoke it. */}
      {issuedSecret && (
        <div className="rounded-md border border-status-ok-dim bg-status-ok-dim/30 p-3 text-xs space-y-2">
          <div className="flex items-start gap-2 text-status-ok">
            <Check className="w-4 h-4 mt-0.5 shrink-0" />
            <div>
              <div className="font-semibold">
                Secret <code className="font-mono">{issuedSecret.namespace}/{issuedSecret.secretName}</code> created
              </div>
              <div className="text-kb-text-secondary mt-0.5 leading-relaxed">
                Token prefix <code className="font-mono">{issuedSecret.tokenPrefix}</code> — find
                it in Admin → Agent Tokens to rotate or revoke. The helm command below already
                references the Secret by name.
              </div>
            </div>
          </div>
        </div>
      )}

      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-[10px] font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider">
            Helm install
          </span>
          <button
            type="button"
            onClick={() => copy('helm', helmCmd)}
            className="flex items-center gap-1 px-2 py-0.5 rounded text-[10px] text-kb-text-secondary hover:bg-kb-elevated"
          >
            {copiedKey === 'helm' ? (
              <>
                <Check className="w-3 h-3 text-status-ok" />
                Copied
              </>
            ) : (
              <>
                <Copy className="w-3 h-3" />
                Copy
              </>
            )}
          </button>
        </div>
        <pre className="bg-kb-bg border border-kb-border rounded-md p-3 text-[11px] font-mono text-kb-text-primary overflow-x-auto whitespace-pre">
{helmCmd}
        </pre>
      </div>

      <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
        {needsToken
          ? 'Run the command above after the Secret has been created. Need more tokens or a different tenant? Admin → Agent Tokens.'
          : 'Channel auth is on disabled — no token needed. For multi-cluster fleets, switch the channel to enforced via Settings → Agents & Ingest and re-run this wizard for token issuance.'}
      </p>
    </div>
  )
}

// ─── Step 4: Notifications (optional) ────────────────────────────────
//
// Mirrors the Settings → Notifications tab's coverage: global toggles
// + per-channel enable + Slack/Discord webhooks + SMTP. Seeds from
// the current effective config so existing env-baseline values don't
// get wiped by partial overrides on save.

function StepNotifications({ onDone, alreadyDone }: { onDone: () => void; alreadyDone: boolean }) {
  const queryClient = useQueryClient()
  const { data: existing } = useQuery({
    queryKey: ['admin', 'settings', 'notifications'],
    queryFn: api.getSettingsNotifications,
  })
  const eff = existing?.effective

  // Global
  const [minSeverity, setMinSeverity] = useState('warning')
  const [cooldownSeconds, setCooldownSeconds] = useState('3600')
  const [baseURL, setBaseURL] = useState('')
  const [includeResolved, setIncludeResolved] = useState(false)

  // Slack / Discord
  const [slackEnabled, setSlackEnabled] = useState(true)
  const [slackURL, setSlackURL] = useState('')
  const [revealSlack, setRevealSlack] = useState(false)
  const [discordEnabled, setDiscordEnabled] = useState(true)
  const [discordURL, setDiscordURL] = useState('')
  const [revealDiscord, setRevealDiscord] = useState(false)

  // Email
  const [emailEnabled, setEmailEnabled] = useState(false)
  const [emailHost, setEmailHost] = useState('')
  const [emailPort, setEmailPort] = useState('587')
  const [emailUsername, setEmailUsername] = useState('')
  const [emailPassword, setEmailPassword] = useState('')
  const [revealEmailPassword, setRevealEmailPassword] = useState(false)
  const [emailFrom, setEmailFrom] = useState('')
  const [emailTo, setEmailTo] = useState('')
  const [emailDigestMode, setEmailDigestMode] = useState('instant')

  const [error, setError] = useState<string | null>(null)
  const [seeded, setSeeded] = useState(false)

  useEffect(() => {
    if (seeded || !eff) return
    setMinSeverity(eff.minSeverity || 'warning')
    setCooldownSeconds(String(eff.cooldownSeconds ?? 3600))
    setBaseURL(eff.baseURL || '')
    setIncludeResolved(eff.includeResolved)
    setSlackEnabled(eff.slackEnabled)
    setDiscordEnabled(eff.discordEnabled)
    setEmailEnabled(eff.emailEnabled)
    setEmailHost(eff.emailHost || '')
    setEmailPort(String(eff.emailPort ?? 587))
    setEmailUsername(eff.emailUsername || '')
    setEmailFrom(eff.emailFrom || '')
    setEmailTo((eff.emailTo || []).join(', '))
    setEmailDigestMode(eff.emailDigestMode || 'instant')
    setSeeded(true)
  }, [eff, seeded])

  const mutation = useMutation({
    mutationFn: () => {
      const cd = parseInt(cooldownSeconds, 10)
      const port = parseInt(emailPort, 10)
      const to = emailTo
        .split(',')
        .map((s) => s.trim())
        .filter((s) => s.length > 0)
      return api.putSettingsNotifications({
        patch: {
          global: {
            minSeverity,
            cooldownSeconds: !isNaN(cd) && cd >= 0 ? cd : undefined,
            baseURL,
            includeResolved,
          },
          slack: { enabled: slackEnabled },
          discord: { enabled: discordEnabled },
          email: {
            enabled: emailEnabled,
            host: emailHost,
            port: !isNaN(port) && port > 0 ? port : undefined,
            username: emailUsername,
            from: emailFrom,
            to,
            digestMode: emailDigestMode,
          },
        },
        plaintextSlackWebhookURL: slackURL.trim() || undefined,
        plaintextDiscordWebhookURL: discordURL.trim() || undefined,
        plaintextSMTPPassword: emailPassword.trim() || undefined,
      })
    },
    onSuccess: (newData) => {
      setError(null)
      // Seed the Settings → Notifications tab's cache so the wizard's
      // change is visible immediately after Finish. Without this the
      // tab keeps its stale data until the next refetch tick.
      queryClient.setQueryData(['admin', 'settings', 'notifications'], newData)
      // Also bust the legacy /notifications/config consumer (if any
      // surface is still reading it during the transition).
      queryClient.invalidateQueries({ queryKey: ['notifications-config'] })
      onDone()
    },
    onError: (e: Error) => setError(e.message),
  })

  function handleSave(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    mutation.mutate()
  }

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-base font-semibold text-kb-text-primary mb-1">Insight notifications</h2>
        <p className="text-xs text-kb-text-secondary leading-relaxed">
          KubeBolt's insights engine detects crash loops, OOMs, NetworkPolicy gaps, etc. Pipe them to Slack, Discord or email so on-call sees them outside the UI. Per-tenant overrides and test-send live in Settings → Notifications.
        </p>
      </div>

      {alreadyDone ? (
        <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">
          <Check className="w-4 h-4 mt-0.5 shrink-0" />
          <div>Notifications saved. Test each channel from Settings → Notifications when ready.</div>
        </div>
      ) : (
        <form onSubmit={handleSave} className="space-y-4">
          {/* Global — applies to every channel */}
          <MiniCard title="Global" subtitle="Severity threshold, dedup window, and 'view in KubeBolt' link.">
            <Field label="Minimum severity" helper="Insights below this level are dropped before dispatch.">
              <select
                value={minSeverity}
                onChange={(e) => setMinSeverity(e.target.value)}
                className="w-full max-w-md px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
              >
                <option value="info">Info — every insight notifies</option>
                <option value="warning">Warning — and above (recommended)</option>
                <option value="critical">Critical — only critical insights</option>
              </select>
            </Field>

            <Field label="Cooldown (seconds)" helper="Same insight + resource won't re-notify within this window. 3600 = 1h.">
              <input
                type="number"
                min={0}
                value={cooldownSeconds}
                onChange={(e) => setCooldownSeconds(e.target.value)}
                className="w-40 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
              />
            </Field>

            <Field label="Base URL" helper="Optional. Shown as a 'View in KubeBolt' link in every message.">
              <input
                type="text"
                value={baseURL}
                onChange={(e) => setBaseURL(e.target.value)}
                placeholder="https://kubebolt.example.com"
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
              />
            </Field>

            <label className="flex items-start gap-2 text-xs text-kb-text-secondary cursor-pointer">
              <input
                type="checkbox"
                checked={includeResolved}
                onChange={(e) => setIncludeResolved(e.target.checked)}
                className="accent-kb-accent mt-0.5"
              />
              <div>
                <div className="text-kb-text-primary">Notify on resolved insights</div>
                <div className="text-kb-text-tertiary">Also send a message when an active insight clears. Off: only new detections trigger.</div>
              </div>
            </label>
          </MiniCard>

          {/* Slack */}
          <MiniCard
            title="Slack"
            subtitle="Incoming-webhook integration from your Slack app."
            collapsible
            // Always start collapsed so the step's vertical surface
            // stays compact at first glance — the operator expands
            // only the channels they care about. The "configured"
            // hint in the field helper still surfaces existing state
            // when they open the card.
            defaultOpen={false}
            headerRight={
              <ToggleLabel checked={slackEnabled} onChange={setSlackEnabled} label="Enabled" />
            }
          >
            <Field
              label="Webhook URL"
              helper={
                eff?.slackWebhookMasked
                  ? `Currently set: ${eff.slackWebhookMasked}. Leave blank to keep, or paste to replace.`
                  : 'Paste an incoming-webhook URL to enable delivery.'
              }
            >
              <SecretInputInline
                value={slackURL}
                onChange={setSlackURL}
                reveal={revealSlack}
                onToggleReveal={() => setRevealSlack((v) => !v)}
                placeholder={eff?.slackWebhookMasked ? '••••••••' : 'https://hooks.slack.com/services/...'}
              />
            </Field>
          </MiniCard>

          {/* Discord */}
          <MiniCard
            title="Discord"
            subtitle="Channel-integration webhook URL."
            collapsible
            defaultOpen={false}
            headerRight={
              <ToggleLabel checked={discordEnabled} onChange={setDiscordEnabled} label="Enabled" />
            }
          >
            <Field
              label="Webhook URL"
              helper={
                eff?.discordWebhookMasked
                  ? `Currently set: ${eff.discordWebhookMasked}. Leave blank to keep, or paste to replace.`
                  : 'Paste a channel webhook URL to enable delivery.'
              }
            >
              <SecretInputInline
                value={discordURL}
                onChange={setDiscordURL}
                reveal={revealDiscord}
                onToggleReveal={() => setRevealDiscord((v) => !v)}
                placeholder={eff?.discordWebhookMasked ? '••••••••' : 'https://discord.com/api/webhooks/...'}
              />
            </Field>
          </MiniCard>

          {/* Email */}
          <MiniCard
            icon={<Mail className="w-3.5 h-3.5 text-kb-text-tertiary" />}
            title="Email (SMTP)"
            subtitle="Send insights as emails through your SMTP relay."
            collapsible
            defaultOpen={false}
            headerRight={
              <ToggleLabel checked={emailEnabled} onChange={setEmailEnabled} label="Enabled" />
            }
          >
            <div className="grid grid-cols-2 gap-3">
              <Field label="Host">
                <input
                  type="text"
                  value={emailHost}
                  onChange={(e) => setEmailHost(e.target.value)}
                  placeholder="smtp.example.com"
                  className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
                />
              </Field>
              <Field label="Port">
                <input
                  type="number"
                  min={1}
                  max={65535}
                  value={emailPort}
                  onChange={(e) => setEmailPort(e.target.value)}
                  className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
                />
              </Field>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <Field label="Username">
                <input
                  type="text"
                  value={emailUsername}
                  onChange={(e) => setEmailUsername(e.target.value)}
                  className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
                />
              </Field>
              <Field
                label="Password"
                helper={
                  eff?.emailPasswordMasked
                    ? `Currently set: ${eff.emailPasswordMasked}. Leave blank to keep.`
                    : 'Optional for unauthenticated SMTP.'
                }
              >
                <SecretInputInline
                  value={emailPassword}
                  onChange={setEmailPassword}
                  reveal={revealEmailPassword}
                  onToggleReveal={() => setRevealEmailPassword((v) => !v)}
                  placeholder={eff?.emailPasswordMasked ? '••••••••' : ''}
                />
              </Field>
            </div>

            <Field
              label="From"
              helper="Accepts plain (alerts@example.com) or display-name form (KubeBolt Alerts <alerts@example.com>)."
            >
              <input
                type="text"
                value={emailFrom}
                onChange={(e) => setEmailFrom(e.target.value)}
                placeholder="KubeBolt Alerts <alerts@example.com>"
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
              />
            </Field>

            <Field label="Recipients" helper="Comma-separated. Each entry can be plain or display-name form.">
              <input
                type="text"
                value={emailTo}
                onChange={(e) => setEmailTo(e.target.value)}
                placeholder="oncall@example.com, sre@example.com"
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
              />
            </Field>

            <Field label="Digest mode" helper="Instant: one email per insight. Hourly/daily: buffered summary.">
              <select
                value={emailDigestMode}
                onChange={(e) => setEmailDigestMode(e.target.value)}
                className="w-full max-w-md px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
              >
                <option value="instant">Instant</option>
                <option value="hourly">Hourly digest</option>
                <option value="daily">Daily digest</option>
              </select>
            </Field>
          </MiniCard>

          {error && (
            <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
              <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
              <div>{error}</div>
            </div>
          )}

          <button
            type="submit"
            disabled={mutation.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-kb-accent text-kb-bg disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {mutation.isPending ? (
              <>
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
                Saving…
              </>
            ) : (
              <>
                <Bell className="w-3.5 h-3.5" />
                Save notifications
              </>
            )}
          </button>
        </form>
      )}
    </div>
  )
}

// MiniCard wraps a labelled subsection within a wizard step. Slimmer
// chrome than the Settings tab's SectionCard so a stack of 4 of them
// (Global + Slack + Discord + Email) stays readable.
//
// Set `collapsible` to make the body toggleable from the header.
// `defaultOpen` controls the initial state (e.g. open the cards for
// channels that are already configured). The header's right-side
// content (typically the per-channel enable toggle) stays reachable
// even when collapsed so the user can enable/disable without
// expanding; clicks inside that area don't bubble to the row toggler.
function MiniCard({
  icon,
  title,
  subtitle,
  headerRight,
  collapsible,
  defaultOpen = true,
  children,
}: {
  icon?: React.ReactNode
  title: string
  subtitle?: string
  headerRight?: React.ReactNode
  collapsible?: boolean
  defaultOpen?: boolean
  children: React.ReactNode
}) {
  const [open, setOpen] = useState(defaultOpen)
  const isOpen = collapsible ? open : true

  const headerInner = (
    <>
      <div className="flex items-start gap-1.5 min-w-0">
        {collapsible && (
          <ChevronDown
            className={`w-3.5 h-3.5 mt-0.5 text-kb-text-tertiary transition-transform ${
              isOpen ? '' : '-rotate-90'
            }`}
          />
        )}
        {icon}
        <div className="min-w-0">
          <h3 className="text-xs font-semibold text-kb-text-primary">{title}</h3>
          {subtitle && (
            <p className="text-[10px] text-kb-text-tertiary mt-0.5 leading-snug">{subtitle}</p>
          )}
        </div>
      </div>
      {headerRight && (
        // Click-eats wrapper: the enable toggle lives here. We don't
        // want toggling the channel on/off to ALSO expand/collapse the
        // card — those are unrelated affordances.
        <div className="shrink-0" onClick={(e) => e.stopPropagation()}>
          {headerRight}
        </div>
      )}
    </>
  )

  return (
    <section className="bg-kb-bg/40 border border-kb-border rounded-lg">
      {collapsible ? (
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          className={`w-full flex items-start justify-between gap-3 px-3 py-2 text-left hover:bg-kb-card-hover transition-colors ${
            isOpen ? 'border-b border-kb-border' : ''
          }`}
          aria-expanded={isOpen}
        >
          {headerInner}
        </button>
      ) : (
        <header className="flex items-start justify-between gap-3 px-3 py-2 border-b border-kb-border">
          {headerInner}
        </header>
      )}
      {isOpen && <div className="px-3 py-3 space-y-3">{children}</div>}
    </section>
  )
}

function ToggleLabel({
  checked,
  onChange,
  label,
}: {
  checked: boolean
  onChange: (v: boolean) => void
  label: string
}) {
  return (
    <label className="flex items-center gap-1.5 text-[11px] text-kb-text-secondary cursor-pointer">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="accent-kb-accent"
      />
      {label}
    </label>
  )
}

// ─── Shared primitives ────────────────────────────────────────────────

function Field({
  label,
  helper,
  children,
}: {
  label: string
  helper?: string
  children: React.ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <label className="block text-[11px] font-semibold text-kb-text-primary uppercase tracking-wider">
        {label}
      </label>
      {children}
      {helper && <p className="text-[11px] text-kb-text-tertiary leading-relaxed">{helper}</p>}
    </div>
  )
}

function SecretInput({
  label,
  value,
  onChange,
  reveal,
  onToggleReveal,
  placeholder,
  autoFocus,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  reveal: boolean
  onToggleReveal: () => void
  placeholder?: string
  autoFocus?: boolean
}) {
  return (
    <Field label={label}>
      <SecretInputInline
        value={value}
        onChange={onChange}
        reveal={reveal}
        onToggleReveal={onToggleReveal}
        placeholder={placeholder}
        autoFocus={autoFocus}
      />
    </Field>
  )
}

function SecretInputInline({
  value,
  onChange,
  reveal,
  onToggleReveal,
  placeholder,
  autoFocus,
}: {
  value: string
  onChange: (v: string) => void
  reveal: boolean
  onToggleReveal: () => void
  placeholder?: string
  autoFocus?: boolean
}) {
  return (
    <div className="relative">
      <input
        type={reveal ? 'text' : 'password'}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoComplete="off"
        autoFocus={autoFocus}
        className="w-full pl-2 pr-9 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
      />
      <button
        type="button"
        onClick={onToggleReveal}
        className="absolute right-1.5 top-1/2 -translate-y-1/2 p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary"
        aria-label={reveal ? 'Hide secret' : 'Show secret'}
      >
        {reveal ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
      </button>
    </div>
  )
}
