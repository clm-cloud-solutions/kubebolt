import { describe, it, expect } from 'vitest'
import { opencostPromUrlFor, parseOpenCostPrometheus } from './opencostPrometheus'

describe('parseOpenCostPrometheus', () => {
  // The case that broke an install: Prometheus in a namespace that is not the
  // chart's default. Service and namespace have to come out of the URL, because
  // that is all the operator was asked for.
  it('splits an in-cluster service into name, namespace and port', () => {
    expect(parseOpenCostPrometheus('http://kube-prometheus-stack-prometheus.monitoring.svc:9090')).toEqual({
      kind: 'internal',
      serviceName: 'kube-prometheus-stack-prometheus',
      namespaceName: 'monitoring',
      port: 9090,
      scheme: 'http',
      path: '',
    })
  })

  it('accepts the fully qualified cluster.local form', () => {
    const t = parseOpenCostPrometheus('http://prometheus-server.observability.svc.cluster.local:80')
    expect(t).toMatchObject({ kind: 'internal', serviceName: 'prometheus-server', namespaceName: 'observability', port: 80 })
  })

  // Pasting host:port without a scheme is normal, and it must not fall through
  // to the external branch — that would silently change which chart setting the
  // command writes.
  it('treats a bare host:port as in-cluster', () => {
    expect(parseOpenCostPrometheus('prometheus.monitoring.svc:9090')).toMatchObject({
      kind: 'internal',
      serviceName: 'prometheus',
      namespaceName: 'monitoring',
      port: 9090,
    })
  })

  it('defaults the port from the scheme when the URL omits it', () => {
    expect(parseOpenCostPrometheus('https://prom.mon.svc')).toMatchObject({ port: 443, scheme: 'https' })
    expect(parseOpenCostPrometheus('http://prom.mon.svc')).toMatchObject({ port: 80, scheme: 'http' })
  })

  it('keeps a sub-path, which a reverse proxy in front of Prometheus needs', () => {
    expect(parseOpenCostPrometheus('http://mimir.obs.svc:8080/prometheus')).toMatchObject({ path: '/prometheus' })
  })

  it('passes a managed endpoint through as external', () => {
    expect(parseOpenCostPrometheus('https://aps-workspaces.eu-west-1.amazonaws.com/workspaces/ws-123')).toEqual({
      kind: 'external',
      url: 'https://aps-workspaces.eu-west-1.amazonaws.com/workspaces/ws-123',
    })
  })

  // `.svc` has to be the third label. A public host that merely contains the
  // string would otherwise be shredded into a service and a namespace that do
  // not exist — the wrong branch, written confidently.
  it('does not mistake a public host containing "svc" for a Service', () => {
    expect(parseOpenCostPrometheus('https://svc.example.com/prom')).toMatchObject({ kind: 'external' })
    expect(parseOpenCostPrometheus('https://prometheus.svc.example.com')).toMatchObject({ kind: 'external' })
  })

  it('reports empty input as empty, so the command can show a placeholder', () => {
    expect(parseOpenCostPrometheus(undefined)).toEqual({ kind: 'empty' })
    expect(parseOpenCostPrometheus('   ')).toEqual({ kind: 'empty' })
  })

  // The command re-renders on every keystroke, so half-typed text must resolve
  // to something rather than throw.
  it('never throws on partial input', () => {
    for (const partial of ['h', 'http:', 'http://', '://x', 'prometheus.']) {
      expect(() => parseOpenCostPrometheus(partial)).not.toThrow()
    }
  })
})

describe('opencostPromUrlFor', () => {
  const dedicated = 'http://other.ns.svc:9090'
  const promRead = 'http://prometheus.monitoring.svc:9090'

  // The duality this rule exists to remove: with promRead selected the operator
  // is shown ONE box, and its value is the one that counts.
  it('uses the promRead URL when that is the metrics source', () => {
    expect(opencostPromUrlFor('promread', promRead, dedicated)).toBe(promRead)
  })

  // The important half. A leftover in the dedicated field must NOT win, because
  // the field is hidden in this mode — a hidden value overriding a visible one
  // is the trap being replaced, not a convenience.
  it('ignores a stale dedicated value while promRead is active', () => {
    expect(opencostPromUrlFor('promread', promRead, 'http://stale.old.svc:1234')).toBe(promRead)
  })

  it('does not fall back to the dedicated field when promRead is empty', () => {
    expect(opencostPromUrlFor('promread', '', dedicated)).toBe('')
    expect(opencostPromUrlFor('promread', undefined, dedicated)).toBe('')
  })

  it('uses the dedicated field for every other metrics source', () => {
    expect(opencostPromUrlFor('kubelet', promRead, dedicated)).toBe(dedicated)
    expect(opencostPromUrlFor('scrape', promRead, dedicated)).toBe(dedicated)
    expect(opencostPromUrlFor(undefined, promRead, dedicated)).toBe(dedicated)
  })

  it('trims, so a stray space does not become a URL', () => {
    expect(opencostPromUrlFor('kubelet', undefined, '  ' + dedicated + ' ')).toBe(dedicated)
    expect(opencostPromUrlFor('kubelet', undefined, '   ')).toBe('')
  })
})
