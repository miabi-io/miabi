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
  top_user_agents: Category[]
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
  // retention_days caps the range picker (-1 = unlimited); exportable gates CSV export (Enterprise).
  retention_days: number
  exportable: boolean
}

export interface AnalyticsSummary {
  range: { since: string; until: string }
  granularity: 'minute' | 'hour' | 'day'
  totals: AnalyticsTotals
  series: AnalyticsSeriesPoint[]
  status: AnalyticsStatus
  /** Empty without a GeoIP database, indistinguishable from no traffic, so the dashboard hides the panel. */
  top_countries: Category[]
  compare?: AnalyticsTotals
}

// Distinct visitors seen within the live window (server-defined, ~5 minutes).
export interface LiveVisitors {
  visitors: number
  window_seconds: number
}

const CUSTOM_RANGE = /^(\d{4}-\d{2}-\d{2})\.\.(\d{4}-\d{2}-\d{2})$/

// isCustomRange reports whether range is a "YYYY-MM-DD..YYYY-MM-DD" span of local dates.
export function isCustomRange(range: string): boolean {
  return CUSTOM_RANGE.test(range)
}

function localMidnight(date: string, addDays = 0): Date {
  const [y, m, d] = date.split('-').map(Number)
  return new Date(y, m - 1, d + addDays)
}

// Absolute instants so the server cuts days at the viewer's midnight, not UTC's; the end date is inclusive.
function toApiRange(range: string): string {
  const m = CUSTOM_RANGE.exec(range)
  if (!m) return range
  return `${localMidnight(m[1]).toISOString()}..${localMidnight(m[2], 1).toISOString()}`
}

export const analyticsApi = {
  report: (ws: number, range: string, app?: number) =>
    api.get<ApiResponse<AnalyticsReport>>(`${w(ws)}/analytics`, {
      params: { range: toApiRange(range), ...(app ? { app } : {}) },
    }),
  apps: (ws: number, range: string) =>
    api.get<ApiResponse<{ application_ids: number[] }>>(`${w(ws)}/analytics/apps`, {
      params: { range: toApiRange(range) },
    }),
  // The server skips computing the breakdowns for this card.
  summary: (ws: number, range: string, app?: number) =>
    api.get<ApiResponse<AnalyticsSummary>>(`${w(ws)}/analytics/summary`, {
      params: { range: toApiRange(range), ...(app ? { app } : {}) },
    }),
  // Independent of `range`: a right-now number polled while the dashboard is open.
  live: (ws: number, app?: number) =>
    api.get<ApiResponse<LiveVisitors>>(`${w(ws)}/analytics/live`, {
      params: { ...(app ? { app } : {}) },
    }),
  exportPath: (ws: number, range: string, app?: number) => {
    const p = new URLSearchParams({ range: toApiRange(range) })
    if (app) p.set('app', String(app))
    return `${w(ws)}/analytics/export?${p.toString()}`
  },
}
