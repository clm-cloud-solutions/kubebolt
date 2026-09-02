import { Fragment, useEffect, useState, type WheelEvent as ReactWheelEvent } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { BellOff, History, Lightbulb, Loader2, Lock, Save, SlidersHorizontal, X } from 'lucide-react'
import { AdminHub, type AdminHubTab } from './AdminHub'
import { UnsavedChip } from './settings/SettingsField'
import { api, type InsightPolicyRule } from '@/services/api'
import { eeInsightEnvironments } from '@/ee/registry'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { EmptyState } from '@/components/shared/EmptyState'
import { HoverTooltip, TooltipHeader, TooltipNote, TooltipRow } from '@/components/shared/Tooltip'
import { EpisodeHistory } from '@/components/insights/EpisodeHistory'

// Admin / Insights — the #44 matrix hub: [Rules · Environment tuning ·
// Silenced · History]. The class contract drives every editor: a malfunction
// moves only its numeric bar (broken is broken — severity locked, no off);
// an expectation moves only its severity, `off` included.
//
// Editing follows the house configuration contract (in-vivo 01-sep): edits
// accumulate as DRAFTS with the amber «Unsaved» chip, and land only through
// Save changes (Cancel discards) — never silently on change. Saving a layer
// MERGES the drafted knob with the layer's other stored knob, because the
// upsert REPLACES the row (a severity edit must not wipe a threshold).

const CATEGORIES = ['production', 'staging', 'testing', 'development'] as const
const SEVERITIES = ['critical', 'warning', 'info', 'off'] as const

const SEV_CLS: Record<string, string> = {
  critical: 'text-status-error',
  warning: 'text-status-warn',
  info: 'text-status-info',
  off: 'text-kb-text-tertiary',
}

// One drafted layer: null = clear this knob's override on save.
type LayerDraft = { threshold?: number | null; severity?: string | null }

// normalizeDraft — a knob edited BACK to the value on screen is not a change
// (in-vivo 01-sep: «unsaved» must vanish when you undo by hand). The
// comparison baseline is the EFFECTIVE value (what the field showed before
// the edit — override or inherited), NOT this layer's stored row: the first
// cut compared against the stored layer, so returning an inherited value to
// its inherited state still read as dirty. A cleared knob (null) is dirty
// only when this layer actually stores something to clear. Returns undefined
// when nothing differs.
function normalizeDraft(
  effective: { threshold?: number; severity?: string },
  stored: { threshold?: number; severity?: string },
  next: LayerDraft,
): LayerDraft | undefined {
  const out: LayerDraft = {}
  if (next.threshold !== undefined) {
    const dirty =
      next.threshold === null ? stored.threshold !== undefined : next.threshold !== effective.threshold
    if (dirty) out.threshold = next.threshold
  }
  if (next.severity !== undefined) {
    const dirty =
      next.severity === null ? stored.severity !== undefined : next.severity !== effective.severity
    if (dirty) out.severity = next.severity
  }
  return out.threshold !== undefined || out.severity !== undefined ? out : undefined
}

function usePolicies() {
  return useQuery({
    queryKey: ['insight-policies'],
    queryFn: api.getInsightPolicies,
    retry: false,
  })
}

// saveLayer — one rule × one layer (global or a category). Merge-on-save:
// the PUT carries draft-or-stored for BOTH knobs; an all-cleared layer is a
// DELETE (falls back to the layer below).
async function saveLayer(
  rule: InsightPolicyRule,
  category: string | undefined, // undefined = global
  draft: LayerDraft,
) {
  const stored = category
    ? rule.categories?.[category]
    : { threshold: rule.threshold, severity: rule.severity }
  const threshold = draft.threshold === null ? undefined : (draft.threshold ?? stored?.threshold)
  const severity = draft.severity === null ? undefined : (draft.severity ?? stored?.severity)
  if (threshold === undefined && severity === undefined) {
    if (stored?.threshold !== undefined || stored?.severity !== undefined) {
      await api.deleteInsightPolicy(rule.id, category)
    }
    return
  }
  await api.putInsightPolicy(rule.id, { threshold, severity, category })
}

// ── shared cells ────────────────────────────────────────────────────────────

