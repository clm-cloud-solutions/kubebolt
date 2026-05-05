// Centralized prompt templates for contextual "Ask Copilot" triggers.
//
// Each trigger has a typed payload carrying the context visible in the UI
// at the moment of the click. buildTriggerPrompt renders it into a short
// user message — the LLM will call tools for more detail when needed.
//
// Keep prompts intentionally terse: pre-loading identifier + symptom is
// enough for ronda 0. Bloated prompts inflate every session for no gain.

export type CopilotTriggerType =
  | 'manual'
  | 'insight'
  | 'not_ready_resource'
  | 'warning_event'
  | 'flow_edge'
  | 'metric_anomaly'
  | 'resource_inquiry'
  | 'panel_inquiry'

export interface InsightTriggerPayload {
  type: 'insight'
  insight: {
    severity: string
    title: string
    message: string
    resource?: string   // kind (e.g. "Deployment")
    namespace?: string
    name?: string       // resource name when available (may be in `resource` field today)
    suggestion?: string
    lastSeen?: string
  }
}

export interface NotReadyResourceTriggerPayload {
  type: 'not_ready_resource'
  resource: {
    kind: string         // "Pod", "Deployment", "StatefulSet"
    namespace: string
    name: string
    status?: string
    // Free-form context. Keys are preserved in the prompt so the LLM
    // sees them as {key: value} pairs. Keep small — each line costs tokens.
    details?: Record<string, string | number | boolean>
  }
}

export interface WarningEventTriggerPayload {
  type: 'warning_event'
  event: {
    reason: string
    message: string
    object: string         // e.g. "Pod/foo" or "Deployment/bar"
    namespace?: string
    count?: number         // how many times it has repeated
    firstSeen?: string
    lastSeen?: string
  }
}

// FlowEdgeTriggerPayload — Cluster Map Traffic edges and the
// External Endpoint side panel. Same shape serves both: the
// external case leaves dstPod empty and fills dstIp/dstFqdn
// instead, which is how the underlying flow data is keyed too.
export interface FlowEdgeTriggerPayload {
  type: 'flow_edge'
  flow: {
    srcNamespace: string
    srcPod: string
    // Destination: either a pod (pod-to-pod) or an external
    // address (pod-to-outside). dstPod empty => external.
    dstNamespace?: string
    dstPod?: string
    dstIp?: string
    dstFqdn?: string
    verdict: string          // "forwarded" | "dropped"
    ratePerSec: number
    // Optional L7 enrichment when the edge has HTTP visibility.
    l7?: {
      requestsPerSec?: number
      statusClass?: Record<string, number>   // e.g. { ok: 42, server_err: 3 }
      avgLatencyMs?: number
    }
    // Aggregated callers — populated by the external endpoint
    // panel where one external destination has many sources. Left
    // empty for the per-edge case.
    callers?: Array<{ namespace: string; pod: string; forwardedRate: number; droppedRate: number }>
  }
}

// MetricAnomalyTriggerPayload — MetricChart header button and the
// node fleet charts. Sends just enough context for the LLM to
// reason about the curve: the PromQL, unit, visible range, and the
// live stats panel values (now/avg/max/min per series) so it
// doesn't have to re-query for simple interpretations.
export interface MetricAnomalyTriggerPayload {
  type: 'metric_anomaly'
  metric: {
    title: string
    query: string           // first query's PromQL; multi-query charts send the primary one
    unit?: string           // "cores" | "bytes" | "bytes/s" | "percent" | ...
    rangeLabel: string      // "5m" | "15m" | "1h" | ...
    series: Array<{
      name: string
      now?: number
      avg?: number
      max?: number
      min?: number
    }>
    // Optional reference lines breached during the range — gives
    // the LLM a hint that "limit was hit" without re-querying.
    referenceLines?: Array<{ label: string; value: number; breached?: boolean }>
  }
}

