// Formatting helpers shared by the dashboard cards.

export function fmtBytes(n?: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

export function fmtDate(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return Number.isNaN(d.getTime())
    ? '—'
    : d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

export function relTime(ts: string): string {
  const d = new Date(ts).getTime()
  const s = Math.round((Date.now() - d) / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h}h ago`
  return new Date(ts).toLocaleString()
}

export function statusBadge(status: string): string {
  return status === 'running' ? 'badge-success' : status === 'failed' ? 'badge-danger' : 'badge-neutral'
}

export function healthBadge(health: string): string {
  return health === 'healthy' ? 'badge-success' : health === 'unhealthy' ? 'badge-danger' : 'badge-neutral'
}
