import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Bot, Eye, EyeOff, AlertTriangle, CheckCircle2, RotateCcw, Save, Loader2, X } from 'lucide-react'
import { api } from '@/services/api'
import type { CopilotSettingsPutRequest, CopilotSettingsResponse } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import {
  CUSTOM_MODEL_VALUE,
  findModelOption,
  groupModels,
  MODELS_BY_PROVIDER,
  type ProviderID,
} from './modelCatalog'

// CopilotSettingsTab is the V1 admin form for Copilot config. Reads the
// masked settings view via GET /admin/settings/copilot, lets the operator
// edit each field, and PUTs a partial patch on save. Secrets are
// handled via a "reveal-and-replace" pattern — the current API key is
// shown masked (`sk-ant-***xyz`), and clicking the eye toggles the
// input into write-mode where typing replaces it. Clearing the input
// without typing means "no change".
//
// Reset wipes the BoltDB override entirely → next read falls back to
// env. Useful when the operator wants to test what happens with their
// chart values removed without touching the cluster.

type ProviderName = ProviderID

interface FormState {
  provider: ProviderName
  model: string
  apiKey: string // empty = unchanged
  baseURL: string
  hasFallback: boolean
  fallbackProvider: ProviderName
  fallbackModel: string
  fallbackApiKey: string
  fallbackBaseURL: string
  showToolCalls: boolean
  autoCompact: boolean
  maxTokens: string // string so the input is editable; coerced to number on save
  // Auto-compact tunables. Strings while editing for the same reason as
  // maxTokens — coerced on save. Empty means "fall back to env / default".
  sessionBudgetTokens: string
  autoCompactThreshold: string
  compactModel: string
  compactPreserveTurns: string
}

function stateFromResponse(data: CopilotSettingsResponse): FormState {
  const eff = data.effective
  return {
    provider: (eff.provider as ProviderName) || 'anthropic',
    model: eff.model || '',
    apiKey: '', // never prefill — reveal-and-replace pattern
    baseURL: eff.baseURL || '',
    hasFallback: eff.hasFallback,
    fallbackProvider: (eff.fallbackProvider as ProviderName) || 'anthropic',
    fallbackModel: eff.fallbackModel || '',
    fallbackApiKey: '',
    fallbackBaseURL: eff.fallbackBaseURL || '',
    showToolCalls: eff.showToolCalls,
    autoCompact: eff.autoCompact,
    maxTokens: String(eff.maxTokens || 4096),
    sessionBudgetTokens: eff.sessionBudgetTokens != null ? String(eff.sessionBudgetTokens) : '',
    autoCompactThreshold: eff.autoCompactThreshold != null ? String(eff.autoCompactThreshold) : '',
    compactModel: eff.compactModel || '',
    compactPreserveTurns: eff.compactPreserveTurns != null ? String(eff.compactPreserveTurns) : '',
  }
}

