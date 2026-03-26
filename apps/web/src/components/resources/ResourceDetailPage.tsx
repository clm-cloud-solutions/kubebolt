import React, { useState, useEffect, useRef } from 'react'
import { useParams, Link } from 'react-router-dom'
import { ChevronRight, Lock } from 'lucide-react'
import { useResourceDetail, useResourceYAML, useResourceEvents, useTopology, usePodLogs, useDeploymentPods, useDeploymentHistory, useStatefulSetPods, useDaemonSetPods, useJobPods } from '@/hooks/useResources'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { DataFreshnessIndicator } from '@/components/shared/DataFreshnessIndicator'
import { StatusBadge } from './StatusBadge'
import { ResourceUsageCell } from '@/components/shared/ResourceUsageCell'
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
        { id: 'terminal', label: 'Terminal', soon: true },
        { id: 'files', label: 'Files', soon: true },
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
        { id: 'terminal', label: 'Terminal', soon: true },
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
        { id: 'terminal', label: 'Terminal', soon: true },
        { id: 'related', label: 'Related' },
        { id: 'events', label: 'Events' },
        { id: 'monitor', label: 'Monitor' },
      )
      break
    case 'daemonsets':
      base.push(
        { id: 'yaml', label: 'YAML' },
        { id: 'ds-pods', label: 'Pods' },
        { id: 'ds-logs', label: 'Logs' },
        { id: 'terminal', label: 'Terminal', soon: true },
        { id: 'related', label: 'Related' },
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

function StatusOverview({ type, item }: { type: string; item: ResourceItem }) {
  const metrics: { label: string; value: React.ReactNode }[] = []

  switch (type) {
    case 'pods':
      metrics.push(
        { label: 'Phase', value: <div className="flex items-center gap-2"><span className={`w-2.5 h-2.5 rounded-full ${item.status === 'Running' ? 'bg-status-ok' : 'bg-status-warn'}`} />{item.status}</div> },
        { label: 'Ready Containers', value: String(item.ready ?? '-') },
        { label: 'Restart Count', value: String(item.restarts ?? 0) },
        { label: 'Node', value: item.nodeName ? <ResourceLink name={String(item.nodeName)} resourceType="nodes" /> : '-' },
      )
      break
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
        { label: 'Status', value: <div className="flex items-center gap-2"><StatusBadge status={item.status} />{item.status}</div> },
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
          {type === 'pods' && ownerRefs.length > 0 && (
            <InfoField label="Owner">
              <KindNameLink value={`${ownerRefs[0].kind}/${ownerRefs[0].name}`} namespace={item.namespace} />
            </InfoField>
          )}
          {type === 'pods' && Array.isArray(item.containers) && (
            <InfoField label="Ports">
              {(item.containers as Array<Record<string, unknown>>)
                .flatMap(c => Array.isArray(c.ports) ? (c.ports as Array<Record<string, unknown>>).map(p => String(p.containerPort)) : [])
                .join(', ') || '-'}
            </InfoField>
          )}
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
          {type === 'httproutes' && item.gateway != null && <InfoField label="Gateway"><ResourceLink name={String(item.gateway)} namespace={item.namespace} resourceType="gateways" /></InfoField>}
        </div>

        {/* Labels & Annotations */}
        <div className="grid grid-cols-2 gap-x-8 gap-y-4 mt-5 pt-4 border-t border-kb-border">
          <InfoField label="Labels"><Labels labels={item.labels} /></InfoField>
          <InfoField label="Annotations"><Labels labels={item.annotations} /></InfoField>
        </div>
      </Section>

      {/* Metrics */}
      <MetricsBar item={item} />

      {/* Conditions */}
      <ConditionsSection conditions={item.conditions} />
    </div>
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
              <InfoField label="Restart Count">{String(state?.restartCount ?? 0)}</InfoField>
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

function YAMLTab({ type, namespace, name }: { type: string; namespace: string; name: string }) {
  const { data: yaml, isLoading, error } = useResourceYAML(type, namespace, name)

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} />

  const lines = (yaml ?? '').split('\n')

  return (
    <Section title="YAML Configuration" className="relative">
      <div className="absolute top-4 right-5 flex gap-2">
        <button className="px-3 py-1.5 text-[10px] font-mono bg-kb-elevated text-kb-text-tertiary rounded cursor-not-allowed" disabled>
          Save <span className="text-[8px] ml-1 opacity-60">SOON</span>
        </button>
      </div>
      <div className="overflow-auto max-h-[600px] rounded-lg p-3" style={{ backgroundColor: '#0d1117', color: '#c9d1d9' }}>
        <pre className="text-[11px] font-mono leading-5">
          {lines.map((line, i) => (
            <div key={i} className="flex">
              <span className="w-10 text-right pr-3 select-none shrink-0" style={{ color: '#484f58' }}>{i + 1}</span>
              <span>{highlightYAMLLine(line)}</span>
            </div>
          ))}
        </pre>
      </div>
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

function MonitorTab({ item }: { item: ResourceItem }) {
  const cpuUsage = Number(item.cpuUsage ?? 0)
  const cpuPercent = Number(item.cpuPercent ?? 0)
  const memUsage = Number(item.memoryUsage ?? 0)
  const memPercent = Number(item.memoryPercent ?? 0)

  if (cpuUsage === 0 && memUsage === 0) {
    return (
      <div className="text-sm text-kb-text-tertiary text-center py-12">
        No metrics available. Metrics Server may not be installed or this resource type does not report metrics.
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="bg-status-warn-dim border border-status-warn/20 rounded-lg px-4 py-2 text-[11px] text-status-warn">
        Current data is from metrics-server (point-in-time snapshot). Historical time-series requires KubeBolt Agent.
      </div>

      <div className="grid grid-cols-2 gap-4">
        {/* CPU */}
        <Section title="CPU Usage">
          <div className="flex flex-col items-center py-8">
            <div className="relative w-32 h-32">
              <svg className="w-32 h-32 -rotate-90" viewBox="0 0 120 120">
                <circle cx="60" cy="60" r="52" fill="none" stroke="var(--kb-border)" strokeWidth="10" />
                <circle cx="60" cy="60" r="52" fill="none" stroke="#4c9aff" strokeWidth="10"
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

      {/* Network & Disk placeholders */}
      <div className="grid grid-cols-2 gap-4">
        <Section title="Network Usage">
          <div className="text-sm text-kb-text-tertiary text-center py-8">
            Requires KubeBolt Agent for network metrics
          </div>
        </Section>
        <Section title="Disk I/O Usage">
          <div className="text-sm text-kb-text-tertiary text-center py-8">
            Requires KubeBolt Agent for disk metrics
          </div>
        </Section>
      </div>
    </div>
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
            <th className="pb-2 font-normal">Namespace</th>
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
              <td className="py-2 font-mono">{pod.namespace}</td>
              <td className="py-2"><StatusBadge status={pod.status} /></td>
              <td className="py-2 w-36 pr-6">
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" />
              </td>
              <td className="py-2 font-mono">{String(pod.restarts ?? 0)}</td>
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
            <th className="pb-2 font-normal">Namespace</th>
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
              <td className="py-2 font-mono">{pod.namespace}</td>
              <td className="py-2"><StatusBadge status={pod.status} /></td>
              <td className="py-2 w-36 pr-6">
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" />
              </td>
              <td className="py-2 font-mono">{String(pod.restarts ?? 0)}</td>
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
            <th className="pb-2 font-normal">Namespace</th>
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
              <td className="py-2 font-mono">{pod.namespace}</td>
              <td className="py-2">
                {pod.nodeName ? <ResourceLink name={String(pod.nodeName)} resourceType="nodes" /> : '-'}
              </td>
              <td className="py-2"><StatusBadge status={pod.status} /></td>
              <td className="py-2 w-36 pr-6">
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" />
              </td>
              <td className="py-2 font-mono">{String(pod.restarts ?? 0)}</td>
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
                <ResourceUsageCell usage={Number(pod.cpuUsage ?? 0)} request={Number(pod.cpuRequest ?? 0)} limit={Number(pod.cpuLimit ?? 0)} percent={Number(pod.cpuPercent ?? 0)} type="cpu" />
              </td>
              <td className="py-2 w-36 pl-2">
                <ResourceUsageCell usage={Number(pod.memoryUsage ?? 0)} request={Number(pod.memoryRequest ?? 0)} limit={Number(pod.memoryLimit ?? 0)} percent={Number(pod.memoryPercent ?? 0)} type="memory" />
              </td>
              <td className="py-2 font-mono">{String(pod.restarts ?? 0)}</td>
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

// ─── History Tab (Deployments) ───────────────────────────────────

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

// ─── Main Component ──────────────────────────────────────────────

export function ResourceDetailPage() {
  const { type = '', namespace = '', name = '' } = useParams<{ type: string; namespace: string; name: string }>()
  const { data: item, isLoading, error, refetch, dataUpdatedAt, isFetching } = useResourceDetail(type, namespace, name)
  const [activeTab, setActiveTab] = useState('overview')

  // Reset to overview tab when navigating to a different resource
  useEffect(() => {
    setActiveTab('overview')
  }, [type, namespace, name])

  if (isLoading) return <LoadingSpinner />
  if (error || !item) return <ErrorState message={error?.message ?? 'Resource not found'} onRetry={() => refetch()} />

  const tabs = getTabsForResource(type, item)
  const parentLabel = resourceLabels[type] || type
  const parentPath = `/${type}`

  function renderTab() {
    const tab = tabs.find(t => t.id === activeTab)
    if (tab?.soon) {
      return <ComingSoon title={`${tab.label} — Coming Soon`} description="This feature will be available in a future update." />
    }
    switch (activeTab) {
      case 'overview': return <OverviewTab type={type} item={item!} />
      case 'containers': return <ContainersTab item={item!} />
      case 'yaml': return <YAMLTab type={type} namespace={namespace} name={name} />
      case 'logs': return <LogsTab namespace={namespace} name={name} item={item!} />
      case 'volumes': return <VolumesTab item={item!} />
      case 'related': return <RelatedTab type={type} item={item!} />
      case 'events': return <EventsTab type={type} namespace={namespace} name={name} />
      case 'monitor': return <MonitorTab item={item!} />
      case 'deploy-pods': return <DeploymentPodsTab namespace={namespace} name={name} />
      case 'deploy-logs': return <DeploymentLogsTab namespace={namespace} name={name} />
      case 'sts-pods': return <StatefulSetPodsTab namespace={namespace} name={name} />
      case 'sts-logs': return <StatefulSetLogsTab namespace={namespace} name={name} />
      case 'ds-pods': return <DaemonSetPodsTab namespace={namespace} name={name} />
      case 'ds-logs': return <DaemonSetLogsTab namespace={namespace} name={name} />
      case 'job-pods': return <JobPodsTab namespace={namespace} name={name} />
      case 'job-logs': return <JobLogsTab namespace={namespace} name={name} />
      case 'history': return <HistoryTab namespace={namespace} name={name} />
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
          <h1 className="text-lg font-semibold text-kb-text-primary">{item.name}</h1>
          {item.namespace && <div className="text-xs text-kb-text-tertiary font-mono">Namespace: {item.namespace}</div>}
        </div>
        <div className="flex gap-2">
          <button onClick={() => refetch()} className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg hover:bg-kb-card-hover transition-colors text-kb-text-secondary">
            Refresh
          </button>
          <button className="px-3 py-1.5 text-xs bg-kb-card border border-kb-border rounded-lg text-kb-text-tertiary cursor-not-allowed" disabled>
            Describe <span className="text-[8px] ml-1 opacity-60">SOON</span>
          </button>
          <button className="px-3 py-1.5 text-xs bg-status-error-dim border border-status-error/20 rounded-lg text-status-error cursor-not-allowed" disabled>
            Delete <span className="text-[8px] ml-1 opacity-60">SOON</span>
          </button>
        </div>
      </div>

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
