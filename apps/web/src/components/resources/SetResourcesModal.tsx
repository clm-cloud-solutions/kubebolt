import { useEffect, useMemo, useState } from 'react'
import { Cpu, AlertTriangle, Info } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import type { ContainerResourcesPatch, ResourceQuantityInput } from '@/services/api'
import { useQueryClient } from '@tanstack/react-query'
import type { ResourceItem } from '@/types/kubernetes'
import { RolloutStatusPanel } from './RolloutStatusPanel'

// SetResourcesModal — UI for the `kubectl set resources` equivalent.
//
// One row per container (just normal containers in v1; init containers
// get a v2 — most workloads with high-frequency right-sizing pressure
// are normal-container-only). Inside each row, a 2×2 grid of inputs:
// requests/limits × cpu/memory. Each input shows the current value
// inline below it so the operator never has to remember "what was it
// before."
//
// Empty input = leave that dimension alone (the v1 contract — see
// the file-level comment in actions_resources.go for the rationale on
// why "remove a dimension" is deferred to v2). Inputs are validated
// loosely client-side (must look like a quantity); the backend's
// resource.ParseQuantity is the strict gate.
//
// On successful submit, the modal switches to RolloutStatusPanel so
// the operator can watch the rolling update converge — same pattern
// as SetImageModal and RollbackModal.

interface ContainerResourceRow {
  name: string
  // current snapshot from the workload's pod template, formatted as
  // kubectl-style strings ("200m", "512Mi"). Empty string when the
  // container has no value set for that dimension.
  currentRequestCpu: string
  currentLimitCpu: string
  currentRequestMemory: string
  currentLimitMemory: string
}

interface ContainerDraft {
  requestCpu: string
  limitCpu: string
  requestMemory: string
  limitMemory: string
}

// formatCpuKubectl turns the backend's millicores integer into a
// kubectl-style string. We use the kubectl form (no decimals when not
// needed, no "cores" suffix) so the input value looks like what an
// operator typing into a YAML editor would write.
function formatCpuKubectl(millicores: number): string {
  if (!millicores || millicores <= 0) return ''
  if (millicores >= 1000 && millicores % 1000 === 0) return String(millicores / 1000)
  return `${millicores}m`
}

// formatMemoryKubectl picks the most-natural unit (Ki, Mi, Gi, Ti)
// that represents the byte count without losing precision. We prefer
// the IEC binary units that match how K8s emits them in YAML.
function formatMemoryKubectl(bytes: number): string {
  if (!bytes || bytes <= 0) return ''
  const units: [string, number][] = [
    ['Ti', 1024 ** 4],
    ['Gi', 1024 ** 3],
    ['Mi', 1024 ** 2],
    ['Ki', 1024],
  ]
  for (const [unit, divisor] of units) {
    if (bytes >= divisor && bytes % divisor === 0) {
      return `${bytes / divisor}${unit}`
    }
  }
  return String(bytes)
}

// quantityRE is a loose syntax check that catches the most common typos
// (e.g. "512mb" vs "512Mi", "0.5cpu") without trying to replicate
// resource.ParseQuantity. The backend's strict parse is the real gate;
// this just gives the operator a fast hint without a round-trip.
const quantityRE = /^(\d+(\.\d+)?)([numKMGTPE]i?)?$/

function isValidQuantity(s: string): boolean {
  if (s === '') return true // empty = leave alone, valid
  return quantityRE.test(s.trim())
}

function extractContainerRows(item: ResourceItem | undefined): ContainerResourceRow[] {
  if (!item) return []
  const cs = (item as unknown as { containers?: unknown }).containers
  if (!Array.isArray(cs)) return []
  return cs
    .map((c) => {
      if (typeof c !== 'object' || c === null) return null
      const obj = c as { name?: unknown; resources?: unknown }
      if (typeof obj.name !== 'string') return null
      const res = (obj.resources ?? {}) as {
        cpuRequest?: number
        cpuLimit?: number
        memoryRequest?: number
        memoryLimit?: number
      }
      return {
        name: obj.name,
        currentRequestCpu: formatCpuKubectl(Number(res.cpuRequest ?? 0)),
        currentLimitCpu: formatCpuKubectl(Number(res.cpuLimit ?? 0)),
        currentRequestMemory: formatMemoryKubectl(Number(res.memoryRequest ?? 0)),
        currentLimitMemory: formatMemoryKubectl(Number(res.memoryLimit ?? 0)),
      }
    })
    .filter((r): r is ContainerResourceRow => r !== null)
}

