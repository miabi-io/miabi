// TODO: the relative strings are still English; move them into the i18n catalogue.

import { fmtDate } from './datetime'

// Pass a ticking `now` to update live; older than a month falls back to a locale date.
export function relativeTime(iso?: string | null, now: number = Date.now()): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  const diff = Math.round((now - t) / 1000) // seconds in the past (positive)
  const abs = Math.abs(diff)
  const past = diff >= 0
  if (abs < 45) return past ? 'just now' : 'soon'
  if (abs < 90) return past ? '1 min ago' : 'in 1 min'
  const mins = Math.round(abs / 60)
  if (mins < 60) return past ? `${mins} min ago` : `in ${mins} min`
  const hrs = Math.round(abs / 3600)
  if (hrs < 24) return past ? `${hrs}h ago` : `in ${hrs}h`
  const days = Math.round(abs / 86400)
  if (days < 30) return past ? `${days}d ago` : `in ${days}d`
  return fmtDate(iso)
}

// `created` covers a run that failed while queued; `ended` stops a terminal run with no finish time counting up.
export function formatDuration(
  started?: string | null,
  finished?: string | null,
  now: number = Date.now(),
  created?: string | null,
  ended = false,
): string {
  const from = started || created
  if (!from) return '—'
  const start = new Date(from).getTime()
  if (Number.isNaN(start)) return '—'
  if (!finished && ended) return '—'
  const end = finished ? new Date(finished).getTime() : now
  if (end < start) return '—'
  const secs = Math.max(0, Math.round((end - start) / 1000))
  if (secs < 60) return `${secs}s`
  const m = Math.floor(secs / 60)
  const s = secs % 60
  if (m < 60) return `${m}m ${s}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}
