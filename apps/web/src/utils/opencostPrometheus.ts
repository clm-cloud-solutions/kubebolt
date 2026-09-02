// Turning one Prometheus URL into the OpenCost sub-chart's Prometheus settings.
//
// The bundled OpenCost is a SECOND Prometheus consumer, separate from the
// agent's promRead: the agent scrapes OpenCost's /metrics directly, while
// OpenCost itself queries a Prometheus to do allocation math. The wizard never
// set it, so the sub-chart fell back to the official chart's default —
// prometheus-server.prometheus-system.svc:80 — and on any cluster that does not
// happen to run Prometheus under that exact name the pod dies on boot:
//
//   FTL Failed to create Prometheus data source: ... no such host
//
// Not a degraded install: a crash loop. Confirmed in vivo on an AKS lab
// 2026-08-20, where Prometheus simply lives in another namespace.
//
// The official chart splits the setting in two shapes — `internal` (a Service
// addressed by name + namespace + port) and `external` (a plain URL) — but
// asking an operator to know which branch they are in is asking them to read
// our sub-chart's values. They already know their URL, and the URL says which
// it is: an in-cluster Service name always carries a `.svc` label.

/** What a Prometheus URL resolves to in the OpenCost sub-chart's terms. */
export type OpenCostPromTarget =
  | { kind: 'internal'; serviceName: string; namespaceName: string; port: number; scheme: string; path: string }
  | { kind: 'external'; url: string }
  | { kind: 'empty' }

/**
 * parseOpenCostPrometheus classifies a Prometheus URL.
 *
 * `<service>.<namespace>.svc[.cluster.local][:port][/path]` → internal, split
 * into the fields the chart wants. Anything else that parses as a URL →
 * external, passed through whole.
 *
 * Unparseable input resolves to `external` rather than an error: the command is
 * rendered live, character by character, so half-typed text must not blow up
 * mid-keystroke — and a wrong-looking URL in the command is visible, which is
 * more use than a silent omission.
 */
export function parseOpenCostPrometheus(raw: string | undefined): OpenCostPromTarget {
  const value = (raw ?? '').trim()
  if (!value) return { kind: 'empty' }

  let url: URL
  try {
    // A bare host:port ("prometheus.monitoring.svc:9090") is not a URL until it
    // has a scheme, and it is a perfectly normal thing to paste.
    url = new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(value) ? value : `http://${value}`)
  } catch {
    return { kind: 'external', url: value }
  }

  const labels = url.hostname.split('.')
  // `<service>.<namespace>.svc…` — the `.svc` must sit in the THIRD position.
  // Matching it anywhere would swallow a public host that merely contains the
  // string, and a two-label name has no namespace to read.
  const isInternal = labels.length >= 3 && labels[2] === 'svc' && !!labels[0] && !!labels[1]

  if (!isInternal) {
    return { kind: 'external', url: stripTrailingSlash(value) }
  }

  const scheme = url.protocol.replace(':', '') || 'http'
  // An omitted port means the scheme's default, and the chart wants a number,
  // not an empty string that would render as `port=`.
  const port = url.port ? Number(url.port) : scheme === 'https' ? 443 : 80
  const path = url.pathname === '/' ? '' : stripTrailingSlash(url.pathname)

  return { kind: 'internal', serviceName: labels[0], namespaceName: labels[1], port, scheme, path }
}

function stripTrailingSlash(s: string): string {
  return s.endsWith('/') ? s.slice(0, -1) : s
}

/**
 * opencostPromUrlFor decides WHICH Prometheus URL the bundled OpenCost uses.
 *
 * One rule, because two inputs for the same server is how they end up
 * disagreeing: when the metrics source is already promRead, that URL wins and
 * the dedicated field is not shown at all. Otherwise the dedicated field is the
 * only source.
 *
 * Deliberately NOT a fallback chain. Preferring promRead "unless the other one
 * is filled" leaves a hidden value overriding a field the operator can no longer
 * see — the exact trap this is replacing.
 *
 * The trade-off is real and accepted: a promRead pointed at a managed endpoint
 * through a proxy may not be queryable by OpenCost the same way. That case is
 * still reachable, in values.yaml, and it is rarer than mistyping one of two
 * boxes that ask for the same thing.
 */
export function opencostPromUrlFor(
  metricsSource: string | undefined,
  promReadUrl: string | undefined,
  dedicatedUrl: string | undefined,
): string {
  if (metricsSource === 'promread') return (promReadUrl ?? '').trim()
  return (dedicatedUrl ?? '').trim()
}
