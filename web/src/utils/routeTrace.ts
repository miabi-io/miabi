import type { RouteTrace } from '@/api/types'

// One "Name: value" per line; later lines win, as a client sending the header twice would see the last.
export function parseHeaders(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const i = line.indexOf(':')
    if (i <= 0) continue
    const name = line.slice(0, i).trim()
    if (name) out[name] = line.slice(i + 1).trim()
  }
  return out
}

export interface TracePath {
  visited: Set<string>
  stop: string | null
}

// Node keys match the canvas: visitors, gateway, maint, chain, mw:<index>, split, stable, canary, app.
export function tracePath(trace: RouteTrace): TracePath {
  const visited = new Set<string>()
  let stop: string | null = null
  for (const s of trace.steps) {
    const nodes: string[] = []
    switch (s.stage) {
      case 'visitors':
        nodes.push('visitors')
        break
      case 'gateway':
        nodes.push('gateway')
        if (s.code === 'maintenance' || s.code === 'suspended') nodes.push('maint')
        break
      case 'middleware':
        nodes.push('chain')
        if (s.index !== undefined) nodes.push(`mw:${s.index}`)
        break
      case 'split':
        nodes.push('split')
        if (s.params?.backend === 'split') nodes.push('stable', 'canary')
        else if (s.params?.backend) nodes.push(s.params.backend)
        break
      case 'backend':
        nodes.push(s.code === 'not_deployed' ? 'app' : s.params?.role || 'stable')
        break
    }
    nodes.forEach((n) => visited.add(n))
    if (s.outcome === 'stop') stop = nodes[nodes.length - 1] ?? null
  }
  if (trace.result.kind === 'app' && trace.result.backend) {
    if (trace.result.backend === 'split') visited.add('stable').add('canary')
    else visited.add(trace.result.backend)
  }
  return { visited, stop }
}
