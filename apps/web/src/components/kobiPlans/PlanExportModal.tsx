import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, Download, Info } from 'lucide-react'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { Modal } from '@/components/shared/Modal'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import type { PlanExportFormat } from '@/types/kobiPlans'

// PlanExportModal — the text a plan leaves KubeBolt as (plans-05, d84: a
// button first). Looking at it changes nothing; Copy or Download is the
// person taking it, so that records the export and the plan becomes
// exported. The text copied is the one on screen, written to the clipboard
// before the record goes out: a browser only lets the clipboard be written
// right after the click.

const FORMATS: { id: PlanExportFormat; label: string; hint: string }[] = [
  { id: 'commands', label: 'Commands', hint: 'kubectl and helm to run, with the rollback under each change' },
  { id: 'diff', label: 'Diff', hint: 'the patch each change applies, to review or to put in a repository' },
]

export function PlanExportModal({ planId, title, onClose }: { planId: string; title: string; onClose: () => void }) {
  const [format, setFormat] = useState<PlanExportFormat>('commands')
  const [taken, setTaken] = useState<'copied' | 'downloaded' | null>(null)
  const { hasRole } = useAuth()
  const canExport = hasRole('editor')
  const qc = useQueryClient()

  const preview = useQuery({
    queryKey: ['kobi-plan-export', planId, format],
    queryFn: () => api.previewPlanExport(planId, format),
  })
  const record = useMutation({
    mutationFn: () => api.exportPlan(planId, format),
    onSuccess: (res) => {
      if (res.plan) qc.setQueryData(['kobi-plan', planId], res.plan)
      qc.invalidateQueries({ queryKey: ['kobi-plans'] })
    },
  })

  // The confirmation goes away by itself (toasts last 6 s).
  useEffect(() => {
    if (!taken) return
    const t = setTimeout(() => setTaken(null), 6000)
    return () => clearTimeout(t)
  }, [taken])

  const text = preview.data?.export.text ?? ''
  const filename = preview.data?.export.filename ?? 'kobi-plan.txt'
  const notes = preview.data?.export.notes ?? []

  const copy = () => {
    navigator.clipboard.writeText(text).then(
      () => {
        setTaken('copied')
        record.mutate()
      },
      () => undefined,
    )
  }
  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    a.click()
    URL.revokeObjectURL(url)
    setTaken('downloaded')
    record.mutate()
  }

  return (
    <Modal badge="Export" title={title} onClose={onClose} size="2xl">
      <div className="flex-1 overflow-y-auto p-5 space-y-4">
        <div className="flex flex-wrap items-center gap-3">
          <div className="inline-flex rounded-lg border border-kb-border p-0.5" role="tablist" aria-label="Format">
            {FORMATS.map((f) => (
              <button
                key={f.id}
                type="button"
                role="tab"
                aria-selected={format === f.id}
                onClick={() => setFormat(f.id)}
                className={`px-3 py-1 rounded-md text-[12px] transition-colors ${
                  format === f.id ? 'bg-kb-elevated text-kb-text-primary' : 'text-kb-text-tertiary hover:text-kb-text-primary'
                }`}
              >
                {f.label}
              </button>
            ))}
          </div>
          <span className="text-[12px] text-kb-text-secondary">{FORMATS.find((f) => f.id === format)?.hint}</span>
        </div>

        {preview.isLoading ? (
          <LoadingSpinner />
        ) : preview.error ? (
          <ErrorState message="The export could not be prepared" onRetry={() => preview.refetch()} />
        ) : (
          <>
            {notes.length > 0 && (
              <ul className="space-y-1.5 rounded-xl border border-kb-border bg-kb-card px-4 py-3">
                {notes.map((n) => (
                  <li key={n} className="flex items-start gap-2 text-[12px] leading-5 text-kb-text-secondary">
                    <Info className="w-3.5 h-3.5 mt-0.5 shrink-0 text-kb-text-tertiary" />
                    {n}
                  </li>
                ))}
              </ul>
            )}
            <pre
              aria-label="Export text"
              className="max-h-[52vh] overflow-auto rounded-xl border border-kb-border bg-kb-bg px-4 py-3 text-[12px] leading-5 font-mono text-kb-text-primary whitespace-pre"
            >
              {/* Comments dim, so what runs (or applies) stands out. */}
              {text.split('\n').map((line, i, all) => (
                <span key={i} className={line.trimStart().startsWith('#') ? 'text-kb-text-tertiary' : undefined}>
                  {line}
                  {i < all.length - 1 ? '\n' : ''}
                </span>
              ))}
            </pre>
          </>
        )}
      </div>

      <div className="px-5 py-3 border-t border-kb-border flex flex-wrap items-center justify-between gap-3 shrink-0">
        <span className="text-[12px] text-kb-text-tertiary" aria-live="polite">
          {record.isError ? (
            <span className="text-status-error">
              The text was {taken ?? 'taken'}, but the export could not be recorded: {(record.error as Error).message}
            </span>
          ) : taken ? (
            <span className="inline-flex items-center gap-1.5 text-status-ok">
              <Check className="w-3.5 h-3.5" />
              {taken === 'copied' ? 'Copied' : 'Downloaded'}
              {record.isSuccess ? ' · the plan is exported' : ''}
            </span>
          ) : canExport ? (
            'Copying or downloading marks the plan as exported. Nothing is applied.'
          ) : (
            'Editors can export a plan.'
          )}
        </span>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={download}
            disabled={!canExport || !text}
            className="inline-flex items-center gap-1.5 h-9 px-3.5 rounded-lg border border-kb-border text-[13px] font-medium text-kb-text-primary hover:bg-kb-card-hover disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
          >
            <Download className="w-3.5 h-3.5" /> Download
          </button>
          <button
            type="button"
            onClick={copy}
            disabled={!canExport || !text}
            className="inline-flex items-center gap-1.5 h-9 px-3.5 rounded-lg bg-kb-accent text-white text-[13px] font-semibold hover:opacity-90 disabled:opacity-50 disabled:cursor-not-allowed transition-opacity"
          >
            <Copy className="w-3.5 h-3.5" /> Copy
          </button>
        </div>
      </div>
    </Modal>
  )
}
