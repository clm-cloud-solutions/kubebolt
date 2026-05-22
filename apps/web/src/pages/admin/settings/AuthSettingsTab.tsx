import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  CheckCircle2,
  Info,
  KeyRound,
  Loader2,
  Power,
  RefreshCw,
  RotateCcw,
  Save,
  X,
} from 'lucide-react'
import { api } from '@/services/api'
import type {
  AuthSettingsEffective,
  AuthSettingsPutRequest,
  AuthSettingsResponse,
} from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { Modal } from '@/components/shared/Modal'

// AuthSettingsTab is the spec #09 "restart required" domain. Unlike
// every other Settings tab — which hot-reloads — auth changes only
// take effect after the process restarts because the JWT service and
// auth middleware are wired into every authenticated route at boot.
//
// pendingRestart flips to true when the resolved Auth config differs
// from the boot snapshot recorded in the backend. When true, a green
// banner appears with the "Restart now" button. The button triggers
// os.Exit(0) on the backend — Kubernetes restartPolicy:Always brings
// up a fresh container with the persisted values applied.
//
// Editable subset:
//   - Access token TTL (60s - 24h)
//   - Refresh token TTL (5m - 365d)
//
// Read-only:
//   - Auth enabled status (env-controlled)
//   - JWT secret source (env vs auto-generated)

interface FormState {
  accessTokenSeconds: string
  refreshTokenSeconds: string
}

function stateFromResponse(data: AuthSettingsResponse): FormState {
  return {
    accessTokenSeconds: String(data.effective.accessTokenExpirySeconds),
    refreshTokenSeconds: String(data.effective.refreshTokenExpirySeconds),
  }
}

function buildPatch(initial: FormState, current: FormState): AuthSettingsPutRequest {
  const patch: AuthSettingsPutRequest['patch'] = {}
  const access = parseInt(current.accessTokenSeconds, 10)
  if (!isNaN(access) && access > 0 && access !== parseInt(initial.accessTokenSeconds, 10)) {
    patch.accessTokenExpirySeconds = access
  }
  const refresh = parseInt(current.refreshTokenSeconds, 10)
  if (!isNaN(refresh) && refresh > 0 && refresh !== parseInt(initial.refreshTokenSeconds, 10)) {
    patch.refreshTokenExpirySeconds = refresh
  }
  return Object.keys(patch).length > 0 ? { patch } : {}
}

// Format a seconds count as a human-friendly duration string. Keeps
// the unit closest to the magnitude so 900 reads as "15m" not "0.25h".
function fmtSeconds(n: number): string {
  if (n < 60) return `${n}s`
  if (n < 3600) return `${Math.round(n / 60)}m`
  if (n < 86400) {
    const h = n / 3600
    return h === Math.floor(h) ? `${h}h` : `${h.toFixed(1)}h`
  }
  const d = n / 86400
  return d === Math.floor(d) ? `${d}d` : `${d.toFixed(1)}d`
}

export function AuthSettingsTab() {
  const queryClient = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'settings', 'auth'],
    queryFn: api.getSettingsAuth,
  })

  if (isLoading) return <LoadingSpinner />
  if (error || !data) {
    return (
      <div className="rounded-lg border border-status-error-dim bg-status-error-dim/30 p-4 text-xs text-status-error">
        Failed to load Auth settings. Refresh the page or check that the backend has BoltDB persistence enabled.
      </div>
    )
  }

  return (
    <AuthSettingsForm
      data={data}
      onSaved={() => queryClient.invalidateQueries({ queryKey: ['admin', 'settings', 'auth'] })}
    />
  )
}

