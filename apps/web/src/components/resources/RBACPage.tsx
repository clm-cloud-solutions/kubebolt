import { useResources } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { Shield, Link } from 'lucide-react'

export function RBACPage() {
  const { data: roles, isLoading: rolesLoading, error: rolesError, dataUpdatedAt, isFetching } = useResources('clusterroles')
  const { data: bindings, isLoading: bindingsLoading, error: bindingsError } = useResources('clusterrolebindings')

  const isLoading = rolesLoading || bindingsLoading
  const error = rolesError || bindingsError

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const roleItems = roles?.items || []
  const bindingItems = bindings?.items || []

  return (
    <div>
      <div className="flex items-center justify-between mb-4">
        <h1 className="text-lg font-semibold text-kb-text-primary">RBAC</h1>
        <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} refreshInterval={30_000} isFetching={isFetching} />
      </div>

      <div className="grid grid-cols-2 gap-4">
        {/* ClusterRoles */}
        <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
          <div className="px-4 py-3 border-b border-kb-border flex items-center gap-2">
            <Shield className="w-4 h-4 text-status-info" />
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
              Cluster Roles
            </span>
            <span className="ml-auto text-[10px] font-mono text-kb-text-tertiary">{roleItems.length}</span>
          </div>
          <div className="max-h-[500px] overflow-y-auto divide-y divide-kb-border">
            {roleItems.map((role) => (
              <div key={role.name} className="px-4 py-2.5 hover:bg-kb-card-hover transition-colors">
                <span className="text-xs font-mono text-kb-text-primary">{role.name}</span>
              </div>
            ))}
            {roleItems.length === 0 && (
              <div className="py-8 text-center text-xs text-kb-text-tertiary font-mono">No cluster roles</div>
            )}
          </div>
        </div>

        {/* ClusterRoleBindings */}
        <div className="bg-kb-card border border-kb-border rounded-[10px] overflow-hidden">
          <div className="px-4 py-3 border-b border-kb-border flex items-center gap-2">
            <Link className="w-4 h-4 text-status-warn" />
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary">
              Cluster Role Bindings
            </span>
            <span className="ml-auto text-[10px] font-mono text-kb-text-tertiary">{bindingItems.length}</span>
          </div>
          <div className="max-h-[500px] overflow-y-auto divide-y divide-kb-border">
            {bindingItems.map((binding) => (
              <div key={binding.name} className="px-4 py-2.5 hover:bg-kb-card-hover transition-colors">
                <div className="text-xs font-mono text-kb-text-primary">{binding.name}</div>
                {binding.roleRef != null && (
                  <div className="text-[10px] font-mono text-kb-text-tertiary mt-0.5">
                    Role: {String(binding.roleRef)}
                  </div>
                )}
              </div>
            ))}
            {bindingItems.length === 0 && (
              <div className="py-8 text-center text-xs text-kb-text-tertiary font-mono">No bindings</div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
