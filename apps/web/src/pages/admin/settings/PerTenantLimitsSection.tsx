// PerTenantLimitsSection is the per-tenant ingest-overrides surface,
// migrated from the now-removed /admin/ingest-limits page into the
// Settings → Ingest tab. Spec #09 V1.5 — fleet defaults and per-tenant
// overrides belong on the same page because operators reason about
// them together ("does this tenant inherit, or does it have its own
// cap?"), and they share the same enforcement substrate (rate limiter +
// cardinality tracker).
//
// Behavior unchanged from TenantLimitsPage:
//   - Operates on the auto-seeded "default" tenant only. Per-tenant
//     selector lands with the Enterprise multi-tenant management UI;
//     the backend already accepts any tenant id.
//   - Edits the three Prom remote_write knobs: writeSamplesPerSec,
//     writeBurstSamples, maxActiveSeries.
//   - "Save" sends only dirty fields (merge semantics).
//   - "Reset to defaults" clears ALL custom overrides via DELETE; the
//     single-field clear verb is a server-side gap acknowledged in the
//     original page's copy.

import { useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, CheckCircle2, RotateCcw, Save } from 'lucide-react'
import { api, type EffectiveLimits, type LimitsResponse, type TenantLimits } from '@/services/api'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'

const DEFAULT_TENANT_NAME = 'default'

type LimitField = {
  key: keyof EffectiveLimits
  label: string
  unit: string
  help: string
}

const FIELDS: LimitField[] = [
  {
    key: 'writeSamplesPerSec',
    label: 'Write rate',
    unit: 'samples/sec',
    help: 'Sustained samples-per-second the tenant may ingest. Exceeding this rate returns 429 + Retry-After.',
  },
  {
    key: 'writeBurstSamples',
    label: 'Burst',
    unit: 'samples',
    help: 'Maximum tokens in the bucket. Typically ≥ Write rate so brief spikes pass without rate-limiting.',
  },
  {
    key: 'maxActiveSeries',
    label: 'Max active series',
    unit: 'series',
    help: 'Upper bound on distinct active series in VictoriaMetrics. New series past this cap return 413.',
  },
]

function fmtInt(n: number): string {
  return new Intl.NumberFormat('en-US').format(n)
}

function SourceBadge({ source }: { source: 'default' | 'custom' }) {
  if (source === 'custom') {
    return (
      <span className="px-2 py-0.5 rounded-full text-[10px] font-mono font-medium uppercase tracking-wider bg-status-info-dim text-status-info">
        Custom
      </span>
    )
  }
  return (
    <span className="px-2 py-0.5 rounded-full text-[10px] font-mono font-medium uppercase tracking-wider bg-kb-elevated text-kb-text-tertiary">
      Default
    </span>
  )
}

function sourceOf(limits: LimitsResponse, key: keyof EffectiveLimits): 'default' | 'custom' {
  const c = limits.custom
  if (!c) return 'default'
  return c[key] !== undefined ? 'custom' : 'default'
}

