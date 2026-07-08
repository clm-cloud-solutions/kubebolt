import { describe, it, expect } from 'vitest'
import { vmResultToDeploys, buildDeploysVMQuery } from './useDeploysVM'
import type { PromVectorResponse } from '@/services/api'

// Shape mirrors a real kube_replicaset_created⋈kube_replicaset_owner instant
// response captured from VM (cluster_id=kind). value[0] is the eval time;
// value[1] is the ReplicaSet creation unix timestamp = the rollout time.
function resp(
  rows: Array<{ namespace?: string; owner_name?: string; replicaset?: string; createdUnix?: string }>,
): PromVectorResponse {
  return {
    status: 'success',
    data: {
      resultType: 'vector',
      result: rows.map((r) => ({
        metric: {
          cluster_id: '5368e0d2',
          namespace: r.namespace ?? 'shop-backend',
          owner_name: r.owner_name ?? 'orders-api',
          replicaset: r.replicaset ?? 'orders-api-6559889cfd',
          job: 'kube-state-metrics',
        },
        value: [1782853845, r.createdUnix ?? '1782170764'] as [number, string],
      })),
    },
  }
}

describe('vmResultToDeploys', () => {
  it('returns [] for undefined / empty / errored responses', () => {
    expect(vmResultToDeploys(undefined)).toEqual([])
    expect(vmResultToDeploys({ status: 'success', data: { resultType: 'vector', result: [] } })).toEqual([])
    expect(vmResultToDeploys({ status: 'error', error: 'boom' })).toEqual([])
  })

  it('maps owner_name→name, value[1]→deployedAt, kind=Deployment, no image', () => {
    const out = vmResultToDeploys(
      resp([{ namespace: 'autopilot-demo', owner_name: 'image-app', createdUnix: '1782466388' }]),
    )
    expect(out).toHaveLength(1)
    expect(out[0]).toEqual({
      namespace: 'autopilot-demo',
      kind: 'Deployment',
      name: 'image-app',
      // value[1] (creation ts), NOT value[0] (eval ts) — 1782466388s → ISO.
      deployedAt: new Date(1782466388 * 1000).toISOString(),
    })
    expect(out[0].image).toBeUndefined()
  })

  it('sorts newest-first by creation time', () => {
    const out = vmResultToDeploys(
      resp([
        { owner_name: 'old', createdUnix: '1782170000' },
        { owner_name: 'new', createdUnix: '1782900000' },
        { owner_name: 'mid', createdUnix: '1782500000' },
      ]),
    )
    expect(out.map((d) => d.name)).toEqual(['new', 'mid', 'old'])
  })

  it('keeps two ReplicaSets of the same Deployment as two rollouts', () => {
    // A Deployment with two RSs in-window = two rollouts (matches the
    // connector's per-ReplicaSet model). Real case: cilium-operator.
    const out = vmResultToDeploys(
      resp([
        { owner_name: 'cilium-operator', replicaset: 'cilium-operator-58b4b4bdcd', createdUnix: '1782170347' },
        { owner_name: 'cilium-operator', replicaset: 'cilium-operator-67b584b57f', createdUnix: '1782170348' },
      ]),
    )
    expect(out).toHaveLength(2)
    expect(out.every((d) => d.name === 'cilium-operator')).toBe(true)
  })

  it('skips rows missing namespace or owner_name, or with a bad value', () => {
    const broken: PromVectorResponse = {
      status: 'success',
      data: {
        resultType: 'vector',
        result: [
          { metric: { namespace: 'ns' }, value: [1, '1782170764'] }, // no owner_name
          { metric: { owner_name: 'x' }, value: [1, '1782170764'] }, // no namespace
          { metric: { namespace: 'ns', owner_name: 'y' }, value: [1, 'NaN'] }, // bad ts
          { metric: { namespace: 'ns', owner_name: 'z' }, value: [1, '1782170764'] }, // good
        ],
      },
    }
    const out = vmResultToDeploys(broken)
    expect(out.map((d) => d.name)).toEqual(['z'])
  })
})

describe('buildDeploysVMQuery', () => {
  it('windows kube_replicaset_created and joins the Deployment owner', () => {
    const q = buildDeploysVMQuery(1781644245)
    expect(q).toContain('kube_replicaset_created >= 1781644245')
    expect(q).toContain('on(namespace, replicaset)')
    expect(q).toContain('group_left(owner_name)')
    expect(q).toContain('kube_replicaset_owner{owner_kind="Deployment"}')
  })
})
