import { memo } from 'react'
import { Handle, Position, type NodeProps } from 'reactflow'
import {
  Box, Server, Layers, Globe, Database, FileText, Lock,
  Scale, HardDrive, Disc, ArrowRightLeft, Timer, Clock, BarChart3,
  Cloud,
} from 'lucide-react'
import { getDotColor } from '@/utils/colors'
import { UsageBar } from '@/components/resources/UsageBar'
import type { TopologyNode } from '@/types/kubernetes'

const kindIcons: Record<string, React.ReactNode> = {
  Pod:                    <Box className="w-3.5 h-3.5" />,
  Node:                   <Server className="w-3.5 h-3.5" />,
  Deployment:             <Layers className="w-3.5 h-3.5" />,
  Service:                <Globe className="w-3.5 h-3.5" />,
  Ingress:                <ArrowRightLeft className="w-3.5 h-3.5" />,
  StatefulSet:            <Database className="w-3.5 h-3.5" />,
  DaemonSet:              <BarChart3 className="w-3.5 h-3.5" />,
  ReplicaSet:             <Layers className="w-3.5 h-3.5" />,
  ConfigMap:              <FileText className="w-3.5 h-3.5" />,
  Secret:                 <Lock className="w-3.5 h-3.5" />,
  HPA:                    <Scale className="w-3.5 h-3.5" />,
  HorizontalPodAutoscaler: <Scale className="w-3.5 h-3.5" />,
  PersistentVolumeClaim:  <HardDrive className="w-3.5 h-3.5" />,
  PersistentVolume:       <Disc className="w-3.5 h-3.5" />,
  Job:                    <Timer className="w-3.5 h-3.5" />,
  CronJob:                <Clock className="w-3.5 h-3.5" />,
  Gateway:                <Globe className="w-3.5 h-3.5" />,
  HTTPRoute:              <ArrowRightLeft className="w-3.5 h-3.5" />,
  // Synthetic kind injected by the cluster map for pod-to-external
  // destinations discovered via Hubble. Not a real Kubernetes
  // resource — never appears in topology from the backend.
  ExternalEndpoint:       <Cloud className="w-3.5 h-3.5" />,
}

const kindAccent: Record<string, { bg: string; text: string; border: string }> = {
  Deployment:    { bg: 'rgba(34,214,138,0.10)', text: '#22d68a', border: 'border-[#22d68a]/20' },
  StatefulSet:   { bg: 'rgba(34,214,138,0.10)', text: '#22d68a', border: 'border-[#22d68a]/20' },
  DaemonSet:     { bg: 'rgba(34,214,138,0.10)', text: '#22d68a', border: 'border-[#22d68a]/20' },
  Service:       { bg: 'rgba(29,189,125,0.08)', text: '#1DBD7D', border: 'border-[#1DBD7D]/20' },
  Ingress:       { bg: 'rgba(167,139,250,0.08)', text: '#a78bfa', border: 'border-[#a78bfa]/20' },
  ConfigMap:     { bg: 'rgba(255,255,255,0.04)', text: 'var(--kb-text-tertiary)', border: 'border-[#555770]/30' },
  Secret:        { bg: 'rgba(245,166,35,0.10)', text: '#f5a623', border: 'border-[#f5a623]/20' },
  HPA:           { bg: 'rgba(167,139,250,0.08)', text: '#a78bfa', border: 'border-[#a78bfa]/20' },
  PersistentVolumeClaim: { bg: 'rgba(34,211,238,0.08)', text: '#22d3ee', border: 'border-[#22d3ee]/20' },
  PersistentVolume:      { bg: 'rgba(34,211,238,0.08)', text: '#22d3ee', border: 'border-[#22d3ee]/20' },
  Pod:           { bg: 'rgba(34,214,138,0.06)', text: '#22d68a', border: 'border-[#22d68a]/15' },
  Node:          { bg: 'rgba(29,189,125,0.08)', text: '#1DBD7D', border: 'border-[#1DBD7D]/20' },
  Job:           { bg: 'rgba(245,166,35,0.08)', text: '#f5a623', border: 'border-[#f5a623]/20' },
  CronJob:       { bg: 'rgba(245,166,35,0.08)', text: '#f5a623', border: 'border-[#f5a623]/20' },
  Gateway:       { bg: 'rgba(167,139,250,0.08)', text: '#a78bfa', border: 'border-[#a78bfa]/20' },
  HTTPRoute:     { bg: 'rgba(167,139,250,0.08)', text: '#a78bfa', border: 'border-[#a78bfa]/20' },
  // Sky-blue for external peers so they read clearly as "outside the
  // cluster" without clashing with Service (teal) or Ingress (violet).
  ExternalEndpoint: { bg: 'rgba(56,189,248,0.08)', text: '#38bdf8', border: 'border-[#38bdf8]/25' },
}