// RuleCell — id + the one-sentence definition under it, subtle (in-vivo
// 01-sep: an id alone assumes the reader knows all 24 rules). `locked` adds
// the Lock glyph with the house tooltip — no emoji.
function RuleCell({ rule, locked }: { rule: InsightPolicyRule; locked?: boolean }) {
  return (
    <div className="min-w-[220px] max-w-[340px]">
      <div className="flex items-center gap-1.5">
        <span className="font-mono text-[12.5px] text-kb-text-primary" title={rule.name || undefined}>
          {rule.id}
        </span>
        {locked && (
          <HoverTooltip
            minWidth={230}
            body={
              <>
                <TooltipHeader>Severity locked</TooltipHeader>
                <TooltipNote>
                  A malfunction is broken in any environment — its severity never moves and it can
                  never be switched off by policy. The only full silence for one resource is a mute.
                </TooltipNote>
              </>
            }
          >
            <Lock className="w-3 h-3 text-kb-text-tertiary" />
          </HoverTooltip>
        )}
      </div>
      {rule.description && (
        <div className="text-[11px] text-kb-text-tertiary leading-snug mt-0.5">{rule.description}</div>
      )}
    </div>
  )
}

// IgnoredCell — «100% · 45», digits aligned via fixed sub-columns +
// tabular-nums, in the house tooltip (in-vivo 01-sep: the meter bar read as
// decoration and free-width numbers zig-zagged down the column).
function IgnoredCell({ rule }: { rule: InsightPolicyRule }) {
  const st = rule.ignored30d
  if (!st || st.total === 0)
    return (
      <span className="inline-block min-w-[92px] text-right text-kb-text-tertiary font-mono text-[11.5px]">—</span>
    )
  const pct = Math.round((st.ignored / st.total) * 100)
  const pctCls = pct >= 80 ? 'text-status-error' : pct >= 50 ? 'text-status-warn' : 'text-status-ok'
  return (
    <HoverTooltip
      minWidth={250}
      body={
        <>
          <TooltipHeader>Ignored in 30 days</TooltipHeader>
          <TooltipRow label="Closed without action" value={String(st.ignored)} />
          <TooltipRow label="Total closed" value={String(st.total)} />
          <TooltipNote>
            Resolved or expired with no acknowledgement, no action and no mute. A high rate here
            usually means the GLOBAL knob is wrong — not any single environment.
          </TooltipNote>
        </>
      }
    >
      <span className="inline-flex items-baseline justify-end font-mono text-[11.5px] tabular-nums cursor-default">
        <span className={`min-w-[44px] text-right font-semibold ${pctCls}`}>{pct}%</span>
        <span className="text-kb-text-tertiary px-1">·</span>
        <span className="min-w-[36px] text-right text-kb-text-secondary">{st.total}</span>
      </span>
    </HoverTooltip>
  )
}

// House field tones (mirrors the Settings tabs): editable is bg-kb-bg with
// an accent focus; a drafted (unsaved) field gets the amber border.
const fieldBase =
  'rounded-md bg-kb-bg border text-[12px] font-mono text-kb-text-primary focus:outline-none focus:border-kb-accent transition-colors'
const fieldBorder = (dirty: boolean) => (dirty ? 'border-status-warn' : 'border-kb-border')

// Number inputs spin on wheel events while focused, and a Magic Mouse's
// inertial scroll fires dozens of them from a grazing touch (in-vivo
// 01-sep: 5 → 111 without a click). Dropping focus on wheel lets the
// scroll pass through; the click steppers and typing are untouched.
const noWheelSpin = (e: ReactWheelEvent<HTMLInputElement>) => e.currentTarget.blur()

// ── Save bar (shared by the Rules tab and the editor pop) ───────────────────

function SaveBar({
  dirty,
  saving,
  onSave,
  onCancel,
}: {
  dirty: number
  saving: boolean
  onSave: () => void
  onCancel: () => void
}) {
  if (dirty === 0 && !saving) return null
  return (
    <div className="flex items-center justify-end gap-2 pt-3">
      <span className="text-[10px] font-mono font-medium uppercase tracking-wider text-status-warn mr-auto">
        {dirty} unsaved change{dirty === 1 ? '' : 's'}
      </span>
      {!saving && (
        <button
          type="button"
          onClick={onCancel}
          className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-kb-text-secondary border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors"
        >
          <X className="w-3.5 h-3.5" />
          Cancel
        </button>
      )}
      <button
        type="button"
        disabled={saving || dirty === 0}
        onClick={onSave}
        className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white bg-kb-accent rounded-lg hover:bg-kb-accent/90 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        {saving ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Save className="w-3.5 h-3.5" />}
        {saving ? 'Saving…' : 'Save changes'}
      </button>
    </div>
  )
}

// ── Rules tab (global layer) ────────────────────────────────────────────────

