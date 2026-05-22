import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  CheckCircle2,
  Loader2,
  Moon,
  RotateCcw,
  Save,
  Settings,
  Sparkles,
  Sun,
  X,
} from 'lucide-react'
import { api } from '@/services/api'
import { useTheme } from '@/contexts/ThemeContext'
import type {
  GeneralSettingsPutRequest,
  GeneralSettingsResponse,
} from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ConfirmDialog } from './ConfirmDialog'

// GeneralSettingsTab — display name + team-default refresh interval.
// Smallest of the Settings domains in terms of edit surface; meant to
// land first as a low-risk validation of the runtime pattern for non-
// secret, hot-reloadable knobs.
//
// "Display name" surfaces in the sidebar logo area (and browser title
// later) so operators running multiple KubeBolt installs can tell them
// apart at a glance.
//
// "Default refresh interval" seeds new users' RefreshContext. Per-user
// choices via the DataFreshnessIndicator dropdown still win — this is
// a team default, not a forced setting.

const REFRESH_OPTIONS: { value: number; label: string }[] = [
  { value: 5, label: '5 seconds' },
  { value: 10, label: '10 seconds' },
  { value: 15, label: '15 seconds' },
  { value: 30, label: '30 seconds (default)' },
  { value: 60, label: '1 minute' },
  { value: 120, label: '2 minutes' },
]

interface FormState {
  displayName: string
  defaultRefreshIntervalSeconds: string
}

function stateFromResponse(data: GeneralSettingsResponse): FormState {
  return {
    displayName: data.effective.displayName,
    defaultRefreshIntervalSeconds: String(data.effective.defaultRefreshIntervalSeconds),
  }
}

function buildPatch(initial: FormState, current: FormState): GeneralSettingsPutRequest {
  const patch: GeneralSettingsPutRequest['patch'] = {}
  if (current.displayName !== initial.displayName) {
    patch.displayName = current.displayName
  }
  const sec = parseInt(current.defaultRefreshIntervalSeconds, 10)
  if (!isNaN(sec) && sec !== parseInt(initial.defaultRefreshIntervalSeconds, 10)) {
    patch.defaultRefreshIntervalSeconds = sec
  }
  return Object.keys(patch).length > 0 ? { patch } : {}
}

export function GeneralSettingsTab() {
  const queryClient = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'settings', 'general'],
    queryFn: api.getSettingsGeneral,
  })

  if (isLoading) return <LoadingSpinner />
  if (error || !data) {
    return (
      <div className="rounded-lg border border-status-error-dim bg-status-error-dim/30 p-4 text-xs text-status-error">
        Failed to load General settings. Refresh the page or check that the backend has BoltDB persistence enabled.
      </div>
    )
  }

  return (
    <GeneralSettingsForm
      data={data}
      onSaved={() => {
        queryClient.invalidateQueries({ queryKey: ['admin', 'settings', 'general'] })
        // Also bust the public UI config cache so the sidebar logo
        // label re-renders without a page reload.
        queryClient.invalidateQueries({ queryKey: ['ui-config'] })
      }}
    />
  )
}

