import { useResources } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { ResourceTypeIcon, resourceTypeDescription } from '@/utils/resourceIcons'
import { StatusBadge } from './StatusBadge'
import { FolderOpen } from 'lucide-react'
import type { ResourceItem } from '@/types/kubernetes'
import {
  useNamespaceQuotas,
  formatQuotaValue,
  type NamespaceQuotaSummary,
} from '@/hooks/useNamespaceQuotas'

const QUOTA_WARN = 70
const QUOTA_CRIT = 85

// QuotaBar renders one row of the per-resource quota breakdown:
// label (e.g. "limits.cpu"), used/hard text, and a thin bar
// proportional to the used fraction. The bar color tracks the same
// 70/85 thresholds used in the NamespaceTile chip so both surfaces
// agree on what counts as "warm" vs "hot".
function QuotaBar({ resource, used, hard, pct }: { resource: string; used: number; hard: number; pct: number }) {
  const color =
    pct >= QUOTA_CRIT ? 'bg-status-error' : pct >= QUOTA_WARN ? 'bg-status-warn' : 'bg-status-info'
  const pctText = pct >= 99.5 ? '100%' : `${pct.toFixed(0)}%`
  return (
    <div className="space-y-0.5">
      <div className="flex items-baseline justify-between gap-2 text-[10px] font-mono">
        <span className="text-kb-text-secondary truncate">{resource}</span>
        <span className="text-kb-text-tertiary tabular-nums shrink-0">
          {formatQuotaValue(resource, used)} / {formatQuotaValue(resource, hard)}
          <span className="ml-1.5 text-kb-text-secondary">{pctText}</span>
        </span>
      </div>
      <div className="h-[3px] rounded-full bg-kb-elevated overflow-hidden">
        <div
          className={`h-full ${color}`}
          style={{ width: `${Math.max(pct, pct > 0 ? 4 : 0)}%` }}
        />
      </div>
    </div>
  )
}

function NamespaceCard({ ns, quota }: { ns: ResourceItem; quota?: NamespaceQuotaSummary }) {
  const podCount = (ns.podCount as number) ?? 0
  const deploymentCount = (ns.deploymentCount as number) ?? 0
  const serviceCount = (ns.serviceCount as number) ?? 0

  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4 hover:bg-kb-card-hover transition-colors">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <FolderOpen className="w-4 h-4 text-[#a78bfa]" />
          <span className="text-sm font-mono text-kb-text-primary">{ns.name}</span>
        </div>
        <StatusBadge status={ns.status || 'Active'} />
      </div>

      <div className="grid grid-cols-3 gap-2">
        <div className="bg-kb-bg rounded-md p-2 text-center">
          <div className="text-sm font-semibold text-kb-text-primary">{podCount}</div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em]">Pods</div>
        </div>
        <div className="bg-kb-bg rounded-md p-2 text-center">
          <div className="text-sm font-semibold text-kb-text-primary">{deploymentCount}</div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em]">Deploys</div>
        </div>
        <div className="bg-kb-bg rounded-md p-2 text-center">
          <div className="text-sm font-semibold text-kb-text-primary">{serviceCount}</div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em]">Services</div>
        </div>
      </div>

      {/* Quota strip — present-or-absent. When a ResourceQuota is
          bound to the namespace, render the per-resource breakdown.
          When no quota is set, render an explicit "unconstrained"
          marker in the same slot so cards stay visually coherent —
          a row of cards in the same grid will all share the
          same anatomy, with absence of a quota communicated
          explicitly rather than as a void. The unconstrained
          variant is muted to keep visual weight on cards that
          have actual numbers to read. */}
      {quota && quota.items.length > 0 ? (
        <div className="mt-3 pt-3 border-t border-kb-border">
          <div className="flex items-baseline justify-between mb-2">
            <span className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em]">
              Quota · {quota.quotaName}
            </span>
            <span
              className={`text-[10px] font-mono tabular-nums ${
                quota.maxPct >= QUOTA_CRIT
                  ? 'text-status-error'
                  : quota.maxPct >= QUOTA_WARN
                    ? 'text-status-warn'
                    : 'text-kb-text-secondary'
              }`}
            >
              max {quota.maxPct.toFixed(0)}%
            </span>
          </div>
          <div className="space-y-1.5">
            {quota.items.slice(0, 4).map((it) => (
              <QuotaBar key={it.resource} {...it} />
            ))}
            {quota.items.length > 4 && (
              <div className="text-[9px] font-mono text-kb-text-tertiary pt-0.5">
                +{quota.items.length - 4} more
              </div>
            )}
          </div>
        </div>
      ) : (
        <div className="mt-3 pt-3 border-t border-kb-border">
          <div className="flex items-baseline justify-between mb-1">
            <span className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em]">
              Quota · unconstrained
            </span>
            <span className="text-[10px] font-mono text-kb-text-tertiary">—</span>
          </div>
          <div className="text-[10px] font-mono text-kb-text-tertiary leading-snug">
            No ResourceQuota bound to this namespace.
            <span className="text-kb-text-tertiary/70">
              {' '}
              Workloads here can request any cluster-allocatable resources.
            </span>
          </div>
        </div>
      )}
    </div>
  )
}

export function NamespacesPage() {
  const { data, isLoading, error, refetch, dataUpdatedAt, isFetching } = useResources('namespaces')
  const { quotas } = useNamespaceQuotas()

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const namespaces = data?.items || []

  return (
    <div>
      <div className="mb-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-2">
              <ResourceTypeIcon type="namespaces" />
              <h1 className="text-lg font-semibold text-kb-text-primary">Namespaces</h1>
            </div>
            <span className="text-[10px] font-mono px-2.5 py-0.5 rounded bg-kb-elevated text-kb-text-tertiary">
              {namespaces.length} total
            </span>
          </div>
          <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
        </div>
        <p className="text-xs text-kb-text-secondary mt-1">{resourceTypeDescription('namespaces')}</p>
      </div>
      <div className="grid grid-cols-3 gap-3">
        {namespaces.map((ns) => (
          <NamespaceCard key={ns.name} ns={ns} quota={quotas[ns.name]} />
        ))}
      </div>
    </div>
  )
}
