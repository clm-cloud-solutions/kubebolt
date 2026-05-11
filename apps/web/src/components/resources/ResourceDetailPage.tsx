import React, { useState, useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { EditorView, lineNumbers } from '@codemirror/view'
import { yaml } from '@codemirror/lang-yaml'
import { oneDark } from '@codemirror/theme-one-dark'
import { useParams, Link, useNavigate, useSearchParams } from 'react-router-dom'
import { ChevronRight, Lock, RotateCw, ArrowUpDown, ArrowRight, ChevronDown, Image as ImageIcon, Play, Pause, AlertCircle, Cpu, Variable, Tag, Eye, Trash2, RefreshCw, FileText } from 'lucide-react'
import { SetImageModal } from '@/components/resources/SetImageModal'
import { SetResourcesModal } from '@/components/resources/SetResourcesModal'
import { SetEnvModal } from '@/components/resources/SetEnvModal'
import { EditMetadataModal } from '@/components/resources/EditMetadataModal'
import { SecretRevealModal } from '@/components/resources/SecretRevealModal'
import { ScaleModal } from '@/components/resources/ScaleModal'
import { ResourceActionsMenu, type ActionItem } from '@/components/resources/ResourceActionsMenu'
import { RevisionTimeline } from '@/components/resources/RevisionTimeline'
import { RestartHistorySparkline } from '@/components/resources/RestartHistorySparkline'
import { EndpointHealthCell } from '@/components/resources/EndpointHealthCell'
import { RollbackModal } from '@/components/resources/RollbackModal'
import { CronJobTriggerModal } from '@/components/resources/CronJobTriggerModal'
import { DrainModal } from '@/components/resources/DrainModal'
import { NodeSchedulabilityToolbarButton } from '@/components/resources/NodeSchedulabilityToolbarButton'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useResources, useResourceDetail, useResourceDescribe, useResourceYAML, useResourceEvents, useTopology, usePodLogs, useDeploymentPods, useDeploymentHistory, useStatefulSetPods, useDaemonSetPods, useJobPods, useCronJobJobs, useWorkloadHistory } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { MutationErrorToast, classifyMutationError, type MutationErrorVariant } from '@/components/shared/MutationErrorToast'
import { StatusBadge } from './StatusBadge'
import { ResourceUsageCell } from '@/components/shared/ResourceUsageCell'
import { MetricChart, METRIC_ACCENTS } from '@/components/shared/MetricChart'
import { TerminalTab, DeploymentTerminalTab, StatefulSetTerminalTab, DaemonSetTerminalTab } from './TerminalTab'
import { FilesTab } from './FilesTab'
import { PortForwardButton, PortForwardNote } from './PortForwardButton'
import { AskCopilotButton } from '@/components/copilot/AskCopilotButton'
import { useAuth } from '@/contexts/AuthContext'
import { formatAge, formatCPU, formatMemory } from '@/utils/formatters'
import type { ResourceItem } from '@/types/kubernetes'

// ─── Keys Display ───────────────────────────────────────────────

function KeysList({ value }: { value: string }) {
  const keys = value.split(',').map(k => k.trim()).filter(Boolean)
  if (keys.length === 0) return <span className="text-kb-text-tertiary">—</span>
  return (
    <div className="flex flex-wrap gap-1">
      {keys.map((key) => (
        <span key={key} className="px-1.5 py-0.5 rounded bg-kb-elevated text-[10px] font-mono text-kb-text-secondary">
          {key}
        </span>
      ))}
    </div>
  )
}

// ─── Shared Helpers ──────────────────────────────────────────────

const kindToRoute: Record<string, string> = {
  Deployment: 'deployments', StatefulSet: 'statefulsets', DaemonSet: 'daemonsets',
  ReplicaSet: 'replicasets', Pod: 'pods', Service: 'services', Node: 'nodes',
  Ingress: 'ingresses', Job: 'jobs', CronJob: 'cronjobs', ConfigMap: 'configmaps',
  Secret: 'secrets', PersistentVolumeClaim: 'pvcs', PersistentVolume: 'pvs',
  HorizontalPodAutoscaler: 'hpas', HPA: 'hpas', StorageClass: 'storageclasses',
  Gateway: 'gateways', HTTPRoute: 'httproutes', Namespace: 'namespaces',
  PVC: 'pvcs', PV: 'pvs',
}

const routeToKind: Record<string, string> = Object.fromEntries(
  Object.entries(kindToRoute).map(([k, v]) => [v, k])
)

const resourceLabels: Record<string, string> = {
  pods: 'Pods', deployments: 'Deployments', statefulsets: 'StatefulSets',
  daemonsets: 'DaemonSets', jobs: 'Jobs', cronjobs: 'CronJobs', services: 'Services',
  ingresses: 'Ingresses', gateways: 'Gateways', httproutes: 'HTTPRoutes',
  endpoints: 'Endpoints', pvcs: 'PVCs', pvs: 'PVs', storageclasses: 'Storage Classes',
  configmaps: 'ConfigMaps', secrets: 'Secrets', hpas: 'HPAs', nodes: 'Nodes',
  namespaces: 'Namespaces', replicasets: 'ReplicaSets',
}

function ResourceLink({ name, namespace, resourceType }: { name: string; namespace?: string; resourceType: string }) {
  return (
    <Link to={`/${resourceType}/${namespace || '_'}/${name}`} className="text-status-info hover:underline font-mono text-[11px]">
      {name}
    </Link>
  )
}

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

function Section({ title, children, className }: { title: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={`bg-kb-card border border-kb-border rounded-[10px] p-5 ${className ?? ''}`}>
      <div className="text-sm font-semibold text-kb-text-primary mb-4">{title}</div>
      {children}
    </div>
  )
}

function renderValue(value: React.ReactNode): React.ReactNode {
  if (typeof value === 'object' && value !== null && !React.isValidElement(value)) return JSON.stringify(value)
  return value
}

function InfoField({ label, children }: { label: string; children: React.ReactNode }) {
  if (children === undefined || children === null || children === '') return null
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wide text-kb-text-tertiary mb-0.5">{label}</div>
      <div className="text-[12px] text-kb-text-primary">{renderValue(children)}</div>
    </div>
  )
}

function Labels({ labels }: { labels: Record<string, string> | undefined }) {
  if (!labels || Object.keys(labels).length === 0) return <span className="text-[11px] text-kb-text-tertiary">None</span>
  return (
    <div className="flex flex-wrap gap-1.5 overflow-hidden">
      {Object.entries(labels).map(([k, v]) => {
        const display = v.length > 80 ? v.slice(0, 80) + '\u2026' : v
        return (
          <span key={k} className="inline-flex px-2 py-0.5 rounded text-[10px] font-mono bg-kb-elevated text-kb-text-secondary max-w-full truncate" title={`${k}: ${v}`}>
            {k}: {display}
          </span>
        )
      })}
    </div>
  )
}

function extractSelector(selector: unknown): Record<string, string> | undefined {
  if (!selector || typeof selector !== 'object') return undefined
  const s = selector as Record<string, unknown>
  if (s.matchLabels && typeof s.matchLabels === 'object') return s.matchLabels as Record<string, string>
  const flat: Record<string, string> = {}
  for (const [k, v] of Object.entries(s)) { if (typeof v === 'string') flat[k] = v }
  return Object.keys(flat).length > 0 ? flat : undefined
}

function ComingSoon({ title, description }: { title: string; description: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-kb-text-tertiary">
      <Lock className="w-8 h-8 mb-3" />
      <div className="text-sm font-medium text-kb-text-secondary mb-1">{title}</div>
      <div className="text-xs">{description}</div>
    </div>
  )
}

// ─── Tab Definitions ─────────────────────────────────────────────

interface TabDef { id: string; label: string; count?: number; soon?: boolean }

function getTabsForResource(type: string, item: ResourceItem): TabDef[] {
  const containers = Array.isArray(item.containers) ? item.containers.length : 0
  const volumes = Array.isArray(item.volumes) ? item.volumes.length : 0
  const base: TabDef[] = [{ id: 'overview', label: 'Overview' }]

  switch (type) {
    case 'pods':
      base.push(
        { id: 'containers', label: 'Containers', count: containers },
        { id: 'yaml', label: 'YAML' },
        { id: 'logs', label: 'Logs' },
        { id: 'terminal', label: 'Terminal' },
        { id: 'files', label: 'Files' },
        { id: 'volumes', label: 'Volumes', count: volumes },
        { id: 'related', label: 'Related' },
        { id: 'events', label: 'Events' },
        { id: 'monitor', label: 'Monitor' },
      )
      break
    case 'deployments':
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'deploy-pods', label: 'Pods' },
        { id: 'deploy-logs', label: 'Logs' },
        { id: 'terminal', label: 'Terminal' },
        { id: 'related', label: 'Related' },
        { id: 'history', label: 'History' },
        { id: 'events', label: 'Events' },
        { id: 'monitor', label: 'Monitor' },
      )
      break
    case 'statefulsets':
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'sts-pods', label: 'Pods' },
        { id: 'sts-logs', label: 'Logs' },
        { id: 'terminal', label: 'Terminal' },
        { id: 'related', label: 'Related' },
        { id: 'history', label: 'History' },
        { id: 'events', label: 'Events' },
        { id: 'monitor', label: 'Monitor' },
      )
      break
    case 'daemonsets':
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'ds-pods', label: 'Pods' },
        { id: 'ds-logs', label: 'Logs' },
        { id: 'terminal', label: 'Terminal' },
        { id: 'related', label: 'Related' },
        { id: 'history', label: 'History' },
        { id: 'events', label: 'Events' },
        { id: 'monitor', label: 'Monitor' },
      )
      break
    case 'jobs':
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'job-pods', label: 'Pods' },
        { id: 'job-logs', label: 'Logs' },
        { id: 'related', label: 'Related' },
        { id: 'events', label: 'Events' },
      )
      break
    case 'cronjobs':
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'cronjob-jobs', label: 'Jobs' },
        { id: 'related', label: 'Related' },
        { id: 'events', label: 'Events' },
      )
      break
    case 'services':
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'related', label: 'Related' },
        { id: 'events', label: 'Events' },
      )
      break
    case 'nodes':
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'node-pods', label: 'Pods' },
        { id: 'events', label: 'Events' },
        { id: 'monitor', label: 'Monitor' },
      )
      break
    default:
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'events', label: 'Events' },
      )
  }
  return base
}

// ─── Status Overview Cards ───────────────────────────────────────

// LastTerminationField surfaces the previous run's termination
// reason on a pod's status overview. The visual weight is tuned by
// reason: OOMKilled goes red because it's actionable (memory limit
// or leak); Error goes amber because it could be transient; Completed
// reads neutral (Job-style). The relative time tail anchors "is this
// still relevant?" — a finishedAt from 4 days ago carries different
// urgency than one from 30 seconds ago.
function LastTerminationField({
  termination,
  containerName,
}: {
  termination: { reason?: string; finishedAt?: string; exitCode?: number }
  containerName?: string
}) {
  const reason = termination.reason || 'Unknown'
  const isOOM = reason === 'OOMKilled'
  const isError = reason === 'Error' || (termination.exitCode != null && termination.exitCode !== 0 && !isOOM && reason !== 'Completed')
  const tone = isOOM
    ? 'bg-status-error-dim text-status-error'
    : isError
      ? 'bg-status-warn-dim text-status-warn'
      : 'bg-kb-elevated text-kb-text-secondary'
  const ago = termination.finishedAt ? formatAge(termination.finishedAt) : null
  return (
    <div className="flex items-center gap-2 flex-wrap">
      <span className={`inline-flex items-center gap-1.5 px-2 py-0.5 rounded text-[10px] font-mono uppercase tracking-[0.04em] font-semibold ${tone}`}>
        {reason}
      </span>
      {termination.exitCode != null && (
        <span className="text-[10px] font-mono text-kb-text-tertiary">
          exit {termination.exitCode}
        </span>
      )}
      {ago && (
        <span className="text-[10px] font-mono text-kb-text-tertiary">
          {ago} ago{containerName ? ` · ${containerName}` : ''}
        </span>
      )}
    </div>
  )
}

function StatusOverview({ type, item }: { type: string; item: ResourceItem }) {
  const metrics: { label: string; value: React.ReactNode }[] = []

  switch (type) {
    case 'pods': {
      // Walk the pod's containers to find the most recent termination
      // across them. The k8s API only retains the previous run's outcome
      // per container (LastTerminationState), so this is a "what just
      // killed me" snapshot rather than a full history. The CapacityPage
      // OOMKill panel covers cluster-wide trend; here we surface the
      // pod-local signal so opening a crashlooping pod tells the
      // operator the cause without diving into kubectl describe.
      type LastTermination = {
        reason?: string
        finishedAt?: string
        exitCode?: number
      }
      type ContainerSlim = {
        name?: string
        state?: { lastTermination?: LastTermination }
      }
      const containers = (item.containers as ContainerSlim[] | undefined) ?? []
      type Pair = { name?: string; lt: LastTermination }
      const pairs: Pair[] = []
      for (const c of containers) {
        if (c.state?.lastTermination) {
          pairs.push({ name: c.name, lt: c.state.lastTermination })
        }
      }
      pairs.sort((a, b) => {
        const ta = a.lt.finishedAt ? new Date(a.lt.finishedAt).getTime() : 0
        const tb = b.lt.finishedAt ? new Date(b.lt.finishedAt).getTime() : 0
        return tb - ta
      })
      const lastTerm = pairs[0]

      metrics.push(
        { label: 'Phase', value: <div className="flex items-center gap-2"><span className={`w-2.5 h-2.5 rounded-full ${item.status === 'Running' ? 'bg-status-ok' : 'bg-status-warn'}`} />{item.status}</div> },
        { label: 'Ready Containers', value: String(item.ready ?? '-') },
        { label: 'Restart Count', value: String(item.restarts ?? 0) },
        { label: 'Node', value: item.nodeName ? <ResourceLink name={String(item.nodeName)} resourceType="nodes" /> : '-' },
      )
      if (lastTerm) {
        metrics.push({
          label: 'Last Termination',
          value: <LastTerminationField termination={lastTerm.lt} containerName={lastTerm.name} />,
        })
      }
      break
    }
    case 'deployments':
      metrics.push(
        { label: 'Status', value: <div className="flex items-center gap-2"><span className={`w-2.5 h-2.5 rounded-full ${item.status === 'Available' || item.status === 'Running' ? 'bg-status-ok' : 'bg-status-warn'}`} />{item.status}</div> },
        { label: 'Ready Replicas', value: `${item.readyReplicas ?? 0} / ${item.replicas ?? 0}` },
        { label: 'Updated Replicas', value: String(item.updatedReplicas ?? 0) },
        { label: 'Available Replicas', value: String(item.availableReplicas ?? 0) },
      )
      break
    case 'services':
      metrics.push(
        { label: 'Type', value: String(item.type ?? '-') },
        { label: 'Cluster IP', value: <span className="font-mono">{String(item.clusterIP ?? '-')}</span> },
        { label: 'Ports', value: Array.isArray(item.ports) ? (item.ports as Array<Record<string, unknown>>).map(p => `${p.port}/${p.protocol ?? 'TCP'}`).join(', ') : '-' },
        // Endpoints field hovers a tooltip with the ready/notReady
        // breakdown — it's the most operator-actionable signal on
        // this page (more so than ports), so it earns a status-overview
        // slot. Cell handles the gated cases (ExternalName / Headless /
        // KSM-not-scraping) inline so the field just always works.
        {
          label: 'Endpoints',
          value: (
            <EndpointHealthCell
              namespace={String(item.namespace ?? '')}
              name={String(item.name ?? '')}
              serviceType={String(item.type ?? '')}
              clusterIP={String(item.clusterIP ?? '')}
              variant="inline"
            />
          ),
        },
      )
      break
    case 'nodes':
      metrics.push(
        { label: 'Status', value: <div className="flex items-center gap-2"><span className={`w-2.5 h-2.5 rounded-full ${item.status === 'Ready' ? 'bg-status-ok' : 'bg-status-error'}`} />{item.status}</div> },
        { label: 'Kubelet', value: <span className="font-mono text-[11px]">{String(item.kubeletVersion ?? '-')}</span> },
        { label: 'CPU', value: item.cpuCapacity != null ? `${item.cpuCapacity} cores` : '-' },
        { label: 'Memory', value: item.memoryCapacity != null ? formatMemory(Number(item.memoryCapacity)) : '-' },
      )
      break
    default:
      metrics.push(
        { label: 'Status', value: <StatusBadge status={item.status} /> },
      )
  }

  return (
    <Section title="Status Overview">
      <div className={`grid grid-cols-${Math.min(metrics.length, 4)} gap-6`}>
        {metrics.map(m => (
          <div key={m.label}>
            <div className="text-[10px] uppercase tracking-wide text-kb-text-tertiary mb-1">{m.label}</div>
            <div className="text-sm text-kb-text-primary font-medium">{renderValue(m.value)}</div>
          </div>
        ))}
      </div>
    </Section>
  )
}

// ─── Overview Tab ────────────────────────────────────────────────

