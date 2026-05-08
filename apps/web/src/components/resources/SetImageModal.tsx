import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Image as ImageIcon, AlertTriangle, Info, ChevronDown } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import { useQueryClient } from '@tanstack/react-query'
import type { ResourceItem } from '@/types/kubernetes'
import { useRolloutHistory } from '@/hooks/useResources'
import { RolloutStatusPanel } from './RolloutStatusPanel'

// SetImageModal — UI for the `kubectl set image` equivalent.
//
// Layout:
//   - One row per container in the workload's pod template.
//   - The "Current" cell shows what's deployed today.
//   - The "New" input is pre-filled with the current image; operator
//     edits only the rows they want to change. A small dropdown next
//     to the input lists prior images for that container, sourced
//     from the rollout-history endpoint — one click to pick a
//     known-good image without having to remember the tag.
//   - We submit ALL containers in the request (not just the changed
//     ones) — the backend short-circuits to "unchanged" if every
//     image equals current. Sending unchanged rows alongside changed
//     ones makes the audit log unambiguous about what the operator
//     reviewed at decision time.
//   - On successful submit, the modal switches to RolloutStatusPanel
//     and stays open so the operator can watch the rolling update
//     converge or fail.

interface ContainerImage {
  name: string
  image: string
}

function extractContainers(item: ResourceItem | undefined): ContainerImage[] {
  if (!item) return []
  // The detail handler flattens the pod template down to a top-level
  // `containers: [{name, image, imagePullPolicy, resources, ports}]`
  // (see deploymentToMap / templateContainerSpecs in connector.go).
  const cs = (item as unknown as { containers?: unknown }).containers
  if (!Array.isArray(cs)) return []
  return cs
    .map((c) => {
      if (typeof c !== 'object' || c === null) return null
      const obj = c as { name?: unknown; image?: unknown }
      if (typeof obj.name !== 'string' || typeof obj.image !== 'string') return null
      return { name: obj.name, image: obj.image }
    })
    .filter((c): c is ContainerImage => c !== null)
}

function registryHost(image: string): string {
  // Best-effort — `host[:port]/path:tag` vs `path:tag` (Docker Hub).
  // If the leading segment has a `.` or `:` (port) or is `localhost`,
  // it's the registry. Otherwise the implicit default is docker.io.
  const slash = image.indexOf('/')
  if (slash < 0) return 'docker.io'
  const head = image.slice(0, slash)
  if (head === 'localhost' || head.includes('.') || head.includes(':')) {
    return head
  }
  return 'docker.io'
}

function imageRefKind(image: string): 'tag' | 'digest' | 'unspecified' {
  if (image.includes('@sha256:')) return 'digest'
  if (image.includes(':')) {
    // Trim the registry portion (which may have its own colon for the port).
    const slash = image.lastIndexOf('/')
    const tail = slash >= 0 ? image.slice(slash + 1) : image
    if (tail.includes(':')) return 'tag'
  }
  return 'unspecified'
}