// ResourceInquiryTriggerPayload — the persistent "Ask Copilot"
// button on any resource detail header. The active tab tells the
// LLM what the user is probably looking at, so it can tailor its
// answer ("summarize errors" on Logs, "is this right-sized" on
// Monitor, etc.) without the user spelling it out.
export interface ResourceInquiryTriggerPayload {
  type: 'resource_inquiry'
  resource: {
    kind: string
    namespace: string
    name: string
    activeTab: string        // "overview" | "yaml" | "logs" | "monitor" | ...
    // Small freeform blob of known facts. Keep narrow — CRD YAML
    // bloats prompts. The agent can tool-call for more.
    summary?: Record<string, string | number | boolean>
  }
}

// PanelInquiryTriggerPayload — Capacity tab panels (Top Consumers,
// Right-sizing, Recent Deploys). The list-shaped panels don't fit
// metric_anomaly (they're not curves) or resource_inquiry (they're
// not a single resource). Each variant carries the rows visible to
// the user at click-time so the LLM can speak about exactly what's
// on screen instead of re-querying.
export type PanelInquiryKind =
  | 'top_consumers_cpu'
  | 'right_sizing'
  | 'recent_deploys'
  | 'top_workloads_traffic'
  | 'error_hotspots'
  | 'top_latency'
  | 'network_drops'

export interface PanelInquiryTriggerPayload {
  type: 'panel_inquiry'
  panel: PanelInquiryKind
  // Optional human-readable range / window label. Top Consumers
  // doesn't have one; Right-sizing carries "P95 over 7d"; Recent
  // Deploys carries the active range like "15m" or "1h".
  rangeLabel?: string
  // Each row is a free-form blob — keys are preserved verbatim in
  // the prompt. Cap at ~10 rows from the call site; the panel
  // already truncates its visible list and the LLM can tool-call
  // for more if it needs.
  rows: Array<Record<string, string | number>>
  // When the visible list is truncated from a larger total, this
  // tells the LLM there's more it can pull via tools.
  truncatedFromTotal?: number
}

export type CopilotTriggerPayload =
  | InsightTriggerPayload
  | NotReadyResourceTriggerPayload
  | WarningEventTriggerPayload
  | FlowEdgeTriggerPayload
  | MetricAnomalyTriggerPayload
  | ResourceInquiryTriggerPayload
  | PanelInquiryTriggerPayload

