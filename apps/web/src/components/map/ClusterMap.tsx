import { useState, useMemo, useCallback, useEffect, useLayoutEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import ReactFlow, {
  Background,
  MiniMap,
  ReactFlowProvider,
  useReactFlow,
  useNodesState,
  type Node,
  type Edge,
  type NodeTypes,
  type EdgeTypes,
} from 'reactflow'
import 'reactflow/dist/style.css'
import dagre from '@dagrejs/dagre'
import { LayoutGrid, GitBranch, Waypoints, Zap, ZapOff, RotateCcw } from 'lucide-react'
import { useTopology } from '@/hooks/useTopology'
import { useFlowEdges } from '@/hooks/useFlowEdges'
import { LoadingSpinner } from '@/components/shared/LoadingSpinner'
import { ErrorState } from '@/components/shared/ErrorState'
import { ResourceNode } from './ResourceNode'
import { NamespaceRegion } from './NamespaceRegion'
import { ConnectionEdge } from './ConnectionEdge'
import { MapControls } from './MapControls'
import { NodeDetailPanel } from './NodeDetailPanel'
import type { TopologyNode, TopologyEdge } from '@/types/kubernetes'
import type { L7Summary } from '@/services/api'

// Running totals for an intent edge's L7 data. requestsPerSec and
// statusClass sum directly; latencyWeight tracks Σ(avgLatencyMs *
// requestsPerSec) so finalizeL7 can produce a rate-weighted average.
type L7Aggregator = {
  requestsPerSec: number
  statusClass: Record<string, number>
  latencyWeight: number
  latencyReqs: number
}

function mergeL7(hop: { l7?: L7Aggregator }, src?: L7Summary) {
  if (!src) return
  if (!hop.l7) {
    hop.l7 = { requestsPerSec: 0, statusClass: {}, latencyWeight: 0, latencyReqs: 0 }
  }
  hop.l7.requestsPerSec += src.requestsPerSec || 0
  for (const [k, v] of Object.entries(src.statusClass || {})) {
    if (typeof v === 'number') hop.l7.statusClass[k] = (hop.l7.statusClass[k] ?? 0) + v
  }
  if (src.avgLatencyMs && src.requestsPerSec) {
    hop.l7.latencyWeight += src.avgLatencyMs * src.requestsPerSec
    hop.l7.latencyReqs += src.requestsPerSec
  }
}

function finalizeL7(a: L7Aggregator): L7Summary {
  return {
    requestsPerSec: a.requestsPerSec,
    statusClass: a.statusClass,
    avgLatencyMs: a.latencyReqs > 0 ? a.latencyWeight / a.latencyReqs : undefined,
  }
}

const STATUS_CLASS_LABEL: Record<string, string> = {
  ok: '2xx', redir: '3xx', client_err: '4xx', server_err: '5xx', info: '1xx', unknown: '?',
}

const STATUS_CLASS_COLOR: Record<string, string> = {
  ok: '#10b981',
  redir: '#a78bfa',
  client_err: '#f59e0b',
  server_err: '#f43f5e',
  info: '#64748b',
  unknown: '#64748b',
}

// Structured tooltip payload. Carried in edge.data.tooltip so the
// hovered-edge overlay in ClusterMap can render in the same visual
// format as the Monitor tab's chart tooltip — header with title + rule,
// rows of colored dot / label / value.
export interface TrafficTooltipData {
  rate: number
  verdict: string
  l7?: L7Summary
}

function buildTrafficTooltip(
  rate: number,
  verdict: string,
  l7?: L7Summary,
): TrafficTooltipData {
  return { rate, verdict, l7 }
}

// Single row inside the edge hover tooltip. Shape mirrors the Monitor
// tab's chart tooltip: colored dot on the left, label, value flush right.
function TooltipRow({ color, label, value }: { color: string; label: string; value: string }) {
  return (
    <div className="flex items-center gap-2">
      <span
        className="w-2 h-2 rounded-full flex-shrink-0"
        style={{ background: color }}
      />
      <span className="text-kb-text-secondary truncate max-w-[140px]">{label}</span>
      <span className="ml-auto tabular-nums font-mono text-kb-text-primary">{value}</span>
    </div>
  )
}

const nodeTypes: NodeTypes = {
  resource: ResourceNode,
  namespaceRegion: NamespaceRegion,
}
const edgeTypes: EdgeTypes = { connection: ConnectionEdge }

const NS_COLORS = [
  { border: 'rgba(34,214,138,0.15)', bg: 'rgba(34,214,138,0.03)', text: '#22d68a' },
  { border: 'rgba(245,166,35,0.15)', bg: 'rgba(245,166,35,0.03)', text: '#f5a623' },
  { border: 'rgba(29,189,125,0.15)', bg: 'rgba(29,189,125,0.03)', text: '#1DBD7D' },
  { border: 'rgba(167,139,250,0.15)', bg: 'rgba(167,139,250,0.03)', text: '#a78bfa' },
  { border: 'rgba(34,211,238,0.15)', bg: 'rgba(34,211,238,0.03)', text: '#22d3ee' },
  { border: 'rgba(239,64,86,0.15)', bg: 'rgba(239,64,86,0.03)', text: '#ef4056' },
  { border: 'rgba(251,191,36,0.15)', bg: 'rgba(251,191,36,0.03)', text: '#fbbf24' },
  { border: 'rgba(74,222,128,0.15)', bg: 'rgba(74,222,128,0.03)', text: '#4ade80' },
]

const KIND_ORDER: string[] = [
  'Deployment', 'StatefulSet', 'DaemonSet', 'ReplicaSet',
  'Pod', 'Service', 'Ingress', 'Gateway', 'HTTPRoute',
  'ConfigMap', 'Secret', 'HPA', 'HorizontalPodAutoscaler',
  'PersistentVolumeClaim', 'PersistentVolume',
  'Job', 'CronJob', 'Node',
]

const KIND_SHORT: Record<string, string> = {
  Deployment: 'Deploys', StatefulSet: 'StatefulSets', DaemonSet: 'DaemonSets',
  ReplicaSet: 'ReplicaSets', Pod: 'Pods', Service: 'Services', Ingress: 'Ingresses',
  Gateway: 'Gateways', HTTPRoute: 'HTTPRoutes',
  ConfigMap: 'ConfigMaps', Secret: 'Secrets',
  HPA: 'HPAs', HorizontalPodAutoscaler: 'HPAs',
  PersistentVolumeClaim: 'PVCs', PersistentVolume: 'PVs',
  Job: 'Jobs', CronJob: 'CronJobs', Node: 'Nodes',
}

const KIND_FULL: Record<string, string> = {
  Deployment: 'Deployments', StatefulSet: 'StatefulSets', DaemonSet: 'DaemonSets',
  ReplicaSet: 'ReplicaSets', Pod: 'Pods', Service: 'Services', Ingress: 'Ingresses',
  Gateway: 'Gateways (gateway.networking.k8s.io)', HTTPRoute: 'HTTPRoutes (gateway.networking.k8s.io)',
  ConfigMap: 'ConfigMaps', Secret: 'Secrets',
  HPA: 'HorizontalPodAutoscalers', HorizontalPodAutoscaler: 'HorizontalPodAutoscalers',
  PersistentVolumeClaim: 'PersistentVolumeClaims', PersistentVolume: 'PersistentVolumes',
  Job: 'Jobs', CronJob: 'CronJobs', Node: 'Nodes',
}

// Flow layout: column order for horizontal flow (left → right)
const FLOW_COLUMNS: string[][] = [
  ['Ingress', 'Gateway'],
  ['HTTPRoute'],
  ['Service'],
  ['Deployment', 'StatefulSet', 'DaemonSet', 'CronJob'],
  ['ReplicaSet', 'Job'],
  ['Pod'],
  ['ConfigMap', 'Secret'],
  ['HPA'],
  ['PersistentVolumeClaim'],
  ['PersistentVolume'],
  ['Node'],
]

const KIND_TO_ROUTE: Record<string, string> = {
  Pod: 'pods', Node: 'nodes', Deployment: 'deployments', StatefulSet: 'statefulsets',
  DaemonSet: 'daemonsets', ReplicaSet: 'replicasets', Job: 'jobs', CronJob: 'cronjobs',
  Service: 'services', Ingress: 'ingresses', Gateway: 'gateways', HTTPRoute: 'httproutes',
  ConfigMap: 'configmaps', Secret: 'secrets', HPA: 'hpas', HorizontalPodAutoscaler: 'hpas',
  PersistentVolumeClaim: 'pvcs', PersistentVolume: 'pvs',
}

type LayoutMode = 'grid' | 'flow' | 'traffic'

// In Traffic mode we collapse the map down to what matters for flow
// reading: Pods (sources and destinations, so LB fan-out is visible),
// Services (junction nodes for intent-flow routing), and external
// entry points (Ingress / Gateway / HTTPRoute) so outside → cluster
// traffic is visible too. Workloads don't appear in flows — showing
// them just adds isolated nodes. Config / storage / autoscale / node
// kinds are never traffic endpoints.
const TRAFFIC_HIDDEN_KINDS = new Set<string>([
  'Deployment',
  'StatefulSet',
  'DaemonSet',
  'Job',
  'CronJob',
  'ReplicaSet',
  'ConfigMap',
  'Secret',
  'PersistentVolumeClaim',
  'PersistentVolume',
  'HPA',
  'HorizontalPodAutoscaler',
  'Node',
])

const NODE_W = 170
const NODE_H = 90
const GAP_X = 16
const GAP_Y = 14
const NS_PAD_X = 18
const NS_PAD_TOP = 40
const NS_PAD_BOTTOM = 14
const NS_GAP_X = 24
const NS_GAP_Y = 24
const GRID_COLS = 6
const NS_COLS = 3

// Flow layout constants
const FLOW_COL_W = 200
const FLOW_GAP_X = 30
const FLOW_GAP_Y = 12
const FLOW_NODE_H = 80

function filterNodes(
  topoNodes: TopologyNode[],
  hiddenKinds: Set<string>,
  visibleNamespaces: Set<string> | null,
) {
  let filtered = topoNodes.filter((n) => !hiddenKinds.has(n.type || n.kind))
  if (visibleNamespaces) {
    filtered = filtered.filter((n) => {
      const ns = n.namespace || '(cluster)'
      return visibleNamespaces.has(ns)
    })
  }
  return filtered
}

function groupByNamespace(nodes: TopologyNode[]) {
  const nsMap = new Map<string, TopologyNode[]>()
  for (const n of nodes) {
    const ns = n.namespace || '(cluster)'
    if (!nsMap.has(ns)) nsMap.set(ns, [])
    nsMap.get(ns)!.push(n)
  }
  return [...nsMap.keys()]
    .sort((a, b) => {
      if (a === '(cluster)') return 1
      if (b === '(cluster)') return -1
      return a.localeCompare(b)
    })
    .map((ns) => ({ ns, resources: nsMap.get(ns)! }))
}

// ─── Grid Layout ───
function buildGridLayout(filtered: TopologyNode[]) {
  const groups = groupByNamespace(filtered)
  const allNodes: Node[] = []

  interface NSBlock { ns: string; resources: TopologyNode[]; color: typeof NS_COLORS[number]; width: number; height: number }
  const blocks: NSBlock[] = groups.map(({ ns, resources }, i) => {
    const color = NS_COLORS[i % NS_COLORS.length]
    const cols = Math.min(resources.length, GRID_COLS)
    const rows = Math.ceil(resources.length / GRID_COLS)
    const width = Math.max(cols * (NODE_W + GAP_X) - GAP_X + NS_PAD_X * 2, 240)
    const height = rows * (NODE_H + GAP_Y) - GAP_Y + NS_PAD_TOP + NS_PAD_BOTTOM
    return { ns, resources, color, width, height }
  })

  const gridRows: NSBlock[][] = []
  for (let i = 0; i < blocks.length; i += NS_COLS) {
    gridRows.push(blocks.slice(i, i + NS_COLS))
  }

  let offsetY = 0
  gridRows.forEach((row) => {
    let offsetX = 0
    const rowMaxH = Math.max(...row.map((b) => b.height))
    row.forEach((block) => {
      const nsId = `ns__${block.ns}`
      allNodes.push({
        id: nsId, type: 'namespaceRegion',
        position: { x: offsetX, y: offsetY },
        data: { namespace: block.ns, nodeCount: block.resources.length, color: block.color, width: block.width, height: block.height },
        style: { width: block.width, height: block.height },
        selectable: false, draggable: true, dragHandle: ".ns-drag-handle",
      })
      block.resources.forEach((n, i) => {
        allNodes.push({
          id: n.id, type: 'resource', parentNode: nsId, extent: 'parent' as const,
          position: { x: NS_PAD_X + (i % GRID_COLS) * (NODE_W + GAP_X), y: NS_PAD_TOP + Math.floor(i / GRID_COLS) * (NODE_H + GAP_Y) },
          data: n,
        })
      })
      offsetX += block.width + NS_GAP_X
    })
    offsetY += rowMaxH + NS_GAP_Y
  })

  return allNodes
}

// ─── Flow Layout ───
function buildFlowLayout(filtered: TopologyNode[], _edges: TopologyEdge[]) {
  const groups = groupByNamespace(filtered)
  const allNodes: Node[] = []

  interface FlowBlock {
    ns: string
    resources: TopologyNode[]
    color: typeof NS_COLORS[number]
    width: number
    height: number
    activeColumns: number[]
    columns: Map<number, TopologyNode[]>
  }

  // Pre-compute dimensions for every namespace block
  const blocks: FlowBlock[] = groups.map(({ ns, resources }, nsIdx) => {
    const color = NS_COLORS[nsIdx % NS_COLORS.length]

    const columns = new Map<number, TopologyNode[]>()
    for (const n of resources) {
      const kind = n.type || n.kind
      const colIdx = FLOW_COLUMNS.findIndex((col) => col.includes(kind))
      const col = colIdx >= 0 ? colIdx : FLOW_COLUMNS.length
      if (!columns.has(col)) columns.set(col, [])
      columns.get(col)!.push(n)
    }

    const activeColumns = [...columns.keys()].sort((a, b) => a - b)
    const numCols = activeColumns.length
    const maxRows = Math.max(1, ...activeColumns.map((c) => columns.get(c)!.length))
    const width = Math.max(numCols * (FLOW_COL_W + FLOW_GAP_X) - FLOW_GAP_X + NS_PAD_X * 2, 300)
    const height = maxRows * (FLOW_NODE_H + FLOW_GAP_Y) - FLOW_GAP_Y + NS_PAD_TOP + NS_PAD_BOTTOM

    return { ns, resources, color, width, height, activeColumns, columns }
  })

  // Arrange namespace blocks in rows of NS_COLS (same as grid layout)
  const rows: FlowBlock[][] = []
  for (let i = 0; i < blocks.length; i += NS_COLS) {
    rows.push(blocks.slice(i, i + NS_COLS))
  }

  let offsetY = 0
  rows.forEach((row) => {
    let offsetX = 0
    const rowMaxH = Math.max(...row.map((b) => b.height))
    row.forEach((block) => {
      const nsId = `ns__${block.ns}`
      allNodes.push({
        id: nsId, type: 'namespaceRegion',
        position: { x: offsetX, y: offsetY },
        data: { namespace: block.ns, nodeCount: block.resources.length, color: block.color, width: block.width, height: block.height },
        style: { width: block.width, height: block.height },
        selectable: false, draggable: true, dragHandle: ".ns-drag-handle",
      })

      block.activeColumns.forEach((colNum, colVisualIdx) => {
        block.columns.get(colNum)!.forEach((n, rowIdx) => {
          allNodes.push({
            id: n.id, type: 'resource', parentNode: nsId, extent: 'parent' as const,
            position: {
              x: NS_PAD_X + colVisualIdx * (FLOW_COL_W + FLOW_GAP_X),
              y: NS_PAD_TOP + rowIdx * (FLOW_NODE_H + FLOW_GAP_Y),
            },
            data: n,
          })
        })
      })

      offsetX += block.width + NS_GAP_X
    })
    offsetY += rowMaxH + NS_GAP_Y
  })

  return allNodes
}

// ─── Traffic Layout ───
// Kiali-style: dagre left-to-right per namespace. Intent-flow shape
// (caller → Service → callee) is achieved by feeding dagre both the
// topology 'selects' edges (Service → Pod) and the observed pod-to-pod
// flows, so Services naturally sit between callers and their targets
// in the resulting rank graph.
function buildTrafficLayout(
  filtered: TopologyNode[],
  topologyEdges: TopologyEdge[],
  flowEdges: { srcNamespace: string; srcPod: string; dstNamespace: string; dstPod: string }[],
) {
  const groups = groupByNamespace(filtered)
  const allNodes: Node[] = []

  interface TrafficBlock {
    ns: string
    resources: TopologyNode[]
    color: typeof NS_COLORS[number]
    width: number
    height: number
    positions: Map<string, { x: number; y: number }>
  }

  const resourceIds = new Set(filtered.map((n) => n.id))
  const servicesSelectingPod = new Map<string, string[]>()
  for (const e of topologyEdges) {
    if (e.type !== 'selects') continue
    if (!resourceIds.has(e.source) || !resourceIds.has(e.target)) continue
    const list = servicesSelectingPod.get(e.target) || []
    list.push(e.source)
    servicesSelectingPod.set(e.target, list)
  }

  const blocks: TrafficBlock[] = groups.map(({ ns, resources }, nsIdx) => {
    const color = NS_COLORS[nsIdx % NS_COLORS.length]

    const g = new dagre.graphlib.Graph()
    g.setGraph({ rankdir: 'LR', nodesep: 18, ranksep: 70, marginx: 0, marginy: 0 })
    g.setDefaultEdgeLabel(() => ({}))

    const nsIds = new Set(resources.map((n) => n.id))
    for (const n of resources) {
      g.setNode(n.id, { width: NODE_W, height: NODE_H })
    }

    // Structural hints: ownership + service selectors. dagre uses these
    // to shape ranks even when there's no live traffic yet, so an idle
    // map still reads left-to-right.
    for (const e of topologyEdges) {
      if (!nsIds.has(e.source) || !nsIds.has(e.target)) continue
      if (e.type === 'selects' || e.type === 'routes' || e.type === 'owns') {
        g.setEdge(e.source, e.target)
      }
    }

    // Intent-shaped flow hints: route pod → Service → pod when we know
    // which Service selects the destination. Keeps Services in the
    // middle rank even when they receive no direct ingress.
    for (const f of flowEdges) {
      if (f.srcNamespace !== ns || f.dstNamespace !== ns) continue
      const srcId = `Pod/${f.srcNamespace}/${f.srcPod}`
      const dstId = `Pod/${f.dstNamespace}/${f.dstPod}`
      if (!nsIds.has(srcId) || !nsIds.has(dstId)) continue
      const svcs = servicesSelectingPod.get(dstId)
      if (svcs && svcs.length > 0 && nsIds.has(svcs[0])) {
        g.setEdge(srcId, svcs[0])
        g.setEdge(svcs[0], dstId)
      } else {
        g.setEdge(srcId, dstId)
      }
    }

    dagre.layout(g)

    // Dagre can emit negative coordinates depending on how ranks settle,
    // and nodes with very few connections sometimes end up at odd
    // positions. We normalize by shifting so the leftmost/topmost node
    // sits at (0, 0), then size the region to exactly fit the shifted
    // bounding box. Without this, the region width was shorter than
    // some nodes' actual positions and `extent: 'parent'` would clamp
    // them to the edge — edges then rendered to the *unclamped*
    // position, landing in empty space.
    const positions = new Map<string, { x: number; y: number }>()
    let minX = Infinity
    let minY = Infinity
    const raw = new Map<string, { x: number; y: number }>()
    for (const n of resources) {
      const pos = g.node(n.id)
      if (!pos) continue
      const x = pos.x - NODE_W / 2
      const y = pos.y - NODE_H / 2
      raw.set(n.id, { x, y })
      if (x < minX) minX = x
      if (y < minY) minY = y
    }
    if (minX === Infinity) minX = 0
    if (minY === Infinity) minY = 0

    let maxRight = 0
    let maxBottom = 0
    for (const [id, { x, y }] of raw) {
      const sx = x - minX
      const sy = y - minY
      positions.set(id, { x: sx, y: sy })
      maxRight = Math.max(maxRight, sx + NODE_W)
      maxBottom = Math.max(maxBottom, sy + NODE_H)
    }

    const width = Math.max(maxRight + NS_PAD_X * 2, 320)
    const height = Math.max(maxBottom + NS_PAD_TOP + NS_PAD_BOTTOM, 140)

    return { ns, resources, color, width, height, positions }
  })

  const rows: TrafficBlock[][] = []
  for (let i = 0; i < blocks.length; i += NS_COLS) {
    rows.push(blocks.slice(i, i + NS_COLS))
  }

  let offsetY = 0
  rows.forEach((row) => {
    let offsetX = 0
    const rowMaxH = Math.max(...row.map((b) => b.height))
    row.forEach((block) => {
      const nsId = `ns__${block.ns}`
      allNodes.push({
        id: nsId, type: 'namespaceRegion',
        position: { x: offsetX, y: offsetY },
        data: { namespace: block.ns, nodeCount: block.resources.length, color: block.color, width: block.width, height: block.height },
        style: { width: block.width, height: block.height },
        selectable: false, draggable: true, dragHandle: ".ns-drag-handle",
      })
      block.resources.forEach((n) => {
        const pos = block.positions.get(n.id)
        if (!pos) return
        allNodes.push({
          id: n.id, type: 'resource', parentNode: nsId, extent: 'parent' as const,
          position: { x: NS_PAD_X + pos.x, y: NS_PAD_TOP + pos.y },
          data: n,
        })
      })
      offsetX += block.width + NS_GAP_X
    })
    offsetY += rowMaxH + NS_GAP_Y
  })

  return allNodes
}

// LegendItem renders a single swatch+label row used by the bottom legend.
// Kind controls the visual: dot (status), solid/dashed line (edge), or
// dotted-line (edge with moving particles).
function LegendItem({
  kind,
  color,
  label,
  description,
}: {
  kind: 'dot' | 'solid-line' | 'dashed' | 'dotted-line'
  color: string
  label: string
  description?: string
}) {
  const tooltip = description ? `${label} — ${description}` : label
  return (
    <div className="flex items-center gap-1.5" title={tooltip}>
      {kind === 'dot' && (
        <div className="w-2 h-2 rounded-full" style={{ background: color }} />
      )}
      {kind === 'solid-line' && (
        <div className="w-5 h-0.5 rounded" style={{ background: color }} />
      )}
      {kind === 'dashed' && (
        <div className="w-5 h-0.5 border-t border-dashed" style={{ borderColor: color }} />
      )}
      {kind === 'dotted-line' && (
        <div className="flex items-center gap-0.5">
          <div className="w-4 h-0.5 rounded" style={{ background: color }} />
          <div className="w-1.5 h-1.5 rounded-full" style={{ background: color }} />
        </div>
      )}
      <span className="text-[10px] font-mono text-kb-text-secondary">{label}</span>
    </div>
  )
}

// Read/write user preferences that should persist across reloads.
// Preferences live in localStorage and are keyed by feature name.
const PREF_ANIMATIONS = 'kb-map-animations'
const PREF_LAYOUT = 'kb-map-layout'
const PREF_HIDDEN_EDGE_GROUPS = 'kb-map-hidden-edge-groups'

// Edge categories group the many underlying edge types into user-visible
// buckets. Each bucket has one toggle so the map isn't death by a
// thousand filter checkboxes. If a new type lands in ConnectionEdge, it
// must be added to one of these groups (or it defaults to "other").
type EdgeGroupKey = 'ownership' | 'service' | 'config' | 'storage' | 'autoscale' | 'traffic'

const EDGE_GROUPS: Array<{
  key: EdgeGroupKey
  label: string
  description: string
  types: string[]
}> = [
  { key: 'ownership', label: 'Ownership', description: 'Deployment → ReplicaSet → Pod, StatefulSet → Pod, Job → Pod', types: ['owns'] },
  { key: 'service',   label: 'Service',   description: 'Service → Pod selectors, Ingress / Gateway → Service routes', types: ['selects', 'routes'] },
  { key: 'config',    label: 'Config',    description: 'ConfigMap / Secret mounts, envFrom, image pulls',             types: ['mounts', 'envFrom', 'imagePull'] },
  { key: 'storage',   label: 'Storage',   description: 'Volume usage, PVC ↔ PV bindings',                             types: ['uses', 'bound'] },
  { key: 'autoscale', label: 'Autoscale', description: 'HPA → workload target',                                       types: ['hpa'] },
  { key: 'traffic',   label: 'Traffic',   description: 'Live observed pod-to-pod flows (Hubble)',                     types: ['traffic'] },
]

const EDGE_TYPE_TO_GROUP: Record<string, EdgeGroupKey> = (() => {
  const out: Record<string, EdgeGroupKey> = {}
  for (const g of EDGE_GROUPS) {
    for (const t of g.types) out[t] = g.key
  }
  return out
})()

function loadPref(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) ?? fallback
  } catch {
    return fallback
  }
}

