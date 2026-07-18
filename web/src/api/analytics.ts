import api from './client'
import type { ApiResponse } from './types'

const w = (ws: number) => `/workspaces/${ws}`

// One labelled count in a breakdown (top paths, referrers, countries, …).
export interface Category {
  label: string
  count: number
}

// Headline traffic numbers over the whole range.
export interface AnalyticsTotals {
  requests: number
  bytes_in: number
  bytes_out: number
  unique_visitors: number
  error_rate: number // 0..1
  avg_latency_ms: number
  p50_latency_ms: number
  p95_latency_ms: number
  p99_latency_ms: number
}

// One time bucket of the request/latency series (granularity chosen server-side).
export interface AnalyticsSeriesPoint {
  t: string
  requests: number
  bytes_in: number
  bytes_out: number
  errors: number // 4xx+5xx (kept for compatibility)
  errors_4xx: number
  errors_5xx: number
  unique_visitors: number
  avg_latency_ms: number
  p95_latency_ms: number
}

export interface AnalyticsStatus {
  s2xx: number
  s3xx: number
  s4xx: number
  s5xx: number
}

export interface RouteStat {
  route: string
  requests: number
  p95_latency_ms: number
  error_rate: number
}

export interface AnalyticsPerformance {
  request_p50_ms: number
  request_p95_ms: number
  request_p99_ms: number
  upstream_p50_ms: number
  upstream_p95_ms: number
  upstream_p99_ms: number
  avg_request_ms: number
  avg_upstream_ms: number
  avg_overhead_ms: number
  slow_routes: RouteStat[]
}

export interface AnalyticsWeb {
  unique_visitors: number
  bot_requests: number
  human_requests: number
  top_paths: Category[]
  top_referrers: Category[]
  top_countries: Category[]
  top_browsers: Category[]
  top_os: Category[]
  top_devices: Category[]
  top_methods: Category[]
}

export interface AnalyticsReport {
  range: { since: string; until: string }
  granularity: 'minute' | 'hour' | 'day'
  totals: AnalyticsTotals
  series: AnalyticsSeriesPoint[]
  status: AnalyticsStatus
  performance: AnalyticsPerformance
  web: AnalyticsWeb
  // Totals of the immediately preceding equal-length window (period-over-period).
  compare?: AnalyticsTotals
  // Effective retention cap in days (-1 = unlimited) and whether CSV export is
  // entitled — both edition-derived, used to bound the range picker and gate the
  // export button behind Enterprise.
  retention_days: number
  exportable: boolean
}

// Workspace Analytics: HTTP traffic, performance and web analytics rolled up
// from the gateway's request stream. `range` is a window ending now
// (15m, 1h, 24h, 7d, 30d); `app` filters to a single application.
export const analyticsApi = {
  report: (ws: number, range: string, app?: number) =>
    api.get<ApiResponse<AnalyticsReport>>(`${w(ws)}/analytics`, {
      params: { range, ...(app ? { app } : {}) },
    }),
  apps: (ws: number, range: string) =>
    api.get<ApiResponse<{ application_ids: number[] }>>(`${w(ws)}/analytics/apps`, {
      params: { range },
    }),
  // CSV export (Enterprise-gated server-side). Returns the relative API path so
  // the caller can open it as a download.
  exportPath: (ws: number, range: string, app?: number) => {
    const p = new URLSearchParams({ range })
    if (app) p.set('app', String(app))
    return `${w(ws)}/analytics/export?${p.toString()}`
  },
}
