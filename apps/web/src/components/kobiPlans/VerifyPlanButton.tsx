import { useCallback, useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, RefreshCw, X } from 'lucide-react'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import type { KobiPlanView } from '@/types/kobiPlans'

// Verify now (plans-07): the hourly sweep's checks, for one exported plan,
// now. It changes nothing in the cluster: it reads what the workloads show
// and writes it into the plan. Editors only.
//
// The check answers in milliseconds, and often changes nothing (the step is
// still to do): the button stays on «Verifying…» long enough to be seen and a
// toast says what the check found, or that nothing changed. Without them a
// click read as a dead button (26-nov).

// What the button waits at least, so «Verifying…» is seen. None in tests.
const MIN_VISIBLE_MS = import.meta.env.MODE === 'test' ? 0 : 600

export function VerifyPlanButton({ planId, className, label = 'Verify now' }: { planId: string; className: string; label?: string }) {
  const { hasRole } = useAuth()
  const qc = useQueryClient()
  const [found, setFound] = useState<string[] | null>(null)
  const verify = useMutation({
    mutationFn: async () => {
      const [plan] = await Promise.all([api.verifyPlan(planId), new Promise((r) => setTimeout(r, MIN_VISIBLE_MS))])
      return plan
    },
    onMutate: () => {
      setFound(null)
      return { before: qc.getQueryData<KobiPlanView>(['kobi-plan', planId]) }
    },
    onSuccess: (plan, _v, ctx) => {
      setFound(newNotes(ctx?.before, plan))
      qc.setQueryData(['kobi-plan', planId], plan)
      qc.invalidateQueries({ queryKey: ['kobi-plans'] })
    },
  })
  const dismiss = useCallback(() => setFound(null), [])
  if (!hasRole('editor')) return null
  return (
    <span className="inline-flex flex-col items-start gap-1">
      <button type="button" onClick={() => verify.mutate()} disabled={verify.isPending} className={className}>
        <RefreshCw className={`w-3.5 h-3.5 ${verify.isPending ? 'animate-spin' : ''}`} aria-hidden /> {verify.isPending ? 'Verifying…' : label}
      </button>
      {verify.error && <span className="text-[11px] text-status-error max-w-[16rem]">{(verify.error as Error).message}</span>}
      {found && <VerifiedToast notes={found} onDismiss={dismiss} />}
    </span>
  )
}

// newNotes: what this check wrote into the plan (the notes it added), in its
// own words.
function newNotes(before: KobiPlanView | undefined, after: KobiPlanView): string[] {
  const had = before?.notes.length ?? after.notes.length
  return after.notes.slice(had).map((n) => n.text)
}

// VerifiedToast: the same anatomy and the same 6 s as MutationErrorToast, for
// what the check found.
function VerifiedToast({ notes, onDismiss }: { notes: string[]; onDismiss: () => void }) {
  useEffect(() => {
    const t = setTimeout(onDismiss, 6_000)
    return () => clearTimeout(t)
  }, [onDismiss])
  return (
    <div role="status" className="fixed bottom-6 right-6 z-[300] max-w-md w-[420px] bg-kb-card border border-kb-border rounded-lg shadow-2xl">
      <div className="flex items-start gap-3 p-4">
        <CheckCircle2 className="w-5 h-5 text-status-ok shrink-0 mt-0.5" aria-hidden />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-semibold text-kb-text-primary">Checked now</div>
          {notes.length > 0 ? (
            <ul className="mt-1 space-y-1 text-xs text-kb-text-secondary leading-relaxed">
              {notes.map((n, i) => (
                <li key={i}>{n}</li>
              ))}
            </ul>
          ) : (
            <div className="mt-1 text-xs text-kb-text-secondary leading-relaxed">
              Nothing changed since the last check: what the plan suggests is still to do, or still being watched.
            </div>
          )}
        </div>
        <button type="button" onClick={onDismiss} className="text-kb-text-tertiary hover:text-kb-text-primary transition-colors" aria-label="Dismiss">
          <X className="w-4 h-4" />
        </button>
      </div>
    </div>
  )
}
