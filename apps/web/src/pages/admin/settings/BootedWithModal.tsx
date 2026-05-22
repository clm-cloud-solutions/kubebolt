import { useQuery } from '@tanstack/react-query'
import { Eye, EyeOff, AlertTriangle } from 'lucide-react'
import { api } from '@/services/api'
import { Modal } from '@/components/shared/Modal'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'

// BootedWithModal — read-only diagnostic view answering "which Helm /
// env values did this container actually boot with?". Surfaced from
// the Settings page header. No editing — operators looking at this
// already have the Settings tabs for overrides.
//
// Sensitive entries (JWT secret, API keys, webhook URLs, SMTP password)
// show "(set)" with a strikethrough-eye icon rather than the cleartext;
// the masked-preview pattern used elsewhere isn't a fit here because
// this endpoint reports the raw env value, not a content-aware mask.

export function BootedWithModal({ onClose }: { onClose: () => void }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['admin', 'settings', 'booted-with'],
    queryFn: api.getBootedWith,
    // Boot snapshot doesn't change for the lifetime of the process —
    // cache aggressively, no refetch on focus.
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })

  return (
    <Modal badge="Boot" title="KUBEBOLT_* environment at boot" onClose={onClose} size="lg">
      <div className="flex-1 overflow-y-auto p-5 space-y-3">
        <p className="text-[11px] text-kb-text-tertiary leading-relaxed">
          Every <code className="font-mono text-kb-accent">KUBEBOLT_*</code> environment variable
          the process saw at start. Settings persisted via the tabs above OVERRIDE these
          values for fields they cover; the rest remain in effect from this snapshot. To
          change what's here, edit your Helm values (or container env) and restart.
        </p>

        {isLoading && (
          <div className="py-10 flex justify-center">
            <LoadingSpinner size="md" />
          </div>
        )}

        {error && (
          <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim text-status-error text-xs">
            <AlertTriangle className="w-4 h-4 mt-0.5 shrink-0" />
            <div>{(error as Error)?.message || 'Failed to read boot snapshot'}</div>
          </div>
        )}

        {data && data.env.length === 0 && (
          <div className="text-xs text-kb-text-tertiary italic">
            No KUBEBOLT_* env vars were set at boot. The process is running with full
            defaults — every Settings tab shows the baseline as if no Helm chart was
            applied.
          </div>
        )}

        {data && data.env.length > 0 && (
          <div className="border border-kb-border rounded-lg overflow-hidden">
            <table className="w-full text-[11px]">
              <thead className="bg-kb-elevated">
                <tr>
                  <th className="px-3 py-2 text-left font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                    Name
                  </th>
                  <th className="px-3 py-2 text-left font-mono font-semibold text-kb-text-tertiary uppercase tracking-wider text-[10px]">
                    Value
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-kb-border">
                {data.env.map((e) => (
                  <tr key={e.name} className="hover:bg-kb-card-hover">
                    <td className="px-3 py-1.5 font-mono text-kb-text-primary whitespace-nowrap">
                      {e.name}
                    </td>
                    <td className="px-3 py-1.5 font-mono text-kb-text-secondary break-all">
                      {e.sensitive ? (
                        <span className="inline-flex items-center gap-1.5 text-kb-text-tertiary">
                          <EyeOff className="w-3 h-3" />
                          (set, redacted)
                        </span>
                      ) : (
                        <span className="inline-flex items-center gap-1.5">
                          <Eye className="w-3 h-3 text-kb-text-tertiary" />
                          {e.value === '' ? <span className="italic text-kb-text-tertiary">(empty)</span> : e.value}
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <div className="px-3 py-2 border-t border-kb-border bg-kb-elevated text-[10px] font-mono text-kb-text-tertiary text-right">
              {data.count} variable{data.count === 1 ? '' : 's'}
            </div>
          </div>
        )}
      </div>
    </Modal>
  )
}
