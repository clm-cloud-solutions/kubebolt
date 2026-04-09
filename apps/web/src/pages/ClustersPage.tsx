import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { Server, Check, ArrowRightLeft, Shield, Activity, Box, Layers, HardDrive, AlertTriangle } from 'lucide-react'
import { api } from '@/services/api'
import { parseClusterDisplayName } from '@/utils/cluster'
import type { ClusterInfo, ClusterOverview, ClusterHealth } from '@/types/kubernetes'

// TODO: Requires persistence layer (JSON file or SQLite)
// - Rename clusters (display name, without modifying kubeconfig)
// - Hide clusters from the list (soft delete)
// - Upload kubeconfig / paste content to add new clusters
// - Not applicable in in-cluster mode (Helm deployment)

function HealthDot({ status }: { status: 'connected' | 'disconnected' | 'error' | string }) {
  const color = status === 'connected' ? 'bg-status-ok'
    : status === 'error' ? 'bg-status-error'
    : 'bg-kb-text-tertiary'
  return (
    <span className="relative flex h-2.5 w-2.5">
      {status === 'connected' && (
        <span className={`animate-ping absolute inline-flex h-full w-full rounded-full ${color} opacity-40`} />
      )}
      <span className={`relative inline-flex rounded-full h-2.5 w-2.5 ${color}`} />
    </span>
  )
}

function StatItem({ icon, label, value }: { icon: React.ReactNode; label: string; value: string | number }) {
  return (
    <div className="flex items-center gap-2">
      <div className="text-kb-text-tertiary">{icon}</div>
      <div>
        <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-wider">{label}</div>
        <div className="text-sm font-mono text-kb-text-primary">{value}</div>
      </div>
    </div>
  )
}

function ClusterCard({
  cluster,
  overview,
  health,
  onSwitch,
  isSwitching,
}: {
  cluster: ClusterInfo
  overview?: ClusterOverview
  health?: ClusterHealth
  onSwitch: (context: string) => void
  isSwitching: boolean
}) {
  const isActive = cluster.active
  const isConnected = cluster.status === 'connected'
  const hasError = cluster.status === 'error'
  const displayName = parseClusterDisplayName(cluster)

  return (
    <div
      className={`bg-kb-card border rounded-xl p-5 transition-all ${
        isActive && isConnected
          ? 'border-kb-accent/30 ring-1 ring-kb-accent/10'
          : hasError
          ? 'border-status-error/30'
          : 'border-kb-border hover:border-kb-border-active'
      }`}
    >
      {/* Header */}
      <div className="flex items-start justify-between mb-4">
        <div className="flex items-center gap-3 min-w-0">
          <div className={`w-9 h-9 rounded-lg flex items-center justify-center shrink-0 ${
            isConnected ? 'bg-kb-accent-light' : hasError ? 'bg-status-error-dim' : 'bg-kb-elevated'
          }`}>
            <Server className={`w-4.5 h-4.5 ${
              isConnected ? 'text-kb-accent' : hasError ? 'text-status-error' : 'text-kb-text-tertiary'
            }`} />
          </div>
          <div className="min-w-0">
            <div className="text-sm font-semibold text-kb-text-primary truncate">{displayName}</div>
            <div className="text-[10px] font-mono text-kb-text-tertiary truncate" title={cluster.context}>{cluster.context}</div>
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {isActive && isConnected ? (
            <span className="flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-kb-accent-light text-kb-accent text-[10px] font-mono">
              <Check className="w-3 h-3" />
              Active
            </span>
          ) : isActive && hasError ? (
            <span className="flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-status-error-dim text-status-error text-[10px] font-mono">
              <AlertTriangle className="w-3 h-3" />
              Error
            </span>
          ) : (
            <button
              onClick={() => onSwitch(cluster.context)}
              disabled={isSwitching}
              className="flex items-center gap-1.5 px-3 py-1 rounded-lg border border-kb-border text-[11px] font-mono text-kb-text-secondary hover:bg-kb-elevated hover:text-kb-text-primary transition-colors disabled:opacity-50"
            >
              <ArrowRightLeft className="w-3 h-3" />
              Switch
            </button>
          )}
        </div>
      </div>

      {/* Server URL */}
      <div className="text-[10px] font-mono text-kb-text-tertiary mb-4 truncate" title={cluster.server}>
        {cluster.server}
      </div>

      {/* Connection error details */}
      {hasError && cluster.error && (
        <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-status-error-dim mb-4">
          <AlertTriangle className="w-3.5 h-3.5 text-status-error shrink-0 mt-0.5" />
          <span className="text-[10px] font-mono text-status-error break-all">{cluster.error}</span>
        </div>
      )}

      {/* Active + connected cluster details */}
      {isActive && isConnected && overview && (
        <>
          {/* Health bar */}
          <div className="flex items-center gap-2 mb-4 px-3 py-2 rounded-lg bg-kb-bg">
            <HealthDot status={cluster.status} />
            <span className="text-[11px] font-mono text-kb-text-primary capitalize">
              {health?.status || overview?.health?.status || 'connected'}
            </span>
            {health?.score !== undefined && (
              <span className="text-[10px] font-mono text-kb-text-tertiary ml-auto">
                Score: {health.score}/100
              </span>
            )}
          </div>

          {/* Stats grid */}
          <div className="grid grid-cols-2 gap-3 mb-4">
            {overview.kubernetesVersion && (
              <StatItem icon={<Shield className="w-3.5 h-3.5" />} label="Version" value={overview.kubernetesVersion} />
            )}
            {overview.nodes && (
              <StatItem icon={<Server className="w-3.5 h-3.5" />} label="Nodes" value={`${overview.nodes.ready}/${overview.nodes.total}`} />
            )}
            {overview.pods && (
              <StatItem icon={<Box className="w-3.5 h-3.5" />} label="Pods" value={`${overview.pods.ready}/${overview.pods.total}`} />
            )}
            {overview.deployments && (
              <StatItem icon={<Layers className="w-3.5 h-3.5" />} label="Deployments" value={`${overview.deployments.ready}/${overview.deployments.total}`} />
            )}
            {overview.namespaces && (
              <StatItem icon={<Activity className="w-3.5 h-3.5" />} label="Namespaces" value={overview.namespaces.total} />
            )}
            {overview.pvcs && (
              <StatItem icon={<HardDrive className="w-3.5 h-3.5" />} label="PVCs" value={overview.pvcs.total} />
            )}
          </div>

          {/* Health checks */}
          {health?.checks && health.checks.length > 0 && (
            <div className="space-y-1">
              <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-wider mb-1.5">Health Checks</div>
              {health.checks.map((check) => (
                <div key={check.name} className="flex items-center gap-2 text-[10px] font-mono">
                  <div className={`w-1.5 h-1.5 rounded-full ${
                    check.status === 'pass' ? 'bg-status-ok' : check.status === 'warn' ? 'bg-status-warn' : 'bg-status-error'
                  }`} />
                  <span className="text-kb-text-secondary flex-1">{check.name}</span>
                  <span className="text-kb-text-tertiary">{check.message}</span>
                </div>
              ))}
            </div>
          )}
        </>
      )}

      {/* Disconnected cluster */}
      {cluster.status === 'disconnected' && (
        <div className="flex items-center gap-2 px-3 py-2 rounded-lg bg-kb-bg">
          <div className="w-1.5 h-1.5 rounded-full bg-kb-text-tertiary" />
          <span className="text-[11px] font-mono text-kb-text-tertiary">Disconnected</span>
        </div>
      )}
    </div>
  )
}

