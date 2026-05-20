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
// Detection probes:
//   - L4 (this hook): `count(pod_flow_events_total{source="hubble"})`.
//     Emitted by every Hubble connection — drops, allowed flows,
//     forwards, the lot. If this is zero, Hubble isn't shipping
//     anything at all.
//   - L7 (companion): `count(pod_flow_http_requests_total{source="hubble"})`.
//     Emitted only when Cilium's L7 proxy is enabled and seeing
//     traffic. Zero on managed DPv2 (Cilium running but L7 off),
//     zero on a fresh cluster with no HTTP traffic yet, and zero
//     when Hubble itself is absent.
//
// Both cached 60s with `retry: false`. Hubble install state is a
// deliberate ops action so minute-resolution is plenty, and a
// failing query shouldn't spam VM.

interface HubbleDetectionResult {
  available: boolean
  isLoading: boolean
}

// promScalarPresent reads a `count(...)` response and returns true
// iff the result vector has at least one row with a positive value.
// Empty vector → metric doesn't exist (count returns no rows).
// Zero scalar → metric exists but no series — treat both as
// "not available" to avoid false positives on stale series-without-samples.
function promScalarPresent(data: unknown): boolean {
  const result = (
    data as { data?: { result?: { value?: [number, string] }[] } } | undefined
  )?.data?.result
  if (!result || result.length === 0) return false
  const raw = result[0]?.value?.[1]
  if (raw === undefined || raw === null) return false
  return parseFloat(raw) > 0
}

export function useHubbleAvailable(): HubbleDetectionResult {
  const { data, isLoading } = useQuery({
    queryKey: ['hubble-available', 'l4'],
    queryFn: () =>
      api.queryMetrics({ query: `count(pod_flow_events_total{source="hubble"})` }),
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry: false,
  })

  return { available: promScalarPresent(data), isLoading }
}

export function useHubbleL7Available(): HubbleDetectionResult {
  const { data, isLoading } = useQuery({
    queryKey: ['hubble-available', 'l7'],
    queryFn: () =>
      api.queryMetrics({ query: `count(pod_flow_http_requests_total{source="hubble"})` }),
    staleTime: 60_000,
    refetchInterval: 60_000,
    retry: false,
  })

  return { available: promScalarPresent(data), isLoading }
}