function savePref(key: string, value: string) {
  try { localStorage.setItem(key, value) } catch { /* localStorage blocked */ }
}

function ClusterMapInner() {
  const { data: topology, isLoading, error, refetch } = useTopology()
  const [selectedNode, setSelectedNode] = useState<TopologyNode | null>(null)
  const [hiddenKinds, setHiddenKinds] = useState<Set<string>>(new Set())
  const [visibleNamespaces, setVisibleNamespaces] = useState<Set<string> | null>(null)
  const [nsFilterOpen, setNsFilterOpen] = useState(false)
  const [layoutMode, setLayoutMode] = useState<LayoutMode>(() => (loadPref(PREF_LAYOUT, 'flow') as LayoutMode))
  const [animationsEnabled, setAnimationsEnabled] = useState(() => loadPref(PREF_ANIMATIONS, 'on') !== 'off')
  const [hiddenEdgeGroups, setHiddenEdgeGroups] = useState<Set<EdgeGroupKey>>(() => {
    const raw = loadPref(PREF_HIDDEN_EDGE_GROUPS, '')
    if (!raw) return new Set()
    return new Set(raw.split(',').filter(Boolean) as EdgeGroupKey[])
  })
  const trafficEnabled = !hiddenEdgeGroups.has('traffic')
  const { data: flowData } = useFlowEdges({ enabled: trafficEnabled, windowMinutes: 1 })
  // Manual position overrides set by user drag. Keyed by node ID.
  // Cleared when switching layout mode or clicking Reset.
  const [dragOverrides, setDragOverrides] = useState<Map<string, { x: number; y: number }>>(new Map())
  // Tooltip state for traffic edges. ReactFlow's own interaction layer
  // swallows pointer events before they reach our custom edge's SVG, so
  // an SVG <title> doesn't fire — we use ReactFlow's onEdgeMouseEnter /
  // Leave callbacks and render a positioned div overlay instead.
  // We store only the edge id (not the tooltip text) so polling
  // refreshes update the visible tooltip while the mouse stays put.
  const [hoveredEdge, setHoveredEdge] = useState<{ id: string; x: number; y: number } | null>(null)
  const { fitView } = useReactFlow()
  const navigate = useNavigate()

  // Persist preferences on change
  useEffect(() => { savePref(PREF_LAYOUT, layoutMode) }, [layoutMode])
  useEffect(() => { savePref(PREF_ANIMATIONS, animationsEnabled ? 'on' : 'off') }, [animationsEnabled])
  useEffect(() => { savePref(PREF_HIDDEN_EDGE_GROUPS, Array.from(hiddenEdgeGroups).join(',')) }, [hiddenEdgeGroups])

  const toggleEdgeGroup = useCallback((key: EdgeGroupKey) => {
    setHiddenEdgeGroups(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [])

  // Reset manual positions whenever the layout mode OR filters change.
  // Stale overrides from a prior filter state would apply their cached
  // coordinates to nodes that now belong to a completely different
  // region, which was rendering ghost edges hanging in empty space.
  useEffect(() => {
    setDragOverrides(new Map())
  }, [layoutMode, visibleNamespaces, hiddenKinds, hiddenEdgeGroups])

  const allNamespaces = useMemo(() => {
    if (!topology?.nodes) return []
    const ns = new Set(topology.nodes.map((n) => n.namespace || '(cluster)'))
    return [...ns].sort((a, b) => {
      if (a === '(cluster)') return 1
      if (b === '(cluster)') return -1
      return a.localeCompare(b)
    })
  }, [topology?.nodes])

  const availableKinds = useMemo(() => {
    if (!topology?.nodes) return []
    const kinds = new Set(topology.nodes.map((n) => n.type || n.kind))
    const ordered = KIND_ORDER.filter((k) => kinds.has(k))
    const unknown = [...kinds].filter((k) => !KIND_ORDER.includes(k)).sort()
    return [...ordered, ...unknown]
  }, [topology?.nodes])

  const toggleKind = useCallback((kind: string) => {
    setHiddenKinds((prev) => {
      const next = new Set(prev)
      if (next.has(kind)) next.delete(kind)
      else next.add(kind)
      return next
    })
  }, [])

  const toggleNamespace = useCallback((ns: string) => {
    setVisibleNamespaces((prev) => {
      if (prev === null) return new Set([ns])
      const next = new Set(prev)
      if (next.has(ns)) { next.delete(ns); if (next.size === 0) return null }
      else next.add(ns)
      return next
    })
  }, [])

  const showAllNamespaces = useCallback(() => setVisibleNamespaces(null), [])

  // Compute the base layout from topology + filters. This doesn't include
  // user drag overrides — those are applied downstream via useNodesState.
  const computedNodes = useMemo(() => {
    if (!topology?.nodes) return []
    if (layoutMode === 'traffic') {
      // Kiali-style: only render what appears in a flow. Pods that are
      // source/destination of an observed flow, plus Services that
      // select any destination Pod. Everything else is collapsed out,
      // which keeps the map readable and matches the user's mental
      // model of "show me what's actually talking".
      const effectiveHidden = new Set<string>([...hiddenKinds, ...TRAFFIC_HIDDEN_KINDS])
      const kindFiltered = filterNodes(topology.nodes, effectiveHidden, visibleNamespaces)
      const flows = flowData?.edges || []
      const flowPodIds = new Set<string>()
      for (const f of flows) {
        flowPodIds.add(`Pod/${f.srcNamespace}/${f.srcPod}`)
        flowPodIds.add(`Pod/${f.dstNamespace}/${f.dstPod}`)
      }
      const serviceIds = new Set<string>()
      // External entry points (Ingress / Gateway / HTTPRoute) whose
      // `routes` edge targets a Service that sees observed traffic.
      // Keeping them visible in Traffic mode completes the picture
      // "outside → Ingress → Service → Pods" without drowning the
      // map in every Ingress in the cluster.
      const externalEntryIds = new Set<string>()
      for (const e of topology.edges || []) {
        if (e.type === 'selects' && flowPodIds.has(e.target)) {
          serviceIds.add(e.source)
        }
      }
      for (const e of topology.edges || []) {
        if (e.type === 'routes' && serviceIds.has(e.target)) {
          externalEntryIds.add(e.source)
        }
      }
      const trafficVisible = kindFiltered.filter(
        (n) => flowPodIds.has(n.id) || serviceIds.has(n.id) || externalEntryIds.has(n.id)
      )
      return buildTrafficLayout(trafficVisible, topology.edges || [], flows)
    }
    const filtered = filterNodes(topology.nodes, hiddenKinds, visibleNamespaces)
    if (layoutMode === 'flow') {
      return buildFlowLayout(filtered, topology.edges || [])
    }
    return buildGridLayout(filtered)
  }, [topology?.nodes, topology?.edges, hiddenKinds, visibleNamespaces, layoutMode, flowData?.edges])

  // Apply drag overrides + animation flag on top of the computed layout.
  // This is what React Flow actually renders.
  const initialNodes = useMemo(() => {
    return computedNodes.map((n) => {
      const override = dragOverrides.get(n.id)
      const next: Node = override
        ? { ...n, position: override }
        : n
      // Inject animationsEnabled into the data object so ResourceNode can
      // pulse accordingly. Namespace regions don't need it.
      if (next.type === 'resource') {
        return { ...next, data: { ...next.data, animationsEnabled } }
      }
      return next
    })
  }, [computedNodes, dragOverrides, animationsEnabled])

  // useNodesState lets React Flow manage node positions interactively
  // while we still drive the initial layout. We sync whenever the layout
  // is recomputed (topology refetch, filter change, layout switch).
  // useLayoutEffect (not useEffect) so flowNodes is in sync with
  // initialNodes *before* React paints — otherwise ReactFlow would
  // paint one frame with stale nodes but fresh edges, leaving traffic
  // lines hanging in space pointing to just-removed nodes.
  const [flowNodes, setFlowNodes, onNodesChange] = useNodesState(initialNodes)
  useLayoutEffect(() => { setFlowNodes(initialNodes) }, [initialNodes, setFlowNodes])

  // Persist drag deltas when the user lets go of a node. Applies to
  // resource nodes *and* namespace regions — the user can reorganize
  // the whole ns layout by grabbing a region's header label, which is
  // the only element with pointer-events: auto (see NamespaceRegion).
  const onNodeDragStop = useCallback(
    (_: React.MouseEvent, node: Node) => {
      setDragOverrides((prev) => {
        const next = new Map(prev)
        next.set(node.id, { x: node.position.x, y: node.position.y })
        return next
      })
    },
    []
  )

  const resetLayout = useCallback(() => {
    setDragOverrides(new Map())
    setTimeout(() => fitView({ padding: 0.1 }), 100)
  }, [fitView])

  const edges: Edge[] = useMemo(() => {
    if (!topology?.edges) return []
    const visibleIds = new Set(computedNodes.map((n) => n.id))
    const nodeStatusMap = new Map<string, string>()
    if (topology?.nodes) {
      for (const n of topology.nodes) {
        nodeStatusMap.set(n.id, n.status || '')
      }
    }
    // In Traffic mode the map is about the flow itself. 'selects' and
    // 'owns' duplicate or clutter the intent edges and are hidden.
    // 'routes' (Ingress/Gateway → Service) stays, so external entry
    // points connect visually to the rest of the flow graph.
    const structural: Edge[] = topology.edges
      .filter((e) => visibleIds.has(e.source) && visibleIds.has(e.target))
      .filter((e) => {
        if (layoutMode === 'traffic' && (e.type === 'selects' || e.type === 'owns')) {
          return false
        }
        const group = EDGE_TYPE_TO_GROUP[e.type]
        // Unknown edge types are shown by default — less surprising when a
        // new type ships than silently hiding it. Add it to EDGE_GROUPS to
        // make it filterable.
        return !group || !hiddenEdgeGroups.has(group)
      })
      .map((e) => ({
        id: e.id, source: e.source, target: e.target,
        type: 'connection',
        data: {
          edgeType: e.type,
          animated: e.animated,
          sourceStatus: nodeStatusMap.get(e.source) || '',
          targetStatus: nodeStatusMap.get(e.target) || '',
          animationsEnabled,
        },
        animated: e.animated && animationsEnabled,
      }))

    // Traffic edges: only included when the toggle is on and the backend
    // returned data. We skip pairs whose endpoints aren't on the map
    // (filtered out by kind/namespace).
    if (!trafficEnabled || !flowData?.edges?.length) {
      return structural
    }

    const trafficEdges: Edge[] = []

    if (layoutMode === 'traffic') {
      // Intent edges: route pod → Service → pod whenever a Service
      // selects the destination pod. Aggregate first-hop rate across
      // all destination pods behind the same Service so the caller
      // side shows one fat line per (caller, Service, verdict); the
      // per-pod LB distribution appears on the second hop.
      const serviceForPod = new Map<string, string>()
      for (const e of topology.edges) {
        if (e.type !== 'selects') continue
        if (!serviceForPod.has(e.target)) serviceForPod.set(e.target, e.source)
      }

      // Aggregate BOTH hops of the intent flow so edge IDs stay unique.
      // First hop (src_pod → Service) aggregates across all destination
      // pods behind the same Service; the second hop (Service → dst_pod)
      // aggregates across all source pods calling into the same pod.
      // Without this, two demo-load pods calling the same demo-web pod
      // emitted two edges with the same id, which React logs as a
      // duplicate-key warning and renders with undefined behavior
      // (stale SVG particles hanging in empty space).
      //
      // L7 data is carried through both hops so the Service → Pod edge
      // colors by the real HTTP health of that specific pod, while the
      // Pod → Service edge shows the caller's aggregate health toward
      // the Service.
      type Hop = { src: string; dst: string; verdict: string; rate: number; l7?: L7Aggregator }
      const firstHop = new Map<string, Hop>()
      const secondHop = new Map<string, Hop>()
      const directEdges = new Map<string, Hop>()

      for (const f of flowData.edges) {
        const srcId = `Pod/${f.srcNamespace}/${f.srcPod}`
        const dstId = `Pod/${f.dstNamespace}/${f.dstPod}`
        if (!visibleIds.has(srcId) || !visibleIds.has(dstId)) continue

        const svcId = serviceForPod.get(dstId)
        if (svcId && visibleIds.has(svcId)) {
          const firstKey = `${srcId}||${svcId}||${f.verdict}`
          const first = firstHop.get(firstKey) ?? {
            src: srcId, dst: svcId, verdict: f.verdict, rate: 0,
          }
          first.rate += f.ratePerSec
          mergeL7(first, f.l7)
          firstHop.set(firstKey, first)

          const secondKey = `${svcId}||${dstId}||${f.verdict}`
          const second = secondHop.get(secondKey) ?? {
            src: svcId, dst: dstId, verdict: f.verdict, rate: 0,
          }
          second.rate += f.ratePerSec
          mergeL7(second, f.l7)
          secondHop.set(secondKey, second)
        } else {
          // No Service selects this pod — draw pod-to-pod directly
          // (host-network callers, standalone pods). Still aggregate
          // in case multiple flows match the same (src, dst, verdict).
          const key = `${srcId}||${dstId}||${f.verdict}`
          const direct = directEdges.get(key) ?? {
            src: srcId, dst: dstId, verdict: f.verdict, rate: 0,
          }
          direct.rate += f.ratePerSec
          mergeL7(direct, f.l7)
          directEdges.set(key, direct)
        }
      }

      const pushHop = (hop: Hop, idPrefix: string) => {
        const l7 = hop.l7 ? finalizeL7(hop.l7) : undefined
        trafficEdges.push({
          id: `${idPrefix}/${hop.src}->${hop.dst}/${hop.verdict}`,
          source: hop.src, target: hop.dst, type: 'connection',
          data: {
            edgeType: 'traffic', ratePerSec: hop.rate, verdict: hop.verdict,
            l7,
            tooltip: buildTrafficTooltip(hop.rate, hop.verdict, l7),
            sourceStatus: nodeStatusMap.get(hop.src) || '',
            targetStatus: nodeStatusMap.get(hop.dst) || '',
            animationsEnabled,
          },
          animated: animationsEnabled,
        })
      }
      for (const hop of firstHop.values()) pushHop(hop, 'intent')
      for (const hop of secondHop.values()) pushHop(hop, 'intent')
      for (const hop of directEdges.values()) pushHop(hop, 'flow')
    } else {
      // Grid / Flow modes: keep the original pod-to-pod shape. Each
      // edge sources from pod_flow_events_total so the id is unique
      // across (src, dst, verdict).
      for (const f of flowData.edges) {
        const sourceId = `Pod/${f.srcNamespace}/${f.srcPod}`
        const targetId = `Pod/${f.dstNamespace}/${f.dstPod}`
        if (!visibleIds.has(sourceId) || !visibleIds.has(targetId)) continue
        trafficEdges.push({
          id: `flow/${f.srcNamespace}/${f.srcPod}->${f.dstNamespace}/${f.dstPod}/${f.verdict}`,
          source: sourceId,
          target: targetId,
          type: 'connection',
          data: {
            edgeType: 'traffic',
            ratePerSec: f.ratePerSec,
            verdict: f.verdict,
            l7: f.l7,
            tooltip: buildTrafficTooltip(f.ratePerSec, f.verdict, f.l7),
            sourceStatus: nodeStatusMap.get(sourceId) || '',
            targetStatus: nodeStatusMap.get(targetId) || '',
            animationsEnabled,
          },
          animated: animationsEnabled,
        })
      }
    }
    // Visual intensity combines two signals:
    //   - relative rank (edge rate / peak rate on the map) so edges
    //     compare correctly to each other regardless of absolute scale.
    //   - absolute headroom (log of the peak) so a cluster at 5 rps
    //     doesn't look as loud as a cluster at 5000 rps just because
    //     5 rps happens to be the peak of that moment.
    // headroom hits 1.0 around ~300 rps peak traffic. Below that the
    // full visual range is compressed — the busiest edge in a quiet
    // cluster stays visibly calmer than the busiest edge in a hot one.
    let peakRate = 0
    for (const e of trafficEdges) {
      const d = e.data as { ratePerSec?: number; l7?: { requestsPerSec?: number } } | undefined
      const r = d?.l7?.requestsPerSec ?? d?.ratePerSec ?? 0
      if (r > peakRate) peakRate = r
    }
    if (peakRate > 0) {
      const headroom = Math.min(1, Math.log10(peakRate + 1) / 2.5)
      for (const e of trafficEdges) {
        const d = e.data as { ratePerSec?: number; l7?: { requestsPerSec?: number } } | undefined
        const r = d?.l7?.requestsPerSec ?? d?.ratePerSec ?? 0
        ;(e.data as { relativeRate?: number }).relativeRate = (r / peakRate) * headroom
      }
    }

    return [...structural, ...trafficEdges]
  }, [topology?.edges, topology?.nodes, computedNodes, animationsEnabled, trafficEnabled, hiddenEdgeGroups, flowData, layoutMode])

  // Final safety net: only render edges whose endpoints are in the
  // nodes array ReactFlow is about to paint. Without this, any mismatch
  // between computedNodes and flowNodes (rare but possible during
  // drag/filter races) leaves ghost edges hanging in empty space.
  const renderedEdges = useMemo(() => {
    if (edges.length === 0) return edges
    const liveIds = new Set(flowNodes.map((n) => n.id))
    return edges.filter((e) => liveIds.has(e.source) && liveIds.has(e.target))
  }, [edges, flowNodes])

  // Remount ReactFlow on any filter change so its internal edge/node
  // store starts fresh. Prevents ghost SVG paths from nodes that were
  // in the store under a previous filter state.
  const reactFlowKey = useMemo(
    () =>
      [
        layoutMode,
        visibleNamespaces === null ? '*' : [...visibleNamespaces].sort().join(','),
        [...hiddenKinds].sort().join(','),
        [...hiddenEdgeGroups].sort().join(','),
      ].join('|'),
    [layoutMode, visibleNamespaces, hiddenKinds, hiddenEdgeGroups]
  )

  // Refit the view when filters or layout change, but not on every drag.
  // We key off the computed layout (size + layout mode), not the live flowNodes
  // state which mutates on drag.
  useEffect(() => {
    if (computedNodes.length > 0) {
      const t = setTimeout(() => fitView({ padding: 0.1 }), 100)
      return () => clearTimeout(t)
    }
  }, [computedNodes.length, hiddenKinds, visibleNamespaces, layoutMode, fitView])

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.type === 'namespaceRegion') return
      const topoNode = topology?.nodes.find((n) => n.id === node.id)
      if (topoNode) setSelectedNode(topoNode)
    },
    [topology?.nodes]
  )

  const onNodeDoubleClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.type === 'namespaceRegion') return
      const topoNode = topology?.nodes.find((n) => n.id === node.id)
      if (!topoNode) return
      const route = KIND_TO_ROUTE[topoNode.kind]
      if (!route) return
      const ns = topoNode.namespace || '_'
      navigate(`/${route}/${ns}/${topoNode.name}`)
    },
    [topology?.nodes, navigate]
  )

  // Edge hover tooltip: ReactFlow dispatches these events from its own
  // interaction layer (which is why SVG <title> on our custom edge
  // doesn't fire — that layer is above our SVG). Position is frozen at
  // the enter coordinate on purpose: updating on every mousemove causes
  // React to re-render the map each frame, which was flickering the
  // tooltip in and out.
  const onEdgeMouseEnter = useCallback((e: React.MouseEvent, edge: Edge) => {
    const tooltip = (edge.data as { tooltip?: string } | undefined)?.tooltip
    if (!tooltip) return
    setHoveredEdge({ id: edge.id, x: e.clientX, y: e.clientY })
  }, [])
  const onEdgeMouseLeave = useCallback(() => setHoveredEdge(null), [])

  // Resolve the tooltip payload freshly from the current edges on every
  // render. When useFlowEdges polls a new window, the edges memo above
  // rebuilds with updated numbers; looking up by id here means the
  // open tooltip reflects those fresh values without needing to
  // re-hover.
  const hoveredTooltip = useMemo<TrafficTooltipData | null>(() => {
    if (!hoveredEdge) return null
    const edge = renderedEdges.find((e) => e.id === hoveredEdge.id)
    return (edge?.data as { tooltip?: TrafficTooltipData } | undefined)?.tooltip ?? null
  }, [hoveredEdge, renderedEdges])

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const nsCount = visibleNamespaces === null ? allNamespaces.length : visibleNamespaces.size

  return (
    <div className="h-[calc(100vh-52px)] relative">
      <ReactFlow
        key={reactFlowKey}
        nodes={flowNodes}
        edges={renderedEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodesChange={onNodesChange}
        onNodeDragStop={onNodeDragStop}
        onNodeClick={onNodeClick}
        onNodeDoubleClick={onNodeDoubleClick}
        onEdgeMouseEnter={onEdgeMouseEnter}
        onEdgeMouseLeave={onEdgeMouseLeave}
        onPaneClick={() => setSelectedNode(null)}
        fitView
        fitViewOptions={{ padding: 0.1 }}
        proOptions={{ hideAttribution: true }}
        className="bg-kb-bg"
        minZoom={0.03}
        maxZoom={2}
        nodesDraggable
      >
        <Background color="rgba(255,255,255,0.018)" gap={36} />
        <MiniMap
          nodeColor={(node) => {
            if (node.type === 'namespaceRegion') return 'rgba(255,255,255,0.02)'
            const status = (node.data as TopologyNode)?.status || ''
            const s = status.toLowerCase()
            if (['running', 'ready', 'active', 'bound', 'succeeded', 'programmed', 'accepted'].includes(s)) return '#22d68a'
            if (['pending', 'warning'].includes(s)) return '#f5a623'
            if (['failed', 'error', 'crashloopbackoff'].includes(s)) return '#ef4056'
            return '#555770'
          }}
          maskColor="rgba(10,11,15,0.85)"
          className="!bg-kb-surface/90 !border-kb-border !rounded-lg"
          pannable
          zoomable
        />
        <MapControls />
      </ReactFlow>

      {/* Filter Panel */}
      <div className="absolute top-4 left-4 bg-kb-card/95 backdrop-blur-sm border border-kb-border rounded-lg p-3 z-10 w-[250px] space-y-3">
        {/* Layout Toggle */}
        <div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1.5">Layout</div>
          <div className="flex rounded-md border border-kb-border overflow-hidden">
            <button
              onClick={() => setLayoutMode('grid')}
              title="Grid layout — resources arranged in a compact grid"
              className={`flex-1 flex items-center justify-center gap-1.5 px-2 py-1 text-[10px] font-mono transition-colors ${
                layoutMode === 'grid' ? 'bg-status-info-dim text-status-info' : 'bg-kb-elevated/30 text-kb-text-tertiary hover:text-kb-text-secondary'
              }`}
            >
              <LayoutGrid className="w-3 h-3" />
              Grid
            </button>
            <button
              onClick={() => setLayoutMode('flow')}
              title="Flow layout — resources arranged by dependency chain"
              className={`flex-1 flex items-center justify-center gap-1.5 px-2 py-1 text-[10px] font-mono transition-colors border-l border-kb-border ${
                layoutMode === 'flow' ? 'bg-status-info-dim text-status-info' : 'bg-kb-elevated/30 text-kb-text-tertiary hover:text-kb-text-secondary'
              }`}
            >
              <GitBranch className="w-3 h-3" />
              Flow
            </button>
            <button
              onClick={() => setLayoutMode('traffic')}
              title="Traffic layout — Kiali-style intent flow: caller → Service → Pod, routed by observed traffic"
              className={`flex-1 flex items-center justify-center gap-1.5 px-2 py-1 text-[10px] font-mono transition-colors border-l border-kb-border ${
                layoutMode === 'traffic' ? 'bg-status-info-dim text-status-info' : 'bg-kb-elevated/30 text-kb-text-tertiary hover:text-kb-text-secondary'
              }`}
            >
              <Waypoints className="w-3 h-3" />
              Traffic
            </button>
          </div>
        </div>

        {/* Edge category filters */}
        <div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1.5">Edges</div>
          <div className="flex flex-wrap gap-1">
            {EDGE_GROUPS.map((g) => {
              const visible = !hiddenEdgeGroups.has(g.key)
              const isTraffic = g.key === 'traffic'
              const count = isTraffic ? (flowData?.edges?.length ?? 0) : undefined
              return (
                <button
                  key={g.key}
                  onClick={() => toggleEdgeGroup(g.key)}
                  title={g.description}
                  className={`px-2 py-0.5 text-[10px] font-mono rounded border transition-all ${
                    visible
                      ? isTraffic
                        ? 'bg-status-ok-dim border-status-ok/40 text-status-ok'
                        : 'bg-kb-elevated/60 border-kb-border text-kb-text-primary hover:border-kb-border-active'
                      : 'border-kb-border/60 text-kb-text-tertiary opacity-50 hover:opacity-80'
                  }`}
                >
                  {g.label}
                  {isTraffic && count !== undefined && count > 0 && ` (${count})`}
                </button>
              )
            })}
          </div>
        </div>

        {/* View Controls */}
        <div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1.5">View</div>
          <div className="flex rounded-md border border-kb-border overflow-hidden">
            <button
              onClick={() => setAnimationsEnabled((v) => !v)}
              title={animationsEnabled ? 'Disable animations (better performance)' : 'Enable animations'}
              className={`flex-1 flex items-center justify-center gap-1.5 px-2 py-1 text-[10px] font-mono transition-colors ${
                animationsEnabled ? 'bg-status-info-dim text-status-info' : 'bg-kb-elevated/30 text-kb-text-tertiary hover:text-kb-text-secondary'
              }`}
            >
              {animationsEnabled ? <Zap className="w-3 h-3" /> : <ZapOff className="w-3 h-3" />}
              {animationsEnabled ? 'Animated' : 'Static'}
            </button>
            <button
              onClick={resetLayout}
              disabled={dragOverrides.size === 0}
              title={dragOverrides.size === 0 ? 'No manual positions to reset' : `Reset ${dragOverrides.size} moved node(s)`}
              className="flex items-center justify-center gap-1.5 px-2 py-1 text-[10px] font-mono transition-colors border-l border-kb-border bg-kb-elevated/30 text-kb-text-tertiary hover:text-kb-text-secondary disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <RotateCcw className="w-3 h-3" />
              Reset
            </button>
          </div>
        </div>

        {/* Resource Types */}
        <div>
          <div className="text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1.5">
            Resource Types
          </div>
          <div className="flex flex-wrap gap-1">
            {availableKinds.map((kind) => {
              const isVisible = !hiddenKinds.has(kind)
              return (
                <button
                  key={kind}
                  onClick={() => toggleKind(kind)}
                  title={KIND_FULL[kind] || kind}
                  className={`px-1.5 py-0.5 rounded text-[9px] font-mono transition-all ${
                    isVisible
                      ? 'bg-status-info-dim text-status-info border border-status-info/20'
                      : 'bg-kb-elevated/50 text-kb-text-tertiary border border-transparent'
                  }`}
                >
                  {KIND_SHORT[kind] || kind}
                </button>
              )
            })}
          </div>
        </div>

        {/* Namespace Filter */}
        <div>
          <button
            onClick={() => setNsFilterOpen(!nsFilterOpen)}
            className="w-full flex items-center justify-between text-[9px] font-mono text-kb-text-tertiary uppercase tracking-[0.08em] mb-1.5 hover:text-kb-text-secondary transition-colors"
          >
            <span>Namespaces ({nsCount}/{allNamespaces.length})</span>
            <span className="text-[10px]">{nsFilterOpen ? '▲' : '▼'}</span>
          </button>
          {nsFilterOpen && (
            <div className="space-y-0.5 max-h-[200px] overflow-y-auto">
              <button
                onClick={showAllNamespaces}
                className={`w-full text-left px-2 py-1 rounded text-[10px] font-mono transition-colors ${
                  visibleNamespaces === null
                    ? 'bg-status-info-dim text-status-info'
                    : 'text-kb-text-secondary hover:bg-kb-elevated/50'
                }`}
              >
                All namespaces
              </button>
              {allNamespaces.map((ns) => {
                const isActive = visibleNamespaces === null || visibleNamespaces.has(ns)
                return (
                  <button
                    key={ns}
                    onClick={() => toggleNamespace(ns)}
                    className={`w-full text-left px-2 py-1 rounded text-[10px] font-mono transition-colors truncate ${
                      isActive
                        ? 'bg-status-ok-dim/50 text-status-ok'
                        : 'text-kb-text-tertiary hover:bg-kb-elevated/50 hover:text-kb-text-secondary'
                    }`}
                  >
                    {ns}
                  </button>
                )
              })}
            </div>
          )}
        </div>
      </div>

      {/* Flow column headers (only in flow mode) */}
      {layoutMode === 'flow' && (
        <div className="absolute top-4 left-[280px] bg-kb-card/80 backdrop-blur-sm border border-kb-border rounded-lg px-3 py-1.5 z-10 flex gap-3">
          {FLOW_COLUMNS.filter((col) => {
            return col.some((k) => availableKinds.includes(k) && !hiddenKinds.has(k))
          }).map((col) => (
            <span key={col.join(',')} className="text-[8px] font-mono text-kb-text-tertiary uppercase tracking-[0.06em]">
              {col.map((k) => KIND_SHORT[k] || k).join(' / ')}
            </span>
          ))}
          <span className="text-[8px] font-mono text-kb-text-tertiary">→</span>
        </div>
      )}

      {/* Legend — grouped by category so edge types and node status don't
          get visually conflated. */}
      <div className="absolute bottom-4 left-64 bg-kb-card/95 backdrop-blur-sm border border-kb-border rounded-lg p-3 z-10 space-y-2">
        <div className="flex items-center gap-4 flex-wrap">
          <span className="text-[8px] font-mono font-semibold text-kb-text-tertiary uppercase tracking-[0.1em] shrink-0">Edges</span>
          <LegendItem kind="dotted-line" color="#1DBD7D" label="Traffic" description="Service → Pod" />
          <LegendItem kind="dotted-line" color="#a78bfa" label="Routing" description="Ingress → Service" />
          <LegendItem kind="solid-line" color="#22d3ee" label="Storage" description="PVC / Volume" />
          <LegendItem kind="dashed" color="#f5a623" label="Config" description="ConfigMap / Secret" />
          <LegendItem kind="solid-line" color="rgba(255,255,255,0.25)" label="Ownership" description="Deployment → Pod" />
        </div>
        <div className="flex items-center gap-4 flex-wrap border-t border-kb-border pt-2">
          <span className="text-[8px] font-mono font-semibold text-kb-text-tertiary uppercase tracking-[0.1em] shrink-0">Status</span>
          <LegendItem kind="dot" color="#22d68a" label="Healthy" />
          <LegendItem kind="dot" color="#f5a623" label="Warning" />
          <LegendItem kind="dot" color="#ef4056" label="Error" />
          <LegendItem kind="dot" color="#555770" label="Unknown" />
        </div>
      </div>

      {/* Detail panel */}
      {selectedNode && topology && (
        <NodeDetailPanel
          node={selectedNode}
          edges={topology.edges}
          allNodes={topology.nodes}
          onClose={() => setSelectedNode(null)}
        />
      )}

      {/* Edge hover tooltip. Positioned in viewport coordinates
          (position: fixed) so it doesn't inherit the map's zoom or
          panning transforms. pointer-events: none so the overlay
          itself doesn't steal the mouse from the edge underneath.
          Content pulled from the *current* edges array so polling
          refreshes keep the tooltip fresh while the mouse stays put.
          Visual matches the Monitor tab chart tooltip: header + rule,
          colored dot + label + right-aligned value per row. */}
      {hoveredEdge && hoveredTooltip && (
        <div
          style={{
            position: 'fixed',
            left: hoveredEdge.x + 14,
            top: hoveredEdge.y + 14,
            pointerEvents: 'none',
            zIndex: 1000,
          }}
          className="bg-kb-elevated/95 backdrop-blur border border-kb-border rounded-md px-3 py-2 text-[11px] shadow-xl min-w-[200px]"
        >
          <div className="text-kb-text-primary font-mono font-semibold text-[12px] tabular-nums mb-2 pb-1.5 border-b border-kb-border/60 flex items-baseline justify-between gap-3">
            <span>{hoveredTooltip.rate.toFixed(2)} ev/s</span>
            <span className="text-[10px] font-normal uppercase tracking-wider text-kb-text-tertiary">{hoveredTooltip.verdict}</span>
          </div>
          <div className="space-y-1">
            {hoveredTooltip.l7 ? (
              <>
                <TooltipRow
                  color="#94a3b8"
                  label="HTTP"
                  value={`${(hoveredTooltip.l7.requestsPerSec ?? 0).toFixed(2)} req/s`}
                />
                {Object.entries(hoveredTooltip.l7.statusClass ?? {})
                  .filter(([, v]) => (v ?? 0) > 0)
                  .sort(([, a], [, b]) => (b ?? 0) - (a ?? 0))
                  .map(([sc, v]) => (
                    <TooltipRow
                      key={sc}
                      color={STATUS_CLASS_COLOR[sc] ?? '#64748b'}
                      label={STATUS_CLASS_LABEL[sc] ?? sc}
                      value={`${(v as number).toFixed(2)}/s`}
                    />
                  ))}
                {typeof hoveredTooltip.l7.avgLatencyMs === 'number' && hoveredTooltip.l7.avgLatencyMs > 0 && (
                  <TooltipRow
                    color="#94a3b8"
                    label="avg latency"
                    value={`${hoveredTooltip.l7.avgLatencyMs.toFixed(1)} ms`}
                  />
                )}
              </>
            ) : (
              <div className="text-[10px] text-kb-text-tertiary italic">
                No L7 visibility on this pair
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

export function ClusterMap() {
  return (
    <ReactFlowProvider>
      <ClusterMapInner />
    </ReactFlowProvider>
  )
}
