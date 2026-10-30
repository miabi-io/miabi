import type { AddressRecord } from '@/api/types'

export interface PublicAddress {
  ipv4?: string
  ipv6?: string
  hostname?: string
}

const ipv4Pattern = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/
const hostnamePattern = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$/

export function normalizeHost(h: string): string {
  return h.trim().toLowerCase().replace(/\.$/, '')
}

export function isIPv4(v: string): boolean {
  return ipv4Pattern.test(v.trim())
}

// The server is authoritative; this only rejects what it would, so the form can say so before saving.
export function isPublicIPv6(v: string): boolean {
  const s = v.trim().replace(/^\[|\]$/g, '')
  if (!s.includes(':') || s.includes('%')) return false
  let host: string
  try {
    host = new URL(`http://[${s}]/`).hostname.slice(1, -1)
  } catch {
    return false
  }
  if (host.startsWith('::ffff:') || /^::\d*$/.test(host)) return false
  const first = parseInt(host.split(':')[0] || '0', 16)
  // Global unicast is 2000::/3; that also leaves out link-local fe80::/10 and unique-local fc00::/7.
  return first >= 0x2000 && first <= 0x3fff
}

export function isDnsName(v: string): boolean {
  return hostnamePattern.test(normalizeHost(v))
}

/**
 * Mirrors models.PublicAddress.AddressRecords: an A and an AAAA per IP family, else a CNAME to the hostname (never
 * at the apex), and a www host whose bare name is routed too aliases that name.
 */
export function addressRecords(host: string, routed: string[], a: PublicAddress, apex = false): AddressRecord[] {
  const h = normalizeHost(host)
  if (!h || h.startsWith('*')) return []
  const hasIP = !!(a.ipv4 || a.ipv6)
  if (hasIP && !apex && h.startsWith('www.')) {
    const bare = h.slice(4)
    if (bare.includes('.') && routed.some((r) => normalizeHost(r) === bare)) {
      return [{ type: 'CNAME', name: h, value: `${bare}.` }]
    }
  }
  const recs: AddressRecord[] = []
  if (a.ipv4) recs.push({ type: 'A', name: h, value: a.ipv4 })
  if (a.ipv6) recs.push({ type: 'AAAA', name: h, value: a.ipv6 })
  if (!recs.length && a.hostname && !apex) recs.push({ type: 'CNAME', name: h, value: `${a.hostname.replace(/\.$/, '')}.` })
  return recs
}
