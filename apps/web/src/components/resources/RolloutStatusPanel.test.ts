import { describe, it, expect } from 'vitest'
import { detectStatus } from './RolloutStatusPanel'
import type { ResourceItem } from '@/types/kubernetes'

// Helper: build a minimal resource-detail-like payload. The real
// `ResourceItem` type is a discriminated union with many fields; the
// panel only reads the controller-status fields, so a partial shape
// is enough for this unit test.
function detail(fields: Record<string, unknown>): ResourceItem {
  return fields as unknown as ResourceItem
}

describe('detectStatus', () => {
  describe('zero-replica convergence (regression: modal stuck on workloads scaled to 0)', () => {
    it('deployment with replicas=0 converges once the patch is observed', () => {
      const s = detectStatus(
        'deployments',
        detail({
          generation: 5,
          observedGeneration: 5,
          replicas: 0,
          availableReplicas: 0,
          updatedReplicas: 0,
        }),
        5,
      )
      expect(s.converged).toBe(true)
      expect(s.targetCount).toBe(0)
      expect(s.readyCount).toBe(0)
      expect(s.updatedCount).toBe(0)
    })

    it('statefulset with replicas=0 converges once the patch is observed', () => {
      const s = detectStatus(
        'statefulsets',
        detail({
          generation: 3,
          observedGeneration: 3,
          replicas: 0,
          readyReplicas: 0,
          updatedReplicas: 0,
        }),
        3,
      )
      expect(s.converged).toBe(true)
    })

    it('daemonset with desired=0 (no nodes matched) converges once observed', () => {
      const s = detectStatus(
        'daemonsets',
        detail({
          generation: 2,
          observedGeneration: 2,
          desired: 0,
          ready: 0,
          updatedNumber: 0,
        }),
        2,
      )
      expect(s.converged).toBe(true)
    })

    it('replicas=0 but apiserver has NOT observed the new generation — still pending', () => {
      // observedGeneration lags generation: the patch was accepted but the
      // controller hasn't reconciled yet. The genConverged gate keeps us
      // out of "converged" state, exactly as designed — we should not
      // claim success before the apiserver has processed the patch.
      const s = detectStatus(
        'deployments',
        detail({
          generation: 7,
          observedGeneration: 6,
          replicas: 0,
          availableReplicas: 0,
          updatedReplicas: 0,
        }),
        7,
      )
      expect(s.converged).toBe(false)
    })

    it('replicas=0 but generation < expectedGeneration — still pending', () => {
      // The patch hasn't been observed at the apiserver level yet
      // (`generation` still sits at the pre-patch value). Without the
      // expectedGeneration gate we'd false-positive on the OLD revision.
      const s = detectStatus(
        'deployments',
        detail({
          generation: 4,
          observedGeneration: 4,
          replicas: 0,
          availableReplicas: 0,
          updatedReplicas: 0,
        }),
        5,
      )
      expect(s.converged).toBe(false)
    })
  })

  describe('non-zero replicas — existing behavior preserved', () => {
    it('deployment with all replicas ready and updated converges', () => {
      const s = detectStatus(
        'deployments',
        detail({
          generation: 2,
          observedGeneration: 2,
          replicas: 3,
          availableReplicas: 3,
          updatedReplicas: 3,
        }),
        2,
      )
      expect(s.converged).toBe(true)
      expect(s.readyCount).toBe(3)
      expect(s.updatedCount).toBe(3)
      expect(s.targetCount).toBe(3)
    })

    it('deployment with partial rollout does not converge', () => {
      const s = detectStatus(
        'deployments',
        detail({
          generation: 2,
          observedGeneration: 2,
          replicas: 3,
          availableReplicas: 2,
          updatedReplicas: 3,
        }),
        2,
      )
      expect(s.converged).toBe(false)
    })

    it('statefulset with partial readiness does not converge', () => {
      const s = detectStatus(
        'statefulsets',
        detail({
          generation: 2,
          observedGeneration: 2,
          replicas: 3,
          readyReplicas: 2,
          updatedReplicas: 3,
        }),
        2,
      )
      expect(s.converged).toBe(false)
    })

    it('daemonset with one node lagging does not converge', () => {
      const s = detectStatus(
        'daemonsets',
        detail({
          generation: 2,
          observedGeneration: 2,
          desired: 4,
          ready: 3,
          updatedNumber: 4,
        }),
        2,
      )
      expect(s.converged).toBe(false)
    })
  })

  describe('boundary cases', () => {
    it('returns the empty shape when detail is undefined', () => {
      const s = detectStatus('deployments', undefined, 1)
      expect(s.converged).toBe(false)
      expect(s.targetCount).toBe(0)
    })

    it('without expectedGeneration, falls back to generation == observedGeneration', () => {
      const s = detectStatus(
        'deployments',
        detail({
          generation: 5,
          observedGeneration: 5,
          replicas: 0,
          availableReplicas: 0,
          updatedReplicas: 0,
        }),
        undefined,
      )
      expect(s.converged).toBe(true)
    })
  })
})
