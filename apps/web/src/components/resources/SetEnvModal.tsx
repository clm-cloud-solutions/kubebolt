import { useEffect, useMemo, useState } from 'react'
import { Variable, Plus, X, AlertTriangle, Lock, Info, RefreshCw } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import type {
  ContainerEnvPatch,
  EnvVarPatch,
  EnvVarSourcePatch,
  EnvEntryKind,
} from '@/services/api'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { ResourceItem, ResourceList } from '@/types/kubernetes'
import { RolloutStatusPanel } from './RolloutStatusPanel'

// SetEnvModal — UI for the `kubectl set env` equivalent.
//
// Three rough use cases the modal handles:
//
//   - ADD a new env var (literal / ConfigMap ref / Secret ref).
//   - UPDATE an existing var (typically: change a value, or swap a
//     literal for a Secret reference because the value rotated).
//   - REMOVE an obsolete env var.
//
// Multi-container workloads: the operator picks one container at a
// time via the picker. Drafts are kept per-container in component
// state so switching the picker doesn't lose unsaved edits. On Apply,
// drafts from every container are sent in a single request — one
// strategic merge patch, atomic.
//
// SOURCE references (ConfigMap / Secret) come from the namespace's
// existing list — populated via in-place useResources hooks. Source
// NAME is a select; key is a free-text input (v1; key-existence
// validation is a v2 follow-up per the spec).

const SENSITIVE_NAME_RE = /(?:^|_)(?:password|passwd|pass|secret|token|key|credential|api(?:_|$))/i

interface ContainerSpec {
  name: string
  // Existing env entries the operator sees for context. The modal
  // renders these as read-only baseline rows alongside the drafts.
  env: BaselineEnv[]
}

interface BaselineEnv {
  name: string
  kind: EnvEntryKind
  value?: string
  valueFrom?: EnvVarSourcePatch
}

// EnvDraft is one operator-pending change. Combines all three v1
// variants (literal / ConfigMap / Secret) under one shape with a
// `type` discriminator so the row component can switch its inputs
// without juggling unions.
type EnvDraftType = 'literal' | 'configMap' | 'secret'

interface EnvDraft {
  // local React-row id (random uuid-ish) so we can reorder/remove
  // rows by stable identity instead of array index.
  rowId: string
  // operator's intent for this row
  action: 'set' | 'remove'
  name: string
  type: EnvDraftType
  value: string
  cmName: string
  cmKey: string
  secretName: string
  secretKey: string
}

function newRowId(): string {
  return Math.random().toString(36).slice(2, 10)
}

function emptyDraft(): EnvDraft {
  return {
    rowId: newRowId(),
    action: 'set',
    name: '',
    type: 'literal',
    value: '',
    cmName: '',
    cmKey: '',
    secretName: '',
    secretKey: '',
  }
}

// envRowFromBaseline converts a serialized baseline env entry into a
// pre-filled EnvDraft. Used when the operator clicks "edit" on an
// existing entry — the row pre-fills with the current value so the
// edit is a delta, not a re-type.
function draftFromBaseline(b: BaselineEnv): EnvDraft {
  const d = emptyDraft()
  d.name = b.name
  if (b.kind === 'configMap' && b.valueFrom?.configMapKeyRef) {
    d.type = 'configMap'
    d.cmName = b.valueFrom.configMapKeyRef.name
    d.cmKey = b.valueFrom.configMapKeyRef.key
  } else if (b.kind === 'secret' && b.valueFrom?.secretKeyRef) {
    d.type = 'secret'
    d.secretName = b.valueFrom.secretKeyRef.name
    d.secretKey = b.valueFrom.secretKeyRef.key
  } else {
    d.type = 'literal'
    d.value = b.value ?? ''
  }
  return d
}

