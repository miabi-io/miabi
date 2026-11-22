import { describe, expect, it } from 'vitest'
import type { RouteTrace } from '@/api/types'
import { parseHeaders, tracePath } from './routeTrace'

describe('parseHeaders', () => {
  it('reads one header per line and keeps values with colons', () => {
    expect(parseHeaders('Authorization: Bearer a:b\n\nbad line\nX-Canary:1')).toEqual({ Authorization: 'Bearer a:b', 'X-Canary': '1' })
  })
})

describe('tracePath', () => {
  const base = (steps: RouteTrace['steps'], result: RouteTrace['result']): RouteTrace => ({ method: 'GET', url: 'https://a/', steps, result })

  it('marks the middleware that stopped the request', () => {
    const p = tracePath(base([
      { stage: 'visitors', code: 'request', outcome: 'pass', detail: '' },
      { stage: 'gateway', code: 'matched', outcome: 'pass', detail: '' },
      { stage: 'middleware', code: 'mw_auth_missing', outcome: 'stop', index: 1, detail: '' },
    ], { kind: 'early_answer', status: 401, detail: '' }))
    expect([...p.visited]).toEqual(['visitors', 'gateway', 'chain', 'mw:1'])
    expect(p.stop).toBe('mw:1')
  })

  it('visits both releases on a weighted split', () => {
    const p = tracePath(base([
      { stage: 'split', code: 'canary_split', outcome: 'fork', params: { backend: 'split' }, detail: '' },
      { stage: 'backend', code: 'forwarded', outcome: 'pass', params: { role: 'stable' }, detail: '' },
    ], { kind: 'app', backend: 'split', detail: '' }))
    expect(p.visited.has('stable') && p.visited.has('canary')).toBe(true)
    expect(p.stop).toBeNull()
  })

  it('stops at the maintenance box', () => {
    const p = tracePath(base([{ stage: 'gateway', code: 'maintenance', outcome: 'stop', detail: '' }], { kind: 'maintenance', status: 503, detail: '' }))
    expect(p.stop).toBe('maint')
  })
})
