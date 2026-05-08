import { useEffect, useMemo, useState } from 'react'
import { Tag, Plus, X, AlertTriangle, Info, Lock } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import type { EditMetadataBody, MetadataMapEdit } from '@/services/api'
import { useQueryClient } from '@tanstack/react-query'
import type { ResourceItem } from '@/types/kubernetes'

// EditMetadataModal — UI for kubectl label / kubectl annotate combined.
//
// Two tables (labels + annotations), each row is a key/value pair the
// operator can add, edit, or mark for removal. The submit step builds
// the {add, remove} envelope per map; the backend issues one JSON
// merge patch with both maps in the same metadata block, atomic.
//
// Managed-key indicator: keys with reserved prefixes (kubectl.kubernetes.io,
// helm.sh, app.kubernetes.io/managed-by, argocd.argoproj.io, etc.) get
// a small lock pill. Editing them is risky — controllers reconcile them
// back — but we don't BLOCK the edit (kubectl doesn't either). Operator
// gets a warning, decides for themselves.
//
// Scope: ALL kinds the dynamic client supports. The button on the
// detail page is enabled for every resource that has a name.

const NAME_RE = /^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*\/)?[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$/
const VALUE_RE = /^([A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?)?$/

// Reserved-prefix matchers. Keys matching these are managed by tooling
// and editing them silently breaks downstream automation. We mark them
// visually but don't block edits.
const MANAGED_KEY_PREFIXES = [
  'kubectl.kubernetes.io/',
  'kubernetes.io/',
  'k8s.io/',
  'helm.sh/',
  'argocd.argoproj.io/',
  'kustomize.toolkit.fluxcd.io/',
  'fluxcd.io/',
  'deployment.kubernetes.io/',
  'app.kubernetes.io/managed-by',
]

function isManagedKey(key: string): boolean {
  return MANAGED_KEY_PREFIXES.some((p) => key === p || key.startsWith(p))
}

interface MapRow {
  rowId: string
  // baselineValue is the live value (only set for rows that exist on
  // the resource). undefined for new rows added by the operator.
  baselineValue?: string
  key: string
  value: string
  // marked-for-remove rows keep their key but the operator chose
  // "remove" instead of editing the value. The merged submit body
  // routes them to remove[] instead of add{}.
  remove: boolean
}

function newRowId(): string {
  return Math.random().toString(36).slice(2, 10)
}

function rowsFromMap(m: Record<string, string> | undefined): MapRow[] {
  if (!m) return []
  return Object.entries(m).map(([k, v]) => ({
    rowId: newRowId(),
    baselineValue: v,
    key: k,
    value: v,
    remove: false,
  }))
}

// buildEdit converts the operator's row state into the API's
// {add, remove} envelope. Returns undefined when the operator made
// no changes — the submit step skips empty edits so an annotations-
// only patch doesn't accidentally clobber the labels map.
function buildEdit(rows: MapRow[]): MetadataMapEdit | undefined {
  const add: Record<string, string> = {}
  const remove: string[] = []
  let hasChanges = false
  for (const r of rows) {
    if (!r.key) continue
    if (r.remove && r.baselineValue !== undefined) {
      remove.push(r.key)
      hasChanges = true
      continue
    }
    if (r.baselineValue === undefined) {
      // new row — always an add
      add[r.key] = r.value
      hasChanges = true
    } else if (r.value !== r.baselineValue) {
      // existing row, value changed — also an add (merge patch updates)
      add[r.key] = r.value
      hasChanges = true
    }
  }
  if (!hasChanges) return undefined
  const out: MetadataMapEdit = {}
  if (Object.keys(add).length > 0) out.add = add
  if (remove.length > 0) out.remove = remove
  return out
}

export function EditMetadataModal({
  type,
  namespace,
  name,
  resource,
  onClose,
}: {
  type: string
  namespace: string
  name: string
  resource: ResourceItem | undefined
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const baselineLabels = (resource as unknown as { labels?: Record<string, string> })?.labels
  const baselineAnnotations = (resource as unknown as { annotations?: Record<string, string> })?.annotations

  const [labelRows, setLabelRows] = useState<MapRow[]>([])
  const [annotationRows, setAnnotationRows] = useState<MapRow[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Initialize rows from the resource's current state. Only run
  // once — re-initializing on every refetch would clobber the
  // operator's drafts.
  const [initialized, setInitialized] = useState(false)
  useEffect(() => {
    if (initialized) return
    if (!resource) return
    setLabelRows(rowsFromMap(baselineLabels))
    setAnnotationRows(rowsFromMap(baselineAnnotations))
    setInitialized(true)
  }, [initialized, resource, baselineLabels, baselineAnnotations])

  const dirty = useMemo(() => {
    return Boolean(buildEdit(labelRows)) || Boolean(buildEdit(annotationRows))
  }, [labelRows, annotationRows])

  const validationErrors = useMemo(() => {
    const errs: string[] = []
    const seen = new Set<string>()
    for (const r of labelRows) {
      if (r.remove || !r.key) continue
      if (seen.has(`L:${r.key}`)) errs.push(`Duplicate label key: ${r.key}`)
      seen.add(`L:${r.key}`)
      if (!NAME_RE.test(r.key)) {
        errs.push(`Label key ${JSON.stringify(r.key)} is not a valid metadata key`)
      }
      if (r.value.length > 63) {
        errs.push(`Label ${r.key}: value exceeds 63 chars (use an annotation for longer values)`)
      } else if (!VALUE_RE.test(r.value)) {
        errs.push(`Label ${r.key}: value must be empty or letters/digits/dashes/dots/underscores`)
      }
    }
    const seenA = new Set<string>()
    for (const r of annotationRows) {
      if (r.remove || !r.key) continue
      if (seenA.has(r.key)) errs.push(`Duplicate annotation key: ${r.key}`)
      seenA.add(r.key)
      if (!NAME_RE.test(r.key)) {
        errs.push(`Annotation key ${JSON.stringify(r.key)} is not a valid metadata key`)
      }
    }
    return errs
  }, [labelRows, annotationRows])

  async function submit() {
    setBusy(true)
    setError(null)
    try {
      const body: EditMetadataBody = {}
      const labelsEdit = buildEdit(labelRows)
      if (labelsEdit) body.labels = labelsEdit
      const annotationsEdit = buildEdit(annotationRows)
      if (annotationsEdit) body.annotations = annotationsEdit
      if (!body.labels && !body.annotations) {
        setError('No changes to apply')
        return
      }
      await api.editResourceMetadata(type, namespace, name, body)
      queryClient.invalidateQueries({ queryKey: ['resource-detail', type, namespace, name] })
      queryClient.invalidateQueries({ queryKey: ['resources', type] })
      onClose()
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : (e as Error).message
      setError(msg)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      badge={
        <span className="flex items-center gap-1 text-status-info">
          <Tag className="w-3 h-3" /> edit metadata
        </span>
      }
      title={`Edit metadata · ${name}`}
      onClose={onClose}
      size="lg"
    >
      <div className="flex-1 overflow-y-auto px-5 py-4 space-y-5">
        <p className="text-xs text-kb-text-tertiary">
          Equivalent to{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl label
          </code>{' '}
          /{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl annotate
          </code>
          . JSON merge patch — labels and annotations apply atomically in one call.
        </p>

        <MapEditor
          title="Labels"
          rows={labelRows}
          setRows={setLabelRows}
          valueValidator={(v) => v.length <= 63 && VALUE_RE.test(v)}
          maxValueLen={63}
        />

        <MapEditor
          title="Annotations"
          rows={annotationRows}
          setRows={setAnnotationRows}
          // Annotations have no value-content rules and no length cap
          // per-key (just a 256 KiB total cap, enforced server-side).
          valueValidator={() => true}
        />

        <div className="flex items-start gap-2 text-[11px] text-kb-text-tertiary">
          <Info className="w-3.5 h-3.5 mt-0.5 shrink-0" />
          <div>
            Keys with{' '}
            <Lock className="inline-block w-3 h-3 align-text-bottom" />{' '}
            are managed by tooling (kubectl-apply, Helm, Argo CD, Flux, etc.). Edits to those keys may be reconciled back by the controller within seconds.
          </div>
        </div>

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
          disabled={busy || !dirty || validationErrors.length > 0}
          className="px-3 py-1.5 text-xs rounded bg-status-info-dim text-status-info hover:bg-status-info hover:text-kb-bg border border-status-info disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {busy ? 'Applying…' : 'Apply'}
        </button>
      </div>
    </Modal>
  )
}

function MapEditor({
  title,
  rows,
  setRows,
  valueValidator,
  maxValueLen,
}: {
  title: string
  rows: MapRow[]
  setRows: (updater: (prev: MapRow[]) => MapRow[]) => void
  valueValidator: (v: string) => boolean
  maxValueLen?: number
}) {
  function update(rowId: string, patch: Partial<MapRow>) {
    setRows((prev) => prev.map((r) => (r.rowId === rowId ? { ...r, ...patch } : r)))
  }
  function deleteRow(rowId: string) {
    setRows((prev) => prev.filter((r) => r.rowId !== rowId))
  }
  function addRow() {
    setRows((prev) => [
      ...prev,
      { rowId: newRowId(), key: '', value: '', remove: false },
    ])
  }
  return (
    <div>
      <div className="flex items-center justify-between mb-1.5">
        <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
          {title} ({rows.length})
        </span>
        <button
          type="button"
          onClick={addRow}
          className="flex items-center gap-1 text-[11px] text-status-info hover:underline"
        >
          <Plus className="w-3 h-3" /> Add row
        </button>
      </div>
      {rows.length === 0 ? (
        <div className="text-[11px] text-kb-text-tertiary border border-dashed border-kb-border rounded-lg px-3 py-3 text-center">
          No {title.toLowerCase()} on this resource. Click "Add row" to add one.
        </div>
      ) : (
        <div className="space-y-1.5">
          {rows.map((r) => {
            const managed = r.key && isManagedKey(r.key)
            const isExisting = r.baselineValue !== undefined
            const valueChanged = isExisting && !r.remove && r.value !== r.baselineValue
            const valid = !r.key || (NAME_RE.test(r.key) && valueValidator(r.value))
            return (
              <div
                key={r.rowId}
                className={`flex items-center gap-1.5 ${
                  r.remove ? 'opacity-60' : ''
                }`}
              >
                <input
                  type="text"
                  value={r.key}
                  onChange={(e) => update(r.rowId, { key: e.target.value })}
                  placeholder="key"
                  disabled={isExisting}
                  // Lock the key on existing rows so the operator can't
                  // mistakenly rename a label and turn an "update" into
                  // an "add a new + leave the old". To rename: remove
                  // the old + add a new.
                  className={`w-1/3 text-[11px] font-mono bg-kb-bg border rounded px-2 py-1 outline-none ${
                    valid ? 'border-kb-border' : 'border-status-error/50'
                  } ${isExisting ? 'text-kb-text-tertiary' : 'text-kb-text-primary focus:border-kb-border-active'}`}
                />
                {managed && (
                  <span title="Managed by tooling — edits may be reverted by the owning controller">
                    <Lock className="w-3 h-3 text-kb-text-tertiary shrink-0" />
                  </span>
                )}
                <input
                  type="text"
                  value={r.value}
                  onChange={(e) => update(r.rowId, { value: e.target.value })}
                  placeholder="value"
                  maxLength={maxValueLen}
                  disabled={r.remove}
                  className={`flex-1 text-[11px] font-mono bg-kb-bg border rounded px-2 py-1 text-kb-text-primary outline-none focus:border-kb-border-active ${
                    valueChanged ? 'border-status-info/40 bg-status-info-dim/20' : 'border-kb-border'
                  } ${r.remove ? 'line-through' : ''}`}
                />
                {/* Fixed-width action slot — keeps the input columns
                    aligned across rows whose right-side button varies
                    in width (text "REMOVE/UNDO" vs icon X). Without
                    the fixed slot, every row's value input gets a
                    different effective width depending on which kind
                    of action button is rendered. */}
                <div className="w-16 shrink-0 flex justify-end">
                  {isExisting ? (
                    <button
                      type="button"
                      onClick={() => update(r.rowId, { remove: !r.remove })}
                      title={r.remove ? 'Undo remove' : 'Mark for removal'}
                      className={`text-[10px] uppercase tracking-wider px-1.5 py-0.5 rounded ${
                        r.remove
                          ? 'bg-kb-elevated text-kb-text-secondary hover:bg-kb-card'
                          : 'text-status-error hover:bg-status-error-dim'
                      }`}
                    >
                      {r.remove ? 'undo' : 'remove'}
                    </button>
                  ) : (
                    <button
                      type="button"
                      onClick={() => deleteRow(r.rowId)}
                      title="Drop this row"
                      className="p-1 rounded hover:bg-kb-card text-kb-text-tertiary hover:text-status-error"
                    >
                      <X className="w-3 h-3" />
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
