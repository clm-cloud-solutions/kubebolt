/**
 * shortToolName — the tool as an operator should read it, not as the wire
 * spells it.
 *
 * MCP names a tool `mcp__<server key>__<tool>`, and Autopilot now serves two
 * servers: reads under `kubebolt`, mutations under `kubebolt_actions`. They
 * had to split because two entries in the SDK's `mcpServers` cannot share a
 * key, and the key is what prefixes every name.
 *
 * This used to strip a hard-coded `mcp__kubebolt__`, so the day the actions
 * moved, every mutation started rendering raw —
 * `mcp__kubebolt_actions__verify_pods_ready` — and only inside the Executor,
 * because it is the one agent that calls them. Matching ANY server key means a
 * third one would not need a change here.
 */
export function shortToolName(toolName: string): string {
  return toolName.replace(/^mcp__[a-z0-9_]+?__/i, '')
}

// ── External (cloud) connector tools ─────────────────────────────────────
//
// A connector's tools reach the model under two spellings:
//
//   Kobi       mcp__<connectorId>__<local>        mcp__aws__call
//                                                  mcp__gcp__sqladmin_get_instance
//                                                  mcp__azure__monitor
//   Autopilot  mcp__kubebolt__cloud_<id>_<local>  mcp__kubebolt__cloud_aws_call
//
// Autopilot's come through KubeBolt's own MCP server, whose key is
// `kubebolt`, so the same prefix also carries every built-in read
// (mcp__kubebolt__get_pod_logs) and the actions server carries the mutations
// (mcp__kubebolt_actions__…). Neither is an external read: only the `cloud_`
// family under `kubebolt` is. The same family also turns up without the
// server prefix (`cloud_aws_call`, the name KubeBolt's MCP server gives it),
// and reads the same.

// Short names for tool labels, long ones for prose. Unknown ids (a connector
// added later) fall back to the id itself, capitalised.
const CONNECTOR_SHORT: Record<string, string> = { aws: 'AWS', gcp: 'GCP', azure: 'Azure' }
const CONNECTOR_LONG: Record<string, string> = { aws: 'AWS', gcp: 'Google Cloud', azure: 'Azure' }
// Ids the Autopilot form is tried against first: `cloud_<id>_<local>` is
// ambiguous if an id ever contains `_`, and these are the ones that exist.
const KNOWN_CONNECTORS = ['aws', 'gcp', 'azure']

export function connectorShortName(id: string): string {
  return CONNECTOR_SHORT[id] ?? capitalise(id)
}

export function connectorDisplayName(id: string): string {
  return CONNECTOR_LONG[id] ?? capitalise(id)
}

export interface ExternalTool {
  connectorId: string
  // Local tool name, as the connector's catalog spells it ("call",
  // "sqladmin_get_instance", "monitor").
  tool: string
  // What a tool card shows: "AWS · call", "GCP · sqladmin get instance".
  label: string
  consumer: 'kobi' | 'autopilot'
}

/**
 * externalTool recognises a cloud-connector tool name and says which
 * connector it belongs to, or returns null for everything else — KubeBolt's
 * own tools, the actions server, a plain name.
 */
export function externalTool(name: string): ExternalTool | null {
  if (name.startsWith('cloud_')) return autopilotCloudTool(name)
  const m = /^mcp__([a-z0-9_]+?)__(.+)$/i.exec(name)
  if (!m) return null
  const server = m[1].toLowerCase()
  const rest = m[2]

  if (server === 'kubebolt') return rest.startsWith('cloud_') ? autopilotCloudTool(rest) : null
  // Any other server under our own name is ours (the actions server today).
  if (server.startsWith('kubebolt')) return null

  // Older binders kept the provider's own prefix on the tool (AWS names its
  // tools `aws___call_aws`); drop it so the label does not say AWS twice.
  const own = `${server}___`
  const tool = rest.startsWith(own) && rest.length > own.length ? rest.slice(own.length) : rest
  return build(server, tool, 'kobi')
}

// `cloud_<connectorId>_<local>`, as KubeBolt's own MCP server names it.
function autopilotCloudTool(name: string): ExternalTool | null {
  const body = name.slice('cloud_'.length)
  const known = KNOWN_CONNECTORS.find((id) => body.startsWith(`${id}_`) && body.length > id.length + 1)
  if (known) return build(known, body.slice(known.length + 1), 'autopilot')
  const cut = body.indexOf('_')
  if (cut <= 0 || cut === body.length - 1) return null
  return build(body.slice(0, cut), body.slice(cut + 1), 'autopilot')
}

/**
 * toolLabel is a tool's name as a timeline shows it: a cloud read by its
 * provider ("AWS · call"), anything else without its MCP server prefix.
 */
export function toolLabel(name: string): string {
  return externalTool(name)?.label ?? shortToolName(name)
}

function build(connectorId: string, tool: string, consumer: 'kobi' | 'autopilot'): ExternalTool {
  return {
    connectorId,
    tool,
    label: `${connectorShortName(connectorId)} · ${tool.replace(/_+/g, ' ').trim()}`,
    consumer,
  }
}

function capitalise(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s
}
