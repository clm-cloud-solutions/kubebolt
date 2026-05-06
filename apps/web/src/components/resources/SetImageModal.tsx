import { useEffect, useMemo, useState } from 'react'
import { Image as ImageIcon, AlertTriangle, Info } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import { useQueryClient } from '@tanstack/react-query'
import type { ResourceItem } from '@/types/kubernetes'

// SetImageModal — UI for the `kubectl set image` equivalent.
//
// Layout:
//   - One row per container in the workload's pod template.
//   - The "Current" cell shows what's deployed today.
//   - The "New" input is pre-filled with the current image; operator
//     edits only the rows they want to change.
//   - We submit ALL containers in the request (not just the changed
//     ones) — the backend short-circuits to "unchanged" if every
//     image equals current. Sending unchanged rows alongside changed
//     ones makes the audit log unambiguous about what the operator
//     reviewed at decision time.
//
// We pull the container list from the resource detail's
// spec.template.spec.containers — same path the backend reads.

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
                          <input
                            type="text"
                            value={draft}
                            onChange={(e) =>
                              setDrafts({ ...drafts, [c.name]: e.target.value })
                            }
                            className={`flex-1 px-2 py-1.5 text-[11px] font-mono bg-kb-bg border rounded text-kb-text-primary focus:outline-none focus:border-kb-border-active ${
                              changed
                                ? 'border-status-info'
                                : 'border-kb-border'
                            }`}
                            placeholder="registry/image:tag"
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
