import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { Modal } from '@/components/shared/Modal'

// Discard (plans-06, P21) ends a live plan: it stops being live, so a new
// plan can cover its workloads (on Free, the cluster gets its place back).
// Nothing changes in the cluster. Editors only, confirmed first.
export function DiscardPlanButton({ planId, title, className }: { planId: string; title: string; className: string }) {
  const { hasRole } = useAuth()
  const [open, setOpen] = useState(false)
  const qc = useQueryClient()
  const discard = useMutation({
    mutationFn: () => api.discardPlan(planId),
    onSuccess: (plan) => {
      qc.setQueryData(['kobi-plan', planId], plan)
      qc.invalidateQueries({ queryKey: ['kobi-plans'] })
      setOpen(false)
    },
  })
  if (!hasRole('editor')) return null
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} className={className}>
        Discard
      </button>
      {open && (
        <Modal badge="Plan" title={`Discard «${title}»?`} onClose={() => setOpen(false)} size="sm" unbounded>
          <div className="p-5 space-y-3 text-[13px] leading-5 text-kb-text-secondary">
            <p>The plan stops being live, so a new plan can cover its workloads.</p>
            <p>Nothing changes in the cluster: steps already applied stay applied, and the plan keeps its history.</p>
            {discard.error && (
              <p className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{(discard.error as Error).message}</p>
            )}
          </div>
          <div className="px-5 py-4 flex gap-2 justify-end border-t border-kb-border">
            <button
              type="button"
              onClick={() => setOpen(false)}
              className="px-4 py-2 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => discard.mutate()}
              disabled={discard.isPending}
              className="px-4 py-2 text-xs font-medium bg-status-error text-white rounded-lg hover:bg-status-error/90 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {discard.isPending ? 'Discarding…' : 'Discard plan'}
            </button>
          </div>
        </Modal>
      )}
    </>
  )
}
