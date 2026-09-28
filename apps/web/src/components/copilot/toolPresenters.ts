// Per-tool presentation logic for the chat panel's ToolCallCard.
//
// The card no longer shows raw tool output (the model already summarizes it
// in its prose response, and the JSON dump added more noise than signal).
// Instead it shows what's deterministically derivable from the tool's INPUT:
//
//   - summary: a short chip rendered next to the tool name in the collapsed
//     header, so the operator sees Kobi's intent at a glance ("ns=demo · 1h
//     window") without expanding.
//   - command: the kubectl / PromQL equivalent of the same query, with a
//     copy button on the expanded view, so the operator can reproduce in
//     their own terminal — bridges Kobi's autonomy back to the workflow
//     they already know.
//   - inputLines: the full set of arguments the model passed, rendered as
//     key/value pairs. Catches "Kobi queried the wrong thing" (wrong
//     namespace, wrong window, wrong grep) before its conclusions propagate.
//
// Adding a new tool: write a present<ToolName> function below and wire it
// into the switch. Tools without an entry fall back to presentGeneric which
// extracts the input into key/value pairs and skips the kubectl section.

export interface ToolPresentation {
  // Short string shown inline with the tool name when the card is collapsed.
  // Empty string = no chip (the tool name alone is the entire header).
  summary: string
  // Best-effort kubectl/PromQL equivalent. Empty string = no CLI mapping
  // available (e.g. for get_kubebolt_docs). The "Equivalent" section of
  // the expanded card hides itself when this is empty.
  command: string
  // Ordered list of input arguments to render as a key/value table in the
  // expanded "Input" section. Empty list = no Input section rendered.
  inputLines: Array<{ key: string; value: string }>
  // SPA route the operator can follow to inspect the same data in the
  // KubeBolt UI directly. Undefined = no obvious in-product target.
  // Resolved client-side via react-router (not an external <a> — preserves
  // SPA navigation, auth, and state).
  link?: { href: string; label: string }
}

export function presentTool(name: string, input?: Record<string, unknown>): ToolPresentation {
  const i = input ?? {}
  switch (name) {
    case 'get_pod_logs':         return presentGetPodLogs(i)
    case 'get_events':           return presentGetEvents(i)
    case 'get_resource_detail':  return presentResourceOp('get', i)
    case 'get_resource_describe':return presentResourceOp('describe', i)
    case 'get_resource_yaml':    return presentResourceOp('get-yaml', i)
    case 'list_resources':       return presentListResources(i)
    case 'get_workload_history': return presentWorkloadHistory(i)
    case 'get_cluster_overview': return presentClusterOverview()
    case 'get_workload_pods':    return presentWorkloadPods(i)
    case 'get_cronjob_jobs':     return presentCronjobJobs(i)
    case 'get_topology':         return presentTopology()
    case 'get_insights':         return presentInsights()
    case 'get_operational_episodes': return presentOperationalEpisodes(i)
    case 'get_findings':        return presentFindings(i)
    case 'get_finding_workloads': return presentFindingWorkloads(i)
    case 'get_finding_detail':  return presentFindingDetail(i)
    case 'get_runtime_events':  return presentRuntimeEvents(i)
    case 'get_coverage':        return presentCoverage()
    case 'get_right_sizing':    return presentRightSizing(i)
    case 'query_metrics':       return presentQueryMetrics(i)
    case 'get_recent_deploys':  return presentRecentDeploys(i)
    case 'get_fleet_summary':  return presentFleetSummary()
    case 'get_insight_episodes': return presentInsightEpisodes(i)
    case 'get_insight_episode':  return presentInsightEpisode(i)
    case 'search_resources':     return presentSearch(i)
    case 'get_permissions':      return presentPermissions()
    case 'list_clusters':        return presentListClusters()
    case 'get_kubebolt_docs':    return presentDocs(i)
    default:                     return presentGeneric(i)
  }
}

