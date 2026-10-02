import type { TopologyEdge, TopologyNode } from '@/types/kubernetes'

// Replica grouping for the Grid and Flow layouts (finding #34).
//
// A Deployment with 10 pods drew 10 pod cards, and its rollout history drew a
// ReplicaSet card per old revision. The namespace block grows with the tallest
// column, so one busy workload made the whole map unreadable — 600 replicas of
// a density test landed at 3 % zoom. Interchangeable replicas carry one fact:
// "N of them, all healthy". This folds them into one node with a count.
//
// What is NOT folded, because folding would hide the thing the operator opened
// the map to find:
//   - pods that are not healthy — each stays its own card next to the group;
//   - pods of a StatefulSet, or selected by a headless Service: there is no
//     load balancer in front, `redis-0` is not `redis-1`, the pod IS the
//     endpoint;
//   - groups the operator expanded (one click on the group node).
//
// History is hidden unless asked for: a Deployment's retired ReplicaSets
// (scaled to 0/0, kept only for rollback) and Jobs that completed, with their
// pods. Neither receives traffic or uses resources, and both are reachable
// where they belong — the Deployment's History tab, the CronJob's Jobs. A
// rollout in progress still shows the old ReplicaSet, because it still has
// pods; a FAILED Job stays, because it is a signal.
//
// Traffic mode is not grouped here: flows name individual pods, and that view
// is built from them.

/** Group node ids start with this; a click on one expands it. */
export const GROUP_ID_PREFIX = 'group:'

/** Fewer than this many interchangeable members are drawn as they are. */
export const MIN_GROUP_SIZE = 2

export interface GroupOptions {
  /** Show retired ReplicaSets and completed Jobs (with their pods). */
  showHistory?: boolean
}

export interface GroupedGraph {
  nodes: TopologyNode[]
  edges: TopologyEdge[]
  /** group id → ids of the nodes folded into it. */
  members: Map<string, string[]>
}

const UNGROUPED_OWNER_KINDS = new Set(['StatefulSet'])

function kindOf(n: TopologyNode): string {
  return n.kind || n.type || ''
}

/**
 * A pod counts as a healthy replica when the backend says so. Older backends
 * send no `ready` flag; then only the phase is known, and Running / Succeeded
 * is the best available reading.
 */
export function isHealthyPod(n: TopologyNode): boolean {
  const ready = n.metadata?.ready
  if (ready !== undefined) return ready === 'true'
  return n.status === 'Running' || n.status === 'Succeeded'
}

/** An old Deployment revision: a ReplicaSet scaled to zero ("0/0"). */
export function isRetiredReplicaSet(n: TopologyNode): boolean {
  return kindOf(n) === 'ReplicaSet' && /^0\/0$/.test(n.status || '')
}

/** A Job that finished successfully. Failed and running Jobs are not history. */
export function isCompletedJob(n: TopologyNode): boolean {
  return kindOf(n) === 'Job' && n.status === 'Complete'
}

/**
 * Removes history (see the header): retired ReplicaSets of a Deployment,
 * completed Jobs and every pod those Jobs own. Edges to removed nodes go too.
 */
export function withoutHistory(nodes: TopologyNode[], edges: TopologyEdge[]): { nodes: TopologyNode[]; edges: TopologyEdge[] } {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const hidden = new Set<string>()
  for (const e of edges) {
    if (e.type !== 'owns') continue
    const owner = byId.get(e.source)
    const child = byId.get(e.target)
    if (!owner || !child) continue
    if (kindOf(owner) === 'Deployment' && isRetiredReplicaSet(child)) hidden.add(child.id)
  }
  for (const n of nodes) if (isCompletedJob(n)) hidden.add(n.id)
  for (const e of edges) {
    if (e.type === 'owns' && hidden.has(e.source) && kindOf(byId.get(e.source)!) === 'Job') hidden.add(e.target)
  }
  if (hidden.size === 0) return { nodes, edges }
  return {
    nodes: nodes.filter((n) => !hidden.has(n.id)),
    edges: edges.filter((e) => !hidden.has(e.source) && !hidden.has(e.target)),
  }
}

