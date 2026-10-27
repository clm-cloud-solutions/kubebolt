import { useQuery } from '@tanstack/react-query'
import { api } from '@/services/api'

// Two-level Hubble detection.
//
// `useHubbleAvailable` (this hook) probes whether **any** Hubble
// flow data is shipping into VictoriaMetrics — L3/L4 visibility,
// the baseline you get from a working Hubble Relay connection.
// Drives whether the Reliability sub-tab is surfaced at all.
//
// `useHubbleL7Available` (companion below) probes whether **L7
// HTTP metrics specifically** are flowing — the richer signal the
// Reliability page is designed around. Used inside the Reliability
// page to decide between rendering panels vs an L7-unavailable
// explanation.
//
// The split exists because GKE managed Dataplane V2 emits L3/L4
// flows but NOT L7 — Google hasn't exposed the L7 proxy toggle
// through their managed cluster API. Before this split, the
// Reliability tab simply disappeared on managed DPv2, leaving the
// operator with no signal as to WHY. Now the tab stays visible
// (Hubble is there!) but the page tells them L7 isn't available
// on this cluster — actionable copy that prevents the
// "is-Hubble-broken?" false positive.
//
// Both probes look at the dashboard's SELECTED RANGE, not at the
// last few minutes. An instant `count(...)` answered "now" only, so
// a gap in the feed (the backend down for a night, the agent's
// buffer dropping samples) read as "this cluster has no L7" while
// the range on screen held days of HTTP data — and the page blamed
// the Cilium config. Each probe returns the newest sample in the
// range instead (`tlast_over_time`, MetricsQL), so the page can tell
// "never seen in this range" from "not seen since <time>":
//   - L4 (this hook): `pod_flow_events_total{source="hubble"}`.
//     Emitted by every Hubble connection — drops, allowed flows,
//     forwards, the lot.
//   - L7 (companion): `pod_flow_http_requests_total{source="hubble"}`.
//     Emitted only when Cilium's L7 proxy is enabled and seeing
//     traffic. Absent on managed DPv2 (Cilium running but L7 off),
//     on a fresh cluster with no HTTP traffic yet, and when Hubble
//     itself is absent.
//
// Both cached 60s with `retry: false`. Hubble install state is a
// deliberate ops action so minute-resolution is plenty, and a
// failing query shouldn't spam VM.

interface HubbleDetectionResult {
  available: boolean
  // When the newest sample in the range was taken (unix seconds), or
  // null when the range holds none.
  lastSeen: number | null
  isLoading: boolean
}

// The agent ships Hubble series every 30s. Past this, the newest
// sample is no longer "now": the page says since when there is none.
export const HUBBLE_STALE_AFTER_S = 5 * 60

export function isHubbleStale(lastSeen: number | null, nowS = Date.now() / 1000): boolean {
  return lastSeen !== null && nowS - lastSeen > HUBBLE_STALE_AFTER_S
}

// lastSampleTime reads a `max(tlast_over_time(...))` response: the
// timestamp of the newest sample, or null when the result is empty.
export function lastSampleTime(data: unknown): number | null {
  const result = (
    data as { data?: { result?: { value?: [number, string] }[] } } | undefined
  )?.data?.result
  if (!result || result.length === 0) return null
  const raw = result[0]?.value?.[1]
  if (raw === undefined || raw === null) return null
  const t = parseFloat(raw)
  return Number.isFinite(t) && t > 0 ? t : null
}

function useLastHubbleSample(
  level: 'l4' | 'l7',
  metric: string,
  rangeMinutes: number,
): HubbleDetectionResult {
  const { data, isLoading } = useQuery({
    queryKey: ['hubble-available', level, rangeMinutes],
    queryFn: () =>
      api.queryMetrics({
        query: `max(tlast_over_time(${metric}{source="hubble"}[${rangeMinutes}m]))`,
      }),
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry: false,
  })

  const lastSeen = lastSampleTime(data)
  return { available: lastSeen !== null, lastSeen, isLoading }
}

export function useHubbleAvailable(rangeMinutes: number): HubbleDetectionResult {
  return useLastHubbleSample('l4', 'pod_flow_events_total', rangeMinutes)
}

export function useHubbleL7Available(rangeMinutes: number): HubbleDetectionResult {
  return useLastHubbleSample('l7', 'pod_flow_http_requests_total', rangeMinutes)
}
