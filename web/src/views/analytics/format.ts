// Formatting helpers shared by the analytics pages.

export function fmtNum(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(n >= 10_000 ? 0 : 1) + 'k'
  return String(Math.round(n))
}

export function fmtBytes(n: number): string {
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${u[i]}`
}

export function fmtMs(n: number): string {
  if (n >= 1000) return (n / 1000).toFixed(2) + ' s'
  return Math.round(n) + ' ms'
}

export function fmtPct(frac: number): string {
  return (frac * 100).toFixed(frac >= 0.1 || frac === 0 ? 1 : 2) + '%'
}

// delta returns the signed relative change from prev to cur (e.g. 0.12 = +12%),
// or null when there's no comparable baseline.
export function delta(cur: number, prev: number | undefined): number | null {
  if (prev === undefined || prev === 0) return null
  return (cur - prev) / prev
}

export function fmtDelta(d: number | null): string {
  if (d === null) return ''
  const sign = d > 0 ? '+' : ''
  return `${sign}${(d * 100).toFixed(d >= 0.1 || d <= -0.1 ? 0 : 1)}%`
}

// Strip the "mb-ws<id>-" gateway prefix so route names read cleanly.
export function routeLabel(name: string): string {
  return name.replace(/^mb-ws\d+-/, '')
}

// countryName resolves an ISO alpha-2 code to a display name via the platform
// Intl data (no lookup table), falling back to the code itself.
let regionNames: Intl.DisplayNames | null = null
try {
  regionNames = new Intl.DisplayNames(['en'], { type: 'region' })
} catch {
  regionNames = null
}
export function countryName(code: string): string {
  if (!code) return 'Unknown'
  try {
    return regionNames?.of(code.toUpperCase()) || code
  } catch {
    return code
  }
}

// countryFlag turns an alpha-2 code into its regional-indicator flag emoji.
export function countryFlag(code: string): string {
  if (!code || code.length !== 2) return '🏳️'
  const cc = code.toUpperCase()
  if (!/^[A-Z]{2}$/.test(cc)) return '🏳️'
  return String.fromCodePoint(...[...cc].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65))
}
