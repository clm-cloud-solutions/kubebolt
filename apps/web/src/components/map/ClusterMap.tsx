import { useState, useMemo, useCallback, useEffect } from 'react'
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
import { LayoutGrid, GitBranch, Zap, ZapOff, RotateCcw } from 'lucide-react'
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

type LayoutMode = 'grid' | 'flow'

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
        selectable: false, draggable: false,
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
        selectable: false, draggable: false,
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
  const { data: flowData } = useFlowEdges({ enabled: trafficEnabled, windowMinutes: 5 })
  // Manual position overrides set by user drag. Keyed by node ID.
  // Cleared when switching layout mode or clicking Reset.
  const [dragOverrides, setDragOverrides] = useState<Map<string, { x: number; y: number }>>(new Map())
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

  // Reset manual positions whenever the layout mode changes — the new layout
  // picks completely different coordinates so old overrides wouldn't make sense.
  useEffect(() => { setDragOverrides(new Map()) }, [layoutMode])

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
    const filtered = filterNodes(topology.nodes, hiddenKinds, visibleNamespaces)
    if (layoutMode === 'flow') {
      return buildFlowLayout(filtered, topology.edges || [])
    }
    return buildGridLayout(filtered)
  }, [topology?.nodes, topology?.edges, hiddenKinds, visibleNamespaces, layoutMode])

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
  const [flowNodes, setFlowNodes, onNodesChange] = useNodesState(initialNodes)
  useEffect(() => { setFlowNodes(initialNodes) }, [initialNodes, setFlowNodes])

  // Persist drag deltas when the user lets go of a node.
  const onNodeDragStop = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.type === 'namespaceRegion') return
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
    const structural: Edge[] = topology.edges
      .filter((e) => visibleIds.has(e.source) && visibleIds.has(e.target))
      .filter((e) => {
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
    // returned data. Each edge sources from pod_flow_events_total so the
    // id is unique across (src, dst, verdict). We skip pairs whose pods
    // aren't on the map (filtered out by kind/namespace).
    if (!trafficEnabled || !flowData?.edges?.length) {
      return structural
    }
    const traffic: Edge[] = []
    for (const f of flowData.edges) {
      const sourceId = `Pod/${f.srcNamespace}/${f.srcPod}`
      const targetId = `Pod/${f.dstNamespace}/${f.dstPod}`
      if (!visibleIds.has(sourceId) || !visibleIds.has(targetId)) continue
      traffic.push({
        id: `flow/${f.srcNamespace}/${f.srcPod}->${f.dstNamespace}/${f.dstPod}/${f.verdict}`,
        source: sourceId,
        target: targetId,
        type: 'connection',
        data: {
          edgeType: 'traffic',
          ratePerSec: f.ratePerSec,
          verdict: f.verdict,
          sourceStatus: nodeStatusMap.get(sourceId) || '',
          targetStatus: nodeStatusMap.get(targetId) || '',
          animationsEnabled,
        },
        animated: animationsEnabled,
      })
    }
    return [...structural, ...traffic]
  }, [topology?.edges, topology?.nodes, computedNodes, animationsEnabled, trafficEnabled, hiddenEdgeGroups, flowData])

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

  if (isLoading) return <LoadingSpinner />
  if (error) return <ErrorState message={error.message} onRetry={() => refetch()} />

  const nsCount = visibleNamespaces === null ? allNamespaces.length : visibleNamespaces.size

  return (
    <div className="h-[calc(100vh-52px)] relative">
      <ReactFlow
        nodes={flowNodes}
        edges={edges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodesChange={onNodesChange}
        onNodeDragStop={onNodeDragStop}
        onNodeClick={onNodeClick}
        onNodeDoubleClick={onNodeDoubleClick}
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
