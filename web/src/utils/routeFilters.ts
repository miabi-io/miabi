import type { LocationQuery, LocationQueryRaw } from 'vue-router'
import type { Route } from '@/api/types'

export type RouteStatusFilter = '' | 'live' | 'failing' | 'pending' | 'disabled' | 'maintenance'
export type RouteKindFilter = '' | 'custom' | 'generated'
export type RouteSort = 'name' | 'app' | 'status'

export interface RouteFilters {
  q: string
  app: number | null
  status: RouteStatusFilter
  kind: RouteKindFilter
  sort: RouteSort
  desc: boolean
  page: number
}

const statuses: RouteStatusFilter[] = ['live', 'failing', 'pending', 'disabled', 'maintenance']
const kinds: RouteKindFilter[] = ['custom', 'generated']
const sorts: RouteSort[] = ['name', 'app', 'status']

export const ROUTES_PAGE_SIZE = 50

function one(v: LocationQuery[string]): string {
  return (Array.isArray(v) ? v[0] : v) ?? ''
}

// Anything unrecognised in the URL falls back to the default, so a stale link never breaks the page.
export function parseRouteFilters(query: LocationQuery): RouteFilters {
  const app = Number(one(query.app))
  const page = Number(one(query.page))
  const status = one(query.status) as RouteStatusFilter
  const kind = one(query.kind) as RouteKindFilter
  const sort = one(query.sort) as RouteSort
  return {
    q: one(query.q).trim(),
    app: Number.isInteger(app) && app > 0 ? app : null,
    status: statuses.includes(status) ? status : '',
    kind: kinds.includes(kind) ? kind : '',
    sort: sorts.includes(sort) ? sort : 'name',
    desc: one(query.dir) === 'desc',
    page: Number.isInteger(page) && page > 0 ? page : 0,
  }
}

// Defaults are left out, so an unfiltered list keeps a clean URL.
export function routeFiltersToQuery(f: RouteFilters): LocationQueryRaw {
  const q: LocationQueryRaw = {}
  if (f.q) q.q = f.q
  if (f.app) q.app = String(f.app)
  if (f.status) q.status = f.status
  if (f.kind) q.kind = f.kind
  if (f.sort !== 'name') q.sort = f.sort
  if (f.desc) q.dir = 'desc'
  if (f.page > 0) q.page = String(f.page)
  return q
}

function matchesStatus(r: Route, s: RouteStatusFilter): boolean {
  switch (s) {
    case 'live':
      return r.enabled && r.status === 'live'
    case 'failing':
      return r.status === 'error' || r.status === 'offline'
    case 'pending':
      return !r.status || r.status === 'pending'
    case 'disabled':
      return !r.enabled
    case 'maintenance':
      return r.maintenance?.enabled === true
  }
  return true
}

// Failing first when sorting by status: that is what someone sorting by it is looking for.
const statusRank: Record<string, number> = { error: 0, offline: 1, pending: 2, live: 3 }

export function filterRoutes(routes: Route[], f: RouteFilters, appName: (id: number) => string): Route[] {
  const needle = f.q.toLowerCase()
  const out = routes.filter((r) => {
    if (f.app && r.application_id !== f.app) return false
    if (f.kind === 'generated' && !r.generated) return false
    if (f.kind === 'custom' && r.generated) return false
    if (f.status && !matchesStatus(r, f.status)) return false
    if (!needle) return true
    return [r.name, r.display_name ?? '', r.path ?? '', ...(r.hosts ?? [])].some((v) => v.toLowerCase().includes(needle))
  })
  const key = (r: Route): string | number => {
    if (f.sort === 'app') return appName(r.application_id).toLowerCase()
    if (f.sort === 'status') return statusRank[r.status ?? 'pending'] ?? 2
    return (r.display_name || r.name).toLowerCase()
  }
  out.sort((a, b) => {
    const ka = key(a)
    const kb = key(b)
    const c = ka < kb ? -1 : ka > kb ? 1 : a.name.localeCompare(b.name)
    return f.desc ? -c : c
  })
  return out
}
