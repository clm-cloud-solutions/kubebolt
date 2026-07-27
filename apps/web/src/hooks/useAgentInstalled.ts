import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useMetricsOnly } from '@/hooks/useMetricsOnly'

// useAgentInstalled answers "can this cluster serve VM-backed panels?"
// — the gate used by Capacity's trends/right-sizing and by Overview's
// efficiency-band rec counter.
//
// A metrics-only cluster has no live connector, so the agent-integration
// status is an unreliable gate (it depends on the agent being currently
// registered + the metrics-only override, which flaps when the agent is
// offline). But the VM-backed panels read straight from VictoriaMetrics,
// which the agent populates regardless — so for a metrics-only cluster,
// treat the agent as present.
export function useAgentInstalled(): { installed: boolean; isLoading: boolean } {
  const isMetricsOnly = useMetricsOnly()
  const { data: agent, isLoading } = useQuery({
    queryKey: ['integration', 'agent'],
    queryFn: () => api.getIntegration('agent'),
    refetchInterval: 10_000,
    staleTime: 5_000,
  })
  const installed =
    isMetricsOnly || (!!agent && (agent.status === 'installed' || agent.status === 'degraded'))
  return { installed, isLoading }
}
