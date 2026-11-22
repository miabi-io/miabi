import { describe, expect, it } from 'vitest'
import type { RouteStats } from '@/api/types'
import { earlyAnswers, errorShare, formatMs, formatPercent, formatRate } from './routeStats'

const stats = (over: Partial<RouteStats> = {}): RouteStats => ({
  enabled: true, range: { since: '', until: '' }, requests: 1000, per_minute: 66.7, visitors: 10,
  status: { s2xx: 900, s3xx: 0, s4xx: 96, s5xx: 4 }, codes: { '401': 38, '429': 12, '404': 46, '503': 4 },
  p95_ms: 80, avg_ms: 30, avg_gateway_ms: 3, series: [], ...over,
})

describe('earlyAnswers', () => {
  it("keeps only codes the chain can answer, largest first", () => {
    expect(earlyAnswers(stats(), [401, 429, 301])).toEqual([{ code: 401, count: 38 }, { code: 429, count: 12 }])
  })
})

describe('formatting', () => {
  it('formats rates and shares', () => {
    expect(formatRate(0)).toBe('0')
    expect(formatRate(1.04)).toBe('1')
    expect(formatRate(2.5)).toBe('2.5')
    expect(formatRate(66.7)).toBe('67')
    expect(errorShare(stats())).toBe(0.004)
    expect(formatPercent(0.004)).toBe('0.4%')
    expect(formatPercent(0.0004)).toBe('<0.1%')
    expect(formatPercent(0.25)).toBe('25%')
    expect(formatMs(0.01)).toBe('<1 ms')
    expect(formatMs(4)).toBe('4 ms')
    expect(formatMs(84.4)).toBe('84 ms')
  })
})