export function ClustersPage() {
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const { data: clusters } = useQuery({
    queryKey: ['clusters'],
    queryFn: api.listClusters,
    refetchInterval: 30_000,
  })

  const { data: overview } = useQuery({
    queryKey: ['cluster-overview'],
    queryFn: api.getOverview,
  })

  const { data: health } = useQuery({
    queryKey: ['cluster-health'],
    queryFn: api.getHealth,
  })

  const switchMutation = useMutation({
    mutationKey: ['switch-cluster'],
    mutationFn: (context: string) => api.switchCluster(context),
    onMutate: (context: string) => {
      queryClient.setQueryData(['clusters'], (old: ClusterInfo[] | undefined) =>
        old?.map(c => ({ ...c, active: c.context === context }))
      )
      queryClient.setQueryData(['cluster-overview'], undefined)
    },
    onSuccess: () => {
      queryClient.invalidateQueries()
      navigate('/')
    },
    onError: () => {
      queryClient.invalidateQueries()
    },
  })

  const sorted = [...(clusters || [])].sort((a, b) => {
    if (a.active) return -1
    if (b.active) return 1
    return a.context.localeCompare(b.context)
  })

  const connectedCount = clusters?.filter(c => c.status === 'connected').length || 0

  return (
    <div className="p-6 max-w-5xl">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-lg font-semibold text-kb-text-primary">Clusters</h1>
          <p className="text-xs text-kb-text-tertiary mt-0.5">
            {connectedCount} connected · {clusters?.length || 0} available
          </p>
        </div>
        {/* TODO: Add cluster button (requires persistence layer)
        <button className="...">
          <Plus /> Add Cluster
        </button>
        */}
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {sorted.map((cluster) => (
          <ClusterCard
            key={cluster.context}
            cluster={cluster}
            overview={cluster.active ? overview : undefined}
            health={cluster.active ? health : undefined}
            onSwitch={(ctx) => switchMutation.mutate(ctx)}
            isSwitching={switchMutation.isPending}
          />
        ))}
      </div>
    </div>
  )
}
