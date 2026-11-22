export type HostIssue = 'scheme' | 'path' | 'port' | 'invalid' | 'duplicate' | 'unregistered' | 'banned' | 'unverified'

export interface HostCheck {
  host: string
  issue: HostIssue
  // Warnings save; errors are what the server would reject or the gateway could never match.
  level: 'error' | 'warning'
  domain?: string
}

export interface DomainLike {
  name: string
  verified: boolean
  banned?: boolean
}

const LABEL = /^(?!-)[a-z0-9-]{1,63}(?<!-)$/

function syntaxIssue(host: string): HostIssue | null {
  if (host.includes('://')) return 'scheme'
  if (host.includes('/')) return 'path'
  if (host.includes(':')) return 'port'
  const name = host.startsWith('*.') ? host.slice(2) : host
  if (name.length > 253) return 'invalid'
  const labels = name.split('.')
  if (labels.length < 2 || !labels.every((l) => LABEL.test(l))) return 'invalid'
  return null
}

// Mirrors the server's matchDomain: the most specific registered domain wins.
export function coveringDomain<D extends DomainLike>(host: string, domains: D[]): D | null {
  let best: D | null = null
  for (const d of domains) {
    const name = d.name.toLowerCase()
    if (host === name || host.endsWith('.' + name)) {
      if (!best || name.length > best.name.length) best = d
    }
  }
  return best
}

// domains is null while they are unknown, so no domain finding is reported from a failed load.
export function checkHosts(hosts: string[], domains: DomainLike[] | null): HostCheck[] {
  const out: HostCheck[] = []
  const seen = new Set<string>()
  for (const raw of hosts) {
    const host = raw.trim().toLowerCase()
    if (!host) continue
    if (seen.has(host)) {
      // The server de-duplicates on save, so this only tidies the form.
      out.push({ host, issue: 'duplicate', level: 'warning' })
      continue
    }
    seen.add(host)
    const syntax = syntaxIssue(host)
    if (syntax) {
      out.push({ host, issue: syntax, level: 'error' })
      continue
    }
    if (!domains) continue
    const d = coveringDomain(host, domains)
    if (!d) out.push({ host, issue: 'unregistered', level: 'error' })
    else if (d.banned) out.push({ host, issue: 'banned', level: 'error', domain: d.name })
    else if (!d.verified) out.push({ host, issue: 'unverified', level: 'warning', domain: d.name })
  }
  return out
}

export function routeUrls(hosts: string[], path: string, tls: boolean): string[] {
  const p = path.trim() || '/'
  const suffix = p.startsWith('/') ? p : '/' + p
  const urls: string[] = []
  for (const raw of hosts) {
    const host = raw.trim().toLowerCase()
    if (!host || syntaxIssue(host) || host.startsWith('*.')) continue
    const url = `${tls ? 'https' : 'http'}://${host}${suffix}`
    if (!urls.includes(url)) urls.push(url)
  }
  return urls
}