function OverviewTab({ type, item }: { type: string; item: ResourceItem }) {
  const { hasRole } = useAuth()
  const canEdit = hasRole('editor')
  const ownerRefs = Array.isArray(item.ownerReferences) ? item.ownerReferences as Array<Record<string, string>> : []

  return (
    <div className="space-y-4">
      <StatusOverview type={type} item={item} />

      {/* Resource Info */}
      <Section title={`${resourceLabels[type] ?? type} Information`}>
        <div className="grid grid-cols-2 gap-x-8 gap-y-4">
          <InfoField label="Created">
            {item.createdAt ? `${new Date(item.createdAt).toLocaleString()} (${formatAge(item.createdAt)})` : '-'}
          </InfoField>
          {type === 'pods' && (
            <InfoField label="Started">
              {item.startTime ? new Date(String(item.startTime)).toLocaleString() : '-'}
            </InfoField>
          )}
          {type === 'pods' && <InfoField label="Pod IP"><span className="font-mono">{String(item.ip ?? '-')}</span></InfoField>}
          {type === 'pods' && <InfoField label="Host IP"><span className="font-mono">{String(item.hostIP ?? '-')}</span></InfoField>}
          {ownerRefs.length > 0 && (
            <InfoField label="Owner">
              <KindNameLink value={`${ownerRefs[0].kind}/${ownerRefs[0].name}`} namespace={item.namespace} />
            </InfoField>
          )}
          {type === 'pods' && Array.isArray(item.containers) && (() => {
            const ports = (item.containers as Array<Record<string, unknown>>)
              .flatMap(c => Array.isArray(c.ports)
                ? (c.ports as Array<Record<string, unknown>>).map(p => ({
                    port: Number(p.containerPort),
                    container: String(c.name),
                  }))
                : []
              )
            return ports.length > 0 ? (
              <InfoField label="Ports">
                <div className="space-y-2">
                  <div className="flex flex-wrap gap-1.5">
                    {ports.map(({ port, container: ctr }) => (
                      <PortForwardButton key={`${ctr}-${port}`} namespace={item.namespace} pod={item.name} container={ctr} remotePort={port} disabled={!canEdit} />
                    ))}
                  </div>
                  <PortForwardNote />
                </div>
              </InfoField>
            ) : null
          })()}
          {type === 'pods' && <InfoField label="QoS Class">{String(item.qosClass ?? '-')}</InfoField>}

          {type === 'deployments' && <InfoField label="Strategy">{String(item.strategy ?? '-')}</InfoField>}
          {type === 'deployments' && <InfoField label="Replicas">{String(item.replicas ?? '-')}</InfoField>}
          {type === 'deployments' && item.selector != null && (
            <InfoField label="Selector">
              <Labels labels={extractSelector(item.selector)} />
            </InfoField>
          )}

          {type === 'services' && <InfoField label="Type">{String(item.type ?? '-')}</InfoField>}
          {type === 'services' && <InfoField label="Cluster IP"><span className="font-mono">{String(item.clusterIP ?? '-')}</span></InfoField>}
          {type === 'services' && item.selector != null && (
            <InfoField label="Selector"><Labels labels={extractSelector(item.selector)} /></InfoField>
          )}

          {type === 'nodes' && <InfoField label="OS Image">{String(item.osImage ?? '-')}</InfoField>}
          {type === 'nodes' && <InfoField label="Runtime">{String(item.containerRuntime ?? '-')}</InfoField>}
          {type === 'nodes' && <InfoField label="CPU Allocatable">{item.cpuAllocatable != null ? `${item.cpuAllocatable} cores` : '-'}</InfoField>}
          {type === 'nodes' && <InfoField label="Memory Allocatable">{item.memoryAllocatable != null ? formatMemory(Number(item.memoryAllocatable)) : '-'}</InfoField>}

          {type === 'statefulsets' && <InfoField label="Replicas">{`${item.readyReplicas ?? 0}/${item.replicas ?? 0} ready`}</InfoField>}
          {type === 'daemonsets' && <InfoField label="Desired / Ready">{`${item.desired ?? 0} / ${item.ready ?? 0}`}</InfoField>}
          {type === 'jobs' && <InfoField label="Succeeded / Failed">{`${item.succeeded ?? 0} / ${item.failed ?? 0}`}</InfoField>}
          {type === 'cronjobs' && <InfoField label="Schedule"><span className="font-mono">{String(item.schedule ?? '-')}</span></InfoField>}
          {type === 'cronjobs' && <InfoField label="Suspend">{String(item.suspend ?? false)}</InfoField>}
          {type === 'ingresses' && <InfoField label="Hosts"><span className="font-mono">{String(item.hosts ?? '-')}</span></InfoField>}
          {type === 'configmaps' && <InfoField label="Keys"><KeysList value={String(item.keys ?? '-')} /></InfoField>}
          {type === 'configmaps' && <InfoField label="Data Count">{String(item.dataCount ?? 0)}</InfoField>}
          {type === 'secrets' && <InfoField label="Type"><span className="font-mono">{String(item.type ?? '-')}</span></InfoField>}
          {type === 'secrets' && <InfoField label="Keys"><KeysList value={String(item.keys ?? '-')} /></InfoField>}
          {type === 'pvcs' && item.volumeName != null && <InfoField label="Volume"><ResourceLink name={String(item.volumeName)} resourceType="pvs" /></InfoField>}
          {type === 'pvcs' && item.storageClass != null && <InfoField label="Storage Class"><ResourceLink name={String(item.storageClass)} resourceType="storageclasses" /></InfoField>}
          {type === 'pvcs' && <InfoField label="Capacity">{String(item.capacity ?? '-')}</InfoField>}
          {type === 'pvs' && <InfoField label="Capacity">{String(item.capacity ?? '-')}</InfoField>}
          {type === 'pvs' && item.storageClass != null && <InfoField label="Storage Class"><ResourceLink name={String(item.storageClass)} resourceType="storageclasses" /></InfoField>}
          {type === 'pvs' && <InfoField label="Reclaim Policy">{String(item.reclaimPolicy ?? '-')}</InfoField>}
          {type === 'hpas' && <InfoField label="Min / Max Replicas">{`${item.minReplicas ?? '-'} / ${item.maxReplicas ?? '-'}`}</InfoField>}
          {type === 'hpas' && item.targetRef != null && <InfoField label="Target"><KindNameLink value={String(item.targetRef)} namespace={item.namespace} /></InfoField>}
          {type === 'storageclasses' && <InfoField label="Provisioner"><span className="font-mono">{String(item.provisioner ?? '-')}</span></InfoField>}
          {type === 'storageclasses' && <InfoField label="Reclaim Policy">{String(item.reclaimPolicy ?? '-')}</InfoField>}
          {type === 'gateways' && <InfoField label="Class">{String(item.class ?? '-')}</InfoField>}
          {type === 'httproutes' && item.gateway != null && <InfoField label="Gateway"><ResourceLink name={String(item.gateway)} namespace={String(item.gatewayNamespace ?? item.namespace)} resourceType="gateways" /></InfoField>}
        </div>

        {/* Labels & Annotations */}
        <div className="grid grid-cols-2 gap-x-8 gap-y-4 mt-5 pt-4 border-t border-kb-border">
          <InfoField label="Labels"><Labels labels={item.labels} /></InfoField>
          <InfoField label="Annotations"><Labels labels={item.annotations} /></InfoField>
        </div>
      </Section>

      {/* Node load — the two views of the same node belong together:
          (1) Allocation: what the scheduler reserved (sum of pod
              requests / limits vs allocatable). Answers "can a new pod
              fit?" and "is this node oversubscribed?".
          (2) Resource Usage: what's actually being consumed right now
              from metrics-server / kubelet. Answers "is the node hot?".
          Top consumers comes after because it's the drill-down: "who's
          behind those numbers?". */}
      {type === 'nodes' && <NodeAllocationSection item={item} />}
      <MetricsBar item={item} />
      {type === 'nodes' && <NodeTopConsumersSection item={item} />}

      {/* Conditions */}
      <ConditionsSection conditions={item.conditions} />
    </div>
  )
}

function NodeAllocationSection({ item }: { item: ResourceItem }) {
  const cpuReq = Number(item.cpuRequested ?? 0)
  const cpuAlloc = Number(item.cpuAllocatable ?? 0)
  const cpuLim = Number(item.cpuLimitSum ?? 0)
  const memReq = Number(item.memoryRequested ?? 0)
  const memAlloc = Number(item.memoryAllocatable ?? 0)
  const memLim = Number(item.memoryLimitSum ?? 0)
  const podCount = Number(item.podCount ?? 0)
  const maxPods = Number(item.maxPods ?? 110)
  const unschedulable = Boolean(item.unschedulable)
  if (cpuAlloc === 0 && memAlloc === 0) return null

  const cpuReqPct = cpuAlloc > 0 ? (cpuReq / cpuAlloc) * 100 : 0
  const cpuLimPct = cpuAlloc > 0 ? (cpuLim / cpuAlloc) * 100 : 0
  const memReqPct = memAlloc > 0 ? (memReq / memAlloc) * 100 : 0
  const memLimPct = memAlloc > 0 ? (memLim / memAlloc) * 100 : 0
  const podPct = maxPods > 0 ? (podCount / maxPods) * 100 : 0

  return (
    <Section title="Allocation">
      <div className="space-y-4">
        <AllocationBar
          label="CPU"
          reqLabel={`${cpuReq}m`}
          allocLabel={`${cpuAlloc}m`}
          limLabel={cpuLim > 0 ? `${cpuLim}m` : ''}
          reqPct={cpuReqPct}
          limPct={cpuLimPct}
        />
        <AllocationBar
          label="Memory"
          reqLabel={formatMemory(memReq)}
          allocLabel={formatMemory(memAlloc)}
          limLabel={memLim > 0 ? formatMemory(memLim) : ''}
          reqPct={memReqPct}
          limPct={memLimPct}
        />
        <div className="flex items-center gap-3 text-[11px] pt-2 border-t border-kb-border">
          <span className="text-kb-text-tertiary uppercase tracking-wider text-[10px]">Pods</span>
          <span className={`font-mono ${podPct > 90 ? 'text-status-warn' : 'text-kb-text-primary'}`}>
            {podCount} / {maxPods} <span className="text-kb-text-tertiary">({podPct.toFixed(0)}%)</span>
          </span>
          <span className="ml-auto text-kb-text-tertiary uppercase tracking-wider text-[10px]">Schedulable</span>
          {unschedulable
            ? <span className="font-mono text-status-warn">cordoned</span>
            : <span className="font-mono text-status-ok">yes</span>
          }
        </div>
      </div>
    </Section>
  )
}

function AllocationBar({
  label, reqLabel, allocLabel, limLabel, reqPct, limPct,
}: {
  label: string
  reqLabel: string
  allocLabel: string
  limLabel: string
  reqPct: number
  limPct: number
}) {
  // Color the request bar by % of allocatable: green<75, amber 75–90, red >90.
  // A node at 100% requests can't accept new schedulable pods regardless of
  // actual usage, so the threshold is on requests, not measured load.
  const reqColor = reqPct >= 90 ? 'bg-status-error' : reqPct >= 75 ? 'bg-status-warn' : 'bg-status-info'
  // Limits over 100% indicate overcommit — render the overage as a faded
  // red overlay extending past the bar end.
  const overcommit = Math.max(0, limPct - 100)
  return (
    <div>
      <div className="flex justify-between items-baseline text-[11px] mb-1.5">
        <span className="text-kb-text-tertiary uppercase tracking-wider text-[10px]">{label}</span>
        <span className="font-mono text-kb-text-secondary">
          {reqLabel} / {allocLabel} requested
          <span className={reqPct >= 90 ? 'text-status-error font-semibold ml-1' : 'text-kb-text-tertiary ml-1'}>
            ({reqPct.toFixed(0)}%)
          </span>
          {limLabel && (
            <>
              <span className="text-kb-text-tertiary mx-2">·</span>
              limits {limLabel}
              <span className={limPct > 100 ? 'text-status-error font-semibold ml-1' : 'text-kb-text-tertiary ml-1'}>
                ({limPct.toFixed(0)}%)
              </span>
            </>
          )}
        </span>
      </div>
      <div className="relative h-2 rounded-full bg-kb-elevated overflow-hidden">
        <div
          className={`absolute left-0 top-0 h-full ${reqColor} transition-all`}
          style={{ width: `${Math.min(100, reqPct)}%` }}
        />
        {overcommit > 0 && (
          <div className="absolute inset-y-0 right-0 left-0 pointer-events-none">
            <div className="absolute right-0 top-0 h-full bg-status-error/25" style={{ width: `${Math.min(40, overcommit / 5)}%` }} />
          </div>
        )}
      </div>
    </div>
  )
}

function NodeTopConsumersSection({ item }: { item: ResourceItem }) {
  const topCpu = Array.isArray(item.topCpuConsumers) ? item.topCpuConsumers as Array<Record<string, unknown>> : []
  const topMem = Array.isArray(item.topMemConsumers) ? item.topMemConsumers as Array<Record<string, unknown>> : []
  if (topCpu.length === 0 && topMem.length === 0) return null

  return (
    <Section title="Top consumers (by request)">
      <div className="grid grid-cols-2 gap-8">
        <div>
          <div className="text-[10px] text-kb-text-tertiary uppercase tracking-wider mb-2">CPU</div>
          {topCpu.length === 0
            ? <div className="text-[11px] text-kb-text-tertiary italic">— no requests set</div>
            : topCpu.map((p, i) => (
              <div key={i} className="flex items-center justify-between text-[11px] py-1 border-b border-kb-border last:border-b-0">
                <ResourceLink name={String(p.name)} namespace={String(p.namespace)} resourceType="pods" />
                <span className="font-mono text-kb-text-secondary">{Number(p.cpuRequest)}m</span>
              </div>
            ))
          }
        </div>
        <div>
          <div className="text-[10px] text-kb-text-tertiary uppercase tracking-wider mb-2">Memory</div>
          {topMem.length === 0
            ? <div className="text-[11px] text-kb-text-tertiary italic">— no requests set</div>
            : topMem.map((p, i) => (
              <div key={i} className="flex items-center justify-between text-[11px] py-1 border-b border-kb-border last:border-b-0">
                <ResourceLink name={String(p.name)} namespace={String(p.namespace)} resourceType="pods" />
                <span className="font-mono text-kb-text-secondary">{formatMemory(Number(p.memoryRequest))}</span>
              </div>
            ))
          }
        </div>
      </div>
    </Section>
  )
}

function MetricsBar({ item }: { item: ResourceItem }) {
  const cpuUsage = Number(item.cpuUsage ?? 0)
  const cpuRequest = Number(item.cpuRequest ?? 0)
  const cpuLimit = Number(item.cpuLimit ?? 0)
  const cpuPercent = Number(item.cpuPercent ?? 0)
  const memUsage = Number(item.memoryUsage ?? 0)
  const memRequest = Number(item.memoryRequest ?? 0)
  const memLimit = Number(item.memoryLimit ?? 0)
  const memPercent = Number(item.memoryPercent ?? 0)

  const hasCpu = cpuUsage > 0 || cpuRequest > 0
  const hasMem = memUsage > 0 || memRequest > 0
  if (!hasCpu && !hasMem) return null

  return (
    <Section title="Resource Usage">
      <div className="grid grid-cols-2 gap-6">
        {hasCpu && (
          <div>
            <div className="flex justify-between mb-2">
              <span className="text-[10px] text-kb-text-tertiary">CPU</span>
            </div>
            <ResourceUsageCell usage={cpuUsage} request={cpuRequest} limit={cpuLimit} percent={cpuPercent} type="cpu" size="lg" />
          </div>
        )}
        {hasMem && (
          <div>
            <div className="flex justify-between mb-2">
              <span className="text-[10px] text-kb-text-tertiary">Memory</span>
            </div>
            <ResourceUsageCell usage={memUsage} request={memRequest} limit={memLimit} percent={memPercent} type="memory" size="lg" />
          </div>
        )}
      </div>
    </Section>
  )
}

function ConditionsSection({ conditions }: { conditions: unknown }) {
  if (!conditions || !Array.isArray(conditions) || conditions.length === 0) return null
  return (
    <Section title="Conditions">
      <div className="space-y-2">
        {(conditions as Array<Record<string, unknown>>).map((c, i) => (
          <div key={i} className="flex items-center gap-3 py-1.5">
            <StatusBadge
              status={c.status === 'True' ? 'Running' : 'Warning'}
              label={String(c.type)}
            />
            <span className="flex-1 text-[11px] text-kb-text-secondary truncate">
              {c.message ? String(c.message) : ''}
            </span>
            <span className="text-[10px] font-mono text-kb-text-tertiary shrink-0">
              {c.lastTransitionTime ? new Date(String(c.lastTransitionTime)).toLocaleString() : ''}
            </span>
          </div>
        ))}
      </div>
    </Section>
  )
}

// ─── Containers Tab ──────────────────────────────────────────────

