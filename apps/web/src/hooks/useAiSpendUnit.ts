import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'
import { useAuth } from '@/contexts/AuthContext'
import { eeAiSpendUnitDefault } from '@/ee/registry'

export type AiSpendUnit = 'credits' | 'usd'

// useAiSpendUnit — the unit the usage views show AI spend in, as the backend
// decides it (GET /copilot/config's spendUnit, settings.PlatformManagedAI):
//
//   credits  the platform runs the AI (SaaS). A credit is the only unit the
//            customer sees; the LLM cost behind it is the platform's.
//   usd      the org brings its own key (self-hosted EE, OSS). The cost is
//            their own provider bill, so it is theirs to see.
//
// Same query (key and fetcher) as the Copilot panel's, so it costs no extra
// request. Until the answer arrives, and on a backend that does not send the
// field (or sends one it does not know), it says what the edition starts at
// (eeAiSpendUnitDefault): usd in OSS, credits in the Enterprise build.
export function useAiSpendUnit(): AiSpendUnit {
  const auth = useAuth()
  const { data } = useQuery({
    queryKey: ['copilot-config', auth.user?.id ?? 'anon'],
    queryFn: api.getCopilotConfig,
    staleTime: 60_000,
    enabled: auth.sessionReady,
  })
  const said = data?.spendUnit
  return said === 'usd' || said === 'credits' ? said : eeAiSpendUnitDefault
}

export function fmtCredits(n: number): string {
  return Math.round(n).toLocaleString('en-US')
}

export function fmtUsd(n: number): string {
  if (n === 0) return '$0'
  if (n < 0.01) return `$${n.toFixed(4)}`
  if (n < 1) return `$${n.toFixed(3)}`
  return `$${n.toFixed(2)}`
}
