import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  CheckCircle2,
  Gauge,
  Info,
  KeyRound,
  Loader2,
  Network,
  Power,
  RefreshCw,
  RotateCcw,
  Save,
  ShieldCheck,
  Sparkles,
  Timer,
  X,
} from 'lucide-react'
import { api } from '@/services/api'
import type {
  IngestChannelEffective,
  IngestChannelSettingsPutRequest,
  IngestChannelSettingsResponse,
} from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { Modal } from '@/components/shared/Modal'
import { ConfirmDialog } from './ConfirmDialog'
import { Field } from './SettingsField'
import { PerTenantLimitsSection, type PerTenantLimitsHandle } from './PerTenantLimitsSection'

// IngestSettingsTab (Settings → Agents & Ingest) is the spec #09 V2
// surface for everything on the kubebolt-agent ↔ kubebolt data plane.
// Six sections in operator-setup order:
//
//   1. Channel security      — agent auth mode + token audience + mTLS  (RESTART-REQUIRED)
//   2. Cluster auto-register  — agent-proxy peers in the switcher        (hot-reload)
//   3. Rate limiting          — fleet-wide gRPC ingest token bucket     (hot-reload)
//   4. Remote write receiver  — Prom remote_write gate + auth + defaults (mostly hot)
//   5. Per-tenant overrides   — runtime overrides on the limits above    (hot-reload, per-row save)
//   6. SPDY tunnels           — idle watchdog for exec/portforward       (hot-reload)
//
// Cards 1+3+4+6 are channel-wide and share the form's single Save
// button at the bottom. Per-tenant overrides (card 5) has its own
// per-row save because each tenant is an independent record — the
// visual chrome matches, but the save semantics differ. The pendingRestart
// banner only flips on the three restart-required fields in Channel
// security; hot-reload toggles never trigger it.
//
// Layout: each card's content is a 2-column grid on md+ so the
// form doesn't sprawl vertically. Tall-helper fields (auth mode
// with dynamic description, cross-field warnings) span both columns.

interface FormState {
  agentAuthMode: string
  agentTokenAudience: string
  agentRequireMTLS: boolean
  agentRateLimitEnabled: boolean
  agentRateLimitRPS: string
  agentRateLimitBurst: string
  agentAutoRegisterClusters: boolean
  agentRegistryPruneHorizonSecs: string
  remoteWriteEnabled: boolean
  remoteWriteAuthMode: string
  promWriteDefaultSamplesPerSec: string
  promWriteDefaultBurstSamples: string
  promWriteDefaultMaxActiveSeries: string
  promWriteDefaultMaxActiveSeriesGlobal: string
  agentTunnelIdleTimeoutSecs: string
}

function stateFromResponse(d: IngestChannelSettingsResponse): FormState {
  const e = d.effective
  return {
    agentAuthMode: e.agentAuthMode,
    agentTokenAudience: e.agentTokenAudience,
    agentRequireMTLS: e.agentRequireMTLS,
    agentRateLimitEnabled: e.agentRateLimitEnabled,
    agentRateLimitRPS: String(e.agentRateLimitRPS),
    agentRateLimitBurst: String(e.agentRateLimitBurst),
    agentAutoRegisterClusters: e.agentAutoRegisterClusters,
    agentRegistryPruneHorizonSecs: String(e.agentRegistryPruneHorizonSecs),
    remoteWriteEnabled: e.remoteWriteEnabled,
    remoteWriteAuthMode: e.remoteWriteAuthMode,
    promWriteDefaultSamplesPerSec: String(e.promWriteDefaultSamplesPerSec),
    promWriteDefaultBurstSamples: String(e.promWriteDefaultBurstSamples),
    promWriteDefaultMaxActiveSeries: String(e.promWriteDefaultMaxActiveSeries),
    promWriteDefaultMaxActiveSeriesGlobal: String(e.promWriteDefaultMaxActiveSeriesGlobal),
    agentTunnelIdleTimeoutSecs: String(e.agentTunnelIdleTimeoutSecs),
  }
}