function buildPatch(initial: FormState, current: FormState): CopilotSettingsPutRequest {
  const patch: CopilotSettingsPutRequest['patch'] = {}
  const primaryPatch: NonNullable<CopilotSettingsPutRequest['patch']>['primary'] = {}
  if (current.provider !== initial.provider) primaryPatch.provider = current.provider
  if (current.model !== initial.model) primaryPatch.model = current.model
  if (current.baseURL !== initial.baseURL) primaryPatch.baseURL = current.baseURL
  if (Object.keys(primaryPatch).length > 0) patch.primary = primaryPatch

  if (current.hasFallback) {
    const fallbackPatch: NonNullable<CopilotSettingsPutRequest['patch']>['fallback'] = {}
    if (current.fallbackProvider !== initial.fallbackProvider) fallbackPatch.provider = current.fallbackProvider
    if (current.fallbackModel !== initial.fallbackModel) fallbackPatch.model = current.fallbackModel
    if (current.fallbackBaseURL !== initial.fallbackBaseURL) fallbackPatch.baseURL = current.fallbackBaseURL
    if (Object.keys(fallbackPatch).length > 0) patch.fallback = fallbackPatch
  }

  if (current.showToolCalls !== initial.showToolCalls) patch.showToolCalls = current.showToolCalls
  if (current.autoCompact !== initial.autoCompact) patch.autoCompact = current.autoCompact
  const mt = parseInt(current.maxTokens, 10)
  if (!isNaN(mt) && mt > 0 && mt !== parseInt(initial.maxTokens, 10)) patch.maxTokens = mt

  // Auto-compact tunables. Empty input is intentional "leave at env" — we
  // only send a value when the operator typed something meaningful AND it
  // differs from initial. Validation lives server-side; the form rejects
  // obviously bad inputs but the backend is authoritative.
  if (current.sessionBudgetTokens !== initial.sessionBudgetTokens) {
    const n = parseInt(current.sessionBudgetTokens, 10)
    if (current.sessionBudgetTokens === '') {
      // Explicit clear is V2 — for now omit when empty; the value
      // already in storage stays as-is.
    } else if (!isNaN(n) && n >= 0) {
      patch.sessionBudgetTokens = n
    }
  }
  if (current.autoCompactThreshold !== initial.autoCompactThreshold) {
    const f = parseFloat(current.autoCompactThreshold)
    if (current.autoCompactThreshold !== '' && !isNaN(f) && f > 0 && f < 1) {
      patch.autoCompactThreshold = f
    }
  }
  if (current.compactModel !== initial.compactModel) {
    patch.compactModel = current.compactModel
  }
  if (current.compactPreserveTurns !== initial.compactPreserveTurns) {
    const n = parseInt(current.compactPreserveTurns, 10)
    if (current.compactPreserveTurns !== '' && !isNaN(n) && n >= 0) {
      patch.compactPreserveTurns = n
    }
  }

  const req: CopilotSettingsPutRequest = {}
  if (Object.keys(patch).length > 0) req.patch = patch
  if (current.apiKey.trim() !== '') req.plaintextAPIKey = current.apiKey
  if (current.hasFallback && current.fallbackApiKey.trim() !== '') {
    req.plaintextFallbackAPIKey = current.fallbackApiKey
  }
  return req
}

export function CopilotSettingsTab() {
  const queryClient = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'settings', 'copilot'],
    queryFn: api.getSettingsCopilot,
  })

  if (isLoading) return <LoadingSpinner />
  if (error || !data) {
    return (
      <div className="rounded-lg border border-status-error-dim bg-status-error-dim/30 p-4 text-xs text-status-error">
        Failed to load Copilot settings. Refresh the page or check that the backend has BoltDB persistence enabled.
      </div>
    )
  }

  return <CopilotSettingsForm data={data} onSaved={() => queryClient.invalidateQueries({ queryKey: ['admin', 'settings', 'copilot'] })} />
}

