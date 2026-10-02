import { describe, expect, it } from 'vitest'
import type { TopologyEdge, TopologyNode } from '@/types/kubernetes'
import { GROUP_ID_PREFIX, groupReplicas } from './replicaGroups'

function node(kind: string, ns: string, name: string, status = '', metadata?: Record<string, string>): TopologyNode {
  return { id: `${kind}/${ns}/${name}`, type: kind, kind, name, label: name, namespace: ns, status, metadata: metadata ?? {} }
}
function edge(source: TopologyNode, target: TopologyNode, type: string): TopologyEdge {
  return { id: `${source.id}-${target.id}-${type}`, source: source.id, target: target.id, type }
}
const healthy = { ready: 'true' }

// A Deployment, its current ReplicaSet, N healthy pods and a Service selecting them.
function deployment(n: number) {
  const deploy = node('Deployment', 'shop', 'web')
  const rs = node('ReplicaSet', 'shop', 'web-7d9', `${n}/${n}`)
  const svc = node('Service', 'shop', 'web', 'ClusterIP')
  const pods = Array.from({ length: n }, (_, i) => node('Pod', 'shop', `web-7d9-${i}`, 'Running', healthy))
  const nodes = [deploy, rs, svc, ...pods]
  const edges = [edge(deploy, rs, 'owns'), ...pods.flatMap((p) => [edge(rs, p, 'owns'), edge(svc, p, 'selects')])]
  return { deploy, rs, svc, pods, nodes, edges }
}

describe('groupReplicas', () => {
  it('folds the healthy replicas of one ReplicaSet into a single node with a count', () => {
    const d = deployment(10)
    const g = groupReplicas(d.nodes, d.edges, new Set())

    expect(g.nodes.filter((n) => n.kind === 'Pod')).toHaveLength(1)
    const group = g.nodes.find((n) => n.id.startsWith(GROUP_ID_PREFIX))!
    expect(group.metadata?.groupCount).toBe('10')
    expect(group.pods).toHaveLength(10)
    // Ten "selects" and ten "owns" edges collapse into one of each.
    expect(g.edges.filter((e) => e.target === group.id).map((e) => e.type).sort()).toEqual(['owns', 'selects'])
  })

  it('keeps an unhealthy replica as its own node, labelled with why', () => {
    const d = deployment(5)
    d.pods[0].metadata = { ready: 'false', reason: 'CrashLoopBackOff' }
    const g = groupReplicas(d.nodes, d.edges, new Set())

    const crashing = g.nodes.find((n) => n.id === d.pods[0].id)!
    expect(crashing.status).toBe('CrashLoopBackOff')
    expect(g.nodes.find((n) => n.id.startsWith(GROUP_ID_PREFIX))?.metadata?.groupCount).toBe('4')
  })

  it('never folds StatefulSet pods or pods behind a headless Service', () => {
    const sts = node('StatefulSet', 'db', 'redis')
    const sPods = [0, 1, 2].map((i) => node('Pod', 'db', `redis-${i}`, 'Running', healthy))
    const d = deployment(3)
    d.svc.metadata = { headless: 'true' }
    const nodes = [sts, ...sPods, ...d.nodes]
    const edges = [...sPods.map((p) => edge(sts, p, 'owns')), ...d.edges]

    const g = groupReplicas(nodes, edges, new Set())
    expect(g.members.size).toBe(0)
    expect(g.nodes).toHaveLength(nodes.length)
  })

  it('hides a Deployment’s retired ReplicaSets, but not the current one', () => {
    const d = deployment(2)
    const old = [1, 2, 3].map((i) => node('ReplicaSet', 'shop', `web-old${i}`, '0/0'))
    const nodes = [...d.nodes, ...old]
    const edges = [...d.edges, ...old.map((rs) => edge(d.deploy, rs, 'owns'))]

    const g = groupReplicas(nodes, edges, new Set())
    expect(g.nodes.some((n) => old.some((o) => o.id === n.id))).toBe(false)
    expect(g.edges.some((e) => old.some((o) => o.id === e.target))).toBe(false)
    expect(g.nodes.some((n) => n.id === d.rs.id)).toBe(true)

    const withHistory = groupReplicas(nodes, edges, new Set(), { showHistory: true })
    expect(withHistory.nodes.filter((n) => n.kind === 'ReplicaSet')).toHaveLength(4)
  })

  it('hides completed Jobs and their pods, keeps failed and running ones', () => {
    const cron = node('CronJob', 'ops', 'backup', 'Scheduled')
    const done = node('Job', 'ops', 'backup-1', 'Complete')
    const failed = node('Job', 'ops', 'backup-2', 'Failed')
    const running = node('Job', 'ops', 'backup-3', 'Running')
    const donePod = node('Pod', 'ops', 'backup-1-x', 'Succeeded', healthy)
    const failedPod = node('Pod', 'ops', 'backup-2-x', 'Failed', { ready: 'false' })
    const runningPod = node('Pod', 'ops', 'backup-3-x', 'Running', healthy)
    const nodes = [cron, done, failed, running, donePod, failedPod, runningPod]
    const edges = [
      edge(cron, done, 'owns'), edge(cron, failed, 'owns'), edge(cron, running, 'owns'),
      edge(done, donePod, 'owns'), edge(failed, failedPod, 'owns'), edge(running, runningPod, 'owns'),
    ]

    const ids = groupReplicas(nodes, edges, new Set()).nodes.map((n) => n.id)
    expect(ids).not.toContain(done.id)
    expect(ids).not.toContain(donePod.id)
    expect(ids).toEqual(expect.arrayContaining([cron.id, failed.id, failedPod.id, running.id, runningPod.id]))

    const all = groupReplicas(nodes, edges, new Set(), { showHistory: true }).nodes.map((n) => n.id)
    expect(all).toEqual(expect.arrayContaining([done.id, donePod.id]))
  })

  it('draws an expanded group as individual nodes', () => {
    const d = deployment(4)
    const groupId = `${GROUP_ID_PREFIX}Pod:${d.rs.id}`
    const g = groupReplicas(d.nodes, d.edges, new Set([groupId]))
    expect(g.nodes.filter((n) => n.kind === 'Pod')).toHaveLength(4)
  })

  it('leaves a single replica as it is', () => {
    const d = deployment(1)
    const g = groupReplicas(d.nodes, d.edges, new Set())
    expect(g.members.size).toBe(0)
  })
})
