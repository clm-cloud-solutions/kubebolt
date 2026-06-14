// Reference-line builders shared by the workload Monitor tabs
// (ResourceDetailPage) and the Capacity dashboard. Both surfaces
// overlay request/limit thresholds on usage charts; keeping the
// builders here guarantees the two render identical labels, colors,
// and the request==limit dedupe behavior.

// Mirrors MetricChart's ReferenceLineSpec (defaultHidden starts the
// line toggled off; its header pill still renders).
export type RefSpec = { y: number; label: string; color?: string; shortLabel?: string; defaultHidden?: boolean }

export function buildCpuRefs(request: number | null, limit: number | null): RefSpec[] {
  // When request === limit (common for guaranteed QoS pods), the two lines
  // overlap and their labels collide. Render them as one combined line.
  if (request != null && limit != null && Math.abs(request - limit) < 1e-9) {
    return [{
      y: limit,
      label: `request / limit ${(limit * 1000).toFixed(0)}m`,
      color: '#ef4444',
      shortLabel: 'req/limit',
    }]
  }
  const refs: RefSpec[] = []
  if (request != null) refs.push({ y: request, label: `request ${(request * 1000).toFixed(0)}m` })
  if (limit != null) refs.push({ y: limit, label: `limit ${(limit * 1000).toFixed(0)}m`, color: '#ef4444' })
  return refs
}

export function buildMemRefs(request: number | null, limit: number | null): RefSpec[] {
  if (request != null && limit != null && request === limit) {
    return [{
      y: limit,
      label: `request / limit ${formatMemoryShort(limit)}`,
      color: '#ef4444',
      shortLabel: 'req/limit',
    }]
  }
  const refs: RefSpec[] = []
  if (request != null) refs.push({ y: request, label: `request ${formatMemoryShort(request)}` })
  if (limit != null) refs.push({ y: limit, label: `limit ${formatMemoryShort(limit)}`, color: '#ef4444' })
  return refs
}

export function formatMemoryShort(bytes: number): string {
  const abs = Math.abs(bytes)
  if (abs < 1024) return `${bytes} B`
  if (abs < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KiB`
  if (abs < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(0)} MiB`
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GiB`
}
