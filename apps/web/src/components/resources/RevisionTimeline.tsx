import { useState } from 'react'
import { RotateCcw, GitCompare } from 'lucide-react'
import { Link } from 'react-router-dom'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { useDeploymentHistory, useWorkloadHistory } from '@/hooks/useResources'
import { formatAge } from '@/utils/formatters'
import { StatusBadge } from './StatusBadge'
import { RevisionDiffModal } from './RevisionDiffModal'

// Shared "View Diff" cell button — opens the revision YAML diff modal.
function ViewDiffButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      title="View YAML diff vs previous revision"
      className="px-2 py-1 text-[11px] rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors inline-flex items-center gap-1"
    >
      <GitCompare className="w-3 h-3" />
      View Diff
    </button>
  )
}

// ResourceLink is duplicated here from ResourceDetailPage's local
// (un-exported) helper. Same one-line component as the original; we
// inline it instead of exporting because exporting would force a
// surface change on a file that's already large and shipped.
function ResourceLink({ name, namespace, resourceType }: { name: string; namespace?: string; resourceType: string }) {
  return (
    <Link to={`/${resourceType}/${namespace || '_'}/${name}`} className="text-status-info hover:underline font-mono text-[11px]">
      {name}
    </Link>
  )
}

// RevisionTimeline preserves the original History tab layout
// (HistoryTab + WorkloadHistoryTab in ResourceDetailPage.tsx) and
// adds one thing: a per-row Rollback action. Visual layout, columns,
// fonts, badges, and empty-state copy are intentionally unchanged.
//
// Cut 4 will plug the RollbackModal into onRollback. Until then,
// onRollback opens a placeholder modal-state in the parent — the
// button is rendered but the action is staged.
//
// For Deployments the data source is the legacy ReplicaSet-derived
// history endpoint (via useDeploymentHistory). For STS/DS it's the
// ControllerRevision-derived endpoint (via useWorkloadHistory).
// Both already return the fields the original tables rendered;
// switching to the new ?detailed=true endpoint here would force
// design changes (different field names, no readyReplicas), which
// is the opposite of what's wanted in this cut.

interface Props {
  type: 'deployments' | 'statefulsets' | 'daemonsets'
  namespace: string
  name: string
  onRollback?: (revision: number) => void
  canEdit?: boolean
}

export function RevisionTimeline({ type, namespace, name, onRollback, canEdit }: Props) {
  if (type === 'deployments') {
    return (
      <DeploymentHistoryTable
        namespace={namespace}
        name={name}
        onRollback={onRollback}
        canEdit={!!canEdit}
      />
    )
  }
  return (
    <WorkloadHistoryTable
      type={type}
      namespace={namespace}
      name={name}
      onRollback={onRollback}
      canEdit={!!canEdit}
    />
  )
}

