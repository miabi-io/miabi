import type { RouteStats } from '@/api/types'

// Only codes some middleware in the chain can answer with; the same code from the app would be misattributed otherwise.
export function earlyAnswers(stats: RouteStats, chainCodes: number[]): { code: number; count: number }[] {
  const wanted = new Set(chainCodes)
  return Object.entries(stats.codes)
    .map(([code, count]) => ({ code: Number(code), count }))
    .filter((c) => wanted.has(c.code) && c.count > 0)
    .sort((a, b) => b.count - a.count)
}

export function errorShare(stats: RouteStats): number {
  return stats.requests > 0 ? stats.status.s5xx / stats.requests : 0
}

export function formatRate(perMinute: number): string {
  if (perMinute <= 0) return '0'
  if (perMinute < 10) return perMinute.toFixed(1).replace(/\.0$/, '')
  return Math.round(perMinute).toLocaleString()
}

// Gateway events carry whole milliseconds, so anything under one is rounding, not a measurement.
export function formatMs(ms: number): string {
  if (ms < 1) return '<1 ms'
  return ms < 10 ? `${ms.toFixed(1).replace(/\.0$/, '')} ms` : `${Math.round(ms).toLocaleString()} ms`
}

export function formatPercent(share: number): string {
  if (share <= 0) return '0%'
  if (share < 0.001) return '<0.1%'
  return `${(share * 100).toFixed(share < 0.1 ? 1 : 0)}%`
}
