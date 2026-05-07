import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from '@/services/api'
import type { ResourceItem } from '@/types/kubernetes'

// useNodeSchedulability — shared mutation hook for cordon /
// uncordon. Encapsulates the optimistic-update + cache-sync +
// rollback dance so call-sites only worry about WHEN to fire it.
//
// Two consumers today:
//   - NodeActionMenu (three-dot popover on the Nodes list cards)
//   - NodeSchedulabilityToolbarButton (Node detail page toolbar)
//
// Both need exactly the same mutation: optimistic flip across
// every cronjobs-list-cache entry, API call, sync detail cache
// from the canonical response (which has the defensive informer-
// cache override on the backend), no refetchQueries on success
// (the backend's GET would hit the lagging informer and override
// our correct optimistic update — same bug pattern that bit
// rollback/cordon historically). On error: refetch to roll back +
// surface error to the caller.

interface UseNodeSchedulability {
  // Run the mutation. `target=true` cordons, `false` uncordons.
  // Returns void; consult `error` after for failure handling.
  run: (target: boolean) => Promise<void>
  // True while the request is in flight.
  busy: boolean
  // Last failed-mutation context (null when none). The action
  // string is "Cordon" or "Uncordon" so the caller can pass it
  // directly to MutationErrorToast.
  error: { err: unknown; action: string } | null
  clearError: () => void
}

export function useNodeSchedulability(node: ResourceItem): UseNodeSchedulability {
  const queryClient = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<{ err: unknown; action: string } | null>(null)

  const nodeName = node.name

  async function run(target: boolean) {
    setBusy(true)
    setError(null)

    // Optimistic flip across every nodes-list cache entry. Prefix
    // match catches paginated / filtered variants too.
    queryClient.setQueriesData<{ items: ResourceItem[] }>(
      { queryKey: ['resources', 'nodes'] },
      (old) => {
        if (!old) return old
        return {
          ...old,
          items: old.items.map((n) =>
            n.name === nodeName ? { ...n, unschedulable: target } : n,
          ),
        }
      },
    )

    try {
      const res = target
        ? await api.cordonNode(nodeName, 'ui')
        : await api.uncordonNode(nodeName, 'ui')
      // Sync detail cache. Backend overrides the informer-cache
      // value with the just-patched value before responding so
      // this is reliable even if its informer briefly lags.
      if (res.node) {
        queryClient.setQueryData(['resource-detail', 'nodes', '_', nodeName], res.node)
      }
      // No refetchQueries on success — the backend's GET reads
      // from the informer cache which can lag the Patch by ~ms,
      // returning the pre-patch value and overriding our correct
      // optimistic update. The periodic refetchInterval reconciles
      // any drift later.
    } catch (err) {
      // On error, refetch to roll back the optimistic flip.
      queryClient.refetchQueries({ queryKey: ['resources', 'nodes'], type: 'active' })
      setError({
        err: err instanceof ApiError ? err : err,
        action: target ? 'Cordon' : 'Uncordon',
      })
    } finally {
      setBusy(false)
    }
  }

  return { run, busy, error, clearError: () => setError(null) }
}
