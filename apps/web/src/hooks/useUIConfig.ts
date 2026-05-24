import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useUIConfig fetches the public /config/ui endpoint that carries the
// operator-set display name + default refresh interval. Kept as a
// thin TanStack Query wrapper so:
//
//   - Multiple consumers (Topbar, RefreshContext fallback) share one
//     cached result instead of refetching per mount.
//   - Stale-time is long (5min) — these values change at most when an
//     admin saves the General tab; until then they're effectively
//     static. Aggressive caching keeps the chrome from flickering.
//   - The hook returns sane fallbacks on error so the UI keeps
//     rendering "KubeBolt" + 30s polling rather than a broken topbar.
//
// Update-on-save is handled by the General settings form invalidating
// the same ['ui-config'] key after a successful PUT.

const FALLBACK = {
  displayName: '',
  defaultRefreshIntervalSeconds: 30,
}

export function useUIConfig() {
  const { data } = useQuery({
    queryKey: ['ui-config'],
    queryFn: api.getUIConfig,
    staleTime: 5 * 60 * 1000,
    retry: false, // failing means the backend is down — fallbacks suffice
  })
  return data ?? FALLBACK
}
