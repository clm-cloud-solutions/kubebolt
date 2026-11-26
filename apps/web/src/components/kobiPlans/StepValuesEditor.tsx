import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/services/api'
import type { PlanStep } from '@/types/kobiPlans'
import { editableValues, valueRange, valuesBody } from '@/utils/kobiPlans'

// StepValuesEditor: a person sets a pending step's values (plans-06, d85).
// Only what the step already changes, each with the engine's value and the
// floor the plan holds to (the per-pod P95). The server decides: a value
// under the floor comes back refused with its figure, and the refusal stays
// in the plan's notes.
export function StepValuesEditor({ planId, step, onDone }: { planId: string; step: PlanStep; onDone: () => void }) {
  const rows = editableValues(step)
  const [edits, setEdits] = useState<Record<string, string>>({})
  const qc = useQueryClient()
  const save = useMutation({
    mutationFn: (body: ReturnType<typeof valuesBody>) => api.reviseStep(planId, step.id, body),
    onSuccess: (plan) => {
      qc.setQueryData(['kobi-plan', planId], plan)
      qc.invalidateQueries({ queryKey: ['kobi-plans'] })
      onDone()
    },
    onError: () => {
      // A refusal is written into the plan's notes: read them again.
      qc.invalidateQueries({ queryKey: ['kobi-plan', planId] })
    },
  })
  const engineValues = Object.fromEntries(rows.filter((r) => r.engine).map((r) => [r.key, r.engine as string]))

  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate(valuesBody(rows, edits))
      }}
    >
      {rows.map((r) => (
        <label key={r.key} className="flex flex-wrap items-center gap-2 text-[13px]">
          <span className="w-28 font-mono text-kb-text-secondary">
            {r.res} {r.field === 'requests' ? 'request' : 'limit'}
          </span>
          <input
            aria-label={`${r.res} ${r.field === 'requests' ? 'request' : 'limit'}`}
            value={edits[r.key] ?? r.value}
            onChange={(e) => setEdits((v) => ({ ...v, [r.key]: e.target.value }))}
            className="w-24 h-8 px-2 rounded-md border border-kb-border bg-kb-card font-mono text-[13px] text-kb-text-primary focus:outline-none focus:border-kb-accent"
          />
          <span className="text-[12px] text-kb-text-tertiary">
            {[r.engine && `engine ${r.engine}`, valueRange(r)].filter(Boolean).join(' · ')}
          </span>
        </label>
      ))}
      {save.error && (
        <div className="px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">{(save.error as Error).message}</div>
      )}
      <div className="flex flex-wrap items-center gap-2 pt-1">
        <button
          type="submit"
          disabled={save.isPending}
          className="h-8 px-3 rounded-lg bg-kb-accent text-white text-[12px] font-semibold hover:opacity-90 disabled:opacity-50 transition-opacity"
        >
          {save.isPending ? 'Saving…' : 'Save values'}
        </button>
        <button
          type="button"
          onClick={onDone}
          className="h-8 px-3 rounded-lg border border-kb-border text-[12px] text-kb-text-secondary hover:bg-kb-elevated transition-colors"
        >
          Cancel
        </button>
        {step.imposed && Object.keys(engineValues).length > 0 && (
          <button
            type="button"
            onClick={() => setEdits(engineValues)}
            className="h-8 px-2 text-[12px] text-kb-text-secondary hover:text-kb-text-primary underline-offset-2 hover:underline"
          >
            Use the engine&apos;s values
          </button>
        )}
      </div>
      <div className="text-[12px] text-kb-text-tertiary">
        Saving makes a new version of the plan. A value under the engine&apos;s is taken at high risk; one outside the range is refused.
      </div>
    </form>
  )
}
