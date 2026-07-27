import { useState, useCallback } from 'react'
import { OVERVIEW_RANGE_OPTIONS } from '@/components/shared/RangeSelector'

// useDashboardRange is the shared time-lens for the per-cluster
// dashboard sub-tabs (Capacity + Reliability). Both tabs answer
// questions about the SAME cluster at the SAME investigation
// altitude, so they share ONE range value — flipping tabs mid-
// investigation keeps the window instead of snapping back to the
// default (the friction this replaces).
//
// PERSISTENCE = sessionStorage, deliberately NOT localStorage:
//   - Survives tab switches AND drilling into a resource and back —
//     the whole point.
//   - Reverts to the 15m default on a FRESH browser session, so the
//     morning "what's happening now" scan always starts recent and
//     never silently re-runs an expensive 30d query because of a
//     capacity-planning session from last week.
// A permanent localStorage value would poison that daily default and
// make heavy ranges the per-visit baseline — see the range-persistence
// discussion. Overview has no selector (instantaneous snapshot), and
// the per-resource Monitor tabs stay independent (different altitude).

const STORAGE_KEY = 'kb-dashboard-range-minutes'
const DEFAULT_MINUTES = 15

const VALID_MINUTES = new Set(OVERVIEW_RANGE_OPTIONS.map((o) => o.minutes))

function readInitial(): number {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    if (raw != null) {
      const n = parseInt(raw, 10)
      // Guard against a stale value from an option that no longer
      // exists (range list changed between releases).
      if (VALID_MINUTES.has(n)) return n
    }
  } catch {
    // sessionStorage unavailable (private mode / SSR) — fall through.
  }
  return DEFAULT_MINUTES
}

export function useDashboardRange(): [number, (minutes: number) => void] {
  const [rangeMinutes, setRangeMinutesState] = useState(readInitial)

  const setRangeMinutes = useCallback((minutes: number) => {
    setRangeMinutesState(minutes)
    try {
      sessionStorage.setItem(STORAGE_KEY, String(minutes))
    } catch {
      // Persist is best-effort; the in-memory value still drives the
      // session even when storage is blocked.
    }
  }, [])

  return [rangeMinutes, setRangeMinutes]
}
