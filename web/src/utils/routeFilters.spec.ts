import { describe, expect, it } from 'vitest'
import type { Route } from '@/api/types'
import { filterRoutes, parseRouteFilters, routeFiltersToQuery } from './routeFilters'

const r = (over: Partial<Route>): Route => ({
  id: 1, name: 'r', application_id: 1, enabled: true, status: 'live', hosts: [], path: '/', ...over,
}) as Route

const routes: Route[] = [
  r({ id: 1, name: 'web', application_id: 1, hosts: ['shop.example.com'] }),
  r({ id: 2, name: 'api', application_id: 2, status: 'error', hosts: ['api.example.com'] }),
  r({ id: 3, name: 'mb-ext-1', application_id: 1, generated: true, status: 'offline' }),
  r({ id: 4, name: 'old', application_id: 2, enabled: false, status: 'pending' }),
  r({ id: 5, name: 'docs', application_id: 1, maintenance: { enabled: true } }),
]
const appName = (id: number) => (id === 1 ? 'shop' : 'backend')
const names = (list: Route[]) => list.map((x) => x.name)

describe('route filters', () => {
  it('reads and writes the query string, dropping defaults and bad values', () => {
    const f = parseRouteFilters({ q: ' shop ', app: '2', status: 'failing', kind: 'nope', sort: 'status', dir: 'desc', page: '-1' })
    expect(f).toEqual({ q: 'shop', app: 2, status: 'failing', kind: '', sort: 'status', desc: true, page: 0 })
    expect(routeFiltersToQuery(f)).toEqual({ q: 'shop', app: '2', status: 'failing', sort: 'status', dir: 'desc' })
    expect(routeFiltersToQuery(parseRouteFilters({}))).toEqual({})
  })

  it('searches name, host and path', () => {
    expect(names(filterRoutes(routes, parseRouteFilters({ q: 'example.com' }), appName))).toEqual(['api', 'web'])
  })

  it('filters by app, kind and status', () => {
    expect(names(filterRoutes(routes, parseRouteFilters({ app: '1', kind: 'custom' }), appName))).toEqual(['docs', 'web'])
    expect(names(filterRoutes(routes, parseRouteFilters({ status: 'failing' }), appName))).toEqual(['api', 'mb-ext-1'])
    expect(names(filterRoutes(routes, parseRouteFilters({ status: 'disabled' }), appName))).toEqual(['old'])
    expect(names(filterRoutes(routes, parseRouteFilters({ status: 'maintenance' }), appName))).toEqual(['docs'])
  })

  it('sorts failing routes first by status', () => {
    expect(names(filterRoutes(routes, parseRouteFilters({ sort: 'status' }), appName)).slice(0, 2)).toEqual(['api', 'mb-ext-1'])
    expect(names(filterRoutes(routes, parseRouteFilters({ sort: 'app' }), appName))[0]).toBe('api')
  })
})