function DeploymentHistoryTable({
  namespace,
  name,
  onRollback,
  canEdit,
}: {
  namespace: string
  name: string
  onRollback?: (revision: number) => void
  canEdit: boolean
}) {
  const { data, isLoading, error } = useDeploymentHistory(namespace, name)
  const [diffRevision, setDiffRevision] = useState<number | null>(null)
  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const items = data?.items ?? []
  if (items.length === 0) {
    return <div className="text-sm text-kb-text-tertiary text-center py-12">No revision history found</div>
  }

  return (
    <Section title="Revision History">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Revision</th>
            <th className="pb-2 font-normal">ReplicaSet</th>
            <th className="pb-2 font-normal">Image</th>
            <th className="pb-2 font-normal">Replicas</th>
            <th className="pb-2 font-normal">Status</th>
            <th className="pb-2 font-normal">Created</th>
            <th className="pb-2 font-normal text-right">Actions</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {items.map((item, i) => {
            const replicas = Number(item.replicas ?? 0)
            const readyReplicas = Number(item.readyReplicas ?? 0)
            const isActive = replicas > 0
            const revisionNum = Number(item.revision ?? i + 1)
            return (
              <tr key={i} className={`border-t border-kb-border ${isActive ? 'bg-status-ok/5' : ''}`}>
                <td className="py-2">
                  <span className="font-mono">{String(item.revision ?? i + 1)}</span>
                  {isActive && (
                    <span className="ml-2 px-1.5 py-0.5 rounded text-[9px] font-medium bg-status-ok/20 text-status-ok">Active</span>
                  )}
                </td>
                <td className="py-2">
                  <ResourceLink name={item.name} namespace={item.namespace} resourceType="replicasets" />
                </td>
                <td className="py-2 font-mono text-kb-text-tertiary max-w-xs truncate">{String(item.image ?? '-')}</td>
                <td className="py-2 font-mono">{readyReplicas}/{replicas}</td>
                <td className="py-2">
                  <StatusBadge status={isActive ? 'Running' : 'Terminated'} label={isActive ? 'Active' : 'Scaled down'} />
                </td>
                <td className="py-2 font-mono text-kb-text-tertiary">{item.createdAt ? formatAge(item.createdAt) : '-'}</td>
                <td className="py-2 text-right">
                  <div className="inline-flex items-center gap-1.5">
                    {Number.isFinite(revisionNum) && revisionNum > 0 && (
                      <ViewDiffButton onClick={() => setDiffRevision(revisionNum)} />
                    )}
                    {!isActive && onRollback && Number.isFinite(revisionNum) && revisionNum > 0 && (
                      <button
                        onClick={() => onRollback(revisionNum)}
                        disabled={!canEdit}
                        title={!canEdit ? 'Editor role required' : `Rollback to revision ${revisionNum}`}
                        className="px-2 py-1 text-[11px] rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors inline-flex items-center gap-1 disabled:opacity-40 disabled:cursor-not-allowed"
                      >
                        <RotateCcw className="w-3 h-3" />
                        Rollback to
                      </button>
                    )}
                  </div>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      {diffRevision != null && (
        <RevisionDiffModal
          type="deployments"
          namespace={namespace}
          name={name}
          targetRevision={diffRevision}
          onClose={() => setDiffRevision(null)}
          onRollback={onRollback}
          canEdit={canEdit}
        />
      )}
    </Section>
  )
}

function WorkloadHistoryTable({
  type,
  namespace,
  name,
  onRollback,
  canEdit,
}: {
  type: string
  namespace: string
  name: string
  onRollback?: (revision: number) => void
  canEdit: boolean
}) {
  const { data, isLoading, error } = useWorkloadHistory(type, namespace, name)
  const [diffRevision, setDiffRevision] = useState<number | null>(null)
  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const items = data?.items ?? []
  if (items.length === 0) {
    return <div className="text-sm text-kb-text-tertiary text-center py-12">No revision history found</div>
  }

  return (
    <Section title="Revision History (ControllerRevisions)">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Revision</th>
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Age</th>
            <th className="pb-2 font-normal text-right">Actions</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {items.map((item, i) => {
            const isLatest = i === 0
            const revisionNum = Number(item.revision ?? 0)
            return (
              <tr key={i} className={`border-t border-kb-border ${isLatest ? 'bg-status-ok/5' : ''}`}>
                <td className="py-2">
                  <span className="font-mono">{String(item.revision ?? '')}</span>
                  {isLatest && (
                    <span className="ml-2 px-1.5 py-0.5 rounded text-[9px] font-medium bg-status-ok/20 text-status-ok">Current</span>
                  )}
                </td>
                <td className="py-2 font-mono">{String(item.name ?? '')}</td>
                <td className="py-2 font-mono text-kb-text-tertiary">{item.createdAt ? formatAge(String(item.createdAt)) : '-'}</td>
                <td className="py-2 text-right">
                  <div className="inline-flex items-center gap-1.5">
                    {Number.isFinite(revisionNum) && revisionNum > 0 && (
                      <ViewDiffButton onClick={() => setDiffRevision(revisionNum)} />
                    )}
                    {!isLatest && onRollback && Number.isFinite(revisionNum) && revisionNum > 0 && (
                      <button
                        onClick={() => onRollback(revisionNum)}
                        disabled={!canEdit}
                        title={!canEdit ? 'Editor role required' : `Rollback to revision ${revisionNum}`}
                        className="px-2 py-1 text-[11px] rounded border border-kb-border text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors inline-flex items-center gap-1 disabled:opacity-40 disabled:cursor-not-allowed"
                      >
                        <RotateCcw className="w-3 h-3" />
                        Rollback to
                      </button>
                    )}
                  </div>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      {diffRevision != null && (
        <RevisionDiffModal
          type={type as 'statefulsets' | 'daemonsets'}
          namespace={namespace}
          name={name}
          targetRevision={diffRevision}
          onClose={() => setDiffRevision(null)}
          onRollback={onRollback}
          canEdit={canEdit}
        />
      )}
    </Section>
  )
}

// Section is reused from ResourceDetailPage's local helper. The
// original History tab wraps its table in <Section title="Revision
// History">, which renders a card with bg-kb-card + border + padding.
// We replicate the same shell here so the table sits inside the same
// card chrome instead of bare on the page.
function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="bg-kb-card border border-kb-border rounded-xl p-4">
      <h3 className="text-sm font-medium text-kb-text-primary mb-3">{title}</h3>
      {children}
    </section>
  )
}
