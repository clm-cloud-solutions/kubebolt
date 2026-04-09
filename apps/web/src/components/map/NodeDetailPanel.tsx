import { X, ExternalLink } from 'lucide-react'
import { Link } from 'react-router-dom'
import { StatusBadge } from '@/components/resources/StatusBadge'
import { UsageBar } from '@/components/resources/UsageBar'
import type { TopologyNode, TopologyEdge } from '@/types/kubernetes'

const KIND_TO_ROUTE: Record<string, string> = {
  Pod: 'pods', Node: 'nodes', Deployment: 'deployments', StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets', ReplicaSet: 'replicasets', Job: 'jobs', CronJob: 'cronjobs',
  Service: 'services', Ingress: 'ingresses', Gateway: 'gateways', HTTPRoute: 'httproutes',
  ConfigMap: 'configmaps', Secret: 'secrets', HPA: 'hpas', HorizontalPodAutoscaler: 'hpas',
  PersistentVolumeClaim: 'pvcs', PersistentVolume: 'pvs',
}

interface NodeDetailPanelProps {
  node: TopologyNode
  edges: TopologyEdge[]
  allNodes: TopologyNode[]
  onClose: () => void
}

export function NodeDetailPanel({ node, edges, allNodes, onClose }: NodeDetailPanelProps) {
  // Find connected resources
  const connectedIds = new Set<string>()
  edges.forEach((edge) => {
    if (edge.source === node.id) connectedIds.add(edge.target)
    if (edge.target === node.id) connectedIds.add(edge.source)
  })
  const connected = allNodes.filter((n) => connectedIds.has(n.id))

  return (
    <div className="absolute right-0 top-0 bottom-0 w-[320px] bg-kb-card border-l border-kb-border z-20 flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-kb-border shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <span className="text-sm font-mono text-kb-text-primary truncate">{node.label}</span>
          <StatusBadge status={node.status} />
        </div>
        <div className="flex items-center gap-1 shrink-0">
          {KIND_TO_ROUTE[node.kind] && (
            <Link
              to={`/${KIND_TO_ROUTE[node.kind]}/${node.namespace || '_'}/${node.name}`}
              className="p-1 rounded hover:bg-kb-elevated text-kb-text-secondary hover:text-kb-accent transition-colors"
              title="View details"
            >
              <ExternalLink className="w-4 h-4" />
            </Link>
          )}
          <button
            onClick={onClose}
            className="p-1 rounded hover:bg-kb-elevated text-kb-text-secondary hover:text-kb-text-primary transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {/* Kind & Namespace */}
        <div className="grid grid-cols-2 gap-3">
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1">Kind</div>
            <div className="text-xs font-mono text-kb-text-primary">{node.kind}</div>
          </div>
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1">Namespace</div>
            <div className="text-xs font-mono text-kb-text-primary">{node.namespace || '-'}</div>
          </div>
        </div>

        {/* Metrics */}
        {(node.cpu || node.memory) && (
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-2">Metrics</div>
            <div className="space-y-2">
              {node.cpu && (
                <div>
                  <div className="flex items-center justify-between mb-1">
                    <span className="text-[10px] font-mono text-kb-text-tertiary">CPU</span>
                    <span className="text-[10px] font-mono text-kb-text-secondary">{Math.round(node.cpu.percentUsed)}%</span>
                  </div>
                  <UsageBar percent={node.cpu.percentUsed} height={4} />
                </div>
              )}
              {node.memory && (
                <div>
                  <div className="flex items-center justify-between mb-1">
                    <span className="text-[10px] font-mono text-kb-text-tertiary">Memory</span>
                    <span className="text-[10px] font-mono text-kb-text-secondary">{Math.round(node.memory.percentUsed)}%</span>
                  </div>
                  <UsageBar percent={node.memory.percentUsed} height={4} />
                </div>
              )}
              {node.pods && (
                <div>
                  <div className="flex items-center justify-between mb-1">
                    <span className="text-[10px] font-mono text-kb-text-tertiary">Pods</span>
                    <span className="text-[10px] font-mono text-kb-text-secondary">{node.pods.length}</span>
                  </div>
                </div>
              )}
            </div>
          </div>
        )}

        {/* Connected Resources */}
        {connected.length > 0 && (
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-2">
              Connected Resources ({connected.length})
            </div>
            <div className="space-y-1">
              {connected.map((cn) => (
                <div key={cn.id} className="flex items-center gap-2 px-2 py-1.5 rounded bg-kb-bg">
                  <div className={`w-1.5 h-1.5 rounded-full ${cn.status === 'Running' || cn.status === 'Active' ? 'bg-status-ok' : 'bg-kb-text-tertiary'}`} />
                  <span className="text-[11px] font-mono text-kb-text-primary truncate flex-1">{cn.label}</span>
                  <span className="text-[9px] font-mono text-kb-text-tertiary uppercase">{cn.kind}</span>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Metadata */}
        {node.metadata && Object.keys(node.metadata).length > 0 && (
          <div>
            <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-2">Metadata</div>
            <div className="space-y-1">
              {Object.entries(node.metadata).map(([key, value]) => (
                <div key={key} className="flex items-start gap-2">
                  <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">{key}:</span>
                  <span className="text-[10px] font-mono text-kb-text-secondary break-all">{value}</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
