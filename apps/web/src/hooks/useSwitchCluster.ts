import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { api } from '@/services/api'
import { useCopilot } from '@/contexts/CopilotContext'
import type { ClusterInfo } from '@/types/kubernetes'

// useSwitchCluster — the ONE cluster-switch flow, extracted from Topbar so
// other surfaces (the episode detail's «Switch context», in-vivo ask 01-sep)
// reuse the exact same contract instead of forking it:
//
//  - mutationKey ['switch-cluster'] is what Layout's useIsMutating watches to
//    paint the single centered "Connecting" overlay.
//  - The mutation stays pending until the NEW cluster's overview reached a
//    terminal state, cancelling any in-flight overview fetch first — a
//    request fired for the PREVIOUS cluster may still be pending, and React
//    Query would dedupe onto it and cache the old cluster's data.
//  - Optimistic active-flag flip on mutate; Copilot transcript wiped on
//    success (it referenced the previous cluster's resources).
//
// `goHome` (default true — the Topbar behavior) navigates to Overview on
// success; org-level pages that survive a switch (episode detail) pass false
// and stay put.
export function useSwitchCluster(opts?: { goHome?: boolean; onStarted?: () => void }) {
  const goHome = opts?.goHome ?? true
  const onStarted = opts?.onStarted
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const { clearHistory: clearCopilotHistory } = useCopilot()

  return useMutation({
    mutationKey: ['switch-cluster'],
    mutationFn: async (context: string) => {
      let switchErr: unknown = null
      try {
        await api.switchCluster(context)
      } catch (e) {
        switchErr = e
      }
      await queryClient.cancelQueries({ queryKey: ['cluster-overview'] })
      await queryClient.refetchQueries({ queryKey: ['cluster-overview'] })
      if (switchErr) throw switchErr
    },
    onMutate: (context: string) => {
      queryClient.setQueryData(['clusters'], (old: ClusterInfo[] | undefined) =>
        old?.map((c) => ({ ...c, active: c.context === context })),
      )
      onStarted?.()
    },
    onSuccess: () => {
      clearCopilotHistory()
      void queryClient.invalidateQueries()
      if (goHome) navigate('/')
    },
    onError: () => {
      void queryClient.invalidateQueries()
    },
  })
}
