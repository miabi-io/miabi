// Shared display helpers for stored TLS certificates.
import type { Certificate } from '@/api/types'

// Re-exported so its callers keep one import while the formatting follows the language.
export { fmtDate } from './datetime'

export function daysLeft(c: Certificate): number {
  return Math.floor((new Date(c.not_after).getTime() - Date.now()) / 86400000)
}

// expiryBadge maps the remaining validity to a badge class + label.
export function expiryBadge(c: Certificate): { cls: string; text: string } {
  const d = daysLeft(c)
  if (d < 0) return { cls: 'badge-danger', text: 'expired' }
  if (d <= 14) return { cls: 'badge-danger', text: `${d}d left` }
  if (d <= 30) return { cls: 'badge-warning', text: `${d}d left` }
  return { cls: 'badge-success', text: `${d}d left` }
}