// Path to a resource detail page with an optional tab querystring.
// The detail route is /:type/:namespace/:name?tab=<tab>; cluster-scoped
// resources use "_" as the namespace placeholder per the existing route.
function detailPath(type: string, namespace: string, name: string, tab?: string): string {
  const ns = namespace || '_'
  const base = `/${encodeURIComponent(type)}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`
  return tab ? `${base}?tab=${tab}` : base
}

// Resource types Kobi can describe via the API but that don't have a
// dedicated KubeBolt UI page yet (no list route, no detail route, no
// sidebar entry). Mirrors the "describe-only" tier in SPEC §3.2.1.
//
// Keeping this list here lets us suppress the "Open in UI" link on tool
// cards for these types — without it, the link generates a broken route
// (`/resourcequotas/...` for example) that falls through to a 404 or a
// catch-all and confuses the operator.
//
// When a type graduates to full UI support (informer + list page + sidebar
// + route per the SPEC checklist), REMOVE it from this set so the link
// reappears automatically.
const TYPES_WITHOUT_UI_PAGE = new Set<string>([
  'resourcequotas',
  'limitranges',
  'serviceaccounts',
  'networkpolicies',
  'poddisruptionbudgets',
  'priorityclasses',
  'ingressclasses',
])

// True when KubeBolt has neither a list page nor a detail page for the
// given resource type — so any in-product link would land on a dead route.
function hasUIPage(type: string): boolean {
  return !TYPES_WITHOUT_UI_PAGE.has(type)
}

function presentGetPodLogs(i: Record<string, unknown>): ToolPresentation {
  const ns = str(i.namespace)
  const name = str(i.name)
  const container = str(i.container)
  const previous = i.previous === true
  const sinceTime = str(i.sinceTime)
  const endTime = str(i.endTime)
  const since = str(i.since)
  const tailLines = i.tailLines ? Number(i.tailLines) : undefined
  const grep = str(i.grep)
  const timestamps = i.timestamps === true

  // ── Summary chip ────────────────────────────────────────────
  const parts: string[] = []
  if (ns && name) parts.push(`${ns}/${name}`)
  if (previous) parts.push('previous')
  else if (sinceTime && endTime) parts.push(`${shortTime(sinceTime)}–${shortTime(endTime)}`)
  else if (sinceTime) parts.push(`since ${shortTime(sinceTime)}`)
  else if (since) parts.push(`last ${since}`)
  if (grep) parts.push('filtered')
  const summary = parts.join(' · ')

  // ── kubectl equivalent ──────────────────────────────────────
  let cmd = `kubectl logs -n ${ns} ${name}`
  if (container) cmd += ` -c ${container}`
  if (previous) cmd += ' --previous'
  if (sinceTime) cmd += ` --since-time=${sinceTime}`
  else if (since) cmd += ` --since=${since}`
  if (tailLines) cmd += ` --tail=${tailLines}`
  if (timestamps || endTime) cmd += ' --timestamps'
  // Kubelet doesn't support endTime natively — pipe to awk for the upper bound.
  if (endTime) cmd += ` | awk '$1 <= "${endTime}"'`
  if (grep) cmd += ` | grep -E ${shellQuote(grep)}`

  return {
    summary,
    command: cmd,
    inputLines: makeInputLines(i),
    link: ns && name ? { href: detailPath('pods', ns, name, 'logs'), label: 'Open Logs' } : undefined,
  }
}