const defaultAccent = { bg: 'rgba(255,255,255,0.04)', text: 'var(--kb-text-tertiary)', border: 'border-kb-border' }

const OK_STATUSES = new Set(['running', 'ready', 'active', 'bound', 'succeeded', 'programmed', 'accepted', 'available'])
const ERROR_STATUSES = new Set(['failed', 'error', 'crashloopbackoff', 'imagepullbackoff', 'evicted', 'oomkilled'])

// Extend TopologyNode with the runtime flag injected by ClusterMap.
interface ResourceNodeData extends TopologyNode {
  animationsEnabled?: boolean
}

function ResourceNodeComponent({ data, selected }: NodeProps<ResourceNodeData>) {
  const kind = data.type || data.kind || ''
  const icon = kindIcons[kind] || <Box className="w-3.5 h-3.5" />
  const accent = kindAccent[kind] || defaultAccent

  const status = (data.status || '').toLowerCase()
  const isOk = OK_STATUSES.has(status)
  const isError = ERROR_STATUSES.has(status)
  const pulsing = data.animationsEnabled !== false && (isOk || isError)

  return (
    <div className="relative w-[170px]">
      {/* Pulse halo — absolute under the card, same rounded corners.
          Green for healthy resources, red for errors. Reduced motion honored. */}
      {pulsing && (
        <div
          className="absolute inset-0 rounded-[10px] pointer-events-none motion-reduce:hidden animate-kb-node-pulse"
          style={{
            boxShadow: isError
              ? '0 0 0 0 rgba(239, 64, 86, 0.45)'
              : '0 0 0 0 rgba(34, 214, 138, 0.40)',
          }}
        />
      )}
      <div
        className={`relative bg-kb-card border ${accent.border} rounded-[10px] p-2.5 w-[170px] transition-all ${
          selected ? 'ring-1 ring-status-info shadow-lg shadow-status-info/10' : 'hover:bg-kb-card-hover'
        }`}
      >
      <Handle type="target" position={Position.Left} className="!bg-kb-text-tertiary !border-kb-bg !w-1.5 !h-1.5 !-left-1" />
      <Handle type="source" position={Position.Right} className="!bg-kb-text-tertiary !border-kb-bg !w-1.5 !h-1.5 !-right-1" />

      {/* Header: icon + name */}
      <div className="flex items-center gap-2 mb-1.5">
        <div
          className="w-5 h-5 rounded-[5px] flex items-center justify-center shrink-0"
          style={{ background: accent.bg, color: accent.text }}
        >
          {icon}
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-[11px] font-medium text-kb-text-primary truncate leading-tight">
            {data.label}
          </div>
        </div>
        <div className={`w-2 h-2 rounded-full shrink-0 ${getDotColor(data.status)}`} />
      </div>

      {/* Type + status. For ExternalEndpoint nodes we swap the ugly
          "EXTERNALENDPOINT" label for a shorter "EXTERNAL", and if
          we know both a hostname and the resolved IP we show the IP
          inline so users can correlate with raw netstat / packet
          captures. */}
      <div className="flex items-center justify-between mb-1.5">
        <span className="text-[8px] font-mono text-kb-text-tertiary uppercase tracking-[0.04em]">
          {kind === 'ExternalEndpoint' ? 'EXTERNAL' : kind}
        </span>
        {kind === 'ExternalEndpoint' && data.metadata?.ip ? (
          <span className="text-[9px] font-mono text-kb-text-tertiary truncate ml-2">
            {data.metadata.ip}
          </span>
        ) : data.metadata?.replicas ? (
          <span className="text-[9px] font-mono text-kb-text-secondary">{data.metadata.replicas}</span>
        ) : null}
      </div>

      {/* Pod dots */}
      {data.pods && data.pods.length > 0 && (
        <div className="flex gap-[3px] mb-1.5 flex-wrap">
          {data.pods.map((pod) => (
            <div
              key={pod.name}
              className={`w-[7px] h-[7px] rounded-full ${getDotColor(pod.status)}`}
              title={`${pod.name}: ${pod.status}`}
            />
          ))}
        </div>
      )}

      {/* CPU/Memory micro bars */}
      {data.cpu && (
        <div className="space-y-1 mt-1">
          <div className="flex items-center gap-1.5">
            <span className="text-[7px] font-mono text-kb-text-tertiary uppercase w-6">CPU</span>
            <UsageBar percent={data.cpu.percentUsed ?? 0} height={2} className="flex-1" />
          </div>
          {data.memory && (
            <div className="flex items-center gap-1.5">
              <span className="text-[7px] font-mono text-kb-text-tertiary uppercase w-6">MEM</span>
              <UsageBar percent={data.memory.percentUsed ?? 0} height={2} className="flex-1" />
            </div>
          )}
        </div>
      )}
      </div>
    </div>
  )
}

export const ResourceNode = memo(ResourceNodeComponent)