function GeneralSettingsForm({
  data,
  onSaved,
}: {
  data: GeneralSettingsResponse
  onSaved: () => void
}) {
  const [initial, setInitial] = useState<FormState>(() => stateFromResponse(data))
  const [form, setForm] = useState<FormState>(() => stateFromResponse(data))
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false)
  const [rerunWizardConfirmOpen, setRerunWizardConfirmOpen] = useState(false)
  const [rerunWizardBusy, setRerunWizardBusy] = useState(false)
  const [rerunWizardError, setRerunWizardError] = useState<string | null>(null)
  const { theme, toggleTheme } = useTheme()

  const dirtyMap = {
    displayName: form.displayName !== initial.displayName,
    defaultRefreshIntervalSeconds:
      form.defaultRefreshIntervalSeconds !== initial.defaultRefreshIntervalSeconds,
  }
  const isDirty = Object.values(dirtyMap).some(Boolean)

  const saveMutation = useMutation({
    mutationFn: () => api.putSettingsGeneral(buildPatch(initial, form)),
    onSuccess: (newData) => {
      const next = stateFromResponse(newData)
      setInitial(next)
      setForm(next)
      setSavedAt(Date.now())
      onSaved()
    },
  })

  const resetMutation = useMutation({
    mutationFn: () => api.resetSettingsGeneral(),
    onSuccess: () => onSaved(),
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    saveMutation.mutate()
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      {/* Appearance is a per-USER preference (stored in localStorage),
          distinct from the cluster-wide settings below. Surfaced here
          for discoverability — the Topbar's sun/moon icon does the
          same thing in one click. */}
      <SectionCard
        icon={theme === 'dark' ? <Moon className="w-4 h-4 text-kb-text-secondary" /> : <Sun className="w-4 h-4 text-status-info" />}
        title="Appearance"
        subtitle="Theme preference for your browser. Each user picks their own; not shared across the team."
      >
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="text-xs text-kb-text-primary">
              {theme === 'dark' ? 'Dark mode' : 'Light mode'}
            </div>
            <p className="text-[11px] text-kb-text-tertiary mt-0.5 leading-relaxed">
              Saved to <code className="font-mono text-kb-accent">localStorage</code> in this browser. Clearing site data resets it.
            </p>
          </div>
          <button
            type="button"
            onClick={toggleTheme}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border shrink-0"
          >
            {theme === 'dark' ? (
              <>
                <Sun className="w-3.5 h-3.5" />
                Switch to light
              </>
            ) : (
              <>
                <Moon className="w-3.5 h-3.5" />
                Switch to dark
              </>
            )}
          </button>
        </div>
      </SectionCard>

      <SectionCard
        icon={<Settings className="w-4 h-4 text-kb-accent" />}
        title="Branding"
        subtitle="What this KubeBolt install calls itself."
      >
        <Field
          label="Display name"
          dirty={dirtyMap.displayName}
          helper="Shown in the sidebar logo area and (later) the browser tab title. Leave blank to fall back to 'KubeBolt'. Max 64 characters."
        >
          <input
            type="text"
            placeholder="KubeBolt"
            maxLength={64}
            className="w-full max-w-md px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.displayName}
            onChange={(e) => setForm({ ...form, displayName: e.target.value })}
          />
        </Field>
      </SectionCard>

      <SectionCard
        icon={<Sparkles className="w-4 h-4 text-kb-text-tertiary" />}
        title="Setup wizard"
        subtitle="Re-run the welcome flow — useful for onboarding videos and per-cluster demos."
      >
        <button
          type="button"
          onClick={() => {
            setRerunWizardError(null)
            setRerunWizardConfirmOpen(true)
          }}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border"
        >
          <Sparkles className="w-3.5 h-3.5" />
          Re-run setup wizard
        </button>
        {rerunWizardError && (
          <div className="mt-2 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <div>{rerunWizardError}</div>
          </div>
        )}
      </SectionCard>

      <SectionCard
        title="Defaults"
        subtitle="Baseline UX values applied when a user has no personal preference yet."
      >
        <Field
          label="Default refresh interval"
          dirty={dirtyMap.defaultRefreshIntervalSeconds}
          helper="Seeds the polling cadence for new users. Per-user choices via the freshness dropdown still win over this default."
        >
          <select
            className="w-full max-w-xs px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.defaultRefreshIntervalSeconds}
            onChange={(e) =>
              setForm({ ...form, defaultRefreshIntervalSeconds: e.target.value })
            }
          >
            {REFRESH_OPTIONS.map((opt) => (
              <option key={opt.value} value={String(opt.value)}>
                {opt.label}
              </option>
            ))}
          </select>
        </Field>
      </SectionCard>

      <div className="bg-kb-card border border-kb-border rounded-xl">
        <div className="p-3 flex items-center justify-between gap-3">
          <button
            type="button"
            onClick={() => setResetConfirmOpen(true)}
            disabled={resetMutation.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated disabled:opacity-50"
          >
            <RotateCcw className="w-3.5 h-3.5" />
            {resetMutation.isPending ? 'Resetting…' : 'Reset to env defaults'}
          </button>
          <div className="flex items-center gap-2">
            {isDirty && !saveMutation.isPending && (
              <button
                type="button"
                onClick={() => setForm(initial)}
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
            <div>General settings saved. The sidebar and refresh default pick up the new values immediately.</div>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={resetConfirmOpen}
        badge="Reset"
        variant="danger"
        title="Reset General settings to env defaults?"
        description={
          <>Clears the UI-set <strong className="text-kb-text-primary">Display name</strong> and <strong className="text-kb-text-primary">Default refresh interval</strong> overrides. The next read falls back to the values from <code className="font-mono text-kb-accent">KUBEBOLT_*</code> env vars. Theme is per-browser and not affected.</>
        }
        confirmLabel="Reset"
        onConfirm={() => {
          setResetConfirmOpen(false)
          resetMutation.mutate()
        }}
        onCancel={() => setResetConfirmOpen(false)}
        busy={resetMutation.isPending}
      />

      <ConfirmDialog
        open={rerunWizardConfirmOpen}
        badge="Wizard"
        title="Re-run setup wizard on next page load?"
        description="The welcome overlay will appear again. You can dismiss it from any step — nothing is forced."
        confirmLabel="Re-run wizard"
        busy={rerunWizardBusy}
        onConfirm={async () => {
          setRerunWizardBusy(true)
          setRerunWizardError(null)
          try {
            await api.resetSetup()
            window.location.reload()
          } catch (e) {
            setRerunWizardError((e as Error).message || 'Failed to reset wizard state')
            setRerunWizardBusy(false)
            setRerunWizardConfirmOpen(false)
          }
        }}
        onCancel={() => setRerunWizardConfirmOpen(false)}
      />
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

function UnsavedChip() {
  return (
    <span className="text-[10px] font-mono font-medium uppercase tracking-wider text-status-warn">
      Unsaved
    </span>
  )
}

function SectionCard({
  icon,
  title,
  subtitle,
  children,
}: {
  icon?: React.ReactNode
  title: string
  subtitle?: string
  children: React.ReactNode
}) {
  return (
    <section className="bg-kb-card border border-kb-border rounded-xl">
      <header className="flex items-start gap-2 px-5 py-4 border-b border-kb-border">
        {icon && <div className="mt-0.5 shrink-0">{icon}</div>}
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-kb-text-primary">{title}</h2>
          {subtitle && (
            <p className="text-[11px] text-kb-text-tertiary mt-0.5 leading-snug">{subtitle}</p>
          )}
        </div>
      </header>
      <div className="px-5 py-4 space-y-4">{children}</div>
    </section>
  )
}