function nameFromId(id: string): string {
  const parts = id.split('/')
  return parts[parts.length - 1] || id
}

export function groupReplicas(
  allNodes: TopologyNode[],
  allEdges: TopologyEdge[],
  expanded: ReadonlySet<string>,
  opts: GroupOptions = {},
): GroupedGraph {
  const { nodes, edges } = opts.showHistory ? { nodes: allNodes, edges: allEdges } : withoutHistory(allNodes, allEdges)
  const byId = new Map(nodes.map((n) => [n.id, n]))

  // Owner of each pod / ReplicaSet, from the ownership edges the backend
  // already builds. A pod without an owner (a bare pod) is never grouped.
  const ownerOf = new Map<string, string>()
  const pinned = new Set<string>()
  for (const e of edges) {
    if (e.type === 'owns') ownerOf.set(e.target, e.source)
    if (e.type === 'selects' && byId.get(e.source)?.metadata?.headless === 'true') {
      pinned.add(e.target)
    }
  }

  const candidates = new Map<string, TopologyNode[]>() // group id → members
  const ownerOfGroup = new Map<string, TopologyNode>()
  for (const n of nodes) {
    if (kindOf(n) !== 'Pod') continue
    const ownerId = ownerOf.get(n.id)
    const owner = ownerId ? byId.get(ownerId) : undefined
    if (!owner) continue
    if (UNGROUPED_OWNER_KINDS.has(kindOf(owner)) || pinned.has(n.id) || !isHealthyPod(n)) continue
    const groupId = `${GROUP_ID_PREFIX}Pod:${owner.id}`
    if (expanded.has(groupId)) continue
    const list = candidates.get(groupId) ?? []
    list.push(n)
    candidates.set(groupId, list)
    ownerOfGroup.set(groupId, owner)
  }

  const memberToGroup = new Map<string, string>()
  const members = new Map<string, string[]>()
  const groupNodes: TopologyNode[] = []
  for (const [groupId, list] of candidates) {
    if (list.length < MIN_GROUP_SIZE) continue
    const owner = ownerOfGroup.get(groupId)!
    for (const m of list) memberToGroup.set(m.id, groupId)
    members.set(groupId, list.map((m) => m.id))
    const allSucceeded = list.every((m) => m.status === 'Succeeded')
    const name = owner.name || nameFromId(owner.id)
    groupNodes.push({
      id: groupId,
      type: 'Pod',
      kind: 'Pod',
      name,
      label: name,
      namespace: owner.namespace,
      status: allSucceeded ? 'Succeeded' : 'Running',
      metadata: { groupCount: String(list.length), groupOf: owner.id },
      // The per-replica status row ResourceNode already draws for a workload.
      pods: list.map((m) => ({ name: m.name, status: m.status, ready: true })),
    })
  }

  // An unhealthy pod drawn on its own shows WHY, not its phase: CrashLoopBackOff
  // is still phase Running and would otherwise get a green dot.
  const outNodes: TopologyNode[] = []
  for (const n of nodes) {
    if (memberToGroup.has(n.id)) continue
    const reason = n.metadata?.reason
    outNodes.push(kindOf(n) === 'Pod' && reason && !isHealthyPod(n) ? { ...n, status: reason } : n)
  }
  outNodes.push(...groupNodes)

  // Edges follow their endpoints into the group, deduplicated: ten "selects"
  // from one Service to ten pods become one edge to the group.
  const seen = new Set<string>()
  const outEdges: TopologyEdge[] = []
  for (const e of edges) {
    const source = memberToGroup.get(e.source) ?? e.source
    const target = memberToGroup.get(e.target) ?? e.target
    if (source === target) continue
    const key = `${source}|${target}|${e.type}`
    if (seen.has(key)) continue
    seen.add(key)
    outEdges.push(source === e.source && target === e.target ? e : { ...e, id: `${source}-${target}-${e.type}`, source, target })
  }

  return { nodes: outNodes, edges: outEdges, members }
}
