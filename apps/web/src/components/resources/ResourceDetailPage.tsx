import React from 'react'
import { useParams, Link } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'
import { useResourceDetail } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { StatusBadge } from './StatusBadge'
import { UsageBar } from './UsageBar'
import { formatAge, formatCPU, formatMemory } from '@/utils/formatters'
import type { ResourceItem } from '@/types/kubernetes'

// Maps a Kubernetes kind to the URL segment used in KubeBolt routes
const kindToRoute: Record<string, string> = {
  Deployment: 'deployments',
  StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets',
  ReplicaSet: 'replicasets',
  Pod: 'pods',
  Service: 'services',
  Node: 'nodes',
  Ingress: 'ingresses',
  Job: 'jobs',
  CronJob: 'cronjobs',
  ConfigMap: 'configmaps',
  Secret: 'secrets',
  PersistentVolumeClaim: 'pvcs',
  PersistentVolume: 'pvs',
  HorizontalPodAutoscaler: 'hpas',
  StorageClass: 'storageclasses',
  Gateway: 'gateways',
  HTTPRoute: 'httproutes',
  Namespace: 'namespaces',
}

function ResourceLink({ name, namespace, resourceType }: { name: string; namespace?: string; resourceType: string }) {
  const ns = namespace || '_'
  return (
    <Link
      to={`/${resourceType}/${ns}/${name}`}
      className="text-status-info hover:underline font-mono text-[11px]"
    >
      {name}
    </Link>
  )
}

// Parses "Kind/Name" format (e.g. "Deployment/my-app") into a link
function KindNameLink({ value, namespace }: { value: string; namespace?: string }) {
  const parts = value.split('/')
  if (parts.length !== 2) return <span className="font-mono text-[11px]">{value}</span>
  const [kind, name] = parts
  const route = kindToRoute[kind]
  if (!route) return <span className="font-mono text-[11px]">{value}</span>
  return (
    <span className="text-[11px]">
      <span className="text-kb-text-tertiary">{kind}/</span>
      <ResourceLink name={name} namespace={namespace} resourceType={route} />
    </span>
  )
}

const resourceLabels: Record<string, string> = {
  pods: 'Pods',
  deployments: 'Deployments',
  statefulsets: 'StatefulSets',
  daemonsets: 'DaemonSets',
  jobs: 'Jobs',
  cronjobs: 'CronJobs',
  services: 'Services',
  ingresses: 'Ingresses',
  gateways: 'Gateways',
  httproutes: 'HTTPRoutes',
  endpoints: 'Endpoints',
  pvcs: 'Persistent Volume Claims',
  pvs: 'Persistent Volumes',
  storageclasses: 'Storage Classes',
  configmaps: 'ConfigMaps',
  secrets: 'Secrets',
  hpas: 'HPAs',
  nodes: 'Nodes',
  namespaces: 'Namespaces',
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="bg-kb-card border border-kb-border rounded-[10px] p-4">
      <div className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary mb-3">
        {title}
      </div>
      {children}
    </div>
  )
}

function renderValue(value: React.ReactNode): React.ReactNode {
  if (typeof value === 'object' && value !== null && !React.isValidElement(value)) {
    return JSON.stringify(value)
  }
  return value
}

function Field({ label, value, mono }: { label: string; value: React.ReactNode; mono?: boolean }) {
  if (value === undefined || value === null || value === '') return null
  return (
    <div className="flex items-start gap-3 py-1.5">
      <span className="text-[11px] text-kb-text-tertiary w-36 shrink-0">{label}</span>
      <span className={`text-[11px] text-kb-text-primary break-all ${mono ? 'font-mono' : ''}`}>
        {renderValue(value)}
      </span>
    </div>
  )
}

function Labels({ labels }: { labels: Record<string, string> | undefined }) {
  if (!labels || Object.keys(labels).length === 0) {
    return <span className="text-[11px] text-kb-text-tertiary">None</span>
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {Object.entries(labels).map(([k, v]) => (
        <span
          key={k}
          className="inline-flex px-2 py-0.5 rounded text-[10px] font-mono bg-kb-elevated text-kb-text-secondary"
        >
          {k}={v}
        </span>
      ))}
    </div>
  )
}

function MetricsSection({ item }: { item: ResourceItem }) {
  const cpuUsage = Number(item.cpuUsage ?? 0)
  const cpuPercent = Number(item.cpuPercent ?? 0)
  const memUsage = Number(item.memoryUsage ?? 0)
  const memPercent = Number(item.memoryPercent ?? 0)

  if (cpuUsage === 0 && memUsage === 0) return null

  return (
    <Section title="Resource Usage">
      <div className="grid grid-cols-2 gap-4">
        {cpuUsage > 0 && (
          <div>
            <div className="text-[10px] text-kb-text-tertiary mb-1">CPU</div>
            <UsageBar percent={cpuPercent} height={6} showLabel />
            <div className="text-[11px] font-mono text-kb-text-secondary mt-1">{formatCPU(cpuUsage)}</div>
          </div>
        )}
        {memUsage > 0 && (
          <div>
            <div className="text-[10px] text-kb-text-tertiary mb-1">Memory</div>
            <UsageBar percent={memPercent} height={6} showLabel />
            <div className="text-[11px] font-mono text-kb-text-secondary mt-1">{formatMemory(memUsage)}</div>
          </div>
        )}
      </div>
    </Section>
  )
}

