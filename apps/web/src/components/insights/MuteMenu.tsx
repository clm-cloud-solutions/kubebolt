import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { BellOff } from 'lucide-react'
import { api } from '@/services/api'
import type { Insight } from '@/types/kubernetes'
import { MutationErrorToast } from '@/components/shared/MutationErrorToast'

// MuteMenu — «Silenciar recurso» from the insight card (#54). Expiration is
// MANDATORY by design: 7d · 30d · until it resolves · permanent WITH a
// written reason ("debe doler un poco pedirla" — the friction is the
// feature, not an accident). The backend enforces the same contract; this
// menu just makes the honest paths one click and the permanent one a
// deliberate act.

const WEEK = 7 * 24 * 3600_000
const MONTH = 30 * 24 * 3600_000

export function MuteMenu({ insight }: { insight: Insight }) {
  const [open, setOpen] = useState(false)
  const [permanent, setPermanent] = useState(false)
  const [reason, setReason] = useState('')
  const [error, setError] = useState<unknown>(null)
  const queryClient = useQueryClient()

  const create = useMutation({
    mutationFn: (body: { expiresAt?: string; untilResolved?: boolean; reason?: string }) =>
      api.createInsightMute({
        ruleId: insight.ruleId ?? '',
        resource: insight.resource,
        ...body,
      }),
    onSuccess: () => {
      setOpen(false)
      setPermanent(false)
      setReason('')
      // The card disappearing from the default view IS the feedback.
      void queryClient.invalidateQueries({ queryKey: ['insight-mutes'] })
    },
    onError: (e) => setError(e),
  })

  if (!insight.ruleId) return null // legacy payload without identity — nothing to key on

  return (
    <div className="relative">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="px-2.5 py-1 rounded-md border border-kb-border text-[10.5px] font-mono text-kb-text-secondary hover:text-kb-text-primary hover:border-kb-border-active transition-colors inline-flex items-center gap-1.5"
      >
        <BellOff className="w-3 h-3" />
        Silence
      </button>
      {open && (
        <>
          {/* Backdrop closes the menu without silencing anything. */}
          <div className="fixed inset-0 z-10" onClick={() => setOpen(false)} aria-hidden />
          <div className="absolute z-20 mt-1 left-0 w-56 bg-kb-card border border-kb-border rounded-lg shadow-lg p-1">
            <div className="px-2 py-1 text-[9px] font-mono uppercase tracking-[0.14em] text-kb-text-tertiary">
              Silence this resource
            </div>
            {(
              [
                { label: 'For 7 days', body: { expiresAt: new Date(Date.now() + WEEK).toISOString() } },
                { label: 'For 30 days', body: { expiresAt: new Date(Date.now() + MONTH).toISOString() } },
                { label: 'Until it resolves', body: { untilResolved: true } },
              ] as const
            ).map((opt) => (
              <button
                key={opt.label}
                type="button"
                disabled={create.isPending}
                onClick={() => create.mutate(opt.body)}
                className="w-full text-left px-2 py-1.5 rounded-md text-[11px] text-kb-text-secondary hover:bg-kb-card-hover hover:text-kb-text-primary transition-colors disabled:opacity-50"
              >
                {opt.label}
              </button>
            ))}
            {!permanent ? (
              <button
                type="button"
                onClick={() => setPermanent(true)}
                className="w-full text-left px-2 py-1.5 rounded-md text-[11px] text-kb-text-secondary hover:bg-kb-card-hover hover:text-kb-text-primary transition-colors"
              >
                Permanently…
              </button>
            ) : (
              <div className="px-2 py-1.5 space-y-1.5">
                <input
                  autoFocus
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && reason.trim()) create.mutate({ reason: reason.trim() })
                  }}
                  placeholder="Why, for whoever finds it later"
                  className="w-full bg-kb-elevated border border-kb-border rounded px-2 py-1 text-[11px] text-kb-text-primary placeholder:text-kb-text-tertiary focus:outline-none focus:border-kb-border-active"
                />
                <button
                  type="button"
                  disabled={!reason.trim() || create.isPending}
                  onClick={() => create.mutate({ reason: reason.trim() })}
                  className="w-full px-2 py-1 rounded-md bg-kb-elevated text-[11px] font-mono text-kb-text-primary hover:bg-kb-card-hover disabled:opacity-40 transition-colors"
                >
                  Silence permanently
                </button>
              </div>
            )}
          </div>
        </>
      )}
      {error != null && (
        <MutationErrorToast error={error} action="Silence" onDismiss={() => setError(null)} />
      )}
    </div>
  )
}