function RulesTab() {
  const { data, isLoading, error } = usePolicies()
  const queryClient = useQueryClient()
  const [drafts, setDrafts] = useState<Record<string, LayerDraft>>({})

  const setDraft = (id: string, patch: LayerDraft) =>
    setDrafts((d) => {
      const rule = data?.rules.find((r) => r.id === id)
      const merged = { ...(d[id] ?? {}), ...patch }
      const norm = rule
        ? normalizeDraft(
            {
              threshold: rule.threshold ?? rule.defaultThreshold,
              severity: rule.severity ?? rule.defaultSeverity,
            },
            { threshold: rule.threshold, severity: rule.severity },
            merged,
          )
        : merged
      if (norm === undefined) {
        const { [id]: _gone, ...rest } = d
        return rest
      }
      return { ...d, [id]: norm }
    })

  const save = useMutation({
    mutationFn: async () => {
      for (const [id, draft] of Object.entries(drafts)) {
        const rule = data?.rules.find((r) => r.id === id)
        if (rule) await saveLayer(rule, undefined, draft)
      }
    },
    onSuccess: () => {
      setDrafts({})
      void queryClient.invalidateQueries({ queryKey: ['insight-policies'] })
    },
  })

  if (isLoading) return <LoadingSpinner />
  if (error || !data)
    return (
      <EmptyState
        icon={<SlidersHorizontal className="w-10 h-10" />}
        title="Rule policies unavailable"
        message="This surface needs the KubeBolt database (Enterprise/Cloud)."
      />
    )

  const dirtyCount = Object.keys(drafts).length
  const groups: { title: string; note: string; rules: InsightPolicyRule[] }[] = [
    {
      title: 'Malfunctions',
      note: 'broken is broken, anywhere — severity locked, only the numeric bar moves (never off)',
      rules: data.rules.filter((r) => r.class === 'malfunction'),
    },
    {
      title: 'Expectations',
      note: 'violated on purpose in some environments — severity is the dial, off included',
      rules: data.rules.filter((r) => r.class === 'expectation'),
    },
  ]

  return (
    <div className="space-y-6">
      {groups.map((g) => (
        <div key={g.title}>
          <div className="flex items-baseline gap-2 mb-2">
            <h3 className="text-sm font-semibold text-kb-text-primary">{g.title}</h3>
            <span className="text-[11px] text-kb-text-tertiary">{g.note}</span>
          </div>
          <div className="bg-kb-card border border-kb-border rounded-xl overflow-x-auto">
            <table className="w-full text-[12px]">
              <thead>
                {/* Percentage widths (in-vivo 01-sep): the page's slack
                    spreads across the VALUE columns — the rule column keeps
                    a fixed share, so text and values stay close. Identical
                    shares on both class tables keep them aligned. */}
                <tr className="text-[10px] font-mono uppercase tracking-wider text-kb-text-tertiary border-b border-kb-border">
                  <th className="text-left px-4 py-2 w-[40%]">Rule</th>
                  <th className="text-left px-3 py-2 w-[20%]">Severity</th>
                  <th className="text-left px-3 py-2 w-[20%]">Threshold</th>
                  <th className="text-right px-4 py-2 w-[20%]">Ignored 30d</th>
                </tr>
              </thead>
              <tbody>
                {g.rules.map((r) => {
                  const draft = drafts[r.id]
                  const dirty = draft !== undefined
                  const sevValue = draft?.severity === null ? '' : (draft?.severity ?? r.severity ?? '')
                  const thrValue =
                    draft?.threshold === null
                      ? ''
                      : String(draft?.threshold ?? r.threshold ?? r.defaultThreshold ?? '')
                  return (
                    <tr key={r.id} className="border-b border-kb-border last:border-0 align-top">
                      <td className="px-4 py-2.5">
                        <div className="flex items-start gap-2">
                          <RuleCell rule={r} locked={r.class === 'malfunction'} />
                          {dirty && <UnsavedChip />}
                        </div>
                      </td>
                      <td className="px-3 py-2.5">
                        {r.class === 'expectation' ? (
                          <select
                            value={sevValue}
                            onChange={(e) =>
                              setDraft(r.id, { severity: e.target.value === '' ? null : e.target.value })
                            }
                            className={`${fieldBase} ${fieldBorder(draft?.severity !== undefined)} px-2 py-1 ${SEV_CLS[sevValue] ?? 'text-kb-text-tertiary'}`}
                          >
                            <option value="">↳ default ({r.defaultSeverity})</option>
                            {SEVERITIES.map((s) => (
                              <option key={s} value={s}>
                                {s}
                              </option>
                            ))}
                          </select>
                        ) : (
                          <span className={`font-mono ${SEV_CLS[r.defaultSeverity] ?? ''}`}>
                            {r.defaultSeverity}
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2.5">
                        {r.hasThreshold ? (
                          <input
                            type="number"
                            min={sliderSpec(r).min}
                            step={sliderSpec(r).step}
                            onWheel={noWheelSpin}
                            value={thrValue}
                            placeholder={String(r.defaultThreshold ?? '')}
                            title={`${r.thresholdLabel ?? 'threshold'} — empty returns to the default`}
                            onChange={(e) => {
                              const raw = e.target.value
                              if (raw === '') setDraft(r.id, { threshold: null })
                              else {
                                const v = Number(raw)
                                if (!isNaN(v)) setDraft(r.id, { threshold: v })
                              }
                            }}
                            className={`${fieldBase} ${fieldBorder(draft?.threshold !== undefined)} w-24 px-2 py-1 text-right tabular-nums`}
                          />
                        ) : (
                          <span className="text-kb-text-tertiary">—</span>
                        )}
                      </td>
                      <td className="px-4 py-2.5 text-right">
                        <IgnoredCell rule={r} />
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      ))}
      {save.isError && (
        <p className="text-[12px] text-status-error">
          {(save.error as Error)?.message || 'Failed to save rule policies.'}
        </p>
      )}
      <SaveBar
        dirty={dirtyCount}
        saving={save.isPending}
        onSave={() => save.mutate()}
        onCancel={() => setDrafts({})}
      />
    </div>
  )
}

// ── Environment tuning tab — the matrix (Pantalla 1 of the mockup) ──────────

function effectiveFor(r: InsightPolicyRule, cat: string) {
  const ov = r.categories?.[cat]
  return {
    severity: ov?.severity ?? r.severity ?? r.defaultSeverity,
    threshold: ov?.threshold ?? r.threshold ?? r.defaultThreshold,
    // An OWN layer on this category (any knob) — what the accent ring marks.
    overridden: ov !== undefined && (ov.threshold !== undefined || ov.severity !== undefined),
  }
}

function fmtThreshold(r: InsightPolicyRule, v?: number): string {
  if (v === undefined) return '—'
  if (r.thresholdLabel?.includes('days')) return `${v} d`
  if (r.thresholdLabel?.includes('ratio')) return `${Math.round(v * 100)}%`
  return `> ${v}`
}

// ValuePill — the mockup's `.v`. The accent RING (not the fill) marks «this
// cell is overridden from the shipped default» — homogeneous across numeric
// and severity pills, «off» included (in-vivo 01-sep: «green means changed»
// only held for numbers).
function ValuePill({ text, kind, ringed }: { text: string; kind: string; ringed?: boolean }) {
  const base =
    'inline-block min-w-[52px] text-center px-2 py-0.5 rounded-[5px] text-[12px] font-mono tabular-nums'
  const cls =
    kind === 'critical' ? 'bg-status-error-dim text-status-error font-semibold'
    : kind === 'warning' ? 'bg-status-warn-dim text-status-warn'
    : kind === 'info' ? 'bg-status-info-dim text-status-info'
    : kind === 'off' ? 'bg-kb-elevated text-kb-text-tertiary'
    : 'bg-kb-elevated text-kb-text-primary' // numeric
  // «off» never gets the ring (in-vivo 01-sep): off is BY NATURE an
  // override — no shipped default is off — so ringing it added noise.
  const ring = ringed && kind !== 'off' ? 'shadow-[inset_0_0_0_1px_var(--kb-accent)]' : ''
  return <span className={`${base} ${cls} ${ring}`}>{text}</span>
}

// sliderSpec — a draggable range per threshold unit (in-vivo 01-sep: a
// decorative track that ignores the drag is worse than no track).
function sliderSpec(r: InsightPolicyRule): { min: number; max: number; step: number } {
  const def = r.defaultThreshold ?? 1
  if (r.thresholdLabel?.includes('ratio')) return { min: 0.05, max: 1, step: 0.05 }
  if (r.thresholdLabel?.includes('days')) return { min: 1, max: 60, step: 1 }
  return { min: 1, max: Math.max(25, def * 5), step: 1 }
}

// PolicyEditorPop — Pantallas 2/3: one row per environment. Edits are
// DRAFTS; Save changes commits (merge-on-save per layer), Cancel discards —
// nothing lands silently.
function PolicyEditorPop({ rule, onClose }: { rule: InsightPolicyRule; onClose: () => void }) {
  const queryClient = useQueryClient()
  const [drafts, setDrafts] = useState<Record<string, LayerDraft>>({})
  const spec = sliderSpec(rule)

  const setDraft = (cat: string, patch: LayerDraft) =>
    setDrafts((d) => {
      const eff = effectiveFor(rule, cat)
      const norm = normalizeDraft(
        { threshold: eff.threshold, severity: eff.severity },
        rule.categories?.[cat] ?? {},
        { ...(d[cat] ?? {}), ...patch },
      )
      if (norm === undefined) {
        const { [cat]: _gone, ...rest } = d
        return rest
      }
      return { ...d, [cat]: norm }
    })

  const save = useMutation({
    mutationFn: async () => {
      for (const [cat, draft] of Object.entries(drafts)) {
        await saveLayer(rule, cat, draft)
      }
    },
    onSuccess: () => {
      setDrafts({})
      void queryClient.invalidateQueries({ queryKey: ['insight-policies'] })
    },
  })

  const dirtyCount = Object.keys(drafts).length
  const boolMalfunction = rule.class === 'malfunction' && !rule.hasThreshold

  // Drafted view of one category's knobs.
  const view = (cat: string) => {
    const eff = effectiveFor(rule, cat)
    const d = drafts[cat]
    return {
      severity:
        d?.severity === null ? (rule.severity ?? rule.defaultSeverity) : (d?.severity ?? eff.severity),
      threshold:
        d?.threshold === null ? (rule.threshold ?? rule.defaultThreshold) : (d?.threshold ?? eff.threshold),
      dirty: d !== undefined,
    }
  }

  return (
    <div className="fixed inset-0 z-[99999] flex items-center justify-center" onClick={onClose}>
      {/* House modal backdrop: strong dim + blur so the dense matrix behind
          stops competing with the editor. */}
      <div className="absolute inset-0 bg-black/70 backdrop-blur-sm" aria-hidden />
      <div
        onClick={(e) => e.stopPropagation()}
        className="relative w-[min(540px,92vw)] bg-kb-card border border-kb-border-active rounded-[10px] shadow-2xl px-[18px] py-4"
      >
        <div className="flex items-center gap-2">
          <h4 className="text-[15px] font-bold text-kb-text-primary">{rule.name || rule.id}</h4>
          {dirtyCount > 0 && <UnsavedChip />}
        </div>
        <p className="text-[12.5px] font-mono text-kb-text-tertiary mt-0.5">
          {rule.id}
          {rule.class === 'malfunction' ? (
            <>
              {' · severity '}
              <ValuePill text={rule.defaultSeverity} kind={rule.defaultSeverity} /> in every
              environment{' '}
              <span className="inline-flex items-center gap-1 text-[11px]">
                <Lock className="w-3 h-3" /> fixed
              </span>
            </>
          ) : rule.hasThreshold ? (
            <> · severity dial + numeric bar per environment</>
          ) : (
            <> · no bar to move — boolean rule, severity is the dial</>
          )}
        </p>
        {rule.description && (
          <p className="text-[11.5px] text-kb-text-tertiary leading-snug mt-1 mb-2">{rule.description}</p>
        )}

        {CATEGORIES.map((cat) => {
          const v = view(cat)
          return (
            <div
              key={cat}
              className="grid grid-cols-[104px_1fr_auto] gap-3 items-center py-2 border-t border-kb-border"
            >
              <span className="text-[12px] font-mono text-kb-text-secondary inline-flex items-center gap-1.5">
                {cat}
                {v.dirty && <span className="w-1.5 h-1.5 rounded-full bg-status-warn" title="unsaved" />}
              </span>

              {boolMalfunction ? (
                <span>
                  <ValuePill
                    text={effectiveFor(rule, cat).severity}
                    kind={effectiveFor(rule, cat).severity}
                  />
                </span>
              ) : rule.class === 'expectation' ? (
                <span className="inline-flex border border-kb-border rounded-[7px] overflow-hidden w-fit">
                  {SEVERITIES.slice()
                    .reverse()
                    .map((s) => {
                      // The SELECTED segment wears its severity's own color —
                      // same palette as the matrix pills.
                      const onCls =
                        s === 'critical' ? 'bg-status-error-dim text-status-error'
                        : s === 'warning' ? 'bg-status-warn-dim text-status-warn'
                        : s === 'info' ? 'bg-status-info-dim text-status-info'
                        : 'bg-kb-elevated text-kb-text-primary'
                      return (
                        <button
                          key={s}
                          type="button"
                          onClick={() => setDraft(cat, { severity: s })}
                          className={`px-2.5 py-1 text-[11px] font-mono border-r border-kb-border last:border-r-0 transition-colors ${
                            v.severity === s
                              ? `${onCls} font-semibold`
                              : 'text-kb-text-tertiary hover:text-kb-text-primary'
                          }`}
                        >
                          {s}
                        </button>
                      )
                    })}
                </span>
              ) : (
                <input
                  type="range"
                  min={spec.min}
                  max={spec.max}
                  step={spec.step}
                  value={Number(v.threshold ?? spec.min)}
                  onChange={(e) => setDraft(cat, { threshold: Number(e.target.value) })}
                  style={{ accentColor: 'var(--kb-accent)' }}
                  className="w-full h-[5px] cursor-pointer"
                  title={`${rule.thresholdLabel ?? 'threshold'} — drag, or type in the field`}
                />
              )}

              {rule.hasThreshold ? (
                <input
                  type="number"
                  min={spec.min}
                  step={spec.step}
                  onWheel={noWheelSpin}
                  value={String(v.threshold ?? '')}
                  onChange={(e) => {
                    const raw = e.target.value
                    if (raw === '') setDraft(cat, { threshold: null })
                    else {
                      const n = Number(raw)
                      if (!isNaN(n)) setDraft(cat, { threshold: n })
                    }
                  }}
                  title={`${rule.thresholdLabel ?? 'threshold'} — empty resets this layer's bar`}
                  className={`${fieldBase} ${fieldBorder(drafts[cat]?.threshold !== undefined)} w-[84px] px-2 py-1 text-center tabular-nums`}
                />
              ) : rule.class === 'expectation' ? (
                <button
                  type="button"
                  onClick={() => setDraft(cat, { severity: null })}
                  title="Back to the layer below (global, else the shipped default)"
                  className="text-[10.5px] font-mono text-kb-text-tertiary hover:text-kb-text-primary"
                >
                  ↳ inherit
                </button>
              ) : (
                <span />
              )}
            </div>
          )
        })}

        {save.isError && (
          <p className="mt-2 text-[12px] text-status-error">
            {(save.error as Error)?.message || 'Failed to save.'}
          </p>
        )}

        <div className="mt-3 pt-3 border-t border-dashed border-kb-border">
          <p className="text-[12.5px] text-kb-text-tertiary">
            Saved changes apply to every cluster classified in that category on its next evaluation
            (≤30s).
            {rule.class === 'malfunction' && (
              <>
                {' '}
                There is no «off» for a malfunction — a full stop for one resource is a mute, and it
                should hurt a little to ask for it.
              </>
            )}
          </p>
          {!boolMalfunction && (
            <SaveBar
              dirty={dirtyCount}
              saving={save.isPending}
              onSave={() => save.mutate()}
              onCancel={() => setDrafts({})}
            />
          )}
        </div>
      </div>
    </div>
  )
}

function EnvironmentsTab() {
  const { data, isLoading, error } = usePolicies()
  const [editing, setEditing] = useState<InsightPolicyRule | null>(null)
  // «Adjust rule» seam: /admin/insights?tab=environments&rule=<id> lands with
  // that rule's editor already open. One-shot — closing clears the param.
  const [params, setParams] = useSearchParams()
  const wanted = params.get('rule')
  useEffect(() => {
    if (wanted && data && !editing) {
      const r = data.rules.find((x) => x.id === wanted)
      if (r) setEditing(r)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wanted, data])
  const closeEditor = () => {
    setEditing(null)
    if (wanted) {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          next.delete('rule')
          return next
        },
        { replace: true },
      )
    }
  }

  if (isLoading) return <LoadingSpinner />
  if (error || !data)
    return (
      <EmptyState
        icon={<SlidersHorizontal className="w-10 h-10" />}
        title="Rule policies unavailable"
        message="This surface needs the KubeBolt database (Enterprise/Cloud)."
      />
    )
  // Keep the pop's rule fresh across refetches.
  const editingRule = editing ? data.rules.find((r) => r.id === editing.id) ?? null : null

  const groups: { title: string; hint: string; rules: InsightPolicyRule[] }[] = [
    {
      title: 'Malfunctions',
      hint: '· severity fixed — only the bar moves',
      rules: data.rules.filter((r) => r.class === 'malfunction'),
    },
    {
      title: 'Expectations',
      hint: '· no bar to move — severity is the dial',
      rules: data.rules.filter((r) => r.class === 'expectation'),
    },
  ]

  return (
    <div>
      {/* envbar — the closed-set note is load-bearing copy (#14). */}
      <div className="flex items-center gap-2 flex-wrap mb-4">
        <span className="text-[12px] font-mono tracking-[0.05em] text-kb-text-tertiary mr-0.5">CATEGORIES</span>
        {CATEGORIES.map((c) => (
          <span
            key={c}
            className="px-[11px] py-1 rounded-full border border-kb-accent bg-kb-accent-light text-kb-accent text-[11.5px] font-mono font-semibold whitespace-nowrap"
          >
            {c}
          </span>
        ))}
        <span className="text-[11.5px] text-kb-text-tertiary ml-1">
          closed set — assigned in{' '}
          <a href="/clusters" className="text-kb-accent font-semibold hover:underline">
            Clusters
          </a>
          , because it prices the node-hour
        </span>
      </div>

      <div className="bg-kb-card border border-kb-border rounded-xl overflow-x-auto">
        <table className="w-full text-[13.5px] min-w-[760px] border-collapse">
          <thead>
            <tr>
              <th className="text-left px-3 py-2 text-[10.5px] font-mono font-semibold uppercase tracking-[0.07em] text-kb-text-tertiary border-b border-kb-border w-[32%]">
                Rule
              </th>
              {CATEGORIES.map((c) => (
                <th
                  key={c}
                  className="text-center px-3 py-2 text-[10.5px] font-mono font-semibold uppercase tracking-[0.07em] text-kb-text-tertiary border-b border-kb-border whitespace-nowrap"
                >
                  {c}
                </th>
              ))}
              <th className="text-right px-3 py-2 text-[10.5px] font-mono font-semibold uppercase tracking-[0.07em] text-kb-text-tertiary border-b border-kb-border whitespace-nowrap">
                Ignored 30d
              </th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <Fragment key={g.title}>
                <tr className="bg-kb-surface">
                  <td colSpan={6} className="px-3 py-1.5">
                    <span className="text-[10.5px] font-mono font-semibold uppercase tracking-[0.09em] text-kb-text-tertiary">
                      {g.title}
                    </span>
                    <span className="text-[11.5px] text-kb-text-tertiary ml-1.5">{g.hint}</span>
                  </td>
                </tr>
                {g.rules.map((r) => {
                  const boolMalfunction = r.class === 'malfunction' && !r.hasThreshold
                  return (
                    <tr
                      key={r.id}
                      onClick={() => setEditing(r)}
                      className="border-b border-kb-border last:border-0 cursor-pointer hover:bg-kb-card-hover align-top"
                      title={
                        boolMalfunction
                          ? 'Boolean malfunction — open to see why there is no dial'
                          : 'Edit per environment'
                      }
                    >
                      <td className="px-3 py-2.5">
                        <RuleCell rule={r} locked={r.class === 'malfunction'} />
                      </td>
                      {CATEGORIES.map((cat) => {
                        const eff = effectiveFor(r, cat)
                        if (eff.severity === 'off') {
                          return (
                            <td key={cat} className="px-3 py-2.5 text-center">
                              <ValuePill text="off" kind="off" ringed={eff.overridden} />
                            </td>
                          )
                        }
                        if (r.hasThreshold) {
                          return (
                            <td key={cat} className="px-3 py-2.5 text-center">
                              <ValuePill
                                text={fmtThreshold(r, eff.threshold)}
                                kind="num"
                                ringed={eff.overridden || eff.threshold !== r.defaultThreshold}
                              />
                            </td>
                          )
                        }
                        return (
                          <td key={cat} className="px-3 py-2.5 text-center">
                            <ValuePill text={eff.severity} kind={eff.severity} ringed={eff.overridden} />
                          </td>
                        )
                      })}
                      <td className="px-3 py-2.5 text-right">
                        <IgnoredCell rule={r} />
                      </td>
                    </tr>
                  )
                })}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>

      <p className="mt-3.5 pt-3 border-t border-dashed border-kb-border text-[12.5px] text-kb-text-tertiary">
        A{' '}
        <span className="shadow-[inset_0_0_0_1px_var(--kb-accent)] rounded-[5px] px-1.5 py-0.5 text-[11px] font-mono">
          green ring
        </span>{' '}
        marks a value overridden from the shipped default — numeric or severity alike. «off»
        carries no ring: no shipped default is off, so off is always an override. Click a row to
        tune it; nothing applies until you Save.
      </p>

      {editingRule && <PolicyEditorPop rule={editingRule} onClose={closeEditor} />}
    </div>
  )
}

// ── Silenced tab (A7: BOTH layers) ──────────────────────────────────────────

function SilencedTab() {
  const { data: policies } = usePolicies()
  const queryClient = useQueryClient()
  const { data: muteData } = useQuery({
    queryKey: ['insight-mutes', 'all'],
    queryFn: () => api.getInsightMutes('all'),
    retry: false,
  })
  const unmute = useMutation({
    mutationFn: (id: string) => api.deleteInsightMute(id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['insight-mutes'] }),
  })
  const resetPolicy = useMutation({
    mutationFn: (v: { rule: string; category?: string }) => api.deleteInsightPolicy(v.rule, v.category),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ['insight-policies'] }),
  })

  // Layer 1 — rules off by POLICY (what a whole environment stopped seeing).
  const offRows: { rule: string; layer: string }[] = []
  for (const r of policies?.rules ?? []) {
    if (r.severity === 'off') offRows.push({ rule: r.id, layer: 'global' })
    for (const [cat, ov] of Object.entries(r.categories ?? {})) {
      if (ov.severity === 'off') offRows.push({ rule: r.id, layer: cat })
    }
  }
  const mutes = muteData?.mutes ?? []

  return (
    <div className="space-y-6">
      <div>
        <div className="flex items-baseline gap-2 mb-2">
          <h3 className="text-sm font-semibold text-kb-text-primary">Off by policy</h3>
          <span className="text-[11px] text-kb-text-tertiary">
            the profile layer (#44) — whole environments, expectations only
          </span>
        </div>
        {offRows.length === 0 ? (
          <p className="text-[11px] text-kb-text-tertiary">No rule is switched off by policy.</p>
        ) : (
          <div className="bg-kb-card border border-kb-border rounded-xl">
            {offRows.map((row) => (
              <div
                key={row.rule + row.layer}
                className="flex items-center gap-3 px-4 py-2 border-b border-kb-border last:border-0 text-[12px]"
              >
                <span className="font-mono text-kb-text-primary">{row.rule}</span>
                <span className="px-1.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary text-[10px] font-mono">
                  {row.layer}
                </span>
                <span className="flex-1" />
                <button
                  type="button"
                  onClick={() =>
                    resetPolicy.mutate({
                      rule: row.rule,
                      category: row.layer === 'global' ? undefined : row.layer,
                    })
                  }
                  className="px-2 py-0.5 rounded border border-kb-border text-[10.5px] font-mono text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active transition-colors"
                >
                  Turn back on
                </button>
              </div>
            ))}
          </div>
        )}
      </div>

      <div>
        <div className="flex items-baseline gap-2 mb-2">
          <h3 className="text-sm font-semibold text-kb-text-primary">Muted resources</h3>
          <span className="text-[11px] text-kb-text-tertiary">
            the exception layer (#54) — one resource at a time, org-wide view
          </span>
        </div>
        {mutes.length === 0 ? (
          <p className="text-[11px] text-kb-text-tertiary">No active mutes.</p>
        ) : (
          <div className="bg-kb-card border border-kb-border rounded-xl">
            {mutes.map((m) => (
              <div
                key={m.id}
                className="flex items-center gap-3 px-4 py-2 border-b border-kb-border last:border-0 text-[12px]"
              >
                <span className="font-mono text-kb-text-primary">{m.ruleId}</span>
                <span className="font-mono text-kb-text-secondary truncate">{m.resource}</span>
                {/* Name, not UUID (in-vivo 01-sep) — resolved server-side,
                    dead clusters included; the UID stays in the tooltip. */}
                <span
                  className="px-1.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary text-[10px] font-mono shrink-0"
                  title={m.clusterId}
                >
                  {m.clusterName || `${m.clusterId.slice(0, 8)}…`}
                </span>
                <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
                  {m.untilResolved
                    ? 'until resolved'
                    : m.expiresAt
                      ? `until ${new Date(m.expiresAt).toLocaleDateString()}`
                      : 'permanent'}
                  {m.createdBy && ` · ${m.createdBy}`}
                </span>
                {m.reason && (
                  <span className="text-[10px] text-kb-text-tertiary truncate" title={m.reason}>
                    — {m.reason}
                  </span>
                )}
                <span className="flex-1" />
                <button
                  type="button"
                  disabled={unmute.isPending}
                  onClick={() => unmute.mutate(m.id)}
                  className="px-2 py-0.5 rounded border border-kb-border text-[10.5px] font-mono text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active transition-colors disabled:opacity-50"
                >
                  Unsilence
                </button>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

// ── the hub ─────────────────────────────────────────────────────────────────

export function InsightsHubPage() {
  const tabs: AdminHubTab[] = [
    {
      key: 'rules',
      label: 'Rules',
      Icon: Lightbulb,
      title: 'Rules',
      subtitle: 'The 24 shipped rules and the org-wide (global) overrides. Class decides the knob.',
      render: () => <RulesTab />,
    },
    // Environment tuning needs the cluster environment classification (an
    // Enterprise billing field); the edition says whether the tab exists.
    ...(eeInsightEnvironments
      ? [
          {
            key: 'environments',
            label: 'Environment tuning',
            Icon: SlidersHorizontal,
            title: 'Environment tuning',
            subtitle: 'Adjust the expectation, not the volume — what each rule means in each environment class.',
            render: () => <EnvironmentsTab />,
          } as AdminHubTab,
        ]
      : []),
    {
      key: 'silenced',
      label: 'Silenced',
      Icon: BellOff,
      title: 'Silenced',
      subtitle:
        'Both suppression layers, always counted: policy (whole environments) and mutes (single resources).',
      render: () => <SilencedTab />,
    },
    {
      key: 'history',
      label: 'History',
      Icon: History,
      title: 'Episode history',
      subtitle: 'The org-wide lifecycle record — dead clusters included.',
      render: () => <EpisodeHistory severity="" pageSize={25} defaultScope="all" />,
    },
  ]
  return <AdminHub tabs={tabs} />
}