function buildPatch(initial: FormState, current: FormState): IngestChannelSettingsPutRequest {
  const patch: NonNullable<IngestChannelSettingsPutRequest['patch']> = {}
  if (current.agentAuthMode !== initial.agentAuthMode) {
    patch.agentAuthMode = current.agentAuthMode
  }
  if (current.agentTokenAudience !== initial.agentTokenAudience) {
    patch.agentTokenAudience = current.agentTokenAudience
  }
  if (current.agentRequireMTLS !== initial.agentRequireMTLS) {
    patch.agentRequireMTLS = current.agentRequireMTLS
  }
  if (current.agentRateLimitEnabled !== initial.agentRateLimitEnabled) {
    patch.agentRateLimitEnabled = current.agentRateLimitEnabled
  }
  const rps = parseInt(current.agentRateLimitRPS, 10)
  if (!isNaN(rps) && rps > 0 && rps !== parseInt(initial.agentRateLimitRPS, 10)) {
    patch.agentRateLimitRPS = rps
  }
  const burst = parseInt(current.agentRateLimitBurst, 10)
  if (!isNaN(burst) && burst > 0 && burst !== parseInt(initial.agentRateLimitBurst, 10)) {
    patch.agentRateLimitBurst = burst
  }
  if (current.agentAutoRegisterClusters !== initial.agentAutoRegisterClusters) {
    patch.agentAutoRegisterClusters = current.agentAutoRegisterClusters
  }
  const prune = parseInt(current.agentRegistryPruneHorizonSecs, 10)
  if (!isNaN(prune) && prune > 0 && prune !== parseInt(initial.agentRegistryPruneHorizonSecs, 10)) {
    patch.agentRegistryPruneHorizonSecs = prune
  }
  if (current.remoteWriteEnabled !== initial.remoteWriteEnabled) {
    patch.remoteWriteEnabled = current.remoteWriteEnabled
  }
  if (current.remoteWriteAuthMode !== initial.remoteWriteAuthMode) {
    patch.remoteWriteAuthMode = current.remoteWriteAuthMode
  }
  const samples = parseInt(current.promWriteDefaultSamplesPerSec, 10)
  if (!isNaN(samples) && samples > 0 && samples !== parseInt(initial.promWriteDefaultSamplesPerSec, 10)) {
    patch.promWriteDefaultSamplesPerSec = samples
  }
  const samplesBurst = parseInt(current.promWriteDefaultBurstSamples, 10)
  if (
    !isNaN(samplesBurst) &&
    samplesBurst > 0 &&
    samplesBurst !== parseInt(initial.promWriteDefaultBurstSamples, 10)
  ) {
    patch.promWriteDefaultBurstSamples = samplesBurst
  }
  const series = parseInt(current.promWriteDefaultMaxActiveSeries, 10)
  if (
    !isNaN(series) &&
    series > 0 &&
    series !== parseInt(initial.promWriteDefaultMaxActiveSeries, 10)
  ) {
    patch.promWriteDefaultMaxActiveSeries = series
  }
  // Global cap accepts 0 (disabled) as a meaningful explicit value.
  const seriesGlobal = parseInt(current.promWriteDefaultMaxActiveSeriesGlobal, 10)
  if (
    !isNaN(seriesGlobal) &&
    seriesGlobal >= 0 &&
    seriesGlobal !== parseInt(initial.promWriteDefaultMaxActiveSeriesGlobal, 10)
  ) {
    patch.promWriteDefaultMaxActiveSeriesGlobal = seriesGlobal
  }
  // Tunnel idle 0 = watchdog off; same explicit-zero semantic.
  const tunnel = parseInt(current.agentTunnelIdleTimeoutSecs, 10)
  if (
    !isNaN(tunnel) &&
    tunnel >= 0 &&
    tunnel !== parseInt(initial.agentTunnelIdleTimeoutSecs, 10)
  ) {
    patch.agentTunnelIdleTimeoutSecs = tunnel
  }
  return Object.keys(patch).length > 0 ? { patch } : {}
}

function fmtSeconds(n: number): string {
  if (n === 0) return 'off'
  if (n < 60) return `${n}s`
  if (n < 3600) return `${Math.round(n / 60)}m`
  if (n < 86400) {
    const h = n / 3600
    return h === Math.floor(h) ? `${h}h` : `${h.toFixed(1)}h`
  }
  const d = n / 86400
  return d === Math.floor(d) ? `${d}d` : `${d.toFixed(1)}d`
}

const AUTH_MODE_OPTIONS = [
  { value: 'disabled', label: 'Disabled', desc: 'Accept every connection. Use only during migration.' },
  { value: 'permissive', label: 'Permissive', desc: 'Try to authenticate; on failure log + accept. Useful while rolling tokens out.' },
  { value: 'enforced', label: 'Enforced', desc: 'Reject any unauthenticated / failed connection. Production posture.' },
] as const