function ContainersTab({ item }: { item: ResourceItem }) {
  const containers = Array.isArray(item.containers) ? item.containers as Array<Record<string, unknown>> : []
  if (containers.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No containers</div>

  return (
    <div className="space-y-4">
      {containers.map((c, i) => {
        const state = c.state as Record<string, unknown> | undefined
        const stateLabel = state?.state ? String(state.state) : 'unknown'
        const ready = state?.ready === true
        const resources = c.resources as Record<string, unknown> | undefined
        const mounts = Array.isArray(c.volumeMounts) ? c.volumeMounts as Array<Record<string, unknown>> : []

        return (
          <Section key={String(c.name ?? i)} title="">
            <div className="flex items-center justify-between mb-4">
              <div className="flex items-center gap-2">
                <span className="px-2.5 py-1 rounded text-xs font-medium bg-status-info text-white">{String(c.name)}</span>
                <span className="text-[11px] font-mono text-kb-text-secondary">{String(c.image ?? '')}</span>
              </div>
              <StatusBadge status={ready ? 'Running' : 'Warning'} label={ready ? 'Ready' : 'Not Ready'} />
            </div>

            <div className="grid grid-cols-2 gap-x-8 gap-y-3">
              <InfoField label="Image"><span className="font-mono text-[11px]">{String(c.image ?? '-')}</span></InfoField>
              <InfoField label="Image Pull Policy">{String(c.imagePullPolicy ?? '-')}</InfoField>
              <InfoField label="State">
                <div className="flex items-center gap-2">
                  <StatusBadge status={stateLabel === 'running' ? 'Running' : stateLabel === 'waiting' ? 'Warning' : 'Terminated'} label={stateLabel} />
                  {state?.startedAt != null && <span className="text-[10px] text-kb-text-tertiary">since {new Date(String(state.startedAt)).toLocaleString()}</span>}
                </div>
              </InfoField>
              <InfoField label="Restart Count">
                <div className="flex items-center gap-3">
                  <span>{String(state?.restartCount ?? 0)}</span>
                  <RestartHistorySparkline
                    namespace={String(item.namespace)}
                    pod={String(item.name)}
                    container={String(c.name)}
                  />
                </div>
              </InfoField>
            </div>

            {resources && (
              <div className="mt-4 pt-3 border-t border-kb-border">
                <div className="text-[10px] uppercase tracking-wide text-kb-text-tertiary mb-2">Resources</div>
                <div className="grid grid-cols-4 gap-3">
                  <InfoField label="CPU Request">{resources.cpuRequest != null ? formatCPU(Number(resources.cpuRequest)) : '-'}</InfoField>
                  <InfoField label="CPU Limit">{resources.cpuLimit != null ? formatCPU(Number(resources.cpuLimit)) : '-'}</InfoField>
                  <InfoField label="Memory Request">{resources.memoryRequest != null ? formatMemory(Number(resources.memoryRequest)) : '-'}</InfoField>
                  <InfoField label="Memory Limit">{resources.memoryLimit != null ? formatMemory(Number(resources.memoryLimit)) : '-'}</InfoField>
                </div>
              </div>
            )}

            {mounts.length > 0 && (
              <div className="mt-4 pt-3 border-t border-kb-border">
                <div className="text-[10px] uppercase tracking-wide text-kb-text-tertiary mb-2">Volume Mounts</div>
                <table className="w-full text-[11px]">
                  <thead>
                    <tr className="text-kb-text-tertiary text-left">
                      <th className="pb-1.5 font-normal">Name</th>
                      <th className="pb-1.5 font-normal">Mount Path</th>
                      <th className="pb-1.5 font-normal">Read Only</th>
                    </tr>
                  </thead>
                  <tbody className="font-mono text-kb-text-secondary">
                    {mounts.map((m, mi) => (
                      <tr key={mi} className="border-t border-kb-border">
                        <td className="py-1.5">{String(m.name ?? '-')}</td>
                        <td className="py-1.5">{String(m.mountPath ?? '-')}</td>
                        <td className="py-1.5">{m.readOnly ? <span className="text-status-warn">RO</span> : 'RW'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Section>
        )
      })}
    </div>
  )
}

// ─── YAML Tab ────────────────────────────────────────────────────

function highlightLogLine(line: string): React.ReactNode {
  // Error levels in red
  if (/\b(ERROR|FATAL|PANIC|error|fatal|panic)\b/.test(line)) {
    return <span className="text-[#f97583]">{line}</span>
  }
  // Warnings in yellow
  if (/\b(WARN|WARNING|warn|warning)\b/.test(line)) {
    return <span className="text-[#f0b72f]">{line}</span>
  }
  // Debug in dim
  if (/\b(DEBUG|TRACE|debug|trace)\b/.test(line)) {
    return <span className="text-[#6a737d]">{line}</span>
  }
  // Highlight timestamps at start of line
  const tsMatch = line.match(/^(\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}[^\s]*)(.*)$/)
  if (tsMatch) {
    return <><span className="text-[#79c0ff]">{tsMatch[1]}</span><span className="text-[#b5e5a4]">{tsMatch[2]}</span></>
  }
  // Default: green tint for terminal feel
  return <span className="text-[#b5e5a4]">{line}</span>
}

function LogOutput({ logs, logsEndRef }: { logs: string | undefined; logsEndRef: React.RefObject<HTMLDivElement> }) {
  if (logs === undefined) return null
  if (!logs) return <pre className="p-4 text-[11px] font-mono text-kb-text-tertiary">No logs available</pre>
  const lines = logs.split('\n')
  return (
    <pre className="p-4 text-[11px] font-mono leading-5 overflow-auto max-h-[600px]">
      {lines.map((line, i) => (
        <div key={i}>{highlightLogLine(line)}</div>
      ))}
      <div ref={logsEndRef} />
    </pre>
  )
}

function highlightYAMLLine(line: string): React.ReactNode {
  // Comment lines
  if (/^\s*#/.test(line)) {
    return <span className="yaml-comment">{line}</span>
  }

  // Key: value lines
  const kvMatch = line.match(/^(\s*)([\w.\-/]+)(:)(.*)$/)
  if (kvMatch) {
    const [, indent, key, colon, rest] = kvMatch
    return (
      <>
        <span>{indent}</span>
        <span className="yaml-key">{key}</span>
        <span>{colon}</span>
        {highlightValue(rest)}
      </>
    )
  }

  // List items with key: value
  const listKvMatch = line.match(/^(\s*-\s+)([\w.\-/]+)(:)(.*)$/)
  if (listKvMatch) {
    const [, prefix, key, colon, rest] = listKvMatch
    return (
      <>
        <span>{prefix}</span>
        <span className="yaml-key">{key}</span>
        <span>{colon}</span>
        {highlightValue(rest)}
      </>
    )
  }

  // List items with plain value
  const listMatch = line.match(/^(\s*-\s+)(.*)$/)
  if (listMatch) {
    const [, prefix, val] = listMatch
    return (
      <>
        <span>{prefix}</span>
        {highlightValue(' ' + val)}
      </>
    )
  }

  return <span>{line}</span>
}

function highlightValue(raw: string): React.ReactNode {
  const trimmed = raw.trim()
  if (!trimmed || trimmed === '') return <span>{raw}</span>

  // Quoted strings
  if (/^["'].*["']$/.test(trimmed)) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-string">{trimmed}</span></>
  }

  // Booleans
  if (/^(true|false)$/i.test(trimmed)) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-bool">{trimmed}</span></>
  }

  // Null
  if (/^(null|~)$/i.test(trimmed)) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-null">{trimmed}</span></>
  }

  // Numbers
  if (/^-?\d+(\.\d+)?([eE][+-]?\d+)?$/.test(trimmed)) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-number">{trimmed}</span></>
  }

  // Plain strings (unquoted)
  if (trimmed.length > 0) {
    const leading = raw.slice(0, raw.indexOf(trimmed))
    return <><span>{leading}</span><span className="yaml-string">{trimmed}</span></>
  }

  return <span>{raw}</span>
}

function highlightDescribeLine(line: string): React.ReactNode {
  const trimmed = line.trimStart()
  const indent = line.slice(0, line.length - trimmed.length)

  // Empty lines
  if (trimmed.length === 0) return <span>{line}</span>

  // Event lines (table rows with Normal/Warning)
  if (/^\s*(Normal|Warning)\s/.test(line)) {
    const isWarning = line.includes('Warning')
    return <span className={isWarning ? 'text-status-warn' : 'text-status-ok'}>{line}</span>
  }

  // Key-only lines (end with ":" and optional whitespace, no value)
  // e.g., "Containers:", "  application-controller:", "  Args:", "  Environment:"
  const keyOnlyMatch = line.match(/^(\s*)([\w][\w\s/.-]*?)(:\s*)$/)
  if (keyOnlyMatch) {
    const [, ind, key, colon] = keyOnlyMatch
    const isBold = ind.length <= 2
    return (
      <>
        <span>{ind}</span>
        <span className={`yaml-key ${isBold ? 'font-semibold' : ''}`}>{key}</span>
        <span>{colon}</span>
      </>
    )
  }

  // Key: value — kubectl describe uses aligned columns.
  // Top-level fields (0-2 indent): match "Key: value" with 1+ space after colon
  // Deeper fields (3+ indent): require 2+ spaces after colon to avoid matching
  // annotation continuations like "meta.helm.sh/release-name: value"
  const minSpaces = indent.length <= 2 ? 1 : 2
  const kvRegex = new RegExp(`^(\\s*)(\\w[\\w\\s/.-]*?)(:\\s{${minSpaces},})(.*)$`)
  const kvMatch = line.match(kvRegex)
  if (kvMatch) {
    const [, ind, key, colon, value] = kvMatch
    const isBold = ind.length <= 2
    return (
      <>
        <span>{ind}</span>
        <span className={`yaml-key ${isBold ? 'font-semibold' : ''}`}>{key}</span>
        <span>{colon}</span>
        {value && <span className="yaml-string">{value}</span>}
      </>
    )
  }

  // Continuation/value lines (indented, not matching key: pattern)
  if (indent.length >= 2) {
    return <span className="yaml-string">{line}</span>
  }

  return <span>{line}</span>
}

function DeleteModal({ type, namespace, name, onClose, onDeleted }: {
  type: string; namespace: string; name: string; onClose: () => void; onDeleted: () => void
}) {
  const [confirmText, setConfirmText] = useState('')
  const [forceDelete, setForceDelete] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<MutationErrorVariant | null>(null)

  const resourceLabel = resourceLabels[type] ? resourceLabels[type].replace(/s$/, '') : type
  const canDelete = confirmText === name

  useEffect(() => {
    function handleEsc(e: KeyboardEvent) { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', handleEsc)
    return () => document.removeEventListener('keydown', handleEsc)
  }, [onClose])

  async function handleDelete() {
    setDeleting(true)
    setError(null)
    try {
      await api.deleteResource(type, namespace, name, { force: forceDelete })
      onDeleted()
    } catch (err) {
      setError(classifyMutationError(err))
      setDeleting(false)
    }
  }

  return createPortal(
    <div className="fixed inset-0 z-[99999] flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-black/70 backdrop-blur-sm" />
      <div
        className="relative w-[90vw] max-w-md bg-kb-card border border-kb-border rounded-xl shadow-2xl flex flex-col overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="px-5 py-4 flex items-start justify-between">
          <div className="flex items-start gap-3">
            <div className="w-8 h-8 rounded-lg bg-status-error-dim flex items-center justify-center shrink-0 mt-0.5">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className="text-status-error">
                <path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/>
              </svg>
            </div>
            <div>
              <h4 className="text-sm font-semibold text-kb-text-primary">Delete {resourceLabel}</h4>
              <p className="text-[11px] text-kb-text-tertiary">This action cannot be undone.</p>
            </div>
          </div>
          <button onClick={onClose} className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M18 6L6 18M6 6l12 12"/></svg>
          </button>
        </div>

        {/* Resource info */}
        <div className="mx-5 px-3 py-2.5 rounded-lg bg-status-error-dim/30 border border-status-error/10">
          <div className="text-[11px] font-semibold text-status-error mb-1">You are about to delete:</div>
          <div className="text-[11px] font-mono text-kb-text-secondary space-y-0.5">
            <div>Name: <span className="text-kb-text-primary">{name}</span></div>
            <div>Type: <span className="text-kb-text-primary">{type}</span></div>
            {namespace && namespace !== '_' && <div>Namespace: <span className="text-kb-text-primary">{namespace}</span></div>}
          </div>
        </div>

        {/* Confirmation input */}
        <div className="px-5 pt-4">
          <label className="text-[11px] text-kb-text-secondary block mb-1.5">
            Type <span className="font-mono font-semibold text-kb-text-primary">{name}</span> to confirm:
          </label>
          <input
            type="text"
            value={confirmText}
            onChange={e => setConfirmText(e.target.value)}
            placeholder={name}
            autoFocus
            className="w-full px-3 py-2 text-xs font-mono bg-kb-bg border border-kb-border rounded-lg text-kb-text-primary outline-none focus:border-status-error/50 transition-colors"
          />
        </div>

        {/* Options */}
        <div className="px-5 pt-3 space-y-2">
          <label className="flex items-start gap-2 cursor-pointer">
            <input
              type="checkbox"
              checked={forceDelete}
              onChange={e => setForceDelete(e.target.checked)}
              className="mt-0.5 rounded"
            />
            <div>
              <div className="text-[11px] text-kb-text-secondary">Force delete (grace period = 0)</div>
              <div className="text-[10px] text-kb-text-tertiary">If finalizers exist and resource is not deleted after 3 seconds, finalizers will be removed automatically</div>
            </div>
          </label>
        </div>

        {/* Error — classified so reader-mode 403s render a friendly
            tier hint with a link to Configure instead of raw apiserver
            text. */}
        {error && (
          <div className="mx-5 mt-3 px-3 py-2.5 rounded-lg bg-status-error-dim border border-status-error/20 text-xs text-kb-text-primary">
            {error.title && <div className="font-semibold text-status-error mb-1">{error.title}</div>}
            <div className="text-kb-text-secondary leading-relaxed">{error.body}</div>
            {error.cta && (
              <Link
                to={error.cta.to}
                onClick={onClose}
                className="inline-flex items-center gap-1.5 mt-2 px-2.5 py-1 rounded bg-kb-accent text-kb-on-accent text-[11px] font-medium hover:bg-kb-accent-hover transition-colors"
              >
                {error.cta.label}
              </Link>
            )}
            {error.detail && (
              <details className="mt-1.5">
                <summary className="text-[10px] font-mono text-kb-text-tertiary cursor-pointer">Server error</summary>
                <pre className="mt-1 text-[10px] font-mono text-kb-text-tertiary whitespace-pre-wrap break-all">{error.detail}</pre>
              </details>
            )}
          </div>
        )}

        {/* Actions */}
        <div className="px-5 py-4 flex gap-2 justify-end">
          <button
            onClick={onClose}
            className="px-4 py-2 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={handleDelete}
            disabled={!canDelete || deleting}
            className="px-4 py-2 text-xs font-medium bg-status-error text-white rounded-lg hover:bg-status-error/90 transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            {deleting ? 'Deleting...' : 'Delete'}
          </button>
        </div>
      </div>
    </div>,
    document.body
  )
}

function DescribeModal({ type, namespace, name, onClose }: { type: string; namespace: string; name: string; onClose: () => void }) {
  const { data: output, isLoading, error } = useResourceDescribe(type, namespace, name, true)

  useEffect(() => {
    function handleEsc(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleEsc)
    return () => document.removeEventListener('keydown', handleEsc)
  }, [onClose])

  return createPortal(
    <div className="fixed inset-0 z-[99999] flex items-center justify-center" onClick={onClose}>
      <div className="absolute inset-0 bg-black/70 backdrop-blur-sm" />
      <div
        className="relative w-[90vw] max-w-5xl max-h-[85vh] bg-kb-card border border-kb-border rounded-xl shadow-2xl flex flex-col overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="px-5 py-3 border-b border-kb-border flex items-center justify-between shrink-0">
          <div className="flex items-center gap-3">
            <span className="text-[10px] font-mono uppercase tracking-[0.08em] text-kb-text-tertiary bg-kb-elevated px-2 py-0.5 rounded">kubectl describe</span>
            <span className="text-sm text-kb-text-primary font-medium">{name}</span>
          </div>
          <button
            onClick={onClose}
            className="p-1 rounded hover:bg-kb-elevated text-kb-text-tertiary hover:text-kb-text-primary transition-colors"
          >
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M18 6L6 18M6 6l12 12"/></svg>
          </button>
        </div>

        {/* Content */}
        <div className="flex-1 overflow-auto p-4" style={{ backgroundColor: '#0d1117', color: '#c9d1d9' }}>
          {isLoading && (
            <div className="py-12 text-center text-sm text-kb-text-tertiary">Loading describe output...</div>
          )}
          {error && (
            <div className="py-12 text-center text-sm text-status-error">{(error as Error).message}</div>
          )}
          {output && (
            <pre className="text-[11px] font-mono leading-5">
              {output.split('\n').map((line, i) => (
                <div key={i} className="flex">
                  <span className="w-10 text-right pr-3 select-none shrink-0" style={{ color: '#484f58' }}>{i + 1}</span>
                  <span className="flex-1">{highlightDescribeLine(line)}</span>
                </div>
              ))}
            </pre>
          )}
        </div>
      </div>
    </div>,
    document.body
  )
}

function YAMLEditor({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const editorRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)

  useEffect(() => {
    if (!editorRef.current) return

    const view = new EditorView({
      doc: value,
      extensions: [
        yaml(),
        oneDark,
        EditorView.updateListener.of(update => {
          if (update.docChanged) {
            onChange(update.state.doc.toString())
          }
        }),
        EditorView.theme({
          '&': { fontSize: '11px', maxHeight: '600px' },
          '.cm-scroller': { overflow: 'auto', fontFamily: "'JetBrains Mono', 'Fira Code', Menlo, monospace" },
          '.cm-content': { padding: '12px 0' },
          '.cm-gutters': { backgroundColor: '#0d1117', border: 'none' },
          '&.cm-editor': { backgroundColor: '#0d1117', borderRadius: '8px' },
        }),
        lineNumbers(),
      ],
      parent: editorRef.current,
    })

    viewRef.current = view

    return () => {
      view.destroy()
      viewRef.current = null
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return <div ref={editorRef} className="rounded-lg overflow-hidden border border-kb-border" />
}

function YAMLTab({ type, namespace, name, canEdit }: { type: string; namespace: string; name: string; canEdit: boolean }) {
  const { data: yaml, isLoading, error, refetch } = useResourceYAML(type, namespace, name)
  const [editing, setEditing] = useState(false)
  const [editValue, setEditValue] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const lines = (yaml ?? '').split('\n')

  function startEdit() {
    setEditValue(yaml ?? '')
    setSaveError(null)
    setEditing(true)
  }

  function cancelEdit() {
    setEditing(false)
    setSaveError(null)
  }

  async function saveEdit() {
    setSaving(true)
    setSaveError(null)
    try {
      await api.applyResourceYAML(type, namespace, name, editValue)
      setEditing(false)
      refetch()
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : 'Failed to apply YAML')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Section title="YAML Configuration" className="relative">
      <div className="absolute top-4 right-5 flex gap-2">
        {editing ? (
          <>
            <button
              onClick={cancelEdit}
              className="px-3 py-1.5 text-[10px] font-mono bg-kb-elevated text-kb-text-secondary rounded hover:bg-kb-card-hover transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={saveEdit}
              disabled={saving || !canEdit}
              title={!canEdit ? 'Editor role required' : undefined}
              className="px-3 py-1.5 text-[10px] font-mono bg-status-info text-white rounded hover:bg-status-info/90 transition-colors disabled:opacity-50 flex items-center gap-1"
            >
              {saving ? 'Applying...' : 'Apply'}
            </button>
          </>
        ) : (
          <>
            <button
              onClick={() => {
                navigator.clipboard.writeText(yaml ?? '')
                setCopied(true)
                setTimeout(() => setCopied(false), 2000)
              }}
              className="px-3 py-1.5 text-[10px] font-mono bg-kb-elevated text-kb-text-secondary rounded hover:bg-kb-card-hover transition-colors"
            >
              {copied ? 'Copied!' : 'Copy'}
            </button>
            <button
              onClick={() => {
                const blob = new Blob([yaml ?? ''], { type: 'application/yaml' })
                const url = URL.createObjectURL(blob)
                const a = document.createElement('a')
                a.href = url
                a.download = `${name}.yaml`
                a.click()
                URL.revokeObjectURL(url)
              }}
              className="px-3 py-1.5 text-[10px] font-mono bg-kb-elevated text-kb-text-secondary rounded hover:bg-kb-card-hover transition-colors"
            >
              Download
            </button>
            <button
              onClick={startEdit}
              disabled={!canEdit}
              title={!canEdit ? 'Editor role required' : undefined}
              className="px-3 py-1.5 text-[10px] font-mono bg-kb-elevated text-kb-text-secondary rounded hover:bg-kb-card-hover transition-colors disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-kb-elevated"
            >
              Edit
            </button>
          </>
        )}
      </div>

      {saveError && (
        <div className="mb-2 px-3 py-2 rounded bg-status-error-dim border border-status-error/20 text-xs text-status-error font-mono">
          {saveError}
        </div>
      )}

      {editing ? (
        <YAMLEditor value={editValue} onChange={setEditValue} />
      ) : (
        <div className="overflow-auto max-h-[600px] rounded-lg p-3" style={{ backgroundColor: '#0d1117', color: '#c9d1d9' }}>
          <pre className="text-[11px] font-mono leading-5 whitespace-pre-wrap break-all">
            {lines.map((line, i) => (
              <div key={i} className="flex">
                <span className="w-10 text-right pr-3 select-none shrink-0" style={{ color: '#484f58' }}>{i + 1}</span>
                <span className="flex-1 min-w-0">{highlightYAMLLine(line)}</span>
              </div>
            ))}
          </pre>
        </div>
      )}
    </Section>
  )
}

// ─── Volumes Tab ─────────────────────────────────────────────────

function VolumesTab({ item }: { item: ResourceItem }) {
  const volumes = Array.isArray(item.volumes) ? item.volumes as Array<Record<string, unknown>> : []
  if (volumes.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No volumes</div>

  const containers = Array.isArray(item.containers) ? item.containers as Array<Record<string, unknown>> : []

  return (
    <Section title="Volumes">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Type</th>
            <th className="pb-2 font-normal">Details</th>
            <th className="pb-2 font-normal">Volume Mounts</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {volumes.map((vol, i) => {
            const mounts: string[] = []
            containers.forEach(c => {
              const vm = Array.isArray(c.volumeMounts) ? c.volumeMounts as Array<Record<string, unknown>> : []
              vm.forEach(m => {
                if (m.name === vol.name) {
                  mounts.push(`${String(c.name)} \u2192 ${String(m.mountPath)}${m.readOnly ? ' (RO)' : ''}`)
                }
              })
            })
            return (
              <tr key={i} className="border-t border-kb-border">
                <td className="py-2 font-mono">{String(vol.name ?? '-')}</td>
                <td className="py-2">
                  <span className="px-1.5 py-0.5 rounded text-[9px] font-mono bg-kb-elevated">{String(vol.type ?? '-')}</span>
                </td>
                <td className="py-2 font-mono text-kb-text-tertiary">{String(vol.details ?? '-')}</td>
                <td className="py-2 font-mono">{mounts.length > 0 ? mounts.join(', ') : '-'}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </Section>
  )
}

// ─── Related Tab ─────────────────────────────────────────────────

function RelatedTab({ type, item }: { type: string; item: ResourceItem }) {
  const { data: topology, isLoading } = useTopology()
  const ownerRefs = Array.isArray(item.ownerReferences) ? item.ownerReferences as Array<Record<string, string>> : []

  // Build the topology node ID for this resource
  const kind = routeToKind[type] ?? type
  const ns = item.namespace || ''
  const nodeId = ns ? `${kind}/${ns}/${item.name}` : `${kind}/${item.name}`

  // Find related resources from topology edges
  const related: Array<{ kind: string; name: string; namespace: string; relation: string }> = []

  // Add owner references (parents)
  ownerRefs.forEach(ref => {
    related.push({ kind: ref.kind, name: ref.name, namespace: item.namespace || '', relation: 'Owner' })
  })

  // Find children and connections from topology
  if (topology) {
    const { edges, nodes } = topology
    const nodeMap = new Map(nodes.map(n => [n.id, n]))

    edges.forEach(edge => {
      if (edge.source === nodeId && !ownerRefs.some(r => `${r.kind}/${ns}/${r.name}` === edge.target)) {
        const target = nodeMap.get(edge.target)
        if (target) {
          related.push({ kind: target.kind, name: target.name, namespace: target.namespace, relation: edge.type || 'related' })
        }
      }
      if (edge.target === nodeId && !ownerRefs.some(r => `${r.kind}/${ns}/${r.name}` === edge.source)) {
        const source = nodeMap.get(edge.source)
        if (source) {
          related.push({ kind: source.kind, name: source.name, namespace: source.namespace, relation: edge.type || 'related' })
        }
      }
    })
  }

  if (isLoading) return <LoadingSpinner />

  if (related.length === 0) {
    return <div className="text-sm text-kb-text-tertiary text-center py-12">No related resources found</div>
  }

  return (
    <Section title="Related">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Kind</th>
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Relation</th>
          </tr>
        </thead>
        <tbody>
          {related.map((ref, i) => {
            const route = kindToRoute[ref.kind]
            return (
              <tr key={`${ref.kind}-${ref.name}-${i}`} className="border-t border-kb-border">
                <td className="py-2">
                  <span className="px-2 py-0.5 rounded text-[9px] font-medium bg-status-info text-white">{ref.kind}</span>
                </td>
                <td className="py-2">
                  {route ? (
                    <ResourceLink name={ref.name} namespace={ref.namespace} resourceType={route} />
                  ) : (
                    <span className="font-mono text-kb-text-secondary">{ref.name}</span>
                  )}
                </td>
                <td className="py-2 text-kb-text-tertiary capitalize">{ref.relation}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </Section>
  )
}

// ─── Events Tab ──────────────────────────────────────────────────

function EventsTab({ type, namespace, name }: { type: string; namespace: string; name: string }) {
  const kind = routeToKind[type] ?? ''
  const { data, isLoading, error } = useResourceEvents(kind, namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const events = data?.items ?? []

  return (
    <Section title="Events">
      {events.length === 0 ? (
        <div className="text-sm text-kb-text-tertiary text-center py-8">No events found</div>
      ) : (
        <table className="w-full text-[11px]">
          <thead>
            <tr className="text-kb-text-tertiary text-left">
              <th className="pb-2 font-normal">Type</th>
              <th className="pb-2 font-normal">Reason</th>
              <th className="pb-2 font-normal">Message</th>
              <th className="pb-2 font-normal">Source</th>
              <th className="pb-2 font-normal">Last Seen</th>
            </tr>
          </thead>
          <tbody className="text-kb-text-secondary">
            {events.map((evt, i) => (
              <tr key={i} className="border-t border-kb-border">
                <td className="py-2">
                  <StatusBadge status={String(evt.type) === 'Warning' ? 'Warning' : 'Running'} label={String(evt.type ?? 'Normal')} />
                </td>
                <td className="py-2 font-mono">{String(evt.reason ?? '-')}</td>
                <td className="py-2 max-w-md truncate">{String(evt.message ?? '-')}</td>
                <td className="py-2 font-mono text-kb-text-tertiary">{String(evt.source ?? '-')}</td>
                <td className="py-2 font-mono text-kb-text-tertiary shrink-0">
                  {evt.timestamp ? formatAge(String(evt.timestamp)) : '-'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Section>
  )
}

// ─── Logs Tab (Pod) ──────────────────────────────────────────────

function LogsTab({ namespace, name, item }: { namespace: string; name: string; item: ResourceItem }) {
  const containers = Array.isArray(item.containers) ? item.containers as Array<Record<string, unknown>> : []
  const containerNames = containers.map(c => String(c.name ?? ''))
  const [selectedContainer, setSelectedContainer] = useState(containerNames[0] ?? '')
  const [tailLines, setTailLines] = useState(100)

  const { data: logs, isLoading, error, refetch } = usePodLogs(namespace, name, selectedContainer, tailLines)
  const logsEndRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (logs) {
      logsEndRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [logs])

  return (
    <div className="space-y-3">
      {/* Controls */}
      <div className="flex items-center gap-3">
        {containerNames.length > 1 && (
          <select
            value={selectedContainer}
            onChange={(e) => setSelectedContainer(e.target.value)}
            className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary"
          >
            {containerNames.map(cn => (
              <option key={cn} value={cn}>{cn}</option>
            ))}
          </select>
        )}
        {containerNames.length === 1 && (
          <span className="text-xs font-mono text-kb-text-secondary">{containerNames[0]}</span>
        )}
        <select
          value={tailLines}
          onChange={(e) => setTailLines(Number(e.target.value))}
          className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary"
        >
          <option value={100}>Last 100 lines</option>
          <option value={500}>Last 500 lines</option>
          <option value={1000}>Last 1000 lines</option>
          <option value={5000}>Last 5000 lines</option>
        </select>
        <button
          onClick={() => refetch()}
          className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors text-kb-text-secondary"
        >
          Refresh
        </button>
        <div className="flex items-center gap-1.5 ml-auto">
          <span className="relative flex h-2 w-2">
            <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-status-ok opacity-75" />
            <span className="relative inline-flex rounded-full h-2 w-2 bg-status-ok" />
          </span>
          <span className="text-[10px] text-kb-text-tertiary">Auto-refresh 10s</span>
        </div>
      </div>

      {/* Log output */}
      <div className="bg-[#0d1117] rounded-[10px] border border-kb-border overflow-hidden">
        {isLoading && !logs && (
          <div className="p-8 text-center text-sm text-kb-text-tertiary">Loading logs...</div>
        )}
        {error && (
          <div className="p-8 text-center text-sm text-status-error">{(error as Error).message}</div>
        )}
        <LogOutput logs={logs} logsEndRef={logsEndRef} />
      </div>
    </div>
  )
}

// ─── Monitor Tab ─────────────────────────────────────────────────

function MonitorTab({ type, item }: { type: string; item: ResourceItem }) {
  // Trend charts read from VictoriaMetrics, which is fed by the
  // KubeBolt Agent (and, in the future, by other metrics-providing
  // integrations). When no such integration is installed in this
  // cluster, all per-type charts would render empty — so we fall
  // back to the snapshot donuts (Metrics Server) for every type
  // and surface an inline CTA pointing operators at the install
  // path. Same component shape as the existing Services/Jobs
  // fallback, just with a more actionable banner.
  const { data: agent, isLoading } = useQuery({
    queryKey: ['integration', 'agent'],
    queryFn: () => api.getIntegration('agent'),
    refetchInterval: 10_000,
    staleTime: 5_000,
  })

  if (isLoading) return <LoadingSpinner />

  const trendSourceInstalled =
    agent && (agent.status === 'installed' || agent.status === 'degraded')

  if (!trendSourceInstalled) {
    return <MonitorDonuts item={item} agentInstalled={false} />
  }

  switch (type) {
    case 'pods':
      return <PodMonitorCharts item={item} />
    case 'deployments':
      return <DeploymentMonitorCharts item={item} />
    case 'statefulsets':
      return <StatefulSetMonitorCharts item={item} />
    case 'daemonsets':
      return <DaemonSetMonitorCharts item={item} />
    case 'nodes':
      return <NodeMonitorCharts item={item} />
    default:
      return <MonitorDonuts item={item} agentInstalled />
  }
}

const MONITOR_BANNER_DISMISSED_KEY = 'kb-monitor-banner-dismissed'

function PodMonitorCharts({ item }: { item: ResourceItem }) {
  const ns = String(item.namespace)
  const name = String(item.name)
  // Both labels are emitted by the agent's stats collector on every sample.
  // Filtering by namespace+name (rather than UID) keeps the chart continuous
  // across pod recreations with the same name — useful for debugging.
  const selector = `namespace="${ns}",pod="${name}"`

  const sums = podResourceSums(item)

  const [bannerDismissed, setBannerDismissed] = useState(
    () => typeof window !== 'undefined' && window.localStorage.getItem(MONITOR_BANNER_DISMISSED_KEY) === 'true',
  )
  const dismissBanner = () => {
    try {
      window.localStorage.setItem(MONITOR_BANNER_DISMISSED_KEY, 'true')
    } catch {
      // Ignore storage errors (private mode, quota, etc.) — UI still dismisses.
    }
    setBannerDismissed(true)
  }

  const cpuRefs = buildCpuRefs(sums.cpuRequest, sums.cpuLimit)
  const memRefs = buildMemRefs(sums.memoryRequest, sums.memoryLimit)

  return (
    <div className="space-y-4">
      {!bannerDismissed && (
        <div className="bg-kb-elevated border border-kb-border rounded-lg px-4 py-2 text-[11px] text-kb-text-secondary flex items-center gap-3">
          <span className="flex-1">
            Historical time-series from KubeBolt Agent (sampled every 15s). If the charts are empty, confirm the agent DaemonSet is running (<code>make agent-logs</code>).
          </span>
          <button
            onClick={dismissBanner}
            className="text-kb-text-tertiary hover:text-kb-text-primary transition-colors p-0.5 rounded hover:bg-kb-surface"
            title="Dismiss"
            aria-label="Dismiss banner"
          >
            <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <MetricChart
          title="CPU by container"
          unit="cores"
          // sum by (container) collapses historical pod_uid instances
          // (e.g. pod restarts) into one line per container. If the pod
          // has never been recreated the query behaves identically.
          //
          // `job=""` selects the agent's own emission. When an external
          // Prometheus also scrapes the kubelet (kube-prom-stack), Prom's
          // series carry `job="kubelet"` and would otherwise double-count
          // the rate AND inject `container=""` pod-level cAdvisor rows
          // that render as a phantom "Series" legend entry. The agent's
          // emission carries `workload_kind` + `workload_name` labels
          // (PodsCache enrichment) that the chart's parent Workload
          // Monitor depends on, so the agent stays the primary source
          // and this filter excludes Prom's overlap entirely.
          query={`sum by (container) (rate(container_cpu_usage_seconds_total{${selector},job="",container!=""}[1m]))`}
          referenceLines={cpuRefs}
          accents={METRIC_ACCENTS.cpu}
          chartType="area"
        />
        <MetricChart
          title="Memory working set by container"
          unit="bytes"
          // Same job=""/container!="" filter pattern as CPU above —
          // agent-only, excludes Prom kubelet scrape's overlap.
          query={`sum by (container) (container_memory_working_set_bytes{${selector},job="",container!=""})`}
          referenceLines={memRefs}
          accents={METRIC_ACCENTS.memory}
          chartType="area"
        />
      </div>

      <MetricChart
        title="Network traffic (RX up / TX down)"
        unit="bytes/s"
        queries={[
          // container_network_* with container="" is the cAdvisor pod-level
          // row (the pause container owns the pod's network namespace).
          // Without the filter the per-container rows duplicate the counters
          // and inflate the rate.
          //
          // `interface=~"eth.*|en.*|cilium_.*"` drops the kernel default
          // pseudo-interfaces (gre0, sit0, ip6_vti0, etc.) that exist in
          // every Linux network namespace with all-zero counters. Without
          // this filter, Prom kubelet scrape's per-interface emission
          // renders 9+ flat lines at zero — the agent stamped fewer of
          // these because /stats/summary collapses by default, but Prom's
          // /metrics/cadvisor surfaces every interface the kernel reports.
          //
          // `job=""` keeps the agent-first convention from CPU/Memory
          // above. Network is less duplicative than CPU/Memory (Prom and
          // agent agree on the labels), but using the same filter keeps
          // the chart consistent and yields the workload_* enrichment for
          // the Pod Monitor's secondary indicators.
          { query: `sum by (interface) (rate(container_network_receive_bytes_total{${selector},job="",container="",interface=~"eth.*|en.*|cilium_.*"}[1m]))`, prefix: 'RX' },
          { query: `sum by (interface) (rate(container_network_transmit_bytes_total{${selector},job="",container="",interface=~"eth.*|en.*|cilium_.*"}[1m]))`, prefix: 'TX', negate: true },
        ]}
        accents={METRIC_ACCENTS.networkRxTx}
        chartType="area"
        height={200}
      />
    </div>
  )
}

// ─── Workload Monitor Charts ─────────────────────────────────────────────
// Shared by Deployment / StatefulSet / DaemonSet. Each wrapper below builds
// the right PromQL selector and passes the replica count so reference lines
// can be multiplied out to the workload-level budget.

interface WorkloadChartsProps {
  item: ResourceItem
  selector: string
  replicas: number
  kindLabel: string // "Deployment", "StatefulSet", etc. — used in chart titles
}

function WorkloadMonitorCharts({ item, selector, replicas, kindLabel }: WorkloadChartsProps) {
  const perPod = podResourceSums(item)
  const mul = (v: number | null) => (v != null ? v * replicas : null)

  const cpuRefs = buildCpuRefs(mul(perPod.cpuRequest), mul(perPod.cpuLimit))
  const memRefs = buildMemRefs(mul(perPod.memoryRequest), mul(perPod.memoryLimit))

  // Coverage-gap detection — count pods that have data in VM and compare
  // to declared replicas. The chart sums whatever VM has; the reference
  // lines multiply per-pod budget × declared replicas. If the agent isn't
  // on every node hosting a replica (Pending pod due to node pressure,
  // NoSchedule taint, namespace-scoped agent), the chart shows healthy
  // headroom while individual pods may be at limit. The banner makes
  // that gap visible.
  //
  // DaemonSet skipped: `replicas` for DS comes from specReplicas which
  // doesn't reflect the cluster's actual node count, so the comparison
  // wouldn't be meaningful. A future iteration could query the node
  // count separately for DS coverage.
  const coverageEnabled = kindLabel !== 'DaemonSet' && replicas > 1
  const { data: coverageResp } = useQuery({
    queryKey: ['workload-coverage', selector],
    queryFn: () =>
      api.queryMetrics({
        // Group by `pod` (canonical label since v1.0). Grouping by a
        // non-existent label collapses every matching series into one
        // bucket, returning count=1 regardless of how many replicas are
        // observed — false-positive coverage banner.
        query: `count(group(rate(container_cpu_usage_seconds_total{${selector}}[1m])) by (pod))`,
      }),
    enabled: coverageEnabled,
    staleTime: 30_000,
    refetchInterval: 60_000,
  })
  const observedPods = (() => {
    if (!coverageResp?.data?.result?.length) return null
    const v = Number(coverageResp.data.result[0].value[1])
    return Number.isFinite(v) ? v : null
  })()
  const coverageGap = coverageEnabled && observedPods != null && observedPods < replicas

  const [bannerDismissed, setBannerDismissed] = useState(
    () => typeof window !== 'undefined' && window.localStorage.getItem(MONITOR_BANNER_DISMISSED_KEY) === 'true',
  )
  const dismissBanner = () => {
    try { window.localStorage.setItem(MONITOR_BANNER_DISMISSED_KEY, 'true') } catch { /* ignore */ }
    setBannerDismissed(true)
  }

  const replicaWord = kindLabel === 'DaemonSet' ? 'node' : 'replica'
  const replicaLabel = `${replicas} ${replicaWord}${replicas !== 1 ? 's' : ''}`

  return (
    <div className="space-y-4">
      {!bannerDismissed && (
        <div className="bg-kb-elevated border border-kb-border rounded-lg px-4 py-2 text-[11px] text-kb-text-secondary flex items-center gap-3">
          <span className="flex-1">
            Historical time-series from KubeBolt Agent, aggregated across {replicaLabel}. Reference lines show the {kindLabel}-level request and limit budget (per-pod request × replicas).
          </span>
          <button
            onClick={dismissBanner}
            className="text-kb-text-tertiary hover:text-kb-text-primary transition-colors p-0.5 rounded hover:bg-kb-surface"
            title="Dismiss"
            aria-label="Dismiss banner"
          >
            <svg className="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <line x1="18" y1="6" x2="6" y2="18" />
              <line x1="6" y1="6" x2="18" y2="18" />
            </svg>
          </button>
        </div>
      )}

      {coverageGap && (
        <div className="bg-status-warn-dim border border-status-warn/30 rounded-lg px-4 py-2.5 text-[11px] text-kb-text-primary flex items-start gap-3">
          <svg className="w-4 h-4 text-status-warn shrink-0 mt-0.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
            <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
            <line x1="12" y1="9" x2="12" y2="13" />
            <line x1="12" y1="17" x2="12.01" y2="17" />
          </svg>
          <div className="flex-1">
            <div className="font-semibold text-status-warn mb-0.5">Partial coverage — KubeBolt Agent has data for {observedPods} of {replicas} {replicaWord}s</div>
            <div className="text-kb-text-secondary leading-relaxed">
              The chart sums what the agent observes; the reference lines multiply per-pod budget × declared {replicaWord}s. With {(replicas ?? 0) - (observedPods ?? 0)} {replicaWord}{(replicas ?? 0) - (observedPods ?? 0) === 1 ? '' : 's'} unobserved, an individual pod can be near limit while the workload-level sum looks healthy.
            </div>
            <div className="text-kb-text-tertiary mt-1">
              Common causes: agent pod Pending due to node resource pressure, taint without matching toleration, namespace-scoped agent, or agent not yet rolled to a new node. See the <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">priority</code> + <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-kb-card">tolerations</code> values in the agent helm chart for full-coverage installs.
            </div>
          </div>
        </div>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <MetricChart
          title={`CPU by container (sum across ${replicaLabel})`}
          unit="cores"
          query={`sum by (container) (rate(container_cpu_usage_seconds_total{${selector}}[1m]))`}
          referenceLines={cpuRefs}
          accents={METRIC_ACCENTS.cpu}
          chartType="area"
        />
        <MetricChart
          title={`Memory working set by container (sum across ${replicaLabel})`}
          unit="bytes"
          query={`sum by (container) (container_memory_working_set_bytes{${selector}})`}
          referenceLines={memRefs}
          accents={METRIC_ACCENTS.memory}
          chartType="area"
        />
      </div>

      <MetricChart
        title={`Network traffic — total across ${replicaLabel} (RX up / TX down)`}
        unit="bytes/s"
        queries={[
          { query: `sum(rate(container_network_receive_bytes_total{${selector},container=""}[1m]))`, prefix: 'RX' },
          { query: `sum(rate(container_network_transmit_bytes_total{${selector},container=""}[1m]))`, prefix: 'TX', negate: true },
        ]}
        seriesLabel={(_labels, prefix) => prefix ?? 'total'}
        accents={METRIC_ACCENTS.networkRxTx}
        chartType="area"
        height={200}
      />
    </div>
  )
}

function DeploymentMonitorCharts({ item }: { item: ResourceItem }) {
  const ns = String(item.namespace)
  const name = String(item.name)
  const replicas = Math.max(1, Number(item.specReplicas ?? 1) || 1)
  // Pods of a Deployment carry workload_kind=ReplicaSet with a name that is
  // the deployment name plus a hash suffix (e.g. my-app-7b4d5f6c89). We match
  // by prefix anchored at end to avoid overlap with sibling deployments that
  // share a prefix.
  const selector = `namespace="${ns}",workload_kind="ReplicaSet",workload_name=~"${escapeRegex(name)}-[a-z0-9]+$"`
  return <WorkloadMonitorCharts item={item} selector={selector} replicas={replicas} kindLabel="Deployment" />
}

function StatefulSetMonitorCharts({ item }: { item: ResourceItem }) {
  const ns = String(item.namespace)
  const name = String(item.name)
  const replicas = Math.max(1, Number(item.specReplicas ?? 1) || 1)
  const selector = `namespace="${ns}",workload_kind="StatefulSet",workload_name="${name}"`
  return <WorkloadMonitorCharts item={item} selector={selector} replicas={replicas} kindLabel="StatefulSet" />
}

function DaemonSetMonitorCharts({ item }: { item: ResourceItem }) {
  const ns = String(item.namespace)
  const name = String(item.name)
  const replicas = Math.max(1, Number(item.specReplicas ?? 0) || 1)
  const selector = `namespace="${ns}",workload_kind="DaemonSet",workload_name="${name}"`
  return <WorkloadMonitorCharts item={item} selector={selector} replicas={replicas} kindLabel="DaemonSet" />
}

// ─── Node Monitor Charts ─────────────────────────────────────────────────

function NodeMonitorCharts({ item }: { item: ResourceItem }) {
  const name = String(item.name)
  const selector = `node="${name}"`

  // The node detail exposes total capacity (millicores + bytes). We surface
  // it as a reference line so the chart conveys "how close are we to full".
  const cpuCapacity = Number(item.cpuCapacity ?? 0) / 1000 // millicores → cores
  const memCapacity = Number(item.memoryCapacity ?? 0)

  const cpuRefs = cpuCapacity > 0
    ? [{ y: cpuCapacity, label: `capacity ${cpuCapacity.toFixed(1)} cores`, color: '#ef4444' }]
    : []
  const memRefs = memCapacity > 0
    ? [{ y: memCapacity, label: `capacity ${formatMemoryShort(memCapacity)}`, color: '#ef4444' }]
    : []

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <MetricChart
          title="CPU usage"
          unit="cores"
          query={`sum(rate(node_cpu_usage_seconds_total{${selector}}[1m]))`}
          referenceLines={cpuRefs}
          accents={METRIC_ACCENTS.cpu}
          chartType="area"
        />
        <MetricChart
          title="Memory working set"
          unit="bytes"
          query={`node_memory_working_set_bytes{${selector}}`}
          referenceLines={memRefs}
          accents={METRIC_ACCENTS.memory}
          chartType="area"
        />
        <MetricChart
          title="Filesystem usage"
          unit="percent"
          // Per-mountpoint % used, with OSS-minimal fallback.
          //
          // Primary branch — node-exporter naming
          //   (node_filesystem_avail_bytes / node_filesystem_size_bytes):
          //   one series per mountpoint, the rich view P25-04 introduced.
          //   Filters drop pseudo-filesystems (tmpfs, overlay, cgroup,
          //   etc. — memory/kernel artifacts) and high-cardinality
          //   ephemeral mounts (kubelet per-pod volumes, /etc/* and
          //   /run/* bind-mounts) that would explode cardinality
          //   without adding operator signal.
          //
          // Fallback branch — kubelet stats summary naming
          //   (node_fs_used_bytes / node_fs_capacity_bytes):
          //   single coarse series per node, emitted unconditionally
          //   by the agent's StatsCollector. Guarded with
          //   `unless on(node) node_filesystem_avail_bytes{selector}`
          //   so it ONLY fires when no node-exporter series exist for
          //   this node — otherwise the two branches would both
          //   return (left=per-mountpoint, right=coarse) and the
          //   chart would show one phantom extra series with no
          //   mountpoint label per node. The `unless ... on(node)`
          //   eliminates that overlap cleanly.
          //
          // Why this matters: P25-04 silently traded the agent's
          // built-in metric for node-exporter's, leaving OSS-minimal
          // installs (agent only, no vmagent sidecar) with an empty
          // Filesystem panel — surfaced in cluster-validation when a
          // freshly-installed humo-1 had no node-exporter and the
          // chart went blank. The fallback restores the OSS-default
          // experience without sacrificing the Prom-mature operator's
          // per-mountpoint view.
          //
          // Reference lines at the two operator-meaningful thresholds:
          // 80% (start watching), 95% (page someone). Same thresholds
          // apply to both branches.
          query={`(100 * (1 - node_filesystem_avail_bytes{${selector},fstype!~"tmpfs|overlay|ramfs|squashfs|devtmpfs|cgroup|cgroup2|proc|sysfs|nsfs|mqueue|securityfs|tracefs|configfs|debugfs|fuse|fusectl|hugetlbfs|pstore|bpf|autofs|binfmt_misc|rpc_pipefs|none",mountpoint!~"^/etc/.*|^/run/.*|^/var/lib/kubelet/.*|^/dev/.*|^/sys/.*|^/proc/.*"} / node_filesystem_size_bytes{${selector},fstype!~"tmpfs|overlay|ramfs|squashfs|devtmpfs|cgroup|cgroup2|proc|sysfs|nsfs|mqueue|securityfs|tracefs|configfs|debugfs|fuse|fusectl|hugetlbfs|pstore|bpf|autofs|binfmt_misc|rpc_pipefs|none",mountpoint!~"^/etc/.*|^/run/.*|^/var/lib/kubelet/.*|^/dev/.*|^/sys/.*|^/proc/.*"})) or ((100 * node_fs_used_bytes{${selector}} / node_fs_capacity_bytes{${selector}}) unless on(node) node_filesystem_avail_bytes{${selector}})`}
          seriesLabel={(labels) => labels.mountpoint || labels.device || 'fs'}
          referenceLines={[
            { y: 80, label: '80%', color: '#f5a623', shortLabel: '80%' },
            { y: 95, label: '95%', color: '#ef4444', shortLabel: '95%' },
          ]}
          accents={METRIC_ACCENTS.filesystem}
          chartType="area"
        />
        <MetricChart
          title="Network traffic (RX up / TX down)"
          unit="bytes/s"
          queries={[
            { query: `sum(rate(node_network_receive_bytes_total{${selector}}[1m]))`, prefix: 'RX' },
            { query: `sum(rate(node_network_transmit_bytes_total{${selector}}[1m]))`, prefix: 'TX', negate: true },
          ]}
          seriesLabel={(_labels, prefix) => prefix ?? 'total'}
          accents={METRIC_ACCENTS.networkRxTx}
          chartType="area"
        />
        {/* Load average — three series (1m / 5m / 15m). The 1m line
            tracks the immediate "right now" load; 5m and 15m smooth
            out spikes and surface sustained pressure. Operators
            comparing the three see whether load is climbing,
            falling, or steady — a single line wouldn't tell that
            story. No reference line: the right "high load" value
            depends on core count (load == cores ≈ saturated) and
            we don't have core count plumbed into this component
            cheaply enough to justify the wiring for a hint. */}
        <MetricChart
          title="Load average"
          unit="count"
          queries={[
            { query: `node_load1{${selector}}`, prefix: '1m' },
            { query: `node_load5{${selector}}`, prefix: '5m' },
            { query: `node_load15{${selector}}`, prefix: '15m' },
          ]}
          seriesLabel={(_labels, prefix) => prefix ?? 'load'}
          chartType="line"
        />
        {/* PSI pressure — % of time at least one task was waiting on
            cpu / io / memory in the last minute. Raw rate() is
            dimensionless 0-1; we multiply by 100 to render as a
            percentage so MetricChart's `percent` axis can do the
            formatting and the operator reads "12% blocked" instead
            of "0.12". Reference lines at 10% (watch) / 30% (page)
            match the WARN/CRIT bands the list-view PSI badge
            uses. Three series keep cpu/io/memory separable so the
            binding axis is obvious at a glance. */}
        <MetricChart
          title="PSI pressure (waiting)"
          unit="percent"
          queries={[
            { query: `100 * rate(node_pressure_cpu_waiting_seconds_total{${selector}}[1m])`, prefix: 'cpu' },
            { query: `100 * rate(node_pressure_io_waiting_seconds_total{${selector}}[1m])`, prefix: 'io' },
            { query: `100 * rate(node_pressure_memory_waiting_seconds_total{${selector}}[1m])`, prefix: 'memory' },
          ]}
          seriesLabel={(_labels, prefix) => prefix ?? 'psi'}
          referenceLines={[
            { y: 10, label: '10% watch', color: '#f5a623', shortLabel: '10%' },
            { y: 30, label: '30% page', color: '#ef4444', shortLabel: '30%' },
          ]}
          chartType="line"
        />
      </div>
    </div>
  )
}

// ─── Helpers ────────────────────────────────────────────────────────────

type RefSpec = { y: number; label: string; color?: string; shortLabel?: string }

function buildCpuRefs(request: number | null, limit: number | null): RefSpec[] {
  // When request === limit (common for guaranteed QoS pods), the two lines
  // overlap and their labels collide. Render them as one combined line.
  if (request != null && limit != null && Math.abs(request - limit) < 1e-9) {
    return [{
      y: limit,
      label: `request / limit ${(limit * 1000).toFixed(0)}m`,
      color: '#ef4444',
      shortLabel: 'req/limit',
    }]
  }
  const refs: RefSpec[] = []
  if (request != null) refs.push({ y: request, label: `request ${(request * 1000).toFixed(0)}m` })
  if (limit != null) refs.push({ y: limit, label: `limit ${(limit * 1000).toFixed(0)}m`, color: '#ef4444' })
  return refs
}

function buildMemRefs(request: number | null, limit: number | null): RefSpec[] {
  if (request != null && limit != null && request === limit) {
    return [{
      y: limit,
      label: `request / limit ${formatMemoryShort(limit)}`,
      color: '#ef4444',
      shortLabel: 'req/limit',
    }]
  }
  const refs: RefSpec[] = []
  if (request != null) refs.push({ y: request, label: `request ${formatMemoryShort(request)}` })
  if (limit != null) refs.push({ y: limit, label: `limit ${formatMemoryShort(limit)}`, color: '#ef4444' })
  return refs
}

// escapeRegex quotes characters that are special in PromQL =~ matchers.
// Resource names follow DNS-1123 (alphanumeric + dashes), but we still
// escape defensively in case a name includes a dot or other regex glyph.
function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// podResourceSums aggregates requests/limits across all containers in the pod.
// Returns null for any field that no container defined. CPU is returned in
// cores (the backend exposes cpuRequest/cpuLimit as millicores).
function podResourceSums(item: ResourceItem): {
  cpuRequest: number | null
  cpuLimit: number | null
  memoryRequest: number | null
  memoryLimit: number | null
} {
  const containers = Array.isArray(item.containers) ? (item.containers as Array<Record<string, unknown>>) : []
  let cpuReq = 0, cpuLim = 0, memReq = 0, memLim = 0
  let anyCpuReq = false, anyCpuLim = false, anyMemReq = false, anyMemLim = false

  for (const c of containers) {
    const r = c?.resources as Record<string, unknown> | undefined
    if (!r) continue
    const cpuR = typeof r.cpuRequest === 'number' ? r.cpuRequest : 0
    const cpuL = typeof r.cpuLimit === 'number' ? r.cpuLimit : 0
    const memR = typeof r.memoryRequest === 'number' ? r.memoryRequest : 0
    const memL = typeof r.memoryLimit === 'number' ? r.memoryLimit : 0
    if (cpuR > 0) { cpuReq += cpuR; anyCpuReq = true }
    if (cpuL > 0) { cpuLim += cpuL; anyCpuLim = true }
    if (memR > 0) { memReq += memR; anyMemReq = true }
    if (memL > 0) { memLim += memL; anyMemLim = true }
  }

  return {
    cpuRequest: anyCpuReq ? cpuReq / 1000 : null,
    cpuLimit: anyCpuLim ? cpuLim / 1000 : null,
    memoryRequest: anyMemReq ? memReq : null,
    memoryLimit: anyMemLim ? memLim : null,
  }
}

function formatMemoryShort(bytes: number): string {
  const abs = Math.abs(bytes)
  if (abs < 1024) return `${bytes} B`
  if (abs < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KiB`
  if (abs < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(0)} MiB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GiB`
}

// MonitorDonuts renders the snapshot view (current CPU/Memory from
// Metrics Server). Used in two cases:
//
//   - Resource types that simply don't have agent-side trend metrics
//     (Services, Jobs, etc.) — banner reads as "this type has no
//     trends".
//   - Any resource type when no metrics-providing integration is
//     installed in the cluster — banner reads as "install the agent
//     for trends" with an inline CTA. Caller passes `agentInstalled
//     = false` for this case.
function MonitorDonuts({
  item,
  agentInstalled = true,
}: {
  item: ResourceItem
  agentInstalled?: boolean
}) {
  const cpuUsage = Number(item.cpuUsage ?? 0)
  const cpuPercent = Number(item.cpuPercent ?? 0)
  const memUsage = Number(item.memoryUsage ?? 0)
  const memPercent = Number(item.memoryPercent ?? 0)

  if (cpuUsage === 0 && memUsage === 0) {
    return (
      <div className="space-y-4">
        {!agentInstalled && <AgentTrendsCTA />}
        <div className="text-sm text-kb-text-tertiary text-center py-12">
          No metrics available. Metrics Server may not be installed or this resource type does not report metrics.
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {agentInstalled ? (
        <div className="bg-status-warn-dim border border-status-warn/20 rounded-lg px-4 py-2 text-[11px] text-status-warn">
          Current data is from metrics-server (point-in-time snapshot). Historical time-series for this resource type will land in a later iteration.
        </div>
      ) : (
        <AgentTrendsCTA />
      )}

      <div className="grid grid-cols-2 gap-4">
        {/* CPU */}
        <Section title="CPU Usage">
          <div className="flex flex-col items-center py-8">
            <div className="relative w-32 h-32">
              <svg className="w-32 h-32 -rotate-90" viewBox="0 0 120 120">
                <circle cx="60" cy="60" r="52" fill="none" stroke="var(--kb-border)" strokeWidth="10" />
                <circle cx="60" cy="60" r="52" fill="none" stroke="var(--kb-accent)" strokeWidth="10"
                  strokeDasharray={`${cpuPercent * 3.267} 326.7`} strokeLinecap="round" />
              </svg>
              <div className="absolute inset-0 flex flex-col items-center justify-center">
                <span className="text-2xl font-semibold text-kb-text-primary">{Math.round(cpuPercent)}%</span>
              </div>
            </div>
            <div className="mt-4 text-center">
              <div className="text-sm font-mono text-kb-text-primary">{formatCPU(cpuUsage)}</div>
              <div className="text-[10px] text-kb-text-tertiary">current usage</div>
            </div>
          </div>
        </Section>

        {/* Memory */}
        <Section title="Memory Usage">
          <div className="flex flex-col items-center py-8">
            <div className="relative w-32 h-32">
              <svg className="w-32 h-32 -rotate-90" viewBox="0 0 120 120">
                <circle cx="60" cy="60" r="52" fill="none" stroke="var(--kb-border)" strokeWidth="10" />
                <circle cx="60" cy="60" r="52" fill="none" stroke="#22d68a" strokeWidth="10"
                  strokeDasharray={`${memPercent * 3.267} 326.7`} strokeLinecap="round" />
              </svg>
              <div className="absolute inset-0 flex flex-col items-center justify-center">
                <span className="text-2xl font-semibold text-kb-text-primary">{Math.round(memPercent)}%</span>
              </div>
            </div>
            <div className="mt-4 text-center">
              <div className="text-sm font-mono text-kb-text-primary">{formatMemory(memUsage)}</div>
              <div className="text-[10px] text-kb-text-tertiary">current usage</div>
            </div>
          </div>
        </Section>
      </div>

      {/* Network & Disk placeholders — Metrics Server has no
          equivalent for these, so they stay locked until the agent
          ships per-pod RX/TX byte counters and per-container disk
          I/O samples. */}
      <div className="grid grid-cols-2 gap-4">
        <AgentLockedTile
          title="Network Usage"
          description="The agent samples per-resource RX and TX bytes every 15s and renders them as a single chart with TX below the zero line — at a glance you see direction, peak, and ratio. Anomaly hooks (Ask Kobi) plug into it too."
        />
        <AgentLockedTile
          title="Disk I/O Usage"
          description="Per-container read/write throughput plus filesystem usage trends, scoped to the resource you're viewing. Catches noisy-neighbor PVCs and runaway log rotations before they fill the node."
        />
      </div>
    </div>
  )
}

const MONITOR_CTA_EXPANDED_KEY = 'kb-monitor-cta-expanded'

// AgentTrendsCTA — banner shown above the snapshot donuts when no
// trend-providing integration is installed. Same accent-tinted card
// used on other "agent unlocks more" affordances. Collapses to a
// single row by default so it doesn't dominate the Monitor tab on
// every visit; the chevron expands a richer pitch (description,
// feature bullets, helm one-liner). Preference is persisted so the
// user's choice carries across navigations within the session.
function AgentTrendsCTA() {
  const [expanded, setExpanded] = useState(
    () =>
      typeof window !== 'undefined' &&
      window.localStorage.getItem(MONITOR_CTA_EXPANDED_KEY) === 'true',
  )

  const toggle = () => {
    const next = !expanded
    setExpanded(next)
    try {
      window.localStorage.setItem(MONITOR_CTA_EXPANDED_KEY, next ? 'true' : 'false')
    } catch {
      // Ignore storage errors (private mode, quota); UI still toggles.
    }
  }

  return (
    <div className="rounded-lg border border-kb-border bg-kb-card border-l-4 border-l-kb-accent">
      {/* Header row — clickable to toggle. The Install button sits
          inside the header but stops propagation so its click
          navigates instead of toggling. */}
      <button
        type="button"
        onClick={toggle}
        aria-expanded={expanded}
        className="w-full flex items-center gap-3 p-3 text-left hover:bg-kb-elevated/30 transition-colors rounded-lg"
      >
        <div className="w-7 h-7 rounded-md bg-kb-accent-light flex items-center justify-center shrink-0">
          <Lock className="w-3.5 h-3.5 text-kb-accent" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-[13px] font-semibold text-kb-text-primary truncate">
            Time-series trends require the KubeBolt Agent
          </div>
          {!expanded && (
            <div className="text-[11px] text-kb-text-tertiary truncate">
              Click to learn what the agent unlocks
            </div>
          )}
        </div>
        <Link
          to="/admin/integrations"
          onClick={(e) => e.stopPropagation()}
          className="inline-flex items-center gap-1.5 px-3.5 py-1.5 rounded-md bg-kb-accent text-white text-xs font-semibold shadow-sm shadow-kb-accent/30 ring-1 ring-inset ring-white/15 hover:opacity-95 hover:shadow-md hover:shadow-kb-accent/40 active:scale-[0.98] transition-all shrink-0"
        >
          Install agent
          <ArrowRight className="w-3.5 h-3.5" strokeWidth={2.5} />
        </Link>
        <ChevronDown
          className={`w-4 h-4 text-kb-text-tertiary shrink-0 transition-transform ${expanded ? 'rotate-180' : ''}`}
        />
      </button>

      {/* Expanded body — the rich pitch. Indented so it aligns with
          the title text in the header rather than the icon. */}
      {expanded && (
        <div className="px-4 pb-4 pl-[60px]">
          <p className="text-[12px] text-kb-text-secondary leading-relaxed">
            The donuts below show <strong className="text-kb-text-primary font-medium">current</strong> CPU and memory from the Kubernetes Metrics Server — that's everything this cluster exposes today. Install the agent (or another metrics integration when available) to unlock:
          </p>
          <ul className="text-[12px] text-kb-text-secondary mt-2 space-y-1 ml-1">
            <li className="flex items-start gap-2">
              <span className="text-kb-accent mt-1.5 shrink-0">•</span>
              <span>Historical CPU and memory trends with selectable range (5m → 24h)</span>
            </li>
            <li className="flex items-start gap-2">
              <span className="text-kb-accent mt-1.5 shrink-0">•</span>
              <span>Network traffic charts (RX up / TX down) per resource</span>
            </li>
            <li className="flex items-start gap-2">
              <span className="text-kb-accent mt-1.5 shrink-0">•</span>
              <span>Filesystem and disk I/O activity</span>
            </li>
          </ul>
          <div className="mt-3 pt-3 border-t border-kb-border flex items-center gap-2 text-[10px]">
            <span className="text-kb-text-tertiary font-mono uppercase tracking-wider shrink-0">Or via Helm</span>
            <code className="font-mono text-kb-text-secondary bg-kb-bg border border-kb-border rounded px-2 py-1 truncate flex-1">
              helm install kubebolt-agent oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent --namespace kubebolt-system --create-namespace
            </code>
          </div>
        </div>
      )}
    </div>
  )
}

// AgentLockedTile — placeholder used inside MonitorDonuts where a
// metric (Network, Disk I/O) only exists with the agent installed.
// Shows a lock badge, a one-line "what's missing" headline, and a
// short paragraph explaining what the agent would have shown here
// instead of just the bare "Requires Agent" line.
function AgentLockedTile({
  title,
  description,
}: {
  title: string
  description: string
}) {
  return (
    <Section title={title}>
      <div className="flex flex-col items-center justify-center text-center py-7 px-4 gap-2">
        <div className="w-9 h-9 rounded-full bg-kb-accent-light flex items-center justify-center">
          <Lock className="w-4 h-4 text-kb-accent" />
        </div>
        <div className="text-xs font-semibold text-kb-text-primary">
          Available with the KubeBolt Agent
        </div>
        <p className="text-[11px] text-kb-text-tertiary leading-relaxed max-w-xs">
          {description}
        </p>
      </div>
    </Section>
  )
}

// ─── Workload Pods Tabs ──────────────────────────────────────────

function DeploymentPodsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useDeploymentPods(namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const pods = data?.items ?? []
  if (pods.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No pods found</div>

  return (
    <Section title="Pods">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Ready</th>
            <th className="pb-2 font-normal">Status</th>
            <th className="pb-2 font-normal pr-6">CPU</th>
            <th className="pb-2 font-normal pl-2">Memory</th>
            <th className="pb-2 font-normal">Restarts</th>
            <th className="pb-2 font-normal">IP</th>
            <th className="pb-2 font-normal">Node</th>
            <th className="pb-2 font-normal">Age</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {pods.map((pod, i) => (
            <tr key={i} className="border-t border-kb-border">
              <td className="py-2"><ResourceLink name={pod.name} namespace={pod.namespace} resourceType="pods" /></td>
              <td className="py-2">{(() => { const val = String(pod.ready ?? '0/0'); const [r, t] = val.split('/'); return <StatusBadge status={r === t && t !== '0' ? 'Running' : 'Warning'} label={val} /> })()}</td>
              <td className="py-2"><StatusBadge status={pod.status} /></td>
              <td className="py-2 w-36 pr-6">
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2">
                <RestartHistorySparkline
                  namespace={String(pod.namespace)}
                  pod={String(pod.name)}
                  variant="badge"
                  lifetimeCount={Number(pod.restarts ?? 0)}
                />
              </td>
              <td className="py-2 font-mono text-kb-text-secondary">{String(pod.ip ?? '—')}</td>
              <td className="py-2">{pod.nodeName ? <Link to={`/nodes/_/${pod.nodeName}`} className="text-[11px] font-mono text-status-info hover:underline">{String(pod.nodeName)}</Link> : <span className="text-kb-text-tertiary">—</span>}</td>
              <td className="py-2 font-mono text-kb-text-tertiary">{pod.createdAt ? formatAge(pod.createdAt) : '-'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  )
}

function StatefulSetPodsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useStatefulSetPods(namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const pods = data?.items ?? []
  if (pods.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No pods found</div>

  return (
    <Section title="Pods">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Ready</th>
            <th className="pb-2 font-normal">Status</th>
            <th className="pb-2 font-normal pr-6">CPU</th>
            <th className="pb-2 font-normal pl-2">Memory</th>
            <th className="pb-2 font-normal">Restarts</th>
            <th className="pb-2 font-normal">IP</th>
            <th className="pb-2 font-normal">Node</th>
            <th className="pb-2 font-normal">Age</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {pods.map((pod, i) => (
            <tr key={i} className="border-t border-kb-border">
              <td className="py-2"><ResourceLink name={pod.name} namespace={pod.namespace} resourceType="pods" /></td>
              <td className="py-2">{(() => { const val = String(pod.ready ?? '0/0'); const [r, t] = val.split('/'); return <StatusBadge status={r === t && t !== '0' ? 'Running' : 'Warning'} label={val} /> })()}</td>
              <td className="py-2"><StatusBadge status={pod.status} /></td>
              <td className="py-2 w-36 pr-6">
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2">
                <RestartHistorySparkline
                  namespace={String(pod.namespace)}
                  pod={String(pod.name)}
                  variant="badge"
                  lifetimeCount={Number(pod.restarts ?? 0)}
                />
              </td>
              <td className="py-2 font-mono text-kb-text-secondary">{String(pod.ip ?? '—')}</td>
              <td className="py-2">{pod.nodeName ? <Link to={`/nodes/_/${pod.nodeName}`} className="text-[11px] font-mono text-status-info hover:underline">{String(pod.nodeName)}</Link> : <span className="text-kb-text-tertiary">—</span>}</td>
              <td className="py-2 font-mono text-kb-text-tertiary">{pod.createdAt ? formatAge(pod.createdAt) : '-'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  )
}

function DaemonSetPodsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useDaemonSetPods(namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const pods = data?.items ?? []
  if (pods.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No pods found</div>

  return (
    <Section title="Pods">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Ready</th>
            <th className="pb-2 font-normal">Status</th>
            <th className="pb-2 font-normal pr-6">CPU</th>
            <th className="pb-2 font-normal pl-2">Memory</th>
            <th className="pb-2 font-normal">Restarts</th>
            <th className="pb-2 font-normal">IP</th>
            <th className="pb-2 font-normal">Node</th>
            <th className="pb-2 font-normal">Age</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {pods.map((pod, i) => (
            <tr key={i} className="border-t border-kb-border">
              <td className="py-2"><ResourceLink name={pod.name} namespace={pod.namespace} resourceType="pods" /></td>
              <td className="py-2">{(() => { const val = String(pod.ready ?? '0/0'); const [r, t] = val.split('/'); return <StatusBadge status={r === t && t !== '0' ? 'Running' : 'Warning'} label={val} /> })()}</td>
              <td className="py-2"><StatusBadge status={pod.status} /></td>
              <td className="py-2 w-36 pr-6">
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2">
                <RestartHistorySparkline
                  namespace={String(pod.namespace)}
                  pod={String(pod.name)}
                  variant="badge"
                  lifetimeCount={Number(pod.restarts ?? 0)}
                />
              </td>
              <td className="py-2 font-mono text-kb-text-secondary">{String(pod.ip ?? '—')}</td>
              <td className="py-2">{pod.nodeName ? <Link to={`/nodes/_/${pod.nodeName}`} className="text-[11px] font-mono text-status-info hover:underline">{String(pod.nodeName)}</Link> : <span className="text-kb-text-tertiary">—</span>}</td>
              <td className="py-2 font-mono text-kb-text-tertiary">{pod.createdAt ? formatAge(pod.createdAt) : '-'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  )
}

function JobPodsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useJobPods(namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const pods = data?.items ?? []
  if (pods.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No pods found</div>

  return (
    <Section title="Pods">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Node</th>
            <th className="pb-2 font-normal">Status</th>
            <th className="pb-2 font-normal pr-6">CPU</th>
            <th className="pb-2 font-normal pl-2">Memory</th>
            <th className="pb-2 font-normal">Restarts</th>
            <th className="pb-2 font-normal">Age</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {pods.map((pod, i) => (
            <tr key={i} className="border-t border-kb-border">
              <td className="py-2"><ResourceLink name={pod.name} namespace={pod.namespace} resourceType="pods" /></td>
              <td className="py-2">
                {pod.nodeName ? <ResourceLink name={String(pod.nodeName)} resourceType="nodes" /> : '-'}
              </td>
              <td className="py-2"><StatusBadge status={pod.status} /></td>
              <td className="py-2 w-36 pr-6">
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2">
                <RestartHistorySparkline
                  namespace={String(pod.namespace)}
                  pod={String(pod.name)}
                  variant="badge"
                  lifetimeCount={Number(pod.restarts ?? 0)}
                />
              </td>
              <td className="py-2 font-mono text-kb-text-tertiary">{pod.createdAt ? formatAge(pod.createdAt) : '-'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  )
}

// ─── Workload Logs Tabs ──────────────────────────────────────────

function WorkloadLogsTab({ pods, isLoading: podsLoading, error: podsError }: { pods: ResourceItem[]; isLoading: boolean; error: Error | null }) {
  const [selectedPod, setSelectedPod] = useState('')
  const [selectedContainer, setSelectedContainer] = useState('')
  const [tailLines, setTailLines] = useState(100)
  const logsEndRef = useRef<HTMLDivElement>(null)

  // Set default pod when pods load
  useEffect(() => {
    if (pods.length > 0 && !selectedPod) {
      setSelectedPod(pods[0].name)
    }
  }, [pods, selectedPod])

  // Get containers for selected pod
  const currentPod = pods.find(p => p.name === selectedPod)
  const containers = currentPod && Array.isArray(currentPod.containers)
    ? (currentPod.containers as Array<Record<string, unknown>>).map(c => String(c.name ?? ''))
    : []

  // Set default container when pod changes
  useEffect(() => {
    if (containers.length > 0 && !containers.includes(selectedContainer)) {
      setSelectedContainer(containers[0])
    }
  }, [selectedPod, containers, selectedContainer])

  const podNamespace = currentPod?.namespace ?? ''
  const { data: logs, isLoading: logsLoading, error: logsError, refetch } = usePodLogs(podNamespace, selectedPod, selectedContainer, tailLines)

  useEffect(() => {
    if (logs) {
      logsEndRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [logs])

  if (podsLoading) return <LoadingSpinner />
  if (podsError) return <ErrorState message={podsError.message} />
  if (pods.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No pods found</div>

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3">
        <select
          value={selectedPod}
          onChange={(e) => { setSelectedPod(e.target.value); setSelectedContainer('') }}
          className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary"
        >
          {pods.map(p => (
            <option key={p.name} value={p.name}>{p.name}</option>
          ))}
        </select>
        {containers.length > 1 && (
          <select
            value={selectedContainer}
            onChange={(e) => setSelectedContainer(e.target.value)}
            className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary"
          >
            {containers.map(cn => (
              <option key={cn} value={cn}>{cn}</option>
            ))}
          </select>
        )}
        {containers.length === 1 && (
          <span className="text-xs font-mono text-kb-text-secondary">{containers[0]}</span>
        )}
        <select
          value={tailLines}
          onChange={(e) => setTailLines(Number(e.target.value))}
          className="px-2 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-primary"
        >
          <option value={100}>Last 100 lines</option>
          <option value={500}>Last 500 lines</option>
          <option value={1000}>Last 1000 lines</option>
        </select>
        <button
          onClick={() => refetch()}
          className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors text-kb-text-secondary"
        >
          Refresh
        </button>
        <div className="flex items-center gap-1.5 ml-auto">
          <span className="relative flex h-2 w-2">
            <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-status-ok opacity-75" />
            <span className="relative inline-flex rounded-full h-2 w-2 bg-status-ok" />
          </span>
          <span className="text-[10px] text-kb-text-tertiary">Auto-refresh 10s</span>
        </div>
      </div>

      <div className="bg-[#0d1117] rounded-[10px] border border-kb-border overflow-hidden">
        {logsLoading && !logs && (
          <div className="p-8 text-center text-sm text-kb-text-tertiary">Loading logs...</div>
        )}
        {logsError && (
          <div className="p-8 text-center text-sm text-status-error">{(logsError as Error).message}</div>
        )}
        <LogOutput logs={logs} logsEndRef={logsEndRef} />
      </div>
    </div>
  )
}

function DeploymentLogsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useDeploymentPods(namespace, name)
  return <WorkloadLogsTab pods={data?.items ?? []} isLoading={isLoading} error={error} />
}

function StatefulSetLogsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useStatefulSetPods(namespace, name)
  return <WorkloadLogsTab pods={data?.items ?? []} isLoading={isLoading} error={error} />
}

function DaemonSetLogsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useDaemonSetPods(namespace, name)
  return <WorkloadLogsTab pods={data?.items ?? []} isLoading={isLoading} error={error} />
}

function JobLogsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useJobPods(namespace, name)
  return <WorkloadLogsTab pods={data?.items ?? []} isLoading={isLoading} error={error} />
}

// ─── Node Pods Tab ────────────────────────────────────────────────
// Lists every pod scheduled on this node. Reuses the generic
// /resources/pods endpoint with the new ?node= filter, so we don't need
// a dedicated handler. Limit bumped to 200 because nodes commonly carry
// 30–110 pods and we don't want pagination chrome on a tab.

function NodePodsTab({ nodeName }: { nodeName: string }) {
  const { data, isLoading, error } = useResources('pods', { node: nodeName, limit: 200 })

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const pods = data?.items ?? []
  if (pods.length === 0) {
    return <div className="text-sm text-kb-text-tertiary text-center py-12">No pods found on this node</div>
  }

  return (
    <Section title={`Pods on ${nodeName} (${pods.length})`}>
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Namespace</th>
            <th className="pb-2 font-normal">Ready</th>
            <th className="pb-2 font-normal">Status</th>
            <th className="pb-2 font-normal pr-6">CPU</th>
            <th className="pb-2 font-normal pl-2">Memory</th>
            <th className="pb-2 font-normal">Restarts</th>
            <th className="pb-2 font-normal">IP</th>
            <th className="pb-2 font-normal">Age</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {pods.map((pod: ResourceItem, i: number) => (
            <tr key={i} className="border-t border-kb-border">
              <td className="py-2"><ResourceLink name={pod.name} namespace={pod.namespace} resourceType="pods" /></td>
              <td className="py-2 font-mono text-kb-text-tertiary">{String(pod.namespace ?? '—')}</td>
              <td className="py-2">{(() => { const val = String(pod.ready ?? '0/0'); const [r, t] = val.split('/'); return <StatusBadge status={r === t && t !== '0' ? 'Running' : 'Warning'} label={val} /> })()}</td>
              <td className="py-2"><StatusBadge status={pod.status} /></td>
              <td className="py-2 w-36 pr-6">
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" hasMetrics={pod.cpuUsage !== undefined || pod.memoryUsage !== undefined} />
              </td>
              <td className="py-2">
                <RestartHistorySparkline
                  namespace={String(pod.namespace)}
                  pod={String(pod.name)}
                  variant="badge"
                  lifetimeCount={Number(pod.restarts ?? 0)}
                />
              </td>
              <td className="py-2 font-mono text-kb-text-secondary">{String(pod.ip ?? '—')}</td>
              <td className="py-2 font-mono text-kb-text-tertiary">{pod.createdAt ? formatAge(pod.createdAt) : '-'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  )
}

// ─── History Tab (Deployments) ───────────────────────────────────

function CronJobJobsTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useCronJobJobs(namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const jobs = data?.items ?? []
  if (jobs.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No jobs found</div>

  return (
    <Section title="Jobs">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Status</th>
            <th className="pb-2 font-normal">Completions</th>
            <th className="pb-2 font-normal">Duration</th>
            <th className="pb-2 font-normal">Age</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {jobs.map((job, i) => (
            <tr key={i} className="border-t border-kb-border">
              <td className="py-2"><ResourceLink name={job.name} namespace={job.namespace} resourceType="jobs" /></td>
              <td className="py-2"><StatusBadge status={job.status} /></td>
              <td className="py-2 font-mono">{String(job.completions ?? '—')}</td>
              <td className="py-2 font-mono">{String(job.duration ?? '—')}</td>
              <td className="py-2 font-mono text-kb-text-tertiary">{job.createdAt ? formatAge(job.createdAt) : '-'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  )
}

function HistoryTab({ namespace, name }: { namespace: string; name: string }) {
  const { data, isLoading, error } = useDeploymentHistory(namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const items = data?.items ?? []
  if (items.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No revision history found</div>

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
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {items.map((item, i) => {
            const replicas = Number(item.replicas ?? 0)
            const readyReplicas = Number(item.readyReplicas ?? 0)
            const isActive = replicas > 0
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
                <td className="py-2"><StatusBadge status={isActive ? 'Running' : 'Terminated'} label={isActive ? 'Active' : 'Scaled down'} /></td>
                <td className="py-2 font-mono text-kb-text-tertiary">{item.createdAt ? formatAge(item.createdAt) : '-'}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </Section>
  )
}

function WorkloadHistoryTab({ type, namespace, name }: { type: string; namespace: string; name: string }) {
  const { data, isLoading, error } = useWorkloadHistory(type, namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const items = data?.items ?? []
  if (items.length === 0) return <div className="text-sm text-kb-text-tertiary text-center py-12">No revision history found</div>

  return (
    <Section title="Revision History (ControllerRevisions)">
      <table className="w-full text-[11px]">
        <thead>
          <tr className="text-kb-text-tertiary text-left">
            <th className="pb-2 font-normal">Revision</th>
            <th className="pb-2 font-normal">Name</th>
            <th className="pb-2 font-normal">Age</th>
          </tr>
        </thead>
        <tbody className="text-kb-text-secondary">
          {items.map((item, i) => {
            const isLatest = i === 0
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
              </tr>
            )
          })}
        </tbody>
      </table>
    </Section>
  )
}

// ─── Main Component ──────────────────────────────────────────────

export function ResourceDetailPage() {
  const { type = '', namespace = '', name = '' } = useParams<{ type: string; namespace: string; name: string }>()
  const { data: item, isLoading, error, refetch, dataUpdatedAt, isFetching } = useResourceDetail(type, namespace, name)
  // Tab is URL-driven (?tab=pods etc.) so deep links from the Copilot's
  // ActionProposalCard land directly on the right view. Default is "overview".
  const [searchParams, setSearchParams] = useSearchParams()
  const activeTab = searchParams.get('tab') ?? 'overview'
  const setActiveTab = (tab: string) => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (tab === 'overview') next.delete('tab')
        else next.set('tab', tab)
        return next
      },
      { replace: true },
    )
  }
  const [showDescribe, setShowDescribe] = useState(false)
  const [showRestart, setShowRestart] = useState(false)
  // showScale opens the ScaleModal (post-refactor: was a popover, now
  // a modal so it can be triggered from the Actions menu without an
  // anchor mismatch). scaleValue is gone — the modal manages its own
  // input state, seeded from currentReplicas.
  const [showScale, setShowScale] = useState(false)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [showDelete, setShowDelete] = useState(false)
  const [showSetImage, setShowSetImage] = useState(false)
  const [showSetResources, setShowSetResources] = useState(false)
  const [showSetEnv, setShowSetEnv] = useState(false)
  const [showEditMetadata, setShowEditMetadata] = useState(false)
  const [showSecretReveal, setShowSecretReveal] = useState(false)
  // showRollback carries the target revision so the modal can render
  // before/after diffs without re-fetching. null = closed. Cut 4
  // implements the modal; this Cut 3 just plumbs the open trigger.
  const [showRollback, setShowRollback] = useState<{ revision: number } | null>(null)
  // CronJob actions: trigger opens a small modal; suspend/resume
  // are single-action mutations with their own loading state
  // tracked via actionLoading (so the existing Restart/Scale state
  // doesn't conflict).
  const [showTrigger, setShowTrigger] = useState(false)
  // Drain opens the existing DrainModal — same component the
  // Nodes list uses. State lives on the detail page so the modal
  // mounts at this level rather than per-button.
  const [showDrain, setShowDrain] = useState(false)
  // Surfaced when a cluster-mutation action returns 4xx/5xx — replaces
  // the bare alert() that used to dump raw apiserver text. The toast
  // detects agentRbacForbidden and offers a 1-click jump to the
  // Integrations page so the operator can switch the agent's tier.
  const [mutationError, setMutationError] = useState<{ err: unknown; action: string } | null>(null)
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const { hasRole } = useAuth()
  const canEdit = hasRole('editor')
  const canDelete = hasRole('admin')

  // Tab state lives in the URL so changing resource (different path) starts
  // fresh without an explicit reset. If the previous URL had ?tab=, that's
  // dropped naturally on navigation.

  if (isLoading) return <LoadingSpinner />
  // Only show full-screen error when we have NO data to render.
  // If we previously loaded `item` and a subsequent refetch failed
  // (transient backend hiccup, informer cache lost a watch event,
  // brief 404 between create-and-observe), keep showing the cached
  // data instead of yanking the operator out of their workflow.
  // The DataFreshnessIndicator already exposes refetch/error state
  // to the user; no need to bulldoze the page for a temporary blip.
  if (!item) return <ErrorState message={error?.message ?? 'Resource not found'} onRetry={() => refetch()} />

  const tabs = getTabsForResource(type, item)
  const parentLabel = resourceLabels[type] || type
  const parentPath = `/${type}`

  // Toolbar Actions ▾ menu items — built per-kind. Primary actions
  // (Restart, Set image for workloads; Trigger/Suspend for cronjobs;
  // Cordon/Drain for nodes; Reveal for secrets) stay inline in the
  // toolbar; the writes below are the second-tier actions that pile
  // up otherwise. Edit metadata is universal — when it's the ONLY
  // item, the toolbar promotes it inline instead of showing a sad
  // single-entry dropdown.
  const isWorkload = type === 'deployments' || type === 'statefulsets' || type === 'daemonsets'
  const isPaused = (item as unknown as { paused?: boolean })?.paused === true
  const menuItems: ActionItem[] = []
  if (type === 'deployments' || type === 'statefulsets') {
    menuItems.push({
      id: 'scale',
      label: 'Scale',
      icon: ArrowUpDown,
      disabled: !canEdit,
      hint: !canEdit ? 'Editor role required' : undefined,
      onClick: () => setShowScale(true),
    })
  }
  if (isWorkload) {
    menuItems.push({
      id: 'set-resources',
      label: 'Set resources',
      icon: Cpu,
      disabled: !canEdit,
      hint: !canEdit ? 'Editor role required' : undefined,
      onClick: () => setShowSetResources(true),
    })
    menuItems.push({
      id: 'set-env',
      label: 'Set env',
      icon: Variable,
      disabled: !canEdit,
      hint: !canEdit ? 'Editor role required' : undefined,
      onClick: () => setShowSetEnv(true),
    })
  }
  if (type === 'deployments') {
    menuItems.push(
      isPaused
        ? {
            id: 'rollout-resume',
            label: 'Resume rollout',
            icon: Play,
            variant: 'success',
            disabled: !canEdit || actionLoading === 'rollout-resume',
            hint: !canEdit ? 'Editor role required' : 'Resume rollout — kubectl rollout resume',
            onClick: async () => {
              setActionLoading('rollout-resume')
              try {
                const res = await api.resumeRollout(type, namespace, name, 'ui')
                if (res.deployment) {
                  queryClient.setQueryData(['resource-detail', type, namespace, name], res.deployment)
                }
                queryClient.setQueriesData<{ items: ResourceItem[] }>(
                  { queryKey: ['resources', 'deployments'] },
                  (old) => {
                    if (!old) return old
                    return {
                      ...old,
                      items: old.items.map((d) =>
                        d.name === name && d.namespace === namespace ? { ...d, paused: false } : d,
                      ),
                    }
                  },
                )
              } catch (err) {
                queryClient.refetchQueries({ queryKey: ['resources', 'deployments'], type: 'active' })
                setMutationError({ err, action: 'Resume rollout' })
              } finally {
                setActionLoading(null)
              }
            },
          }
        : {
            id: 'rollout-pause',
            label: 'Pause rollout',
            icon: Pause,
            disabled: !canEdit || actionLoading === 'rollout-pause',
            hint: !canEdit ? 'Editor role required' : 'Pause rollout — kubectl rollout pause',
            onClick: async () => {
              setActionLoading('rollout-pause')
              try {
                const res = await api.pauseRollout(type, namespace, name, 'ui')
                if (res.deployment) {
                  queryClient.setQueryData(['resource-detail', type, namespace, name], res.deployment)
                }
                queryClient.setQueriesData<{ items: ResourceItem[] }>(
                  { queryKey: ['resources', 'deployments'] },
                  (old) => {
                    if (!old) return old
                    return {
                      ...old,
                      items: old.items.map((d) =>
                        d.name === name && d.namespace === namespace ? { ...d, paused: true } : d,
                      ),
                    }
                  },
                )
              } catch (err) {
                queryClient.refetchQueries({ queryKey: ['resources', 'deployments'], type: 'active' })
                setMutationError({ err, action: 'Pause rollout' })
              } finally {
                setActionLoading(null)
              }
            },
          },
    )
  }
  // Edit metadata — universal across all kinds, always last in the
  // menu. When this is the ONLY item, the toolbar promotes it inline
  // (see render branch below) so the operator doesn't pay a click
  // for a single-entry dropdown.
  menuItems.push({
    id: 'edit-metadata',
    label: 'Edit metadata',
    icon: Tag,
    disabled: !canEdit,
    hint: !canEdit ? 'Editor role required' : 'Edit labels and annotations (kubectl label / annotate)',
    onClick: () => setShowEditMetadata(true),
    separator: isWorkload, // visually group "metadata" apart from the workload-template writes above
  })

  function renderTab() {
    const tab = tabs.find(t => t.id === activeTab)
    if (tab?.soon) {
      return <ComingSoon title={`${tab.label} — Coming Soon`} description="This feature will be available in a future update." />
    }
    switch (activeTab) {
      case 'overview': return <OverviewTab type={type} item={item!} />
      case 'containers': return <ContainersTab item={item!} />
      case 'yaml': return <YAMLTab type={type} namespace={namespace} name={name} canEdit={canEdit} />
      case 'logs': return <LogsTab namespace={namespace} name={name} item={item!} />
      case 'volumes': return <VolumesTab item={item!} />
      case 'related': return <RelatedTab type={type} item={item!} />
      case 'events': return <EventsTab type={type} namespace={namespace} name={name} />
      case 'monitor': return <MonitorTab type={type} item={item!} />
      case 'deploy-pods': return <DeploymentPodsTab namespace={namespace} name={name} />
      case 'deploy-logs': return <DeploymentLogsTab namespace={namespace} name={name} />
      case 'sts-pods': return <StatefulSetPodsTab namespace={namespace} name={name} />
      case 'sts-logs': return <StatefulSetLogsTab namespace={namespace} name={name} />
      case 'ds-pods': return <DaemonSetPodsTab namespace={namespace} name={name} />
      case 'ds-logs': return <DaemonSetLogsTab namespace={namespace} name={name} />
      case 'job-pods': return <JobPodsTab namespace={namespace} name={name} />
      case 'node-pods': return <NodePodsTab nodeName={name} />
      case 'job-logs': return <JobLogsTab namespace={namespace} name={name} />
      case 'history':
        if (type === 'deployments' || type === 'statefulsets' || type === 'daemonsets') {
          return (
            <RevisionTimeline
              type={type as 'deployments' | 'statefulsets' | 'daemonsets'}
              namespace={namespace}
              name={name}
              canEdit={canEdit}
              onRollback={(rev) => setShowRollback({ revision: rev })}
            />
          )
        }
        // Other workload kinds keep the legacy view (for now this
        // path is unused — only the three kinds above expose History).
        if (type === 'deployments') return <HistoryTab namespace={namespace} name={name} />
        return <WorkloadHistoryTab type={type} namespace={namespace} name={name} />
      case 'cronjob-jobs': return <CronJobJobsTab namespace={namespace} name={name} />
      case 'files': return <FilesTab namespace={namespace} name={name} item={item!} />
      case 'terminal':
        if (type === 'pods') return <TerminalTab namespace={namespace} name={name} item={item!} />
        if (type === 'deployments') return <DeploymentTerminalTab namespace={namespace} name={name} />
        if (type === 'statefulsets') return <StatefulSetTerminalTab namespace={namespace} name={name} />
        if (type === 'daemonsets') return <DaemonSetTerminalTab namespace={namespace} name={name} />
        return null
      default: return <ComingSoon title="Coming Soon" description="This feature will be available in a future update." />
    }
  }

  return (
    <div className="space-y-4">
      {/* Breadcrumb */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-1.5 text-[11px] font-mono text-kb-text-tertiary">
          <Link to={parentPath} className="hover:text-kb-text-primary transition-colors">{parentLabel}</Link>
          <ChevronRight size={12} />
          {item.namespace && (
            <>
              <span>{item.namespace}</span>
              <ChevronRight size={12} />
            </>
          )}
          <span className="text-kb-text-primary">{item.name}</span>
        </div>
        <DataFreshnessIndicator dataUpdatedAt={dataUpdatedAt} isFetching={isFetching} />
      </div>

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <div className="flex items-center gap-2">
            <h1 className="text-lg font-semibold text-kb-text-primary">{item.name}</h1>
            {/* SchedulingDisabled badge for cordoned nodes — surfaces
                state at the top of the detail page instead of buried
                in the allocation card. Mirrors the badge on the
                Nodes list cards so the visual language is consistent. */}
            {type === 'nodes' && (item as unknown as { unschedulable?: boolean }).unschedulable === true && (
              <span
                className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-status-warn-dim text-status-warn uppercase tracking-wide whitespace-nowrap"
                title="Node is cordoned — new pods will not be scheduled here"
              >
                SchedulingDisabled
              </span>
            )}
            {/* CronJob suspended badge — same visual language as
                SchedulingDisabled so the operator scans for "this
                resource is in a paused/restricted state" the same
                way across resource types. */}
            {type === 'cronjobs' && (item as unknown as { suspend?: boolean }).suspend === true && (
              <span
                className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-status-warn-dim text-status-warn uppercase tracking-wide whitespace-nowrap"
                title="CronJob is suspended — scheduled runs will not fire until you resume"
              >
                Suspended
              </span>
            )}
            {/* Deployment rollout-paused badge — same visual language
                as SchedulingDisabled / Suspended (Tier 2 #5). Surfaces
                the paused state at the top of the page so the operator
                doesn't have to dig into the YAML to know the
                Deployment controller is frozen. */}
            {type === 'deployments' && (item as unknown as { paused?: boolean }).paused === true && (
              <span
                className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-status-warn-dim text-status-warn uppercase tracking-wide whitespace-nowrap"
                title="Deployment rollout is paused — the deployment controller is not reconciling. Existing pods continue running. Resume to roll forward, or rollback to revert."
              >
                Rollout paused
              </span>
            )}
          </div>
          {item.namespace && <div className="text-xs text-kb-text-tertiary font-mono">Namespace: {item.namespace}</div>}
        </div>
        <div className="flex gap-2 items-center">
          {/* Persistent Ask Copilot — uses the resource_inquiry
              trigger which tailors the prompt to the active tab
              ("interpret these metrics" on Monitor, "summarize log
              errors" on Logs, etc.) without the user spelling it
              out. Always visible so the operator doesn't have to
              wonder whether this resource type is supported. */}
          <AskCopilotButton
            variant="text"
            label="Ask Kobi"
            payload={{
              type: 'resource_inquiry',
              resource: {
                kind: routeToKind[type] ?? type,
                namespace: item.namespace ?? '',
                name: item.name,
                activeTab,
                summary: {
                  ...(item.status ? { status: String(item.status) } : {}),
                  ...(item.ready ? { ready: String(item.ready) } : {}),
                  ...(item.replicas !== undefined ? { replicas: Number(item.replicas) } : {}),
                  ...(item.restarts !== undefined ? { restarts: Number(item.restarts) } : {}),
                  ...(item.age ? { age: String(item.age) } : {}),
                  ...(type === 'services' && item.type ? { serviceType: String(item.type) } : {}),
                  ...(type === 'services' && item.clusterIP ? { clusterIP: String(item.clusterIP) } : {}),
                  ...(type === 'nodes' && item.kubeletVersion ? { kubeletVersion: String(item.kubeletVersion) } : {}),
                  ...(type === 'nodes' && item.osImage ? { osImage: String(item.osImage) } : {}),
                },
              },
            }}
          />
          <button
            onClick={() => refetch()}
            title="Refetch this resource from the apiserver"
            className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors text-kb-text-secondary flex items-center gap-1.5"
          >
            <RefreshCw className="w-3 h-3" />
            Refresh
          </button>
          <button
            onClick={() => setShowDescribe(!showDescribe)}
            title="Open kubectl describe output for this resource"
            className={`px-3 py-1.5 text-xs border rounded-lg transition-colors flex items-center gap-1.5 ${
              showDescribe
                ? 'bg-status-info-dim border-status-info/20 text-status-info'
                : 'bg-kb-card border-kb-border text-kb-text-secondary hover:bg-kb-card-hover'
            }`}
          >
            <FileText className="w-3 h-3" />
            Describe
          </button>
          {/* Workload write actions — Set image kept inline as the most-
              used "deploy a new version" verb. Restart stays inline below.
              Scale, Set resources, Set env, Pause/Resume rollout, Edit
              metadata all moved into the Actions ▾ menu (computed below). */}
          {['deployments', 'statefulsets', 'daemonsets'].includes(type) && (
            <button
              onClick={() => { setShowSetImage(true); setShowRestart(false); setShowScale(false) }}
              disabled={!canEdit}
              title={!canEdit ? 'Editor role required' : 'Set container image (kubectl set image)'}
              className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <ImageIcon className="w-3 h-3" />
              Set image
            </button>
          )}
          {/* Set resources / Set env / Pause-Resume rollout — moved
              into the Actions ▾ menu (rendered below, before Delete).
              See PRIMARY_ACTIONS_BY_TYPE design in the toolbar refactor. */}
          {['deployments', 'statefulsets', 'daemonsets'].includes(type) && (
            <div className="relative">
              <button
                onClick={() => { setShowRestart(!showRestart); setShowScale(false) }}
                disabled={actionLoading === 'restart' || !canEdit}
                title={!canEdit ? 'Editor role required' : undefined}
                className={`px-3 py-1.5 text-xs border rounded-lg transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed ${
                  showRestart ? 'bg-status-warn-dim border-status-warn/20 text-status-warn' : 'bg-kb-card border-kb-border text-kb-text-secondary hover:bg-kb-card-hover'
                }`}
              >
                <RotateCw className={`w-3 h-3 ${actionLoading === 'restart' ? 'animate-spin' : ''}`} />
                Restart
              </button>
              {showRestart && (
                <div className="absolute top-full right-0 mt-1 bg-kb-card border border-kb-border rounded-xl shadow-xl z-50 p-4 w-72">
                  <h4 className="text-sm font-semibold text-kb-text-primary mb-1">
                    Restart {type === 'deployments' ? 'Deployment' : type === 'statefulsets' ? 'StatefulSet' : 'DaemonSet'}
                  </h4>
                  <p className="text-[11px] text-kb-text-tertiary mb-4">
                    This will restart all pods by updating the template with a new restart annotation. This action cannot be undone.
                  </p>
                  <div className="flex gap-2 justify-end">
                    <button
                      onClick={() => setShowRestart(false)}
                      className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors"
                    >
                      Cancel
                    </button>
                    <button
                      onClick={async () => {
                        setActionLoading('restart')
                        setShowRestart(false)
                        try {
                          const res = await api.restartResource(type, namespace, name)
                          if (res.resource) {
                            queryClient.setQueryData(['resource-detail', type, namespace, name], res.resource)
                          }
                          queryClient.invalidateQueries({ queryKey: ['resources'] })
                        } catch (err) {
                          setMutationError({ err, action: 'Restart' })
                        } finally {
                          setActionLoading(null)
                        }
                      }}
                      className="px-3 py-1.5 text-xs font-medium bg-status-info text-white rounded-lg hover:bg-status-info/90 transition-colors flex items-center gap-1.5"
                    >
                      <RotateCw className="w-3 h-3" />
                      Restart
                    </button>
                  </div>
                </div>
              )}
            </div>
          )}
          {/* Node maintenance — toolbar parity with the Nodes list
              cards. Cordon/Uncordon swaps based on spec.unschedulable;
              Drain opens the same modal the list page uses. */}
          {type === 'nodes' && item && (
            <NodeSchedulabilityToolbarButton node={item} canEdit={canEdit} />
          )}
          {type === 'nodes' && (
            <button
              onClick={() => setShowDrain(true)}
              disabled={!hasRole('admin')}
              title={hasRole('admin') ? 'Evict pods (kubectl drain)' : 'Admin role required'}
              className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <AlertCircle className="w-3 h-3" />
              Drain…
            </button>
          )}
          {/* CronJob: trigger now / suspend or resume. Trigger
              opens a small modal (jobName + concurrency warning).
              Suspend/Resume are direct mutations — single click.
              The button switches label/icon based on the current
              spec.suspend so the operator never sees both. */}
          {type === 'cronjobs' && (
            <button
              onClick={() => setShowTrigger(true)}
              disabled={!canEdit}
              title={!canEdit ? 'Editor role required' : 'Run a one-off Job from this CronJob (kubectl create job --from=cronjob/X)'}
              className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <Play className="w-3 h-3" />
              Trigger now
            </button>
          )}
          {type === 'cronjobs' && (item as unknown as { suspend?: boolean }).suspend !== true && (
            <button
              onClick={async () => {
                setActionLoading('suspend')
                try {
                  const res = await api.suspendCronJob(namespace, name, 'ui')
                  if (res.cronJob) {
                    queryClient.setQueryData(['resource-detail', type, namespace, name], res.cronJob)
                  }
                  // Optimistic flip across every cronjobs-list cache
                  // entry. We deliberately do NOT call
                  // invalidateQueries afterward — the backend's GET
                  // path reads from the informer cache which can lag
                  // a Patch by a few hundred ms, returning the
                  // pre-patch suspend=false and overriding our
                  // correct optimistic update. The periodic
                  // refetchInterval reconciles drift later. Same
                  // bug + same fix as cordon/uncordon.
                  queryClient.setQueriesData<{ items: ResourceItem[] }>(
                    { queryKey: ['resources', 'cronjobs'] },
                    (old) => {
                      if (!old) return old
                      return {
                        ...old,
                        items: old.items.map((c) =>
                          c.name === name && c.namespace === namespace
                            ? { ...c, suspend: true }
                            : c,
                        ),
                      }
                    },
                  )
                } catch (err) {
                  // On error, refetch to roll back the optimistic
                  // flip — simpler than snapshotting every variant
                  // of the list cache.
                  queryClient.refetchQueries({ queryKey: ['resources', 'cronjobs'], type: 'active' })
                  setMutationError({ err, action: 'Suspend' })
                } finally {
                  setActionLoading(null)
                }
              }}
              disabled={!canEdit || actionLoading === 'suspend'}
              title={!canEdit ? 'Editor role required' : 'Pause the schedule — no new scheduled runs will fire until resumed'}
              className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <Pause className={`w-3 h-3 ${actionLoading === 'suspend' ? 'animate-pulse' : ''}`} />
              Suspend
            </button>
          )}
          {type === 'cronjobs' && (item as unknown as { suspend?: boolean }).suspend === true && (
            <button
              onClick={async () => {
                setActionLoading('resume')
                try {
                  const res = await api.resumeCronJob(namespace, name, 'ui')
                  if (res.cronJob) {
                    queryClient.setQueryData(['resource-detail', type, namespace, name], res.cronJob)
                  }
                  // Same optimistic-flip-no-invalidate dance as
                  // suspend above — see comment there.
                  queryClient.setQueriesData<{ items: ResourceItem[] }>(
                    { queryKey: ['resources', 'cronjobs'] },
                    (old) => {
                      if (!old) return old
                      return {
                        ...old,
                        items: old.items.map((c) =>
                          c.name === name && c.namespace === namespace
                            ? { ...c, suspend: false }
                            : c,
                        ),
                      }
                    },
                  )
                } catch (err) {
                  queryClient.refetchQueries({ queryKey: ['resources', 'cronjobs'], type: 'active' })
                  setMutationError({ err, action: 'Resume' })
                } finally {
                  setActionLoading(null)
                }
              }}
              disabled={!canEdit || actionLoading === 'resume'}
              title={!canEdit ? 'Editor role required' : 'Resume the schedule — scheduled runs will fire again'}
              className="px-3 py-1.5 text-xs bg-status-ok-dim border border-status-ok/30 rounded-lg text-status-ok hover:bg-status-ok/20 transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <Play className={`w-3 h-3 ${actionLoading === 'resume' ? 'animate-pulse' : ''}`} />
              Resume
            </button>
          )}
          {/* Reveal — Tier 2 #9. Only for Secrets. Editor+ at the
              route level; the backend escalates to Admin for
              production-pattern namespaces. The yellow-warn styling
              telegraphs that this is a sensitive action distinct
              from the routine edit operations next to it. */}
          {type === 'secrets' && (
            <button
              onClick={() => { setShowSecretReveal(true); setShowRestart(false); setShowScale(false) }}
              disabled={!canEdit}
              title={!canEdit ? 'Editor role required' : 'Decode and reveal Secret values (audited; reason required)'}
              className="px-3 py-1.5 text-xs bg-status-warn-dim border border-status-warn/30 rounded-lg text-status-warn hover:bg-status-warn/20 transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <Eye className="w-3 h-3" />
              Reveal
            </button>
          )}
          {/* Actions ▾ menu — overflow dropdown for the less-frequent
              writes (Scale, Set resources, Set env, Pause/Resume
              rollout, Edit metadata). Computed via menuItems below
              based on kind. When the menu would only have one item
              (eg Pod / Service / ConfigMap with just Edit metadata),
              we render that item inline directly to avoid a sad
              "Actions ▾" dropdown that contains a single thing. */}
          {menuItems.length > 1 ? (
            <ResourceActionsMenu items={menuItems} />
          ) : menuItems.length === 1 ? (
            <button
              onClick={() => { menuItems[0].onClick(); setShowRestart(false); setShowScale(false) }}
              disabled={menuItems[0].disabled}
              title={menuItems[0].hint}
              className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-secondary hover:bg-kb-card-hover transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {(() => {
                const Icon = menuItems[0].icon
                return <Icon className="w-3 h-3" />
              })()}
              {menuItems[0].label}
            </button>
          ) : null}
          <button
            onClick={() => { setShowDelete(true); setShowRestart(false); setShowScale(false) }}
            disabled={!canDelete}
            title={!canDelete ? 'Admin role required' : undefined}
            className="px-3 py-1.5 text-xs bg-status-error-dim border border-status-error/20 rounded-lg text-status-error hover:bg-status-error/20 transition-colors flex items-center gap-1.5 disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-status-error-dim"
          >
            <Trash2 className="w-3 h-3" />
            Delete
          </button>
        </div>
      </div>

      {/* Describe modal */}
      {showDescribe && (
        <DescribeModal type={type} namespace={namespace} name={name} onClose={() => setShowDescribe(false)} />
      )}

      {/* Set image modal */}
      {showSetImage && ['deployments', 'statefulsets', 'daemonsets'].includes(type) && (
        <SetImageModal
          type={type as 'deployments' | 'statefulsets' | 'daemonsets'}
          namespace={namespace}
          name={name}
          resource={item}
          onClose={() => setShowSetImage(false)}
        />
      )}

      {/* Set resources modal — Tier 2 #6 */}
      {showSetResources && ['deployments', 'statefulsets', 'daemonsets'].includes(type) && (
        <SetResourcesModal
          type={type as 'deployments' | 'statefulsets' | 'daemonsets'}
          namespace={namespace}
          name={name}
          resource={item}
          onClose={() => setShowSetResources(false)}
        />
      )}

      {/* Set env modal — Tier 2 #7 */}
      {showSetEnv && ['deployments', 'statefulsets', 'daemonsets'].includes(type) && (
        <SetEnvModal
          type={type as 'deployments' | 'statefulsets' | 'daemonsets'}
          namespace={namespace}
          name={name}
          resource={item}
          onClose={() => setShowSetEnv(false)}
        />
      )}

      {/* Edit metadata modal — Tier 2 #8. Universal across kinds. */}
      {showEditMetadata && (
        <EditMetadataModal
          type={type}
          namespace={namespace}
          name={name}
          resource={item}
          onClose={() => setShowEditMetadata(false)}
        />
      )}

      {/* Secret reveal modal — Tier 2 #9. Only mounts for Secrets. */}
      {showSecretReveal && type === 'secrets' && (
        <SecretRevealModal
          namespace={namespace}
          name={name}
          resource={item}
          onClose={() => setShowSecretReveal(false)}
        />
      )}

      {/* Scale modal — Tier 1 (refactored). Used to be an inline
          toolbar popover; moved into a modal so the Actions ▾ menu
          item has a clean trigger. */}
      {showScale && (type === 'deployments' || type === 'statefulsets') && (
        <ScaleModal
          type={type as 'deployments' | 'statefulsets'}
          namespace={namespace}
          name={name}
          currentReplicas={Number(item.replicas ?? 1)}
          onClose={() => setShowScale(false)}
        />
      )}

      {/* Rollback confirmation modal */}
      {showRollback && ['deployments', 'statefulsets', 'daemonsets'].includes(type) && (
        <RollbackModal
          type={type as 'deployments' | 'statefulsets' | 'daemonsets'}
          namespace={namespace}
          name={name}
          targetRevision={showRollback.revision}
          resource={item}
          onClose={() => setShowRollback(null)}
        />
      )}

      {/* CronJob trigger modal */}
      {showTrigger && type === 'cronjobs' && item && (
        <CronJobTriggerModal
          cronJob={item}
          onClose={() => setShowTrigger(false)}
        />
      )}

      {/* Node drain modal — same component the Nodes list page
          uses. Mounted here only for type=nodes so the show/hide
          state doesn't leak across resource types. */}
      {showDrain && type === 'nodes' && item && (
        <DrainModal node={item} onClose={() => setShowDrain(false)} />
      )}

      {/* Delete modal */}
      {showDelete && (
        <DeleteModal
          type={type}
          namespace={namespace}
          name={item.name}
          onClose={() => setShowDelete(false)}
          onDeleted={() => {
            queryClient.invalidateQueries({ queryKey: ['resources'] })
            navigate(`/${type}`)
          }}
        />
      )}

      {/* Mutation error toast — fixed bottom-right; replaces the
          old alert() calls for restart/scale/etc. */}
      {mutationError && (
        <MutationErrorToast
          error={mutationError.err}
          action={mutationError.action}
          onDismiss={() => setMutationError(null)}
        />
      )}

      {/* Tabs */}
      <div className="flex gap-1 border-b border-kb-border">
        {tabs.map(tab => (
          <button
            key={tab.id}
            onClick={() => !tab.soon ? setActiveTab(tab.id) : setActiveTab(tab.id)}
            className={`px-3 py-2 text-xs font-medium transition-colors relative ${
              activeTab === tab.id
                ? 'text-status-info border-b-2 border-status-info -mb-px'
                : 'text-kb-text-tertiary hover:text-kb-text-secondary'
            }`}
          >
            {tab.label}
            {tab.count != null && (
              <span className="ml-1.5 px-1.5 py-0.5 rounded-full text-[9px] bg-kb-elevated">{tab.count}</span>
            )}
            {tab.soon && (
              <span className="ml-1 text-[8px] opacity-50">SOON</span>
            )}
          </button>
        ))}
      </div>

      {/* Tab Content */}
      {renderTab()}
    </div>
  )
}