function extractContainers(item: ResourceItem | undefined): ContainerSpec[] {
  if (!item) return []
  const cs = (item as unknown as { containers?: unknown }).containers
  if (!Array.isArray(cs)) return []
  return cs
    .map((c) => {
      if (typeof c !== 'object' || c === null) return null
      const obj = c as { name?: unknown; env?: unknown }
      if (typeof obj.name !== 'string') return null
      const env: BaselineEnv[] = []
      if (Array.isArray(obj.env)) {
        for (const e of obj.env) {
          if (typeof e !== 'object' || e === null) continue
          const ev = e as { name?: unknown; kind?: unknown; value?: unknown; valueFrom?: unknown }
          if (typeof ev.name !== 'string') continue
          env.push({
            name: ev.name,
            kind: (typeof ev.kind === 'string' ? ev.kind : 'literal') as EnvEntryKind,
            value: typeof ev.value === 'string' ? ev.value : undefined,
            valueFrom: ev.valueFrom as EnvVarSourcePatch | undefined,
          })
        }
      }
      return { name: obj.name, env }
    })
    .filter((c): c is ContainerSpec => c !== null)
}

function buildEnvVarPatch(d: EnvDraft): EnvVarPatch | { error: string } {
  if (d.action === 'remove') {
    return { name: d.name, action: 'remove' }
  }
  if (d.type === 'literal') {
    return { name: d.name, action: 'set', value: d.value }
  }
  if (d.type === 'configMap') {
    if (!d.cmName) return { error: `${d.name}: ConfigMap name is required` }
    if (!d.cmKey) return { error: `${d.name}: ConfigMap key is required` }
    return {
      name: d.name,
      action: 'set',
      valueFrom: { configMapKeyRef: { name: d.cmName, key: d.cmKey } },
    }
  }
  if (d.type === 'secret') {
    if (!d.secretName) return { error: `${d.name}: Secret name is required` }
    if (!d.secretKey) return { error: `${d.name}: Secret key is required` }
    return {
      name: d.name,
      action: 'set',
      valueFrom: { secretKeyRef: { name: d.secretName, key: d.secretKey } },
    }
  }
  return { error: `${d.name}: invalid type` }
}

const NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/

