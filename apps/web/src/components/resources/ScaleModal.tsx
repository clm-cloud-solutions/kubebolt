import { useState } from 'react'
import { ArrowUpDown, AlertTriangle, RefreshCw } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api, ApiError } from '@/services/api'
import { useQueryClient } from '@tanstack/react-query'

// ScaleModal — dialog for `kubectl scale deploy/X --replicas=N`.
//
// Originally wired as an inline popover under a Scale button in the
// toolbar. Moved into a focused modal as part of the toolbar refactor
// — Scale lives in the Actions menu now, and triggering an inline
// popover from a menu item is awkward (the popover anchors to a
// button that no longer exists). The modal pattern matches Set
// resources / Set env / Set image, so the operator gets a consistent
// "open a focused dialog, fill the input, Apply" flow regardless of
// which write action they pick.

interface Props {
  type: 'deployments' | 'statefulsets'
  namespace: string
  name: string
  currentReplicas: number
  onClose: () => void
}

export function ScaleModal({ type, namespace, name, currentReplicas, onClose }: Props) {
  const queryClient = useQueryClient()
  const [replicas, setReplicas] = useState<number>(currentReplicas)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const dirty = replicas !== currentReplicas
  const friendlyType = type === 'deployments' ? 'Deployment' : 'StatefulSet'

  async function submit() {
    if (replicas < 0) {
      setError('Replicas must be 0 or greater')
      return
    }
    setBusy(true)
    setError(null)
    try {
      const res = await api.scaleResource(type, namespace, name, replicas)
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
          <ArrowUpDown className="w-3 h-3" /> scale
        </span>
      }
      title={`Scale · ${name}`}
      onClose={onClose}
      size="sm"
      unbounded
    >
      <div className="px-5 py-4 space-y-3">
        <p className="text-xs text-kb-text-tertiary">
          Equivalent to{' '}
          <code className="font-mono px-1 py-px rounded bg-kb-elevated text-kb-text-primary text-[11px]">
            kubectl scale {type === 'deployments' ? 'deploy' : 'sts'}/{name} --replicas={replicas}
          </code>
          . Adjusts <code className="font-mono">spec.replicas</code> on the {friendlyType}.
        </p>

        <div>
          <label className="block">
            <span className="text-[11px] uppercase tracking-wider text-kb-text-tertiary font-medium">
              Replicas
            </span>
            <input
              type="number"
              min="0"
              value={replicas}
              onChange={(e) => setReplicas(Math.max(0, parseInt(e.target.value) || 0))}
              autoFocus
              onKeyDown={(e) => {
                if (e.key === 'Enter' && dirty && !busy) submit()
              }}
              className="mt-1 w-full px-2 py-1.5 text-xs font-mono bg-kb-bg border border-kb-border rounded-md text-kb-text-primary outline-none focus:border-kb-border-active"
            />
            <div className="mt-1 text-[10px] font-mono text-kb-text-tertiary">
              Currently {currentReplicas} replica{currentReplicas === 1 ? '' : 's'}
            </div>
          </label>
        </div>

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
          disabled={busy || !dirty}
          className="px-3 py-1.5 text-xs rounded bg-status-info-dim text-status-info hover:bg-status-info hover:text-kb-bg border border-status-info disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-1.5"
        >
          {busy && <RefreshCw className="w-3 h-3 animate-spin" />}
          {busy ? 'Scaling…' : 'Scale'}
        </button>
      </div>
    </Modal>
  )
}