function presentGetEvents(i: Record<string, unknown>): ToolPresentation {
  const ns = str(i.namespace)
  const involvedKind = str(i.involvedKind)
  const involvedName = str(i.involvedName)
  const limit = i.limit ? Number(i.limit) : undefined

  let summary = ns || 'all namespaces'
  if (involvedKind && involvedName) summary += ` · ${involvedKind}/${involvedName}`
  else if (involvedKind) summary += ` · ${involvedKind}`

  let cmd = `kubectl get events${ns ? ` -n ${ns}` : ' --all-namespaces'} --sort-by=.lastTimestamp`
  const selectors: string[] = []
  if (involvedKind) selectors.push(`involvedObject.kind=${involvedKind}`)
  if (involvedName) selectors.push(`involvedObject.name=${involvedName}`)
  if (selectors.length > 0) cmd += ` --field-selector ${selectors.join(',')}`
  if (limit) cmd += ` | head -n ${limit + 1}`

  // When the query targets a specific resource that has a UI page,
  // deep-link to that resource's Events tab. Otherwise fall back to the
  // global /events page (which exists for all involved kinds).
  const involvedType = involvedKind ? involvedKind.toLowerCase() + 's' : ''
  const link = involvedKind && involvedName && hasUIPage(involvedType)
    ? { href: detailPath(involvedType, ns, involvedName, 'events'), label: 'Open Events' }
    : { href: '/events', label: 'Open Events' }

  return { summary, command: cmd, inputLines: makeInputLines(i), link }
}

function presentResourceOp(op: 'get' | 'describe' | 'get-yaml', i: Record<string, unknown>): ToolPresentation {
  const type = str(i.type)
  const ns = str(i.namespace)
  const name = str(i.name)
  const summary = ns ? `${type}/${ns}/${name}` : `${type}/${name}`

  let cmd: string
  if (op === 'describe') {
    cmd = `kubectl describe ${type} ${name}${ns ? ` -n ${ns}` : ''}`
  } else if (op === 'get-yaml') {
    cmd = `kubectl get ${type} ${name}${ns ? ` -n ${ns}` : ''} -o yaml`
  } else {
    cmd = `kubectl get ${type} ${name}${ns ? ` -n ${ns}` : ''} -o json`
  }

  // Deep-link straight into the YAML tab when that's what was queried; the
  // generic detail/describe ops land on Overview. Suppress the link for
  // describe-only types (no UI page exists yet — see SPEC §3.2.1).
  const tab = op === 'get-yaml' ? 'yaml' : undefined
  const label = op === 'get-yaml' ? 'Open YAML' : 'Open in UI'

  return {
    summary,
    command: cmd,
    inputLines: makeInputLines(i),
    link: type && name && hasUIPage(type)
      ? { href: detailPath(type, ns, name, tab), label }
      : undefined,
  }
}

function presentListResources(i: Record<string, unknown>): ToolPresentation {
  const type = str(i.type)
  const ns = str(i.namespace)
  const search = str(i.search)
  const summary = ns ? `${type} in ${ns}${search ? ` · "${search}"` : ''}` : `${type} (all ns)${search ? ` · "${search}"` : ''}`

  let cmd = `kubectl get ${type}${ns ? ` -n ${ns}` : ' --all-namespaces'}`
  if (search) cmd += ` # filtered client-side by name match: "${search}"`

  return {
    summary,
    command: cmd,
    inputLines: makeInputLines(i),
    link: type && hasUIPage(type)
      ? { href: `/${encodeURIComponent(type)}`, label: `Open ${type}` }
      : undefined,
  }
}

function presentWorkloadHistory(i: Record<string, unknown>): ToolPresentation {
  const type = str(i.type) || 'deployment'
  const ns = str(i.namespace)
  const name = str(i.name)
  const summary = ns ? `${type}/${ns}/${name}` : `${type}/${name}`
  const cmd = `kubectl rollout history ${type}/${name}${ns ? ` -n ${ns}` : ''}`
  return {
    summary,
    command: cmd,
    inputLines: makeInputLines(i),
    link: name ? { href: detailPath(type, ns, name, 'history'), label: 'Open History' } : undefined,
  }
}

function presentClusterOverview(): ToolPresentation {
  // No inputs — the tool inspects the connected cluster as a whole. Provide
  // a kubectl approximation that surfaces the same set of facts the
  // KubeBolt overview aggregates server-side, so the operator can reproduce.
  const command = [
    'kubectl cluster-info',
    'kubectl get nodes -o wide',
    'kubectl get pods --all-namespaces --field-selector=status.phase!=Running',
    'kubectl top nodes 2>/dev/null',
  ].join('\n')
  return {
    summary: 'connected cluster',
    command,
    inputLines: [],
    link: { href: '/', label: 'Open Overview' },
  }
}