export function SetResourcesModal({
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
  const containers = useMemo(() => extractContainerRows(resource), [resource])
  const [drafts, setDrafts] = useState<Record<string, ContainerDraft>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applying, setApplying] = useState<{
    expectedGeneration: number
    submittedAtMs: number
  } | null>(null)

  // Initialize drafts pre-filled with current values so the operator
  // sees the existing state in the inputs and can edit only what they
  // want to change. Re-runs if the resource refreshes mid-modal so
  // the operator never edits a stale baseline.
  useEffect(() => {
    setDrafts((prev) => {
      const next = { ...prev }
      for (const c of containers) {
        if (next[c.name] === undefined) {
          next[c.name] = {
            requestCpu: c.currentRequestCpu,
            limitCpu: c.currentLimitCpu,
            requestMemory: c.currentRequestMemory,
            limitMemory: c.currentLimitMemory,
          }
        }
      }
      return next
    })
  }, [containers])

  // dirty = at least one input differs from its current value
  const dirty = useMemo(() => {
    return containers.some((c) => {
      const d = drafts[c.name]
      if (!d) return false
      return (
        d.requestCpu !== c.currentRequestCpu ||
        d.limitCpu !== c.currentLimitCpu ||
        d.requestMemory !== c.currentRequestMemory ||
        d.limitMemory !== c.currentLimitMemory
      )
    })
  }, [containers, drafts])

  // Aggregate validation errors so the Apply button stays disabled
  // until everything is sane. The detailed per-input red-text is
  // shown inline; this just gates submission.
  const invalidCount = useMemo(() => {
    let n = 0
    for (const c of containers) {
      const d = drafts[c.name]
      if (!d) continue
      if (!isValidQuantity(d.requestCpu)) n++
      if (!isValidQuantity(d.limitCpu)) n++
      if (!isValidQuantity(d.requestMemory)) n++
      if (!isValidQuantity(d.limitMemory)) n++
    }
    return n
  }, [containers, drafts])

  async function submit() {
    setBusy(true)
    setError(null)
    const preGen = ((resource as unknown as { generation?: number })?.generation ?? 0) + 1
    const submittedAtMs = Date.now()
    try {
      // Build the request body — only include containers that have at
      // least one dimension different from current. The backend treats
      // empty strings as "leave alone" so sending unchanged dims is
      // safe, but skipping unchanged containers keeps the audit log
      // tight and avoids a phantom set-resources entry for a no-op.
      const containersBody: ContainerResourcesPatch[] = []
      for (const c of containers) {
        const d = drafts[c.name]
        if (!d) continue
        const requests: ResourceQuantityInput = {}
        const limits: ResourceQuantityInput = {}
        if (d.requestCpu !== c.currentRequestCpu) requests.cpu = d.requestCpu
        if (d.requestMemory !== c.currentRequestMemory) requests.memory = d.requestMemory
        if (d.limitCpu !== c.currentLimitCpu) limits.cpu = d.limitCpu
        if (d.limitMemory !== c.currentLimitMemory) limits.memory = d.limitMemory
        const hasReq = requests.cpu !== undefined || requests.memory !== undefined
        const hasLim = limits.cpu !== undefined || limits.memory !== undefined
        if (!hasReq && !hasLim) continue
        const row: ContainerResourcesPatch = { container: c.name }
        if (hasReq) row.requests = requests
        if (hasLim) row.limits = limits
        containersBody.push(row)
      }

      if (containersBody.length === 0) {
        setError('No changes to apply')
        return
      }

      const res = await api.setResourcesResource(type, namespace, name, containersBody)
      if (res.resource) {
        queryClient.setQueryData(['resource-detail', type, namespace, name], res.resource)
      }
      queryClient.invalidateQueries({ queryKey: ['resources'] })
      // Switch to live progress — same pattern as SetImageModal so the
      // operator can watch the rolling update finish or catch a bad
      // patch (e.g. quota rejection) inside the same modal.
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
            <Cpu className="w-3 h-3" /> applying
          </span>
        }
        title={`Set resources · ${name}`}
        onClose={onClose}
        size="lg"
      >
        <div className="flex-1 overflow-y-auto px-5 py-4">
          <RolloutStatusPanel
            type={type}
            namespace={namespace}
            name={name}
            title="Applying new resource limits…"
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
          <Cpu className="w-3 h-3" /> set resources
        </span>
      }
      title={`Set resources · ${name}`}
      onClose={onClose}
      size="lg"
    >
      <div className="flex-1 overflow-y-auto px-5 py-4 space-y-4">
        <p className="text-xs text-kb-text-tertiary">
          Equivalent to{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl set resources {type === 'deployments' ? 'deploy' : type === 'statefulsets' ? 'sts' : 'ds'}/{name}
          </code>
          . Strategic merge patch — only the dimensions you change are touched. Leave a field blank or unchanged to skip it.
        </p>

        {containers.length === 0 ? (
          <div className="text-xs text-kb-text-tertiary border border-kb-border rounded-lg p-4 text-center">
            No containers detected on this workload's pod template.
          </div>
        ) : (
          <div className="space-y-4">
            {containers.map((c) => {
              const d = drafts[c.name]
              if (!d) return null
              return (
                <div key={c.name} className="border border-kb-border rounded-lg overflow-hidden">
                  <div className="px-3 py-2 bg-kb-elevated border-b border-kb-border">
                    <span className="font-mono text-[11px] text-kb-text-primary">{c.name}</span>
                  </div>
                  <table className="w-full text-xs">
                    <thead>
                      <tr className="text-left text-kb-text-tertiary uppercase tracking-wider text-[10px] border-b border-kb-border">
                        <th className="px-3 py-2 font-medium w-20"></th>
                        <th className="px-3 py-2 font-medium">CPU</th>
                        <th className="px-3 py-2 font-medium">Memory</th>
                      </tr>
                    </thead>
                    <tbody>
                      <ResourceInputRow
                        label="Request"
                        cpu={d.requestCpu}
                        memory={d.requestMemory}
                        currentCpu={c.currentRequestCpu}
                        currentMemory={c.currentRequestMemory}
                        onCpuChange={(v) => setDrafts({ ...drafts, [c.name]: { ...d, requestCpu: v } })}
                        onMemoryChange={(v) => setDrafts({ ...drafts, [c.name]: { ...d, requestMemory: v } })}
                      />
                      <ResourceInputRow
                        label="Limit"
                        cpu={d.limitCpu}
                        memory={d.limitMemory}
                        currentCpu={c.currentLimitCpu}
                        currentMemory={c.currentLimitMemory}
                        onCpuChange={(v) => setDrafts({ ...drafts, [c.name]: { ...d, limitCpu: v } })}
                        onMemoryChange={(v) => setDrafts({ ...drafts, [c.name]: { ...d, limitMemory: v } })}
                      />
                    </tbody>
                  </table>
                </div>
              )
            })}
          </div>
        )}

        <div className="flex items-start gap-2 text-[11px] text-kb-text-tertiary">
          <Info className="w-3.5 h-3.5 mt-0.5 shrink-0" />
          <div>
            CPU accepts <code className="font-mono">200m</code> (millicores), <code className="font-mono">0.5</code> (fractional cores), or <code className="font-mono">2</code> (cores).
            Memory uses IEC suffixes — <code className="font-mono">512Mi</code>, <code className="font-mono">2Gi</code>, etc.
            Removing a dimension entirely (clearing a request/limit you no longer want) needs the YAML editor in this version.
          </div>
        </div>

        {error && (
          <div className="flex items-center gap-2 text-xs text-status-error">
            <AlertTriangle className="w-3 h-3" />
            <span className="break-words">{error}</span>
          </div>
        )}

        {dirty && invalidCount === 0 && (
          <p className="text-[11px] text-kb-text-tertiary">
            Triggers a rolling update with the workload's current strategy.
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
          disabled={busy || !dirty || invalidCount > 0 || containers.length === 0}
          className="px-3 py-1.5 text-xs rounded bg-status-info-dim text-status-info hover:bg-status-info hover:text-kb-bg border border-status-info disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {busy ? 'Applying…' : 'Apply'}
        </button>
      </div>
    </Modal>
  )
}

function ResourceInputRow({
  label,
  cpu,
  memory,
  currentCpu,
  currentMemory,
  onCpuChange,
  onMemoryChange,
}: {
  label: 'Request' | 'Limit'
  cpu: string
  memory: string
  currentCpu: string
  currentMemory: string
  onCpuChange: (v: string) => void
  onMemoryChange: (v: string) => void
}) {
  const cpuChanged = cpu !== currentCpu
  const memChanged = memory !== currentMemory
  const cpuValid = isValidQuantity(cpu)
  const memValid = isValidQuantity(memory)
  return (
    <tr className="border-b border-kb-border last:border-b-0">
      <td className="px-3 py-2.5 text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
        {label}
      </td>
      <td className="px-3 py-2">
        <ResourceInput
          value={cpu}
          changed={cpuChanged}
          valid={cpuValid}
          placeholder={currentCpu || '—'}
          onChange={onCpuChange}
        />
        {currentCpu && cpu !== currentCpu && (
          <div className="text-[10px] font-mono text-kb-text-tertiary mt-1">
            current: {currentCpu}
          </div>
        )}
      </td>
      <td className="px-3 py-2">
        <ResourceInput
          value={memory}
          changed={memChanged}
          valid={memValid}
          placeholder={currentMemory || '—'}
          onChange={onMemoryChange}
        />
        {currentMemory && memory !== currentMemory && (
          <div className="text-[10px] font-mono text-kb-text-tertiary mt-1">
            current: {currentMemory}
          </div>
        )}
      </td>
    </tr>
  )
}

function ResourceInput({
  value,
  changed,
  valid,
  placeholder,
  onChange,
}: {
  value: string
  changed: boolean
  valid: boolean
  placeholder: string
  onChange: (v: string) => void
}) {
  const baseClasses =
    'w-full px-2 py-1 text-[11px] font-mono bg-kb-bg border rounded text-kb-text-primary outline-none focus:border-kb-border-active'
  const stateClasses = !valid
    ? 'border-status-error/50'
    : changed
    ? 'border-status-info/40 bg-status-info-dim/20'
    : 'border-kb-border'
  return (
    <input
      type="text"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      className={`${baseClasses} ${stateClasses}`}
    />
  )
}
