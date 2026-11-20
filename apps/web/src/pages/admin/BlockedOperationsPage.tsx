import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Ban, Loader2, Save, ShieldAlert } from 'lucide-react'
import { api, ApiError, type BlockedOperationsView } from '@/services/api'
import { CloudProviderIcon } from '@/components/shared/CloudProviderIcon'

// Blocked operations (debt-09) — the emergency list of cloud connector
// operations no call may run, applied before each connector's own reviewed
// list on the next call. It fails closed: if KubeBolt cannot read this list it
// refuses every cloud call until it can.

const HOW: Record<string, string> = {
  aws: 'service.operation, e.g. s3.getobject · s3.* blocks a whole service',
  azure: 'the command name, e.g. storage_account_get · storage_* blocks a group',
  gcp: 'the tool name, e.g. list_log_entries · list_* blocks a group',
}

const parse = (text: string) =>
  text
    .split('\n')
    .map((l) => l.trim().toLowerCase())
    .filter(Boolean)

export function summarize(entries: string[]): string {
  if (entries.includes('*')) return 'paused: every call is refused'
  if (entries.length === 0) return 'nothing blocked'
  return `${entries.length} blocked`
}

export function BlockedOperationsPage() {
  const queryClient = useQueryClient()
  const { data, error: loadError } = useQuery({
    queryKey: ['platform', 'settings', 'blocked-operations'],
    queryFn: () => api.getBlockedOperations(),
  })
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [error, setError] = useState<{ field?: string; message: string } | null>(null)
  const [saved, setSaved] = useState(false)

  const stored = (v: BlockedOperationsView | undefined, id: string) => v?.connectors[id] ?? []
  useEffect(() => {
    if (!data) return
    setDrafts(Object.fromEntries(data.available.map((c) => [c.id, stored(data, c.id).join('\n')])))
  }, [data])

  const changed = useMemo(() => {
    if (!data) return {}
    const out: Record<string, string[]> = {}
    for (const c of data.available) {
      const next = parse(drafts[c.id] ?? '')
      if (next.join('\n') !== stored(data, c.id).join('\n')) out[c.id] = next
    }
    return out
  }, [data, drafts])
  const dirty = Object.keys(changed).length > 0

  const save = useMutation({
    mutationFn: () => api.putBlockedOperations({ connectors: changed }),
    onSuccess: (view) => {
      setError(null)
      setSaved(true)
      setTimeout(() => setSaved(false), 6000)
      queryClient.setQueryData(['platform', 'settings', 'blocked-operations'], view)
    },
    onError: (e) => {
      const p = e instanceof ApiError ? (e.payload as { field?: string; message?: string } | undefined) : undefined
      setError({ field: p?.field, message: p?.message || (e instanceof Error ? e.message : 'Failed to save') })
    },
  })

  if (loadError) {
    return (
      <p className="text-xs text-status-error">
        {loadError instanceof Error ? loadError.message : 'The blocked operations cannot be read.'}
      </p>
    )
  }
  if (!data) return <Loader2 className="w-4 h-4 animate-spin text-kb-text-tertiary" />

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2.5 rounded-lg border border-kb-border bg-kb-card px-3.5 py-3 text-xs text-kb-text-secondary">
        <ShieldAlert className="w-4 h-4 mt-0.5 shrink-0 text-status-warn" />
        <p>
          An emergency switch. A blocked operation is refused before the connector&apos;s own reviewed list and before
          it reaches the cloud, starting with Kobi&apos;s next call.
          Use <code className="font-mono">*</code> to pause a connector entirely. If KubeBolt cannot read this list,
          it refuses every cloud call until it can.
        </p>
      </div>

      {data.available.length === 0 && (
        <p className="text-xs text-kb-text-tertiary">This install has no cloud connectors.</p>
      )}

      {data.available.map((c) => {
        const entries = stored(data, c.id)
        const fieldError = error?.field === `connectors.${c.id}` ? error.message : null
        return (
          <Card
            key={c.id}
            icon={<CloudProviderIcon provider={c.id} className="w-4 h-4" />}
            title={c.name}
            subtitle={HOW[c.id] ?? 'one operation per line'}
            headerRight={
              <span
                className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-mono ${
                  entries.includes('*') ? 'bg-status-warn-dim text-status-warn' : 'bg-kb-elevated text-kb-text-secondary'
                }`}
              >
                <Ban className="w-3 h-3" />
                {summarize(entries)}
              </span>
            }
          >
            <label className="block space-y-1.5">
              <span className="sr-only">Blocked {c.name} operations, one per line</span>
              <textarea
                id={`blocked-${c.id}`}
                rows={Math.min(8, Math.max(3, parse(drafts[c.id] ?? '').length + 1))}
                spellCheck={false}
                placeholder="one operation per line"
                value={drafts[c.id] ?? ''}
                onChange={(e) => setDrafts((d) => ({ ...d, [c.id]: e.target.value }))}
                className={`w-full px-2.5 py-2 rounded-md bg-kb-bg border text-xs font-mono text-kb-text-primary focus:outline-none focus:border-kb-accent ${
                  fieldError ? 'border-status-error' : 'border-kb-border'
                }`}
              />
              {fieldError && <span className="block text-[11px] text-status-error">{fieldError}</span>}
            </label>
          </Card>
        )
      })}

      {data.available.length > 0 && (
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={() => save.mutate()}
            disabled={!dirty || save.isPending}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium bg-kb-accent text-white hover:opacity-90 disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {save.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Save className="w-3.5 h-3.5" />}
            Save
          </button>
          {saved && <span className="text-[11px] text-status-ok">Saved. It applies from the next call.</span>}
          {error && !error.field && <span className="text-[11px] text-status-error">{error.message}</span>}
        </div>
      )}
    </div>
  )
}

function Card({
  icon,
  title,
  subtitle,
  headerRight,
  children,
}: {
  icon: ReactNode
  title: string
  subtitle: string
  headerRight: ReactNode
  children: ReactNode
}) {
  return (
    <section className="rounded-lg border border-kb-border bg-kb-card p-4 space-y-3">
      <header className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-2 min-w-0">
          <span className="mt-0.5 shrink-0">{icon}</span>
          <div className="min-w-0">
            <h3 className="text-sm font-medium text-kb-text-primary">{title}</h3>
            <p className="text-[11px] font-mono text-kb-text-tertiary">{subtitle}</p>
          </div>
        </div>
        <div className="shrink-0">{headerRight}</div>
      </header>
      {children}
    </section>
  )
}