function ContainersSection({ containers }: { containers: unknown }) {
  if (!containers || !Array.isArray(containers) || containers.length === 0) return null
  return (
    <Section title="Containers">
      <div className="space-y-3">
        {(containers as Array<Record<string, unknown>>).map((c, i) => (
          <div key={String(c.name ?? i)} className="bg-kb-elevated rounded-lg p-3">
            <div className="flex items-center gap-2 mb-2">
              <span className="text-xs font-medium text-kb-text-primary">{String(c.name)}</span>
              {c.ready !== undefined && (
                <StatusBadge status={c.ready ? 'Running' : 'NotReady'} label={c.ready ? 'Ready' : 'Not Ready'} />
              )}
            </div>
            <div className="space-y-0.5">
              <Field label="Image" value={String(c.image ?? '')} mono />
              {Array.isArray(c.ports) && c.ports.length > 0 && (
                <Field
                  label="Ports"
                  value={(c.ports as Array<Record<string, unknown>>).map((p: Record<string, unknown>) => `${p.containerPort}/${p.protocol ?? 'TCP'}`).join(', ')}
                  mono
                />
              )}
              {c.cpuRequest != null && <Field label="CPU Request" value={formatCPU(Number(c.cpuRequest))} mono />}
              {c.cpuLimit != null && <Field label="CPU Limit" value={formatCPU(Number(c.cpuLimit))} mono />}
              {c.memoryRequest != null && <Field label="Memory Request" value={formatMemory(Number(c.memoryRequest))} mono />}
              {c.memoryLimit != null && <Field label="Memory Limit" value={formatMemory(Number(c.memoryLimit))} mono />}
            </div>
          </div>
        ))}
      </div>
    </Section>
  )
}

function PortsSection({ ports }: { ports: unknown }) {
  if (!ports || !Array.isArray(ports) || ports.length === 0) return null
  return (
    <Section title="Ports">
      <div className="overflow-x-auto">
        <table className="w-full text-[11px]">
          <thead>
            <tr className="text-kb-text-tertiary text-left">
              <th className="pb-1.5 font-normal">Name</th>
              <th className="pb-1.5 font-normal">Port</th>
              <th className="pb-1.5 font-normal">Target</th>
              <th className="pb-1.5 font-normal">Protocol</th>
              {(ports as Array<Record<string, unknown>>).some(p => p.nodePort) && (
                <th className="pb-1.5 font-normal">Node Port</th>
              )}
            </tr>
          </thead>
          <tbody className="font-mono text-kb-text-secondary">
            {(ports as Array<Record<string, unknown>>).map((p, i) => (
              <tr key={i} className="border-t border-kb-border">
                <td className="py-1.5">{String(p.name ?? '-')}</td>
                <td className="py-1.5">{String(p.port ?? '-')}</td>
                <td className="py-1.5">{String(p.targetPort ?? '-')}</td>
                <td className="py-1.5">{String(p.protocol ?? 'TCP')}</td>
                {(ports as Array<Record<string, unknown>>).some(pp => pp.nodePort) && (
                  <td className="py-1.5">{p.nodePort ? String(p.nodePort) : '-'}</td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Section>
  )
}

function PodDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      {item.nodeName && (
        <Field label="Node" value={<ResourceLink name={String(item.nodeName)} resourceType="nodes" />} />
      )}
      <Field label="IP" value={String(item.ip ?? '')} mono />
      <Field label="Ready" value={String(item.ready ?? '')} />
      <Field label="Restarts" value={item.restarts != null ? String(item.restarts) : undefined} />
      <Field label="QoS Class" value={String(item.qosClass ?? '')} />
    </>
  )
}

function extractSelector(selector: unknown): Record<string, string> | undefined {
  if (!selector || typeof selector !== 'object') return undefined
  const s = selector as Record<string, unknown>
  if (s.matchLabels && typeof s.matchLabels === 'object') {
    return s.matchLabels as Record<string, string>
  }
  // Flat key-value selector
  const flat: Record<string, string> = {}
  for (const [k, v] of Object.entries(s)) {
    if (typeof v === 'string') flat[k] = v
  }
  return Object.keys(flat).length > 0 ? flat : undefined
}

function DeploymentDetails({ item }: { item: ResourceItem }) {
  const selectorLabels = extractSelector(item.selector)
  return (
    <>
      <Field label="Replicas" value={`${item.readyReplicas ?? 0}/${item.replicas ?? 0} ready`} />
      <Field label="Updated" value={item.updatedReplicas != null ? String(item.updatedReplicas) : undefined} />
      <Field label="Available" value={item.availableReplicas != null ? String(item.availableReplicas) : undefined} />
      <Field label="Strategy" value={String(item.strategy ?? '')} />
      {selectorLabels && <Field label="Selector" value={<Labels labels={selectorLabels} />} />}
    </>
  )
}

function ServiceDetails({ item }: { item: ResourceItem }) {
  const selectorLabels = extractSelector(item.selector)
  return (
    <>
      <Field label="Type" value={String(item.type ?? '')} />
      <Field label="Cluster IP" value={String(item.clusterIP ?? '')} mono />
      {item.externalIP && <Field label="External IP" value={String(item.externalIP)} mono />}
      {selectorLabels && <Field label="Selector" value={<Labels labels={selectorLabels} />} />}
    </>
  )
}

function NodeDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Kubelet" value={String(item.kubeletVersion ?? '')} mono />
      <Field label="OS Image" value={String(item.osImage ?? '')} />
      <Field label="Runtime" value={String(item.containerRuntime ?? '')} />
      <Field label="CPU Capacity" value={item.cpuCapacity != null ? `${item.cpuCapacity} cores` : undefined} />
      <Field label="Memory Capacity" value={item.memoryCapacity != null ? formatMemory(Number(item.memoryCapacity)) : undefined} />
      <Field label="CPU Allocatable" value={item.cpuAllocatable != null ? `${item.cpuAllocatable} cores` : undefined} />
      <Field label="Memory Allocatable" value={item.memoryAllocatable != null ? formatMemory(Number(item.memoryAllocatable)) : undefined} />
    </>
  )
}

function StatefulSetDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Replicas" value={`${item.readyReplicas ?? 0}/${item.replicas ?? 0} ready`} />
    </>
  )
}

function DaemonSetDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Desired" value={String(item.desired ?? '')} />
      <Field label="Ready" value={String(item.ready ?? '')} />
      <Field label="Available" value={String(item.numberAvailable ?? '')} />
    </>
  )
}

function JobDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Succeeded" value={String(item.succeeded ?? 0)} />
      <Field label="Failed" value={String(item.failed ?? 0)} />
      <Field label="Active" value={String(item.active ?? 0)} />
      <Field label="Completions" value={String(item.completions ?? '')} />
      <Field label="Duration" value={String(item.duration ?? '')} />
    </>
  )
}

function CronJobDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Schedule" value={String(item.schedule ?? '')} mono />
      <Field label="Suspend" value={String(item.suspend ?? false)} />
      <Field label="Active Jobs" value={String(item.activeJobs ?? 0)} />
      <Field label="Last Schedule" value={item.lastScheduleTime ? formatAge(String(item.lastScheduleTime)) + ' ago' : '-'} />
    </>
  )
}

function IngressDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Hosts" value={String(item.hosts ?? '')} mono />
      <Field label="Address" value={String(item.address ?? '')} mono />
    </>
  )
}

function PVCDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      {item.volumeName && (
        <Field label="Volume" value={<ResourceLink name={String(item.volumeName)} resourceType="pvs" />} />
      )}
      {item.storageClass && (
        <Field label="Storage Class" value={<ResourceLink name={String(item.storageClass)} resourceType="storageclasses" />} />
      )}
      <Field label="Capacity" value={String(item.capacity ?? '')} />
      <Field label="Access Modes" value={String(item.accessModes ?? '')} />
    </>
  )
}

function PVDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Capacity" value={String(item.capacity ?? '')} />
      {item.storageClass && (
        <Field label="Storage Class" value={<ResourceLink name={String(item.storageClass)} resourceType="storageclasses" />} />
      )}
      <Field label="Access Modes" value={String(item.accessModes ?? '')} />
      <Field label="Reclaim Policy" value={String(item.reclaimPolicy ?? '')} />
    </>
  )
}

function HPADetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Min Replicas" value={String(item.minReplicas ?? '')} />
      <Field label="Max Replicas" value={String(item.maxReplicas ?? '')} />
      <Field label="Current" value={String(item.currentReplicas ?? '')} />
      <Field label="Desired" value={String(item.desiredReplicas ?? '')} />
      {item.targetRef && (
        <Field label="Target" value={<KindNameLink value={String(item.targetRef)} namespace={item.namespace} />} />
      )}
    </>
  )
}

function ConfigMapDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Keys" value={String(item.keys ?? '')} />
      <Field label="Data Count" value={String(item.dataCount ?? 0)} />
    </>
  )
}

function SecretDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Type" value={String(item.type ?? '')} mono />
      <Field label="Keys" value={String(item.keys ?? '')} />
      <Field label="Data Count" value={String(item.dataCount ?? 0)} />
    </>
  )
}

function GatewayDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Class" value={String(item.class ?? '')} />
      <Field label="Address" value={String(item.address ?? '')} mono />
      <Field label="Listeners" value={String(item.listeners ?? '')} />
    </>
  )
}

function HTTPRouteDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Hostnames" value={String(item.hostnames ?? '')} mono />
      {item.gateway && (
        <Field label="Gateway" value={<ResourceLink name={String(item.gateway)} namespace={item.namespace} resourceType="gateways" />} />
      )}
      <Field label="Backends" value={String(item.backends ?? '')} />
    </>
  )
}

function StorageClassDetails({ item }: { item: ResourceItem }) {
  return (
    <>
      <Field label="Provisioner" value={String(item.provisioner ?? '')} mono />
      <Field label="Reclaim Policy" value={String(item.reclaimPolicy ?? '')} />
      <Field label="Volume Binding" value={String(item.volumeBindingMode ?? '')} />
    </>
  )
}

function EndpointDetails({ item }: { item: ResourceItem }) {
  const addresses = item.addresses
  return (
    <>
      {Array.isArray(addresses) && addresses.length > 0 && (
        <Field label="Addresses" value={addresses.join(', ')} mono />
      )}
    </>
  )
}

function ResourceSpecificFields({ type, item }: { type: string; item: ResourceItem }) {
  switch (type) {
    case 'pods': return <PodDetails item={item} />
    case 'deployments': return <DeploymentDetails item={item} />
    case 'services': return <ServiceDetails item={item} />
    case 'nodes': return <NodeDetails item={item} />
    case 'statefulsets': return <StatefulSetDetails item={item} />
    case 'daemonsets': return <DaemonSetDetails item={item} />
    case 'jobs': return <JobDetails item={item} />
    case 'cronjobs': return <CronJobDetails item={item} />
    case 'ingresses': return <IngressDetails item={item} />
    case 'pvcs': return <PVCDetails item={item} />
    case 'pvs': return <PVDetails item={item} />
    case 'hpas': return <HPADetails item={item} />
    case 'configmaps': return <ConfigMapDetails item={item} />
    case 'secrets': return <SecretDetails item={item} />
    case 'gateways': return <GatewayDetails item={item} />
    case 'httproutes': return <HTTPRouteDetails item={item} />
    case 'storageclasses': return <StorageClassDetails item={item} />
    case 'endpoints': return <EndpointDetails item={item} />
    default: return null
  }
}

export function ResourceDetailPage() {
  const { type = '', namespace = '', name = '' } = useParams<{ type: string; namespace: string; name: string }>()
  const { data: item, isLoading, error, refetch } = useResourceDetail(type, namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error || !item) return <ErrorState message={error?.message ?? 'Resource not found'} onRetry={() => refetch()} />

  const parentLabel = resourceLabels[type] || type
  const parentPath = `/${type}`

  return (
    <div className="space-y-4">
      {/* Breadcrumb */}
      <div className="flex items-center gap-1.5 text-[11px] font-mono text-kb-text-tertiary">
        <Link to={parentPath} className="hover:text-kb-text-primary transition-colors">
          {parentLabel}
        </Link>
        <ChevronRight size={12} />
        {item.namespace && (
          <>
            <span>{item.namespace}</span>
            <ChevronRight size={12} />
          </>
        )}
        <span className="text-kb-text-primary">{item.name}</span>
      </div>

      {/* Header */}
      <div className="flex items-center gap-3">
        <h1 className="text-lg font-semibold text-kb-text-primary">{item.name}</h1>
        <StatusBadge status={item.status} size="md" />
      </div>

      {/* Overview */}
      <Section title="Overview">
        <div className="space-y-0.5">
          {item.namespace && (
            <Field label="Namespace" value={<ResourceLink name={item.namespace} resourceType="namespaces" />} />
          )}
          <Field label="Status" value={<StatusBadge status={item.status} />} />
          <Field label="Age" value={item.createdAt ? formatAge(item.createdAt) : item.age} />
          <Field label="Created" value={item.createdAt ? new Date(item.createdAt).toLocaleString() : undefined} />
          <ResourceSpecificFields type={type} item={item} />
        </div>
      </Section>

      {/* Metrics */}
      <MetricsSection item={item} />

      {/* Containers (pods) */}
      <ContainersSection containers={item.containers} />

      {/* Ports (services) */}
      <PortsSection ports={item.ports} />

      {/* Labels */}
      <Section title="Labels">
        <Labels labels={item.labels} />
      </Section>

      {/* Annotations */}
      <Section title="Annotations">
        <Labels labels={item.annotations} />
      </Section>
    </div>
  )
}