export function PerTenantLimitsSection() {
  const queryClient = useQueryClient()

  const { data: tenants, isLoading: tenantsLoading, error: tenantsError } = useQuery({
    queryKey: ['admin-tenants'],
    queryFn: api.listTenants,
  })
  const defaultTenant = tenants?.find((t) => t.name === DEFAULT_TENANT_NAME)

  const { data: limits, isLoading: limitsLoading, error: limitsError } = useQuery({
    queryKey: ['admin-tenant-limits', defaultTenant?.id],
    queryFn: () => api.getTenantLimits(defaultTenant!.id),
    enabled: !!defaultTenant,
  })

  const [form, setForm] = useState<Record<keyof EffectiveLimits, number>>({
    writeSamplesPerSec: 0,
    writeBurstSamples: 0,
    maxActiveSeries: 0,
  })
  useEffect(() => {
    if (limits) {
      setForm({
        writeSamplesPerSec: limits.effective.writeSamplesPerSec,
        writeBurstSamples: limits.effective.writeBurstSamples,
        maxActiveSeries: limits.effective.maxActiveSeries,
      })
    }
  }, [limits])

  const [saving, setSaving] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [warnings, setWarnings] = useState<string[]>([])
  const [savedAt, setSavedAt] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)

  const dirty = useMemo(() => {
    if (!limits) return {} as Record<keyof EffectiveLimits, boolean>
    return {
      writeSamplesPerSec: form.writeSamplesPerSec !== limits.effective.writeSamplesPerSec,
      writeBurstSamples: form.writeBurstSamples !== limits.effective.writeBurstSamples,
      maxActiveSeries: form.maxActiveSeries !== limits.effective.maxActiveSeries,
    }
  }, [form, limits])

  const anyDirty = dirty.writeSamplesPerSec || dirty.writeBurstSamples || dirty.maxActiveSeries
  const hasCustom = !!limits?.custom

  async function handleSave() {
    if (!defaultTenant || !anyDirty) return
    setError(null)
    setWarnings([])
    setSavedAt(null)
    setSaving(true)
    try {
      const patch: TenantLimits = {}
      if (dirty.writeSamplesPerSec) patch.writeSamplesPerSec = form.writeSamplesPerSec
      if (dirty.writeBurstSamples) patch.writeBurstSamples = form.writeBurstSamples
      if (dirty.maxActiveSeries) patch.maxActiveSeries = form.maxActiveSeries
      const { warnings: w } = await api.setTenantLimits(defaultTenant.id, patch)
      setWarnings(w)
      setSavedAt(Date.now())
      queryClient.invalidateQueries({ queryKey: ['admin-tenant-limits', defaultTenant.id] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to save limits')
    } finally {
      setSaving(false)
    }
  }

  async function handleReset() {
    if (!defaultTenant || !hasCustom) return
    setError(null)
    setWarnings([])
    setSavedAt(null)
    setResetting(true)
    try {
      await api.resetTenantLimits(defaultTenant.id)
      queryClient.invalidateQueries({ queryKey: ['admin-tenant-limits', defaultTenant.id] })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to reset limits')
    } finally {
      setResetting(false)
    }
  }

  // Renderer falls back to a compact loader / error inside the section
  // — the parent (IngestSettingsTab) already has the page chrome and
  // the fleet-defaults card above, so we don't want a full-page state.
  if (tenantsLoading || limitsLoading) {
    return (
      <section className="bg-kb-card border border-kb-border rounded-xl p-5">
        <div className="flex items-center justify-center py-10">
          <LoadingSpinner size="md" />
        </div>
      </section>
    )
  }
  if (tenantsError) {
    return (
      <section className="bg-kb-card border border-kb-border rounded-xl p-5">
        <ErrorState message={tenantsError instanceof Error ? tenantsError.message : 'Failed to load tenants'} />
      </section>
    )
  }
  if (!defaultTenant) {
    return (
      <section className="bg-kb-card border border-kb-border rounded-xl p-5">
        <ErrorState message="Default tenant not found. The backend should auto-seed it on first boot." />
      </section>
    )
  }
  if (limitsError || !limits) {
    return (
      <section className="bg-kb-card border border-kb-border rounded-xl p-5">
        <ErrorState message={limitsError instanceof Error ? limitsError.message : 'Failed to load limits'} />
      </section>
    )
  }

  return (
    <section className="bg-kb-card border border-kb-border rounded-xl overflow-hidden">
      <header className="px-5 py-4 border-b border-kb-border">
        <h2 className="text-sm font-semibold text-kb-text-primary">Per-tenant overrides</h2>
        <p className="text-[11px] text-kb-text-tertiary mt-0.5 leading-snug">
          Caps that win over the fleet defaults above for a specific tenant. Per-tenant overrides apply
          to the default tenant (the only one surfaced in OSS); clearing them returns each field to the
          fleet default.
        </p>
        <p className="text-[10px] text-kb-text-tertiary mt-2 font-mono">tenant: {defaultTenant.id}</p>
      </header>

      <div className="divide-y divide-kb-border">
        {FIELDS.map((field) => {
          const source = sourceOf(limits, field.key)
          const defaultValue = limits.defaults[field.key]
          const isDirty = dirty[field.key]
          return (
            <div key={field.key} className="px-5 py-4 flex items-start gap-6">
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 mb-1">
                  <label className="text-sm font-medium text-kb-text-primary" htmlFor={`limit-${field.key}`}>
                    {field.label}
                  </label>
                  <SourceBadge source={source} />
                  {isDirty && (
                    <span className="text-[10px] font-mono font-medium uppercase tracking-wider text-status-warn">
                      Unsaved
                    </span>
                  )}
                </div>
                <p className="text-xs text-kb-text-tertiary max-w-xl">{field.help}</p>
                <p className="text-[11px] text-kb-text-tertiary mt-1 font-mono">
                  Fleet default: {fmtInt(defaultValue)} {field.unit}
                </p>
              </div>
              <div className="flex items-center gap-2 shrink-0">
                <input
                  id={`limit-${field.key}`}
                  type="number"
                  min={0}
                  step={1}
                  value={form[field.key]}
                  onChange={(e) => {
                    const n = Number.parseInt(e.target.value, 10)
                    setForm((f) => ({ ...f, [field.key]: Number.isFinite(n) && n >= 0 ? n : 0 }))
                  }}
                  className="w-36 px-3 py-1.5 text-sm font-mono text-right bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary focus:outline-none focus:border-kb-accent transition-colors"
                />
                <span className="text-xs text-kb-text-tertiary w-24">{field.unit}</span>
              </div>
            </div>
          )
        })}
      </div>

      <div className="px-5 py-4 border-t border-kb-border flex items-center justify-between gap-4 flex-wrap">
        <div className="text-[11px] text-kb-text-tertiary max-w-md">
          To drop a single override, click <strong className="text-kb-text-secondary">Reset to defaults</strong> and
          re-apply only the values you want to customize.
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={handleReset}
            disabled={!hasCustom || resetting || saving}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
            title={hasCustom ? 'Clear every per-tenant override' : 'Nothing to reset — all fields use fleet defaults'}
          >
            <RotateCcw className="w-3.5 h-3.5" />
            {resetting ? 'Resetting…' : 'Reset to defaults'}
          </button>
          <button
            type="button"
            onClick={handleSave}
            disabled={!anyDirty || saving || resetting}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
          >
            <Save className="w-3.5 h-3.5" />
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>

      {error && (
        <div className="mx-5 mb-4 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
          <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
          <div>{error}</div>
        </div>
      )}

      {savedAt !== null && !error && (
        <div className="mx-5 mb-4 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-ok-dim text-status-ok text-xs">
          <CheckCircle2 className="w-4 h-4 mt-0.5 shrink-0" />
          <div>Limits saved. Rate &amp; cardinality enforcement picks up the new values on the next request.</div>
        </div>
      )}

      {warnings.length > 0 && (
        <div className="mx-5 mb-4 flex items-start gap-2 px-3 py-2 rounded-lg bg-status-warn-dim text-status-warn text-xs">
          <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
          <div className="space-y-0.5">
            {warnings.map((w, i) => (
              <div key={i}>{w}</div>
            ))}
          </div>
        </div>
      )}
    </section>
  )
}
