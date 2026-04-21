// Centralized prompt templates for contextual "Ask Copilot" triggers.
//
// Each trigger has a typed payload carrying the context visible in the UI
// at the moment of the click. buildTriggerPrompt renders it into a short
// user message — the LLM will call tools for more detail when needed.
//
// Keep prompts intentionally terse: pre-loading identifier + symptom is
// enough for ronda 0. Bloated prompts inflate every session for no gain.

export type CopilotTriggerType = 'manual' | 'insight' | 'not_ready_resource'

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

export type CopilotTriggerPayload =
  | InsightTriggerPayload
  | NotReadyResourceTriggerPayload

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
        `${r.kind}: ${r.namespace}/${r.name}`,
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
  }
}
