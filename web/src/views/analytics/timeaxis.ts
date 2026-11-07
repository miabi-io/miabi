// Ticks land on wall-clock boundaries; daily buckets are cut on UTC midnight so they are labelled in UTC.

import type { AnalyticsReport } from '@/api/analytics'

export type Granularity = AnalyticsReport['granularity']

// Every candidate divides the day evenly so ticks land on the same wall-clock marks.
const STEPS: Record<Granularity, number[]> = {
  minute: [1, 2, 5, 10, 15, 30, 60, 120, 180],
  hour: [1, 2, 3, 4, 6, 12, 24],
  day: [1, 2, 5, 7, 14, 30],
}

// Sized for the widest locale label ("11:45 PM").
const TICK_PX = 76

const DAY_MS = 86_400_000

// Counted from the epoch, since in half-hour-offset zones no UTC hourly bucket lands on a local :00.
function aligned(d: Date, gran: Granularity, step: number): boolean {
  if (gran === 'day') return Math.floor(d.getTime() / DAY_MS) % step === 0
  if (gran === 'hour') {
    const hours = Math.round((d.getTime() - d.getTimezoneOffset() * 60_000) / 3_600_000)
    return hours % step === 0
  }
  return (d.getHours() * 60 + d.getMinutes()) % step === 0
}

export interface Tick {
  i: number // index into the series
  text: string
  major: boolean // carries a date rather than a time
}

function dayKey(d: Date): string {
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`
}

export function timeLabel(d: Date): string {
  // `numeric` hours keep 12-hour locales short; 24-hour locales still zero-pad.
  return d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
}

export function dateLabel(d: Date, withYear = false, utc = false): string {
  return d.toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    ...(withYear ? { year: 'numeric' } : {}),
    ...(utc ? { timeZone: 'UTC' } : {}),
  })
}

// The first tick of each day carries the date, so a window spanning midnight says which day each side is.
export function buildTicks(times: Date[], gran: Granularity, width: number): Tick[] {
  if (!times.length) return []
  const maxTicks = Math.max(2, Math.floor(width / TICK_PX))
  const steps = STEPS[gran]

  let step = steps[steps.length - 1]
  for (const s of steps) {
    if (times.filter((d) => aligned(d, gran, s)).length <= maxTicks) {
      step = s
      break
    }
  }

  let idx = times.map((_, i) => i).filter((i) => aligned(times[i], gran, step))
  // A window too short for a boundary still gets its ends labelled.
  if (idx.length < 2 && times.length > 1) idx = [0, times.length - 1]

  const last = times[times.length - 1]
  if (gran === 'day') {
    const withYear = times[0].getUTCFullYear() !== new Date().getUTCFullYear()
    return idx.map((i) => ({
      i,
      text: dateLabel(times[i], withYear, true),
      major: times[i].getUTCDate() === 1,
    }))
  }

  const multiDay = dayKey(times[0]) !== dayKey(last)
  let prevDay = ''
  return idx.map((i) => {
    const d = times[i]
    const newDay = dayKey(d) !== prevDay
    prevDay = dayKey(d)
    const asDate = multiDay && newDay
    return { i, text: asDate ? dateLabel(d) : timeLabel(d), major: asDate }
  })
}

export function bucketLabel(d: Date, gran: Granularity): string {
  const utc = gran === 'day'
  const withYear = (utc ? d.getUTCFullYear() : d.getFullYear()) !== new Date().getFullYear()
  const day = d.toLocaleDateString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    ...(withYear ? { year: 'numeric' } : {}),
    ...(utc ? { timeZone: 'UTC' } : {}),
  })
  if (gran === 'day') return `${day} (UTC)`
  if (gran === 'hour') {
    const end = new Date(d.getTime() + 3_600_000)
    return `${day} · ${timeLabel(d)} – ${timeLabel(end)}`
  }
  return `${day} · ${timeLabel(d)}`
}

export function windowLabel(since: string, until: string): string {
  const a = new Date(since)
  const b = new Date(until)
  if (isNaN(a.getTime()) || isNaN(b.getTime())) return ''
  const sameDay = a.toDateString() === b.toDateString()
  const withYear = a.getFullYear() !== new Date().getFullYear()
  const left = `${dateLabel(a, withYear)}, ${timeLabel(a)}`
  const right = sameDay ? timeLabel(b) : `${dateLabel(b, withYear)}, ${timeLabel(b)}`
  return `${left} → ${right}`
}
