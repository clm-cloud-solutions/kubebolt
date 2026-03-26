import { ShieldOff } from 'lucide-react'

const resourceLabels: Record<string, string> = {
  pods: 'Pods',
  nodes: 'Nodes',
  deployments: 'Deployments',
  statefulsets: 'StatefulSets',
  daemonsets: 'DaemonSets',
  jobs: 'Jobs',
  cronjobs: 'CronJobs',
  services: 'Services',
  ingresses: 'Ingresses',
  configmaps: 'ConfigMaps',
  secrets: 'Secrets',
  pvcs: 'PVCs',
  pvs: 'PVs',
  hpas: 'HPAs',
  storageclasses: 'StorageClasses',
  namespaces: 'Namespaces',
  events: 'Events',
  gateways: 'Gateways',
  httproutes: 'HTTPRoutes',
  endpoints: 'Endpoints',
}

interface PermissionDeniedProps {
  resourceType?: string
  message?: string
}

export function PermissionDenied({ resourceType, message }: PermissionDeniedProps) {
  const label = resourceType ? (resourceLabels[resourceType] || resourceType) : 'this resource'
  return (
    <div className="flex flex-col items-center justify-center p-12 text-center">
      <div className="w-12 h-12 rounded-2xl bg-status-warn-dim flex items-center justify-center mb-4">
        <ShieldOff className="w-6 h-6 text-status-warn" />
      </div>
      <h3 className="text-sm font-medium text-kb-text-primary mb-1">Access Restricted</h3>
      <p className="text-xs text-kb-text-secondary mb-2">
        {message || `Your kubeconfig does not have permission to view ${label}.`}
      </p>
      <p className="text-[10px] font-mono text-kb-text-tertiary uppercase tracking-[0.06em]">
        Contact your cluster administrator to request access
      </p>
    </div>
  )
}