function AuthSettingsForm({
  data,
  onSaved,
}: {
  data: AuthSettingsResponse
  onSaved: () => void
}) {
  const [initial, setInitial] = useState<FormState>(() => stateFromResponse(data))
  const [form, setForm] = useState<FormState>(() => stateFromResponse(data))
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [restartConfirm, setRestartConfirm] = useState(false)

  const dirtyMap = {
    accessTokenSeconds: form.accessTokenSeconds !== initial.accessTokenSeconds,
    refreshTokenSeconds: form.refreshTokenSeconds !== initial.refreshTokenSeconds,
  }
  const isDirty = Object.values(dirtyMap).some(Boolean)

  const saveMutation = useMutation({
    mutationFn: () => api.putSettingsAuth(buildPatch(initial, form)),
    onSuccess: (newData) => {
      const next = stateFromResponse(newData)
      setInitial(next)
      setForm(next)
      setSavedAt(Date.now())
      onSaved()
    },
  })

  const resetMutation = useMutation({
    mutationFn: () => api.resetSettingsAuth(),
    onSuccess: () => onSaved(),
  })

  const restartMutation = useMutation({
    mutationFn: () => api.systemRestart(),
    onSuccess: () => {
      // The backend exits ~1s after responding. From the UI's
      // perspective, the next query refetch will fail until the
      // container comes back. Show a sticky overlay so the operator
      // knows the restart is in flight.
      setRestartConfirm(false)
      // Poll the auth endpoint until it answers — that's the all-clear
      // signal that the new container is up and serving.
      const start = Date.now()
      const tick = async () => {
        try {
          await api.getSettingsAuth()
          // Came back online — refresh the page so every query refetches
          // with the new TTLs in effect.
          window.location.reload()
        } catch {
          if (Date.now() - start < 60_000) {
            setTimeout(tick, 1000)
          }
        }
      }
      setTimeout(tick, 2000)
    },
  })

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    saveMutation.mutate()
  }

  const pendingRestart = data.pendingRestart

  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      {/* Always-on notice that this domain requires restart, distinct
          from the success/pending banners below — operators benefit
          from the up-front warning before they start editing. */}
      <div className="flex items-start gap-2 rounded-xl border border-status-info-dim bg-status-info-dim/30 p-4 text-xs text-status-info">
        <Info className="w-4 h-4 shrink-0 mt-0.5" />
        <div>
          <div className="font-semibold mb-0.5">Auth changes require a restart</div>
          <div className="text-kb-text-secondary">
            The JWT service is wired into every request at boot. New TTLs are persisted
            immediately but won't take effect until the process restarts. Existing sessions
            keep working with the boot-time TTLs; new logins after restart get the new values.
          </div>
        </div>
      </div>

      {pendingRestart && !restartMutation.isPending && (
        <div className="flex items-start gap-3 rounded-xl border border-status-warn-dim bg-status-warn-dim/30 p-4">
          <RefreshCw className="w-4 h-4 shrink-0 mt-0.5 text-status-warn" />
          <div className="flex-1 min-w-0">
            <div className="text-xs font-semibold text-status-warn">Pending restart</div>
            <div className="text-[11px] text-kb-text-secondary mt-0.5 leading-relaxed">
              The values stored here differ from what the running process is using. Restart
              KubeBolt to apply.
            </div>
            {/* Side-by-side current vs pending comparison so the operator
                knows exactly what's about to change. */}
            <div className="mt-2 grid grid-cols-2 gap-3 text-[11px]">
              <DiffBlock title="Running now" eff={data.bootSnapshot} />
              <DiffBlock title="Will be after restart" eff={data.effective} />
            </div>
          </div>
          <button
            type="button"
            onClick={() => setRestartConfirm(true)}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-status-warn text-white hover:opacity-90 shrink-0"
          >
            <Power className="w-3.5 h-3.5" />
            Restart now
          </button>
        </div>
      )}

      {restartMutation.isPending && (
        <div className="flex items-start gap-2 rounded-xl border border-kb-border bg-kb-card p-4 text-xs text-kb-text-secondary">
          <Loader2 className="w-4 h-4 shrink-0 mt-0.5 animate-spin" />
          <div>
            <div className="text-kb-text-primary font-semibold mb-0.5">Restarting…</div>
            <div>
              Waiting for the new container to come online. The page will reload automatically.
              If it doesn't, restart by hand and refresh.
            </div>
          </div>
        </div>
      )}

      {/* Current state (read-only) */}
      <SectionCard
        icon={<KeyRound className="w-4 h-4 text-kb-accent" />}
        title="Current state"
        subtitle="The values the running process is using right now. Edit below — changes apply on next restart."
      >
        <div className="grid grid-cols-2 gap-4 text-xs">
          <ReadField
            label="Authentication"
            value={data.bootSnapshot.enabled ? 'Enabled' : 'Disabled'}
            helper="Controlled by KUBEBOLT_AUTH_ENABLED at boot. Toggling auth off from the UI isn't supported in V1."
          />
          <ReadField
            label="JWT secret"
            value={data.jwtSecretMasked || '—'}
            subValue={data.jwtSecretFromEnv ? 'set via KUBEBOLT_JWT_SECRET' : 'auto-generated, stored in BoltDB'}
            helper="Rotate via env only — UI rotation would invalidate every refresh token and every encrypted settings secret."
          />
          <ReadField
            label="Access token TTL"
            value={fmtSeconds(data.bootSnapshot.accessTokenExpirySeconds)}
            helper={`${data.bootSnapshot.accessTokenExpirySeconds}s. Short-lived bearer used on every API call.`}
          />
          <ReadField
            label="Refresh token TTL"
            value={fmtSeconds(data.bootSnapshot.refreshTokenExpirySeconds)}
            helper={`${data.bootSnapshot.refreshTokenExpirySeconds}s. Cookie that lets the UI silently get fresh access tokens.`}
          />
        </div>
      </SectionCard>

      {/* Editable TTLs */}
      <SectionCard
        title="Token TTLs"
        subtitle="Shorter access TTLs reduce blast radius if a token leaks; longer refresh TTLs reduce login frequency."
      >
        <Field
          label="Access token TTL (seconds)"
          dirty={dirtyMap.accessTokenSeconds}
          helper="60 – 86400 (1m – 24h). Default 900 (15m)."
        >
          <input
            type="number"
            min={60}
            max={86400}
            className="w-40 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.accessTokenSeconds}
            onChange={(e) => setForm({ ...form, accessTokenSeconds: e.target.value })}
          />
        </Field>

        <Field
          label="Refresh token TTL (seconds)"
          dirty={dirtyMap.refreshTokenSeconds}
          helper="300 – 31536000 (5m – 365d). Default 604800 (7d). Must be greater than the access token TTL."
        >
          <input
            type="number"
            min={300}
            max={31536000}
            className="w-40 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            value={form.refreshTokenSeconds}
            onChange={(e) => setForm({ ...form, refreshTokenSeconds: e.target.value })}
          />
        </Field>
      </SectionCard>

      {/* Action card with banners */}
      <div className="bg-kb-card border border-kb-border rounded-xl">
        <div className="p-3 flex items-center justify-between gap-3">
          <button
            type="button"
            onClick={() => {
              if (confirm('Reset Auth settings to environment defaults? Stored TTL overrides will be cleared on next save read.')) {
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

        {savedAt && !isDirty && !saveMutation.isPending && !pendingRestart && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">
            <CheckCircle2 className="w-4 h-4 mt-0.5 shrink-0" />
            <div>Auth settings saved. The running process is already using these values.</div>
          </div>
        )}

        {savedAt && !isDirty && !saveMutation.isPending && pendingRestart && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-info-dim text-status-info text-xs">
            <CheckCircle2 className="w-4 h-4 mt-0.5 shrink-0" />
            <div>Saved. A restart is required to apply — use the "Restart now" button above when ready.</div>
          </div>
        )}
      </div>

      {restartConfirm && (
        <RestartConfirmDialog
          onCancel={() => setRestartConfirm(false)}
          onConfirm={() => restartMutation.mutate()}
        />
      )}
    </form>
  )
}

