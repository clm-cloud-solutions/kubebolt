// An EndpointSlice a Kubernetes controller writes is rewritten within seconds
// of any edit (debt-20): the EndpointSlice controller rebuilds it from its
// Service's selector, the mirroring controller copies it from the core/v1
// Endpoints of a Service without one. The YAML tab reads the slice's own
// labels to say so before an edit, and the API refuses one with a 409
// (cluster/endpointslice_managed.go).

export interface ManagedSlice {
  // What to edit instead: the Service (built from its selector) or the
  // Endpoints the slice mirrors (editable as `coreendpoints`).
  source: 'service' | 'endpoints'
  service: string
}

const MANAGED_BY = 'endpointslice.kubernetes.io/managed-by'
const SERVICE_NAME = 'kubernetes.io/service-name'

// The value of an indented `key: value` line, compared as text: a label key
// is full of dots, and a pattern built from it would read each as any character.
function label(yaml: string, key: string): string | undefined {
  for (const line of yaml.split('\n')) {
    if (!/^\s/.test(line)) continue
    const text = line.trim()
    if (!text.startsWith(`${key}:`)) continue
    const value = text.slice(key.length + 1).trim().replace(/^["']|["']$/g, '')
    return value || undefined
  }
  return undefined
}

export function managedSliceOf(yaml: string): ManagedSlice | null {
  const service = label(yaml, SERVICE_NAME) ?? ''
  switch (label(yaml, MANAGED_BY)) {
    case 'endpointslice-controller.k8s.io':
      return { source: 'service', service }
    case 'endpointslicemirroring-controller.k8s.io':
      return { source: 'endpoints', service }
  }
  return null
}
