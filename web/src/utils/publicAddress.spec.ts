import { describe, it, expect } from 'vitest'
import { addressRecords, isDnsName, isIPv4, isPublicIPv6 } from './publicAddress'

describe('public address validation', () => {
  it('accepts only dotted-quad IPv4', () => {
    expect(isIPv4('203.0.113.10')).toBe(true)
    expect(isIPv4(' 203.0.113.10 ')).toBe(true)
    expect(isIPv4('10.0.0')).toBe(false)
    expect(isIPv4('256.1.1.1')).toBe(false)
    expect(isIPv4('2001:db8::1')).toBe(false)
  })
  it('accepts only global unicast IPv6', () => {
    expect(isPublicIPv6('2001:db8::10')).toBe(true)
    expect(isPublicIPv6('[2a01:4f8:c0c:1234::1]')).toBe(true)
    expect(isPublicIPv6('fe80::1')).toBe(false)
    expect(isPublicIPv6('fd00::1')).toBe(false)
    expect(isPublicIPv6('::1')).toBe(false)
    expect(isPublicIPv6('::ffff:203.0.113.1')).toBe(false)
    expect(isPublicIPv6('203.0.113.1')).toBe(false)
    expect(isPublicIPv6('2001:db8::zz')).toBe(false)
  })
  it('wants a DNS name, not a machine name or URL', () => {
    expect(isDnsName('LB.example.com.')).toBe(true)
    expect(isDnsName('orbstack')).toBe(false)
    expect(isDnsName('https://lb.example.com')).toBe(false)
  })
})

describe('addressRecords', () => {
  const dual = { ipv4: '203.0.113.1', ipv6: '2001:db8::1', hostname: 'gw.example.net' }
  const fmt = (r: { type: string; value: string }[]) => r.map((x) => `${x.type} ${x.value}`)

  it('writes an A and an AAAA on a dual-stack cluster', () => {
    expect(fmt(addressRecords('app.example.com', [], dual))).toEqual(['A 203.0.113.1', 'AAAA 2001:db8::1'])
  })
  it('aliases www to its bare name when both are routed', () => {
    expect(fmt(addressRecords('www.example.com', ['example.com', 'www.example.com'], dual))).toEqual(['CNAME example.com.'])
    expect(fmt(addressRecords('www.example.com', ['www.example.com'], dual))).toEqual(['A 203.0.113.1', 'AAAA 2001:db8::1'])
  })
  it('falls back to a CNAME, never at the apex', () => {
    expect(fmt(addressRecords('app.example.com', [], { hostname: 'gw.example.net' }))).toEqual(['CNAME gw.example.net.'])
    expect(addressRecords('example.com', [], { hostname: 'gw.example.net' }, true)).toEqual([])
  })
})