function presentWorkloadPods(i: Record<string, unknown>): ToolPresentation {
  const type = str(i.type)
  const ns = str(i.namespace)
  const name = str(i.name)
  const summary = `${type}/${ns}/${name}`
  // The selector lives inside the workload spec, so a faithful one-liner
  // requires resolving it first. Show a copy-pasteable two-step that
  // always works, with the helper command commented for clarity.
  const command = [
    `# Resolve the selector for the workload, then list pods that match it.`,
    `selector=$(kubectl get ${type} ${name} -n ${ns} -o jsonpath='{.spec.selector.matchLabels}' \\`,
    `  | jq -r 'to_entries | map("\\(.key)=\\(.value)") | join(",")')`,
    `kubectl get pods -n ${ns} -l "$selector"`,
  ].join('\n')
  return {
    summary,
    command,
    inputLines: makeInputLines(i),
    link: type && name ? { href: detailPath(type, ns, name, 'pods'), label: 'Open Pods' } : undefined,
  }
}

function presentCronjobJobs(i: Record<string, unknown>): ToolPresentation {
  const ns = str(i.namespace)
  const name = str(i.name)
  const summary = `${ns}/${name}`
  const command =
    `kubectl get jobs -n ${ns} -o json \\\n` +
    `  | jq '.items[] | select(.metadata.ownerReferences[]?.name=="${name}") | .metadata.name'`
  return { summary, command, inputLines: makeInputLines(i) }
}

function presentTopology(): ToolPresentation {
  // KubeBolt-specific aggregation — no single kubectl equivalent. The link
  // alone is what makes this card actionable; the chip carries the intent.
  return {
    summary: 'cluster-wide graph',
    command: '',
    inputLines: [],
    link: { href: '/map', label: 'Open Cluster Map' },
  }
}

function presentInsights(): ToolPresentation {
  return {
    summary: 'active insights',
    command: '',
    inputLines: [],
    link: { href: '/insights', label: 'Open Insights' },
  }
}

function presentFleetSummary(): ToolPresentation {
  // The only tool that is not about the selected cluster, so the chip says so.
  return {
    summary: 'every cluster',
    command: '',
    inputLines: [],
    link: { href: '/fleet', label: 'Open Fleet' },
  }
}

function presentFindings(i: Record<string, unknown>): ToolPresentation {
  // With no facet the tool returns a posture, not rows — the chip should say
  // which of the two the operator is looking at.
  const facets = ['severity', 'source', 'kind', 'image', 'resource']
    .map((k) => str(i[k]))
    .filter(Boolean)
  const bits = facets.length > 0 ? facets : ['posture']
  if (str(i.cluster) === 'all') bits.push('all clusters')
  if (str(i.status) === 'resolved') bits.push('resolved')
  return {
    summary: bits.join(' · '),
    command: '',
    inputLines: makeInputLines(i),
    link: { href: '/security', label: 'Open Security' },
  }
}

// The Security page keeps one route per lens; the link lands on the lens Kobi
// ranked, so the operator checks the same list rather than the default tab.
const SECURITY_LENS_ROUTES: Record<string, string> = {
  vulnerability: '/security',
  configuration: '/security/configuration',
  rbac: '/security/permissions',
  compliance: '/security/compliance',
}

function presentFindingWorkloads(i: Record<string, unknown>): ToolPresentation {
  const group = str(i.group)
  const bits = [group ? `${group} by workload` : 'by workload']
  for (const k of ['severity', 'kind']) {
    const v = str(i[k])
    if (v) bits.push(v)
  }
  if (str(i.cluster) === 'all') bits.push('all clusters')
  if (str(i.status) === 'resolved') bits.push('resolved')
  return {
    summary: bits.join(' · '),
    command: '',
    inputLines: makeInputLines(i),
    link: { href: SECURITY_LENS_ROUTES[group] ?? '/security', label: 'Open Security' },
  }
}

