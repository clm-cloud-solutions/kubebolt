export function getStatusColor(status: string): string {
  const s = status.toLowerCase()
  if (['running', 'ready', 'active', 'healthy', 'available', 'bound', 'succeeded', 'complete', 'pass'].includes(s)) {
    return 'text-status-ok'
  }
  if (['pending', 'warning', 'warn', 'degraded', 'terminating'].includes(s)) {
    return 'text-status-warn'
  }
  if (['failed', 'error', 'crashloopbackoff', 'critical', 'imagepullbackoff', 'fail', 'evicted', 'oomkilled'].includes(s)) {
    return 'text-status-error'
  }
  return 'text-status-info'
}

export function getStatusBgColor(status: string): string {
  const s = status.toLowerCase()
  if (['running', 'ready', 'active', 'healthy', 'available', 'bound', 'succeeded', 'complete', 'pass'].includes(s)) {
    return 'bg-status-ok-dim text-status-ok'
  }
  if (['pending', 'warning', 'warn', 'degraded', 'terminating'].includes(s)) {
    return 'bg-status-warn-dim text-status-warn'
  }
  if (['failed', 'error', 'crashloopbackoff', 'critical', 'imagepullbackoff', 'fail', 'evicted', 'oomkilled'].includes(s)) {
    return 'bg-status-error-dim text-status-error'
  }
  return 'bg-status-info-dim text-status-info'
}

export function getCPUColor(percent: number): string {
  if (percent >= 80) return 'bg-status-error'
  if (percent >= 50) return 'bg-status-warn'
  return 'bg-status-ok'
}

export function getMemColor(percent: number): string {
  if (percent >= 80) return 'bg-status-error'
  if (percent >= 50) return 'bg-status-warn'
  return 'bg-status-ok'
}

export function getUsageBarColor(percent: number): string {
  if (percent >= 80) return '#ef4056'
  if (percent >= 50) return '#f5a623'
  return '#22d68a'
}

export const statusColorMap: Record<string, string> = {
  ok: '#22d68a',
  warn: '#f5a623',
  error: '#ef4056',
  info: '#4c9aff',
  healthy: '#22d68a',
  degraded: '#f5a623',
  critical: '#ef4056',
  running: '#22d68a',
  pending: '#f5a623',
  failed: '#ef4056',
}

export function getDotColor(status: string): string {
  const s = status.toLowerCase()
  if (['running', 'ready', 'active', 'healthy', 'succeeded'].includes(s)) return 'bg-status-ok'
  if (['pending', 'warning'].includes(s)) return 'bg-status-warn'
  if (['failed', 'error', 'crashloopbackoff'].includes(s)) return 'bg-status-error'
  return 'bg-[#555770]'
}