function RestartConfirmDialog({
  onCancel,
  onConfirm,
}: {
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Modal badge="Restart" title="Restart KubeBolt?" onClose={onCancel} size="sm" unbounded>
      <div className="p-5 space-y-3">
        <p className="text-xs text-kb-text-secondary leading-relaxed">
          The backend will exit and Kubernetes will start a fresh container with the new Auth
          settings. Active sessions stay valid; the UI reconnects automatically after ~10–30
          seconds.
        </p>
        <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
          Outside Kubernetes (e.g. <code className="font-mono text-kb-accent">go run</code> locally) the process just exits
          — restart it manually.
        </p>
      </div>
      <div className="px-5 py-3 border-t border-kb-border flex items-center justify-end gap-2 shrink-0">
        <button
          type="button"
          onClick={onCancel}
          className="px-3 py-1.5 rounded-md text-xs text-kb-text-secondary hover:bg-kb-elevated border border-kb-border"
        >
          Cancel
        </button>
        <button
          type="button"
          onClick={onConfirm}
          className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-status-warn text-white hover:opacity-90"
        >
          <Power className="w-3.5 h-3.5" />
          Restart now
        </button>
      </div>
    </Modal>
  )
}

function DiffBlock({ title, eff }: { title: string; eff: AuthSettingsEffective }) {
  return (
    <div className="rounded-md bg-kb-bg border border-kb-border p-2">
      <div className="text-[10px] font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider mb-1">
        {title}
      </div>
      <div className="space-y-0.5 text-[11px] text-kb-text-primary font-mono">
        <div>access: {fmtSeconds(eff.accessTokenExpirySeconds)}</div>
        <div>refresh: {fmtSeconds(eff.refreshTokenExpirySeconds)}</div>
      </div>
    </div>
  )
}

function ReadField({
  label,
  value,
  subValue,
  helper,
}: {
  label: string
  value: string
  subValue?: string
  helper: string
}) {
  return (
    <div className="space-y-1">
      <div className="text-[10px] font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider">
        {label}
      </div>
      <div className="text-xs font-mono text-kb-text-primary">{value}</div>
      {subValue && (
        <div className="text-[10px] font-mono text-kb-text-tertiary">{subValue}</div>
      )}
      <p className="text-[11px] text-kb-text-tertiary leading-relaxed">{helper}</p>
    </div>
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