export function IngestSettingsTab() {
  const queryClient = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'settings', 'ingest-channel'],
    queryFn: api.getSettingsIngestChannel,
  })

  if (isLoading) return <LoadingSpinner />
  if (error || !data) {
    // Per-tenant section is independent and still useful even when
    // the channel-wide GET fails. Render the error inline at the
    // position the channel form would have taken, and let per-tenant
    // fall in its natural slot.
    return (
      <div className="space-y-5">
        <div className="rounded-lg border border-status-error-dim bg-status-error-dim/30 p-4 text-xs text-status-error">
          Failed to load channel-wide settings. Refresh the page or check that the backend has BoltDB persistence enabled. Per-tenant overrides below still work.
        </div>
        <PerTenantLimitsSection />
      </div>
    )
  }

  // Channel form owns the cards + the bottom Save bar; per-tenant
  // overrides is slotted in BETWEEN remote write and SPDY tunnels via
  // a render prop, so the whole page reads as one continuous list of
  // sections without a "this is the new bit / this is the old bit"
  // visual divider.
  return (
    <IngestChannelForm
      data={data}
      onSaved={() => queryClient.invalidateQueries({ queryKey: ['admin', 'settings', 'ingest-channel'] })}
    />
  )
}

function IngestChannelForm({
  data,
  onSaved,
}: {
  data: IngestChannelSettingsResponse
  onSaved: () => void
}) {
  const [initial, setInitial] = useState<FormState>(() => stateFromResponse(data))
  const [form, setForm] = useState<FormState>(() => stateFromResponse(data))
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [restartConfirm, setRestartConfirm] = useState(false)
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false)
  // Confirmation gate for "switching to enforced" — operationally
  // sensitive because agents without valid auth will be rejected after
  // the next restart.
  const [enforcedConfirmOpen, setEnforcedConfirmOpen] = useState(false)

  // V2 polish — unified save / reset across channel-wide + per-tenant
  // sections. Per-tenant is its own component with its own state and
  // its own API endpoint; we drive it imperatively via this ref so
  // the operator sees ONE Save button at the bottom that covers both.
  const perTenantRef = useRef<PerTenantLimitsHandle>(null)
  const [perTenantState, setPerTenantState] = useState<{ isDirty: boolean; hasCustom: boolean }>({
    isDirty: false,
    hasCustom: false,
  })
  // Combined error / warnings bubbled up from the per-tenant save so
  // the parent's status banners can surface them in the unified bottom
  // bar instead of the per-tenant section rendering its own.
  const [perTenantError, setPerTenantError] = useState<string | null>(null)
  const [perTenantWarnings, setPerTenantWarnings] = useState<string[]>([])

  const dirtyMap = {
    agentAuthMode: form.agentAuthMode !== initial.agentAuthMode,
    agentTokenAudience: form.agentTokenAudience !== initial.agentTokenAudience,
    agentRequireMTLS: form.agentRequireMTLS !== initial.agentRequireMTLS,
    agentRateLimitEnabled: form.agentRateLimitEnabled !== initial.agentRateLimitEnabled,
    agentRateLimitRPS: form.agentRateLimitRPS !== initial.agentRateLimitRPS,
    agentRateLimitBurst: form.agentRateLimitBurst !== initial.agentRateLimitBurst,
    agentAutoRegisterClusters: form.agentAutoRegisterClusters !== initial.agentAutoRegisterClusters,
    agentRegistryPruneHorizonSecs:
      form.agentRegistryPruneHorizonSecs !== initial.agentRegistryPruneHorizonSecs,
    remoteWriteEnabled: form.remoteWriteEnabled !== initial.remoteWriteEnabled,
    remoteWriteAuthMode: form.remoteWriteAuthMode !== initial.remoteWriteAuthMode,
    promWriteDefaultSamplesPerSec:
      form.promWriteDefaultSamplesPerSec !== initial.promWriteDefaultSamplesPerSec,
    promWriteDefaultBurstSamples:
      form.promWriteDefaultBurstSamples !== initial.promWriteDefaultBurstSamples,
    promWriteDefaultMaxActiveSeries:
      form.promWriteDefaultMaxActiveSeries !== initial.promWriteDefaultMaxActiveSeries,
    promWriteDefaultMaxActiveSeriesGlobal:
      form.promWriteDefaultMaxActiveSeriesGlobal !== initial.promWriteDefaultMaxActiveSeriesGlobal,
    agentTunnelIdleTimeoutSecs:
      form.agentTunnelIdleTimeoutSecs !== initial.agentTunnelIdleTimeoutSecs,
  }
  // Channel-wide dirty (the 15 fields above) OR per-tenant dirty
  // (the 3 fields inside PerTenantLimitsSection). Save button at the
  // bottom is enabled when EITHER side has changes.
  const channelDirty = Object.values(dirtyMap).some(Boolean)
  const isDirty = channelDirty || perTenantState.isDirty

  const saveMutation = useMutation({
    // Combines channel save (when channelDirty) + per-tenant save
    // (when perTenantState.isDirty) into one operator-facing action.
    // Each is independent at the API level; we await both and
    // aggregate the warnings. Errors from either propagate to
    // saveMutation.error so the bottom-bar status banner picks them up.
    mutationFn: async () => {
      setPerTenantError(null)
      setPerTenantWarnings([])
      let newChannelData: IngestChannelSettingsResponse | null = null
      let perTenantWarns: string[] = []

      if (channelDirty) {
        newChannelData = await api.putSettingsIngestChannel(buildPatch(initial, form))
      }
      if (perTenantState.isDirty && perTenantRef.current) {
        try {
          const result = await perTenantRef.current.save()
          perTenantWarns = result.warnings
        } catch (err) {
          // Surface in the bottom-bar status panel and re-throw so the
          // mutation registers as failed (banner stays red).
          setPerTenantError(err instanceof Error ? err.message : 'Failed to save per-tenant limits')
          throw err
        }
      }
      return { newChannelData, perTenantWarns }
    },
    onSuccess: ({ newChannelData, perTenantWarns }) => {
      if (newChannelData) {
        const next = stateFromResponse(newChannelData)
        setInitial(next)
        setForm(next)
      }
      setPerTenantWarnings(perTenantWarns)
      setSavedAt(Date.now())
      onSaved()
    },
  })

  const resetMutation = useMutation({
    // Mirrors saveMutation — clears BOTH the channel-wide BoltDB row
    // AND the per-tenant overrides (when hasCustom). The unified
    // confirm modal warns the operator that both are wiped.
    mutationFn: async () => {
      setPerTenantError(null)
      setPerTenantWarnings([])
      await api.resetSettingsIngestChannel()
      if (perTenantState.hasCustom && perTenantRef.current) {
        try {
          await perTenantRef.current.reset()
        } catch (err) {
          setPerTenantError(err instanceof Error ? err.message : 'Failed to reset per-tenant limits')
          throw err
        }
      }
    },
    onSuccess: () => onSaved(),
  })

  const restartMutation = useMutation({
    mutationFn: () => api.systemRestart(),
    onSuccess: () => {
      setRestartConfirm(false)
      const start = Date.now()
      const tick = async () => {
        try {
          await api.getSettingsIngestChannel()
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

  // If the user is escalating to "enforced" AND the running interceptor
  // is NOT already enforced, show the confirmation modal before save.
  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const escalatingToEnforced =
      form.agentAuthMode === 'enforced' &&
      data.bootSnapshot.agentAuthMode !== 'enforced'
    if (escalatingToEnforced) {
      setEnforcedConfirmOpen(true)
      return
    }
    saveMutation.mutate()
  }

  const pendingRestart = data.pendingRestart

  // Cross-field warning: enabling the remote_write receiver while its
  // auth mode is disabled = unauthenticated endpoint. Not blocking
  // (operator may want this in dev), just surfaced.
  const remoteWriteUnauth =
    form.remoteWriteEnabled && form.remoteWriteAuthMode === 'disabled'

  return (
    <form onSubmit={handleSubmit} className="space-y-5">
      <div className="flex items-start gap-2 rounded-xl border border-status-info-dim bg-status-info-dim/30 p-4 text-xs text-status-info">
        <Info className="w-4 h-4 shrink-0 mt-0.5" />
        <div>
          <div className="font-semibold mb-0.5">Channel security requires a restart to apply</div>
          <div className="text-kb-text-secondary">
            Auth mode, token audience, and require-mTLS are wired into the gRPC interceptor at boot.
            Rate limiting, auto-register, remote_write, and tunnel timeout apply on the next request
            or tick — no restart needed.
          </div>
        </div>
      </div>

      {pendingRestart && !restartMutation.isPending && (
        <div className="flex items-start gap-3 rounded-xl border border-status-warn-dim bg-status-warn-dim/30 p-4">
          <RefreshCw className="w-4 h-4 shrink-0 mt-0.5 text-status-warn" />
          <div className="flex-1 min-w-0">
            <div className="text-xs font-semibold text-status-warn">Pending restart</div>
            <div className="text-[11px] text-kb-text-secondary mt-0.5 leading-relaxed">
              Channel-security values differ from what the running gRPC interceptor is using.
              Restart KubeBolt to apply.
            </div>
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
            </div>
          </div>
        </div>
      )}

      {/* ─── 1. Channel security (restart-required) ──────────────────── */}
      <SectionCard
        icon={<ShieldCheck className="w-4 h-4 text-kb-accent" />}
        title="Channel security"
        subtitle="How kubebolt-agent authenticates to this backend over gRPC. Changes require a restart."
      >
        <div className="grid grid-cols-1 md:grid-cols-2 gap-x-5 gap-y-4">
          {/* Auth mode spans both columns because its dynamic description
              text wraps below the select and benefits from full width. */}
          <div className="md:col-span-2">
            <Field
              stacked
              label="Agent auth mode"
              dirty={dirtyMap.agentAuthMode}
              helper={
                AUTH_MODE_OPTIONS.find((o) => o.value === form.agentAuthMode)?.desc ||
                'Three-tier enforcement on the gRPC channel.'
              }
            >
              <select
                value={form.agentAuthMode}
                onChange={(e) => setForm({ ...form, agentAuthMode: e.target.value })}
                className="w-48 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
              >
                {AUTH_MODE_OPTIONS.map((opt) => (
                  <option key={opt.value} value={opt.value}>
                    {opt.label}
                  </option>
                ))}
              </select>
            </Field>
          </div>

          <Field
            stacked
            label="Token audience"
            dirty={dirtyMap.agentTokenAudience}
            helper="Must match the agent helm chart's auth.tokenReview.audience value."
          >
            <input
              type="text"
              placeholder="kubebolt-backend"
              value={form.agentTokenAudience}
              onChange={(e) => setForm({ ...form, agentTokenAudience: e.target.value })}
              className="w-full px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary font-mono focus:outline-none focus:border-kb-accent"
            />
          </Field>

          <Field
            stacked
            label="Require mTLS"
            dirty={dirtyMap.agentRequireMTLS}
            helper="Rejects clients without a verified TLS client cert. Requires KUBEBOLT_AGENT_TLS_CLIENT_CA in env."
          >
            <ToggleSwitch
              checked={form.agentRequireMTLS}
              onChange={(checked) => setForm({ ...form, agentRequireMTLS: checked })}
            />
          </Field>
        </div>
      </SectionCard>

      {/* ─── 2. Cluster auto-registration ──────────────────────────────── */}
      <SectionCard
        icon={<Sparkles className="w-4 h-4 text-kb-accent" />}
        title="Cluster auto-registration"
        subtitle="When on, every agent advertising kube-proxy surfaces its cluster in the switcher without operator action."
      >
        <div className="grid grid-cols-1 md:grid-cols-2 gap-x-5 gap-y-4">
          <Field
            stacked
            label="Auto-register agent-proxy clusters"
            dirty={dirtyMap.agentAutoRegisterClusters}
            helper="Off for single-cluster setups. On for multi-cluster fleet operators."
          >
            <ToggleSwitch
              checked={form.agentAutoRegisterClusters}
              onChange={(checked) => setForm({ ...form, agentAutoRegisterClusters: checked })}
            />
          </Field>

          <Field
            stacked
            label="Registry prune horizon (seconds)"
            dirty={dirtyMap.agentRegistryPruneHorizonSecs}
            helper={`Disconnected records older than this are pruned hourly. Currently: ${fmtSeconds(parseInt(form.agentRegistryPruneHorizonSecs, 10) || 0)}.`}
          >
            <input
              type="number"
              min={1}
              value={form.agentRegistryPruneHorizonSecs}
              onChange={(e) =>
                setForm({ ...form, agentRegistryPruneHorizonSecs: e.target.value })
              }
              className="w-40 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            />
          </Field>
        </div>
      </SectionCard>

      {/* ─── 3. Rate limiting (gRPC fleet-wide) ────────────────────────── */}
      <SectionCard
        icon={<Gauge className="w-4 h-4 text-kb-accent" />}
        title="Rate limiting"
        subtitle="Fleet-wide token bucket for the gRPC ingest. Per-tenant overrides below win when set."
      >
        <div className="grid grid-cols-1 md:grid-cols-3 gap-x-5 gap-y-4">
          <Field
            stacked
            label="Enabled"
            dirty={dirtyMap.agentRateLimitEnabled}
            helper="Off by default — V1 OSS shares the same limit across tenants."
          >
            <ToggleSwitch
              checked={form.agentRateLimitEnabled}
              onChange={(checked) => setForm({ ...form, agentRateLimitEnabled: checked })}
            />
          </Field>

          <Field
            stacked
            label="Requests per second"
            dirty={dirtyMap.agentRateLimitRPS}
            helper="Sustained rate. Default 1000."
          >
            <input
              type="number"
              min={1}
              disabled={!form.agentRateLimitEnabled}
              value={form.agentRateLimitRPS}
              onChange={(e) => setForm({ ...form, agentRateLimitRPS: e.target.value })}
              className="w-32 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
            />
          </Field>

          <Field
            stacked
            label="Burst"
            dirty={dirtyMap.agentRateLimitBurst}
            helper="Peak burst before rate-limiting kicks in. Default 2000."
          >
            <input
              type="number"
              min={1}
              disabled={!form.agentRateLimitEnabled}
              value={form.agentRateLimitBurst}
              onChange={(e) => setForm({ ...form, agentRateLimitBurst: e.target.value })}
              className="w-32 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
            />
          </Field>
        </div>
      </SectionCard>

      {/* ─── 4. Remote write receiver ──────────────────────────────────── */}
      <SectionCard
        icon={<Network className="w-4 h-4 text-kb-accent" />}
        title="Remote write receiver"
        subtitle="HTTP endpoint that accepts Prometheus remote_write payloads from vmagent / external Prom installs."
      >
        <div className="grid grid-cols-1 md:grid-cols-2 gap-x-5 gap-y-4">
          <Field
            stacked
            label="Enabled"
            dirty={dirtyMap.remoteWriteEnabled}
            helper="Mounts POST /api/v1/prom/write. Off by default."
          >
            <ToggleSwitch
              checked={form.remoteWriteEnabled}
              onChange={(checked) => setForm({ ...form, remoteWriteEnabled: checked })}
            />
          </Field>

          <Field
            stacked
            label="Auth mode"
            dirty={dirtyMap.remoteWriteAuthMode}
            helper="Bearer-token check on the receiver endpoint."
          >
            <select
              disabled={!form.remoteWriteEnabled}
              value={form.remoteWriteAuthMode}
              onChange={(e) => setForm({ ...form, remoteWriteAuthMode: e.target.value })}
              className="w-48 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
            >
              {AUTH_MODE_OPTIONS.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </select>
          </Field>

          <Field
            stacked
            label="Default samples/sec per tenant"
            dirty={dirtyMap.promWriteDefaultSamplesPerSec}
            helper="Fleet default for tenants without per-tenant override. Default 10000. Captured at boot — applies on next restart."
          >
            <input
              type="number"
              min={1}
              disabled={!form.remoteWriteEnabled}
              value={form.promWriteDefaultSamplesPerSec}
              onChange={(e) =>
                setForm({ ...form, promWriteDefaultSamplesPerSec: e.target.value })
              }
              className="w-32 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
            />
          </Field>

          <Field
            stacked
            label="Default burst per tenant"
            dirty={dirtyMap.promWriteDefaultBurstSamples}
            helper="Token bucket size. Default 100000."
          >
            <input
              type="number"
              min={1}
              disabled={!form.remoteWriteEnabled}
              value={form.promWriteDefaultBurstSamples}
              onChange={(e) =>
                setForm({ ...form, promWriteDefaultBurstSamples: e.target.value })
              }
              className="w-32 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
            />
          </Field>

          <Field
            stacked
            label="Default max active series per tenant"
            dirty={dirtyMap.promWriteDefaultMaxActiveSeries}
            helper="Above the cap → 413 + Retry-After. Default 1000000."
          >
            <input
              type="number"
              min={1}
              disabled={!form.remoteWriteEnabled}
              value={form.promWriteDefaultMaxActiveSeries}
              onChange={(e) =>
                setForm({ ...form, promWriteDefaultMaxActiveSeries: e.target.value })
              }
              className="w-32 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
            />
          </Field>

          <Field
            stacked
            label="Global active-series cap"
            dirty={dirtyMap.promWriteDefaultMaxActiveSeriesGlobal}
            helper="Hard cap across all tenants. 0 = disabled (per-tenant caps only)."
          >
            <input
              type="number"
              min={0}
              disabled={!form.remoteWriteEnabled}
              value={form.promWriteDefaultMaxActiveSeriesGlobal}
              onChange={(e) =>
                setForm({ ...form, promWriteDefaultMaxActiveSeriesGlobal: e.target.value })
              }
              className="w-32 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent disabled:opacity-40"
            />
          </Field>

          {remoteWriteUnauth && (
            <div className="md:col-span-2 flex items-start gap-2 rounded-md bg-status-warn-dim/40 border border-status-warn-dim p-2 text-[11px] text-status-warn leading-relaxed">
              <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
              <div>
                Remote write is on AND auth mode is <code className="font-mono">disabled</code>.
                Any network-reachable client can POST samples to{' '}
                <code className="font-mono">/api/v1/prom/write</code>. Set auth mode to{' '}
                <code className="font-mono">permissive</code> or{' '}
                <code className="font-mono">enforced</code> for production.
              </div>
            </div>
          )}
        </div>
      </SectionCard>

      {/* ─── 5. Per-tenant overrides ──────────────────────────────────── */}
      {/* Slotted INLINE between Remote write and SPDY tunnels so the
          page reads as one continuous list. Embedded mode hides the
          per-tenant card's own action bar + banners; the bottom Save
          bar of this form covers BOTH channel-wide AND per-tenant
          changes in a single operator-facing action. */}
      <PerTenantLimitsSection
        ref={perTenantRef}
        embedded
        onStateChange={setPerTenantState}
      />

      {/* ─── 6. SPDY tunnels ───────────────────────────────────────────── */}
      <SectionCard
        icon={<Timer className="w-4 h-4 text-kb-accent" />}
        title="SPDY tunnels"
        subtitle="Idle watchdog for exec / port-forward / file browser sessions routed through agent-proxy."
      >
        <div className="grid grid-cols-1 md:grid-cols-2 gap-x-5 gap-y-4">
          <Field
            stacked
            label="Idle timeout (seconds)"
            dirty={dirtyMap.agentTunnelIdleTimeoutSecs}
            helper={`Closes inactive tunnels. 0 disables (tests only). Default 300 (5m). Currently: ${fmtSeconds(parseInt(form.agentTunnelIdleTimeoutSecs, 10) || 0)}. Applies to new tunnels.`}
          >
            <input
              type="number"
              min={0}
              value={form.agentTunnelIdleTimeoutSecs}
              onChange={(e) =>
                setForm({ ...form, agentTunnelIdleTimeoutSecs: e.target.value })
              }
              className="w-40 px-2 py-1.5 rounded-md bg-kb-bg border border-kb-border text-xs text-kb-text-primary focus:outline-none focus:border-kb-accent"
            />
          </Field>
        </div>
      </SectionCard>

      {/* Action bar */}
      <div className="bg-kb-card border border-kb-border rounded-xl">
        <div className="p-3 flex items-center justify-between gap-3">
          <button
            type="button"
            onClick={() => setResetConfirmOpen(true)}
            disabled={resetMutation.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            <RotateCcw className="w-3.5 h-3.5" />
            {resetMutation.isPending ? 'Resetting…' : 'Reset to env defaults'}
          </button>
          <div className="flex items-center gap-2">
            {channelDirty && !saveMutation.isPending && (
              <button
                type="button"
                onClick={() => setForm(initial)}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors"
                title="Revert channel-wide changes. Per-tenant overrides keep their edits — clear them individually in the per-tenant card."
              >
                <X className="w-3.5 h-3.5" />
                Cancel
              </button>
            )}
            <button
              type="submit"
              disabled={!isDirty || saveMutation.isPending}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            >
              {saveMutation.isPending ? (
                <Loader2 className="w-3.5 h-3.5 animate-spin" />
              ) : (
                <Save className="w-3.5 h-3.5" />
              )}
              {saveMutation.isPending ? 'Saving…' : 'Save changes'}
            </button>
          </div>
        </div>

        {saveMutation.isError && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <div>{perTenantError || (saveMutation.error as Error)?.message || 'Failed to save.'}</div>
          </div>
        )}

        {savedAt && !isDirty && !saveMutation.isPending && !pendingRestart && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">
            <CheckCircle2 className="w-4 h-4 mt-0.5 shrink-0" />
            <div>Saved. Hot-reload fields are already in effect.</div>
          </div>
        )}

        {perTenantWarnings.length > 0 && !saveMutation.isPending && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-warn-dim text-status-warn text-xs">
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <div className="space-y-0.5">
              {perTenantWarnings.map((w, i) => (
                <div key={i}>{w}</div>
              ))}
            </div>
          </div>
        )}

        {savedAt && !isDirty && !saveMutation.isPending && pendingRestart && (
          <div className="mx-3 mb-3 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-info-dim text-status-info text-xs">
            <CheckCircle2 className="w-4 h-4 mt-0.5 shrink-0" />
            <div>
              Saved. Channel security changes require a restart — use "Restart now" above when ready.
            </div>
          </div>
        )}
      </div>

      <ConfirmDialog
        open={resetConfirmOpen}
        badge="Reset"
        variant="danger"
        title="Reset Agents & Ingest settings to env defaults?"
        description={
          perTenantState.hasCustom
            ? 'Clears every stored override in this domain — channel-wide settings AND per-tenant overrides. The next process start uses the values from the KUBEBOLT_AGENT_* / KUBEBOLT_REMOTE_WRITE_* / KUBEBOLT_PROM_WRITE_DEFAULT_* env vars (or built-in defaults). Channel-security fields take effect on next restart; everything else applies on the next request.'
            : 'Clears every stored override in this domain. The next process start uses the values from the KUBEBOLT_AGENT_* / KUBEBOLT_REMOTE_WRITE_* / KUBEBOLT_PROM_WRITE_DEFAULT_* env vars (or built-in defaults). Channel-security fields take effect on next restart; everything else applies on the next request.'
        }
        confirmLabel="Reset"
        onConfirm={() => {
          setResetConfirmOpen(false)
          resetMutation.mutate()
        }}
        onCancel={() => setResetConfirmOpen(false)}
        busy={resetMutation.isPending}
      />

      {restartConfirm && (
        <RestartConfirmDialog
          onCancel={() => setRestartConfirm(false)}
          onConfirm={() => restartMutation.mutate()}
        />
      )}

      {enforcedConfirmOpen && (
        <EnforcedConfirmDialog
          onCancel={() => setEnforcedConfirmOpen(false)}
          onConfirm={() => {
            setEnforcedConfirmOpen(false)
            saveMutation.mutate()
          }}
        />
      )}
    </form>
  )
}

// ─── Sub-components ────────────────────────────────────────────────────

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

function ToggleSwitch({
  checked,
  onChange,
  disabled,
}: {
  checked: boolean
  onChange: (next: boolean) => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full transition-colors disabled:opacity-40 disabled:cursor-not-allowed ${
        checked ? 'bg-kb-accent' : 'bg-kb-elevated border border-kb-border'
      }`}
    >
      <span
        className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform ${
          checked ? 'translate-x-[1.125rem]' : 'translate-x-0.5'
        }`}
      />
    </button>
  )
}

function DiffBlock({ title, eff }: { title: string; eff: IngestChannelEffective }) {
  return (
    <div className="rounded-md bg-kb-bg border border-kb-border p-2">
      <div className="text-[10px] font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider mb-1">
        {title}
      </div>
      <div className="space-y-0.5 text-[11px] text-kb-text-primary font-mono">
        <div>auth: {eff.agentAuthMode}</div>
        <div>audience: {eff.agentTokenAudience}</div>
        <div>mTLS: {eff.agentRequireMTLS ? 'required' : 'off'}</div>
      </div>
    </div>
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
          The backend will exit and Kubernetes will start a fresh container with the new
          channel-security settings. Connected agents reconnect after ~10–30 seconds.
        </p>
        <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
          Outside Kubernetes (e.g. <code className="font-mono text-kb-accent">go run</code>{' '}
          locally) the process just exits — restart it manually.
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

function EnforcedConfirmDialog({
  onCancel,
  onConfirm,
}: {
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <Modal
      badge="Confirm"
      title="Switch agent auth to enforced?"
      onClose={onCancel}
      size="sm"
      unbounded
    >
      <div className="p-5 space-y-3">
        <div className="flex items-start gap-2 rounded-md bg-status-warn-dim/40 border border-status-warn-dim p-3 text-[11px] text-status-warn leading-relaxed">
          <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
          <div>
            After the next restart, the gRPC server will{' '}
            <strong>reject any agent connection without valid credentials</strong>. Agents
            without ingest tokens or with expired tokens will fail to connect.
          </div>
        </div>
        <p className="text-xs text-kb-text-secondary leading-relaxed">
          Before flipping to enforced, make sure every agent in the fleet has been issued an
          ingest token via <code className="font-mono">/admin/agent-tokens</code> and that the
          agent helm chart has been redeployed with the token mounted.
        </p>
        <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
          A safer rollout: stay on <code className="font-mono">permissive</code> for 24h, watch
          for WARN logs ("agent rejected") in <code className="font-mono">kubectl logs</code>,
          fix any agent that's still anonymous, and only then promote to{' '}
          <code className="font-mono">enforced</code>.
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
          <KeyRound className="w-3.5 h-3.5" />
          Save and stage enforced
        </button>
      </div>
    </Modal>
  )
}
