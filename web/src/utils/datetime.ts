// SPDX-FileCopyrightText: 2026 Jonas Kaninda
// SPDX-License-Identifier: AGPL-3.0-or-later

import { activeLanguage } from '@/i18n'

// Locale-aware formatting for code that cannot reach $d/$n: utils, stores, and views not
// yet migrated. A bare toLocaleDateString() takes the browser's language, not the chosen
// one. Migrated views should prefer $d/$n, which read the same formats.

const EM_DASH = '—'

function parse(iso?: string | null): Date | null {
  if (!iso) return null
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? null : d
}

/** A calendar date: "9 Sept 2026". Empty or unparseable input renders an em dash. */
export function fmtDate(iso?: string | null): string {
  const d = parse(iso)
  if (!d) return EM_DASH
  return d.toLocaleDateString(activeLanguage(), { year: 'numeric', month: 'short', day: 'numeric' })
}

/** A date with clock time. */
export function fmtDateTime(iso?: string | null): string {
  const d = parse(iso)
  if (!d) return EM_DASH
  return d.toLocaleString(activeLanguage(), {
    year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
  })
}

/** Clock time alone. */
export function fmtTime(iso?: string | null): string {
  const d = parse(iso)
  if (!d) return EM_DASH
  return d.toLocaleTimeString(activeLanguage(), { hour: '2-digit', minute: '2-digit' })
}

/** A grouped number: "1,024" in English, "1 024" in French. */
export function fmtNumber(n?: number | null, options?: Intl.NumberFormatOptions): string {
  if (n === null || n === undefined || Number.isNaN(n)) return EM_DASH
  return n.toLocaleString(activeLanguage(), options)
}