export function buildTriggerPrompt(payload: CopilotTriggerPayload): string {
  switch (payload.type) {
    case 'insight': {
      const i = payload.insight
      const lines = [
        `Diagnose this insight in detail and recommend a fix.`,
        ``,
        `Insight: ${i.severity} — ${i.title}`,
      ]
      if (i.namespace && (i.resource || i.name)) {
        const ref = i.name ?? i.resource
        lines.push(`Resource: ${i.namespace}/${ref}`)
      } else if (i.resource) {
        lines.push(`Resource: ${i.resource}`)
      }
      if (i.lastSeen) lines.push(`Last seen: ${i.lastSeen}`)
      lines.push(`Message: ${i.message}`)
      if (i.suggestion) lines.push(`Existing suggestion: ${i.suggestion}`)
      lines.push(``, `What's the root cause, and what should I do?`)
      return lines.join('\n')
    }
    case 'not_ready_resource': {
      const r = payload.resource
      const lines = [
        `Investigate this ${r.kind} and tell me what's wrong.`,
        ``,
        r.namespace ? `${r.kind}: ${r.namespace}/${r.name}` : `${r.kind}: ${r.name}`,
      ]
      if (r.status) lines.push(`Status: ${r.status}`)
      if (r.details) {
        for (const [k, v] of Object.entries(r.details)) {
          if (v === undefined || v === null || v === '') continue
          lines.push(`${k}: ${v}`)
        }
      }
      lines.push(``, `Explain what's happening and suggest actionable fixes.`)
      return lines.join('\n')
    }
    case 'warning_event': {
      const e = payload.event
      const lines = [
        `Explain this Kubernetes Warning event and its impact.`,
        ``,
        `Reason: ${e.reason}`,
        `Object: ${e.namespace ? e.namespace + '/' + e.object : e.object}`,
        `Message: ${e.message}`,
      ]
      if (typeof e.count === 'number') lines.push(`Count: ${e.count} occurrences`)
      if (e.firstSeen) lines.push(`First seen: ${e.firstSeen}`)
      if (e.lastSeen) lines.push(`Last seen: ${e.lastSeen}`)
      lines.push(
        ``,
        `Is this benign or something I need to act on? If actionable, how do I fix the root cause?`,
      )
      return lines.join('\n')
    }
    case 'flow_edge': {
      const f = payload.flow
      const src = `${f.srcNamespace}/${f.srcPod}`
      // External vs pod destination changes the phrasing — for an
      // external destination the "should this be happening at all"
      // question is more interesting than for in-cluster traffic.
      const isExternal = !f.dstPod
      const dst = isExternal
        ? (f.dstFqdn || f.dstIp || 'external')
        : `${f.dstNamespace ?? ''}/${f.dstPod}`

      const lines: string[] = []
      if (isExternal) {
        lines.push(`Investigate this egress traffic from my cluster to an external endpoint.`)
      } else if (f.verdict === 'dropped') {
        lines.push(`Investigate why these pod-to-pod packets are being dropped.`)
      } else {
        lines.push(`Diagnose this pod-to-pod traffic flow.`)
      }

      lines.push(
        ``,
        `Source: ${src}`,
        `Destination: ${dst}${f.dstFqdn && f.dstIp ? ` (${f.dstIp})` : ''}`,
        `Verdict: ${f.verdict}`,
        `Rate: ${f.ratePerSec.toFixed(2)} events/s`,
      )

      if (f.l7) {
        if (typeof f.l7.requestsPerSec === 'number') {
          lines.push(`HTTP rate: ${f.l7.requestsPerSec.toFixed(2)} req/s`)
        }
        if (typeof f.l7.avgLatencyMs === 'number') {
          lines.push(`Avg latency: ${f.l7.avgLatencyMs.toFixed(1)} ms`)
        }
        if (f.l7.statusClass) {
          const classes = Object.entries(f.l7.statusClass)
            .filter(([, v]) => v > 0)
            .map(([k, v]) => `${k}=${v}`)
            .join(', ')
          if (classes) lines.push(`Status classes: ${classes}`)
        }
      }

      if (f.callers && f.callers.length > 0) {
        lines.push(``, `Callers:`)
        for (const c of f.callers.slice(0, 10)) {
          const parts = [`  - ${c.namespace}/${c.pod}: ${c.forwardedRate.toFixed(2)} fwd/s`]
          if (c.droppedRate > 0) parts.push(`+ ${c.droppedRate.toFixed(2)} dropped/s`)
          lines.push(parts.join(' '))
        }
      }

      lines.push(``)
      if (isExternal) {
        lines.push(
          `Is this connection expected? What is this external host, and should any of the callers be reaching it?`,
        )
      } else if (f.verdict === 'dropped') {
        lines.push(
          `What's causing the drops — a NetworkPolicy, a CiliumNetworkPolicy, or something else? How do I confirm and fix it?`,
        )
      } else if (f.l7?.statusClass && (f.l7.statusClass.server_err || f.l7.statusClass.client_err)) {
        lines.push(
          `The errors are concerning — root cause the 5xx/4xx and tell me what to change.`,
        )
      } else {
        lines.push(`Summarize what this flow represents and flag anything unusual.`)
      }
      return lines.join('\n')
    }
    case 'metric_anomaly': {
      const m = payload.metric
      const lines = [
        `Interpret this metric chart and flag anything unusual.`,
        ``,
        `Chart: ${m.title}`,
        `Query: ${m.query}`,
        `Range: ${m.rangeLabel}`,
      ]
      if (m.unit) lines.push(`Unit: ${m.unit}`)

      if (m.series.length > 0) {
        lines.push(``, `Series (now / avg / max / min):`)
        for (const s of m.series.slice(0, 12)) {
          const parts: string[] = [`  - ${s.name}`]
          const stats: string[] = []
          if (typeof s.now === 'number') stats.push(`now=${formatNumber(s.now)}`)
          if (typeof s.avg === 'number') stats.push(`avg=${formatNumber(s.avg)}`)
          if (typeof s.max === 'number') stats.push(`max=${formatNumber(s.max)}`)
          if (typeof s.min === 'number') stats.push(`min=${formatNumber(s.min)}`)
          if (stats.length > 0) parts.push(stats.join(' · '))
          lines.push(parts.join(': '))
        }
      }

      if (m.referenceLines && m.referenceLines.length > 0) {
        lines.push(``, `Reference lines:`)
        for (const r of m.referenceLines) {
          const breached = r.breached ? ' (breached in range)' : ''
          lines.push(`  - ${r.label}: ${formatNumber(r.value)}${breached}`)
        }
      }

      lines.push(
        ``,
        `What story does this chart tell? Any anomalies, spikes, sustained pressure, or trends I should act on?`,
      )
      return lines.join('\n')
    }
    case 'panel_inquiry': {
      const p = payload
      // Question is panel-specific so the LLM knows which lens to
      // read the rows through. The rows themselves are dumb blobs —
      // the panel knows what it shows; we just preserve the keys
      // verbatim and let the LLM map them. Single-row clicks
      // (per-row Kobi) get a singular phrasing so the LLM knows to
      // focus on one workload instead of summarizing a list.
      const isSingle = p.rows.length === 1
      const questions: Record<
        PanelInquiryKind,
        { lead: string; close: string; singleLead?: string; singleClose?: string }
      > = {
        top_consumers_cpu: {
          lead: `Look at the top CPU consumers in my cluster and tell me what's running hot.`,
          close: `Anything here look anomalous, or is this just baseline load? Flag the workloads worth investigating.`,
          singleLead: `This workload is one of the heaviest CPU consumers in the cluster — explain what's going on.`,
          singleClose: `Is this load expected for what this workload does, and what should I check or change?`,
        },
        right_sizing: {
          lead: `Walk me through these right-sizing recommendations.`,
          close: `Prioritize by risk and tell me what to change. Which should I apply first, and where could a change cause an OOM or throttling?`,
          singleLead: `Explain this right-sizing recommendation in detail.`,
          singleClose: `What should I change, and what's the risk if I apply the suggestion? Walk me through how to roll it out safely.`,
        },
        recent_deploys: {
          lead: `Summarize the recent rollout activity in my cluster.`,
          close: `Was anything risky or unusual? Should I correlate with errors / anomalies in the same window?`,
          singleLead: `Tell me about this rollout.`,
          singleClose: `Was it routine, or worth investigating? Check whether it correlates with any errors, restarts, or metric anomalies in the same window.`,
        },
        top_workloads_traffic: {
          lead: `Walk me through the cluster's top HTTP workloads and flag anything that looks off.`,
          close: `Look at the request rates, error rates, and latency together — flag services with suspicious error patterns, latency outliers, or load shapes that don't fit what I'd expect for them.`,
          // Per-row variant intentionally absent: this panel doesn't
          // ship a per-row Kobi (the bar + chips + sparkline already
          // tell each row's story). Falls back to the multi-row lead.
        },
        error_hotspots: {
          lead: `Walk me through these HTTP error hot-spots.`,
          close: `Prioritize by risk — which one would wake me up at 3am, and what's the likely root cause? Remember: 4xx points at the caller, 5xx points at the receiver.`,
          singleLead: `Investigate this HTTP error hot-spot.`,
          singleClose: `What's the most likely root cause given the source, destination, and status class breakdown? Walk me through how to confirm and how to fix it.`,
        },
        top_latency: {
          lead: `Walk me through the cluster's slowest HTTP workloads.`,
          close: `Which of these latencies are concerning vs expected for their workload type, and what's most likely causing the slow ones (downstream dependency, GC, lock contention, cold starts)? Note: these are average latencies — outliers can pull the avg, but consistent high avg points at a real problem.`,
          // No per-row variant — TopLatencyWorkloads uses
          // panel-level Kobi only.
        },
        network_drops: {
          lead: `Walk me through these dropped network flows.`,
          close: `Most dropped flows in a Cilium cluster come from NetworkPolicies blocking traffic — but they can also be connection refused, host firewall, or pod restarting. Tell me which of these look like NetworkPolicy issues vs other causes, and how to confirm.`,
          singleLead: `Investigate this dropped network flow.`,
          singleClose: `What's most likely blocking this traffic — a NetworkPolicy, a CiliumNetworkPolicy, the destination pod being down, or something else? Walk me through how to confirm and remediate.`,
        },
      }
      const q = questions[p.panel]
      const lead = isSingle ? q.singleLead ?? q.lead : q.lead
      const close = isSingle ? q.singleClose ?? q.close : q.close
      const lines: string[] = [lead, ``]
      if (p.rangeLabel) lines.push(`Range: ${p.rangeLabel}`)
      if (typeof p.truncatedFromTotal === 'number' && p.truncatedFromTotal > p.rows.length) {
        lines.push(
          `Showing ${p.rows.length} of ${p.truncatedFromTotal} rows (the rest are available via tools).`,
        )
      }
      if (p.rows.length > 0) {
        lines.push(``, `Rows:`)
        for (const row of p.rows) {
          const parts: string[] = []
          for (const [k, v] of Object.entries(row)) {
            if (v === undefined || v === null || v === '') continue
            parts.push(`${k}=${v}`)
          }
          if (parts.length > 0) lines.push(`  - ${parts.join(' · ')}`)
        }
      }
      lines.push(``, close)
      return lines.join('\n')
    }
    case 'resource_inquiry': {
      const r = payload.resource
      // Active tab shapes the question — opening Copilot from Logs
      // implies "help me read these logs", from Monitor implies
      // "look at these metrics", etc. Avoids the user having to
      // say it in words.
      const tabPrompts: Record<string, string> = {
        overview: `Explain what this resource does, its dependencies, and its current health.`,
        yaml: `Review this resource's spec — flag anything non-standard or potentially problematic.`,
        logs: `Summarize the recent log activity. Any errors or patterns I should act on?`,
        terminal: `Offer guidance on what's commonly useful to run inside this container.`,
        monitor: `Interpret the current metrics. Is this resource healthy and right-sized?`,
        events: `Are any of the recent events concerning, or are they routine?`,
        related: `Summarize the relationships here and what they mean for this resource's reliability.`,
        history: `Walk me through the recent revision history — any rollbacks or noteworthy changes?`,
        files: `Help me explore the filesystem — what should I check first?`,
        pods: `Summarize the health of the pods owned by this workload.`,
      }
      const question = tabPrompts[r.activeTab] ?? `Help me understand this resource.`

      const lines = [
        question,
        ``,
        `${r.kind}: ${r.namespace}/${r.name}`,
        `Current tab: ${r.activeTab}`,
      ]
      if (r.summary) {
        for (const [k, v] of Object.entries(r.summary)) {
          if (v === undefined || v === null || v === '') continue
          lines.push(`${k}: ${v}`)
        }
      }
      return lines.join('\n')
    }
  }
}

// formatNumber keeps the prompt compact while staying readable —
// big numbers get compact notation, small ones get two decimals.
function formatNumber(v: number): string {
  if (!Number.isFinite(v)) return String(v)
  const abs = Math.abs(v)
  if (abs >= 1e9) return (v / 1e9).toFixed(2) + 'G'
  if (abs >= 1e6) return (v / 1e6).toFixed(2) + 'M'
  if (abs >= 1e3) return (v / 1e3).toFixed(2) + 'k'
  if (abs < 0.01 && abs !== 0) return v.toExponential(1)
  return Number.isInteger(v) ? v.toString() : v.toFixed(2)
}
