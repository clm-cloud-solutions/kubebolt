import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft, ChevronRight, Wrench } from 'lucide-react'
import { Modal } from '@/components/shared/Modal'
import { api } from '@/services/api'
import { FindingDetailBody } from '@/components/security/FindingDetailModal'

// WorkloadFindingsModal — everything wrong with one workload, then one finding.
//
// TWO LEVELS IN ONE DIALOG, not two stacked modals. A modal opened on top of a
// modal is hard to escape and loses the context the operator came from; here the
// content is replaced and a back link returns to the list. Same depth of
// information, one surface.
//
// The list is the workload's findings, because the workload is the unit of work:
// its 47 CVEs are closed by rebuilding one image, and the operator wants to see
// them together before deciding.

type Selected = {
  fingerprint: string
  clusterId: string
  kind: string
  source: string
  severity: string
  title: string
  status: string
  resourceKind?: string
  resourceNamespace?: string
  resourceName?: string
  cisControl?: string
  remediation?: string
  firstSeen: string
  lastSeen: string
}

const SEV_PILL: Record<string, string> = {
  critical: 'bg-status-error-dim text-status-error',
  high: 'bg-status-warn-dim text-status-warn',
  medium: 'bg-status-info/15 text-status-info',
  low: 'bg-kb-elevated text-kb-text-tertiary',
}

const SEV_RANK: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3 }

export function WorkloadFindingsModal({
  workload,
  group,
  cluster,
  onClose,
}: {
  workload: { kind?: string; namespace?: string; name: string }
  group: 'vulnerability' | 'configuration' | 'rbac' | 'compliance'
  cluster: string
  onClose: () => void
}) {
  const [selected, setSelected] = useState<Selected | null>(null)

  // Narrowed SERVER-side. The first cut asked for page 1 and filtered in the
  // browser, which quietly asked "are this workload's findings among the 25 I
  // happen to be holding?" — for every workload but the first, no. A row saying
  // 20 findings opened onto "nothing here".
  //
  // One workload's set is small, so a single generous page is enough and a
  // second paginated list inside a dialog would be more machinery than the
  // problem deserves.
  const { data, isLoading } = useQuery({
    queryKey: ['workload-findings', workload.namespace, workload.name, group, cluster],
    queryFn: () =>
      api.listFindings({
        group,
        cluster: cluster || undefined,
        resourceName: workload.name,
        resourceNamespace: workload.namespace,
        pageSize: 200,
      }),
    staleTime: 30_000,
  })

  const rows = (data?.findings ?? []).sort(
    (a, b) => (SEV_RANK[a.severity] ?? 9) - (SEV_RANK[b.severity] ?? 9),
  )

  const title = selected ? selected.title.split(':')[0] : workload.name
  const badge = selected ? selected.severity : workload.kind || 'workload'

  return (
    <Modal
      badge={badge}
      badgeClass={selected ? (SEV_PILL[selected.severity] ?? SEV_PILL.low) : undefined}
      title={title}
      onClose={onClose}
      size="2xl"
    >
      {selected ? (
        <div className="flex-1 overflow-y-auto">
          <button
            type="button"
            onClick={() => setSelected(null)}
            className="flex items-center gap-1.5 px-5 pt-4 text-[11px] text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
          >
            <ArrowLeft className="w-3.5 h-3.5" />
            All findings on {workload.name}
          </button>
          <FindingDetailBody finding={selected} />
        </div>
      ) : (
        <div className="flex-1 overflow-y-auto">
          <div className="px-5 py-3 border-b border-kb-border text-[11px] font-mono text-kb-text-tertiary">
            {workload.kind}
            {workload.namespace ? ` · ${workload.namespace}` : ''}
            {rows.length > 0 && ` · ${rows.length} finding${rows.length === 1 ? '' : 's'}`}
          </div>
          {isLoading ? (
            <p className="px-5 py-6 text-xs text-kb-text-tertiary">Loading…</p>
          ) : rows.length === 0 ? (
            <p className="px-5 py-6 text-xs text-kb-text-tertiary">
              Nothing on this workload in the current view — it may sit under another tab.
            </p>
          ) : (
            <div className="divide-y divide-kb-border">
              {rows.map((f) => (
                <button
                  key={f.fingerprint}
                  type="button"
                  onClick={() => setSelected(f as Selected)}
                  className="w-full flex items-center gap-3 px-5 py-2.5 text-left hover:bg-kb-card-hover transition-colors"
                >
                  <span
                    className={`shrink-0 px-1.5 py-0.5 rounded text-[9px] font-mono font-bold uppercase ${
                      SEV_PILL[f.severity] ?? SEV_PILL.low
                    }`}
                  >
                    {f.severity}
                  </span>
                  <span className="min-w-0 flex-1 text-xs text-kb-text-primary truncate" title={f.title}>
                    {f.title}
                  </span>
                  {f.remediation && (
                    <Wrench className="w-3.5 h-3.5 shrink-0 text-kb-accent" aria-label="has a fix" />
                  )}
                  <ChevronRight className="w-3.5 h-3.5 shrink-0 text-kb-text-tertiary" />
                </button>
              ))}
            </div>
          )}
        </div>
      )}
    </Modal>
  )
}
