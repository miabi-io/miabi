import { describe, expect, it } from 'vitest'
import { checkHosts, routeUrls } from './hostChecks'

const domains = [
  { name: 'example.com', verified: true },
  { name: 'staging.example.com', verified: false },
  { name: 'bad.io', verified: true, banned: true },
]

describe('checkHosts', () => {
  it('passes good hosts under a verified domain', () => {
    expect(checkHosts(['app.example.com', 'Example.com ', '*.example.com'], domains)).toEqual([])
  })

  it('names what is wrong with the syntax', () => {
    const issues = checkHosts(['https://a.example.com', 'a.example.com/x', 'a.example.com:8080', 'my_app.example.com', '-a.example.com', 'localhost'], domains)
    expect(issues.map((i) => i.issue)).toEqual(['scheme', 'path', 'port', 'invalid', 'invalid', 'invalid'])
    expect(issues.every((i) => i.level === 'error')).toBe(true)
  })

  it('flags duplicates case-insensitively', () => {
    expect(checkHosts(['a.example.com', 'A.example.com'], domains)).toEqual([{ host: 'a.example.com', issue: 'duplicate', level: 'warning' }])
  })

  it('uses the most specific domain, as the server does', () => {
    expect(checkHosts(['api.staging.example.com'], domains)).toEqual([
      { host: 'api.staging.example.com', issue: 'unverified', level: 'warning', domain: 'staging.example.com' },
    ])
  })

  it('reports unregistered and banned domains', () => {
    expect(checkHosts(['x.other.org', 'x.bad.io'], domains).map((i) => i.issue)).toEqual(['unregistered', 'banned'])
  })

  it('reports no domain finding while domains are unknown', () => {
    expect(checkHosts(['x.other.org'], null)).toEqual([])
  })
})

describe('routeUrls', () => {
  it('builds one URL per usable host', () => {
    expect(routeUrls(['a.example.com', 'A.example.com', 'bad host', '*.example.com'], 'api', true)).toEqual(['https://a.example.com/api'])
    expect(routeUrls(['a.example.com'], '', false)).toEqual(['http://a.example.com/'])
  })
})