function CopilotSettingsForm({
  data,
  onSaved,
}: {
  data: CopilotSettingsResponse
  onSaved: () => void
}) {
  // Both `initial` and `form` are state so onSuccess can re-anchor them
  // to whatever the server returns post-save. Without this, the saved
  // value stays "dirty" forever (form != initial), the Save button
  // never disables, and the "Saved" indicator never appears.
  const [initial, setInitial] = useState<FormState>(() => stateFromResponse(data))
  const [form, setForm] = useState<FormState>(() => stateFromResponse(data))
  const [revealAPIKey, setRevealAPIKey] = useState(false)
  const [revealFallbackKey, setRevealFallbackKey] = useState(false)
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const queryClient = useQueryClient()

  // Per-field dirty map. Drives the inline "UNSAVED" chips next to each
  // label so operators can see exactly which controls they touched —
  // pattern adopted from PerTenantLimitsSection.
  // apiKey / fallbackApiKey are "dirty" when non-empty because the
  // reveal-and-replace flow treats empty as "no change".
  const dirtyMap = {
    provider: form.provider !== initial.provider,
    model: form.model !== initial.model,
    apiKey: form.apiKey.trim() !== '',
    baseURL: form.baseURL !== initial.baseURL,
    hasFallback: form.hasFallback !== initial.hasFallback,
    fallbackProvider: form.fallbackProvider !== initial.fallbackProvider,
    fallbackModel: form.fallbackModel !== initial.fallbackModel,
    fallbackApiKey: form.fallbackApiKey.trim() !== '',
    fallbackBaseURL: form.fallbackBaseURL !== initial.fallbackBaseURL,
    showToolCalls: form.showToolCalls !== initial.showToolCalls,
    autoCompact: form.autoCompact !== initial.autoCompact,
    maxTokens: form.maxTokens !== initial.maxTokens,
    sessionBudgetTokens: form.sessionBudgetTokens !== initial.sessionBudgetTokens,
    autoCompactThreshold: form.autoCompactThreshold !== initial.autoCompactThreshold,
    compactModel: form.compactModel !== initial.compactModel,
    compactPreserveTurns: form.compactPreserveTurns !== initial.compactPreserveTurns,
  }
  const isDirty = Object.values(dirtyMap).some(Boolean)

  const saveMutation = useMutation({
    mutationFn: () => api.putSettingsCopilot(buildPatch(initial, form)),
    onSuccess: (newData) => {
      // Re-anchor both initial and form to the server's authoritative
      // post-write view. This clears the dirty state, shows the new
      // masked API key in the helper text, and refreshes any fields
      // the server normalised (e.g. a defaulted provider).
      const next = stateFromResponse(newData)
      setInitial(next)
      setForm(next)
      setSavedAt(Date.now())
      setRevealAPIKey(false)
      setRevealFallbackKey(false)
      onSaved()
      // Also invalidate the public /copilot/config used by the chat panel
      // so the "Configured" pill flips without a page reload.
      // queryKey must match the one in CopilotContext.tsx (kebab string,
      // not a two-element array — that mistake silently no-ops the
      // invalidation and the chat panel's "PROVIDER · MODEL" pill
      // stays on the previous model until full page reload).
      queryClient.invalidateQueries({ queryKey: ['copilot-config'] })
    },
  })

  const resetMutation = useMutation({
    mutationFn: () => api.resetSettingsCopilot(),
    onSuccess: () => {
      onSaved()
      // queryKey must match the one in CopilotContext.tsx (kebab string,
      // not a two-element array — that mistake silently no-ops the
      // invalidation and the chat panel's "PROVIDER · MODEL" pill
      // stays on the previous model until full page reload).
      queryClient.invalidateQueries({ queryKey: ['copilot-config'] })
    },
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    saveMutation.mutate()
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      {!data.secretsReadable && (
        <div className="flex items-start gap-2 rounded-xl border border-status-warn-dim bg-status-warn-dim/30 p-4 text-xs text-status-warn">
          <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
          <div>
            <div className="font-semibold mb-0.5">Stored API key is unreadable</div>
            <div>
              The JWT secret was likely rotated since this key was saved. Re-enter the key below to restore Kobi.
            </div>
          </div>
        </div>
      )}

      {/* Primary provider — white card with all primary controls */}
      <SectionCard
        icon={<Bot className="w-4 h-4 text-kb-accent" />}
        title="Primary provider"
        subtitle="The default model Kobi uses for every chat."
      >
        <Field
          label="Provider"
          dirty={dirtyMap.provider}
          helper="Anthropic uses the native Claude API. OpenAI is the umbrella for OpenAI's own models plus any OpenAI-compatible provider (xAI Grok, Alibaba Qwen, DeepSeek, Meta Llama via Groq, Mistral)."
        >
          <select
            className="w-full max-w-md px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.provider}
            onChange={(e) => {
              const nextProvider = e.target.value as ProviderName
              // Switching provider invalidates the current model — keep it
              // only when the chosen ID exists in the new provider's
              // catalog. Without this, an Anthropic model like
              // claude-sonnet-4-6 would carry into an OpenAI provider and
              // fail at chat time with a 404 from the wrong endpoint.
              const modelCarryOver = findModelOption(nextProvider, form.model) ? form.model : ''
              setForm({ ...form, provider: nextProvider, model: modelCarryOver })
            }}
          >
            <option value="anthropic">Anthropic (Claude)</option>
            <option value="openai">OpenAI + compatible (GPT, Grok, Qwen, DeepSeek, Llama, Mistral)</option>
          </select>
        </Field>

        <Field label="Model" dirty={dirtyMap.model}>
          <ModelPicker
            provider={form.provider}
            value={form.model}
            onChange={(model) => setForm({ ...form, model })}
          />
        </Field>

        <Field
          label="API key"
          dirty={dirtyMap.apiKey}
          helper={
            data.effective.apiKeyMasked
              ? `Currently set: ${data.effective.apiKeyMasked}. Leave blank to keep, or type to replace.`
              : 'Required to enable Kobi.'
          }
        >
          <div className="relative">
            <input
              type={revealAPIKey ? 'text' : 'password'}
              placeholder={data.effective.apiKeyMasked ? '••••••••' : 'sk-ant-...'}
              autoComplete="off"
              className="w-full pl-2 pr-9 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
              value={form.apiKey}
              onChange={(e) => setForm({ ...form, apiKey: e.target.value })}
            />
            <button
              type="button"
              onClick={() => setRevealAPIKey((v) => !v)}
              className="absolute right-1.5 top-1/2 -translate-y-1/2 p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary"
              aria-label={revealAPIKey ? 'Hide API key' : 'Show API key'}
            >
              {revealAPIKey ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
            </button>
          </div>
        </Field>

        <Field
          label="Base URL"
          dirty={dirtyMap.baseURL}
          helper="Optional. Override the provider's endpoint for self-hosted gateways or proxies."
        >
          <input
            type="text"
            placeholder="https://api.anthropic.com (default)"
            className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
            value={form.baseURL}
            onChange={(e) => setForm({ ...form, baseURL: e.target.value })}
          />
        </Field>
      </SectionCard>

      {/* Fallback provider — its own card. Toggle in the header reveals
          the body. Saves vertical space when fallback is off (typical
          case for fresh installs). */}
      <SectionCard
        title="Fallback provider"
        subtitle="Optional secondary model Kobi tries when the primary returns a recoverable error (rate limit, 5xx)."
        headerRight={
          <label className="flex items-center gap-2 text-xs text-kb-text-secondary cursor-pointer">
            <input
              type="checkbox"
              checked={form.hasFallback}
              onChange={(e) => setForm({ ...form, hasFallback: e.target.checked })}
              className="accent-kb-accent"
            />
            Enable
            {dirtyMap.hasFallback && <UnsavedChip />}
          </label>
        }
      >
        {form.hasFallback ? (
          <div className="space-y-3">
            <Field label="Provider" dirty={dirtyMap.fallbackProvider}>
              <select
                className="w-full max-w-md px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
                value={form.fallbackProvider}
                onChange={(e) => {
                  const nextProvider = e.target.value as ProviderName
                  const modelCarryOver = findModelOption(nextProvider, form.fallbackModel) ? form.fallbackModel : ''
                  setForm({ ...form, fallbackProvider: nextProvider, fallbackModel: modelCarryOver })
                }}
              >
                <option value="anthropic">Anthropic (Claude)</option>
                <option value="openai">OpenAI + compatible</option>
              </select>
            </Field>
            <Field label="Model" dirty={dirtyMap.fallbackModel}>
              <ModelPicker
                provider={form.fallbackProvider}
                value={form.fallbackModel}
                onChange={(model) => setForm({ ...form, fallbackModel: model })}
              />
            </Field>
            <Field
              label="Fallback API key"
              dirty={dirtyMap.fallbackApiKey}
              helper={
                data.effective.fallbackApiKeyMasked
                  ? `Currently set: ${data.effective.fallbackApiKeyMasked}. Leave blank to keep, or type to replace.`
                  : 'Required to enable the fallback path.'
              }
            >
              <div className="relative">
                <input
                  type={revealFallbackKey ? 'text' : 'password'}
                  placeholder={data.effective.fallbackApiKeyMasked ? '••••••••' : 'sk-...'}
                  autoComplete="off"
                  className="w-full pl-2 pr-9 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
                  value={form.fallbackApiKey}
                  onChange={(e) => setForm({ ...form, fallbackApiKey: e.target.value })}
                />
                <button
                  type="button"
                  onClick={() => setRevealFallbackKey((v) => !v)}
                  className="absolute right-1.5 top-1/2 -translate-y-1/2 p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary"
                  aria-label={revealFallbackKey ? 'Hide fallback API key' : 'Show fallback API key'}
                >
                  {revealFallbackKey ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
                </button>
              </div>
            </Field>
            <Field
              label="Fallback base URL"
              dirty={dirtyMap.fallbackBaseURL}
              helper="Optional. Override the fallback provider's endpoint — needed for OpenAI-compatible gateways like xAI Grok (api.x.ai/v1), DeepSeek, Groq, Together, etc."
            >
              <input
                type="text"
                placeholder="https://api.openai.com/v1 (default)"
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
                value={form.fallbackBaseURL}
                onChange={(e) => setForm({ ...form, fallbackBaseURL: e.target.value })}
              />
            </Field>
          </div>
        ) : (
          <p className="text-[11px] text-kb-text-tertiary italic">
            Fallback disabled. Enable to auto-retry on rate limits or upstream errors.
          </p>
        )}
      </SectionCard>

      {/* Behavior — knobs that don't fit under primary/fallback */}
      <SectionCard
        title="Behavior"
        subtitle="Output, session, and auto-compaction defaults."
      >
        <Field label="Max tokens" dirty={dirtyMap.maxTokens} helper="Cap on output tokens per provider call. Bump this if Kobi truncates long answers.">
          <input
            type="number"
            min={1}
            className="w-32 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.maxTokens}
            onChange={(e) => setForm({ ...form, maxTokens: e.target.value })}
          />
        </Field>

        <label className="flex items-start gap-2 text-xs text-kb-text-secondary cursor-pointer">
          <input
            type="checkbox"
            checked={form.showToolCalls}
            onChange={(e) => setForm({ ...form, showToolCalls: e.target.checked })}
            className="accent-kb-accent mt-0.5"
          />
          <div>
            <div className="flex items-center gap-2">
              <div className="text-kb-text-primary">Show tool call cards in chat</div>
              {dirtyMap.showToolCalls && <UnsavedChip />}
            </div>
            <div className="text-kb-text-tertiary">
              Persistent expandable cards for each tool Kobi invokes. Off: only the final assistant text remains visible.
            </div>
          </div>
        </label>

        <label className="flex items-start gap-2 text-xs text-kb-text-secondary cursor-pointer">
          <input
            type="checkbox"
            checked={form.autoCompact}
            onChange={(e) => setForm({ ...form, autoCompact: e.target.checked })}
            className="accent-kb-accent mt-0.5"
          />
          <div>
            <div className="flex items-center gap-2">
              <div className="text-kb-text-primary">Auto-compact long sessions</div>
              {dirtyMap.autoCompact && <UnsavedChip />}
            </div>
            <div className="text-kb-text-tertiary">
              Summarize older turns automatically when the session approaches the model's context window. Off: long sessions hit the budget and fail.
            </div>
          </div>
        </label>

        {form.autoCompact && (
          // Tunables only matter when auto-compact is on. Group them
          // visually under the checkbox so the dependency reads top-to-
          // bottom. The fields stay nil-tolerant — empty input means
          // "fall back to env / model default", not "set to zero".
          <div className="grid grid-cols-2 gap-4 pt-1 pl-6 border-l-2 border-kb-border ml-2">
            <Field
              label="Session budget"
              dirty={dirtyMap.sessionBudgetTokens}
              helper="Tokens of context Kobi keeps in flight before compaction kicks in. Blank = auto from the model's context window."
            >
              <input
                type="number"
                min={0}
                placeholder="auto"
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
                value={form.sessionBudgetTokens}
                onChange={(e) => setForm({ ...form, sessionBudgetTokens: e.target.value })}
              />
            </Field>
            <Field
              label="Compact threshold"
              dirty={dirtyMap.autoCompactThreshold}
              helper="Fraction of budget that triggers compaction (0–1). 0.80 = compact when 80% full."
            >
              <input
                type="number"
                min={0.05}
                max={0.99}
                step={0.05}
                placeholder="0.80"
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
                value={form.autoCompactThreshold}
                onChange={(e) => setForm({ ...form, autoCompactThreshold: e.target.value })}
              />
            </Field>
            <Field
              label="Compact model"
              dirty={dirtyMap.compactModel}
              helper="Cheaper model used to run the compaction itself. Blank = auto-pick a cheap model for the primary provider (e.g. claude-haiku-4-5)."
            >
              <input
                type="text"
                placeholder="claude-haiku-4-5"
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
                value={form.compactModel}
                onChange={(e) => setForm({ ...form, compactModel: e.target.value })}
              />
            </Field>
            <Field
              label="Preserve turns"
              dirty={dirtyMap.compactPreserveTurns}
              helper="How many recent turns to keep INTACT after compaction (older ones get summarized)."
            >
              <input
                type="number"
                min={0}
                placeholder="3"
                className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
                value={form.compactPreserveTurns}
                onChange={(e) => setForm({ ...form, compactPreserveTurns: e.target.value })}
              />
            </Field>
          </div>
        )}
      </SectionCard>

      {/* Action card hosts the action bar PLUS any post-action banners
          (success / error). Keeping the banner inside the card makes
          it read as feedback for the action just performed — same
          convention as PerTenantLimitsSection. */}
      <div className="bg-kb-card border border-kb-border rounded-xl">
        <div className="p-3 flex items-center justify-between gap-3">
          <button
            type="button"
            onClick={() => {
              if (confirm('Reset Copilot settings to environment defaults? This clears all UI-configured values.')) {
                resetMutation.mutate()
              }
            }}
            disabled={resetMutation.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated disabled:opacity-50"
          >
            <RotateCcw className="w-3.5 h-3.5" />
            {resetMutation.isPending ? 'Resetting…' : 'Reset to env defaults'}
          </button>
          <div className="flex items-center gap-2">
            {/* Cancel is only rendered while there's something to cancel.
                Discards in-progress edits and clears the reveal-key
                toggles so the form returns to the last-saved state.
                Distinct from "Reset to env defaults" which wipes the
                persisted overrides themselves — Cancel only undoes the
                current editing session. */}
            {isDirty && !saveMutation.isPending && (
              <button
                type="button"
                onClick={() => {
                  setForm(initial)
                  setRevealAPIKey(false)
                  setRevealFallbackKey(false)
                }}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border"
              >
                <X className="w-3.5 h-3.5" />
                Cancel
              </button>
            )}
            <button
              type="submit"
              disabled={!isDirty || saveMutation.isPending}
              className="flex items-center gap-1.5 px-4 py-1.5 rounded-md text-xs font-medium bg-kb-accent text-kb-bg disabled:opacity-50 disabled:cursor-not-allowed"
            >
              {saveMutation.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Save className="w-3.5 h-3.5" />}
              {saveMutation.isPending ? 'Saving…' : 'Save changes'}
            </button>
          </div>
        </div>

        {saveMutation.isError && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <div>{(saveMutation.error as Error)?.message || 'Failed to save.'}</div>
          </div>
        )}

        {savedAt && !isDirty && !saveMutation.isPending && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">
            <CheckCircle2 className="w-4 h-4 mt-0.5 shrink-0" />
            <div>Copilot settings saved. The chat panel and next message pick up the new values immediately.</div>
          </div>
        )}
      </div>
    </form>
  )
}

function Field({
  label,
  helper,
  dirty,
  children,
}: {
  label: string
  helper?: string
  dirty?: boolean
  children: React.ReactNode
}) {
  // Label hierarchy: distinct from the control beneath it via (a) a
  // tangible vertical gap so the eye reads "title → input", and (b)
  // a primary text color + slightly larger size so the label out-weights
  // the helper line. The uppercase + tracking convention stays — it's
  // the design system's marker for field captions, used consistently
  // across admin pages.
  //
  // `dirty` renders an inline UNSAVED chip next to the label — same
  // convention as PerTenantLimitsSection so operators can see which
  // individual controls they touched without scanning the whole form.
  return (
    <div className="space-y-1.5">
      <div className="flex items-center gap-2">
        <label className="block text-[11px] font-semibold text-kb-text-primary uppercase tracking-wider">
          {label}
        </label>
        {dirty && <UnsavedChip />}
      </div>
      {children}
      {helper && <p className="text-[11px] text-kb-text-tertiary leading-relaxed">{helper}</p>}
    </div>
  )
}

// UnsavedChip is the per-field "you have a pending edit" marker. Shared
// between Field-wrapped inputs and inline labels (checkboxes, fallback
// enable toggle) so every Settings page renders the same chip in the
// same place.
function UnsavedChip() {
  return (
    <span className="text-[10px] font-mono font-medium uppercase tracking-wider text-status-warn">
      Unsaved
    </span>
  )
}

// SectionCard is the page's primary content container — white-bg card
// with a header row (icon + title + subtitle + optional right slot)
// and a body separated by a hairline. Matches the convention used in
// NotificationsPage / TenantLimitsPage so the Settings page reads as
// part of the same admin surface and not a separate visual idiom.
function SectionCard({
  icon,
  title,
  subtitle,
  headerRight,
  children,
}: {
  icon?: React.ReactNode
  title: string
  subtitle?: string
  headerRight?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="bg-kb-card border border-kb-border rounded-xl">
      <header className="flex items-start justify-between gap-3 px-5 py-4 border-b border-kb-border">
        <div className="flex items-start gap-2 min-w-0">
          {icon && <div className="mt-0.5 shrink-0">{icon}</div>}
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-kb-text-primary">{title}</h2>
            {subtitle && (
              <p className="text-[11px] text-kb-text-tertiary mt-0.5 leading-snug">{subtitle}</p>
            )}
          </div>
        </div>
        {headerRight && <div className="shrink-0">{headerRight}</div>}
      </header>
      <div className="px-5 py-4 space-y-4">{children}</div>
    </section>
  )
}

// ModelPicker renders the curated catalog as a grouped select, with a
// "Custom..." option that reveals a text input for non-cataloged IDs.
// Below the select, a small descriptor box shows the trade-off of
// whichever model is currently chosen — operators get the context that
// raw model IDs don't carry.
//
// Externally-controlled value: the parent owns `value` (the model ID
// that goes to the API). The picker derives "is this Custom?" from
// whether the value matches any catalog entry — keeping a separate
// "isCustom" flag in state would drift on the first refetch.
function ModelPicker({
  provider,
  value,
  onChange,
}: {
  provider: ProviderName
  value: string
  onChange: (model: string) => void
}) {
  const groups = groupModels(MODELS_BY_PROVIDER[provider] ?? [])
  const matched = findModelOption(provider, value)
  // If the value is non-empty AND isn't in the catalog, treat it as
  // a custom (user-typed) entry. Empty value falls through to "pick one".
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
            // Switching INTO custom from a catalog selection: clear the
            // field so the operator types fresh. Switching back to
            // Custom while already custom keeps whatever they had.
            if (!isCustom) onChange('')
            return
          }
          onChange(next)
        }}
      >
        <option value="" disabled>
          Select a model…
        </option>
        {groups.map((g) => (
          <optgroup key={g.group} label={g.group}>
            {g.items.map((opt) => (
              <option key={opt.id} value={opt.id}>
                {opt.label}
              </option>
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

      {matched && (
        // Borderless descriptor: distinct from an input visually. Mono
        // chip for the model ID, plain text below for the trade-off.
        // Sits flush with the field's left edge so the eye reads it as
        // a continuation of the select, not a separate input.
        <div className="px-0.5 pt-1">
          <code className="text-[10px] font-mono text-kb-text-tertiary">{matched.id}</code>
          <p className="text-[11px] text-kb-text-secondary leading-snug mt-1">
            {matched.description}
          </p>
        </div>
      )}
      {isCustom && (
        <p className="text-[10px] text-kb-text-tertiary px-0.5 pt-1">
          Custom model ID. Make sure your provider (or compatible gateway) recognises this name and that the base URL points to the right endpoint.
        </p>
      )}
    </div>
  )
}
