import { useState, useMemo, useCallback, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import ReactFlow, {
  Background,
  MiniMap,
  ReactFlowProvider,
  useReactFlow,
  type Node,
  type Edge,
  type NodeTypes,
  type EdgeTypes,
} from 'reactflow'
import 'reactflow/dist/style.css'
import { LayoutGrid, GitBranch } from 'lucide-react'
import { useTopology } from '@/hooks/useTopology'
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

function ClusterMapInner() {
  const { data: topology, isLoading, error, refetch } = useTopology()
  const [selectedNode, setSelectedNode] = useState<TopologyNode | null>(null)
  const [hiddenKinds, setHiddenKinds] = useState<Set<string>>(new Set())
  const [visibleNamespaces, setVisibleNamespaces] = useState<Set<string> | null>(null)
  const [nsFilterOpen, setNsFilterOpen] = useState(false)
  const [layoutMode, setLayoutMode] = useState<LayoutMode>('flow')
  const { fitView } = useReactFlow()
  const navigate = useNavigate()

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

  const flowNodes = useMemo(() => {
    if (!topology?.nodes) return []
    const filtered = filterNodes(topology.nodes, hiddenKinds, visibleNamespaces)
    if (layoutMode === 'flow') {
      return buildFlowLayout(filtered, topology.edges || [])
    }
    return buildGridLayout(filtered)
  }, [topology?.nodes, topology?.edges, hiddenKinds, visibleNamespaces, layoutMode])

  const edges: Edge[] = useMemo(() => {
    if (!topology?.edges) return []
    const visibleIds = new Set(flowNodes.map((n) => n.id))
    const nodeStatusMap = new Map<string, string>()
    if (topology?.nodes) {
      for (const n of topology.nodes) {
        nodeStatusMap.set(n.id, n.status || '')
      }
    }
    return topology.edges
      .filter((e) => visibleIds.has(e.source) && visibleIds.has(e.target))
      .map((e) => ({
        id: e.id, source: e.source, target: e.target,
        type: 'connection',
        data: {
          edgeType: e.type,
          animated: e.animated,
          sourceStatus: nodeStatusMap.get(e.source) || '',
          targetStatus: nodeStatusMap.get(e.target) || '',
        },
        animated: e.animated,
      }))
  }, [topology?.edges, topology?.nodes, flowNodes])

  useEffect(() => {
    if (flowNodes.length > 0) {
      const t = setTimeout(() => fitView({ padding: 0.1 }), 100)
      return () => clearTimeout(t)
    }
  }, [flowNodes.length, hiddenKinds, visibleNamespaces, layoutMode, fitView])

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
        onNodeClick={onNodeClick}
        onNodeDoubleClick={onNodeDoubleClick}
        onPaneClick={() => setSelectedNode(null)}
        fitView
        fitViewOptions={{ padding: 0.1 }}
        proOptions={{ hideAttribution: true }}
        className="bg-kb-bg"
        minZoom={0.03}
        maxZoom={2}
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

      {/* Legend */}
      <div className="absolute bottom-4 left-64 bg-kb-card/95 backdrop-blur-sm border border-kb-border rounded-lg px-3 py-2 z-10 flex gap-3 flex-wrap">
        {[
          { color: '#1DBD7D', label: 'Traffic', dot: true },
          { color: '#a78bfa', label: 'Routing', dot: true },
          { color: '#f5a623', label: 'Config', dashed: true },
          { color: '#22d3ee', label: 'Storage' },
          { color: 'rgba(255,255,255,0.10)', label: 'Ownership' },
          { color: '#22d68a', statusDot: true, label: 'Ok' },
          { color: '#ef4056', statusDot: true, label: 'Error' },
        ].map((item) => (
          <div key={item.label} className="flex items-center gap-1.5">
            {item.statusDot ? (
              <div className="w-2 h-2 rounded-full" style={{ background: item.color }} />
            ) : item.dashed ? (
              <div className="w-4 h-0.5 border-t border-dashed" style={{ borderColor: item.color }} />
            ) : item.dot ? (
              <div className="flex items-center gap-0.5">
                <div className="w-3 h-0.5 rounded" style={{ background: item.color }} />
                <div className="w-1 h-1 rounded-full" style={{ background: item.color }} />
              </div>
            ) : (
              <div className="w-4 h-0.5 rounded" style={{ background: item.color }} />
            )}
            <span className="text-[9px] font-mono text-kb-text-secondary">{item.label}</span>
          </div>
        ))}
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
