import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// useUpdateCheck polls the backend's /update-check endpoint, which
// itself caches a GitHub releases lookup for 6h. Frontend staleTime
// of 1h means at most one fetch per hour per session, with the
// backend collapsing those into one real GitHub call per 6h regardless
// of how many sessions ask.
//
// Returns `null` while loading or on error so the chip simply doesn't
// render — there's no failure UX for "couldn't reach the backend's
// own /update-check," only "no update to surface."

export interface UpdateCheckResponse {
  enabled: boolean
  currentVersion?: string
  latestVersion?: string
  isUpdateAvailable: boolean
  releaseUrl?: string
  releaseName?: string
  publishedAt?: string
}

export function useUpdateCheck() {
  const { data } = useQuery({
    queryKey: ['update-check'],
    queryFn: api.getUpdateCheck,
    staleTime: 60 * 60 * 1000, // 1h — backend caches 6h, no point asking faster
    refetchOnWindowFocus: false,
    retry: false,
  })
  return data ?? null
}