export function SetImageModal({
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
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [unchangedNotice, setUnchangedNotice] = useState(false)
  // After a successful patched response we switch to the live
  // rollout-status view. expectedGeneration + submittedAtMs scope
  // convergence + failure detection to THIS submission, same way
  // RollbackModal uses them.
  const [applying, setApplying] = useState<{
    expectedGeneration: number
    submittedAtMs: number
  } | null>(null)

  // Rollout history is the data source for the "prior images"
  // dropdown. Only fetched when the modal is mounted (the hook
  // already gates on namespace+name being non-empty). Same shape
  // is reused by RollbackModal — no duplicate query.
  const { data: history } = useRolloutHistory(type, namespace, name)

  // Index history by container name → ordered list of distinct
  // images, newest revision first. Excludes the current image since
  // it's already shown in the "Current" cell. Capped at 10 entries
  // so a long-lived workload with hundreds of revisions doesn't
  // produce a scrolling popover.
  const priorImagesByContainer = useMemo(() => {
    const out: Record<string, { image: string; revision: number; active: boolean }[]> = {}
    if (!history?.revisions) return out
    const currentByContainer = new Map(containers.map((c) => [c.name, c.image]))
    for (const rev of history.revisions) {
      for (const img of rev.images ?? []) {
        const current = currentByContainer.get(img.container)
        if (img.image === current) continue
        const list = out[img.container] ?? (out[img.container] = [])
        // Dedup — first occurrence wins (newest revision first
        // because history.revisions is sorted DESC).
        if (!list.some((e) => e.image === img.image)) {
          list.push({ image: img.image, revision: rev.revision, active: rev.active })
        }
      }
    }
    for (const k of Object.keys(out)) {
      out[k] = out[k].slice(0, 10)
    }
    return out
  }, [history, containers])

  // Initialize drafts from the live container list. Re-run if the
  // resource refreshes mid-modal so the operator never edits a stale
  // image string.
  useEffect(() => {
    setDrafts((prev) => {
      const next = { ...prev }
      for (const c of containers) {
        if (next[c.name] === undefined) next[c.name] = c.image
      }
      return next
    })
  }, [containers])

  const dirty = useMemo(() => {
    return containers.some((c) => drafts[c.name] !== undefined && drafts[c.name] !== c.image)
  }, [containers, drafts])

  const crossRegistryWarnings = useMemo(() => {
    const warnings: { container: string; from: string; to: string }[] = []
    for (const c of containers) {
      const draft = drafts[c.name]
      if (!draft || draft === c.image) continue
      const fromHost = registryHost(c.image)
      const toHost = registryHost(draft)
      if (fromHost !== toHost) {
        warnings.push({ container: c.name, from: fromHost, to: toHost })
      }
    }
    return warnings
  }, [containers, drafts])

  async function submit() {
    setBusy(true)
    setError(null)
    setUnchangedNotice(false)
    // Pre-await snapshots — see RollbackModal for the rationale on
    // why these are captured BEFORE the network call.
    const preGen = ((resource as unknown as { generation?: number })?.generation ?? 0) + 1
    const submittedAtMs = Date.now()
    try {
      const images = containers.map((c) => ({
        container: c.name,
        image: drafts[c.name] ?? c.image,
      }))
      const res = await api.setImageResource(type, namespace, name, images)
      if (res.status === 'unchanged') {
        setUnchangedNotice(true)
        setBusy(false)
        return
      }
      if (res.resource) {
        queryClient.setQueryData(['resource-detail', type, namespace, name], res.resource)
      }
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      queryClient.invalidateQueries({ queryKey: ['rollout-history', type, namespace, name] })
      // Switch to the live progress view instead of closing — the
      // operator can watch the new pods come up and catch a bad
      // image (ImagePullBackOff) from inside the same modal.
      setApplying({ expectedGeneration: preGen, submittedAtMs })
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
            <ImageIcon className="w-3 h-3" /> applying
          </span>
        }
        title={`Set image · ${name}`}
        onClose={onClose}
        size="lg"
      >
        <div className="flex-1 overflow-y-auto px-5 py-4">
          <RolloutStatusPanel
            type={type}
            namespace={namespace}
            name={name}
            title="Applying new container image…"
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
          <ImageIcon className="w-3 h-3" /> set image
        </span>
      }
      title={`Set image · ${name}`}
      onClose={onClose}
      size="lg"
    >
      <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
        <p className="text-xs text-kb-text-tertiary">
          Equivalent to{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl set image {type === 'deployments' ? 'deploy' : type === 'statefulsets' ? 'sts' : 'ds'}/{name}
          </code>
          . Strategic merge patch — only the targeted containers' image fields are
          touched; env, volumes, probes, and resources are preserved.
        </p>

        {containers.length === 0 ? (
          <div className="text-xs text-kb-text-tertiary border border-kb-border rounded-lg p-4 text-center">
            No containers detected on this workload's pod template.
          </div>
        ) : (
          <div className="border border-kb-border rounded-lg overflow-hidden">
            <table className="w-full text-xs">
              <thead className="bg-kb-elevated border-b border-kb-border">
                <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                  <th className="px-3 py-2 font-medium w-32">Container</th>
                  <th className="px-3 py-2 font-medium">Current</th>
                  <th className="px-3 py-2 font-medium">New</th>
                </tr>
              </thead>
              <tbody>
                {containers.map((c) => {
                  const draft = drafts[c.name] ?? c.image
                  const changed = draft !== c.image
                  const refKind = imageRefKind(draft)
                  const priorImages = priorImagesByContainer[c.name] ?? []
                  return (
                    <tr
                      key={c.name}
                      className={`border-b border-kb-border last:border-b-0 ${changed ? 'bg-status-info-dim/30' : ''}`}
                    >
                      <td className="px-3 py-2.5 font-mono text-kb-text-primary text-[11px]">
                        {c.name}
                      </td>
                      <td className="px-3 py-2.5 font-mono text-kb-text-secondary text-[11px] break-all">
                        {c.image}
                      </td>
                      <td className="px-3 py-2.5">
                        <div className="flex items-center gap-2">
                          <ImageInputWithSuggestions
                            value={draft}
                            onChange={(v) => setDrafts({ ...drafts, [c.name]: v })}
                            changed={changed}
                            priorImages={priorImages}
                          />
                          {refKind === 'digest' && (
                            <span
                              className="text-[9px] font-mono px-1 py-0.5 rounded bg-status-ok-dim text-status-ok uppercase"
                              title="Pinned by digest — immutable"
                            >
                              digest
                            </span>
                          )}
                          {refKind === 'unspecified' && (
                            <span
                              className="text-[9px] font-mono px-1 py-0.5 rounded bg-status-warn-dim text-status-warn uppercase"
                              title="No tag — kubelet will pull :latest"
                            >
                              no tag
                            </span>
                          )}
                        </div>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}

        {crossRegistryWarnings.length > 0 && (
          <div className="flex items-start gap-2 text-[11px] text-status-warn border border-status-warn/30 bg-status-warn-dim rounded p-2.5">
            <AlertTriangle className="w-3.5 h-3.5 mt-0.5 shrink-0" />
            <div>
              <div className="font-semibold mb-1">Registry change detected</div>
              <ul className="space-y-0.5">
                {crossRegistryWarnings.map((w) => (
                  <li key={w.container} className="font-mono">
                    {w.container}: {w.from} → {w.to}
                  </li>
                ))}
              </ul>
              <div className="mt-1 text-kb-text-secondary">
                Make sure the workload's <code className="font-mono">imagePullSecrets</code> covers the new registry, or the pod will fail with ImagePullBackOff.
              </div>
            </div>
          </div>
        )}

        {unchangedNotice && (
          <div className="flex items-center gap-2 text-[11px] text-kb-text-tertiary border border-kb-border bg-kb-elevated rounded p-2.5">
            <Info className="w-3.5 h-3.5 shrink-0" />
            Every requested image already matches the current image. Nothing to roll out.
          </div>
        )}

        {error && (
          <div className="flex items-center gap-2 text-xs text-status-error">
            <AlertTriangle className="w-3 h-3" />
            <span className="break-words">{error}</span>
          </div>
        )}

        {dirty && !unchangedNotice && (
          <p className="text-[11px] text-kb-text-tertiary">
            Triggers a rolling update with the workload's current strategy. New pods become Ready before old ones terminate (controlled by{' '}
            <code className="font-mono">spec.strategy</code> /{' '}
            <code className="font-mono">spec.updateStrategy</code>).
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
          disabled={busy || !dirty || containers.length === 0}
          className="px-3 py-1.5 text-xs rounded bg-status-info-dim text-status-info hover:bg-status-info hover:text-kb-bg border border-status-info disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {busy ? 'Applying…' : 'Apply'}
        </button>
      </div>
    </Modal>
  )
}

// ImageInputWithSuggestions — text input with an optional dropdown
// of prior images for this container. The dropdown only renders the
// trigger when there's history to show; for a brand-new workload
// with one revision, the input behaves as a plain text field.
//
// The popover is rendered via a portal into document.body. The
// modal has THREE overflow-hidden / overflow-y-auto ancestors (the
// shared Modal card, the modal body scroll wrapper, and the table
// wrapper) — an absolutely-positioned popover gets clipped by all of
// them. Portal + position:fixed coordinates derived from the input's
// getBoundingClientRect bypasses the whole stack. Same trick the
// usage tooltip uses.
//
// Click outside / Escape closes the dropdown. Selecting an item
// fills the input and closes the popover.
function ImageInputWithSuggestions({
  value,
  onChange,
  changed,
  priorImages,
}: {
  value: string
  onChange: (v: string) => void
  changed: boolean
  priorImages: { image: string; revision: number; active: boolean }[]
}) {
  const [open, setOpen] = useState(false)
  const wrapperRef = useRef<HTMLDivElement | null>(null)
  const popoverRef = useRef<HTMLDivElement | null>(null)
  const [pos, setPos] = useState<{ top: number; left: number; width: number } | null>(null)

  // Position the portalled popover under the input wrapper. Run
  // synchronously after layout (useLayoutEffect) so the popover
  // appears in the right spot on the same frame it opens — without
  // this, you see a one-frame flash at (0,0) before it lands.
  useLayoutEffect(() => {
    if (!open || !wrapperRef.current) return
    const update = () => {
      const rect = wrapperRef.current!.getBoundingClientRect()
      setPos({ top: rect.bottom + 4, left: rect.left, width: rect.width })
    }
    update()
    // Resize / scroll listeners keep the popover anchored if the
    // user resizes the window or the modal body scrolls. Use
    // capture phase on scroll so we catch scrolls in any ancestor.
    window.addEventListener('resize', update)
    window.addEventListener('scroll', update, true)
    return () => {
      window.removeEventListener('resize', update)
      window.removeEventListener('scroll', update, true)
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    function handleDocClick(e: MouseEvent) {
      const target = e.target as Node
      // Click is inside trigger OR inside popover → keep open. The
      // popover is portalled outside the wrapper, so we have to
      // check both refs explicitly.
      if (wrapperRef.current?.contains(target)) return
      if (popoverRef.current?.contains(target)) return
      setOpen(false)
    }
    function handleEsc(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', handleDocClick)
    document.addEventListener('keydown', handleEsc)
    return () => {
      document.removeEventListener('mousedown', handleDocClick)
      document.removeEventListener('keydown', handleEsc)
    }
  }, [open])

  const hasSuggestions = priorImages.length > 0

  return (
    <div className="relative flex-1" ref={wrapperRef}>
      <div className="flex items-stretch">
        <input
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className={`flex-1 px-2 py-1.5 text-[11px] font-mono bg-kb-bg border ${
            hasSuggestions ? 'rounded-l border-r-0' : 'rounded'
          } text-kb-text-primary focus:outline-none focus:border-kb-border-active ${
            changed ? 'border-status-info' : 'border-kb-border'
          }`}
          placeholder="registry/image:tag"
        />
        {hasSuggestions && (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            title="Previous images for this container"
            className={`px-1.5 border rounded-r ${
              changed ? 'border-status-info' : 'border-kb-border'
            } bg-kb-bg text-kb-text-tertiary hover:text-kb-text-primary hover:bg-kb-elevated`}
          >
            <ChevronDown className={`w-3 h-3 transition-transform ${open ? 'rotate-180' : ''}`} />
          </button>
        )}
      </div>
      {open && hasSuggestions && pos &&
        createPortal(
          <div
            ref={popoverRef}
            className="fixed bg-kb-card border border-kb-border rounded-lg shadow-xl overflow-hidden"
            style={{
              // z-index 100000 sits above the modal's 99999 backdrop
              // — without this the popover renders behind the modal.
              zIndex: 100000,
              top: pos.top,
              left: pos.left,
              width: pos.width,
            }}
          >
            <div className="px-3 py-2 bg-kb-elevated/50 text-[10px] uppercase tracking-wider text-kb-text-tertiary border-b border-kb-border">
              Previous images ({priorImages.length})
            </div>
            <ul className="max-h-64 overflow-y-auto">
              {priorImages.map((p) => (
                <li key={`${p.revision}-${p.image}`}>
                  <button
                    type="button"
                    onClick={() => {
                      onChange(p.image)
                      setOpen(false)
                    }}
                    className="w-full text-left px-3 py-2 text-[11px] font-mono hover:bg-kb-elevated flex items-center gap-2"
                  >
                    <span className="text-kb-text-tertiary shrink-0">rev {p.revision}</span>
                    <span className="text-kb-text-secondary break-all flex-1">{p.image}</span>
                  </button>
                </li>
              ))}
            </ul>
          </div>,
          document.body,
        )}
    </div>
  )
}
