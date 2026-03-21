export function formatCPU(millicores: number): string {
  if (millicores >= 1000) {
    return `${(millicores / 1000).toFixed(1)} cores`
  }
  return `${Math.round(millicores)}m`
}

export function formatMemory(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'Ki', 'Mi', 'Gi', 'Ti']
  const k = 1024
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  const val = bytes / Math.pow(k, i)
  return `${val >= 100 ? Math.round(val) : val.toFixed(1)} ${units[i]}`
}

export function formatAge(timestamp: string): string {
  const now = Date.now()
  const then = new Date(timestamp).getTime()
  const diffMs = now - then
  if (diffMs < 0) return 'just now'

  const seconds = Math.floor(diffMs / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)

  if (days > 0) {
    const remainHours = hours % 24
    return `${days}d ${remainHours}h`
  }
  if (hours > 0) {
    const remainMins = minutes % 60
    return `${hours}h ${remainMins}m`
  }
  if (minutes > 0) return `${minutes}m`
  return `${seconds}s`
}

export function formatPercent(value: number): string {
  return `${Math.round(value)}%`
}

export function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const k = 1000
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  const val = bytes / Math.pow(k, i)
  return `${val >= 100 ? Math.round(val) : val.toFixed(1)} ${units[i]}`
}

export function formatCount(count: number): string {
  if (count >= 1000) return `${(count / 1000).toFixed(1)}k`
  return `${count}`
}