export function SetEnvModal({
  type,
  namespace,
  name,
  resource,
  onClose,
}: {
  type: 'deployments' | 'statefulsets' | 'daemonsets'
  namespace: string
  name: string
  resource: ResourceItem | undefined
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const containers = useMemo(() => extractContainers(resource), [resource])

  const [selectedContainer, setSelectedContainer] = useState<string>(() => containers[0]?.name ?? '')
  // drafts keyed by container name. A given container may have many
  // draft rows (one per planned change); the apply step coalesces.
  const [draftsByContainer, setDraftsByContainer] = useState<Record<string, EnvDraft[]>>({})
  const [triggerRollout, setTriggerRollout] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applying, setApplying] = useState<{ expectedGeneration: number; submittedAtMs: number } | null>(null)

  // Initialize selectedContainer once we have the list.
  useEffect(() => {
    if (!selectedContainer && containers.length > 0) {
      setSelectedContainer(containers[0].name)
    }
  }, [containers, selectedContainer])

  const selectedSpec = useMemo(
    () => containers.find((c) => c.name === selectedContainer),
    [containers, selectedContainer],
  )
  const drafts = draftsByContainer[selectedContainer] ?? []

  // Load CMs and Secrets in this namespace for the source-name dropdowns.
  // Capped at 200 entries — namespaces with more are rare and the
  // operator can re-type a known name as a fallback.
  const { data: cmList } = useQuery<ResourceList>({
    queryKey: ['resources', 'configmaps', { namespace, limit: 200 }],
    queryFn: () => api.getResources('configmaps', { namespace, limit: 200 }),
    staleTime: 30_000,
  })
  const { data: secretList } = useQuery<ResourceList>({
    queryKey: ['resources', 'secrets', { namespace, limit: 200 }],
    queryFn: () => api.getResources('secrets', { namespace, limit: 200 }),
    staleTime: 30_000,
  })
  const cmNames = useMemo(() => (cmList?.items ?? []).map((i) => i.name).sort(), [cmList])
  const secretNames = useMemo(() => (secretList?.items ?? []).map((i) => i.name).sort(), [secretList])

  function setDrafts(updater: (prev: EnvDraft[]) => EnvDraft[]) {
    setDraftsByContainer((m) => ({ ...m, [selectedContainer]: updater(m[selectedContainer] ?? []) }))
  }

  function addEmptyRow() {
    setDrafts((prev) => [...prev, emptyDraft()])
  }

  function addEditRow(b: BaselineEnv) {
    // Pre-filled draft for an existing baseline row — operator
    // wants to update its value or swap its source. We don't dedup
    // here; if the operator already has a draft for this name it'll
    // be the second one, and submit will reject with a clear error
    // (server-side dedup check).
    setDrafts((prev) => [...prev, draftFromBaseline(b)])
  }

  function addRemoveRow(b: BaselineEnv) {
    setDrafts((prev) => [
      ...prev,
      { ...emptyDraft(), name: b.name, action: 'remove' },
    ])
  }

  function updateDraft(rowId: string, patch: Partial<EnvDraft>) {
    setDrafts((prev) => prev.map((d) => (d.rowId === rowId ? { ...d, ...patch } : d)))
  }

  function removeDraft(rowId: string) {
    setDrafts((prev) => prev.filter((d) => d.rowId !== rowId))
  }

  // Drafts that are "marked for remove" by name → we strike the
  // matching baseline row to avoid a confusing dual display.
  const removedNames = useMemo(() => {
    const set = new Set<string>()
    for (const c of Object.keys(draftsByContainer)) {
      for (const d of draftsByContainer[c]) {
        if (d.action === 'remove' && d.name) set.add(`${c}::${d.name}`)
      }
    }
    return set
  }, [draftsByContainer])

  // SET drafts that target an existing baseline entry → we dim the
  // matching baseline row and show a "will be updated" indicator on
  // the draft row. K8s strategic merge updates by name, so this is
  // semantically correct (no duplicate entry is created), but the
  // operator deserves a clear visual signal that an "Add row" with
  // an existing name is going to OVERWRITE that entry rather than
  // create an additional one.
  const updatingNames = useMemo(() => {
    const set = new Set<string>()
    for (const c of Object.keys(draftsByContainer)) {
      for (const d of draftsByContainer[c]) {
        if (d.action === 'set' && d.name) set.add(`${c}::${d.name}`)
      }
    }
    return set
  }, [draftsByContainer])

  // baselineNamesByContainer is the per-container set of existing env
  // names — used by DraftRow to flag set-drafts whose name collides
  // with the baseline. Computed once instead of inside every row.
  const baselineNamesByContainer = useMemo(() => {
    const m: Record<string, Set<string>> = {}
    for (const c of containers) {
      m[c.name] = new Set(c.env.map((e) => e.name))
    }
    return m
  }, [containers])

  // total draft count across all containers — drives the Apply gate
  // and the toolbar count badge.
  const totalDrafts = useMemo(() => {
    let n = 0
    for (const c of Object.keys(draftsByContainer)) n += draftsByContainer[c].length
    return n
  }, [draftsByContainer])

  const dirty = totalDrafts > 0

  const validationErrors = useMemo(() => {
    const errs: string[] = []
    for (const cName of Object.keys(draftsByContainer)) {
      const list = draftsByContainer[cName]
      const seenNames = new Set<string>()
      for (const d of list) {
        if (!d.name) {
          errs.push(`${cName}: a row is missing a name`)
          continue
        }
        if (!NAME_RE.test(d.name)) {
          errs.push(`${cName}: ${d.name} is not a valid env name (must match C_IDENTIFIER)`)
        }
        if (seenNames.has(d.name)) {
          errs.push(`${cName}: ${d.name} is targeted by more than one row`)
        }
        seenNames.add(d.name)
        const p = buildEnvVarPatch(d)
        if ('error' in p) errs.push(`${cName}: ${p.error}`)
      }
    }
    return errs
  }, [draftsByContainer])

  async function submit() {
    setBusy(true)
    setError(null)
    const preGen = ((resource as unknown as { generation?: number })?.generation ?? 0) + 1
    const submittedAtMs = Date.now()
    try {
      const containersBody: ContainerEnvPatch[] = []
      for (const cName of Object.keys(draftsByContainer)) {
        const list = draftsByContainer[cName]
        if (list.length === 0) continue
        const env: EnvVarPatch[] = []
        for (const d of list) {
          const p = buildEnvVarPatch(d)
          if ('error' in p) {
            setError(p.error)
            setBusy(false)
            return
          }
          env.push(p)
        }
        containersBody.push({ container: cName, env })
      }
      if (containersBody.length === 0) {
        setError('No changes to apply')
        return
      }
      const res = await api.setEnvResource(type, namespace, name, {
        containers: containersBody,
        triggerRollout,
      })
      if (res.resource) {
        queryClient.setQueryData(['resource-detail', type, namespace, name], res.resource)
      }
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      // Live progress only when triggerRollout is on — without it,
      // the env change applies on the next pod restart but no
      // rollout is happening now, so RolloutStatusPanel would just
      // sit at "converged" immediately.
      if (triggerRollout) {
        setApplying({ expectedGeneration: preGen, submittedAtMs })
      } else {
        // close after a short pause so the operator sees the success
        // toast — keeping the UI consistent with the no-rollout path.
        setTimeout(onClose, 400)
      }
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message
      setError(msg)
    } finally {
      setBusy(false)
    }
  }

  if (applying) {
    return (
      <Modal
        badge={
          <span className="flex items-center gap-1 px-1 -mx-1 rounded bg-status-info text-kb-bg font-semibold">
            <Variable className="w-3 h-3" /> applying
          </span>
        }
        title={`Set env · ${name}`}
        onClose={onClose}
        size="lg"
      >
        <div className="flex-1 overflow-y-auto px-5 py-4">
          <RolloutStatusPanel
            type={type}
            namespace={namespace}
            name={name}
            title="Applying env changes…"
            expectedGeneration={applying.expectedGeneration}
            submittedAtMs={applying.submittedAtMs}
          />
        </div>
        <div className="px-5 py-3 border-t border-kb-border flex justify-end shrink-0">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated"
          >
            Close
          </button>
        </div>
      </Modal>
    )
  }

  return (
    <Modal
      badge={
        <span className="flex items-center gap-1 text-status-info">
          <Variable className="w-3 h-3" /> set env
        </span>
      }
      title={`Set env · ${name}`}
      onClose={onClose}
      size="lg"
    >
      <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
        <p className="text-xs text-kb-text-tertiary">
          Equivalent to{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl set env {type === 'deployments' ? 'deploy' : type === 'statefulsets' ? 'sts' : 'ds'}/{name}
          </code>
          . Strategic merge patch — adds, updates, and removes specific entries; existing entries you don't touch are preserved.
        </p>

        {containers.length === 0 ? (
          <div className="text-xs text-kb-text-tertiary border border-kb-border rounded-lg p-4 text-center">
            No containers detected on this workload's pod template.
          </div>
        ) : (
          <>
            {/* Container picker — only render when there's more than
                one container; single-container workloads don't need
                the chrome. */}
            {containers.length > 1 && (
              <div className="flex items-center gap-2">
                <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
                  Container
                </span>
                <select
                  value={selectedContainer}
                  onChange={(e) => setSelectedContainer(e.target.value)}
                  className="text-xs font-mono bg-kb-bg border border-kb-border rounded px-2 py-1 text-kb-text-primary focus:border-kb-border-active outline-none"
                >
                  {containers.map((c) => {
                    const n = (draftsByContainer[c.name]?.length ?? 0)
                    return (
                      <option key={c.name} value={c.name}>
                        {c.name}{n > 0 ? ` · ${n} pending` : ''}
                      </option>
                    )
                  })}
                </select>
              </div>
            )}

            {/* Existing env baseline */}
            {selectedSpec && (
              <div>
                <div className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium mb-1.5">
                  Existing env
                </div>
                <div className="border border-kb-border rounded-lg overflow-hidden">
                  {selectedSpec.env.length === 0 ? (
                    <div className="text-[11px] text-kb-text-tertiary px-3 py-3 text-center">
                      No env entries on this container.
                    </div>
                  ) : (
                    <table className="w-full text-xs">
                      <thead className="bg-kb-elevated border-b border-kb-border">
                        <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                          <th className="px-3 py-2 font-medium w-40">Name</th>
                          <th className="px-3 py-2 font-medium w-20">Type</th>
                          <th className="px-3 py-2 font-medium">Value / Source</th>
                          <th className="px-3 py-2 font-medium w-24"></th>
                        </tr>
                      </thead>
                      <tbody>
                        {selectedSpec.env.map((b) => {
                          const isMarkedRemoved = removedNames.has(`${selectedContainer}::${b.name}`)
                          const isMarkedUpdated = updatingNames.has(`${selectedContainer}::${b.name}`)
                          // Three visual states:
                          //   normal      → show edit/remove actions
                          //   updating    → dim + "→ pending update" pill, no actions (operator
                          //                 manages it from the draft row below)
                          //   removed     → strike-through, no actions (operator manages it from
                          //                 the draft row below)
                          const rowState = isMarkedRemoved ? 'removed' : isMarkedUpdated ? 'updating' : 'normal'
                          return (
                            <tr
                              key={b.name}
                              className={`border-b border-kb-border last:border-b-0 ${
                                rowState === 'removed'
                                  ? 'opacity-40 line-through'
                                  : rowState === 'updating'
                                  ? 'opacity-60'
                                  : ''
                              }`}
                            >
                              <td className="px-3 py-2 font-mono text-[11px] text-kb-text-primary break-all">
                                {b.name}
                                {SENSITIVE_NAME_RE.test(b.name) && (
                                  <Lock className="inline-block w-3 h-3 ml-1 text-kb-text-tertiary" />
                                )}
                              </td>
                              <td className="px-3 py-2 text-[10px] uppercase tracking-wider text-kb-text-tertiary">
                                {b.kind}
                              </td>
                              <td className="px-3 py-2 font-mono text-[11px] text-kb-text-secondary break-all">
                                {renderBaselineValue(b)}
                              </td>
                              <td className="px-3 py-2 text-right">
                                {rowState === 'normal' && (
                                  <span className="flex justify-end gap-1.5">
                                    <button
                                      type="button"
                                      onClick={() => addEditRow(b)}
                                      className="text-[10px] text-status-info hover:underline"
                                      title="Edit this entry — opens a draft row pre-filled with the current value"
                                    >
                                      edit
                                    </button>
                                    <button
                                      type="button"
                                      onClick={() => addRemoveRow(b)}
                                      className="text-[10px] text-status-error hover:underline"
                                      title="Remove this entry on apply"
                                    >
                                      remove
                                    </button>
                                  </span>
                                )}
                                {rowState === 'updating' && (
                                  <span className="text-[9px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-status-info-dim text-status-info">
                                    pending update
                                  </span>
                                )}
                                {rowState === 'removed' && (
                                  <span className="text-[9px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-status-error-dim text-status-error">
                                    pending remove
                                  </span>
                                )}
                              </td>
                            </tr>
                          )
                        })}
                      </tbody>
                    </table>
                  )}
                </div>
              </div>
            )}

            {/* Draft rows — pending changes for this container. */}
            <div>
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
                  Pending changes ({drafts.length})
                </span>
                <button
                  type="button"
                  onClick={addEmptyRow}
                  className="flex items-center gap-1 text-[11px] text-status-info hover:underline"
                >
                  <Plus className="w-3 h-3" /> Add row
                </button>
              </div>
              {drafts.length === 0 ? (
                <div className="text-[11px] text-kb-text-tertiary border border-dashed border-kb-border rounded-lg px-3 py-3 text-center">
                  No pending changes for this container. Click "Add row" or use Edit/Remove on an existing entry above.
                </div>
              ) : (
                <div className="space-y-2">
                  {drafts.map((d) => (
                    <DraftRow
                      key={d.rowId}
                      draft={d}
                      cmNames={cmNames}
                      secretNames={secretNames}
                      existsInBaseline={
                        d.name !== '' &&
                        (baselineNamesByContainer[selectedContainer]?.has(d.name) ?? false)
                      }
                      onUpdate={(patch) => updateDraft(d.rowId, patch)}
                      onRemove={() => removeDraft(d.rowId)}
                    />
                  ))}
                </div>
              )}
            </div>
          </>
        )}

        {/* Trigger rollout toggle */}
        <label className="flex items-start gap-2 text-[11px] text-kb-text-secondary cursor-pointer select-none">
          <input
            type="checkbox"
            checked={triggerRollout}
            onChange={(e) => setTriggerRollout(e.target.checked)}
            className="mt-0.5 accent-status-info"
          />
          <div>
            <span className="font-medium text-kb-text-primary">Trigger rollout after applying</span>
            <div className="text-kb-text-tertiary">
              Adds the rollout-restart annotation. Existing pods are recreated immediately so they pick up literal-value changes — without this, env changes apply only on the next pod restart.
            </div>
          </div>
        </label>

        {validationErrors.length > 0 && (
          <div className="text-[11px] text-status-error space-y-0.5">
            {validationErrors.map((e, i) => (
              <div key={i} className="flex items-start gap-1.5">
                <AlertTriangle className="w-3 h-3 mt-0.5 shrink-0" />
                <span>{e}</span>
              </div>
            ))}
          </div>
        )}

        {error && (
          <div className="flex items-center gap-2 text-xs text-status-error">
            <AlertTriangle className="w-3 h-3" />
            <span className="break-words">{error}</span>
          </div>
        )}

        {dirty && (
          <p className="text-[11px] text-kb-text-tertiary flex items-start gap-1.5">
            <Info className="w-3 h-3 mt-0.5 shrink-0" />
            {triggerRollout
              ? 'Patch + rollout in one step. The modal switches to live progress on apply.'
              : 'Patch only. Existing pods keep running with the old env until they restart.'}
          </p>
        )}
      </div>

      <div className="px-5 py-3 border-t border-kb-border flex justify-end gap-2 shrink-0">
        <button
          onClick={onClose}
          disabled={busy}
          className="px-3 py-1.5 text-xs rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated disabled:opacity-50"
        >
          Cancel
        </button>
        <button
          onClick={submit}
          disabled={busy || !dirty || validationErrors.length > 0 || containers.length === 0}
          className="px-3 py-1.5 text-xs rounded bg-status-info-dim text-status-info hover:bg-status-info hover:text-kb-bg border border-status-info disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1.5"
        >
          {busy && <RefreshCw className="w-3 h-3 animate-spin" />}
          {busy ? 'Applying…' : 'Apply'}
        </button>
      </div>
    </Modal>
  )
}

function renderBaselineValue(b: BaselineEnv): string {
  if (b.kind === 'literal') {
    if (!b.value) return '(empty)'
    if (SENSITIVE_NAME_RE.test(b.name)) return '••••• (literal)'
    return b.value
  }
  if (b.kind === 'configMap' && b.valueFrom?.configMapKeyRef) {
    return `cm: ${b.valueFrom.configMapKeyRef.name} / ${b.valueFrom.configMapKeyRef.key}`
  }
  if (b.kind === 'secret' && b.valueFrom?.secretKeyRef) {
    return `secret: ${b.valueFrom.secretKeyRef.name} / ${b.valueFrom.secretKeyRef.key}`
  }
  if (b.kind === 'field' && b.valueFrom?.fieldRef) {
    return `field: ${b.valueFrom.fieldRef.fieldPath}`
  }
  return `(${b.kind})`
}

function DraftRow({
  draft: d,
  cmNames,
  secretNames,
  existsInBaseline,
  onUpdate,
  onRemove,
}: {
  draft: EnvDraft
  cmNames: string[]
  secretNames: string[]
  // True when a baseline entry on the same container already has
  // this name. Drives the "will update existing" pill — strategic
  // merge merges by name, so an "Add row" with an existing name is
  // semantically an update, not an add. Surfacing this prevents the
  // confusion of "I added a new row but it overwrote the old one."
  existsInBaseline: boolean
  onUpdate: (patch: Partial<EnvDraft>) => void
  onRemove: () => void
}) {
  const sensitive = d.name && SENSITIVE_NAME_RE.test(d.name) && d.action === 'set' && d.type === 'literal'
  // Only call out the collision when the operator's intent is SET —
  // remove always targets an existing entry by definition.
  const collidesWithBaseline = d.action === 'set' && existsInBaseline
  return (
    <div
      className={`border rounded-lg p-2.5 space-y-2 ${
        d.action === 'remove' ? 'border-status-error/40 bg-status-error-dim/20' : 'border-kb-border bg-kb-elevated'
      }`}
    >
      <div className="flex items-center gap-2">
        <select
          value={d.action}
          onChange={(e) => onUpdate({ action: e.target.value as 'set' | 'remove' })}
          className="text-[11px] font-mono bg-kb-bg border border-kb-border rounded px-1.5 py-0.5 text-kb-text-primary outline-none"
        >
          <option value="set">SET</option>
          <option value="remove">REMOVE</option>
        </select>
        <input
          type="text"
          value={d.name}
          onChange={(e) => onUpdate({ name: e.target.value })}
          placeholder="ENV_NAME"
          className="flex-1 text-[11px] font-mono bg-kb-bg border border-kb-border rounded px-2 py-1 text-kb-text-primary outline-none focus:border-kb-border-active"
        />
        {collidesWithBaseline && (
          <span
            className="text-[9px] uppercase tracking-wider px-1.5 py-0.5 rounded bg-status-info-dim text-status-info whitespace-nowrap"
            title="An entry with this name already exists. Strategic merge will update it — no duplicate is created."
          >
            updates existing
          </span>
        )}
        <button
          type="button"
          onClick={onRemove}
          title="Drop this row"
          className="p-1 rounded hover:bg-kb-card text-kb-text-tertiary hover:text-status-error"
        >
          <X className="w-3 h-3" />
        </button>
      </div>

      {d.action === 'set' && (
        <>
          <div className="flex items-center gap-2">
            <span className="text-[10px] uppercase tracking-wider text-kb-text-tertiary w-12 font-medium">Type</span>
            <select
              value={d.type}
              onChange={(e) => onUpdate({ type: e.target.value as EnvDraftType })}
              className="text-[11px] font-mono bg-kb-bg border border-kb-border rounded px-1.5 py-0.5 text-kb-text-primary outline-none"
            >
              <option value="literal">Literal</option>
              <option value="configMap">ConfigMap ref</option>
              <option value="secret">Secret ref</option>
            </select>
          </div>

          {d.type === 'literal' && (
            <div className="flex items-center gap-2">
              <span className="text-[10px] uppercase tracking-wider text-kb-text-tertiary w-12 font-medium">Value</span>
              <input
                type="text"
                value={d.value}
                onChange={(e) => onUpdate({ value: e.target.value })}
                placeholder="literal value"
                className="flex-1 text-[11px] font-mono bg-kb-bg border border-kb-border rounded px-2 py-1 text-kb-text-primary outline-none focus:border-kb-border-active"
              />
            </div>
          )}

          {d.type === 'configMap' && (
            <SourceKeyRow
              sourceLabel="ConfigMap"
              sourceValue={d.cmName}
              keyValue={d.cmKey}
              sourceOptions={cmNames}
              onSourceChange={(v) => onUpdate({ cmName: v })}
              onKeyChange={(v) => onUpdate({ cmKey: v })}
            />
          )}

          {d.type === 'secret' && (
            <SourceKeyRow
              sourceLabel="Secret"
              sourceValue={d.secretName}
              keyValue={d.secretKey}
              sourceOptions={secretNames}
              onSourceChange={(v) => onUpdate({ secretName: v })}
              onKeyChange={(v) => onUpdate({ secretKey: v })}
            />
          )}

          {sensitive && (
            <div className="flex items-start gap-1.5 text-[11px] text-status-warn">
              <AlertTriangle className="w-3 h-3 mt-0.5 shrink-0" />
              <span>
                The name <code className="font-mono">{d.name}</code> looks sensitive. Consider using a Secret reference instead of a literal value.
              </span>
            </div>
          )}
        </>
      )}
    </div>
  )
}

function SourceKeyRow({
  sourceLabel,
  sourceValue,
  keyValue,
  sourceOptions,
  onSourceChange,
  onKeyChange,
}: {
  sourceLabel: string
  sourceValue: string
  keyValue: string
  sourceOptions: string[]
  onSourceChange: (v: string) => void
  onKeyChange: (v: string) => void
}) {
  return (
    <>
      <div className="flex items-center gap-2">
        <span className="text-[10px] uppercase tracking-wider text-kb-text-tertiary w-12 font-medium">
          {sourceLabel}
        </span>
        <select
          value={sourceValue}
          onChange={(e) => onSourceChange(e.target.value)}
          className="flex-1 text-[11px] font-mono bg-kb-bg border border-kb-border rounded px-2 py-1 text-kb-text-primary outline-none focus:border-kb-border-active"
        >
          <option value="">Select…</option>
          {sourceOptions.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-[10px] uppercase tracking-wider text-kb-text-tertiary w-12 font-medium">Key</span>
        <input
          type="text"
          value={keyValue}
          onChange={(e) => onKeyChange(e.target.value)}
          placeholder="key inside the source"
          className="flex-1 text-[11px] font-mono bg-kb-bg border border-kb-border rounded px-2 py-1 text-kb-text-primary outline-none focus:border-kb-border-active"
        />
      </div>
    </>
  )
}
