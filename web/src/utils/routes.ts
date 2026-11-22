import type { Route } from '@/api/types'

// One place for how a route's sync state looks, so the app tab and the workspace list never disagree.
export function routeStatusBadge(r: Route): string {
  const s = r.status ?? 'pending'
  if (s === 'live') return 'badge-success'
  if (s === 'error') return 'badge-danger'
  if (s === 'offline') return 'badge-warning'
  return 'badge-neutral'
}

export function routeStatusLabel(r: Route): string {
  return r.status ?? 'pending'
}

export function routeUrl(r: Route, host: string): string {
  const scheme = r.tls_mode && r.tls_mode !== 'none' ? 'https' : 'http'
  const path = r.path && r.path !== '/' ? r.path : ''
  return `${scheme}://${host}${path}`
}