function presentFindingDetail(i: Record<string, unknown>): ToolPresentation {
  // A live re-read of the scanner, not a kubectl call the operator could
  // repeat, so no Equivalent section.
  const fp = str(i.fingerprint)
  return {
    summary: fp ? `finding ${fp.slice(0, 8)}` : 'one finding',
    command: '',
    inputLines: makeInputLines(i),
    link: { href: '/security', label: 'Open Security' },
  }
}

function presentRightSizing(i: Record<string, unknown>): ToolPresentation {
  const bits = ['7d P95']
  for (const k of ['namespace', 'severity']) {
    const v = str(i[k])
    if (v) bits.push(v)
  }
  return {
    summary: bits.join(' · '),
    command: '',
    inputLines: makeInputLines(i),
    link: { href: '/capacity', label: 'Open Capacity' },
  }
}

function presentCoverage(): ToolPresentation {
  return {
    summary: 'what KubeBolt can see',
    command: '',
    inputLines: [],
    link: { href: '/clusters', label: 'Open Clusters' },
  }
}

function presentQueryMetrics(i: Record<string, unknown>): ToolPresentation {
  // The query IS the input worth seeing; the chip carries its window.
  const range = str(i.range)
  const query = str(i.query)
  return {
    summary: range ? `PromQL · last ${range}` : 'PromQL · now',
    command: query,
    inputLines: makeInputLines(i),
  }
}

function presentRuntimeEvents(i: Record<string, unknown>): ToolPresentation {
  const hours = Number(i.sinceHours)
  const bits = [Number.isFinite(hours) && hours > 0 ? `last ${hours}h` : 'last 24h']
  for (const k of ['priority', 'namespace', 'pod']) {
    const v = str(i[k])
    if (v) bits.push(v)
  }
  if (str(i.cluster) === 'all') bits.push('all clusters')
  return {
    summary: bits.join(' · '),
    command: '',
    inputLines: makeInputLines(i),
    link: { href: '/security/runtime', label: 'Open Runtime' },
  }
}

function presentRecentDeploys(i: Record<string, unknown>): ToolPresentation {
  // The Capacity dashboard is where the same rollouts are drawn as markers
  // on the trends and listed under Recent Deploys.
  const hours = Number(i.sinceHours)
  const bits = [Number.isFinite(hours) && hours > 0 ? `last ${hours}h` : 'last 24h']
  const ns = str(i.namespace)
  const name = str(i.name)
  if (ns || name) bits.push([ns, name].filter(Boolean).join('/'))
  return {
    summary: bits.join(' · '),
    command: ns ? `kubectl get rs -n ${ns} --sort-by=.metadata.creationTimestamp` : 'kubectl get rs -A --sort-by=.metadata.creationTimestamp',
    inputLines: makeInputLines(i),
    link: { href: '/capacity', label: 'Open Capacity' },
  }
}

function presentInsightEpisodes(i: Record<string, unknown>): ToolPresentation {
  // The chips that matter are the ones that change what came back: the window,
  // the scope, and any filter. "all clusters" especially — it is the only way
  // to reach a cluster that is gone, and worth seeing without expanding.
  const hours = Number(i.sinceHours)
  const bits = [Number.isFinite(hours) && hours > 0 ? `last ${hours}h` : 'last 24h']
  if (str(i.cluster) === 'all') bits.push('all clusters')
  else if (str(i.cluster)) bits.push(str(i.cluster))
  if (str(i.status)) bits.push(str(i.status))
  if (str(i.severity)) bits.push(str(i.severity))
  if (str(i.rule)) bits.push(str(i.rule))
  return {
    summary: bits.join(' · '),
    command: '',
    inputLines: makeInputLines(i),
    link: { href: '/insights?view=history', label: 'Open Insight History' },
  }
}

function presentInsightEpisode(i: Record<string, unknown>): ToolPresentation {
  // Deep-links to the very episode Kobi read, so the operator can check the
  // timeline it is describing rather than take its word for it.
  const id = str(i.id)
  return {
    summary: id ? `episode ${id.slice(0, 8)}` : 'one episode',
    command: '',
    inputLines: makeInputLines(i),
    link: id ? { href: `/insights/episodes/${encodeURIComponent(id)}`, label: 'Open episode' } : undefined,
  }
}

