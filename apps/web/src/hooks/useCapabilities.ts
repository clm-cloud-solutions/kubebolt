import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, type CapabilityState } from '@/services/api'

// useCapabilities reads the org's capability registry (#50) — the single
// source of truth for "what is this org not getting, and why". One shared
// queryKey so the Plan & usage section and the point-of-use banners dedupe
// the round-trip. On installs without the registry (OSS, EE without a DB)
// the endpoint 503s: retry is off, `capabilities` stays empty, and every
// consumer renders nothing.
export function useCapabilities() {
  const { data, isLoading } = useQuery({
    queryKey: ['account-capabilities'],
    queryFn: api.getAccountCapabilities,
    refetchInterval: 60_000, // the evaluator's own cadence
    staleTime: 30_000,
    retry: false,
  })
  const capabilities = data?.capabilities ?? []
  const byId = useMemo(() => {
    const m: Record<string, CapabilityState> = {}
    for (const c of capabilities) m[c.id] = c
    return m
  }, [capabilities])
  return { capabilities, byId, loaded: !!data, isLoading }
}