function presentOperationalEpisodes(i: Record<string, unknown>): ToolPresentation {
  // Bursts are a KubeBolt aggregation over the episode store — no kubectl
  // equivalent. The link lands on the Bursts view with the SAME window Kobi
  // asked for, so the operator checks the answer against the data rather than
  // against a default range. Only the windows the view offers as presets can
  // be carried; anything else opens on the default rather than inventing a
  // range the page would silently ignore.
  const hours = Number(i.sinceHours)
  const valid = Number.isFinite(hours) && hours > 0
  const window = valid ? `last ${hours}h` : 'last 24h'
  const range = !valid || hours === 24 ? '' : hours === 168 ? '7d' : hours === 720 ? '30d' : ''
  return {
    summary: `${window} · operational bursts`,
    command: '',
    inputLines: makeInputLines(i),
    link: {
      href: range ? `/insights?view=bursts&range=${range}` : '/insights?view=bursts',
      label: 'Open Bursts',
    },
  }
}

function presentSearch(i: Record<string, unknown>): ToolPresentation {
  const q = str(i.query)
  const summary = q ? `"${q}"` : ''
  // KubeBolt search spans 16 resource types with name-substring matching;
  // closest kubectl is a noisy grep across `get all`, useful as a reminder
  // that this is a UI feature without a clean CLI parallel.
  const command = q
    ? `# KubeBolt server-side search across pods, deployments, services, etc.\n` +
      `kubectl get all --all-namespaces -o name | grep -i ${shellQuote(q)}`
    : ''
  return { summary, command, inputLines: makeInputLines(i) }
}

function presentPermissions(): ToolPresentation {
  return { summary: 'current ServiceAccount', command: 'kubectl auth can-i --list', inputLines: [] }
}

function presentListClusters(): ToolPresentation {
  return {
    summary: 'registered contexts',
    command: 'kubectl config get-contexts',
    inputLines: [],
    link: { href: '/clusters', label: 'Open Clusters' },
  }
}

function presentDocs(i: Record<string, unknown>): ToolPresentation {
  const topic = str(i.topic)
  // KubeBolt-internal docs lookup — no CLI equivalent.
  return { summary: topic ? `topic: ${topic}` : 'KubeBolt docs', command: '', inputLines: makeInputLines(i) }
}

function presentGeneric(i: Record<string, unknown>): ToolPresentation {
  // Pull up to two non-empty string args for a hint chip; skip noise like
  // empty strings, false booleans, zero numbers.
  const meaningfulPairs = Object.entries(i)
    .filter(([, v]) => v !== '' && v !== false && v !== 0 && v != null)
    .slice(0, 2)
  const summary = meaningfulPairs
    .map(([k, v]) => `${k}=${truncateValue(String(v))}`)
    .join(' · ')
  return { summary, command: '', inputLines: makeInputLines(i) }
}

// ─── helpers ──────────────────────────────────────────────────

function makeInputLines(i: Record<string, unknown>): Array<{ key: string; value: string }> {
  return Object.entries(i)
    .filter(([, v]) => v !== '' && v !== false && v != null)
    .map(([k, v]) => ({ key: k, value: typeof v === 'string' ? v : JSON.stringify(v) }))
}

function str(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

// "2026-05-14T19:00:00Z" → "19:00Z" for compact chip display. Falls back to
// the original string when it doesn't look like an RFC3339 timestamp.
function shortTime(rfc3339: string): string {
  const m = rfc3339.match(/T(\d{2}:\d{2})/)
  return m ? `${m[1]}Z` : rfc3339
}

function truncateValue(s: string, max = 18): string {
  return s.length <= max ? s : s.slice(0, max - 1) + '…'
}

// Single-quote shell quoting: replace single quotes with the standard
// '\'' escape so the resulting command is safe to paste into bash/zsh.
function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}
